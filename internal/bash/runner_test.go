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

	"github.com/icediceice/light-tools/internal/secret"
	"github.com/icediceice/light-tools/internal/snapshot"
)

func shellSource(posix, powershell string) string {
	if runtime.GOOS == "windows" {
		return powershell
	}
	return posix
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
	runner, err := NewRunner([]string{root}, filepath.Join(root, "spills"), secret.New(filepath.Join(root, "secrets")), snapshot.New(filepath.Join(root, "snapshots")))
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

func TestSecretRefsAreResolvedAndScrubbed(t *testing.T) {
	root := t.TempDir()
	vault := secret.New(filepath.Join(root, "secrets"))
	if err := vault.Set("token", "top-secret-value"); err != nil {
		t.Fatal(err)
	}
	runner, err := NewRunner([]string{root}, filepath.Join(root, "spills"), vault, snapshot.New(filepath.Join(root, "snapshots")))
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

// The defect this whole lane exists to fix: a protectable glob used to cost two
// calls and hand back neither a path list nor a revert. It must now run on the
// FIRST call, and the capture it returns must actually restore.
func TestProtectableGlobRunsOnFirstContactAndReverts(t *testing.T) {
	runner, vault, root := newGuardRunner(t)
	for _, name := range []string{"a.tmp", "b.tmp"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	result, err := runner.Run(context.Background(), Request{Command: "rm *.tmp", Cwd: root})
	if err != nil {
		t.Fatal(err)
	}
	if result["protection"] != "capture-backed" {
		t.Fatalf("protectable surface was not capture-backed: %#v", result)
	}
	captureID, _ := result["capture_id"].(string)
	if captureID == "" {
		t.Fatalf("no capture_id on a capture-backed run: %#v", result)
	}
	for _, name := range []string{"a.tmp", "b.tmp"} {
		if _, err := os.Stat(filepath.Join(root, name)); !os.IsNotExist(err) {
			// Report what the shell actually did. A bare "still present" sent
			// this to CI three times without saying whether the command failed,
			// never ran, or ran against the wrong paths.
			t.Fatalf("the command did not run on first contact: %s still present (%v)\n  exit_code=%v\n  stdout=%q\n  stderr=%q",
				name, err, result["exit_code"], result["stdout"], result["stderr"])
		}
	}

	results, err := vault.RestoreCapture(captureID, false)
	if err != nil {
		t.Fatalf("revert failed: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("revert covered %d paths, want 2: %#v", len(results), results)
	}
	for _, name := range []string{"a.tmp", "b.tmp"} {
		data, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatalf("revert did not restore %s: %v", name, err)
		}
		if string(data) != name {
			t.Fatalf("revert restored wrong bytes for %s: %q", name, data)
		}
	}
}

// The pin is POSIX shell syntax, and PowerShell parses a leading quoted word as
// a string literal rather than a command. Pinning on Windows therefore turned a
// captured mutation into a silent no-op: the capture succeeded, the caller was
// told the surface was protected, and the files were still there. The capture
// still happens on Windows; only the rewrite is withheld.
func TestPinIsWithheldOnWindowsWherePowerShellCannotRunIt(t *testing.T) {
	plan, ok := planGlobMutation("rm *.tmp")
	if !ok {
		t.Fatal("rm *.tmp must be recognised as a modeled glob mutation")
	}
	groups := [][]surfaceEntry{{{Path: "/tmp/a.tmp"}, {Path: "/tmp/b.tmp"}}}
	pinned := pinCommand(plan, groups)

	if runtime.GOOS == "windows" {
		if pinned != "" {
			t.Fatalf("Windows must not pin; PowerShell cannot invoke %q as a command", pinned)
		}
		return
	}
	if !strings.Contains(pinned, "'/tmp/a.tmp'") || !strings.Contains(pinned, "'/tmp/b.tmp'") {
		t.Fatalf("the POSIX pin dropped its captured paths: %q", pinned)
	}
}

// Pinning rewrites the command to name the captured paths literally, so the
// quoting has to survive the shell exactly. A path carrying a space or an
// apostrophe is where a naive rewrite stops being a no-op and starts deleting
// something the caller never named.
func TestPinnedSurfaceSurvivesAwkwardFilenames(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX quoting rules")
	}
	runner, vault, root := newGuardRunner(t)
	names := []string{"plain.tmp", "with space.tmp", "it's.tmp", "dollar$sign.tmp"}
	for _, name := range names {
		if err := os.WriteFile(filepath.Join(root, name), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	bystander := filepath.Join(root, "keep.txt")
	if err := os.WriteFile(bystander, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(context.Background(), Request{Command: "rm *.tmp", Cwd: root})
	if err != nil {
		t.Fatal(err)
	}
	if result["protection"] != "capture-backed" {
		t.Fatalf("awkward but ordinary filenames left the protected lane: %#v", result)
	}
	for _, name := range names {
		if _, err := os.Stat(filepath.Join(root, name)); !os.IsNotExist(err) {
			t.Fatalf("pinned command did not remove %q: %v", name, err)
		}
	}
	if _, err := os.Stat(bystander); err != nil {
		t.Fatalf("pinned command reached a path outside the captured surface: %v", err)
	}
	captureID, _ := result["capture_id"].(string)
	if _, err := vault.RestoreCapture(captureID, false); err != nil {
		t.Fatalf("revert failed: %v", err)
	}
	for _, name := range names {
		data, err := os.ReadFile(filepath.Join(root, name))
		if err != nil || string(data) != name {
			t.Fatalf("revert did not restore %q: data=%q err=%v", name, data, err)
		}
	}
}

// A directory cannot be quarantined for rm — the coreutils call would fail on
// it, and substituting success would change the command's own outcome. So the
// surface is not protectable, nothing runs, and the reason names the blocker.
func TestDirectoryInSurfaceRefusesWithoutRunning(t *testing.T) {
	runner, _, root := newGuardRunner(t)
	if err := os.WriteFile(filepath.Join(root, "keep.txt"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(context.Background(), Request{Command: "rm *", Cwd: root})
	if err != nil {
		t.Fatal(err)
	}
	if result["protection"] != "refused" {
		t.Fatalf("unprotectable surface was not refused: %#v", result)
	}
	if result["capture_id"] != nil {
		t.Fatalf("a refused call must not mint a capture: %#v", result)
	}
	reason, _ := result["reason"].(string)
	if !strings.Contains(reason, "nested") || !strings.Contains(reason, "directory") {
		t.Fatalf("reason did not name the blocking directory: %q", reason)
	}
	if _, err := os.Stat(filepath.Join(root, "keep.txt")); err != nil {
		t.Fatalf("a refused call ran anyway: %v", err)
	}
	if result["surface_digest"] == "" || result["surface_digest"] == nil {
		t.Fatalf("refusal carried no digest to confirm with: %#v", result)
	}
}

// Confirming an unprotectable surface runs it, but the terminal must say the
// run has no revert. "ran" and "ran revertibly" are different facts.
func TestConfirmedUnprotectableRunIsLabelledUnbacked(t *testing.T) {
	runner, _, root := newGuardRunner(t)
	if err := os.WriteFile(filepath.Join(root, "keep.txt"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	request := Request{Command: "sed -i s/a/b/ *", Cwd: root}
	refusal, err := runner.Run(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	digest, _ := refusal["surface_digest"].(string)
	if digest == "" {
		t.Fatalf("no digest to confirm: %#v", refusal)
	}

	request.Confirm = digest
	confirmed, err := runner.Run(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if confirmed["protection"] != "unprotected_confirmed" {
		t.Fatalf("confirmed run was not labelled unbacked: %#v", confirmed)
	}
	if confirmed["capture_id"] != nil {
		t.Fatalf("an unbacked run must not advertise a capture: %#v", confirmed)
	}
}

// A confirm carried forward onto a different surface must not succeed by
// repetition: the digest binds one enumerated surface and nothing else.
func TestStaleConfirmIsRefusedAndNamesBothSurfaces(t *testing.T) {
	runner, _, root := newGuardRunner(t)
	if err := os.Mkdir(filepath.Join(root, "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "one.txt"), []byte("one"), 0o600); err != nil {
		t.Fatal(err)
	}
	first, err := runner.Run(context.Background(), Request{Command: "rm *", Cwd: root})
	if err != nil {
		t.Fatal(err)
	}
	stale, _ := first["surface_digest"].(string)

	if err := os.WriteFile(filepath.Join(root, "two.txt"), []byte("two"), 0o600); err != nil {
		t.Fatal(err)
	}
	second, err := runner.Run(context.Background(), Request{Command: "rm *", Cwd: root, Confirm: stale})
	if err != nil {
		t.Fatal(err)
	}
	if second["protection"] != "refused" {
		t.Fatalf("a stale confirm was accepted: %#v", second)
	}
	fresh, _ := second["surface_digest"].(string)
	if fresh == "" || fresh == stale {
		t.Fatalf("the digest did not change with the surface: stale=%q fresh=%q", stale, fresh)
	}
}

// Only an UNQUOTED glob on a modeled mutator enters the lane. A quoted pattern
// is a literal, explicit filenames need no enumeration, and an unmodeled
// command was never this guard's business.
func TestUnguardedShapesPassStraightThrough(t *testing.T) {
	runner, _, root := newGuardRunner(t)
	for _, name := range []string{"named-a", "named-b"} {
		if err := os.WriteFile(filepath.Join(root, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	explicit, err := runner.Run(context.Background(), Request{Command: "rm named-a named-b", Cwd: root})
	if err != nil {
		t.Fatal(err)
	}
	if explicit["protection"] != nil {
		t.Fatalf("explicit filenames were guarded: %#v", explicit)
	}

	quoted, err := runner.Run(context.Background(), Request{
		Command: shellSource("printf '%s' '*.tmp'", "[Console]::Out.Write('*.tmp')"), Cwd: root,
	})
	if err != nil {
		t.Fatal(err)
	}
	if quoted["protection"] != nil {
		t.Fatalf("a quoted literal was guarded: %#v", quoted)
	}
}

// A pipeline's surface cannot be honestly enumerated, so it never claims to be
// capture-backed even though it contains a modeled mutator and a glob.
func TestPipelineIsNeverClaimedAsProtected(t *testing.T) {
	runner, _, root := newGuardRunner(t)
	if err := os.WriteFile(filepath.Join(root, "a.tmp"), []byte("a"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(context.Background(), Request{
		Command: shellSource("printf '%s' ok | tee out.txt", "'ok' | Tee-Object out.txt"), Cwd: root,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result["protection"] != nil {
		t.Fatalf("a pipeline was given a protection verdict: %#v", result)
	}
}

// The async lane captures BEFORE it queues, so a task that starts running
// immediately is already backed.
func TestAsyncProtectedGlobCapturesBeforeQueueing(t *testing.T) {
	runner, _, root := newGuardRunner(t)
	if err := os.WriteFile(filepath.Join(root, "a.tmp"), []byte("a"), 0o600); err != nil {
		t.Fatal(err)
	}
	queued, err := runner.Run(context.Background(), Request{Command: "rm *.tmp", Cwd: root, Async: true})
	if err != nil {
		t.Fatal(err)
	}
	if queued["task_id"] == nil || queued["status"] != "queued" {
		t.Fatalf("async protected glob did not queue: %#v", queued)
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

func TestAcceptanceConfirmCannotAuthorizeChangedCommand(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("acceptance case uses POSIX rm flags")
	}
	runner, _, root := newGuardRunner(t)
	if err := os.WriteFile(filepath.Join(root, "keep.txt"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	preview, err := runner.Run(context.Background(), Request{Command: "rm *", Cwd: root})
	if err != nil {
		t.Fatal(err)
	}
	digest, _ := preview["surface_digest"].(string)
	changed, err := runner.Run(context.Background(), Request{Command: "rm -rf *", Cwd: root, Confirm: digest})
	if err != nil {
		t.Fatal(err)
	}
	if changed["protection"] != "refused" {
		t.Fatalf("confirm for a different command was accepted: %#v", changed)
	}
	if _, err := os.Stat(filepath.Join(root, "keep.txt")); err != nil {
		t.Fatalf("changed command ran under stale authorization: %v", err)
	}
}

func TestAcceptanceRefusalReturnsCompleteSurface(t *testing.T) {
	runner, _, root := newGuardRunner(t)
	if err := os.Mkdir(filepath.Join(root, "blocker"), 0o700); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 25; index++ {
		name := filepath.Join(root, fmt.Sprintf("item-%02d.tmp", index))
		if err := os.WriteFile(name, []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	result, err := runner.Run(context.Background(), Request{Command: "rm *", Cwd: root})
	if err != nil {
		t.Fatal(err)
	}
	surface, _ := result["surface"].([]string)
	count, _ := result["surface_count"].(int)
	if len(surface) != count {
		t.Fatalf("surface was truncated: returned=%d expanded=%d surface=%v", len(surface), count, surface)
	}
}

func TestAcceptanceCapturedSurfaceCannotDriftBeforeExecution(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("acceptance case uses POSIX rm")
	}
	runner, vault, root := newGuardRunner(t)
	first := filepath.Join(root, "first.tmp")
	second := filepath.Join(root, "second.tmp")
	if err := os.WriteFile(first, []byte("first"), 0o600); err != nil {
		t.Fatal(err)
	}
	request := Request{Command: "rm *.tmp", Cwd: root}
	guard, refusal, err := runner.prepareGlobGuard(request)
	if err != nil || refusal != nil || guard == nil {
		t.Fatalf("guard preparation failed: guard=%#v refusal=%#v err=%v", guard, refusal, err)
	}
	if err := os.WriteFile(second, []byte("second"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, runErr := runner.runSync(context.Background(), request, guard)
	result, runErr = runner.sealGuard(guard, result, runErr)
	if runErr != nil || result["protection"] != "capture-backed" {
		t.Fatalf("protected run failed: result=%#v err=%v", result, runErr)
	}
	if _, err := vault.RestoreCapture(guard.CaptureID, false); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(second); err != nil || string(data) != "second" {
		t.Fatalf("path added after capture was deleted outside the revert surface: data=%q err=%v", data, err)
	}
}
