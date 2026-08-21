package snapshot

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRingRetentionIntegrityAndBoundedReap(t *testing.T) {
	root := t.TempDir()
	vault := New(filepath.Join(root, "snapshots"))
	path := filepath.Join(root, "work", "file.txt")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	for index, value := range []string{"zero", "one", "two", "three"} {
		if err := vault.Capture(path, []byte(value), 0o640); err != nil {
			t.Fatal(err)
		}
		if index < 3 {
			time.Sleep(time.Millisecond)
		}
	}
	entries, err := vault.List(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 || entries[0].Version != 1 || entries[2].Version != 3 {
		t.Fatalf("bad ring metadata: %#v", entries)
	}
	latest, mode, err := vault.Load(path, 1)
	if err != nil {
		t.Fatal(err)
	}
	if string(latest) != "three" || mode.Perm() != 0o640 {
		t.Fatalf("latest = %q mode=%o", latest, mode.Perm())
	}

	directory := vault.pathDirectory(path)
	if err := os.WriteFile(filepath.Join(directory, entries[0].File), []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := vault.Load(path, 1); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("tampered snapshot was accepted: %v", err)
	}

	outside := filepath.Join(root, "outside.txt")
	if err := os.WriteFile(outside, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := vault.Reap(time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("reap escaped snapshot root: %v", err)
	}
}

func TestMetadataPathIdentityMismatchRejected(t *testing.T) {
	root := t.TempDir()
	vault := New(filepath.Join(root, "snapshots"))
	path := filepath.Join(root, "a.txt")
	if err := vault.Capture(path, []byte("a"), 0o600); err != nil {
		t.Fatal(err)
	}
	metadataPath := filepath.Join(vault.pathDirectory(path), "metadata.json")
	data, err := os.ReadFile(metadataPath)
	if err != nil {
		t.Fatal(err)
	}
	data = []byte(strings.Replace(string(data), filepath.Clean(path), filepath.Join(root, "other.txt"), 1))
	if err := os.WriteFile(metadataPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := vault.List(path); err == nil || !strings.Contains(err.Error(), "identity") {
		t.Fatalf("metadata identity mismatch was accepted: %v", err)
	}
}
