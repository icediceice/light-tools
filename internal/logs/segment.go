package logs

// The retained slice of Light-CF's light-logs segmenter.
//
// Segmentation itself is NOT ported. The donor segments a journal window into
// named Spans so a reader knows where to drill, then expands each span's
// templates underneath. Here there is no span layer: Analyze groups templates
// over the whole stream in one pass, so Span, Shape, Segment, classifyLine,
// maskedSignature and the key regexes they need (hexRE, digitsRE,
// levelPrefixRE, componentRE, fileLineRE, panicStartRE, stackFrameRE) have no
// caller and were pruned rather than carried as dead code.
//
// What survives is what collapse.go genuinely reaches: the two prefix regexes
// stripLinePrefix uses, and the line splitter whose numbering must agree with
// read_block's.

import (
	"regexp"
	"strings"
)

var (
	// journaldRE matches "Aug 21 01:08:36 host ident[123]: " and the short-iso
	// "2026-08-21T01:08:36+0700 host ident[123]: " forms, CAPTURING the syslog
	// identifier. Stripping the timestamp is what stops every line templating
	// identically. The [pid] is optional because plenty of units log without
	// one.
	journaldRE = regexp.MustCompile(`^(?:[A-Z][a-z]{2} +\d{1,2} \d{2}:\d{2}:\d{2}|\S*\d{2}:\d{2}:\d{2}\S*) +\S+ +([A-Za-z0-9_.\-]+)(?:\[\d+\])?: `)
	// tsPrefixRE strips a bare leading timestamp ("2026/08/21 01:08:36 ",
	// "2026-08-21 01:08:36.123 ", "01:08:36.123 ").
	tsPrefixRE = regexp.MustCompile(`^(?:\d{4}[-/]\d{2}[-/]\d{2}[T ])?\d{2}:\d{2}:\d{2}(?:[.,]\d+)?(?:Z|[+\-]\d{2}:?\d{2})? *`)
)

// splitRawLines splits raw into addressable lines. A single trailing newline
// terminates the last line rather than creating an empty one, so line numbers
// match what read_block reports for the same bytes.
func splitRawLines(raw string) []string {
	if raw == "" {
		return nil
	}
	lines := strings.Split(raw, "\n")
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1]
	}
	return lines
}
