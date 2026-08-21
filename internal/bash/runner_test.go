package bash

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/icediceice/light-tools/internal/secret"
)

func shellSource(posix, powershell string) string {
	if runtime.GOOS == "windows" {
		return powershell
	}
	return posix
}


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
		Command: shellSource("printf '%s' \"$TOKEN\"", "[Console]::Out.Write($env:TOKEN)"), Cwd: root,
		EnvRefs: map[string]string{"TOKEN": "token"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result["stdout"] != "[REDACTED]" || strings.Contains(result["stdout"].(string), "top-secret-value") {
		t.Fatalf("environment secret leaked: %#v", result)
	}

	result, err = runner.Run(context.Background(), Request{
		Command: shellSource("value=$(cat \"$TOKEN_FILE\"); printf '%s' \"$value\"", "[Console]::Out.Write((Get-Content -Raw $env:TOKEN_FILE))"), Cwd: root,
		FileRefs: map[string]string{"TOKEN_FILE": "token"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result["stdout"] != "[REDACTED]" {
		t.Fatalf("file secret leaked: %#v", result)
	}
}

func TestContainsFilenameWildcard(t *testing.T) {
	cases := []struct {
		name    string
		command string
		goos    string
		want    bool
	}{
		{name: "star", command: "rm *.tmp", goos: "linux", want: true},
		{name: "question marks", command: "rm ??", goos: "linux", want: true},
		{name: "path segment", command: "rm build/*", goos: "linux", want: true},
		{name: "single quoted literal", command: "printf '%s' '*.tmp'", goos: "linux", want: false},
		{name: "double quoted literal", command: "printf \"%s\" \"*.tmp\"", goos: "linux", want: false},
		{name: "escaped literal", command: "printf %s \\*", goos: "linux", want: false},
		{name: "explicit files", command: "rm a.tmp b.tmp", goos: "linux", want: false},
		{name: "url query", command: "curl https://example.test/?a=1", goos: "linux", want: false},
		{name: "assignment", command: "PATTERN=*.tmp printf ok", goos: "linux", want: false},
		{name: "powershell provider wildcard", command: "Remove-Item \"*.tmp\"", goos: "windows", want: true},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if got := containsFilenameWildcard(test.command, test.goos); got != test.want {
				t.Fatalf("containsFilenameWildcard(%q, %q) = %v, want %v", test.command, test.goos, got, test.want)
			}
		})
	}
}

func TestWildcardRequestPreviewsThenExecutesIdenticalRetry(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"a.tmp", "b.tmp"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	runner, err := NewRunner([]string{root}, filepath.Join(root, "spills"), secret.New(filepath.Join(root, "secrets")))
	if err != nil {
		t.Fatal(err)
	}
	request := Request{Command: "rm *.tmp", Cwd: root}
	first, err := runner.Run(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if first["wildcard_preview"] != true || first["dry_run"] != true {
		t.Fatalf("first request was not a no-execution preview: %#v", first)
	}
	if _, err := os.Stat(filepath.Join(root, "a.tmp")); err != nil {
		t.Fatalf("preview executed command: %v", err)
	}

	second, err := runner.Run(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if second["exit_code"] != 0 {
		t.Fatalf("retry failed: %#v", second)
	}
	if _, err := os.Stat(filepath.Join(root, "a.tmp")); !os.IsNotExist(err) {
		t.Fatalf("identical retry did not execute: %v", err)
	}

	if err := os.WriteFile(filepath.Join(root, "named-a"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "named-b"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	explicit, err := runner.Run(context.Background(), Request{Command: "rm named-a named-b", Cwd: root})
	if err != nil || explicit["wildcard_preview"] == true {
		t.Fatalf("explicit filenames were guarded: result=%#v err=%v", explicit, err)
	}
}

func TestWildcardReceiptKeysWholeRequestAndFailsSafe(t *testing.T) {
	root := t.TempDir()
	runner, err := NewRunner([]string{root}, filepath.Join(root, "spills"), secret.New(filepath.Join(root, "secrets")))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(100, 0)
	base := Request{Command: "printf '%s' *", Cwd: root}
	if _, guarded, err := runner.guardWildcardRequest(base, "linux", now); err != nil || !guarded {
		t.Fatalf("first request: guarded=%v err=%v", guarded, err)
	}
	changed := base
	changed.TimeoutMS = 1
	if _, guarded, err := runner.guardWildcardRequest(changed, "linux", now); err != nil || !guarded {
		t.Fatalf("changed request consumed receipt: guarded=%v err=%v", guarded, err)
	}
	if _, guarded, err := runner.guardWildcardRequest(base, "linux", now.Add(wildcardReceiptTTL+time.Second)); err != nil || !guarded {
		t.Fatalf("expired receipt did not re-preview: guarded=%v err=%v", guarded, err)
	}

	runner.wildcardMu.Lock()
	runner.wildcardReceipts = make(map[[32]byte]time.Time)
	runner.wildcardMu.Unlock()
	for index := 0; index < wildcardReceiptCap; index++ {
		request := Request{Command: fmt.Sprintf("printf %%s file-%d-*", index), Cwd: root}
		insertedAt := now.Add(time.Duration(index) * time.Second)
		if _, guarded, err := runner.guardWildcardRequest(request, "linux", insertedAt); err != nil || !guarded {
			t.Fatalf("cap fill %d: guarded=%v err=%v", index, guarded, err)
		}
	}
	overflow := Request{Command: "printf %s file-overflow-*", Cwd: root}
	if _, guarded, err := runner.guardWildcardRequest(overflow, "linux", now.Add(wildcardReceiptCap*time.Second)); err != nil || !guarded {
		t.Fatalf("overflow request: guarded=%v err=%v", guarded, err)
	}
	evicted := Request{Command: "printf %s file-0-*", Cwd: root}
	if _, guarded, err := runner.guardWildcardRequest(evicted, "linux", now); err != nil || !guarded {
		t.Fatalf("evicted receipt did not fail safe: guarded=%v err=%v", guarded, err)
	}
}

func TestWildcardAsyncPreviewPrecedesTaskCreation(t *testing.T) {
	root := t.TempDir()
	runner, err := NewRunner([]string{root}, filepath.Join(root, "spills"), secret.New(filepath.Join(root, "secrets")))
	if err != nil {
		t.Fatal(err)
	}
	request := Request{Command: "printf '%s' *", Cwd: root, Async: true}
	first, err := runner.Run(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if first["wildcard_preview"] != true {
		t.Fatalf("first async request queued instead of previewing: %#v", first)
	}
	second, err := runner.Run(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if second["task_id"] == nil || second["status"] != "queued" {
		t.Fatalf("identical async retry did not queue: %#v", second)
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
