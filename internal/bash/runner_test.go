package bash

import (
	"context"
	"fmt"
	"os"
	"os/exec"
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

func TestFilterEnvironmentNeverReturnsNil(t *testing.T) {
	for _, goos := range []string{"windows", "linux", "darwin"} {
		t.Run(goos, func(t *testing.T) {
			if got := filterEnvironment(nil, goos); got == nil {
				t.Fatal("filterEnvironment returned nil; assigning nil to exec.Cmd.Env inherits the full parent environment")
			}
		})
	}
}

func TestFilterEnvironmentWindowsFoldsCaseAndStaysMinimal(t *testing.T) {
	expected := []string{
		`Path=C:\Program Files\Go\bin;C:\Users\a b\bin`,
		`PATHEXT=.COM;.EXE;.BAT;.CMD`,
		`SystemRoot=C:\Windows`,
		`windir=C:\Windows`,
		`ComSpec=C:\Windows\System32\cmd.exe`,
		`USERPROFILE=C:\Users\runner`,
		`APPDATA=C:\Users\runner\AppData\Roaming`,
		`LOCALAPPDATA=C:\Users\runner\AppData\Local`,
		`SystemDrive=C:`,
		`ProgramData=C:\ProgramData`,
		`PSModulePath=C:\Windows\System32\WindowsPowerShell\v1.0\Modules`,
		`TEMP=C:\Users\runner\AppData\Local\Temp`,
		`TMP=C:\Users\runner\AppData\Local\Temp`,
	}
	entries := append([]string{}, expected...)
	entries = append(entries, `AWS_SECRET_ACCESS_KEY=must-not-leak`, `=C:=C:\work`)

	got := filterEnvironment(entries, "windows")
	for _, want := range expected {
		if !containsEnvironmentEntry(got, want) {
			t.Fatalf("allowed Windows entry was not preserved verbatim: want=%q got=%#v", want, got)
		}
	}
	if len(got) != len(expected) {
		t.Fatalf("Windows filter retained entries outside the allowlist: %#v", got)
	}
	for _, item := range got {
		name, _, ok := strings.Cut(item, "=")
		if !ok || name == "" {
			t.Fatalf("invalid or per-drive environment entry survived: %q", item)
		}
		if strings.EqualFold(name, "AWS_SECRET_ACCESS_KEY") {
			t.Fatalf("unauthorized environment variable survived: %q", item)
		}
	}
}

func TestFilterEnvironmentPosixStaysExactCase(t *testing.T) {
	got := filterEnvironment([]string{"Path=/nope", "PATH=/usr/bin", "HOME=/home/runner", "SECRET=x"}, "linux")
	if !containsEnvironmentEntry(got, "PATH=/usr/bin") || !containsEnvironmentEntry(got, "HOME=/home/runner") {
		t.Fatalf("allowed POSIX entries missing: %#v", got)
	}
	if containsEnvironmentEntry(got, "Path=/nope") || containsEnvironmentEntry(got, "SECRET=x") {
		t.Fatalf("POSIX filtering became case-insensitive or leaked a secret: %#v", got)
	}
}

func TestFilterEnvironmentSuppliesPATHEXTDefault(t *testing.T) {
	got := filterEnvironment([]string{`Path=C:\Go\bin`}, "windows")
	if !containsEnvironmentEntry(got, "PATHEXT=.COM;.EXE;.BAT;.CMD") {
		t.Fatalf("PATHEXT default missing: %#v", got)
	}
}

func TestRunnerResolvesExternalCommandAndKeepsEnvironmentMinimal(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		if os.Getenv("CI") != "" {
			t.Fatalf("go must be on PATH under CI: this test is the only native Windows evidence for the light_bash environment boundary: %v", err)
		}
		t.Skip("go is not available on the parent PATH")
	}
	t.Setenv("LIGHT_TOOLS_BOUNDARY_MARKER", "must-not-leak")

	root := t.TempDir()
	runner, err := NewRunner([]string{root}, filepath.Join(root, "spills"), secret.New(filepath.Join(root, "secrets")))
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(context.Background(), Request{
		Command: shellSource(
			`go env GOCACHE; printf '%s' "$LIGHT_TOOLS_BOUNDARY_MARKER"`,
			`& go env GOCACHE; [Console]::Out.Write([string]$env:LIGHT_TOOLS_BOUNDARY_MARKER)`,
		),
		Cwd: root,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result["exit_code"] != 0 {
		t.Fatalf("external command failed: %#v", result)
	}
	stdout, _ := result["stdout"].(string)
	if strings.TrimSpace(stdout) == "" {
		t.Fatalf("go env GOCACHE returned no cache path: %#v", result)
	}
	if strings.Contains(stdout, "must-not-leak") {
		t.Fatalf("child inherited a non-allowlisted parent variable: %#v", result)
	}
}

func containsEnvironmentEntry(entries []string, want string) bool {
	for _, entry := range entries {
		if entry == want {
			return true
		}
	}
	return false
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
