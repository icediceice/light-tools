package bash

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/icediceice/light-tools/internal/childenv"
	"github.com/icediceice/light-tools/internal/secret"
	"github.com/icediceice/light-tools/internal/security"
	"github.com/icediceice/light-tools/internal/snapshot"
)

const outputLimit = 128 * 1024

type Request struct {
	Verb       string            `json:"verb,omitempty"`
	TaskID     string            `json:"task_id,omitempty"`
	Async      bool              `json:"async,omitempty"`
	Command    string            `json:"command,omitempty"`
	Cwd        string            `json:"cwd,omitempty"`
	TimeoutMS  int               `json:"timeout_ms,omitempty"`
	OutputMode string            `json:"output_mode,omitempty"`
	Lines      int               `json:"lines,omitempty"`
	Filter     string            `json:"filter,omitempty"`
	SpillID    string            `json:"spill_id,omitempty"`
	Spill      string            `json:"spill,omitempty"`
	LineRange  string            `json:"line_range,omitempty"`
	EnvRefs    map[string]string `json:"env_refs,omitempty"`
	FileRefs   map[string]string `json:"file_refs,omitempty"`
	// Confirm acknowledges one specific unprotectable surface by its digest.
	// It is never needed for a surface the capture lane can back.
	Confirm string `json:"confirm,omitempty"`
}

type Runner struct {
	confiner *security.Confiner
	spills   *SpillStore
	secrets  *secret.Vault
	tasks    *TaskManager
	// captures backs glob mutators with a revertible snapshot of their whole
	// surface. Nil disables the protected lane, which leaves every modeled
	// glob on the confirm fence rather than running it unbacked.
	captures *snapshot.Vault
	// captureLimit is the per-stream in-memory ceiling for command output.
	// Zero means captureLimit. It is a field rather than a bare constant so a
	// test can drive the truncation path without generating 24 MiB.
	captureLimit int
}

func NewRunner(roots []string, spillRoot string, secrets *secret.Vault, captures *snapshot.Vault) (*Runner, error) {
	spills, err := NewSpillStore(spillRoot, time.Hour)
	if err != nil {
		return nil, err
	}
	confiner, err := security.NewConfiner(roots, nil)
	if err != nil {
		return nil, err
	}
	return &Runner{confiner: confiner, spills: spills, secrets: secrets, tasks: NewTaskManager(), captures: captures}, nil
}

func (r *Runner) Run(ctx context.Context, request Request) (map[string]any, error) {
	switch request.Verb {
	case "status":
		return r.tasks.Status(request.TaskID)
	case "collect":
		return r.tasks.Collect(request.TaskID)
	case "cancel":
		return r.tasks.Cancel(request.TaskID)
	}
	guard, refusal, err := r.prepareGlobGuard(request)
	if err != nil {
		return nil, err
	}
	if refusal != nil {
		return refusal, nil
	}
	if request.Async {
		request.Async = false
		id, err := r.tasks.Start(func(taskContext context.Context) (map[string]any, error) {
			result, err := r.runSync(taskContext, request, guard)
			return r.sealGuard(guard, result, err)
		})
		if err != nil {
			return nil, err
		}
		return map[string]any{"task_id": id, "status": "queued"}, nil
	}
	result, err := r.runSync(ctx, request, guard)
	return r.sealGuard(guard, result, err)
}

// globGuard is the decision carried from admission through to the terminal.
type globGuard struct {
	CaptureID string
	Entries   int
	// Pinned is the command rewritten to name the captured paths literally. It
	// replaces the caller's command so the shell cannot expand the glob a
	// second time and mutate something the capture never saw.
	Pinned      string
	Unprotected bool
}

