// Package release holds test-only contract guards for the candidate/promote
// release lifecycle. There is no production code here on purpose: the artifacts
// under review are workflow files, and these tests are the only mechanical
// re-check that their load-bearing invariants survive future edits.
package release

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func workflow(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join("..", "..", ".github", "workflows", name)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Windows runners check out with core.autocrlf enabled, so normalise before
	// any line-shaped assertion below.
	return strings.ReplaceAll(string(raw), "\r\n", "\n")
}

// gh resolves the "base repository" for repository-scoped commands such as
// `gh release create` from --repo, then the GH_REPO environment variable, then
// the git remotes of the working directory. GITHUB_REPOSITORY is not consulted
// (see `gh help environment`). promote-release.yml runs no actions/checkout, so
// without GH_REPO or --repo the publish step aborts with "could not determine
// base repository" AFTER refs/tags/v<version> has already been created, leaving
// a module-proxy-visible tag with no release and no assets.
func TestPromoteWorkflowCanResolveBaseRepository(t *testing.T) {
	body := workflow(t, "promote-release.yml")
	if !strings.Contains(body, "gh release create") {
		t.Fatalf("promote-release.yml no longer publishes a release; update this contract test")
	}
	if strings.Contains(body, "actions/checkout") {
		return
	}
	if strings.Contains(body, "GH_REPO:") || strings.Contains(body, "--repo") {
		return
	}
	t.Fatalf("promote-release.yml runs `gh release create` with no actions/checkout, no GH_REPO env and no --repo flag: gh cannot resolve the base repository, so publication fails after the tag ref is already created")
}

// The candidate workflow must never be able to publish. Its only write-shaped
// capability would be a contents:write token, a git ref POST, or a release
// creation; none may appear.
func TestCandidateWorkflowHasNoPublicationPath(t *testing.T) {
	body := workflow(t, "release.yml")
	for _, forbidden := range []string{"contents: write", "git/refs", "gh release create"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("release.yml must have no publication path, found %q", forbidden)
		}
	}
	if !strings.Contains(body, "permissions:\n  contents: read") {
		t.Fatalf("release.yml must declare a top-level read-only token")
	}
}

// Promotion must create the tag atomically and only then publish, and it must
// verify the tag it publishes against.
func TestPromoteWorkflowCreatesTagBeforeVerifiedRelease(t *testing.T) {
	body := workflow(t, "promote-release.yml")
	refPost := strings.Index(body, "git/refs")
	publish := strings.Index(body, "gh release create")
	if refPost < 0 || publish < 0 {
		t.Fatalf("promote-release.yml must both create refs/tags and create a release")
	}
	if refPost > publish {
		t.Fatalf("promote-release.yml publishes before creating the tag ref")
	}
	if !strings.Contains(body, "--verify-tag") {
		t.Fatalf("promote-release.yml must publish with --verify-tag")
	}
}
