package filetool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	fileop "github.com/icediceice/light-tools/internal/file"
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
// suppressed, never the size of the whole source file.
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
	encoded, err := json.Marshal(stub)
	if err != nil {
		t.Fatal(err)
	}
	if saved >= int(info.Size()) {
		t.Fatalf("delta %d credits the whole %d-byte file rather than the bounded response", saved, info.Size())
	}
	if saved+200 < len(encoded) { // stub + modest response overhead must cover the credit
		t.Fatalf("delta %d is implausibly larger than a stubbed response", saved)
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
