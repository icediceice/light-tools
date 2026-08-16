package security

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestResolveRejectsSymlinkedParentCreate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink permissions vary on Windows")
	}
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveBeneath(filepath.Join(root, "escape", "new.txt"), []string{root}); err == nil {
		t.Fatal("expected symlinked parent escape rejection")
	}
}

func TestRecheckDetectsParentSwap(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink permissions vary on Windows")
	}
	root := t.TempDir()
	outside := t.TempDir()
	parent := filepath.Join(root, "parent")
	if err := os.Mkdir(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(parent, "new.txt")
	resolved, err := ResolveBeneath(path, []string{root})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(parent, parent+".old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, parent); err != nil {
		t.Fatal(err)
	}
	if err := Recheck(path, resolved, []string{root}); err == nil {
		t.Fatal("expected parent-swap detection")
	}
}
