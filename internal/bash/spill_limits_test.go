package bash

import (
	"strings"
	"testing"
	"time"
)

func TestSpillCountAndByteLimits(t *testing.T) {
	store, err := NewSpillStore(t.TempDir(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	store.maximum = 1
	store.maxBytes = 4

	if _, err := store.Store([]byte("12345")); err == nil || !strings.Contains(err.Error(), "byte limit") {
		t.Fatalf("oversized spill accepted: %v", err)
	}
	if _, err := store.Store([]byte("1234")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Store([]byte("next")); err == nil || !strings.Contains(err.Error(), "spill limit") {
		t.Fatalf("spill count limit not enforced: %v", err)
	}
}
