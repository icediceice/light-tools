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
