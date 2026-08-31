package filetool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	fileop "github.com/icediceice/light-tools/internal/file"
	"github.com/icediceice/light-tools/internal/mcp"
	"github.com/icediceice/light-tools/internal/security"
)

func boundsHandler(t *testing.T) (*Handler, string) {
	t.Helper()
	root := t.TempDir()
	confiner, err := security.NewConfiner([]string{root}, nil)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := New(Options{Confiner: confiner, SnapshotRoot: filepath.Join(root, ".snap")})
	if err != nil {
		t.Fatal(err)
	}
	return handler, root
}

// resultText extracts the single text block of a read result.
func resultText(t *testing.T, value any) string {
	t.Helper()
	result, ok := value.(mcp.Result)
	if !ok || len(result.Content) != 1 {
		t.Fatalf("unexpected result %#v", value)
	}
	return result.Content[0].Text
}

// readResult drives one read and decodes EITHER delivered shape into the
// same map the JSON envelope would have produced: the canonical form begins
// '{' and the plain render begins '=', so the first byte discriminates.
func readResult(t *testing.T, handler *Handler, request Request) map[string]any {
	t.Helper()
	request.Verb = "read"
	value, err := handler.read(nil, request)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return decodeReadText(t, resultText(t, value))
}

// decodeReadText discriminates on the first byte and yields the same typed
// value from either shape: numbers as float64 and booleans as bool, exactly
// as encoding/json decodes them, so a test cannot tell which shape it got.
func decodeReadText(t *testing.T, text string) map[string]any {
	t.Helper()
	switch {
	case strings.HasPrefix(text, "{"):
		var result map[string]any
		if err := json.Unmarshal([]byte(text), &result); err != nil {
			t.Fatalf("envelope JSON: %v: %s", err, truncate(text))
		}
		return result
	case strings.HasPrefix(text, "=== "):
		return decodeWindowPlain(t, text)
	default:
		t.Fatalf("read is neither JSON nor a plain render: %s", truncate(text))
		return nil
	}
}

// decodeWindowPlain recovers the canonical window map from the plain render:
// header line, verbatim content, and the final bracketed meta line. Content
// lines are always number-prefixed, so no content byte can forge the meta
// line, and the renderer pads a mid-line-truncated page with exactly one
// newline, which decoding strips — the content is byte-exact either way.
func decodeWindowPlain(t *testing.T, text string) map[string]any {
	t.Helper()
	headerEnd := strings.Index(text, "\n")
	if headerEnd < 0 {
		t.Fatalf("plain read has no header line: %s", truncate(text))
	}
	metaStart := strings.LastIndex(text, "\n[meta ")
	if metaStart < headerEnd {
		t.Fatalf("plain read has no meta line: %s", truncate(text))
	}
	meta := strings.TrimSuffix(text[metaStart+1:], "\n")
	if !strings.HasSuffix(meta, "]") {
		t.Fatalf("malformed meta line: %q", meta)
	}
	content := ""
	if metaStart > headerEnd {
		content = strings.TrimSuffix(text[headerEnd+1:metaStart], "\n")
	}
	result := map[string]any{
		"path":    strings.TrimSuffix(strings.TrimPrefix(text[:headerEnd], "=== "), " ==="),
		"content": content,
	}
	fields := strings.TrimSuffix(strings.TrimPrefix(meta, "[meta "), "]")
	for fields != "" {
		if note, ok := strings.CutPrefix(fields, "note="); ok {
			result["note"] = note // free text: the note consumes the rest of the line
			break
		}
		var field string
		if index := strings.Index(fields, " "); index >= 0 {
			field, fields = fields[:index], fields[index+1:]
		} else {
			field, fields = fields, ""
		}
		name, value, found := strings.Cut(field, "=")
		if !found {
			t.Fatalf("malformed meta field %q", field)
		}
		switch name {
		case "total_lines", "bytes", "tokens", "next_offset":
			result[name] = parseMetaNumber(t, value)
		case "continued", "truncated":
			result[name] = value == "true"
		case "sha256", "spill_id":
			result[name] = value
		default:
			t.Fatalf("unknown meta field %q", name)
		}
	}
	return result
}

// decodeSymbolResult decodes either shape of a named-symbol read.
func decodeSymbolResult(t *testing.T, text string) map[string]any {
	t.Helper()
	switch {
	case strings.HasPrefix(text, "{"):
		var result map[string]any
		if err := json.Unmarshal([]byte(text), &result); err != nil {
			t.Fatalf("symbol JSON: %v: %s", err, truncate(text))
		}
		return result
	case strings.HasPrefix(text, "=== "):
		return decodeSymbolPlain(t, text)
	default:
		t.Fatalf("symbol read is neither JSON nor a plain render: %s", truncate(text))
		return nil
	}
}

