package security

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestConfinerRejectsSymlinkedParentCreate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink permissions vary on Windows")
	}
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	confiner, err := NewConfiner([]string{root}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := confiner.Resolve(filepath.Join(root, "escape", "new.txt")); err == nil {
		t.Fatal("expected symlinked parent escape rejection")
	}
}

func TestConfinerRecheckDetectsParentSwap(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink permissions vary on Windows")
	}
	root := t.TempDir()
	outside := t.TempDir()
	parent := filepath.Join(root, "parent")
	if err := os.Mkdir(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	confiner, err := NewConfiner([]string{root}, nil)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(parent, "new.txt")
	resolved, err := confiner.Resolve(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(parent, parent+".old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, parent); err != nil {
		t.Fatal(err)
	}
	if err := confiner.Recheck(path, resolved); err == nil {
		t.Fatal("expected parent-swap detection")
	}
}