// prepareGlobGuard implements the two lanes.
//
// A modeled mutator whose entire expanded surface is protectable is snapshotted
// and then allowed through on FIRST contact — the backup replaces the
// confirmation, because an acknowledgement that buys no recoverability is a
// round trip that costs a turn and returns nothing.
//
// Anything else keeps the confirm fence, and says WHY it could not be backed.
func (r *Runner) prepareGlobGuard(request Request) (*globGuard, map[string]any, error) {
	if request.Command == "" || request.OutputMode == "read_block" {
		return nil, nil, nil
	}
	plan, ok := planGlobMutation(request.Command)
	if !ok {
		return nil, nil, nil
	}
	globbed := false
	for _, index := range plan.Operands {
		if plan.Tokens[index].HasGlob && !plan.Tokens[index].Quoted {
			globbed = true
			break
		}
	}
	if !globbed {
		return nil, nil, nil
	}
	cwd := request.Cwd
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	resolvedCwd, err := r.confiner.Resolve(cwd)
	if err != nil {
		return nil, nil, err
	}
	groups, err := expandSurface(plan, resolvedCwd)
	if err != nil {
		return nil, nil, err
	}
	flat := flattenSurface(groups)
	if len(flat) == 0 {
		return nil, nil, nil
	}
	decision := decideGlobProtection(plan, groups)
	digest := surfaceDigest(request.Command, resolvedCwd, flat)

	if decision.Protectable && r.captures != nil {
		paths := make([]string, 0, len(flat))
		for _, entry := range flat {
			paths = append(paths, entry.Path)
		}
		id := snapshot.NewCaptureID()
		if _, err := r.captures.CaptureSurface(id, request.Command, paths); err != nil {
			return nil, nil, fmt.Errorf("could not back up the surface before running: %w", err)
		}
		return &globGuard{CaptureID: id, Entries: len(flat), Pinned: pinCommand(plan, groups)}, nil, nil
	}

	reason := decision.Reason
	if reason == "" && r.captures == nil {
		reason = "no snapshot vault is configured, so no revert could be recorded"
	}
	if confirmMatches(request.Confirm, digest) {
		return &globGuard{Unprotected: true}, nil, nil
	}
	if strings.TrimSpace(request.Confirm) != "" {
		return nil, map[string]any{
			"protection":     "refused",
			"error":          "confirm names a different surface",
			"confirm_given":  strings.TrimSpace(request.Confirm),
			"surface_digest": digest,
			"surface":        renderSurface(flat),
			"surface_count":  len(flat),
			"reason":         reason,
			"hint": fmt.Sprintf(
				"the surface changed since it was shown — re-send with confirm:%q for THIS surface, or drop confirm to see it printed again", digest),
		}, nil
	}
	return nil, map[string]any{
		"protection":     "refused",
		"ran":            false,
		"surface_digest": digest,
		"surface":        renderSurface(flat),
		"surface_count":  len(flat),
		"reason":         reason,
		"hint": fmt.Sprintf(
			"this surface cannot be backed up, so nothing ran. Re-send the IDENTICAL call with confirm:%q to run it WITHOUT any revert, or name the files explicitly.", digest),
	}, nil
}

// sealGuard records what the command left behind and states the protection
// outcome on the terminal. "ran" and "ran revertibly" are different facts, so
// an unprotected confirmed run says so rather than staying silent.
func (r *Runner) sealGuard(guard *globGuard, result map[string]any, err error) (map[string]any, error) {
	if guard == nil || result == nil {
		return result, err
	}
	if guard.Unprotected {
		result["protection"] = "unprotected_confirmed"
		result["protection_note"] = "this surface ran WITHOUT capture backing; no revert exists for it"
		return result, err
	}
	if guard.CaptureID == "" {
		return result, err
	}
	if sealErr := r.captures.SealCapture(guard.CaptureID); sealErr != nil {
		result["protection"] = "error"
		result["capture_id"] = guard.CaptureID
		result["protection_note"] = fmt.Sprintf(
			"the capture could not be sealed (%v) — the effect's outcome is partially unknown; revert with light_file{verb:\"vault_restore\", capture_id:%q}",
			sealErr, guard.CaptureID)
		return result, err
	}
	result["protection"] = "capture-backed"
	result["capture_id"] = guard.CaptureID
	note := fmt.Sprintf(
		"%d path(s) under capture %s — revert with light_file{verb:\"vault_restore\", capture_id:%q} (add force:true to clobber later writers; non-force skips changed paths)",
		guard.Entries, guard.CaptureID, guard.CaptureID)
	if guard.Pinned == "" {
		// No exact-path pin was available for this shell, so the shell
		// re-expands the pattern itself. The capture still reverts every path
		// that existed when the snapshot was taken, but a match created
		// between the snapshot and the effect is outside it. Say so, rather
		// than let "capture-backed" be read as covering the whole mutation
		// surface on a platform where it cannot.
		result["pinned"] = false
		note += "; this surface was NOT pinned on this platform, so the shell re-expands the pattern and a path created after the snapshot can be affected without being in the capture"
	}
	result["protection_note"] = note
	return result, err
}

