package security

import (
	"errors"
	"fmt"
	"path/filepath"
)

// Confiner resolves caller-supplied paths beneath configured roots while
// excluding private state roots. It is immutable after construction and safe
// to share between handlers.
//
// An unconfined Confiner has no allowed-root boundary at all. That is a policy
// choice, not a weaker object: canonicalization, symlink evaluation and the
// denied private-state check are invariants and still run on every call.
type Confiner struct {
	roots      []string
	denied     []string
	unconfined bool
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

// Policy is the resolved confinement decision. It is computed ONCE in the
// composition root and handed to every tool, because light_file, light_bash and
// light_ops each construct their own Confiner and would otherwise drift apart.
type Policy struct {
	Roots      []string
	Denied     []string
	Unconfined bool
}

// Confiner builds the Confiner this policy describes.
func (p Policy) Confiner() (*Confiner, error) {
	if p.Unconfined {
		return NewUnconfined(p.Denied)
	}
	return NewConfiner(p.Roots, p.Denied)
}

// WithExtraRoots returns a copy whose allowed roots also include extra. It is a
// no-op when unconfined: there is no boundary left to widen, and silently
// recording extra roots would imply one exists.
func (p Policy) WithExtraRoots(extra ...string) Policy {
	if p.Unconfined {
		return p
	}
	p.Roots = append(append([]string{}, p.Roots...), extra...)
	return p
}

// NewUnconfined returns a Confiner with no allowed-root boundary. It exists
// because "anywhere on this filesystem" cannot be expressed as a root value:
// on Windows there is no single filesystem root, so allowed_roots = ["/"] would
// silently cover neither C:\ nor D:\. denied is still canonicalized and still
// enforced on every Resolve and Permit.
func NewUnconfined(denied []string) (*Confiner, error) {
	c := &Confiner{unconfined: true}
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
// ancestor remains confined. An unconfined Confiner skips only the allowed-root
// test; every other check still applies.
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
	// Ordered deliberately: the denied private-state check above runs FIRST, so
	// an unconfined policy never reaches light-tools' own secrets, snapshots,
	// spills or telemetry. Widening the boundary must not widen that.
	if c.unconfined {
		return resolved, nil
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
