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

	"github.com/icediceice/light-tools/internal/security"
)

func testHash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func testConfiner(t *testing.T, root string) *security.Confiner {
	t.Helper()
	confiner, err := security.NewConfiner([]string{root}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return confiner
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
	_, err := Commit(context.Background(), CommitRequest{Path: path, Data: []byte("ours"), ExpectedSHA: readSHA, Confiner: testConfiner(t, root)})
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
	confiner := testConfiner(t, root)
	path := filepath.Join(root, "value.txt")
	if err := os.WriteFile(path, []byte("first"), 0o640); err != nil {
		t.Fatal(err)
	}
	result, err := Commit(context.Background(), CommitRequest{Path: path, Data: []byte("second"), Confiner: confiner})
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
	if _, err := Commit(context.Background(), CommitRequest{Path: link, Data: []byte("bad"), Confiner: confiner}); err == nil {
		t.Fatal("expected symlink target rejection")
	}
}

func TestCommitRejectsDeniedTargetBeforeSnapshot(t *testing.T) {
	root := t.TempDir()
	private := filepath.Join(root, "private")
	if err := os.Mkdir(private, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(private, "master.key")
	if err := os.WriteFile(path, []byte("key"), 0o600); err != nil {
		t.Fatal(err)
	}
	confiner, err := security.NewConfiner([]string{root}, []string{private})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Commit(context.Background(), CommitRequest{Path: path, Data: []byte("overwrite"), Confiner: confiner}); err == nil {
		t.Fatal("expected denied target refusal")
	}
	if data, _ := os.ReadFile(path); string(data) != "key" {
		t.Fatalf("denied target changed: %q", data)
	}
}