// runSync takes the guard rather than a bare command so the pinned surface is
// structural: a caller holding a capture cannot accidentally execute the
// original glob, which would let the shell expand it a second time and mutate
// paths the capture never saw.
func (r *Runner) runSync(ctx context.Context, request Request, guard *globGuard) (map[string]any, error) {
	if guard != nil && guard.Pinned != "" {
		request.Command = guard.Pinned
	}
	if request.OutputMode == "read_block" {
		spillID := request.SpillID
		if spillID == "" {
			spillID = request.Spill
		}
		value, err := r.spills.Read(spillID, request.LineRange)
		return map[string]any{"content": value}, err
	}
	if request.Command == "" {
		return nil, fmt.Errorf("command is required")
	}
	cwd := request.Cwd
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	resolvedCwd, err := r.confiner.Resolve(cwd)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(resolvedCwd)
	if err != nil || !info.IsDir() {
		return nil, fmt.Errorf("cwd is not a directory")
	}
	timeout := time.Duration(request.TimeoutMS) * time.Millisecond
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	runContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	command := shellCommand(runContext, request.Command)
	command.Dir = resolvedCwd
	command.Env = minimalEnvironment()
	configureProcess(command)
	stdout, stderr := newBoundedBuffer(r.captureLimit), newBoundedBuffer(r.captureLimit)
	command.Stdout, command.Stderr = stdout, stderr

	values, cleanup, err := r.injectSecrets(command, request)
	if err != nil {
		return nil, err
	}
	defer cleanup()

	err = command.Run()
	exitCode := 0
	// The deadline check MUST precede the ExitError branch. A timeout kills the
	// process group, so the kill surfaces as an ExitError first and the timeout
	// would be silently reported as a bare exit_code -1 with empty output.
	timedOut := errors.Is(runContext.Err(), context.DeadlineExceeded)
	if timedOut {
		exitCode = -1
	} else if exit, ok := err.(*exec.ExitError); ok {
		exitCode = exit.ExitCode()
	} else if err != nil {
		return nil, err
	}
	rawStdout := scrub(stdout.String(), values)
	rawStderr := annotateGoModuleError(scrub(stderr.String(), values))
	var spillID string
	if len(rawStdout)+len(rawStderr) > outputLimit {
		full := "STDOUT\n" + rawStdout + "\nSTDERR\n" + rawStderr
		spillID, err = r.spills.Store([]byte(full))
		if err != nil {
			return nil, err
		}
	}
	stdoutText, stderrText := rawStdout, rawStderr
	if request.OutputMode != "" && request.OutputMode != "auto" {
		stdoutText = filterOutput(stdoutText, request.OutputMode, request.Lines, request.Filter)
		stderrText = filterOutput(stderrText, request.OutputMode, request.Lines, request.Filter)
	}
	result := map[string]any{"stdout": stdoutText, "stderr": stderrText, "exit_code": exitCode}
	if dropped := stdout.Dropped() + stderr.Dropped(); dropped > 0 {
		// Deliberately NOT the same signal as "truncated" below. That one says
		// the output moved to a spill and is recoverable in full. These bytes
		// are gone: the command outran the in-memory bound and they were never
		// retained. Reporting them separately keeps the caller from reading a
		// capped result as a complete one.
		result["output_capped"] = true
		result["dropped_bytes"] = dropped
		result["capture_limit_bytes"] = stdout.Limit()
	}
	if timedOut {
		// Partial stdout/stderr is kept deliberately: it is usually the only
		// evidence of where the command hung.
		result["timed_out"] = true
		result["timeout_ms"] = timeout.Milliseconds()
		result["error"] = fmt.Sprintf("command timed out after %s", timeout)
	}
	if spillID != "" {
		result["spill_id"] = spillID
		result["stdout"] = tail(stdoutText, 80)
		result["stderr"] = tail(stderrText, 80)
		result["truncated"] = true
	}
	return result, nil
}

