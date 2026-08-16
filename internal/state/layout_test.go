package state

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestResolveCreatesSeparatePrivateStores(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(root, "runtime"))
	layout, err := Resolve()
	if err != nil {
		t.Fatal(err)
	}
	paths := []string{layout.Config, layout.Secrets, layout.Snapshots, layout.Spills}
	seen := make(map[string]bool)
	for _, path := range paths {
		if seen[path] {
			t.Fatalf("stores share a root: %#v", layout)
		}
		seen[path] = true
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if runtime.GOOS != "windows" && info.Mode().Perm() != 0o700 {
			t.Fatalf("%s mode=%o", path, info.Mode().Perm())
		}
		schema, err := os.ReadFile(filepath.Join(path, "SCHEMA"))
		if err != nil || string(schema) != SchemaVersion+"\n" {
			t.Fatalf("%s schema=%q err=%v", path, schema, err)
		}
	}
}
