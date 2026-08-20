package secret

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
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

func TestVaultProcessHelper(t *testing.T) {
	root := os.Getenv("LIGHT_TOOLS_TEST_VAULT_ROOT")
	name := os.Getenv("LIGHT_TOOLS_TEST_VAULT_NAME")
	if root == "" || name == "" {
		return
	}
	if err := New(root).Set(name, "value-"+name); err != nil {
		t.Fatal(err)
	}
}

func TestVaultSerializesIndependentProcesses(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("process locking is compile-checked on Windows")
	}
	root := t.TempDir()
	var commands []*exec.Cmd
	for index := 0; index < 12; index++ {
		name := fmt.Sprintf("process-%02d", index)
		command := exec.Command(os.Args[0], "-test.run=^TestVaultProcessHelper$")
		command.Env = append(os.Environ(), "LIGHT_TOOLS_TEST_VAULT_ROOT="+root, "LIGHT_TOOLS_TEST_VAULT_NAME="+name)
		if err := command.Start(); err != nil {
			t.Fatal(err)
		}
		commands = append(commands, command)
	}
	for _, command := range commands {
		if err := command.Wait(); err != nil {
			t.Fatalf("subprocess write: %v", err)
		}
	}
	names, err := New(root).List()
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != len(commands) {
		t.Fatalf("lost concurrent process writes: got %d, want %d (%v)", len(names), len(commands), names)
	}
}

func TestVaultGroupsPreserveEmptyAndRefuseMerge(t *testing.T) {
	vault := New(t.TempDir())
	if err := vault.AddGroup("Production"); err != nil {
		t.Fatal(err)
	}
	if err := vault.AddGroup("Empty"); err != nil {
		t.Fatal(err)
	}
	if err := vault.SetWithGroup("token", "value", "Production"); err != nil {
		t.Fatal(err)
	}
	if err := vault.RenameGroup("Production", "Primary"); err != nil {
		t.Fatal(err)
	}
	if err := vault.RenameGroup("Primary", "Empty"); err == nil {
		t.Fatal("group rename merged into an existing group")
	}
	overview, err := vault.Overview()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(overview.Groups, ",") != "Empty,Primary" {
		t.Fatalf("explicit groups were not preserved: %#v", overview.Groups)
	}
	if len(overview.Secrets) != 1 || overview.Secrets[0].Group != "Primary" || overview.Secrets[0].UpdatedAt.IsZero() {
		t.Fatalf("secret metadata not updated: %#v", overview.Secrets)
	}
	if err := vault.DeleteGroup("Primary"); err != nil {
		t.Fatal(err)
	}
	overview, err = vault.Overview()
	if err != nil {
		t.Fatal(err)
	}
	if len(overview.Secrets) != 1 || overview.Secrets[0].Group != "" {
		t.Fatalf("deleting a group did not unassign its secret: %#v", overview.Secrets)
	}
	if value, err := vault.Resolve("token"); err != nil || value != "value" {
		t.Fatalf("deleting a group deleted its secret: %q, %v", value, err)
	}
}

func TestVaultPreservesUnknownEncryptedFields(t *testing.T) {
	vault := New(t.TempDir())
	store := diskStore{
		Values: map[string]string{"old": "value"},
		Extra:  map[string]json.RawMessage{"future": json.RawMessage(`{"enabled":true}`)},
	}
	store.ensure()
	if err := vault.save(store); err != nil {
		t.Fatal(err)
	}
	if err := vault.Set("new", "value"); err != nil {
		t.Fatal(err)
	}
	loaded, err := vault.load()
	if err != nil {
		t.Fatal(err)
	}
	if string(loaded.Extra["future"]) != `{"enabled":true}` {
		t.Fatalf("unknown field was dropped: %s", loaded.Extra["future"])
	}
}

func TestVaultGrowthCapStillAllowsRecovery(t *testing.T) {
	vault := New(t.TempDir())
	store := diskStore{Values: map[string]string{"oversized": strings.Repeat("x", maxVaultPlaintext+1024)}}
	store.ensure()
	if err := vault.save(store); err != nil {
		t.Fatal(err)
	}
	if err := vault.Set("another", "value"); err == nil {
		t.Fatal("growth above total cap was accepted")
	}
	if err := vault.Remove("oversized"); err != nil {
		t.Fatalf("shrinking an oversized vault was refused: %v", err)
	}
	if err := vault.Set("another", "value"); err != nil {
		t.Fatalf("vault did not recover after shrink: %v", err)
	}
}

func TestVaultRejectsOversizedSecret(t *testing.T) {
	vault := New(t.TempDir())
	if err := vault.Set("large", strings.Repeat("x", maxSecretBytes+1)); err == nil {
		t.Fatal("oversized secret was accepted")
	}
}

func TestVaultMissingKeyDoesNotMintReplacement(t *testing.T) {
	root := t.TempDir()
	vault := New(root)
	if err := vault.Set("token", "value"); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "master.key")); err != nil {
		t.Fatal(err)
	}
	if _, err := vault.Resolve("token"); err == nil || !strings.Contains(err.Error(), "restore") {
		t.Fatalf("missing key did not produce restore guidance: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "master.key")); !os.IsNotExist(err) {
		t.Fatalf("missing key was silently recreated: %v", err)
	}
}

func TestVaultRejectsTamperedCiphertext(t *testing.T) {
	root := t.TempDir()
	vault := New(root)
	if err := vault.Set("token", "value"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "vault.enc")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	ciphertext, err := base64.RawStdEncoding.DecodeString(string(data))
	if err != nil {
		t.Fatal(err)
	}
	ciphertext[len(ciphertext)/2] ^= 1
	data = []byte(base64.RawStdEncoding.EncodeToString(ciphertext))
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := vault.Resolve("token"); err == nil {
		t.Fatal("tampered ciphertext decrypted")
	}
}

func TestVaultRejectsInvalidNamesAndGroups(t *testing.T) {
	vault := New(t.TempDir())
	for _, name := range []string{"", "../escape", "has space", "slash/name"} {
		if err := vault.Set(name, "value"); err == nil {
			t.Errorf("Set(%q) accepted invalid name", name)
		}
	}
	for _, group := range []string{"", "   ", "line\nbreak", strings.Repeat("x", 65)} {
		if err := vault.AddGroup(group); err == nil {
			t.Errorf("AddGroup(%q) accepted invalid group", group)
		}
	}
}
