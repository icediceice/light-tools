// Package security confines filesystem operations to configured roots.
package security

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ResolveBeneath canonicalizes path beneath one of roots. For creates it
// canonicalizes the nearest existing ancestor and then rejoins the missing
// suffix, preventing a symlinked parent from escaping the root.
func ResolveBeneath(path string, roots []string) (string, error) {
	if path == "" {
		return "", errors.New("path is required")
	}
	if len(roots) == 0 {
		return "", errors.New("at least one allowed root is required")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("absolute path: %w", err)
	}
	absolute = filepath.Clean(absolute)

	canonicalRoots := make([]string, 0, len(roots))
	for _, root := range roots {
		canonical, err := canonicalExisting(root)
		if err != nil {
			return "", fmt.Errorf("allowed root %q: %w", root, err)
		}
		canonicalRoots = append(canonicalRoots, canonical)
	}

	ancestor, suffix, err := nearestExisting(absolute)
	if err != nil {
		return "", err
	}
	canonicalAncestor, err := filepath.EvalSymlinks(ancestor)
	if err != nil {
		return "", fmt.Errorf("canonicalize ancestor: %w", err)
	}
	canonicalAncestor, err = filepath.Abs(canonicalAncestor)
	if err != nil {
		return "", err
	}
	resolved := canonicalAncestor
	for i := len(suffix) - 1; i >= 0; i-- {
		resolved = filepath.Join(resolved, suffix[i])
	}
	resolved = filepath.Clean(resolved)

	for _, root := range canonicalRoots {
		if !within(root, resolved) {
			continue
		}
		existingRelative, err := filepath.Rel(root, canonicalAncestor)
		if err != nil {
			return "", err
		}
		if err := platformBeneath(root, existingRelative); err != nil {
			return "", err
		}
		return resolved, nil
	}
	return "", fmt.Errorf("path %q escapes allowed roots", path)
}

// Recheck verifies that path still resolves to the expected canonical identity.
func Recheck(path, expected string, roots []string) error {
	resolved, err := ResolveBeneath(path, roots)
	if err != nil {
		return err
	}
	if !samePath(resolved, expected) {
		return fmt.Errorf("path identity changed: expected %q, got %q", expected, resolved)
	}
	return nil
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
