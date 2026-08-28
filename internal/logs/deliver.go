package logs

// Elision implies recovery.
//
// This file owns the ONE rule the three shell tools must not each re-implement:
// if the view handed to a model is not the bytes the command produced, the
// exact bytes must be recoverable, and the pointer that recovers them must
// resolve. An outline whose spill_id does not exist is worse than no
// compaction at all — it tells the reader the evidence is one call away when it
// is gone.

import (
	"strings"

	"github.com/icediceice/light-tools/internal/telemetry"
)

// Spiller stores one stream's exact bytes and returns the id that recovers
// them. It is the narrow slice of bash.SpillStore this package needs, declared
// here so light_ssh and light_ops do not import light_bash to reach it.
type Spiller interface {
	Store(data []byte) (string, error)
}

// Stream is one compacted stream, ready to be attached to a tool result.
type Stream struct {
	Result
	// SpillID recovers the EXACT bytes this view was rendered from — this
	// stream's own bytes, never a concatenation with another stream. Empty when
	// nothing was elided and the view IS the output.
	SpillID string
	// Skipped is true when Text is the exact raw output because compaction
	// stood down rather than because there was nothing to compact.
	Skipped bool
	// SpillFailed narrows Skipped to the case callers must SURFACE: the view
	// would have elided, but the bytes backing it could not be stored, so the
	// output was returned exact instead. The escape-hatch case sets Skipped
	// alone — there the whole point is a byte-identical legacy shape, and an
	// extra result key would defeat it.
	SpillFailed bool
}

// Compact renders one stream and guarantees elision implies recovery.
//
// FAIL-OPEN is the whole point, and it is not a nicety. The spill store caps
// live records at 64 with a one-hour TTL and frees them only on expiry, so
// Store legitimately fails on a busy session — and by the time it does, the
// command HAS ALREADY RUN. Returning that error instead of the result would
// report an RPC failure for a command whose side effect already happened,
// inviting a retry that performs it twice. So a failed spill degrades to the
// exact raw output with Skipped set, never to an outline whose pointer does not
// resolve, and never to a lost exit code.
//
// Telemetry is recorded ONCE, against the bytes actually delivered. Recording
// the analyzed size and then delivering raw on a fail-open would report a
// saving that did not happen.
func Compact(raw string, opts Options, spills Spiller, recorder telemetry.Recorder) Stream {
	record := func(s Stream) Stream {
		if recorder != nil {
			recorder.RecordCompactBytes(len(raw), len(s.Text))
		}
		return s
	}

	if Disabled() {
		return record(exact(raw))
	}

	res := Analyze(raw, opts)
	if !res.Elided {
		// The view IS the output. No spill: minting one here would burn a
		// record from a 64-slot budget to recover bytes the caller already has.
		return record(Stream{Result: res})
	}
	if spills == nil {
		return record(spillFailed(raw))
	}
	id, err := spills.Store([]byte(raw))
	if err != nil || id == "" {
		return record(spillFailed(raw))
	}
	return record(Stream{Result: res, SpillID: id})
}

// spillFailed is the fail-open view for an elision that could not be backed.
func spillFailed(raw string) Stream {
	s := exact(raw)
	s.SpillFailed = true
	return s
}

// exact is the fail-open view: the output as the command produced it.
func exact(raw string) Stream {
	return Stream{
		Result:  Result{Text: raw, Considered: len(raw), Delivered: len(raw), Lines: len(splitRawLines(raw))},
		Skipped: true,
	}
}

// RecoveryHint is the pointer text placed ADJACENT to an outline. Recovery runs
// through light_bash's read_block for all three tools because they share one
// spill store — which is why main.go hands bashRunner.Spills() to each.
func RecoveryHint(spillID string) string {
	var b strings.Builder
	b.WriteString(`recover exact lines: light_bash{output_mode:"read_block", spill:"`)
	b.WriteString(spillID)
	b.WriteString(`", line_range:"N-M"}`)
	return b.String()
}
