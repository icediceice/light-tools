package filetool

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"mime"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/icediceice/light-tools/internal/mcp"

	"github.com/icediceice/light-tools/internal/symbol"
)

const (
	imageLimit = 9 * 1024 * 1024
	readBudget = 128 * 1024
	// maxReadLines caps a single-path read. Without it a caller-supplied limit
	// was honoured verbatim: limit:999999 returned over a megabyte in one
	// response, which is the defect this ceiling closes.
	maxReadLines = 5000
	// maxReadBytes refuses a file big enough to exhaust memory before any
	// output bound can apply, because the whole file is read to slice it.
	maxReadBytes = 256 * 1024 * 1024
)

// splitLines splits content into logical lines, treating a terminal newline as
// a DELIMITER rather than an extra empty line. strings.Split leaves a phantom
// trailing element, which overcounted total_lines by one and produced an empty
// final page once next_offset became observable.
func splitLines(data []byte) []string {
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	if text == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(text, "\n"), "\n")
}

type readCursor struct {
	Item int `json:"item"`
	Byte int `json:"byte"`
}

func (h *Handler) read(_ context.Context, request Request) (any, error) {
	if len(request.Items) > 0 {
		return h.readItems(request)
	}
	path, err := h.resolve(request.Path)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		request.Path = path
		return h.list(request)
	}
	if isImage(path) {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		if len(data) <= imageLimit {
			mimeType := mime.TypeByExtension(strings.ToLower(filepath.Ext(path)))
			if mimeType == "" {
				mimeType = "application/octet-stream"
			}
			return mcp.Result{Content: []mcp.Content{mcp.Image(data, mimeType)}}, nil
		}
		return textJSON(map[string]any{"path": path, "bytes": len(data), "description": "image exceeds 9 MiB MCP image limit"})
	}
	if request.Name != "" {
		return h.symbol(request)
	}
	if request.Offset == 0 && request.Limit == 0 {
		return h.outline(request)
	}
	return h.readWindow(path, request.Offset, request.Limit, request.ContextEpoch, request.Force, request.ExpectedSHA)
}

func (h *Handler) readItems(request Request) (any, error) {
	cursor := readCursor{}
	if request.Cursor != "" {
		encoded, err := base64.RawURLEncoding.DecodeString(request.Cursor)
		if err != nil || json.Unmarshal(encoded, &cursor) != nil || cursor.Item < 0 || cursor.Item >= len(request.Items) || cursor.Byte < 0 {
			return nil, fmt.Errorf("invalid continuation cursor")
		}
	}
	var builder strings.Builder
	for index := cursor.Item; index < len(request.Items); index++ {
		item := request.Items[index]
		section, err := h.renderItem(item, request.ContextEpoch, request.Force)
		if err != nil {
			return nil, err
		}
		startByte := 0
		if index == cursor.Item {
			startByte = cursor.Byte
			if startByte > len(section) {
				return nil, fmt.Errorf("continuation cursor exceeds item content")
			}
		}
		section = section[startByte:]
		remaining := readBudget - builder.Len()
		if len(section) > remaining {
			if remaining > 0 {
				builder.WriteString(section[:safeUTF8Boundary(section, remaining)])
			}
			next := readCursor{Item: index, Byte: startByte + safeUTF8Boundary(section, remaining)}
			encoded, _ := json.Marshal(next)
			builder.WriteString("\n[CONTINUE " + base64.RawURLEncoding.EncodeToString(encoded) + "]")
			return mcp.Result{Content: []mcp.Content{mcp.Text(builder.String())}}, nil
		}
		builder.WriteString(section)
		cursor.Byte = 0
	}
	return mcp.Result{Content: []mcp.Content{mcp.Text(builder.String())}}, nil
}

