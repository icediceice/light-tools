// Package security confines filesystem operations to configured roots.
package security

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// canonicalCandidate resolves symlinks in the nearest existing ancestor and
// rejoins any missing suffix without following a not-yet-created path.
func canonicalCandidate(path string) (string, string, error) {
	if path == "" {
		return "", "", errors.New("path is required")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", "", fmt.Errorf("absolute path: %w", err)
	}
	absolute = filepath.Clean(absolute)
	ancestor, suffix, err := nearestExisting(absolute)
	if err != nil {
		return "", "", err
	}
	canonicalAncestor, err := filepath.EvalSymlinks(ancestor)
	if err != nil {
		return "", "", fmt.Errorf("canonicalize ancestor: %w", err)
	}
	canonicalAncestor, err = filepath.Abs(canonicalAncestor)
	if err != nil {
		return "", "", err
	}
	resolved := canonicalAncestor
	for index := len(suffix) - 1; index >= 0; index-- {
		resolved = filepath.Join(resolved, suffix[index])
	}
	return filepath.Clean(resolved), filepath.Clean(canonicalAncestor), nil
}

func nearestExisting(path string) (string, []string, error) {
	current := path
	var suffix []string
	for {
		_, err := os.Lstat(current)
		if err == nil {
			return current, suffix, nil
		}
		if !os.IsNotExist(err) {
			return "", nil, fmt.Errorf("inspect %q: %w", current, err)
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", nil, fmt.Errorf("no existing ancestor for %q", path)
		}
		suffix = append(suffix, filepath.Base(current))
		current = parent
	}
}

func canonicalExisting(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", err
	}
	return filepath.Clean(canonical), nil
}

func within(root, path string) bool {
	relative, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative))
}

func samePath(a, b string) bool {
	if filepath.Separator == '\\' {
		return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
	}
	return filepath.Clean(a) == filepath.Clean(b)
}
