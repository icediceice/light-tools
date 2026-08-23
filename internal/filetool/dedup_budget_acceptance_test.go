package filetool

import (
	"regexp"
	"strings"
	"testing"

	"github.com/icediceice/light-tools/internal/mcp"
)

func TestBatchItemDedupCreditRespectsSharedResponseBudget(t *testing.T) {
	handler, recorder, root := recorderHandler(t)
	path := writeLines(t, root, "large-batch-item.txt", 50000)
	request := Request{
		Items:        []Item{{Path: path, Offset: 0, Limit: 50000}},
		ContextEpoch: "acceptance-budget",
	}

	if _, err := handler.readItems(request); err != nil {
		t.Fatal(err)
	}
	if len(recorder.dedup) != 0 {
		t.Fatalf("first observation recorded savings: %v", recorder.dedup)
	}
	if _, err := handler.readItems(request); err != nil {
		t.Fatal(err)
	}
	if len(recorder.dedup) != 1 {
		t.Fatalf("dedup observations = %v, want exactly one", recorder.dedup)
	}
	if saved := recorder.dedup[0]; saved > readBudget+512 {
		t.Fatalf("dedup credited %d bytes, above the shared %d-byte response budget", saved, readBudget)
	}
}

func TestBatchContinuationSurvivesDedupLedger(t *testing.T) {
	handler, _, root := recorderHandler(t)
	path := writeLines(t, root, "continued-batch-item.txt", 50000)
	request := Request{
		Items:        []Item{{Path: path, Offset: 0, Limit: 50000}},
		ContextEpoch: "acceptance-continuation",
	}

	first, err := handler.readItems(request)
	if err != nil {
		t.Fatal(err)
	}
	result, ok := first.(mcp.Result)
	if !ok || len(result.Content) != 1 {
		t.Fatalf("unexpected first-page result: %#v", first)
	}
	match := regexp.MustCompile(`\[CONTINUE ([A-Za-z0-9_-]+)\]`).FindStringSubmatch(result.Content[0].Text)
	if len(match) != 2 {
		t.Fatal("first page did not return a continuation cursor")
	}

	request.Cursor = match[1]
	second, err := handler.readItems(request)
	if err != nil {
		t.Fatalf("valid continuation failed after the first page entered the dedup ledger: %v", err)
	}
	if strings.Contains(second.(mcp.Result).Content[0].Text, "[dedup]") {
		t.Fatal("continuation was replaced by a dedup stub")
	}
}
