package logs

import (
	"fmt"
	"strings"
	"testing"
)

// TestTemplatizeMasksInsideAnUnderscoreIdentifier pins the one subtle reason
// varTokenRE is unanchored. Go's RE2 has no lookbehind and \b counts _ as a
// word character, so an ANCHORED pattern leaves both halves of
// cmd_<digits>_<hex> unmasked and every command line becomes its own template.
func TestTemplatizeMasksInsideAnUnderscoreIdentifier(t *testing.T) {
	a, aSlots := Templatize(`exec [cmd_1787331706690_652e50e3]: R="/Git"`)
	b, bSlots := Templatize(`exec [cmd_1787331916689_be9e0d5e]: R="/Git"`)

	if a != b {
		t.Fatalf("two command lines differing only in their id produced different templates:\n a=%q\n b=%q", a, b)
	}
	if len(aSlots) != len(bSlots) {
		t.Fatalf("slot arity differs: %d vs %d", len(aSlots), len(bSlots))
	}
	// The captured values must be the ORIGINAL tokens, not the mask.
	if aSlots[0] != "1787331706690" || aSlots[1] != "652e50e3" {
		t.Fatalf("captured slots lost the original values: %#v", aSlots)
	}
	if strings.Contains(a, "1787331706690") || strings.Contains(a, "652e50e3") {
		t.Fatalf("template still carries a variable token: %q", a)
	}
}

// TestGroupTemplatesGroupsAcrossInterleaving is the case that defeats
// consecutive-run grouping: the repeated lines alternate, so every run is
// length one and a run-based grouping collapses nothing.
func TestGroupTemplatesGroupsAcrossInterleaving(t *testing.T) {
	lines := []string{
		"Found left-over process 3884 (PM2) in control group. Ignoring.",
		"This usually indicates unclean termination of a previous run.",
		"Found left-over process 369457 (adb) in control group. Ignoring.",
		"This usually indicates unclean termination of a previous run.",
		"Found left-over process 195960 (python3) in control group. Ignoring.",
		"This usually indicates unclean termination of a previous run.",
	}
	groups := GroupTemplates(lines, 1)

	if len(groups) != 2 {
		var got []string
		for _, g := range groups {
			got = append(got, fmt.Sprintf("%q x%d", g.Template, g.Count()))
		}
		t.Fatalf("want 2 groups across the interleaving, got %d: %v", len(groups), got)
	}
	if groups[0].Count() != 3 || groups[1].Count() != 3 {
		t.Fatalf("group counts = %d/%d, want 3/3", groups[0].Count(), groups[1].Count())
	}
	// First-occurrence order, and line numbers must survive the interleaving.
	wantLines := []int{1, 3, 5}
	for i, n := range groups[0].Lines {
		if n != wantLines[i] {
			t.Fatalf("group 0 line numbers = %v, want %v", groups[0].Lines, wantLines)
		}
	}
}

// TestRenderFoldsConstantSlotsAndNamesOnlyVaryingOnes proves the table shows
// the values that CHANGED and keeps everything else inline as readable text.
func TestRenderFoldsConstantSlotsAndNamesOnlyVaryingOnes(t *testing.T) {
	// Worker ids are deliberately NOT a sequence: this test is about the
	// constant fold, and an ascending set would render as a counter range
	// instead of the value list, exercising the wrong branch.
	lines := []string{
		"queue capacity set to 10 on worker 7",
		"queue capacity set to 10 on worker 3",
		"queue capacity set to 10 on worker 9",
	}
	out := strings.Join(RenderTemplateGroup(GroupTemplates(lines, 1)[0], ""), "\n")

	// "10" never varies, so it is folded back literally rather than becoming a slot.
	if !strings.Contains(out, "capacity set to 10 on worker") {
		t.Fatalf("constant slot was not folded back into the template:\n%s", out)
	}
	if strings.Contains(out, "▪2") {
		t.Fatalf("a constant slot was rendered as a varying one:\n%s", out)
	}
	if !strings.Contains(out, "▪1: 7 3 9") {
		t.Fatalf("the varying slot was not listed:\n%s", out)
	}
}

