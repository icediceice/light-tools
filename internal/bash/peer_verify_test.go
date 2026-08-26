package bash

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPeerExplicitCaptureFailureStillRunsUnbacked(t *testing.T) {
	runner, _, root := newGuardRunner(t)
	if err := os.WriteFile(filepath.Join(root, "snapshots"), []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "target.txt")
	if err := os.WriteFile(path, []byte("target"), 0o600); err != nil {
		t.Fatal(err)
	}

	result, err := runner.Run(context.Background(), Request{Command: "rm target.txt", Cwd: root})
	if err != nil {
		t.Fatalf("explicit mutation was turned into a hard failure: %v", err)
	}
	if result["protection"] != "unbacked" {
		t.Fatalf("capture failure did not degrade to unbacked: %#v", result)
	}
	reason, _ := result["reason"].(string)
	if !strings.Contains(reason, "could not be captured") {
		t.Fatalf("capture failure reason was not preserved: %q", reason)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatalf("the command did not run: stat=%v", statErr)
	}
}

func TestPeerSedInPlaceSpellingsAreModeled(t *testing.T) {
	tests := []struct {
		name      string
		command   string
		badOption string
	}{
		{name: "short", command: "sed -i s/a/b/ target.txt"},
		{name: "short backup", command: "sed -i.bak s/a/b/ target.txt", badOption: "-i.bak"},
		{name: "long", command: "sed --in-place s/a/b/ target.txt"},
		{name: "long backup", command: "sed --in-place=.bak s/a/b/ target.txt", badOption: "--in-place=.bak"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plan, ok := planGlobMutation(test.command)
			if !ok {
				t.Fatalf("%q is not modeled as a mutation", test.command)
			}
			if plan.Effect != effectEdit {
				t.Fatalf("%q modeled with effect %q", test.command, plan.Effect)
			}
			bad := unmodeledGlobFlags(plan)
			if test.badOption == "" {
				if len(bad) != 0 {
					t.Fatalf("%q has unexpected unmodeled options: %v", test.command, bad)
				}
				return
			}
			if len(bad) != 1 || bad[0] != test.badOption {
				t.Fatalf("%q options=%v, want [%s]", test.command, bad, test.badOption)
			}
		})
	}
}
