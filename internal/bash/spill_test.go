package bash

import (
	"strings"
	"testing"
	"time"
)

func TestOpaqueSpillRecovery(t *testing.T) {
	store, err := NewSpillStore(t.TempDir(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	id, err := store.Store([]byte("one\ntwo\nthree\n"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(id, "/") || strings.Contains(id, ".") {
		t.Fatalf("spill id leaks path shape: %q", id)
	}
	value, err := store.Read(id, "2-3")
	if err != nil {
		t.Fatal(err)
	}
	if value != "two\nthree" {
		t.Fatalf("unexpected range: %q", value)
	}
	if _, err := store.Read("../escape", ""); err == nil {
		t.Fatal("caller-supplied path should not resolve")
	}
}