// Spills exposes the store so light_file can hand oversized reads to the SAME
// spill the caller recovers through light_bash output_mode:read_block.
func (r *Runner) Spills() *SpillStore { return r.spills }

func (r *Runner) injectSecrets(command *exec.Cmd, request Request) ([]string, func(), error) {
	var values []string
	var temporary []string
	cleanup := func() {
		for _, path := range temporary {
			if info, err := os.Stat(path); err == nil {
				_ = os.WriteFile(path, make([]byte, info.Size()), 0o600)
			}
			_ = os.Remove(path)
		}
	}
	for variable, name := range request.EnvRefs {
		value, err := r.secrets.Resolve(name)
		if err != nil {
			cleanup()
			return nil, cleanup, err
		}
		command.Env = append(command.Env, variable+"="+value)
		values = append(values, value)
	}
	for variable, name := range request.FileRefs {
		value, err := r.secrets.Resolve(name)
		if err != nil {
			cleanup()
			return nil, cleanup, err
		}
		file, err := os.CreateTemp("", "light-tools-secret-*")
		if err != nil {
			cleanup()
			return nil, cleanup, err
		}
		if err := file.Chmod(0o600); err != nil {
			file.Close()
			cleanup()
			return nil, cleanup, err
		}
		if _, err := file.WriteString(value); err != nil {
			file.Close()
			cleanup()
			return nil, cleanup, err
		}
		file.Close()
		temporary = append(temporary, file.Name())
		command.Env = append(command.Env, variable+"="+file.Name())
		values = append(values, value)
	}
	return values, cleanup, nil
}

func shellCommand(ctx context.Context, source string) *exec.Cmd {
	if runtime.GOOS == "windows" {
		return exec.CommandContext(ctx, "powershell", "-NoProfile", "-Command", source)
	}
	return exec.CommandContext(ctx, "/bin/bash", "-c", source)
}

func minimalEnvironment() []string {
	return childenv.Minimal()
}

func filterOutput(value, mode string, lines int, filter string) string {
	if lines <= 0 {
		lines = 80
	}
	switch mode {
	case "head":
		parts := strings.Split(value, "\n")
		if len(parts) > lines {
			parts = parts[:lines]
		}
		return strings.Join(parts, "\n")
	case "tail":
		return tail(value, lines)
	case "grep":
		expression, err := regexp.Compile(filter)
		if err != nil {
			expression = regexp.MustCompile(regexp.QuoteMeta(filter))
		}
		var matches []string
		for _, line := range strings.Split(value, "\n") {
			if expression.MatchString(line) {
				matches = append(matches, line)
			}
		}
		return strings.Join(matches, "\n")
	}
	return value
}

func tail(value string, lines int) string {
	parts := strings.Split(value, "\n")
	if len(parts) > lines {
		parts = parts[len(parts)-lines:]
	}
	return strings.Join(parts, "\n")
}

// redactionMarker is the readable form. A replacement may never be LONGER than
// the secret it replaces: scrub runs on the stream boundedBuffer has already
// capped, so an expanding substitution re-inflates output past the memory bound
// the cap exists to hold. A one-byte secret emitted repeatedly would turn a
// capped 24 MiB stream into roughly 240 MiB and then overrun the spill ceiling,
// failing the call instead of honestly returning the capped result.
const redactionMarker = "[REDACTED]"

func redactionFor(secretValue string) string {
	if len(secretValue) >= len(redactionMarker) {
		return redactionMarker
	}
	return strings.Repeat("*", len(secretValue))
}

func scrub(value string, secrets []string) string {
	for _, secretValue := range secrets {
		if secretValue != "" {
			value = strings.ReplaceAll(value, secretValue, redactionFor(secretValue))
		}
	}
	return value
}

func annotateGoModuleError(stderr string) string {
	if strings.Contains(stderr, "missing go.sum entry") {
		return stderr + "\nlight-tools: Go dependency metadata is incomplete; run go mod tidy in the module root."
	}
	if strings.Contains(stderr, "go.mod file not found") {
		return stderr + "\nlight-tools: choose a cwd inside a Go module or initialize one with go mod init."
	}
	return stderr
}

func cleanPath(path string) string { return filepath.Clean(path) }
