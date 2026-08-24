package bash

import (
	"context"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/icediceice/light-tools/internal/secret"
)

// scrub runs on the stream boundedBuffer has ALREADY capped, so a replacement
// longer than the secret it replaces defeats the memory bound: the retained
// bytes grow again, after the ceiling that was supposed to hold them.
func TestRedactionNeverExpandsTheStreamItScrubs(t *testing.T) {
	for _, secretValue := range []string{"x", "ab", "short", "123456789", "0123456789", "a-long-enough-secret"} {
		if got := len(redactionFor(secretValue)); got > len(secretValue) {
			t.Fatalf("secret %q of %d bytes redacts to %d bytes — the replacement expands the stream",
				secretValue, len(secretValue), got)
		}
	}
	// The readable marker must survive for secrets that can carry it.
	if got := redactionFor("0123456789"); got != redactionMarker {
		t.Fatalf("a secret at marker length redacted to %q, want %q", got, redactionMarker)
	}
	// Non-expansion has to hold for the whole scrub, not just one replacement.
	before := strings.Repeat("x", 4096)
	if after := scrub(before, []string{"x"}); len(after) > len(before) {
		t.Fatalf("scrub grew %d bytes to %d", len(before), len(after))
	}
	if strings.Contains(scrub(before, []string{"x"}), "x") {
		t.Fatal("the secret survived redaction")
	}
}

// End to end: a runaway command whose output is saturated with a SHORT
// referenced secret must still honour the capture limit. Before redaction was
// made non-expanding a one-byte secret inflated the retained stream tenfold
// past the cap, and a large enough run then overran the spill ceiling and
// failed the call instead of returning the capped result.
func TestShortSecretCannotInflateCappedOutputPastTheLimit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell loop to generate output")
	}
	root := t.TempDir()
	vault := secret.New(filepath.Join(root, "secrets"))
	if err := vault.Set("pin", "x"); err != nil {
		t.Fatal(err)
	}
	runner, err := NewRunner(testPolicy(root), filepath.Join(root, "spills"), vault, nil)
	if err != nil {
		t.Fatal(err)
	}
	const limit = 512
	runner.captureLimit = limit

	result, err := runner.Run(context.Background(), Request{
		// 4000 one-byte writes against a 512-byte ceiling. Pre-fix the retained
		// 512 bytes redacted to 5120, eight times the cap it had just passed.
		Command: `for i in $(seq 1 4000); do printf '%s' "$TOKEN"; done`,
		Cwd:     root,
		EnvRefs: map[string]string{"TOKEN": "pin"},
	})
	if err != nil {
		t.Fatalf("a capped command carrying a secret must still return a result: %v", err)
	}
	if result["output_capped"] != true {
		t.Fatalf("output outran the limit but was not reported as capped: %#v", result)
	}
	stdout, _ := result["stdout"].(string)
	if len(stdout) > limit {
		t.Fatalf("redaction re-inflated the retained stream to %d bytes past the %d-byte cap", len(stdout), limit)
	}
	if strings.Contains(stdout, "x") {
		t.Fatalf("the secret survived redaction in capped output: %q", stdout)
	}
}