func (h *Handler) renderItem(item Item, epoch string, force bool) (string, error) {
	path, err := h.resolve(item.Path)
	if err != nil {
		return "", err
	}
	header := fmt.Sprintf("=== %s ===\n", path)
	if isImage(path) {
		info, err := os.Stat(path)
		if err != nil {
			return "", err
		}
		return header + fmt.Sprintf("[image description] extension=%s bytes=%d (batch reads do not emit image blocks)\n", filepath.Ext(path), info.Size()), nil
	}
	if item.Name != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		symbols, extractionErr := symbol.Extract(path, data)
		if extractionErr != nil {
			return header + "[symbols unavailable] " + extractionErr.Error() + "\n", nil
		}
		lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
		var builder strings.Builder
		builder.WriteString(header)
		matchCount := 0
		for _, candidate := range symbols {
			if candidate.Name != item.Name {
				continue
			}
			matchCount++
			fmt.Fprintf(&builder, "--- %s %s lines %d-%d", candidate.Kind, candidate.Name, candidate.StartLine, candidate.EndLine)
			if candidate.Parent != "" {
				fmt.Fprintf(&builder, " parent=%s", candidate.Parent)
			}
			builder.WriteString(" ---\n")
			start, end := candidate.StartLine-1, candidate.EndLine
			if start >= 0 && end <= len(lines) && start < end {
				builder.WriteString(strings.Join(lines[start:end], "\n"))
				builder.WriteByte('\n')
			}
		}
		if matchCount == 0 {
			builder.WriteString("[no symbol matches]\n")
		}
		return builder.String(), nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	hash := hashBytes(data)
	// The bounded window is rendered BEFORE the ledger decision so a dedup hit
	// can be credited with exactly the response bytes it suppressed. Deciding
	// first would credit the whole file whenever a narrow window repeated.
	lines := splitLines(data)
	offset, limit := item.Offset, item.Limit
	if offset < 0 {
		offset = 0
	}
	if offset > len(lines) {
		offset = len(lines)
	}
	if limit <= 0 {
		limit = 120
	}
	end := min(len(lines), offset+limit)
	var builder strings.Builder
	builder.WriteString(header)
	for index := offset; index < end; index++ {
		fmt.Fprintf(&builder, "%6d\t%s\n", index+1, lines[index])
	}
	fmt.Fprintf(&builder, "[meta total_lines=%d bytes=%d tokens=%d next_offset=%d continued=%t]\n", len(lines), len(data), estimateTokens(data), end, end < len(lines))
	section := builder.String()
	if h.cache.ShouldElide(epoch, path, hash, force) {
		stub := header + fmt.Sprintf("[dedup] sha256:%s\n", hash)
		if saved := len(section) - len(stub); saved > 0 {
			h.recorder.RecordDedupBytes(saved)
		}
		return stub, nil
	}
	return section, nil
}

