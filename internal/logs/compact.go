package logs

// Deterministic, LLM-free bounding primitives, ported from Light-CF's
// light-logs package.
//
// The donor's PreFilter (npm/pip/yarn line dropping, global exact-dup collapse)
// is deliberately NOT ported: it DROPS lines, and here the template outline is
// the primary view rather than a header above a full body, so a dropped line is
// a line the reader can no longer see without going to the spill. The template
// engine in collapse.go achieves the same collapse LOSSLESSLY, keeping every
// distinct line kind including one-offs.
//
// What is ported is the bounding that every view still needs: terminal control
// semantics, a rune-safe per-line clamp and a byte budget that keeps a head AND
// a tail so a verdict on the last line survives.

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// terminalNormalize applies a terminal's own semantics to raw control bytes in
// ONE forward pass — one cursor, one output buffer, every control case a branch
// of the same switch:
//
//   - CSI: ESC [ params(0x30–0x3F) intermediates(0x20–0x2F) final(0x40–0x7E),
//     deleted whole. The parameter range covers the ':' of colon-form SGR
//     (ESC[38:2::255:0:0m), which a [0-9;] class silently leaks.
//   - OSC: ESC ] payload terminated by BEL or ST (ESC \), deleted whole — the
//     payload (OSC-8 URLs, window titles) is terminal metadata, not visible
//     text. The scan NEVER crosses a line feed: an unterminated OSC stops at
//     the LF instead of swallowing the next line's content.
//   - CRLF folds to LF; a bare CR overwrites in place, so only the segment
//     after the last CR of the line survives (an in-place progress redraw
//     keeps its final frame).
//
// A malformed or truncated control sequence stays VISIBLE rather than
// authorizing a deletion: when no terminator arrives, the ESC is emitted
// literally and scanning continues, so no byte is ever dropped on a guess.
func terminalNormalize(text string) string {
	out := make([]byte, 0, len(text))
	lineStart := 0 // index in out where the current output line begins
	for i := 0; i < len(text); {
		c := text[i]
		switch {
		case c == '\x1b' && i+1 < len(text) && text[i+1] == '[': // CSI
			// ECMA-48 orders parameter bytes strictly before intermediate
			// bytes; a parameter after an intermediate is malformed and stays
			// visible rather than being deleted on a guess.
			j := i + 2
			for j < len(text) && text[j] >= 0x30 && text[j] <= 0x3F { // parameter bytes
				j++
			}
			for j < len(text) && text[j] >= 0x20 && text[j] <= 0x2F { // intermediate bytes
				j++
			}
			if j < len(text) && text[j] >= 0x40 && text[j] <= 0x7E {
				i = j + 1 // final byte: delete the whole sequence
				continue
			}
			out = append(out, c) // malformed: stay visible
			i++
		case c == '\x1b' && i+1 < len(text) && text[i+1] == ']': // OSC
			j := i + 2
			term := 0 // 1 = BEL, 2 = ST
			for ; j < len(text); j++ {
				if text[j] == '\x07' {
					term = 1
					break
				}
				if text[j] == '\n' {
					break // never cross a line
				}
				if text[j] == '\x1b' && j+1 < len(text) && text[j+1] == '\\' {
					term = 2
					break
				}
			}
			switch term {
			case 1:
				i = j + 1
			case 2:
				i = j + 2
			default:
				out = append(out, c) // unterminated: stay visible
				i++
			}
		case c == '\r':
			if i+1 < len(text) && text[i+1] == '\n' {
				out = append(out, '\n') // CRLF folds to LF
				lineStart = len(out)
				i += 2
				continue
			}
			out = out[:lineStart] // bare CR: overwrite from line start
			i++
		case c == '\n':
			out = append(out, c)
			lineStart = len(out)
			i++
		default:
			out = append(out, c)
			i++
		}
	}
	return string(out)
}

// runeSafeHead returns at most maxBytes from the front of text without
// splitting a UTF-8 sequence.
func runeSafeHead(text string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(text) <= maxBytes {
		return text
	}
	cut := maxBytes
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut]
}

// runeSafeTail is the end-side twin: at most maxBytes from the back, whole
// runes only.
func runeSafeTail(text string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(text) <= maxBytes {
		return text
	}
	start := len(text) - maxBytes
	for start < len(text) && !utf8.RuneStart(text[start]) {
		start++
	}
	return text[start:]
}

// dedupAnnotationRE matches a trailing " (×N)" annotation.
var dedupAnnotationRE = regexp.MustCompile(`\s+\(×[0-9]+\)$`)

// StripDedupAnnotation removes a trailing " (×N)" dedup annotation, returning
// the line as the command emitted it. Consumers that MATCH content (anchor
// selection, spill grep) must match against this view — the annotation is a
// display fact, not content — while still RENDERING the annotated line
// verbatim.
func StripDedupAnnotation(line string) string {
	return dedupAnnotationRE.ReplaceAllString(line, "")
}

