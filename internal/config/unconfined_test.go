package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// An absent allowed_roots key is the unconfined default. This used to seed the
// server's working directory, which is exactly what stopped light_file editing
// anything outside the project it was spawned in.
func TestAbsentAllowedRootsMeansUnconfined(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte("log_roots = [\"/var/log\"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	value, err := Load(path, dir)
	if err != nil {
		t.Fatal(err)
	}
	if value.RootsConfigured {
		t.Fatal("absent allowed_roots reported as configured")
	}
	if len(value.AllowedRoots) != 0 {
		t.Fatalf("absent allowed_roots seeded a boundary: %v", value.AllowedRoots)
	}
}

// A missing config file is the same posture as a config file without the key.
func TestMissingConfigFileIsUnconfined(t *testing.T) {
	dir := t.TempDir()
	value, err := Load(filepath.Join(dir, "config.toml"), dir)
	if err != nil {
		t.Fatal(err)
	}
	if value.RootsConfigured || len(value.AllowedRoots) != 0 {
		t.Fatalf("missing config produced a boundary: configured=%v roots=%v", value.RootsConfigured, value.AllowedRoots)
	}
}

// A present key still confines, and still resolves relative roots against the
// server's working directory.
func TestPresentAllowedRootsStillConfines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte("allowed_roots = [\"work\"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	value, err := Load(path, dir)
	if err != nil {
		t.Fatal(err)
	}
	if !value.RootsConfigured {
		t.Fatal("present allowed_roots reported as absent")
	}
	if len(value.AllowedRoots) != 1 || value.AllowedRoots[0] != filepath.Join(dir, "work") {
		t.Fatalf("relative root not joined to the working directory: %v", value.AllowedRoots)
	}
}

// An operator who writes an empty list is asking for confinement and would
// otherwise silently get its opposite. Refuse instead of guessing.
func TestEmptyAllowedRootsIsRefusedRatherThanTreatedAsUnconfined(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte("allowed_roots = []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path, dir)
	if err == nil {
		t.Fatal("an empty allowed_roots list was accepted as unconfined")
	}
	if !strings.Contains(err.Error(), "present but empty") {
		t.Fatalf("unhelpful error for an empty allowed_roots: %v", err)
	}
}
