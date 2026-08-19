package bash

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/icediceice/light-tools/internal/secret"
	"github.com/icediceice/light-tools/internal/security"
)

const (
	outputLimit       = 128 * 1024
	wildcardReceiptTTL = 10 * time.Minute
	wildcardReceiptCap = 64
)

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
}

type Runner struct {
	roots   []string
	spills  *SpillStore
	secrets *secret.Vault
	tasks   *TaskManager

	wildcardMu       sync.Mutex
	wildcardReceipts map[[sha256.Size]byte]time.Time
}

func NewRunner(roots []string, spillRoot string, secrets *secret.Vault) (*Runner, error) {
	spills, err := NewSpillStore(spillRoot, time.Hour)
	if err != nil {
		return nil, err
	}
	return &Runner{roots: roots, spills: spills, secrets: secrets, tasks: NewTaskManager()}, nil
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
	if request.Async {
		request.Async = false
		id, err := r.tasks.Start(func(taskContext context.Context) (map[string]any, error) {
			return r.runSync(taskContext, request)
		})
		if err != nil {
			return nil, err
		}
		return map[string]any{"task_id": id, "status": "queued"}, nil
	}
	return r.runSync(ctx, request)
}

func (r *Runner) runSync(ctx context.Context, request Request) (map[string]any, error) {
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
	resolvedCwd, err := security.ResolveBeneath(cwd, r.roots)
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
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr

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
	allowed := map[string]bool{"PATH": true, "HOME": true, "LANG": true, "LC_ALL": true, "TERM": true, "TMPDIR": true, "SSH_AUTH_SOCK": true, "SYSTEMROOT": true}
	var environment []string
	for _, item := range os.Environ() {
		name, _, _ := strings.Cut(item, "=")
		if allowed[name] {
			environment = append(environment, item)
		}
	}
	sort.Strings(environment)
	return environment
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

func scrub(value string, secrets []string) string {
	for _, secretValue := range secrets {
		if secretValue != "" {
			value = strings.ReplaceAll(value, secretValue, "[REDACTED]")
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
