package file

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"
)

type TransformResult struct {
	Data         []byte
	Replacements int
	Diff         string
}

func Transform(m Mutation, preimage []byte) (TransformResult, error) {
	if err := m.Validate(); err != nil {
		return TransformResult{}, err
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
	case VerbEdit, VerbRewrite:
		candidate, err = editText(normalized, m)
	case VerbSed:
		candidate, replaced, err = sedText(normalized, m)
	default:
		return TransformResult{}, fmt.Errorf("verb %q is not a content transform", m.Verb)
	}
	if err != nil {
		return TransformResult{}, err
	}
	if !m.AllowUnbalanced && (m.Verb == VerbEdit || m.Verb == VerbRewrite) && !balanced(candidate) {
		return TransformResult{}, fmt.Errorf("replacement leaves brackets unbalanced; pass allow_unbalanced:true to override")
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

func editText(preimage string, m Mutation) (string, error) {
	lines := strings.Split(preimage, "\n")
	start := m.StartLine - 1
	end := m.EndLine
	if end == 0 {
		end = m.StartLine
	}
	if start < 0 || start >= len(lines) || end < m.StartLine || end > len(lines) {
		return "", fmt.Errorf("edit span %d..%d outside 1..%d", m.StartLine, end, len(lines))
	}
	if m.StartGuard != "" && lines[start] != m.StartGuard {
		relocated := locateGuard(lines, m.StartGuard, m.EndGuard)
		if relocated < 0 {
			return "", fmt.Errorf("start_guard no longer matches")
		}
		width := end - start
		start = relocated
		end = relocated + width
		if end > len(lines) {
			return "", fmt.Errorf("relocated edit span exceeds file")
		}
	}
	if m.EndGuard != "" && lines[end-1] != m.EndGuard {
		return "", fmt.Errorf("end_guard no longer matches")
	}
	replacement := strings.Split(normalizeInput(*m.NewString), "\n")
	updated := make([]string, 0, len(lines)-(end-start)+len(replacement))
	updated = append(updated, lines[:start]...)
	updated = append(updated, replacement...)
	updated = append(updated, lines[end:]...)
	return strings.Join(updated, "\n"), nil
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
