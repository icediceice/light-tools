package file

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func testHash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func TestCommitCASConflictAfterConcurrentWriter(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "value.txt")
	if err := os.WriteFile(path, []byte("first"), 0o640); err != nil {
		t.Fatal(err)
	}
	readSHA := testHash([]byte("first"))
	if err := os.WriteFile(path, []byte("racer"), 0o640); err != nil {
		t.Fatal(err)
	}
	_, err := Commit(context.Background(), CommitRequest{Path: path, Data: []byte("ours"), ExpectedSHA: readSHA, AllowedRoots: []string{root}})
	if err == nil || !strings.Contains(err.Error(), "CAS conflict") {
		t.Fatalf("expected CAS conflict, got %v", err)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "racer" {
		t.Fatalf("conflict overwrote racer: %q", data)
	}
}

func TestCommitPreservesModeAndRejectsSymlinkTarget(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink permissions vary on Windows")
	}
	root := t.TempDir()
	path := filepath.Join(root, "value.txt")
	if err := os.WriteFile(path, []byte("first"), 0o640); err != nil {
		t.Fatal(err)
	}
	result, err := Commit(context.Background(), CommitRequest{Path: path, Data: []byte("second"), AllowedRoots: []string{root}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Unchanged {
		t.Fatal("unexpected unchanged result")
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("mode changed to %o", info.Mode().Perm())
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := Commit(context.Background(), CommitRequest{Path: link, Data: []byte("bad"), AllowedRoots: []string{root}}); err == nil {
		t.Fatal("expected symlink target rejection")
	}
}
