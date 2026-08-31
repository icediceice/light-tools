package readcache

import (
	"testing"
	"time"
)

func TestEpochScopedDedup(t *testing.T) {
	ledger := New(time.Minute, 8)
	if ledger.ShouldElide("", "a", "hash", false) {
		t.Fatal("empty epoch must disable dedup")
	}
	if ledger.ShouldElide("one", "a", "hash", false) {
		t.Fatal("first observation must materialize")
	}
	if !ledger.ShouldElide("one", "a", "hash", false) {
		t.Fatal("same epoch and hash should dedup")
	}
	if ledger.ShouldElide("two", "a", "hash", false) {
		t.Fatal("new epoch must materialize")
	}
	if ledger.ShouldElide("one", "a", "hash", true) {
		t.Fatal("force must materialize")
	}
	ledger.Invalidate("a")
	if ledger.ShouldElide("one", "a", "hash", false) {
		t.Fatal("write invalidation must materialize")
	}
}

// A hit may only credit a delivery the ledger actually saw shipped: the size
// is absent until RecordDelivery lands, re-recorded on a re-materialized
// observation, and invisible across epochs.
func TestDeliveryRecording(t *testing.T) {
	ledger := New(time.Minute, 8)
	if _, ok := ledger.PriorDelivery("e", "a", "hash"); ok {
		t.Fatal("no prior observation must record no delivery")
	}
	if ledger.ShouldElide("e", "a", "hash", false) {
		t.Fatal("first observation must materialize")
	}
	if _, ok := ledger.PriorDelivery("e", "a", "hash"); ok {
		t.Fatal("an unmaterialized miss must credit nothing")
	}
	ledger.RecordDelivery("e", "a", "hash", 4321)
	if !ledger.ShouldElide("e", "a", "hash", false) {
		t.Fatal("recorded observation should dedup")
	}
	prior, ok := ledger.PriorDelivery("e", "a", "hash")
	if !ok || prior != 4321 {
		t.Fatalf("prior delivery = %d,%v; want 4321,true", prior, ok)
	}
	ledger.RecordDelivery("e", "a", "hash", 8765)
	if prior, _ := ledger.PriorDelivery("e", "a", "hash"); prior != 8765 {
		t.Fatalf("a re-materialized observation must re-record, got %d", prior)
	}
	ledger.RecordDelivery("other", "a", "hash", 1)
	if _, ok := ledger.PriorDelivery("e2", "a", "hash"); ok {
		t.Fatal("recordings must stay epoch-scoped")
	}
}
