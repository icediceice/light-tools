package secret

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

func TestVaultEncryptsValuesAndListsOnlyNames(t *testing.T) {
	root := t.TempDir()
	vault := New(root)
	value := "sensitive-value-that-must-not-be-plaintext"
	if err := vault.Set("api-token", value); err != nil {
		t.Fatal(err)
	}
	resolved, err := vault.Resolve("api-token")
	if err != nil || resolved != value {
		t.Fatalf("resolve = %q, %v", resolved, err)
	}
	names, err := vault.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 1 || names[0] != "api-token" {
		t.Fatalf("list exposed wrong data: %#v", names)
	}
	ciphertext, err := os.ReadFile(filepath.Join(root, "vault.enc"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(ciphertext), value) {
		t.Fatal("vault persisted plaintext secret")
	}
	if runtime.GOOS != "windows" {
		for _, name := range []string{"master.key", "vault.enc"} {
			info, err := os.Stat(filepath.Join(root, name))
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != 0o600 {
				t.Fatalf("%s mode = %o", name, info.Mode().Perm())
			}
		}
	}
}

func TestVaultConcurrentSetAndRemove(t *testing.T) {
	vault := New(t.TempDir())
	var group sync.WaitGroup
	for index := 0; index < 24; index++ {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			name := fmt.Sprintf("secret-%02d", index)
			if err := vault.Set(name, name+"-value"); err != nil {
				t.Errorf("set %s: %v", name, err)
			}
		}(index)
	}
	group.Wait()
	names, err := vault.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 24 {
		t.Fatalf("got %d secrets, want 24", len(names))
	}
	if err := vault.Remove(names[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := vault.Resolve(names[0]); err == nil {
		t.Fatal("removed secret still resolves")
	}
}

func TestVaultRejectsInvalidNames(t *testing.T) {
	vault := New(t.TempDir())
	for _, name := range []string{"", "../escape", "has space", "slash/name"} {
		if err := vault.Set(name, "value"); err == nil {
			t.Errorf("Set(%q) accepted invalid name", name)
		}
	}
}
