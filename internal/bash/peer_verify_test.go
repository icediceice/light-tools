package bash

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// PEER VERIFY G1: the explicit lane promised "capture-only, never refuse".
// A regular file the process cannot READ is still protectable by the hazard
// matrix (Lstat succeeds, mode is regular), so the bounded capture is attempted
// and os.Open fails with EACCES. That error is not TooLarge, so
// prepareGlobGuard returns it and the whole light_bash call fails — a command
// that ran before this change now does not run at all.
func TestPeerUnreadableExplicitSurfaceStillRuns(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission case")
	}
	if os.Geteuid() == 0 {
		t.Skip("root can read a 0000-mode file")
	}
	runner, _, root := newGuardRunner(t)
	path := filepath.Join(root, "locked.txt")
	if err := os.WriteFile(path, []byte("locked"), 0o000); err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(context.Background(), Request{Command: "rm locked.txt", Cwd: root})
	if err != nil {
		t.Fatalf("explicit rm of an unreadable file was turned into a hard failure: %v", err)
	}
	if result["protection"] != "unbacked" {
		t.Fatalf("unreadable surface did not degrade to unbacked: %#v", result)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatalf("the command did not run: stat=%v", statErr)
	}
}

// PEER VERIFY G2: step 1 promised "sed enters the lane only with an in-place
// flag". --in-place IS the in-place flag; sedOperands only recognises -i.
func TestPeerSedLongInPlaceIsModeled(t *testing.T) {
	for _, command := range []string{
		"sed --in-place s/a/b/ target.txt",
		"sed --in-place=.bak s/a/b/ target.txt",
	} {
		plan, ok := planGlobMutation(command)
		if command == "sed --in-place=.bak s/a/b/ target.txt" {
			// A suffix form writes a side file; it must be MODELED but then
			// rejected as an unmodeled option, exactly like -i.bak.
			if !ok {
				t.Errorf("%q is not modeled at all, so it never reaches the unmodeled-option reason", command)
			}
			continue
		}
		if !ok {
			t.Errorf("%q is not modeled as a mutation: it now runs with no guard at all", command)
			continue
		}
		if plan.Effect != effectEdit {
			t.Errorf("%q modeled with effect %q", command, plan.Effect)
		}
	}
}

// PEER VERIFY: what the dominant `go fmt` shape actually reports now. Purely
// observational — it records the reason string the new unbacked label carries.
func TestPeerGoFmtRecursiveShapeObservation(t *testing.T) {
	runner, _, root := newGuardRunner(t)
	for _, command := range []string{"go fmt ./...", "gofmt -w .", "rm -rf build"} {
		guard, refusal, err := runner.prepareGlobGuard(Request{Command: command, Cwd: root})
		t.Logf("%-14s guard=%#v refusal=%#v err=%v", command, guard, refusal, err)
	}
}

// PEER VERIFY G1 (second trigger, worse blast): if the snapshot vault itself
// cannot be written — full disk, wrong ownership, read-only mount — the same
// non-TooLarge branch fires and EVERY modeled explicit mutation stops running.
func TestPeerUnwritableVaultDoesNotBrickExplicitMutations(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("POSIX permission case")
	}
	runner, vault, root := newGuardRunner(t)
	_ = vault
	snapshots := filepath.Join(root, "snapshots")
	if err := os.MkdirAll(snapshots, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(snapshots, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(snapshots, 0o700) })
	path := filepath.Join(root, "a.txt")
	if err := os.WriteFile(path, []byte("a"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(context.Background(), Request{Command: "rm a.txt", Cwd: root})
	if err != nil {
		t.Fatalf("an unwritable vault turned an ordinary rm into a hard failure: %v", err)
	}
	if result["protection"] != "unbacked" {
		t.Fatalf("unwritable vault did not degrade to unbacked: %#v", result)
	}
}
