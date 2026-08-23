package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestVerifyInitPrintIsSideEffectFree(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(root, "runtime"))

	captureStdout(t, func() error {
		return runInit([]string{"--client", "print"})
	})

	for _, path := range []string{
		filepath.Join(root, "config", "light-tools"),
		filepath.Join(root, "data", "light-tools-secrets"),
		filepath.Join(root, "data", "light-tools-snapshots"),
		filepath.Join(root, "runtime", "light-tools-spills"),
	} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("print mode wrote %s: %v", path, err)
		}
	}
}

func TestVerifyMergePreservesForeignJSONNumbers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp_config.json")
	const exact = "9007199254740993"
	existing := []byte(`{"sequence":9007199254740993,"mcpServers":{"foreign":{"command":"other","sequence":9007199254740993}}}`)
	if err := os.WriteFile(path, existing, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := mergeAntigravityConfig(path, "light-tools", map[string]any{"command": "light-tools"}); err != nil {
		t.Fatal(err)
	}

	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	decoder.UseNumber()
	var document map[string]any
	if err := decoder.Decode(&document); err != nil {
		t.Fatal(err)
	}
	if got := document["sequence"].(json.Number).String(); got != exact {
		t.Errorf("unrelated top-level number changed: got %s, want %s", got, exact)
	}
	foreign := document["mcpServers"].(map[string]any)["foreign"].(map[string]any)
	if got := foreign["sequence"].(json.Number).String(); got != exact {
		t.Errorf("foreign server number changed: got %s, want %s", got, exact)
	}
}
