package bash

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/icediceice/light-tools/internal/secret"
	"github.com/icediceice/light-tools/internal/snapshot"
)

func compactionRunner(t *testing.T) (*Runner, string) {
	t.Helper()
	root := t.TempDir()
	runner, err := NewRunner(
		testPolicy(root),
		filepath.Join(root, "spills"),
		secret.New(filepath.Join(root, "secrets")),
		snapshot.New(filepath.Join(root, "snapshots")),
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	return runner, root
}

// emitLines is a repetitive-output generator that avoids seq/jot, which are not
// present everywhere the suite runs.
func emitLines(count int, format string) string {
	return shellSource(
		fmt.Sprintf(`i=1; while [ $i -le %d ]; do printf '%s\n' "$i"; i=$((i+1)); done`, count, format),
		fmt.Sprintf(`1..%d | ForEach-Object { "%s" -f $_ }`, count, strings.ReplaceAll(format, "%s", "{0}")),
	)
}

func runCompacted(t *testing.T, runner *Runner, root, command string) map[string]any {
	t.Helper()
	result, err := runner.Run(context.Background(), Request{Command: command, Cwd: root})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestRepetitiveStdoutCollapsesAndSpillsItsOwnStream(t *testing.T) {
	runner, root := compactionRunner(t)
	result := runCompacted(t, runner, root, emitLines(500, "compiling module %s of 500"))

	stdout, _ := result["stdout"].(string)
	if strings.Count(stdout, "compiling module") != 1 {
		t.Fatalf("500 repeats were not collapsed to one row:\n%s", stdout)
	}
	if !strings.Contains(stdout, "×500") {
		t.Fatalf("repeat count missing from the outline:\n%s", stdout)
	}
	if result["truncated"] != true {
		t.Fatalf("an elided stdout did not report truncated: %#v", result)
	}

	spillID, _ := result["stdout_spill_id"].(string)
	if spillID == "" {
		t.Fatalf("an elided stdout carried no recovery pointer: %#v", result)
	}
	if hint, _ := result["stdout_recover"].(string); !strings.Contains(hint, spillID) {
		t.Fatalf("recovery hint does not name its own spill: %q", hint)
	}

	// The pointer must resolve, and line 1 of THIS stream's spill must be the
	// first line the command actually printed.
	recovered, err := runner.Run(context.Background(), Request{
		OutputMode: "read_block", Spill: spillID, LineRange: "1-1",
	})
	if err != nil {
		t.Fatalf("the outline's own pointer did not resolve: %v", err)
	}
	if got, _ := recovered["content"].(string); strings.TrimSpace(got) != "compiling module 1 of 500" {
		t.Fatalf("stdout spill line 1 = %q, want the command's first line", got)
	}
}

// The signal-preservation guard at the tool boundary: a verdict occurs exactly
// once, and it is the line the reader came for.
func TestLoneVerdictSurvivesAmongRepetitiveOutput(t *testing.T) {
	for _, verdict := range []string{"BUILD FAILED", "panic: runtime error", "exit status 1"} {
		t.Run(verdict, func(t *testing.T) {
			runner, root := compactionRunner(t)
			command := emitLines(500, "compiling module %s of 500") + "; " +
				shellSource(fmt.Sprintf("echo '%s'", verdict), fmt.Sprintf("Write-Output '%s'", verdict))
			result := runCompacted(t, runner, root, command)

			stdout, _ := result["stdout"].(string)
			if !strings.Contains(stdout, verdict) {
				t.Fatalf("the lone verdict %q was summarised away:\n%s", verdict, stdout)
			}
			if strings.Contains(stdout, "one-off line kind") {
				t.Fatalf("singleton suppression leaked into the tool result:\n%s", stdout)
			}
		})
	}
}

// Each stream gets its OWN spill, so each outline's line 1 is that stream's
// line 1. A single aggregate spill would put the STDOUT header inside every
// stdout range and the whole stdout length inside every stderr offset.
func TestStdoutAndStderrSpillSeparately(t *testing.T) {
	runner, root := compactionRunner(t)
	command := emitLines(300, "out line %s") + "; " +
		shellSource(
			`i=1; while [ $i -le 300 ]; do printf 'err line %s\n' "$i" >&2; i=$((i+1)); done`,
			`1..300 | ForEach-Object { [Console]::Error.WriteLine("err line {0}" -f $_) }`,
		)
	result := runCompacted(t, runner, root, command)

	outID, _ := result["stdout_spill_id"].(string)
	errID, _ := result["stderr_spill_id"].(string)
	if outID == "" || errID == "" {
		t.Fatalf("both streams elided but did not both spill: %#v", result)
	}
	if outID == errID {
		t.Fatal("stdout and stderr shared one spill id; their line numbers cannot both be right")
	}

	for _, c := range []struct{ id, want string }{{outID, "out line 1"}, {errID, "err line 1"}} {
		recovered, err := runner.Run(context.Background(), Request{
			OutputMode: "read_block", Spill: c.id, LineRange: "1-1",
		})
		if err != nil {
			t.Fatalf("spill %s did not resolve: %v", c.id, err)
		}
		if got, _ := recovered["content"].(string); strings.TrimSpace(got) != c.want {
			t.Fatalf("spill line 1 = %q, want %q — this stream's spill indexes another stream", got, c.want)
		}
	}
}

// Fail-open. The command has ALREADY RUN by the time a spill is attempted, so
// an exhausted spill store must degrade to exact output — never to an RPC error
// that invites a retry of a side effect that already happened, and never to an
// outline whose pointer does not resolve.
func TestSpillExhaustionFallsBackToRawWithExitCodeIntact(t *testing.T) {
	runner, root := compactionRunner(t)
	for i := 0; i < defaultMaximumSpills; i++ {
		if _, err := runner.spills.Store([]byte("filler")); err != nil {
			t.Fatalf("could not fill the spill store at %d: %v", i, err)
		}
	}

	command := emitLines(500, "retrying connection %s") + "; " +
		shellSource("exit 3", "exit 3")
	result, err := runner.Run(context.Background(), Request{Command: command, Cwd: root})
	if err != nil {
		t.Fatalf("a full spill store turned a completed command into an RPC error: %v", err)
	}
	if result["exit_code"] != 3 {
		t.Fatalf("exit code lost to a spill failure: %#v", result["exit_code"])
	}
	if result["stdout_spill_id"] != nil {
		t.Fatalf("a pointer was emitted with no spill behind it: %#v", result)
	}
	if result["stdout_compaction_skipped"] != true {
		t.Fatalf("fail-open was not reported: %#v", result)
	}
	stdout, _ := result["stdout"].(string)
	if !strings.Contains(stdout, "retrying connection 500") {
		t.Fatalf("fail-open did not return the exact output:\n%s", stdout)
	}
}

// LIGHT_NO_COMPACT is the byte-identical legacy escape hatch: same bytes out,
// and none of the compaction keys.
func TestNoCompactEnvReturnsExactOutput(t *testing.T) {
	t.Setenv("LIGHT_NO_COMPACT", "1")
	runner, root := compactionRunner(t)
	result := runCompacted(t, runner, root, emitLines(500, "compiling module %s of 500"))

	stdout, _ := result["stdout"].(string)
	if strings.Count(stdout, "compiling module") != 500 {
		t.Fatalf("the escape hatch did not return all 500 lines (got %d)", strings.Count(stdout, "compiling module"))
	}
	for _, key := range []string{"stdout_spill_id", "stdout_recover", "stdout_compaction_skipped", "truncated"} {
		if _, present := result[key]; present {
			t.Fatalf("escape hatch leaked compaction key %q: %#v", key, result)
		}
	}
}

// Acceptance: at BOTH ends of the size range — a small-but-repetitive 200-line
// output and a 5,000-line flood — the view must be an outline and the pointer
// beside it must return that stream byte-for-byte.
//
// Fidelity is asserted against CAPTURED output, not the process's true total:
// newBoundedBuffer drops bytes past the capture limit before anything is
// stored, and those bytes never reach a spill. The sizes here stay well inside
// that limit, and output_capped would flag it if they did not.
func TestAcceptanceOutlineRecoversItsStreamByteForByte(t *testing.T) {
	for _, count := range []int{200, 5000} {
		t.Run(strconv.Itoa(count), func(t *testing.T) {
			runner, root := compactionRunner(t)
			result := runCompacted(t, runner, root,
				emitLines(count, fmt.Sprintf("compiling module %%s of %d", count)))

			if result["output_capped"] == true {
				t.Fatalf("fixture outran the capture limit; fidelity cannot be asserted: %#v", result)
			}
			stdout, _ := result["stdout"].(string)
			if strings.Count(stdout, "compiling module") != 1 {
				t.Fatalf("%d repeats did not collapse to one row:\n%s", count, stdout)
			}
			if !strings.Contains(stdout, fmt.Sprintf("×%d", count)) {
				t.Fatalf("outline did not report the repeat count:\n%s", stdout)
			}
			// The counter must survive as a rendered range, not be masked away.
			if !strings.Contains(stdout, fmt.Sprintf("1..%d", count)) {
				t.Fatalf("the varying counter slot was not rendered:\n%s", stdout)
			}

			spillID, _ := result["stdout_spill_id"].(string)
			if spillID == "" {
				t.Fatalf("an outline was returned with no pointer: %#v", result)
			}
			recovered, err := runner.Run(context.Background(), Request{
				OutputMode: "read_block", Spill: spillID,
			})
			if err != nil {
				t.Fatalf("the adjacent pointer did not resolve: %v", err)
			}
			got, _ := recovered["content"].(string)

			var want strings.Builder
			for i := 1; i <= count; i++ {
				fmt.Fprintf(&want, "compiling module %d of %d\n", i, count)
			}
			if got != want.String() {
				t.Fatalf("recovery was not byte-for-byte: got %dB, want %dB", len(got), want.Len())
			}
		})
	}
}

// Compaction runs AFTER the caller's own filter, so an outline range and the
// spill behind it address the same text. Compacting pre-filter output would
// index lines the spill does not contain.
func TestFilteredOutputAlignsWithItsSpill(t *testing.T) {
	runner, root := compactionRunner(t)
	command := shellSource(
		`i=1; while [ $i -le 300 ]; do printf 'keep %s\n' "$i"; printf 'drop %s\n' "$i"; i=$((i+1)); done`,
		`1..300 | ForEach-Object { "keep {0}" -f $_; "drop {0}" -f $_ }`,
	)
	result, err := runner.Run(context.Background(), Request{
		Command: command, Cwd: root, OutputMode: "grep", Filter: "keep",
	})
	if err != nil {
		t.Fatal(err)
	}
	stdout, _ := result["stdout"].(string)
	if strings.Contains(stdout, "drop ") {
		t.Fatalf("the filter did not run before compaction:\n%s", stdout)
	}

	spillID, _ := result["stdout_spill_id"].(string)
	if spillID == "" {
		t.Fatalf("filtered output elided without a pointer: %#v", result)
	}
	recovered, err := runner.Run(context.Background(), Request{
		OutputMode: "read_block", Spill: spillID, LineRange: "1-2",
	})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := recovered["content"].(string)
	if strings.Contains(got, "drop ") {
		t.Fatalf("the spill holds pre-filter text, so every outline range is misaligned: %q", got)
	}
	if !strings.HasPrefix(strings.TrimSpace(got), "keep 1") {
		t.Fatalf("filtered spill line 1 = %q, want the first KEPT line", got)
	}
}
