// Package state owns the standalone process's on-disk layout.
package state

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

const SchemaVersion = "1"

// Layout keeps stores deliberately separate so cleanup code cannot be handed a
// common ancestor by accident.
type Layout struct {
	Config    string
	Secrets   string
	Snapshots string
	Spills    string
	// Telemetry holds local-only aggregate snapshots. It is a peer of the other
	// data roots rather than nested under Config because it carries retention
	// and pruning behaviour settings do not.
	Telemetry string
}

// Resolve returns and creates the XDG-backed store roots. No configuration file
// is required; callers may apply explicit overrides after this function.
func Resolve() (Layout, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Layout{}, fmt.Errorf("resolve home: %w", err)
	}
	configBase := envOr("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	dataBase := envOr("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	runtimeBase := os.Getenv("XDG_RUNTIME_DIR")
	if runtimeBase == "" {
		runtimeBase = filepath.Join(os.TempDir(), fmt.Sprintf("light-tools-%d", os.Getpid()))
	}

	layout := Layout{
		Config:    filepath.Join(configBase, "light-tools"),
		Secrets:   filepath.Join(dataBase, "light-tools-secrets"),
		Snapshots: filepath.Join(dataBase, "light-tools-snapshots"),
		Spills:    filepath.Join(runtimeBase, "light-tools-spills"),
		Telemetry: filepath.Join(dataBase, "light-tools-telemetry"),
	}
	for _, root := range []string{layout.Config, layout.Secrets, layout.Snapshots, layout.Spills, layout.Telemetry} {
		if err := initializeStore(root); err != nil {
			return Layout{}, err
		}
	}
	return layout, nil
}

func initializeStore(root string) error {
	if root == "" || root == "." || root == string(filepath.Separator) {
		return errors.New("refusing unsafe state root")
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", root, err)
	}
	if err := os.Chmod(root, 0o700); err != nil && runtime.GOOS != "windows" {
		return fmt.Errorf("chmod %s: %w", root, err)
	}
	version := filepath.Join(root, "SCHEMA")
	if err := os.WriteFile(version, []byte(SchemaVersion+"\n"), 0o600); err != nil {
		return fmt.Errorf("write schema marker: %w", err)
	}
	lock := filepath.Join(root, ".lock")
	f, err := os.OpenFile(lock, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("create store lock: %w", err)
	}
	return f.Close()
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
