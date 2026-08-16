package bash

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/icediceice/light-tools/internal/secret"
)

func TestSecretRefsAreResolvedAndScrubbed(t *testing.T) {
	root := t.TempDir()
	vault := secret.New(filepath.Join(root, "secrets"))
	if err := vault.Set("token", "top-secret-value"); err != nil {
		t.Fatal(err)
	}
	runner, err := NewRunner([]string{root}, filepath.Join(root, "spills"), vault)
	if err != nil {
		t.Fatal(err)
	}

	result, err := runner.Run(context.Background(), Request{
		Command: "printf '%s' \"$TOKEN\"", Cwd: root,
		EnvRefs: map[string]string{"TOKEN": "token"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result["stdout"] != "[REDACTED]" || strings.Contains(result["stdout"].(string), "top-secret-value") {
		t.Fatalf("environment secret leaked: %#v", result)
	}

	result, err = runner.Run(context.Background(), Request{
		Command: "value=$(cat \"$TOKEN_FILE\"); printf '%s' \"$value\"", Cwd: root,
		FileRefs: map[string]string{"TOKEN_FILE": "token"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result["stdout"] != "[REDACTED]" {
		t.Fatalf("file secret leaked: %#v", result)
	}
}

func TestGoModuleErrorAnnotation(t *testing.T) {
	got := annotateGoModuleError("missing go.sum entry for module")
	if !strings.Contains(got, "run go mod tidy") {
		t.Fatalf("missing dependency annotation: %s", got)
	}
	if got := annotateGoModuleError("ordinary error"); got != "ordinary error" {
		t.Fatalf("ordinary error changed: %s", got)
	}
}