func (h *Handler) readWindow(path string, offset, limit int, epoch string, force bool, expectedSHA string) (any, error) {
	// Stat BEFORE reading: readWindow must hold the file to slice it, so
	// without this a "bounded" read can exhaust memory before any output limit
	// applies. Streaming would remove the exposure entirely (see todo).
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.Size() > maxReadBytes {
		return nil, fmt.Errorf("file is %d bytes, above the %d byte single-read ceiling; use items with offset and limit to page it", info.Size(), maxReadBytes)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	hash := hashBytes(data)
	// Continuation identity: a caller paging through a file echoes the sha of
	// the page it came from. Without this, a file edited between pages silently
	// duplicates or drops lines instead of failing.
	if expectedSHA != "" && expectedSHA != hash {
		return nil, fmt.Errorf("file changed between pages (expected_sha %s, now %s) — re-read from offset 0", expectedSHA, hash)
	}
	lines := splitLines(data)
	if offset < 0 {
		offset = 0
	}
	if offset > len(lines) {
		offset = len(lines)
	}
	if limit <= 0 {
		limit = 120
	}
	if limit > maxReadLines {
		limit = maxReadLines
	}
	end := min(len(lines), offset+limit)

	// The first line is always emitted even if it alone exceeds the budget, so
	// a page ALWAYS makes progress. Never advancing would loop the caller
	// forever; advancing without emitting would lose bytes silently.
	var builder strings.Builder
	for index := offset; index < end; index++ {
		line := fmt.Sprintf("%6d\t%s\n", index+1, lines[index])
		if builder.Len() > 0 && builder.Len()+len(line) > readBudget {
			end = index
			break
		}
		builder.WriteString(line)
		if builder.Len() >= readBudget {
			end = index + 1
			break
		}
	}
	content := builder.String()

	// The dedup ledger keys on the whole-file hash, so paging an unchanged file
	// would elide page 2 as an already-seen read. Fold the span into the key.
	if h.cache.ShouldElide(epoch, path, fmt.Sprintf("%s#%d-%d", hash, offset, end), force) {
		return mcp.Result{Content: []mcp.Content{mcp.Text(fmt.Sprintf("[dedup] %s sha256:%s lines %d-%d", path, hash, offset, end))}}, nil
	}

	result := map[string]any{
		"path": path, "content": content, "total_lines": len(lines), "bytes": len(data),
		"tokens": estimateTokens(data), "sha256": hash, "continued": end < len(lines), "next_offset": end,
	}
	// An oversized single line is handed to the SAME spill store light_bash
	// uses, rather than a bespoke error: the caller recovers it verbatim with
	// output_mode:read_block.
	if len(content) > readBudget {
		if h.spills != nil {
			id, spillErr := h.spills.Store([]byte(content))
			if spillErr == nil {
				result["spill_id"] = id
				result["truncated"] = true
				result["content"] = content[:safeUTF8Boundary(content, readBudget)]
				result["note"] = "line " + strconv.Itoa(offset+1) + " exceeds the read budget; full page stored — recover it with light_bash output_mode:read_block spill:" + id
				return textJSON(result)
			}
		}
		result["truncated"] = true
		result["content"] = content[:safeUTF8Boundary(content, readBudget)]
		result["note"] = "line " + strconv.Itoa(offset+1) + " exceeds the read budget and was truncated"
	}
	return textJSON(result)
}

func (h *Handler) list(request Request) (any, error) {
	path, err := h.resolve(request.Path)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	type listed struct {
		Name string `json:"name"`
		Kind string `json:"kind"`
		Size int64  `json:"size,omitempty"`
	}
	result := make([]listed, 0, len(entries))
	for _, entry := range entries {
		if err := h.confiner.Permit(filepath.Join(path, entry.Name())); err != nil {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		kind := "file"
		if entry.IsDir() {
			kind = "directory"
		} else if info.Mode()&os.ModeSymlink != 0 {
			kind = "symlink"
		}
		result = append(result, listed{Name: entry.Name(), Kind: kind, Size: info.Size()})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return textJSON(map[string]any{"path": path, "entries": result})
}

func (h *Handler) outline(request Request) (any, error) {
	path, err := h.resolve(request.Path)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	symbols, extractionErr := symbol.Extract(path, data)
	if extractionErr == nil && len(symbols) > 0 {
		return textJSON(map[string]any{"path": path, "symbols": symbols})
	}
	lines := strings.Count(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n") + 1
	chunks := make([]map[string]int, 0, (lines+79)/80)
	for start := 1; start <= lines; start += 80 {
		end := min(lines, start+79)
		chunks = append(chunks, map[string]int{"start_line": start, "end_line": end})
	}
	note := "symbol extraction unavailable; fixed-size outline"
	if extractionErr != nil {
		note = "symbol extraction unavailable: " + extractionErr.Error() + "; fixed-size outline"
	}
	return textJSON(map[string]any{"path": path, "tree_sitter": false, "note": note, "chunks": chunks})
}

func (h *Handler) symbol(request Request) (any, error) {
	path, err := h.resolve(request.Path)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	symbols, err := symbol.Extract(path, data)
	if err != nil {
		return textJSON(map[string]any{"path": path, "tree_sitter": false, "matches": []any{}, "note": err.Error()})
	}
	var matches []map[string]any
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	for _, candidate := range symbols {
		if candidate.Name != request.Name {
			continue
		}
		start, end := candidate.StartLine-1, candidate.EndLine
		if start < 0 || end > len(lines) || start >= end {
			continue
		}
		matches = append(matches, map[string]any{"symbol": candidate, "content": strings.Join(lines[start:end], "\n")})
	}
	return textJSON(map[string]any{"path": path, "matches": matches})
}

func (h *Handler) identity(request Request) (any, error) {
	path, err := h.resolve(request.Path)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	result := map[string]any{"path": path, "size": info.Size(), "mode": info.Mode().String(), "modified": info.ModTime().UTC()}
	if info.Mode().IsRegular() {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		result["sha256"] = hashBytes(data)
	}
	return textJSON(result)
}

func (h *Handler) diff(request Request) (any, error) {
	left := request.Path
	if left == "" {
		left = request.A
	}
	path, err := h.resolve(left)
	if err != nil {
		return nil, err
	}
	before, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	contextLines := request.DiffContext
	if contextLines <= 0 {
		contextLines = 3
	}

	if request.Patch != "" || request.PatchPath != "" {
		patch := request.Patch
		if request.PatchPath != "" {
			patchPath, err := h.resolve(request.PatchPath)
			if err != nil {
				return nil, err
			}
			data, err := os.ReadFile(patchPath)
			if err != nil {
				return nil, err
			}
			patch = string(data)
		}
		after, applied, err := applyUnifiedPatch(string(before), patch, request.Fuzz)
		if err != nil {
			return nil, err
		}
		return textJSON(map[string]any{"path": path, "applied_hunks": applied, "diff": unifiedDiff(path, path, string(before), after, contextLines)})
	}

	right := request.Target
	if right == "" {
		right = request.B
	}
	if right == "" {
		return nil, fmt.Errorf("diff requires target/b or patch/patch_path")
	}
	target, err := h.resolve(right)
	if err != nil {
		return nil, err
	}
	after, err := os.ReadFile(target)
	if err != nil {
		return nil, err
	}
	return textJSON(map[string]any{"diff": unifiedDiff(path, target, string(before), string(after), contextLines)})
}

func (h *Handler) resolve(path string) (string, error) {
	return h.confiner.Resolve(path)
}

func isImage(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".png", ".jpg", ".jpeg", ".webp", ".gif":
		return true
	}
	return false
}

func hashBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func estimateTokens(data []byte) int {
	runes := utf8.RuneCount(data)
	return (runes + 3) / 4
}

func safeUTF8Boundary(value string, maximum int) int {
	if maximum <= 0 {
		return 0
	}
	if maximum >= len(value) {
		return len(value)
	}
	for maximum > 0 && !utf8.RuneStart(value[maximum]) {
		maximum--
	}
	return maximum
}
