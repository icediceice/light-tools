// Package settings persists UI-owned tool withholding. Each withheld tool is
// one zero-byte 0600 marker file named exactly for the tool, under
// <config>/disabled-tools/. The machine never writes config.toml and no
// disabled_tools key exists there: markers are the only persisted form, and a
// marker ADDS withholding at the next MCP start rather than defining the whole
// set (launch arguments may withhold more and are not visible here).
package settings

import (
	"fmt"
	"os"
	"path/filepath"
)

// confineMarker is the UI-owned request to run confined. It sits BESIDE the
// disabled-tools directory, never inside it: LoadDisabled refuses any name in
// that directory that is not a known tool, so a marker filed there would break
// tool loading rather than configure confinement.
const confineMarker = "confine"

// Store owns the disabled-tools marker directory beneath a config root, and the
// confinement marker beside it.
type Store struct {
	dir        string
	configRoot string
	known      map[string]bool
}

// New returns the settings store for configRoot. known is the complete tool
// name surface; every marker operation is validated against it.
func New(configRoot string, known []string) *Store {
	names := make(map[string]bool, len(known))
	for _, name := range known {
		names[name] = true
	}
	return &Store{dir: filepath.Join(configRoot, "disabled-tools"), configRoot: configRoot, known: names}
}

// LoadConfine reports whether the UI has asked for confinement. Absent means
// the default posture, unconfined — the same "missing means default" rule the
// disabled-tools markers follow. The answer is consumed once at MCP start; a
// running server's confiner is immutable.
func (s *Store) LoadConfine() (bool, error) {
	info, err := os.Lstat(filepath.Join(s.configRoot, confineMarker))
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() {
		return false, fmt.Errorf("confinement marker %q is not a regular file", confineMarker)
	}
	return true, nil
}

// SetConfine creates or removes the confinement marker. Creating one that
// exists is success and removing an absent one is success, so two stores
// operating concurrently commute exactly as SetDisabled does.
func (s *Store) SetConfine(confine bool) error {
	path := filepath.Join(s.configRoot, confineMarker)
	if !confine {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(s.configRoot, 0o700); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if os.IsExist(err) {
			return nil
		}
		return err
	}
	return file.Close()
}

// LoadDisabled returns the withheld set implied by the markers on disk. A
// missing directory is the default posture: nothing withheld. A name that is
// neither a known tool nor store metadata is refused rather than ignored —
// silently registering a tool an operator asked to withhold is the one failure
// mode that matters here, mirroring the launch-flag rule.
func (s *Store) LoadDisabled() (map[string]bool, error) {
	entries, err := os.ReadDir(s.dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	disabled := make(map[string]bool)
	for _, entry := range entries {
		name := entry.Name()
		if name == "SCHEMA" || name == ".lock" {
			continue // store-root metadata, not a marker
		}
		if !entry.Type().IsRegular() {
			return nil, fmt.Errorf("disabled-tools holds a non-file entry %q", name)
		}
		if !s.known[name] {
			return nil, fmt.Errorf("disabled-tools holds a marker for unknown tool %q", name)
		}
		disabled[name] = true
	}
	return disabled, nil
}

// SetDisabled creates or removes exactly ONE tool's marker; it never accepts a
// whole replacement set. Creating a marker that already exists is success, and
// removing an absent one is success, so concurrent stores commute.
func (s *Store) SetDisabled(tool string, disabled bool) error {
	if !s.known[tool] {
		return fmt.Errorf("unknown tool %q", tool)
	}
	path := filepath.Join(s.dir, tool)
	if !disabled {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return err
	}
	// O_EXCL proves the name did not exist; presence alone carries the state.
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if os.IsExist(err) {
			return nil
		}
		return err
	}
	return file.Close()
}