// ClampLines truncates each line to maxWidth runes (rune-safe so multibyte
// lines never split mid-character), appending a "…(+N chars)" marker.
// Returns whether any line was clamped.
func ClampLines(text string, maxWidth int) (string, bool) {
	lines := strings.Split(text, "\n")
	any := false
	for i, l := range lines {
		r := []rune(l)
		if len(r) > maxWidth {
			lines[i] = string(r[:maxWidth]) + fmt.Sprintf("…(+%d chars)", len(r)-maxWidth)
			any = true
		}
	}
	return strings.Join(lines, "\n"), any
}

// seamMarkerFmt renders the BudgetCap omission marker for whole lines lost
// from the middle; its length is always 21 + digits so budget math can
// predict it without formatting.
const seamMarkerFmt = "[…%d lines elided…]"

// seamInlineMarker and seamHybridMarkerFmt label within-LINE byte loss in the
// hard-cut branch, where no whole line ever fit: "[…0 lines elided…]" would
// deny the loss it just performed. The hybrid also names the later lines
// dropped alongside the cut. Lengths are measured, never assumed.
const (
	seamInlineMarker    = "[…middle of line elided…]"
	seamHybridMarkerFmt = "[…middle of line; %d later lines elided…]"
)

// BudgetCap keeps whole lines from BOTH the head and the tail of the text
// within the byte budget, with an explicit omission marker at the seam naming
// how many lines the middle lost. The head is filled first, the tail from the
// end with whatever remains — so a verdict on the last line survives as surely
// as the opening lines. Returns the kept text and the number of lines omitted
// from the middle (recoverable via read_block when the caller spills). When not
// even one whole line fits, the first line is hard-cut rune-safely head+tail
// around a within-line seam that labels the byte loss (and names the later
// lines dropped with it, when there are any).
func BudgetCap(text string, budget int) (string, int) {
	if len(text) <= budget {
		return text, 0
	}
	lines := strings.Split(text, "\n")
	n := len(lines)
	markerLen := func(dropped int) int { return 21 + len(strconv.Itoa(dropped)) }

	// Head: whole lines while they fit the head half of the budget.
	headEnd, headLen := 0, 0
	for headEnd < n {
		add := len(lines[headEnd])
		if headEnd > 0 {
			add++ // join separator
		}
		if headLen+add > budget/2 {
			break
		}
		headLen += add
		headEnd++
	}

	// Tail: whole lines from the end while head + seam marker + tail still fit.
	// Taking a tail line shrinks the dropped count, so the marker can only get
	// shorter — the candidate total stays monotone and the loop terminates.
	tailStart, tailLen, tailCount := n, 0, 0
	for tailStart > headEnd {
		l := len(lines[tailStart-1])
		newTailLen := tailLen + l
		if tailCount > 0 {
			newTailLen++ // separator inside the tail run
		}
		overhead := 1 // "\n" between marker and tail
		if headEnd > 0 {
			overhead += headLen + 1 // head + "\n" before the marker
		}
		if overhead+markerLen(tailStart-1-headEnd)+newTailLen > budget {
			break
		}
		tailLen = newTailLen
		tailCount++
		tailStart--
	}

	dropped := tailStart - headEnd
	if dropped <= 0 {
		// Conservative accounting makes this unreachable (len(text) > budget
		// on entry), but never return a marker with nothing omitted.
		return text, 0
	}

	if headEnd == 0 && tailStart == n {
		// No whole line fits — hard-cut the first line head+tail, rune-safe.
		// The seam labels every loss it performs: a within-line cut must never
		// claim "N lines elided" (N=0) because that denies the byte loss it
		// just made. The integer return still counts only omitted WHOLE lines.
		if n == 1 {
			const marker = seamInlineMarker
			avail := budget - len(marker) - 2
			if avail < 2 {
				// Budget too small for a seam marker at all — head-only.
				return runeSafeHead(lines[0], budget), 0
			}
			h := avail * 4 / 7
			t := avail - h
			return runeSafeHead(lines[0], h) + "\n" + marker + "\n" + runeSafeTail(lines[0], t), 0
		}
		marker := fmt.Sprintf(seamHybridMarkerFmt, n-1)
		avail := budget - len(marker) - 2
		if avail < 2 {
			// Budget too small for a seam marker at all — head-only.
			return runeSafeHead(lines[0], budget), n - 1
		}
		h := avail * 4 / 7
		t := avail - h
		return runeSafeHead(lines[0], h) + "\n" + marker + "\n" + runeSafeTail(lines[0], t), n - 1
	}

	var b strings.Builder
	if headEnd > 0 {
		b.WriteString(strings.Join(lines[:headEnd], "\n"))
		b.WriteString("\n")
	}
	b.WriteString(fmt.Sprintf(seamMarkerFmt, dropped))
	if tailStart < n {
		b.WriteString("\n")
		b.WriteString(strings.Join(lines[tailStart:], "\n"))
	}
	return b.String(), dropped
}