// decodeSymbolPlain walks renderSymbolText's format arithmetically: quoted
// string fields and a "content N bytes" body consumed by exact length, so
// no byte inside a body can forge the next section.
func decodeSymbolPlain(t *testing.T, text string) map[string]any {
	t.Helper()
	headerEnd := strings.Index(text, "\n")
	if headerEnd < 0 {
		t.Fatalf("plain symbol read has no header line: %s", truncate(text))
	}
	result := map[string]any{
		"path": strings.TrimSuffix(strings.TrimPrefix(text[:headerEnd], "=== "), " ==="),
	}
	body := text[headerEnd+1:]
	if note, ok := strings.CutPrefix(body, "[symbols unavailable] "); ok {
		result["tree_sitter"] = false
		result["matches"] = []any{}
		result["note"] = strings.TrimSuffix(note, "\n")
		return result
	}
	if strings.HasPrefix(body, "[no symbol matches]") {
		result["matches"] = []any{}
		return result
	}
	matches := []any{}
	for body != "" {
		line, rest, found := strings.Cut(body, "\n")
		if !found || !strings.HasPrefix(line, "--- ") {
			t.Fatalf("malformed symbol section: %q", line)
		}
		extracted := map[string]any{}
		match := map[string]any{"symbol": extracted}
		// --- <kind> <name> lines <start>-<end> bytes <start>-<end>
		fields := strings.Fields(strings.TrimPrefix(line, "--- "))
		if len(fields) != 7 || fields[2] != "lines" || fields[5] != "bytes" {
			t.Fatalf("malformed symbol section header: %q", line)
		}
		extracted["kind"] = fields[0]
		extracted["name"] = fields[1]
		start, end, ok := strings.Cut(fields[3], "-")
		if !ok {
			t.Fatalf("malformed line range in %q", line)
		}
		extracted["start_line"] = parseMetaNumber(t, start)
		extracted["end_line"] = parseMetaNumber(t, end)
		start, end, ok = strings.Cut(fields[6], "-")
		if !ok {
			t.Fatalf("malformed byte range in %q", line)
		}
		extracted["start_byte"] = parseMetaNumber(t, start)
		extracted["end_byte"] = parseMetaNumber(t, end)
		for {
			field, fieldRest, found := strings.Cut(rest, "\n")
			if !found {
				t.Fatalf("unterminated symbol section: %s", truncate(rest))
			}
			if value, ok := quotedField(field, "signature "); ok {
				extracted["signature"] = value
				rest = fieldRest
				continue
			}
			if value, ok := quotedField(field, "comment "); ok {
				extracted["comment"] = value
				rest = fieldRest
				continue
			}
			if value, ok := quotedField(field, "parent "); ok {
				extracted["parent"] = value
				rest = fieldRest
				continue
			}
			var length int
			if _, err := fmt.Sscanf(field, "content %d bytes", &length); err != nil {
				t.Fatalf("malformed symbol field line: %q", field)
			}
			if len(rest) < length {
				t.Fatalf("symbol content declares %d bytes but only %d remain", length, len(rest))
			}
			match["content"] = rest[:length]
			rest = rest[length:]
			if !strings.HasPrefix(rest, "\n") {
				t.Fatalf("symbol content body is not newline-terminated")
			}
			rest = rest[1:]
			break
		}
		matches = append(matches, match)
		body = rest
	}
	result["matches"] = matches
	return result
}

// quotedField unquotes a prefix-quoted field line such as
// `signature "func X()"`.
func quotedField(field, prefix string) (string, bool) {
	if !strings.HasPrefix(field, prefix) {
		return "", false
	}
	value, err := strconv.Unquote(strings.TrimPrefix(field, prefix))
	return value, err == nil
}

func parseMetaNumber(t *testing.T, value string) float64 {
	t.Helper()
	number, err := strconv.ParseFloat(value, 64)
	if err != nil {
		t.Fatalf("malformed number %q", value)
	}
	return number
}

