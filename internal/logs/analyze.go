package logs

// Analyze is the single entry point light_bash, light_ssh and light_ops share.
//
// ONE STREAM IN, ONE VIEW OUT. It is called once per stream (stdout and stderr
// separately, never concatenated) so the line numbers in the view it returns
// address that stream's own spill starting at line 1. Rendering a joined
// "STDOUT\n…\nSTDERR\n…" blob and indexing into it is the fault this design
// exists to avoid: every stderr range would carry the whole stdout length as an
// offset.
//
// It is PURE — no clock, no environment, no I/O — so the same bytes always
// produce the same view, on the edge and on the hub alike. The escape hatch is
// a separate function (Disabled) the CALLER consults, precisely so that this
// one stays deterministic under test.

import (
	"os"
	"strings"
)

// Defaults for a stream rendered to a model. Callers override per tool.
const (
	defaultLineMax  = 400  // runes kept per rendered line before the clamp marker
	defaultBudget   = 8000 // bytes of view handed back
	defaultMinLines = 40   // below this many lines AND defaultMinBytes, pass through
	defaultMinBytes = 4000
)

// Options bounds one Analyze call. The zero value is valid and means defaults.
type Options struct {
	// LineMax is the per-line rune clamp. 0 → defaultLineMax.
	LineMax int
	// Budget is the byte ceiling on the returned view. 0 → defaultBudget.
	Budget int
	// MinLines and MinBytes are the pass-through floor. Output below BOTH is
	// returned exactly as given: it is already legible, and compacting it would
	// mint a spill record for nothing. 0 → the package defaults; a negative
	// value disables that half of the floor.
	MinLines int
	MinBytes int
	// Indent prefixes every rendered outline row.
	Indent string
}

func (o Options) withDefaults() Options {
	if o.LineMax == 0 {
		o.LineMax = defaultLineMax
	}
	if o.Budget == 0 {
		o.Budget = defaultBudget
	}
	if o.MinLines == 0 {
		o.MinLines = defaultMinLines
	}
	if o.MinBytes == 0 {
		o.MinBytes = defaultMinBytes
	}
	return o
}

// Result is what one stream compacted to.
type Result struct {
	// Text is the view to hand the model.
	Text string
	// Elided is true when Text is not byte-identical to the input. It is the
	// ONLY signal callers may use to decide whether a spill is required: an
	// elided view without a resolvable recovery pointer is the one outcome
	// this package must never produce.
	Elided bool
	// Outlined is true when Text is the template outline rather than the raw
	// text under a budget. Callers use it for telemetry and phrasing, never to
	// decide whether to spill.
	Outlined bool
	// Considered and Delivered are the byte counts either side of the
	// transform — the pair that makes the compaction ratio measured rather
	// than asserted.
	Considered int
	Delivered  int
	// Lines is how many raw lines were considered; Groups how many distinct
	// templates they collapsed to; Dropped how many whole lines the budget
	// omitted from the middle of the view.
	Lines   int
	Groups  int
	Dropped int
}

// Disabled reports whether the LIGHT_NO_COMPACT escape hatch is set. Callers
// check it and return their legacy shape byte-identically; Analyze itself never
// reads the environment.
func Disabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("LIGHT_NO_COMPACT"))) {
	case "", "0", "false", "no", "off":
		return false
	}
	return true
}

// Analyze renders one stream to a bounded view.
//
// Every template group is rendered, INCLUDING groups of one. The donor
// suppresses singletons and replaces them with a "+N one-off line kind(s)"
// count, which is safe there only because its outline sits above the full body.
// Here the outline IS the primary view, and a verdict line — "BUILD FAILED",
// "panic: …", "exit status 1" — occurs exactly once by nature. Suppressing
// singletons would delete precisely the line the reader came for.
//
// The outline must EARN its place. When grouping collapses nothing, every line
// gains a "[n]" prefix and the outline is LONGER than the text it replaces, so
// Analyze falls back to the raw text under the same clamp and budget. Returning
// a bigger "compacted" view is a bug, not a trade-off.
func Analyze(raw string, opts Options) Result {
	opts = opts.withDefaults()
	res := Result{Considered: len(raw)}
	if raw == "" {
		return res
	}

	norm := terminalNormalize(raw)
	lines := splitRawLines(norm)
	res.Lines = len(lines)

	if len(lines) < opts.MinLines && len(raw) <= opts.MinBytes {
		return res.deliver(raw, raw)
	}

	// The fallback view: the stream itself, clamped and budgeted. This is what
	// the outline is measured against, and what is returned when the outline
	// does not beat it.
	bounded, _ := ClampLines(norm, opts.LineMax)
	bounded, boundedDropped := BudgetCap(bounded, opts.Budget)

	groups := GroupTemplates(lines, 1)
	res.Groups = len(groups)

	rows := make([]string, 0, len(groups)*2)
	for _, g := range groups {
		rows = append(rows, RenderTemplateGroup(g, opts.Indent)...)
	}
	outline, _ := ClampLines(strings.Join(rows, "\n"), opts.LineMax)
	outline, outlineDropped := BudgetCap(outline, opts.Budget)

	if len(outline) >= len(bounded) {
		res.Dropped = boundedDropped
		return res.deliver(bounded, raw)
	}
	res.Dropped = outlineDropped
	res.Outlined = true
	return res.deliver(outline, raw)
}

// deliver stamps the outgoing view and derives Elided from it. Elided is
// computed against the ORIGINAL bytes, not the normalized ones: a stream whose
// only change was control-byte normalization is still not what the command
// emitted, and the reader must be able to recover it.
func (r Result) deliver(text, raw string) Result {
	r.Text = text
	r.Delivered = len(text)
	r.Elided = text != raw
	return r
}
