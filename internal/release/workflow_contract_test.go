// Package release holds test-only contract guards for the candidate/promote
// release lifecycle. There is no production code here on purpose: the artifacts
// under review are workflow files, and these tests are the only mechanical
// re-check that their load-bearing invariants survive future edits.
package release

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
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

// ci.yml's MCP transcript step asserts the registration surface, and until now
// nothing re-checked that assertion against the server's own toolNames. The two
// drifted once already: the step still demanded the single-tool posture of the
// retired --enable-shell/--enable-remote/--enable-ops era long after every tool
// became registered by default, so CI failed against a correct server. Read the
// names out of main.go rather than restating them here, so a sixth tool fails
// loudly instead of being silently under-asserted.
func TestCiWorkflowAssertsFullToolRegistration(t *testing.T) {
	names := registeredToolNames(t)
	body := workflow(t, "ci.yml")
	// EVERY step that reads tools/list must assert the WHOLE surface. Bounding
	// this guard to the transcript step alone is exactly how the inspector job
	// kept `n.length!==1||n[0]!=='light_file'` long after the transcript step was
	// fixed — a guard narrower than the invariant it claims to protect.
	for _, step := range []string{
		"MCP initialize and tools/list transcript",
		"MCP Inspector tools/list smoke",
	} {
		start := strings.Index(body, step)
		if start < 0 {
			t.Fatalf("ci.yml no longer runs %q; update this contract test", step)
		}
		// Stop at the next step or the next job, so a later one cannot satisfy this.
		assertion := body[start+len(step):]
		if next := regexp.MustCompile(`(?m)^      - name: |^  [a-z][a-z0-9_-]*:[ \t]*$`).FindStringIndex(assertion); next != nil {
			assertion = assertion[:next[0]]
		}
		for _, name := range names {
			if !strings.Contains(assertion, strconv.Quote(name)) {
				t.Fatalf("ci.yml step %q never names %q: it and cmd/light-tools/main.go toolNames have drifted", step, name)
			}
		}
	}
}

// registeredToolNames reads the registration surface out of main.go rather than
// restating it here, so adding a sixth tool fails these guards loudly instead of
// leaving them silently under-asserting.
func registeredToolNames(t *testing.T) []string {
	t.Helper()
	source, err := os.ReadFile(filepath.Join("..", "..", "cmd", "light-tools", "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	declaration := regexp.MustCompile(`var toolNames = \[\]string\{([^}]*)\}`).FindSubmatch(source)
	if declaration == nil {
		t.Fatalf("cmd/light-tools/main.go no longer declares a toolNames slice; update this contract test")
	}
	matches := regexp.MustCompile(`"([^"]+)"`).FindAllStringSubmatch(string(declaration[1]), -1)
	if len(matches) == 0 {
		t.Fatalf("toolNames declaration parsed to zero names; update this contract test")
	}
	names := make([]string, 0, len(matches))
	for _, match := range matches {
		names = append(names, match[1])
	}
	return names
}

// The same assertion drifted in THREE places, not one. Both release smokes also
// encoded the retired single-tool posture: scripts/mcp-smoke.ps1 asserted a
// one-element tools/list, and scripts/npm-package-smoke.mjs compared only
// tools[0].name, so a correct server failed two further CI jobs that the ci.yml
// fix never touched. Guard every call site — a guard covering one third of the
// surface is worse than none, because the next reader trusts it.
func TestReleaseSmokesAssertFullToolRegistration(t *testing.T) {
	names := registeredToolNames(t)
	for _, script := range []string{"mcp-smoke.ps1", "npm-package-smoke.mjs"} {
		raw, err := os.ReadFile(filepath.Join("..", "..", "scripts", script))
		if err != nil {
			t.Fatal(err)
		}
		body := strings.ReplaceAll(string(raw), "\r\n", "\n")
		for _, name := range names {
			if !strings.Contains(body, strconv.Quote(name)) {
				t.Fatalf("scripts/%s never names %q as a quoted tool: it and cmd/light-tools/main.go toolNames have drifted", script, name)
			}
		}
	}
}

// A merge can append a job that ALREADY exists: git sees two non-overlapping
// insertions and keeps both copies, yaml.safe_load-style parsers silently take
// the last one, and only GitHub rejects the file — as a startup failure with
// zero jobs, no job logs, and the single line "This run likely failed because
// of a workflow file issue". That shipped once, from a duplicated race job.
// Duplicate keys are trivial to detect here and miserable to diagnose there.
func TestWorkflowsDeclareNoDuplicateJobKeys(t *testing.T) {
	for _, name := range []string{"ci.yml", "release.yml", "promote-release.yml"} {
		body := workflow(t, name)
		start := strings.Index(body, "\njobs:\n")
		if start < 0 {
			t.Fatalf("%s declares no jobs block; update this contract test", name)
		}
		// Job names are the only two-space keys with no inline value inside the
		// jobs block; everything a job itself declares is indented further.
		counts := map[string]int{}
		for _, match := range regexp.MustCompile(`(?m)^  ([a-z][a-z0-9_-]*):[ \t]*$`).FindAllStringSubmatch(body[start:], -1) {
			counts[match[1]]++
		}
		if len(counts) == 0 {
			t.Fatalf("%s jobs block parsed to zero job keys; update this contract test", name)
		}
		for job, count := range counts {
			if count > 1 {
				t.Fatalf("%s declares job %q %d times: GitHub refuses the whole workflow, so every check silently disappears rather than failing", name, job, count)
			}
		}
	}
}
