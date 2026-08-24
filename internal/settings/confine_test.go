package settings

import (
	"os"
	"path/filepath"
	"testing"
)

// Absent means the default posture, unconfined — the same rule the
// disabled-tools markers follow.
func TestConfineMarkerAbsentMeansUnconfined(t *testing.T) {
	store := New(t.TempDir(), []string{"light_file"})
	confine, err := store.LoadConfine()
	if err != nil {
		t.Fatal(err)
	}
	if confine {
		t.Fatal("no marker on disk reported as confined")
	}
}

// Setting twice and clearing twice must both succeed, so two stores acting
// concurrently commute exactly as SetDisabled does.
func TestConfineMarkerRoundTripsAndCommutes(t *testing.T) {
	root := t.TempDir()
	store := New(root, []string{"light_file"})
	other := New(root, []string{"light_file"})

	for _, s := range []*Store{store, other} {
		if err := s.SetConfine(true); err != nil {
			t.Fatalf("SetConfine(true): %v", err)
		}
	}
	confine, err := store.LoadConfine()
	if err != nil || !confine {
		t.Fatalf("marker not observed after two writers: %v %v", confine, err)
	}

	for _, s := range []*Store{store, other} {
		if err := s.SetConfine(false); err != nil {
			t.Fatalf("SetConfine(false): %v", err)
		}
	}
	confine, err = store.LoadConfine()
	if err != nil || confine {
		t.Fatalf("marker survived two removals: %v %v", confine, err)
	}
}

// The confinement marker lives BESIDE disabled-tools, never inside it:
// LoadDisabled refuses any name in that directory that is not a known tool, so
// filing it there would break tool loading instead of configuring confinement.
func TestConfineMarkerDoesNotBreakToolMarkerLoading(t *testing.T) {
	root := t.TempDir()
	store := New(root, []string{"light_file"})
	if err := store.SetConfine(true); err != nil {
		t.Fatal(err)
	}
	if err := store.SetDisabled("light_file", true); err != nil {
		t.Fatal(err)
	}
	disabled, err := store.LoadDisabled()
	if err != nil {
		t.Fatalf("the confinement marker was read as a tool marker: %v", err)
	}
	if len(disabled) != 1 || !disabled["light_file"] {
		t.Fatalf("unexpected disabled set: %v", disabled)
	}
	if _, err := os.Stat(filepath.Join(root, confineMarker)); err != nil {
		t.Fatalf("confinement marker not beside the disabled-tools directory: %v", err)
	}
}
