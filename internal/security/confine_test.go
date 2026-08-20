package security

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfinerAllowsConfiguredRootAndDeniesPrivateRoot(t *testing.T) {
	root := t.TempDir()
	private := filepath.Join(root, "private")
	public := filepath.Join(root, "public")
	for _, path := range []string{private, public} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	confiner, err := NewConfiner([]string{root}, []string{private})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := confiner.Resolve(filepath.Join(public, "new.txt")); err != nil || got != filepath.Join(public, "new.txt") {
		t.Fatalf("public resolve = %q, %v", got, err)
	}
	for _, path := range []string{private, filepath.Join(private, "vault.enc")} {
		if _, err := confiner.Resolve(path); err == nil || !strings.Contains(err.Error(), "private state root") {
			t.Fatalf("Resolve(%q) did not deny private root: %v", path, err)
		}
		if err := confiner.Permit(path); err == nil {
			t.Fatalf("Permit(%q) did not deny private root", path)
		}
	}
}

func TestConfinerPermitIgnoresAllowedRootsButStillDeniesSymlink(t *testing.T) {
	root := t.TempDir()
	private := filepath.Join(root, "private")
	outside := t.TempDir()
	if err := os.Mkdir(private, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(outside, "private-link")
	if err := os.Symlink(private, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	confiner, err := NewConfiner([]string{root}, []string{private})
	if err != nil {
		t.Fatal(err)
	}
	if err := confiner.Permit(filepath.Join(outside, "ordinary.log")); err != nil {
		t.Fatalf("Permit rejected trusted path outside allowed roots: %v", err)
	}
	if err := confiner.Permit(filepath.Join(link, "vault.enc")); err == nil {
		t.Fatal("Permit followed symlink into private root")
	}
}