func TestDescribeSlotShapesByCardinality(t *testing.T) {
	cases := []struct {
		name  string
		uniq  []string
		nrows int
		want  string
	}{
		{
			// The case this whole file exists for: a counter, not a value set.
			name:  "monotonic counter",
			uniq:  []string{"599", "600", "601", "602"},
			nrows: 4,
			want:  "599..602  (4 values, +1 each)",
		},
		{
			name:  "ascending but gapped",
			uniq:  []string{"10", "25", "1000"},
			nrows: 3,
			want:  "10..1000  (3 values, ascending)",
		},
		{
			// Opaque ids: listing them is noise, so report the count instead.
			name:  "all distinct and many",
			uniq:  []string{"a1", "b2", "c3", "d4", "e5", "f6", "g7"},
			nrows: 7,
			want:  "7 distinct  (e.g. a1 b2)",
		},
		{
			// A small SET is the informative case and is listed in full.
			name:  "small value set",
			uniq:  []string{"(sleep)", "(java)", "(adb)"},
			nrows: 40,
			want:  "(sleep) (java) (adb)",
		},
		{
			// Not ascending, so not a counter — falls through to the set form.
			name:  "numeric but unordered",
			uniq:  []string{"9", "3", "7"},
			nrows: 3,
			want:  "9 3 7",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := describeSlot(c.uniq, c.nrows); got != c.want {
				t.Fatalf("describeSlot = %q, want %q", got, c.want)
			}
		})
	}
}

// TestDifferingArityNeverMerges — two lines of the same prose shape but a
// different number of captured tokens describe different events.
func TestDifferingArityNeverMerges(t *testing.T) {
	lines := []string{
		"Consumed 44min CPU time",
		"Consumed 6h 9min CPU time",
	}
	if got := len(GroupTemplates(lines, 1)); got != 2 {
		t.Fatalf("want 2 groups for differing arity, got %d", got)
	}
}

// TestGroupedRangesStillAddressRawLines guards the property every drill
// depends on: a rendered [lo-hi] must name real raw line numbers, because
// read_block resolves them against the verbatim spill.
func TestGroupedRangesStillAddressRawLines(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 40; i++ {
		fmt.Fprintf(&b, "Aug 21 03:26:59 ice-server systemd[2158]: light-edge.service: Found left-over process %d (sleep) in control group. Ignoring.\n", 3000+i)
	}
	rawLines := splitRawLines(b.String())

	for _, g := range GroupTemplates(rawLines, 1) {
		for _, n := range g.Lines {
			if n < 1 || n > len(rawLines) {
				t.Fatalf("group line %d is outside the raw range 1..%d", n, len(rawLines))
			}
			// The recorded line must actually contain the grouped content — an
			// independent check that the index is real.
			if !strings.Contains(rawLines[n-1], "left-over process") {
				t.Fatalf("line %d does not hold the grouped content: %q", n, rawLines[n-1])
			}
		}
	}
}

// TestRenderTemplateGroupKeepsASingletonVerbatim is the regression guard for
// the donor's ExpandSpan trap. ExpandSpan drops any group of one and reports
// "+N one-off line kind(s)" instead, which is safe there only because the full
// body sits under the outline. Here the outline IS the view, and a verdict line
// occurs exactly once — summarising it away deletes the answer.
func TestRenderTemplateGroupKeepsASingletonVerbatim(t *testing.T) {
	out := RenderTemplateGroup(GroupTemplates([]string{"BUILD FAILED"}, 42)[0], "")
	if len(out) != 1 {
		t.Fatalf("a singleton rendered %d rows, want 1: %v", len(out), out)
	}
	if !strings.Contains(out[0], "BUILD FAILED") {
		t.Fatalf("singleton line was not rendered verbatim: %q", out[0])
	}
	if strings.Contains(out[0], "×") {
		t.Fatalf("a group of one carried a repeat count: %q", out[0])
	}
	if !strings.Contains(out[0], "[42]") {
		t.Fatalf("singleton lost its raw line number: %q", out[0])
	}
}

// TestStripLinePrefixNeutralisesTimestamps — two lines emitted a second apart
// must not template differently for that reason alone.
func TestStripLinePrefixNeutralisesTimestamps(t *testing.T) {
	a := stripLinePrefix("Aug 21 03:26:59 ice-server systemd[2158]: unit stopped")
	b := stripLinePrefix("Aug 21 03:27:04 ice-server systemd[2158]: unit stopped")
	if a != b || a != "unit stopped" {
		t.Fatalf("journald prefix survived stripping: a=%q b=%q", a, b)
	}
	if got := stripLinePrefix("2026-08-21 01:08:36.123 starting up"); got != "starting up" {
		t.Fatalf("bare timestamp prefix survived stripping: %q", got)
	}
}
