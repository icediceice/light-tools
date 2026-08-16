package filetool

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func boundsHandler(t *testing.T) (*Handler, string) {
	t.Helper()
	root := t.TempDir()
	handler, err := New(Options{Roots: []string{root}, SnapshotRoot: filepath.Join(root, ".snap")})
	if err != nil {
		t.Fatal(err)
	}
	return handler, root
}

func readJSON(t *testing.T, handler *Handler, request Request) map[string]any {
	t.Helper()
	request.Verb = "read"
	value, err := handler.read(nil, request)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(encoded, &envelope); err != nil || len(envelope.Content) == 0 {
		t.Fatalf("unexpected result shape: %s", encoded)
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(envelope.Content[0].Text), &result); err != nil {
		t.Fatalf("content was not JSON: %s", envelope.Content[0].Text)
	}
	return result
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

// A caller-supplied limit was previously honoured verbatim: limit:999999
// returned 1,109,187 bytes in one response.
func TestHugeLimitIsClampedAndContinues(t *testing.T) {
	handler, root := boundsHandler(t)
	path := writeLines(t, root, "big.txt", 12000)
	result := readJSON(t, handler, Request{Path: path, Offset: 0, Limit: 999999})

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
	result := readJSON(t, handler, Request{Path: path, Offset: 0, Limit: 5000})
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
	result := readJSON(t, handler, Request{Path: path, Offset: 0, Limit: 10})
	if total, _ := result["total_lines"].(float64); int(total) != 0 {
		t.Fatalf("empty file should report 0 lines, got %v", result["total_lines"])
	}
}

// Paging is only safe if the file's identity is checked between pages.
func TestChangedFileBetweenPagesIsRefused(t *testing.T) {
	handler, root := boundsHandler(t)
	path := writeLines(t, root, "paged.txt", 10)
	first := readJSON(t, handler, Request{Path: path, Offset: 0, Limit: 2})
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
	first := readJSON(t, handler, Request{Path: path, Offset: 0, Limit: 3, ContextEpoch: epoch})
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
	handler, err := New(Options{
		Roots: []string{root}, SnapshotRoot: filepath.Join(root, ".snap"), Spills: spills,
	})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "long.txt")
	huge := strings.Repeat("y", 200*1024)
	if err := os.WriteFile(path, []byte(huge+"\ntail\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	result := readJSON(t, handler, Request{Path: path, Offset: 0, Limit: 5000})
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
	result := readJSON(t, handler, Request{Path: path, Offset: 0, Limit: 5000})
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
