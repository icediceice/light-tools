package settings

import (
	"os"
	"path/filepath"
	"testing"
)

var allTools = []string{"light_bash", "light_file", "light_ops", "light_scp", "light_ssh"}

func TestMissingDirectoryIsTheDefaultEmptySet(t *testing.T) {
	store := New(t.TempDir(), allTools)
	disabled, err := store.LoadDisabled()
	if err != nil {
		t.Fatal(err)
	}
	if len(disabled) != 0 {
		t.Fatalf("missing directory = %#v, want nothing withheld", disabled)
	}
}

func TestSetDisabledRoundTripsSingleMarkers(t *testing.T) {
	root := t.TempDir()
	store := New(root, allTools)
	if err := store.SetDisabled("light_ops", true); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "disabled-tools", "light_ops")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	// Windows reports 0666 for a file created 0600: the ACL, not the mode bits,
	// carries the restriction there. Assert the mode only where it is meaningful
	// rather than weakening it everywhere.
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("marker mode = %o", info.Mode().Perm())
	}
	if info.Size() != 0 {
		t.Fatalf("marker carries %d bytes; presence alone is the state", info.Size())
	}
	disabled, err := store.LoadDisabled()
	if err != nil || len(disabled) != 1 || !disabled["light_ops"] {
		t.Fatalf("load after disable = %#v err=%v", disabled, err)
	}

	// Re-creating an existing marker and removing an absent one both succeed.
	if err := store.SetDisabled("light_ops", true); err != nil {
		t.Fatalf("re-disable = %v", err)
	}
	if err := store.SetDisabled("light_bash", false); err != nil {
		t.Fatalf("remove-absent = %v", err)
	}
	if err := store.SetDisabled("light_ops", false); err != nil {
		t.Fatal(err)
	}
	disabled, err = store.LoadDisabled()
	if err != nil || len(disabled) != 0 {
		t.Fatalf("load after enable = %#v err=%v", disabled, err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("marker survived removal: %v", err)
	}
}

func TestUnknownNamesAreRefusedOnBothPaths(t *testing.T) {
	root := t.TempDir()
	store := New(root, allTools)
	if err := store.SetDisabled("light_shell", true); err == nil {
		t.Fatal("SetDisabled accepted an unknown tool")
	}
	if err := os.MkdirAll(filepath.Join(root, "disabled-tools"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "disabled-tools", "light_shell"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadDisabled(); err == nil {
		t.Fatal("LoadDisabled ignored a marker for an unknown tool")
	}
}

func TestStoreMetadataIsIgnoredAndForeignEntriesRefused(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "disabled-tools")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"SCHEMA", ".lock"} {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	store := New(root, allTools)
	disabled, err := store.LoadDisabled()
	if err != nil || len(disabled) != 0 {
		t.Fatalf("metadata treated as a marker: %#v err=%v", disabled, err)
	}
	if err := os.Mkdir(filepath.Join(dir, "light_file"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadDisabled(); err == nil {
		t.Fatal("non-file entry accepted as a marker")
	}
}

// Two Store instances over one root (the MCP process and the vault UI) must
// commute: each touches only its own marker and both observe the union.
func TestMarkerOperationsFromTwoStoresCommute(t *testing.T) {
	root := t.TempDir()
	mcpStore, uiStore := New(root, allTools), New(root, allTools)
	if err := mcpStore.SetDisabled("light_scp", true); err != nil {
		t.Fatal(err)
	}
	if err := uiStore.SetDisabled("light_ssh", true); err != nil {
		t.Fatal(err)
	}
	for _, store := range []*Store{mcpStore, uiStore} {
		disabled, err := store.LoadDisabled()
		if err != nil || !disabled["light_scp"] || !disabled["light_ssh"] || len(disabled) != 2 {
			t.Fatalf("store saw %#v err=%v", disabled, err)
		}
	}
	if err := mcpStore.SetDisabled("light_scp", false); err != nil {
		t.Fatal(err)
	}
	disabled, err := uiStore.LoadDisabled()
	if err != nil || !disabled["light_ssh"] || disabled["light_scp"] {
		t.Fatalf("after re-enable = %#v err=%v", disabled, err)
	}
}
