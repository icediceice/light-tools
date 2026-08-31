package logs

// Template collapse — what varies across a set of otherwise-identical lines.
//
// Ported from Light-CF's light-logs package. The donor uses this to describe
// the inside of a span that its segmenter already produced; here there is no
// segmenter, so GroupTemplates runs over the WHOLE line set of one stream and
// every group it finds is rendered.
//
// It is a NAVIGATION aid, never storage. The verbatim spill is written before
// any rendering, so every [L…] span here still addresses raw lines that
// read_block returns byte-for-byte.

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const (
	// slotMark stands in for a captured token inside a template. It is a byte
	// that cannot occur in a log line we would render.
	slotMark = "\x00"
	// arityMark separates template from arity in the bucket key, so two lines
	// with the same shape but a different number of captures never merge.
	arityMark = "\x01"

	// slotWidth bounds one slot's rendered value list.
	slotWidth = 96

	// spanRunCap caps how many ascending runs one line-set span lists before
	// the remainder collapses into a +N marker. GroupTemplates groups
	// non-consecutive lines on purpose, so a scattered 500-line group must
	// not print 500 ordinals to name its coverage.
	spanRunCap = 8
)

// varTokenRE captures the tokens that vary between otherwise-identical lines.
//
// UNANCHORED on purpose, and this is the one subtle thing in the file. Go's
// regexp is RE2, which has no lookbehind, and \b counts _ as a word
// character — so an anchored pattern silently fails to mask the digits and hex
// inside cmd_1787331916689_be9e0d5e, and every command line becomes its own
// template. Dropping the anchors is what makes those group.
//
// Alternation ORDER is load-bearing: quoted and parenthesised runs are
// consumed whole before their contents can be masked piecemeal, and hex
// precedes digits so a long hex token is replaced whole rather than shredded
// into per-digit marks.
var varTokenRE = regexp.MustCompile(
	`"[^"]*"` +
		`|'[^']*'` +
		`|\([^)]{0,60}\)` +
		`|(?:0x)?[0-9a-fA-F]{8,}` +
		`|\d+(?:\.\d+)?(?:ms|us|ns|min|h|s|G|M|K|B)?` +
		`|/[\w./-]{4,}`)

var numTokenRE = regexp.MustCompile(`^\d+$`)

// Templatize reduces one line body to the shape it shares with its repeats,
// KEEPING every token it removed. The values ARE the answer here, so nothing
// is truncated away.
func Templatize(body string) (template string, slots []string) {
	template = varTokenRE.ReplaceAllStringFunc(body, func(m string) string {
		slots = append(slots, m)
		return slotMark
	})
	return template, slots
}

// TemplateGroup is every line in the set that shares a template.
type TemplateGroup struct {
	Template string
	// Rows holds the captured slot values, one row per line. Every row has
	// the same arity — a differing arity forms a separate group.
	Rows [][]string
	// Lines holds the 1-based RAW line number of each row, parallel to Rows,
	// so a value stays traceable to the exact line it came from.
	Lines []int
}

// Count reports how many raw lines the group covers.
func (g TemplateGroup) Count() int { return len(g.Rows) }

// stripLinePrefix removes the journald identity and any bare timestamp. Two
// lines emitted a second apart must not template differently for that reason
// alone.
func stripLinePrefix(l string) string {
	body := l
	if m := journaldRE.FindStringSubmatch(l); m != nil {
		body = l[len(m[0]):]
	}
	return strings.TrimSpace(tsPrefixRE.ReplaceAllString(body, ""))
}

// GroupTemplates groups lines by template in FIRST-OCCURRENCE order.
//
// Grouping is deliberately NOT restricted to consecutive runs. On real output
// the repeated lines are interleaved, so a consecutive-run grouping produces
// groups of size one and collapses nothing. Measured on a live 800-line
// window: 1.1x with run grouping, 3.2x without.
//
// firstLine is the 1-based raw line number of lines[0].
func GroupTemplates(lines []string, firstLine int) []TemplateGroup {
	at := map[string]int{}
	out := []TemplateGroup{}
	for i, l := range lines {
		body := stripLinePrefix(l)
		if body == "" {
			continue
		}
		tmpl, slots := Templatize(body)
		key := tmpl + arityMark + strconv.Itoa(len(slots))
		j, seen := at[key]
		if !seen {
			j = len(out)
			at[key] = j
			out = append(out, TemplateGroup{Template: tmpl})
		}
		out[j].Rows = append(out[j].Rows, slots)
		out[j].Lines = append(out[j].Lines, firstLine+i)
	}
	return out
}

// renderLineSet compresses a group's raw line numbers into ascending runs and
// renders them as one bracketed span: [L42] for a singleton, [L1-16] for one
// contiguous run, [L1,16,17-30] for a scattered set. GroupTemplates groups
// non-consecutive lines on purpose, so a min..max span would name a range
// that is mostly NOT the group — the run list is the honest shape.
//
// Past spanRunCap runs the span lists the first ones and folds the rest into
// a trailing +N counting the omitted lines. Every number named is still a
// real raw line number that read_block resolves byte-for-byte against the
// spill.
func renderLineSet(lines []int) string {
	sorted := append([]int(nil), lines...)
	sort.Ints(sorted)

	type run struct{ lo, hi int }
	runs := []run{}
	for _, n := range sorted {
		if last := len(runs) - 1; last >= 0 && runs[last].hi+1 == n {
			runs[last].hi = n
			continue
		}
		runs = append(runs, run{n, n})
	}

	listed, overflow := runs, 0
	if len(runs) > spanRunCap {
		listed = runs[:spanRunCap]
		for _, r := range runs[spanRunCap:] {
			overflow += r.hi - r.lo + 1
		}
	}

	var b strings.Builder
	b.WriteByte('[')
	for i, r := range listed {
		if i == 0 {
			b.WriteByte('L')
		} else {
			b.WriteByte(',')
		}
		if r.lo == r.hi {
			fmt.Fprintf(&b, "%d", r.lo)
		} else {
			fmt.Fprintf(&b, "%d-%d", r.lo, r.hi)
		}
	}
	if overflow > 0 {
		fmt.Fprintf(&b, " +%d", overflow)
	}
	b.WriteByte(']')
	return b.String()
}

