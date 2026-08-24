package security

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Unconfined removes the allowed-root boundary and NOTHING else. The denied
// private-state roots are what stop a tool call fabricating the telemetry the
// vault UI renders as measured data, so they must survive the widening.
func TestUnconfinedStillDeniesPrivateStateRoots(t *testing.T) {
	root := t.TempDir()
	private := filepath.Join(root, "private")
	if err := os.Mkdir(private, 0o700); err != nil {
		t.Fatal(err)
	}
	confiner, err := NewUnconfined([]string{private})
	if err != nil {
		t.Fatal(err)
	}

	outside := filepath.Join(t.TempDir(), "anywhere.txt")
	if _, err := confiner.Resolve(outside); err != nil {
		t.Fatalf("unconfined Resolve refused a path outside every root: %v", err)
	}
	for _, path := range []string{private, filepath.Join(private, "vault.enc")} {
		if _, err := confiner.Resolve(path); err == nil || !strings.Contains(err.Error(), "private state root") {
			t.Fatalf("unconfined Resolve(%q) did not deny private root: %v", path, err)
		}
		if err := confiner.Permit(path); err == nil {
			t.Fatalf("unconfined Permit(%q) did not deny private root", path)
		}
	}
}

// A symlink pointing into a denied root must not become reachable just because
// the allowed-root test no longer runs: canonicalization happens BEFORE the
// denied check, in both postures.
func TestUnconfinedStillResolvesSymlinksBeforeDenying(t *testing.T) {
	root := t.TempDir()
	private := filepath.Join(root, "private")
	if err := os.Mkdir(private, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "private-link")
	if err := os.Symlink(private, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	confiner, err := NewUnconfined([]string{private})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := confiner.Resolve(filepath.Join(link, "vault.enc")); err == nil {
		t.Fatal("unconfined Resolve followed a symlink into a denied root")
	}
}

// Policy is the one decision every tool consumes; if it stopped producing the
// posture it describes, light_file, light_bash and light_ops would drift apart
// silently rather than fail.
func TestPolicyBuildsThePostureItDescribes(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.txt")

	confined, err := (Policy{Roots: []string{root}}).Confiner()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := confined.Resolve(outside); err == nil {
		t.Fatal("a confined policy admitted a path outside its roots")
	}

	unconfined, err := (Policy{Unconfined: true}).Confiner()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := unconfined.Resolve(outside); err != nil {
		t.Fatalf("an unconfined policy refused a path outside every root: %v", err)
	}
}

// WithExtraRoots is how light_ops folds log_roots in. Widening an unconfined
// policy must stay a no-op: recording extra roots would imply a boundary that
// does not exist, and a later reader would trust it.
func TestWithExtraRootsIsANoOpWhenUnconfined(t *testing.T) {
	extra := t.TempDir()

	widened := (Policy{Roots: []string{"/a"}}).WithExtraRoots(extra)
	if len(widened.Roots) != 2 || widened.Roots[1] != extra {
		t.Fatalf("confined policy did not take the extra root: %v", widened.Roots)
	}

	untouched := (Policy{Unconfined: true}).WithExtraRoots(extra)
	if len(untouched.Roots) != 0 || !untouched.Unconfined {
		t.Fatalf("unconfined policy recorded a boundary it does not have: %+v", untouched)
	}
}
