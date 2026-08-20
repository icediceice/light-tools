package filetool

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/icediceice/light-tools/internal/security"
)

func TestLocateEnginesAndListSkipDeniedDescendants(t *testing.T) {
	root := t.TempDir()
	private := filepath.Join(root, "private")
	public := filepath.Join(root, "public")
	for _, path := range []string{private, public} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(private, "vault.enc"), []byte("vault-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(public, "note.txt"), []byte("vault-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	confiner, err := security.NewConfiner([]string{root}, []string{private})
	if err != nil {
		t.Fatal(err)
	}

	matches, err := locateGo(root, regexp.MustCompile("vault-secret"), 0, confiner.Permit)
	if err != nil {
		t.Fatal(err)
	}
	assertOnlyPublicMatch(t, matches, public)

	if _, err := exec.LookPath("rg"); err == nil {
		matches, err = locateRG(context.Background(), root, "vault-secret", true, 0, confiner.Permit)
		if err != nil {
			t.Fatal(err)
		}
		assertOnlyPublicMatch(t, matches, public)
	}

	snapshotRoot := filepath.Join(root, "snapshots")
	if err := os.Mkdir(snapshotRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	handler, err := New(Options{Confiner: confiner, SnapshotRoot: snapshotRoot})
	if err != nil {
		t.Fatal(err)
	}
	listed := invokeRequest(t, handler, map[string]any{"verb": "list", "path": root})
	if strings.Contains(listed, `"name":"private"`) {
		t.Fatalf("list enumerated denied root: %s", listed)
	}
}

func assertOnlyPublicMatch(t *testing.T, matches []locateMatch, public string) {
	t.Helper()
	if len(matches) != 1 || !strings.HasPrefix(matches[0].Path, public) {
		t.Fatalf("denied locate result escaped: %#v", matches)
	}
}
