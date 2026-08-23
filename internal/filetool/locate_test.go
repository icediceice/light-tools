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

func TestLocateEngineResultContracts(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "contract.txt")
	if err := os.WriteFile(path, []byte("before\nalpha needle omega\nafter\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	permit := func(string) error { return nil }

	goMatches, err := locateGo(path, regexp.MustCompile("needle"), 1, permit)
	if err != nil {
		t.Fatal(err)
	}
	if len(goMatches) != 1 {
		t.Fatalf("Go locate returned %#v", goMatches)
	}
	goMatch := goMatches[0]
	if goMatch.Path != path || goMatch.Line != 2 || goMatch.Start != 6 || goMatch.End != 12 ||
		goMatch.Text != "before\nalpha needle omega\nafter" {
		t.Fatalf("Go locate contract drifted: %#v", goMatch)
	}

	if _, err := exec.LookPath("rg"); err != nil {
		t.Log("rg unavailable; pure-Go contract remains covered")
		return
	}
	rgMatches, err := locateRG(context.Background(), path, "needle", true, 1, permit)
	if err != nil {
		t.Fatal(err)
	}
	if len(rgMatches) != 1 {
		t.Fatalf("rg locate returned %#v", rgMatches)
	}
	rgMatch := rgMatches[0]
	if rgMatch.Path != path || rgMatch.Line != 2 || rgMatch.Start != 6 || rgMatch.End != 12 ||
		rgMatch.Text != "alpha needle omega" {
		t.Fatalf("rg locate contract drifted: %#v", rgMatch)
	}
	// Context is intentionally asserted per engine rather than as parity: rg
	// currently discards JSON context events while the Go fallback joins them.
	if rgMatch.Text == goMatch.Text {
		t.Fatal("fixture no longer exposes the documented locate context-engine difference")
	}
}
