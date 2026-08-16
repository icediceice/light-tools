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
	"strings"

	"github.com/icediceice/light-tools/internal/mcp"
	"github.com/icediceice/light-tools/internal/security"
	"github.com/icediceice/light-tools/internal/symbol"
)

const (
	imageLimit = 9 * 1024 * 1024
	readBudget = 128 * 1024
)

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
	return h.readWindow(path, request.Offset, request.Limit, request.ContextEpoch, request.Force)
}

func (h *Handler) readItems(request Request) (any, error) {
	var builder strings.Builder
	for index, item := range request.Items {
		itemRequest := request
		itemRequest.Path, itemRequest.Offset, itemRequest.Limit, itemRequest.Name = item.Path, item.Offset, item.Limit, item.Name
		itemRequest.Items = nil
		var result any
		var err error
		if item.Name != "" {
			result, err = h.symbol(itemRequest)
		} else {
			path, resolveErr := h.resolve(item.Path)
			if resolveErr != nil {
				return nil, resolveErr
			}
			result, err = h.readWindow(path, item.Offset, item.Limit, request.ContextEpoch, request.Force)
		}
		if err != nil {
			return nil, err
		}
		encoded, err := json.Marshal(result)
		if err != nil {
			return nil, err
		}
		section := fmt.Sprintf("=== item %d: %s ===\n%s\n", index+1, item.Path, encoded)
		if builder.Len()+len(section) > readBudget {
			cursor, _ := json.Marshal(map[string]int{"item": index, "offset": item.Offset})
			builder.WriteString("[CONTINUE " + base64.RawURLEncoding.EncodeToString(cursor) + "]\n")
			break
		}
		builder.WriteString(section)
	}
	return mcp.Result{Content: []mcp.Content{mcp.Text(builder.String())}}, nil
}

func (h *Handler) readWindow(path string, offset, limit int, epoch string, force bool) (any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	hash := hashBytes(data)
	if h.cache.ShouldElide(epoch, path, hash, force) {
		return mcp.Result{Content: []mcp.Content{mcp.Text(fmt.Sprintf("[dedup] %s sha256:%s", path, hash))}}, nil
	}
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	if offset < 0 {
		offset = 0
	}
	if offset > len(lines) {
		offset = len(lines)
	}
	if limit <= 0 {
		limit = 120
	}
	end := offset + limit
	if end > len(lines) {
		end = len(lines)
	}
	var builder strings.Builder
	for index := offset; index < end; index++ {
		fmt.Fprintf(&builder, "%6d\t%s\n", index+1, lines[index])
	}
	return textJSON(map[string]any{
		"path": path, "content": builder.String(), "total_lines": len(lines), "bytes": len(data), "sha256": hash,
		"continued": end < len(lines), "next_offset": end,
	})
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
		end := start + 79
		if end > lines {
			end = lines
		}
		chunks = append(chunks, map[string]int{"start_line": start, "end_line": end})
	}
	return textJSON(map[string]any{"path": path, "tree_sitter": false, "note": "symbol extraction unavailable; fixed-size outline", "chunks": chunks})
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
	path, err := h.resolve(request.Path)
	if err != nil {
		return nil, err
	}
	target, err := h.resolve(request.Target)
	if err != nil {
		return nil, err
	}
	before, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	after, err := os.ReadFile(target)
	if err != nil {
		return nil, err
	}
	return textJSON(map[string]any{"diff": simpleDiff(path, target, string(before), string(after))})
}

func (h *Handler) resolve(path string) (string, error) {
	return security.ResolveBeneath(path, h.roots)
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

func simpleDiff(a, b, before, after string) string {
	if before == after {
		return ""
	}
	return fmt.Sprintf("--- %s\n+++ %s\n-%s\n+%s\n", a, b, before, after)
}
