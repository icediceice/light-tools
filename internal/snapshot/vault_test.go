package snapshot

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
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
	var value metadata
	if err := json.Unmarshal(data, &value); err != nil {
		t.Fatal(err)
	}
	value.Path = filepath.Join(root, "other.txt")
	data, err = json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(metadataPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := vault.List(path); err == nil || !strings.Contains(err.Error(), "identity") {
		t.Fatalf("metadata identity mismatch was accepted: %v", err)
	}
}

func TestAcceptanceCaptureSurvivesPerPathRingRotation(t *testing.T) {
	root := t.TempDir()
	vault := New(filepath.Join(root, "snapshots"))
	path := filepath.Join(root, "captured.txt")
	if err := os.WriteFile(path, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	capture, err := vault.CaptureSurface(NewCaptureID(), "rm *.txt", []string{path})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := vault.SealCapture(capture.ID); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"later-1", "later-2", "later-3", "later-4"} {
		if err := vault.Capture(path, []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := vault.RestoreCapture(capture.ID, true); err != nil {
		t.Fatalf("capture handle stopped working after ring rotation: %v", err)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "original" {
		t.Fatalf("capture restored %q, %v; want original", data, err)
	}
}

func TestAcceptanceCaptureRestoresSymlinkIdentity(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation may require privileges on Windows")
	}
	root := t.TempDir()
	vault := New(filepath.Join(root, "snapshots"))
	target := filepath.Join(root, "target.txt")
	link := filepath.Join(root, "link.txt")
	if err := os.WriteFile(target, []byte("target"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	capture, err := vault.CaptureSurface(NewCaptureID(), "rm *.txt", []string{link})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := vault.SealCapture(capture.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := vault.RestoreCapture(capture.ID, false); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("capture restored a regular file instead of the original symlink: mode=%s", info.Mode())
	}
	if got, err := os.Readlink(link); err != nil || got != target {
		t.Fatalf("restored symlink target=%q err=%v; want %q", got, err, target)
	}
}