func writeLines(t *testing.T, root, name string, count int) string {
	t.Helper()
	path := filepath.Join(root, name)
	var builder strings.Builder
	for index := 0; index < count; index++ {
		fmt.Fprintf(&builder, "line %d\n", index+1)
	}
	if err := os.WriteFile(path, []byte(builder.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// captureRecorder records savings for assertions.
type captureRecorder struct {
	calls  []string
	dedup  []int
	writes []int
}

func (c *captureRecorder) RecordCall(tool string)      { c.calls = append(c.calls, tool) }
func (c *captureRecorder) RecordTerseTokens(int)       {}
func (c *captureRecorder) RecordDedupBytes(saved int)  { c.dedup = append(c.dedup, saved) }
func (c *captureRecorder) RecordWriteBytes(saved int)  { c.writes = append(c.writes, saved) }
func (c *captureRecorder) RecordCompactBytes(int, int) {}

func recorderHandler(t *testing.T) (*Handler, *captureRecorder, string) {
	t.Helper()
	root := t.TempDir()
	confiner, err := security.NewConfiner([]string{root}, nil)
	if err != nil {
		t.Fatal(err)
	}
	recorder := &captureRecorder{}
	handler, err := New(Options{
		Confiner: confiner, SnapshotRoot: filepath.Join(root, ".snap"), Recorder: recorder,
	})
	if err != nil {
		t.Fatal(err)
	}
	return handler, recorder, root
}

// A repeated bounded read records ONLY the bounded delta: the response it
// suppressed, never the size of the whole source file. The credit is EXACT —
// the selected delivery's bytes minus the stub — and force:true reproduces
// that delivery byte-for-byte, so the test can hold the credit to the penny.
func TestDedupRecordsOnlyTheBoundedDelta(t *testing.T) {
	handler, recorder, root := recorderHandler(t)
	path := writeLines(t, root, "wide.txt", 5000)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := handler.readWindow(path, 0, 10, "epoch-1", false, ""); err != nil {
		t.Fatal(err)
	}
	if len(recorder.dedup) != 0 {
		t.Fatalf("first observation recorded savings: %v", recorder.dedup)
	}
	// The identical window repeats: elided, and the credit is bounded by the
	// response that would have been returned.
	stub, err := handler.readWindow(path, 0, 10, "epoch-1", false, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(recorder.dedup) != 1 {
		t.Fatalf("elision did not record exactly one delta: %v", recorder.dedup)
	}
	saved := recorder.dedup[0]
	if saved <= 0 {
		t.Fatalf("delta = %d", saved)
	}
	if saved >= int(info.Size()) {
		t.Fatalf("delta %d credits the whole %d-byte file rather than the bounded response", saved, info.Size())
	}
	// force:true returns exactly the delivery the hit suppressed, so the
	// credit must equal that delivery minus the stub — not an estimate, and
	// not the JSON envelope the plain render may have beaten.
	forced, err := handler.readWindow(path, 0, 10, "epoch-1", true, "")
	if err != nil {
		t.Fatal(err)
	}
	want := len(resultText(t, forced)) - len(resultText(t, stub))
	if saved != want {
		t.Fatalf("dedup credit %d, want exactly %d (forced delivery %d - stub %d)",
			saved, want, len(resultText(t, forced)), len(resultText(t, stub)))
	}
}

// A repeated batch item elides at the readItems layer, and the credit is
// bounded by the shared batch budget: a huge repeated item is credited with at
// most the bytes the bounded response could have carried, never the raw
// section size.
func TestBatchItemDedupRecordsBoundedDelta(t *testing.T) {
	handler, recorder, root := recorderHandler(t)
	path := writeLines(t, root, "item.txt", 50000)
	request := Request{Items: []Item{{Path: path, Offset: 0, Limit: 50000}}, ContextEpoch: "epoch-2"}
	first, err := handler.readItems(request)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(fmt.Sprint(first), "[dedup]") {
		t.Fatal("first observation elided")
	}
	if len(recorder.dedup) != 0 {
		t.Fatalf("first observation recorded savings: %v", recorder.dedup)
	}
	second, err := handler.readItems(request)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(fmt.Sprint(second), "[dedup]") {
		t.Fatal("second observation did not elide")
	}
	if len(recorder.dedup) != 1 || recorder.dedup[0] <= 0 {
		t.Fatalf("elision delta = %v", recorder.dedup)
	}
	if saved := recorder.dedup[0]; saved > readBudget {
		t.Fatalf("delta %d exceeds the %d-byte batch budget the response could carry", saved, readBudget)
	}
}

// Write savings are measured once per commit against the postimage: a tiny
// edit to a large file credits (approximately) the file, and a write that
// carries its whole content credits nothing.
func TestWriteSavingsMeasuredOncePerCommit(t *testing.T) {
	handler, recorder, root := recorderHandler(t)
	path := writeLines(t, root, "target.txt", 3000)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := handler.mutate(context.Background(), Request{Verb: "sed", Path: path, Find: strPtr("line 1500"), Replace: strPtr("brand new line")}.mutation()); err != nil {
		t.Fatal(err)
	}
	if len(recorder.writes) != 1 {
		t.Fatalf("sed commit recorded %d entries: %v", len(recorder.writes), recorder.writes)
	}
	if recorder.writes[0] < int(info.Size())-1024 {
		t.Fatalf("sed credit %d does not track the %d-byte postimage", recorder.writes[0], info.Size())
	}

	// A write carries its full content: no credit.
	recorder.writes = nil
	fresh := filepath.Join(root, "fresh.txt")
	if _, err := handler.mutate(context.Background(), Request{Verb: "write", Path: fresh, Content: strPtr(strings.Repeat("payload ", 64))}.mutation()); err != nil {
		t.Fatal(err)
	}
	if len(recorder.writes) != 0 {
		t.Fatalf("full write credited savings: %v", recorder.writes)
	}

	// Two same-path edits in ONE batch commit exactly once.
	recorder.writes = nil
	if _, err := handler.mutateBatch(context.Background(), []fileop.Mutation{
		{Verb: fileop.VerbEdit, Path: path, StartLine: 10, NewString: strPtr("first")},
		{Verb: fileop.VerbEdit, Path: path, StartLine: 20, NewString: strPtr("second")},
	}); err != nil {
		t.Fatal(err)
	}
	if len(recorder.writes) != 1 {
		t.Fatalf("grouped edits recorded %d entries (want one per commit): %v", len(recorder.writes), recorder.writes)
	}
}

func strPtr(value string) *string { return &value }

// A caller-supplied limit was previously honoured verbatim: limit:999999
// returned 1,109,187 bytes in one response.
func TestHugeLimitIsClampedAndContinues(t *testing.T) {
	handler, root := boundsHandler(t)
	path := writeLines(t, root, "big.txt", 12000)
	result := readResult(t, handler, Request{Path: path, Offset: 0, Limit: 999999})

	if total, _ := result["total_lines"].(float64); int(total) != 12000 {
		t.Fatalf("total_lines should be 12000, got %v", result["total_lines"])
	}
	next, _ := result["next_offset"].(float64)
	if int(next) > maxReadLines {
		t.Fatalf("a page must not exceed %d lines, got next_offset %v", maxReadLines, next)
	}
	if continued, _ := result["continued"].(bool); !continued {
		t.Fatal("continued should be true when lines remain")
	}
	content, _ := result["content"].(string)
	if len(content) > readBudget+1024 {
		t.Fatalf("content exceeded the read budget: %d bytes", len(content))
	}
	if int(next) == 0 {
		t.Fatal("next_offset must advance, otherwise the caller loops forever")
	}
}

// The phantom trailing element made total_lines overcount by one.
func TestTerminalNewlineIsADelimiterNotALine(t *testing.T) {
	handler, root := boundsHandler(t)
	path := writeLines(t, root, "four-hundred.txt", 400)
	result := readResult(t, handler, Request{Path: path, Offset: 0, Limit: 5000})
	if total, _ := result["total_lines"].(float64); int(total) != 400 {
		t.Fatalf("a 400-line file must report 400, got %v", result["total_lines"])
	}
	if continued, _ := result["continued"].(bool); continued {
		t.Fatal("the final page must not report continued")
	}
	if next, _ := result["next_offset"].(float64); int(next) != 400 {
		t.Fatalf("next_offset should equal total_lines on the final page, got %v", next)
	}
}

func TestEmptyFileReportsZeroLines(t *testing.T) {
	handler, root := boundsHandler(t)
	path := filepath.Join(root, "empty.txt")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	result := readResult(t, handler, Request{Path: path, Offset: 0, Limit: 10})
	if total, _ := result["total_lines"].(float64); int(total) != 0 {
		t.Fatalf("empty file should report 0 lines, got %v", result["total_lines"])
	}
}

// Paging is only safe if the file's identity is checked between pages.
func TestChangedFileBetweenPagesIsRefused(t *testing.T) {
	handler, root := boundsHandler(t)
	path := writeLines(t, root, "paged.txt", 10)
	first := readResult(t, handler, Request{Path: path, Offset: 0, Limit: 2})
	sha, _ := first["sha256"].(string)
	if sha == "" {
		t.Fatal("sha256 missing from the page")
	}
	if err := os.WriteFile(path, []byte("rewritten\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := handler.read(nil, Request{Verb: "read", Path: path, Offset: 2, Limit: 2, ExpectedSHA: sha}); err == nil {
		t.Fatal("a page from a changed file must be refused, not silently misaligned")
	}
}

// The dedup ledger keys on the whole-file hash. Before the span was folded into
// the key, page 2 of an unchanged file came back as a [dedup] stub, making the
// continuation contract unusable.
func TestContinuationPageSurvivesTheDedupCache(t *testing.T) {
	handler, root := boundsHandler(t)
	path := writeLines(t, root, "epoch.txt", 10)
	const epoch = "session-1"
	first := readResult(t, handler, Request{Path: path, Offset: 0, Limit: 3, ContextEpoch: epoch})
	if content, _ := first["content"].(string); !strings.Contains(content, "line 1") {
		t.Fatalf("page 1 missing content: %v", first)
	}
	second := readJSON(t, handler, Request{Path: path, Offset: 3, Limit: 3, ContextEpoch: epoch})
	content, _ := second["content"].(string)
	if !strings.Contains(content, "line 4") {
		t.Fatalf("page 2 was elided by the dedup cache instead of returning content: %v", second)
	}
}

// fakeSpills records what filetool hands to the shared spill store.
type fakeSpills struct{ stored [][]byte }

func (f *fakeSpills) Store(data []byte) (string, error) {
	f.stored = append(f.stored, data)
	return fmt.Sprintf("spill-%d", len(f.stored)), nil
}

// An oversized line's FULL page must reach the shared spill store, so the
// caller can recover it verbatim through light_bash read_block.
func TestOversizedLineIsHandedToTheSpillStore(t *testing.T) {
	root := t.TempDir()
	spills := &fakeSpills{}
	confiner, err := security.NewConfiner([]string{root}, nil)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := New(Options{
		Confiner: confiner, SnapshotRoot: filepath.Join(root, ".snap"), Spills: spills,
	})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "long.txt")
	huge := strings.Repeat("y", 200*1024)
	if err := os.WriteFile(path, []byte(huge+"\ntail\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	result := readResult(t, handler, Request{Path: path, Offset: 0, Limit: 5000})
	id, _ := result["spill_id"].(string)
	if id == "" {
		t.Fatalf("oversized page was not spilled: %v", result)
	}
	if len(spills.stored) != 1 {
		t.Fatalf("expected exactly one spill, got %d", len(spills.stored))
	}
	// The spill must hold the WHOLE page, not the truncated response content.
	if !strings.Contains(string(spills.stored[0]), huge) {
		t.Fatal("the spilled page did not contain the full oversized line")
	}
	if content, _ := result["content"].(string); len(content) > readBudget+1024 {
		t.Fatalf("inline content still unbounded: %d bytes", len(content))
	}
}

// A file above the ceiling must be refused BEFORE it is read into memory.
func TestOversizedFileIsRefusedBeforeReading(t *testing.T) {
	handler, root := boundsHandler(t)
	path := filepath.Join(root, "huge.bin")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	// Sparse: allocates no real blocks, so this stays fast and cheap.
	if err := file.Truncate(maxReadBytes + 1); err != nil {
		file.Close()
		t.Skipf("sparse file unsupported here: %v", err)
	}
	file.Close()

	_, err = handler.read(nil, Request{Verb: "read", Path: path, Offset: 0, Limit: 10})
	if err == nil {
		t.Fatal("a file above the ceiling must be refused, not read whole")
	}
	if !strings.Contains(err.Error(), "single-read ceiling") {
		t.Fatalf("refusal should name the ceiling, got %v", err)
	}
}

// A single line larger than the budget must still make progress rather than
// looping or silently dropping bytes.
func TestOversizedSingleLineStillMakesProgress(t *testing.T) {
	handler, root := boundsHandler(t)
	path := filepath.Join(root, "long.txt")
	huge := strings.Repeat("x", 200*1024)
	if err := os.WriteFile(path, []byte(huge+"\nsecond\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	result := readResult(t, handler, Request{Path: path, Offset: 0, Limit: 5000})
	next, _ := result["next_offset"].(float64)
	if int(next) < 1 {
		t.Fatalf("next_offset must advance past the oversized line, got %v", next)
	}
	if truncated, _ := result["truncated"].(bool); !truncated {
		t.Fatalf("an oversized line must be reported as truncated, got %v", result)
	}
	if content, _ := result["content"].(string); len(content) > readBudget+1024 {
		t.Fatalf("oversized line was returned unbounded: %d bytes", len(content))
	}
}