// RenderTemplateGroup renders one group as its template plus one row per
// genuinely-varying slot.
//
// A slot whose value never changes across the group is folded back into the
// template LITERALLY, so the text stays readable instead of degenerating into
// a row of holes with a table underneath restating constants.
//
// A group of ONE renders as its single line with no count suffix — that is
// what keeps a lone verdict ("BUILD FAILED", "panic:", "exit status 1")
// visible verbatim instead of summarised away.
func RenderTemplateGroup(g TemplateGroup, indent string) []string {
	if len(g.Rows) == 0 {
		return nil
	}
	arity := len(g.Rows[0])

	varying := []int{}
	fixed := map[int]string{}
	for i := 0; i < arity; i++ {
		first, same := g.Rows[0][i], true
		for _, r := range g.Rows {
			if r[i] != first {
				same = false
				break
			}
		}
		if same {
			fixed[i] = first
		} else {
			varying = append(varying, i)
		}
	}

	// varying is built ascending, so its index is the slot's display ordinal.
	ordinal := map[int]int{}
	for n, i := range varying {
		ordinal[i] = n + 1
	}

	parts := strings.Split(g.Template, slotMark)
	var head strings.Builder
	for idx, p := range parts {
		head.WriteString(p)
		if idx == len(parts)-1 {
			continue
		}
		if v, ok := fixed[idx]; ok {
			head.WriteString(v)
		} else {
			fmt.Fprintf(&head, "▪%d", ordinal[idx])
		}
	}

	span := renderLineSet(g.Lines)

	line := fmt.Sprintf("%s%-13s %s", indent, span, strings.TrimSpace(head.String()))
	if len(g.Rows) > 1 {
		line += fmt.Sprintf("  ×%d", len(g.Rows))
	}
	out := []string{line}

	for _, i := range varying {
		seen := map[string]bool{}
		uniq := []string{}
		for _, r := range g.Rows {
			if !seen[r[i]] {
				seen[r[i]] = true
				uniq = append(uniq, r[i])
			}
		}
		out = append(out, fmt.Sprintf("%s    ▪%d: %s",
			indent, ordinal[i], describeSlot(uniq, len(g.Rows))))
	}
	return out
}

// describeSlot renders one varying slot's values. CARDINALITY decides the
// shape, because the three cases carry completely different information:
//
//   - a monotonic numeric slot is a COUNTER, and its range is the whole point;
//   - an all-distinct slot with many values is OPAQUE — pids, request ids —
//     and listing them is pure noise;
//   - a small value SET is the informative case.
func describeSlot(uniq []string, nrows int) string {
	if len(uniq) > 2 && allNumeric(uniq) {
		if nums, ok := parseAll(uniq); ok && strictlyAscending(nums) {
			shape := "ascending"
			if stepsOfOne(nums) {
				shape = "+1 each"
			}
			return fmt.Sprintf("%d..%d  (%d values, %s)",
				nums[0], nums[len(nums)-1], len(nums), shape)
		}
	}
	if len(uniq) == nrows && len(uniq) > 6 {
		sample := strings.Join(uniq[:2], " ")
		if len(sample) > 52 {
			sample = sample[:52]
		}
		return fmt.Sprintf("%d distinct  (e.g. %s)", len(uniq), sample)
	}

	joined := strings.Join(uniq, " ")
	if len(joined) <= slotWidth {
		return joined
	}
	// Build the prefix by measured length rather than by appending to a slice
	// and re-joining — append can alias the backing array and silently corrupt
	// the very values being described.
	shown, n := []string{}, 0
	for _, u := range uniq {
		add := len(u)
		if len(shown) > 0 {
			add++
		}
		if n+add > slotWidth-18 {
			break
		}
		shown = append(shown, u)
		n += add
	}
	return fmt.Sprintf("%s  (+%d more, %d distinct)",
		strings.Join(shown, " "), len(uniq)-len(shown), len(uniq))
}

func allNumeric(vals []string) bool {
	for _, v := range vals {
		if !numTokenRE.MatchString(v) {
			return false
		}
	}
	return true
}

func parseAll(vals []string) ([]int, bool) {
	out := make([]int, 0, len(vals))
	for _, v := range vals {
		n, err := strconv.Atoi(v)
		if err != nil {
			return nil, false
		}
		out = append(out, n)
	}
	return out, true
}

func strictlyAscending(nums []int) bool {
	for i := 1; i < len(nums); i++ {
		if nums[i] <= nums[i-1] {
			return false
		}
	}
	return true
}

func stepsOfOne(nums []int) bool {
	for i := 1; i < len(nums); i++ {
		if nums[i]-nums[i-1] != 1 {
			return false
		}
	}
	return true
}
