package filetool

import "testing"

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
