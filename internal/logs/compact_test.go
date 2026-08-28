package logs

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// Terminal-control invariants. In the donor these ran through PreFilter, which
// is not ported; they exercise terminalNormalize directly here, which is the
// function that actually owns every one of these behaviours.

func TestTerminalNormalize_STTerminatedOSCNeverCrossesLines(t *testing.T) {
	raw := "\x1b]8;;https://example.test\x1b\\link\x1b]8;;\x1b\\\nmust-survive\x07tail"
	got := terminalNormalize(raw)
	if !strings.Contains(got, "link") || !strings.Contains(got, "must-survive") {
		t.Fatalf("ST-terminated OSC consumed later output: %q", got)
	}
}

func TestTerminalNormalize_ColonFormSGRStripped(t *testing.T) {
	// A [0-9;] parameter class silently leaks the ':' of colon-form SGR.
	if got := terminalNormalize("\x1b[38:2::255:0:0mred\x1b[0m"); got != "red" {
		t.Fatalf("colon-form SGR leaked control bytes: %q", got)
	}
}

func TestTerminalNormalize_CRLFContentSurvives(t *testing.T) {
	if got := terminalNormalize("alpha\r\nbeta\r\n"); got != "alpha\nbeta\n" {
		t.Fatalf("CRLF content changed: got %q", got)
	}
}

func TestTerminalNormalize_BareCRKeepsLastFrame(t *testing.T) {
	// An in-place redrawn progress frame (bare CR, no LF): a terminal shows
	// only the final frame, so normalization keeps the segment after the last
	// CR within the line.
	got := terminalNormalize("loading 10%\rloading 55%\rloading 100%\nnext line")
	if got != "loading 100%\nnext line" {
		t.Fatalf("bare-CR frame normalization wrong: %q", got)
	}
}

func TestTerminalNormalize_MalformedCSIOrderingStaysVisible(t *testing.T) {
	// ECMA-48 orders parameter bytes before intermediate bytes. A parameter
	// after an intermediate makes the sequence malformed, so normalization must
	// preserve it verbatim rather than deleting bytes on a guess.
	raw := "\x1b[ 1mVISIBLE"
	if got := terminalNormalize(raw); got != raw {
		t.Fatalf("malformed CSI was deleted: got %q, want %q", got, raw)
	}
}

func TestTerminalNormalize_UnterminatedOSCStaysVisible(t *testing.T) {
	raw := "\x1b]8;;no-terminator-here"
	if got := terminalNormalize(raw); got != raw {
		t.Fatalf("unterminated OSC was deleted: got %q, want %q", got, raw)
	}
}

func TestClampLines(t *testing.T) {
	long := strings.Repeat("x", 50)
	got, any := ClampLines(long, 20)
	if !any {
		t.Fatalf("expected clamp to fire")
	}
	if !strings.Contains(got, "…(+30 chars)") {
		t.Fatalf("clamp marker wrong: %q", got)
	}
}

func TestClampLinesIsRuneSafe(t *testing.T) {
	got, any := ClampLines(strings.Repeat("α", 50), 20)
	if !any {
		t.Fatalf("expected clamp to fire")
	}
	if !utf8.ValidString(got) {
		t.Fatalf("clamp split a multibyte rune: %q", got)
	}
}

func TestBudgetCap(t *testing.T) {
	in := "aaaa\nbbbb\ncccc\ndddd"
	got, dropped := BudgetCap(in, 10) // ~2 lines fit (5 bytes each w/ newline)
	if dropped == 0 {
		t.Fatalf("expected middle lines dropped")
	}
	if !strings.HasPrefix(got, "aaaa") {
		t.Fatalf("head bias broken: %q", got)
	}
}

// BudgetCap keeps a TAIL as well as a head — a head-only cap discards exactly
// the verdict a reader came for.
func TestBudgetCapPreservesTailVerdict(t *testing.T) {
	var raw strings.Builder
	for i := 0; i < 200; i++ {
		raw.WriteString(strings.Repeat("x", 80+i))
		raw.WriteByte('\n')
	}
	raw.WriteString("FATAL: migration failed")

	got, dropped := BudgetCap(raw.String(), 16000)
	if dropped == 0 {
		t.Fatal("fixture did not exceed the budget")
	}
	if !strings.Contains(got, "FATAL: migration failed") {
		t.Fatalf("budget discarded the final verdict: dropped=%d", dropped)
	}
	if len(got) > 16000 {
		t.Fatalf("bounded output is %dB, want <= 16000B", len(got))
	}
}

func TestBudgetCap_SingleLongLineDoesNotClaimZeroLinesElided(t *testing.T) {
	raw := strings.Repeat("α", 100)
	got, dropped := BudgetCap(raw, 40)
	if dropped != 0 {
		t.Fatalf("single-line truncation reported omitted whole lines: %d", dropped)
	}
	if strings.Contains(got, "0 lines elided") {
		t.Fatalf("single-line byte loss was mislabeled as zero elided lines: %q", got)
	}
	if len(got) > 40 {
		t.Fatalf("bounded output is %dB, want <= 40B", len(got))
	}
	if !utf8.ValidString(got) {
		t.Fatalf("single-line seam split UTF-8: %q", got)
	}
	if !strings.HasPrefix(got, "α") || !strings.HasSuffix(got, "α") {
		t.Fatalf("single-line head+tail evidence was not retained: %q", got)
	}
}

// StripDedupAnnotation is the consumer-side view of a " (×N)" annotation:
// matching (anchor selection, spill grep) must see the line as the command
// emitted it, while rendering keeps the annotated form.
func TestStripDedupAnnotation(t *testing.T) {
	cases := []struct{ in, want string }{
		{"--- FAIL: TestFoo (0.00s) (×4)", "--- FAIL: TestFoo (0.00s)"},
		{"same line here (×3)", "same line here"},
		{"no annotation here", "no annotation here"},
		{"multi (×2) suffix (×12)", "multi (×2) suffix"},                       // strips only the trailing annotation
		{"edge case literal (×) no digits", "edge case literal (×) no digits"}, // not an annotation
		{"", ""},
	}
	for _, c := range cases {
		if got := StripDedupAnnotation(c.in); got != c.want {
			t.Fatalf("StripDedupAnnotation(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
