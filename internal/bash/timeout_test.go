package bash

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/icediceice/light-tools/internal/secret"
	"github.com/icediceice/light-tools/internal/snapshot"
)

func newTestRunner(t *testing.T) (*Runner, string) {
	t.Helper()
	runner, _, root := newGuardRunner(t)
	return runner, root
}

// newGuardRunner also hands back the capture vault, which the glob-guard tests
// need in order to assert that a revert actually restores.
func newGuardRunner(t *testing.T) (*Runner, *snapshot.Vault, string) {
	t.Helper()
	root := t.TempDir()
	vault := snapshot.New(filepath.Join(root, "snapshots"))
	runner, err := NewRunner(testPolicy(root), filepath.Join(root, "spills"), secret.New(filepath.Join(root, "secrets")), vault)
	if err != nil {
		t.Fatal(err)
	}
	return runner, vault, root
}

// A command killed by its deadline must SAY it timed out. Before the deadline
// check was moved ahead of the ExitError branch, the process-group kill matched
// as an ordinary ExitError and the caller got {"exit_code":-1,"stdout":"",
// "stderr":""} — indistinguishable from a command that simply failed.
func TestTimeoutIsReportedAndKeepsPartialOutput(t *testing.T) {
	runner, root := newTestRunner(t)
	result, err := runner.Run(context.Background(), Request{
		Command:   shellSource("printf before; printf err >&2; sleep 5", "[Console]::Out.Write(\"before\"); [Console]::Error.Write(\"err\"); Start-Sleep -Seconds 5"),
		Cwd:       root,
		TimeoutMS: 300,
	})
	if err != nil {
		t.Fatalf("a timeout is a result, not a transport error: %v", err)
	}
	if timedOut, _ := result["timed_out"].(bool); !timedOut {
		t.Fatalf("timed_out should be true, got %#v", result)
	}
	if code, _ := result["exit_code"].(int); code != -1 {
		t.Fatalf("exit_code should be -1, got %#v", result["exit_code"])
	}
	if message, _ := result["error"].(string); !strings.Contains(message, "timed out") {
		t.Fatalf("error should name the timeout, got %#v", result["error"])
	}
	// Partial output is the only evidence of where the command hung.
	if stdout, _ := result["stdout"].(string); !strings.Contains(stdout, "before") {
		t.Fatalf("partial stdout was discarded, got %#v", result["stdout"])
	}
	if stderr, _ := result["stderr"].(string); !strings.Contains(stderr, "err") {
		t.Fatalf("partial stderr was discarded, got %#v", result["stderr"])
	}
}

// An ordinary non-zero exit must NOT be relabelled as a timeout.
func TestOrdinaryNonZeroExitIsNotATimeout(t *testing.T) {
	runner, root := newTestRunner(t)
	result, err := runner.Run(context.Background(), Request{Command: "exit 7", Cwd: root, TimeoutMS: 30000})
	if err != nil {
		t.Fatal(err)
	}
	if code, _ := result["exit_code"].(int); code != 7 {
		t.Fatalf("exit_code should be 7, got %#v", result["exit_code"])
	}
	if _, present := result["timed_out"]; present {
		t.Fatalf("timed_out must be absent for an ordinary exit, got %#v", result)
	}
}

// Cancellation of the PARENT context is not a deadline. It must not be reported
// as a timeout, so the async path can still stamp it as cancelled.
func TestParentCancellationIsNotReportedAsTimeout(t *testing.T) {
	runner, root := newTestRunner(t)
	ctx, cancel := context.WithCancel(context.Background())
	go cancel()
	result, err := runner.Run(ctx, Request{Command: "sleep 2", Cwd: root, TimeoutMS: 30000})
	if err != nil {
		return // a transport error is an acceptable shape for cancellation
	}
	if timedOut, _ := result["timed_out"].(bool); timedOut {
		t.Fatalf("cancellation must not be reported as a timeout, got %#v", result)
	}
}
