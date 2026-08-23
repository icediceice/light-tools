package file

import (
	"bytes"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

type SpanResult struct {
	RequestedStart int  `json:"requested_start"`
	RequestedEnd   int  `json:"requested_end"`
	AppliedStart   int  `json:"applied_start"`
	AppliedEnd     int  `json:"applied_end"`
	Relocated      bool `json:"relocated,omitempty"`
	Adjusted       bool `json:"adjusted,omitempty"`
}

type TransformResult struct {
	Data         []byte       `json:"-"`
	Replacements int          `json:"replacements,omitempty"`
	Diff         string       `json:"diff,omitempty"`
	Spans        []SpanResult `json:"spans,omitempty"`
}

type resolvedEdit struct {
	start       int
	end         int
	replacement []string
	result      SpanResult
}

func Transform(m Mutation, preimage []byte) (TransformResult, error) {
	if err := m.Validate(); err != nil {
		return TransformResult{}, err
	}
	if m.Verb == VerbEdit || m.Verb == VerbRewrite {
		return TransformEdits([]Mutation{m}, preimage)
	}
	crlf := bytes.Contains(preimage, []byte("\r\n"))
	normalized := strings.ReplaceAll(string(preimage), "\r\n", "\n")
	var (
		candidate string
		replaced  int
		err       error
	)
	switch m.Verb {
	case VerbWrite:
		candidate = normalizeInput(*m.Content)
	case VerbSed:
		candidate, replaced, err = sedText(normalized, m)
	default:
		return TransformResult{}, fmt.Errorf("verb %q is not a content transform", m.Verb)
	}
	if err != nil {
		return TransformResult{}, err
	}
	if crlf {
		candidate = strings.ReplaceAll(candidate, "\n", "\r\n")
	}
	result := TransformResult{Data: []byte(candidate), Replacements: replaced}
	if m.DryRun {
		result.Diff = UnifiedDiff(m.Path, string(preimage), candidate)
	}
	return result, nil
}

// TransformEdits resolves every span against one preimage and applies the
// non-overlapping set bottom-up, so line numbers never drift.
func TransformEdits(mutations []Mutation, preimage []byte) (TransformResult, error) {
	if len(mutations) == 0 {
		return TransformResult{}, fmt.Errorf("no edits")
	}
	crlf := bytes.Contains(preimage, []byte("\r\n"))
	normalized := strings.ReplaceAll(string(preimage), "\r\n", "\n")
	lines := strings.Split(normalized, "\n")
	var edits []resolvedEdit
	allowUnbalanced := true
	dryRun := true
	path := mutations[0].Path
	for _, mutation := range mutations {
		if mutation.Verb != VerbEdit && mutation.Verb != VerbRewrite {
			return TransformResult{}, fmt.Errorf("mixed non-edit mutation in edit batch")
		}
		if err := mutation.Validate(); err != nil {
			return TransformResult{}, err
		}
		if !mutation.AllowUnbalanced {
			allowUnbalanced = false
		}
		if !mutation.DryRun {
			dryRun = false
		}
		spans := mutation.Spans
		if len(spans) == 0 {
			spans = []EditSpan{{
				StartLine: mutation.StartLine, EndLine: mutation.EndLine,
				StartGuard: mutation.StartGuard, EndGuard: mutation.EndGuard, NewString: *mutation.NewString,
			}}
		}
		for _, span := range spans {
			edit, err := resolveEdit(lines, span)
			if err != nil {
				return TransformResult{}, err
			}
			edits = append(edits, edit)
		}
	}
	sort.SliceStable(edits, func(i, j int) bool { return edits[i].start < edits[j].start })
	for index := 1; index < len(edits); index++ {
		if edits[index].start < edits[index-1].end {
			return TransformResult{}, fmt.Errorf("edit spans overlap at lines %d..%d", edits[index].start+1, edits[index-1].end)
		}
	}
	candidateLines := append([]string(nil), lines...)
	for index := len(edits) - 1; index >= 0; index-- {
		edit := edits[index]
		candidateLines = replaceLines(candidateLines, edit.start, edit.end, edit.replacement)
	}
	candidate := strings.Join(candidateLines, "\n")
	if !allowUnbalanced && !balanced(candidate) {
		if len(edits) != 1 || !balanced(strings.Join(edits[0].replacement, "\n")) {
			return TransformResult{}, fmt.Errorf("replacement leaves brackets unbalanced; pass allow_unbalanced:true to override")
		}
		adjusted, ok, err := autoSnap(lines, edits[0])
		if err != nil {
			return TransformResult{}, err
		}
		if !ok {
			return TransformResult{}, fmt.Errorf("replacement leaves brackets unbalanced near end_line %d; check up to ±3 closer-only lines or pass allow_unbalanced:true", edits[0].end)
		}
		edits[0] = adjusted
		candidate = strings.Join(replaceLines(lines, adjusted.start, adjusted.end, adjusted.replacement), "\n")
	}
	if crlf {
		candidate = strings.ReplaceAll(candidate, "\n", "\r\n")
	}
	results := make([]SpanResult, len(edits))
	for index := range edits {
		results[index] = edits[index].result
	}
	result := TransformResult{Data: []byte(candidate), Spans: results}
	if dryRun {
		result.Diff = UnifiedDiff(path, string(preimage), candidate)
	}
	return result, nil
}

func resolveEdit(lines []string, span EditSpan) (resolvedEdit, error) {
	if span.StartLine < 1 {
		return resolvedEdit{}, fmt.Errorf("start_line must be >= 1")
	}
	start := span.StartLine - 1
	if start >= len(lines) {
		return resolvedEdit{}, fmt.Errorf("start_line %d outside 1..%d", span.StartLine, len(lines))
	}
	relocated := false
	if span.StartGuard != "" && lines[start] != span.StartGuard {
		found := locateGuard(lines, span.StartGuard, span.EndGuard)
		if found < 0 {
			return resolvedEdit{}, fmt.Errorf("start_guard no longer matches uniquely")
		}
		start, relocated = found, true
	}
	end := span.EndLine
	if end == 0 {
		if span.EndGuard == "" {
			end = start + 1
		} else {
			found := -1
			for index := start; index < len(lines); index++ {
				if lines[index] == span.EndGuard {
					found = index + 1
					break
				}
			}
			if found < 0 {
				return resolvedEdit{}, fmt.Errorf("end_guard not found after start_guard")
			}
			end = found
		}
	} else if relocated {
		width := span.EndLine - (span.StartLine - 1)
		end = start + width
	}
	if end < start+1 || end > len(lines) {
		return resolvedEdit{}, fmt.Errorf("edit span %d..%d outside 1..%d", start+1, end, len(lines))
	}
	if span.EndGuard != "" && lines[end-1] != span.EndGuard {
		found := -1
		for index := start; index < len(lines); index++ {
			if lines[index] == span.EndGuard {
				found = index + 1
				break
			}
		}
		if found < 0 {
			return resolvedEdit{}, fmt.Errorf("end_guard no longer matches")
		}
		end = found
		relocated = true
	}
	return resolvedEdit{
		start: start, end: end, replacement: strings.Split(normalizeInput(span.NewString), "\n"),
		result: SpanResult{
			RequestedStart: span.StartLine, RequestedEnd: span.EndLine,
			AppliedStart: start + 1, AppliedEnd: end, Relocated: relocated,
		},
	}, nil
}

func autoSnap(lines []string, edit resolvedEdit) (resolvedEdit, bool, error) {
	var candidates []resolvedEdit
	for delta := -3; delta <= 3; delta++ {
		if delta == 0 {
			continue
		}
		end := edit.end + delta
		if end <= edit.start || end > len(lines) || !closerRange(lines, edit.end, end) {
			continue
		}
		candidate := strings.Join(replaceLines(lines, edit.start, end, edit.replacement), "\n")
		if balanced(candidate) {
			adjusted := edit
			adjusted.end = end
			adjusted.result.AppliedEnd = end
			adjusted.result.Adjusted = true
			candidates = append(candidates, adjusted)
		}
	}
	if len(candidates) > 1 {
		return resolvedEdit{}, false, fmt.Errorf("auto-snap is ambiguous across %d closer-only candidates", len(candidates))
	}
	if len(candidates) == 1 {
		return candidates[0], true, nil
	}
	return resolvedEdit{}, false, nil
}

func closerRange(lines []string, from, to int) bool {
	start, end := from, to
	if start > end {
		start, end = end, start
	}
	for index := start; index < end; index++ {
		if index < 0 || index >= len(lines) || !closerOnly(lines[index]) {
			return false
		}
	}
	return true
}

func closerOnly(line string) bool {
	value := strings.TrimSpace(line)
	if value == "" {
		return true
	}
	value = strings.Trim(value, "}]);,")
	return strings.TrimSpace(value) == ""
}

func replaceLines(lines []string, start, end int, replacement []string) []string {
	updated := make([]string, 0, len(lines)-(end-start)+len(replacement))
	updated = append(updated, lines[:start]...)
	updated = append(updated, replacement...)
	updated = append(updated, lines[end:]...)
	return updated
}

func sedText(preimage string, m Mutation) (string, int, error) {
	if m.Regex {
		expression, err := regexp.Compile(*m.Find)
		if err != nil {
			return "", 0, fmt.Errorf("invalid regular expression: %w", err)
		}
		matches := expression.FindAllStringIndex(preimage, -1)
		if err := validateMatchCount(len(matches), m); err != nil {
			return "", 0, err
		}
		limit := len(matches)
		if !m.All && m.Count == 0 {
			limit = 1
		}
		if m.Count > 0 {
			limit = m.Count
		}
		replaced := 0
		result := expression.ReplaceAllStringFunc(preimage, func(match string) string {
			if replaced >= limit {
				return match
			}
			replaced++
			return expression.ReplaceAllString(match, *m.Replace)
		})
		return result, replaced, nil
	}
	matches := strings.Count(preimage, *m.Find)
	if err := validateMatchCount(matches, m); err != nil {
		return "", 0, err
	}
	limit := 1
	if m.All {
		limit = -1
	} else if m.Count > 0 {
		limit = m.Count
	}
	return strings.Replace(preimage, *m.Find, *m.Replace, limit), matchesApplied(matches, limit), nil
}

func validateMatchCount(matches int, m Mutation) error {
	if matches == 0 {
		return fmt.Errorf("not_found: find text did not match")
	}
	if m.Count > 0 && matches != m.Count {
		return fmt.Errorf("count mismatch: expected %d matches, found %d", m.Count, matches)
	}
	if matches > 1 && !m.All && m.Count == 0 {
		return fmt.Errorf("ambiguous: find text matched %d times", matches)
	}
	return nil
}

func matchesApplied(matches, limit int) int {
	if limit < 0 || matches < limit {
		return matches
	}
	return limit
}

func locateGuard(lines []string, start, end string) int {
	found := -1
	for index, line := range lines {
		if line != start {
			continue
		}
		if end != "" {
			matchedEnd := false
			for cursor := index; cursor < len(lines); cursor++ {
				if lines[cursor] == end {
					matchedEnd = true
					break
				}
			}
			if !matchedEnd {
				continue
			}
		}
		if found >= 0 {
			return -1
		}
		found = index
	}
	return found
}

func normalizeInput(value string) string {
	return strings.ReplaceAll(value, "\r\n", "\n")
}

func balanced(value string) bool {
	var stack []rune
	quote := rune(0)
	escaped := false
	for _, character := range value {
		if quote != 0 {
			if escaped {
				escaped = false
			} else if character == '\\' {
				escaped = true
			} else if character == quote {
				quote = 0
			}
			continue
		}
		if character == '\'' || character == '"' || character == '`' {
			quote = character
			continue
		}
		switch character {
		case '(', '[', '{':
			stack = append(stack, character)
		case ')', ']', '}':
			if len(stack) == 0 || !pair(stack[len(stack)-1], character) {
				return false
			}
			stack = stack[:len(stack)-1]
		}
	}
	return len(stack) == 0 && quote == 0
}

func pair(open, close rune) bool {
	return open == '(' && close == ')' || open == '[' && close == ']' || open == '{' && close == '}'
}

func UnifiedDiff(path, before, after string) string {
	if before == after {
		return ""
	}
	var builder strings.Builder
	fmt.Fprintf(&builder, "--- %s\n+++ %s\n", path, path)
	beforeLines := strings.Split(before, "\n")
	afterLines := strings.Split(after, "\n")
	maximum := len(beforeLines)
	if len(afterLines) > maximum {
		maximum = len(afterLines)
	}
	for index := 0; index < maximum; index++ {
		var oldLine, newLine string
		oldOK, newOK := index < len(beforeLines), index < len(afterLines)
		if oldOK {
			oldLine = beforeLines[index]
		}
		if newOK {
			newLine = afterLines[index]
		}
		if oldOK && newOK && oldLine == newLine {
			fmt.Fprintf(&builder, " %s\n", oldLine)
			continue
		}
		if oldOK {
			fmt.Fprintf(&builder, "-%s\n", oldLine)
		}
		if newOK {
			fmt.Fprintf(&builder, "+%s\n", newLine)
		}
	}
	return builder.String()
}
