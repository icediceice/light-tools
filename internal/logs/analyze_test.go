package logs

import (
	"fmt"
	"strings"
	"testing"
)

// noFloor disables the pass-through floor so a fixture can stay small and
// readable while still exercising the compaction path.
func noFloor() Options { return Options{MinLines: 2, MinBytes: -1} }

func TestAnalyzeCollapsesRepetitiveOutput(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 500; i++ {
		fmt.Fprintf(&b, "retrying connection to database backend, attempt %d\n", i)
	}
	res := Analyze(b.String(), noFloor())

	if !res.Outlined {
		t.Fatalf("500 identical-shaped lines did not produce an outline: %q", res.Text)
	}
	if !res.Elided {
		t.Fatal("an outlined result must report Elided so the caller spills")
	}
	if res.Groups != 1 {
		t.Fatalf("want 1 template group, got %d", res.Groups)
	}
	if strings.Count(res.Text, "retrying connection to database backend") != 1 {
		t.Fatalf("retry spam not collapsed to one row:\n%s", res.Text)
	}
	if !strings.Contains(res.Text, "×500") {
		t.Fatalf("repeat count not surfaced:\n%s", res.Text)
	}
	if res.Delivered >= res.Considered {
		t.Fatalf("compaction did not shrink the stream: %d -> %d", res.Considered, res.Delivered)
	}
	if res.Lines != 500 {
		t.Fatalf("Lines = %d, want 500", res.Lines)
	}
}

// TestAnalyzeKeepsALoneVerdictVerbatim is the signal-preservation guard, and
// the single most important property in this package. A verdict occurs exactly
// once, so any design that suppresses singleton groups deletes precisely the
// line the reader is looking for.
func TestAnalyzeKeepsALoneVerdictVerbatim(t *testing.T) {
	for _, verdict := range []string{"BUILD FAILED", "panic: runtime error: index out of range", "exit status 1"} {
		t.Run(verdict, func(t *testing.T) {
			var b strings.Builder
			for i := 0; i < 500; i++ {
				fmt.Fprintf(&b, "compiling module %d of 500\n", i)
			}
			b.WriteString(verdict + "\n")

			res := Analyze(b.String(), noFloor())
			if !res.Outlined {
				t.Fatalf("fixture did not outline: %q", res.Text)
			}
			if !strings.Contains(res.Text, verdict) {
				t.Fatalf("the lone verdict %q was summarised away:\n%s", verdict, res.Text)
			}
			if strings.Contains(res.Text, "one-off line kind") {
				t.Fatalf("singleton suppression leaked into the view:\n%s", res.Text)
			}
		})
	}
}

// A verdict that VARIES must show both values rather than folding to one.
func TestAnalyzeRendersAVaryingVerdictSlot(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 20; i++ {
		fmt.Fprintf(&b, "task %d done. Exit code: %d\n", i, i%2)
	}
	res := Analyze(b.String(), noFloor())

	if !strings.Contains(res.Text, "0 1") && !strings.Contains(res.Text, "1 0") {
		t.Fatalf("the varying exit-code slot did not render both values:\n%s", res.Text)
	}
}

// The counter case the template engine exists for: sixteen restarts hidden
// inside lines that differ only by a climbing number.
func TestAnalyzeSurfacesAClimbingCounter(t *testing.T) {
	var b strings.Builder
	for i := 599; i <= 614; i++ {
		fmt.Fprintf(&b, "Aug 21 03:26:59 ice-server systemd[2158]: light-edge.service: Scheduled restart job, restart counter is at %d.\n", i)
	}
	res := Analyze(b.String(), noFloor())

	if !strings.Contains(res.Text, "599..614") {
		t.Fatalf("the restart counter range never reached the view:\n%s", res.Text)
	}
	if !strings.Contains(res.Text, "(16 values, +1 each)") {
		t.Fatalf("the counter was not recognised as a counter:\n%s", res.Text)
	}
}

// Below the floor nothing is touched — and nothing is elided, so the caller
// never mints a spill record for output that was already legible.
func TestAnalyzeFloorPassesSmallOutputThrough(t *testing.T) {
	raw := "starting\nworking\ndone\n"
	res := Analyze(raw, Options{})

	if res.Text != raw {
		t.Fatalf("small output was rewritten:\ngot  %q\nwant %q", res.Text, raw)
	}
	if res.Elided || res.Outlined {
		t.Fatalf("small output reported Elided=%v Outlined=%v, want false/false", res.Elided, res.Outlined)
	}
	if res.Considered != len(raw) || res.Delivered != len(raw) {
		t.Fatalf("byte counters wrong for a pass-through: %d -> %d", res.Considered, res.Delivered)
	}
}

// The outline must EARN its place. When grouping collapses nothing, every line
// gains a "[n]" prefix and the outline is LONGER than the text it replaces —
// returning a bigger "compacted" view is a bug, not a trade-off.
func TestAnalyzeFallsBackWhenGroupingCollapsesNothing(t *testing.T) {
	var b strings.Builder
	words := []string{"alpha", "bravo", "charlie", "delta", "echo", "foxtrot", "golf", "hotel"}
	for i, w := range words {
		for j := 0; j < 6; j++ {
			fmt.Fprintf(&b, "%s the quick brown fox jumped over lazy dog number %s%d\n", w, w, j)
		}
		_ = i
	}
	raw := b.String()
	res := Analyze(raw, Options{MinLines: 2, MinBytes: -1, Budget: 1 << 20})

	if res.Delivered > res.Considered {
		t.Fatalf("view grew: %d -> %d\n%s", res.Considered, res.Delivered, res.Text)
	}
}

func TestAnalyzeEmptyInput(t *testing.T) {
	res := Analyze("", Options{})
	if res.Text != "" || res.Elided || res.Outlined || res.Considered != 0 || res.Delivered != 0 {
		t.Fatalf("empty input produced %#v", res)
	}
}

// Analyze is pure — the same bytes must always produce the same view, on the
// edge and on the hub alike.
func TestAnalyzeIsDeterministic(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 300; i++ {
		fmt.Fprintf(&b, "worker %d handled request %x in %dms\n", i%7, i*2654435761, i)
	}
	raw := b.String()
	first := Analyze(raw, noFloor())
	for i := 0; i < 5; i++ {
		if got := Analyze(raw, noFloor()); got.Text != first.Text {
			t.Fatalf("run %d differed from the first:\n%s\n---\n%s", i, first.Text, got.Text)
		}
	}
}

// Elided is the caller's ONLY spill trigger, so it must be exactly "the view
// is not the input bytes" — including when the only change was control-byte
// normalization, which the reader still cannot recover from the view alone.
func TestAnalyzeElidedTracksTheInputBytes(t *testing.T) {
	raw := "\x1b[31mred\x1b[0m\nplain\n"
	res := Analyze(raw, Options{MinLines: 2, MinBytes: -1})
	if res.Text == raw {
		t.Fatal("fixture did not change; pick one that normalizes")
	}
	if !res.Elided {
		t.Fatalf("view differs from input but Elided is false: %q", res.Text)
	}
}
