package bash

import (
	"context"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/icediceice/light-tools/internal/secret"
)

// The bound has to hold against a writer that keeps going long after the
// ceiling, and it has to report honestly how much it threw away. A cap that
// silently returns a short result is worse than no cap: the caller reads a
// truncated answer as a complete one.
func TestBoundedBufferCapsRetentionAndCountsTheRest(t *testing.T) {
	buffer := newBoundedBuffer(10)
	chunk := []byte("0123456789")

	written, err := buffer.Write(chunk)
	if err != nil {
		t.Fatalf("first write: %v", err)
	}
	if written != len(chunk) {
		t.Fatalf("short count %d on a write that fit; io.Writer requires the full length", written)
	}
	if buffer.Dropped() != 0 {
		t.Fatalf("dropped %d bytes while still inside the limit", buffer.Dropped())
	}

	// Straddles the ceiling: part retained, part counted.
	written, err = buffer.Write([]byte("abcde"))
	if err != nil {
		t.Fatalf("overflow write: %v", err)
	}
	if written != 5 {
		t.Fatalf("overflow write reported %d, want the full 5", written)
	}
	if got := buffer.String(); got != "0123456789" {
		t.Fatalf("retained %q, want the first 10 bytes only", got)
	}
	if buffer.Dropped() != 5 {
		t.Fatalf("dropped = %d, want 5", buffer.Dropped())
	}

	// Well past the ceiling the buffer must not grow at all.
	for i := 0; i < 1000; i++ {
		if _, err := buffer.Write(chunk); err != nil {
			t.Fatalf("saturated write %d: %v", i, err)
		}
	}
	if length := len(buffer.String()); length != 10 {
		t.Fatalf("buffer grew to %d bytes past its limit", length)
	}
	if want := int64(5 + 1000*10); buffer.Dropped() != want {
		t.Fatalf("dropped = %d, want %d", buffer.Dropped(), want)
	}
}

func TestBoundedBufferZeroLimitFallsBackToTheDefault(t *testing.T) {
	if got := newBoundedBuffer(0).Limit(); got != captureLimit {
		t.Fatalf("zero limit resolved to %d, want the %d default", got, captureLimit)
	}
}

// The end-to-end contract: a command that outruns the in-memory bound still
// returns, and says so. Before this bound existed the buffer grew until the
// process died, because outputLimit was only consulted after Run() returned.
func TestRunawayCommandIsCappedAndReportsWhatItDropped(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell loop to generate output")
	}
	root := t.TempDir()
	vault := secret.New(filepath.Join(root, "secrets"))
	runner, err := NewRunner(testPolicy(root), filepath.Join(root, "spills"), vault, nil)
	if err != nil {
		t.Fatal(err)
	}
	// A small ceiling drives the same code path 24 MiB would, without the wait.
	runner.captureLimit = 2048

	result, err := runner.Run(context.Background(), Request{
		// 400 lines of 100 characters: ~40 KB against a 2 KB ceiling.
		Command: "for i in $(seq 1 400); do printf '%0100d\\n' \"$i\"; done",
		Cwd:     root,
	})
	if err != nil {
		t.Fatalf("a capped command must still return a result: %v", err)
	}
	if result["output_capped"] != true {
		t.Fatalf("output outran the limit but was not reported as capped: %#v", result)
	}
	dropped, ok := result["dropped_bytes"].(int64)
	if !ok || dropped <= 0 {
		t.Fatalf("dropped_bytes missing or not positive: %#v", result["dropped_bytes"])
	}
	if limit := result["capture_limit_bytes"]; limit != 2048 {
		t.Fatalf("capture_limit_bytes = %v, want the 2048 actually applied", limit)
	}
	// The retained head must still be real output, not an empty husk.
	if stdout, _ := result["stdout"].(string); !strings.Contains(stdout, "0000001") {
		t.Fatalf("the retained head lost the start of the stream: %q", stdout)
	}
}

// A command comfortably inside the bound must be untouched: no cap signal, and
// nothing dropped. Otherwise the new fields would train callers to ignore them.
func TestOrdinaryCommandIsNotReportedAsCapped(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell")
	}
	root := t.TempDir()
	vault := secret.New(filepath.Join(root, "secrets"))
	runner, err := NewRunner(testPolicy(root), filepath.Join(root, "spills"), vault, nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(context.Background(), Request{Command: "printf 'hello'", Cwd: root})
	if err != nil {
		t.Fatal(err)
	}
	if _, present := result["output_capped"]; present {
		t.Fatalf("a small command was flagged as capped: %#v", result)
	}
	if _, present := result["dropped_bytes"]; present {
		t.Fatalf("a small command reported dropped bytes: %#v", result)
	}
	if result["stdout"] != "hello" {
		t.Fatalf("stdout = %#v, want hello", result["stdout"])
	}
}
