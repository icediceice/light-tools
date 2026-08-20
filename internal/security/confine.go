package security

import (
	"errors"
	"fmt"
	"path/filepath"
)

// Confiner resolves caller-supplied paths beneath configured roots while
// excluding private state roots. It is immutable after construction and safe
// to share between handlers.
type Confiner struct {
	roots  []string
	denied []string
}

// NewConfiner canonicalizes the policy once so every consumer applies the same
// allowed-root and denied-root boundary.
func NewConfiner(roots, denied []string) (*Confiner, error) {
	if len(roots) == 0 {
		return nil, errors.New("at least one allowed root is required")
	}
	c := &Confiner{}
	for _, root := range roots {
		canonical, err := canonicalExisting(root)
		if err != nil {
			return nil, fmt.Errorf("allowed root %q: %w", root, err)
		}
		c.roots = appendUnique(c.roots, canonical)
	}
	for _, root := range denied {
		if root == "" {
			continue
		}
		canonical, err := canonicalExisting(root)
		if err != nil {
			return nil, fmt.Errorf("denied root %q: %w", root, err)
		}
		c.denied = appendUnique(c.denied, canonical)
	}
	return c, nil
}

// Resolve canonicalizes path beneath an allowed root and rejects paths within a
// denied root. Missing final components are allowed when their nearest existing
// ancestor remains confined.
func (c *Confiner) Resolve(path string) (string, error) {
	if c == nil {
		return "", errors.New("path confiner is required")
	}
	resolved, ancestor, err := canonicalCandidate(path)
	if err != nil {
		return "", err
	}
	if err := c.permitCanonical(resolved); err != nil {
		return "", err
	}
	for _, root := range c.roots {
		if !within(root, resolved) {
			continue
		}
		existingRelative, err := filepath.Rel(root, ancestor)
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

// Permit applies only the denied-root half of the policy. It is used for paths
// discovered by trusted registries or recursive walkers that may legitimately
// live outside the caller-facing allowed roots.
func (c *Confiner) Permit(path string) error {
	if c == nil {
		return errors.New("path confiner is required")
	}
	resolved, _, err := canonicalCandidate(path)
	if err != nil {
		return err
	}
	return c.permitCanonical(resolved)
}

// Recheck verifies that path still resolves to the expected canonical identity.
func (c *Confiner) Recheck(path, expected string) error {
	resolved, err := c.Resolve(path)
	if err != nil {
		return err
	}
	if !samePath(resolved, expected) {
		return fmt.Errorf("path identity changed: expected %q, got %q", expected, resolved)
	}
	return nil
}

func (c *Confiner) permitCanonical(path string) error {
	for _, root := range c.denied {
		if within(root, path) {
			return fmt.Errorf("path %q is inside private state root", path)
		}
	}
	return nil
}

func appendUnique(paths []string, candidate string) []string {
	for _, path := range paths {
		if samePath(path, candidate) {
			return paths
		}
	}
	return append(paths, candidate)
}
