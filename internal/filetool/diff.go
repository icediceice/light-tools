package filetool

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

type diffLine struct {
	kind byte
	text string
}

func unifiedDiff(from, to, before, after string, contextLines int) string {
	if before == after {
		return ""
	}
	oldLines, _ := splitTextLines(before)
	newLines, _ := splitTextLines(after)
	operations := lineDiff(oldLines, newLines)
	if contextLines < 0 {
		contextLines = 0
	}

	var changes []int
	for index, operation := range operations {
		if operation.kind != ' ' {
			changes = append(changes, index)
		}
	}
	if len(changes) == 0 {
		return ""
	}

	type interval struct{ start, end int }
	var hunks []interval
	for _, change := range changes {
		start := max(0, change-contextLines)
		end := min(len(operations), change+contextLines+1)
		if len(hunks) > 0 && start <= hunks[len(hunks)-1].end {
			if end > hunks[len(hunks)-1].end {
				hunks[len(hunks)-1].end = end
			}
			continue
		}
		hunks = append(hunks, interval{start: start, end: end})
	}

	var builder strings.Builder
	fmt.Fprintf(&builder, "--- %s\n+++ %s\n", from, to)
	for _, hunk := range hunks {
		oldBefore, newBefore := consumedLines(operations[:hunk.start])
		oldCount, newCount := consumedLines(operations[hunk.start:hunk.end])
		oldStart, newStart := oldBefore+1, newBefore+1
		if oldCount == 0 {
			oldStart--
		}
		if newCount == 0 {
			newStart--
		}
		fmt.Fprintf(&builder, "@@ -%d,%d +%d,%d @@\n", oldStart, oldCount, newStart, newCount)
		for _, operation := range operations[hunk.start:hunk.end] {
			builder.WriteByte(operation.kind)
			builder.WriteString(operation.text)
			builder.WriteByte('\n')
		}
	}
	return builder.String()
}

func lineDiff(oldLines, newLines []string) []diffLine {
	n, m := len(oldLines), len(newLines)
	if n*m > 4_000_000 {
		operations := make([]diffLine, 0, n+m)
		for _, line := range oldLines {
			operations = append(operations, diffLine{kind: '-', text: line})
		}
		for _, line := range newLines {
			operations = append(operations, diffLine{kind: '+', text: line})
		}
		return operations
	}
	lcs := make([][]int, n+1)
	for index := range lcs {
		lcs[index] = make([]int, m+1)
	}
	for oldIndex := n - 1; oldIndex >= 0; oldIndex-- {
		for newIndex := m - 1; newIndex >= 0; newIndex-- {
			if oldLines[oldIndex] == newLines[newIndex] {
				lcs[oldIndex][newIndex] = lcs[oldIndex+1][newIndex+1] + 1
			} else {
				lcs[oldIndex][newIndex] = max(lcs[oldIndex+1][newIndex], lcs[oldIndex][newIndex+1])
			}
		}
	}
	operations := make([]diffLine, 0, n+m)
	for oldIndex, newIndex := 0, 0; oldIndex < n || newIndex < m; {
		switch {
		case oldIndex < n && newIndex < m && oldLines[oldIndex] == newLines[newIndex]:
			operations = append(operations, diffLine{kind: ' ', text: oldLines[oldIndex]})
			oldIndex++
			newIndex++
		case oldIndex < n && (newIndex == m || lcs[oldIndex+1][newIndex] >= lcs[oldIndex][newIndex+1]):
			operations = append(operations, diffLine{kind: '-', text: oldLines[oldIndex]})
			oldIndex++
		default:
			operations = append(operations, diffLine{kind: '+', text: newLines[newIndex]})
			newIndex++
		}
	}
	return operations
}

func consumedLines(operations []diffLine) (oldCount, newCount int) {
	for _, operation := range operations {
		if operation.kind != '+' {
			oldCount++
		}
		if operation.kind != '-' {
			newCount++
		}
	}
	return oldCount, newCount
}

var hunkHeader = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@`)

type patchHunk struct {
	oldStart int
	oldLines []string
	newLines []string
}

func applyUnifiedPatch(base, patch string, fuzz int) (string, int, error) {
	if fuzz < 0 {
		return "", 0, fmt.Errorf("fuzz cannot be negative")
	}
	hunks, err := parsePatch(patch)
	if err != nil {
		return "", 0, err
	}
	lines, trailingNewline := splitTextLines(base)
	delta := 0
	for index, hunk := range hunks {
		expected := hunk.oldStart - 1 + delta
		position := matchingPosition(lines, hunk.oldLines, expected, fuzz)
		if position < 0 {
			return "", index, fmt.Errorf("patch hunk %d does not match within fuzz %d", index+1, fuzz)
		}
		replacement := append([]string(nil), hunk.newLines...)
		lines = append(lines[:position], append(replacement, lines[position+len(hunk.oldLines):]...)...)
		delta += len(hunk.newLines) - len(hunk.oldLines)
	}
	result := strings.Join(lines, "\n")
	if trailingNewline {
		result += "\n"
	}
	return result, len(hunks), nil
}

func parsePatch(patch string) ([]patchHunk, error) {
	lines := strings.Split(strings.ReplaceAll(patch, "\r\n", "\n"), "\n")
	var hunks []patchHunk
	for index := 0; index < len(lines); {
		match := hunkHeader.FindStringSubmatch(lines[index])
		if match == nil {
			index++
			continue
		}
		oldStart, _ := strconv.Atoi(match[1])
		oldCount := 1
		if match[2] != "" {
			oldCount, _ = strconv.Atoi(match[2])
		}
		hunk := patchHunk{oldStart: oldStart}
		index++
		for index < len(lines) && !strings.HasPrefix(lines[index], "@@ ") {
			line := lines[index]
			if strings.HasPrefix(line, "--- ") || strings.HasPrefix(line, "+++ ") {
				break
			}
			if line == `\ No newline at end of file` {
				index++
				continue
			}
			if line == "" && index == len(lines)-1 {
				break
			}
			if line == "" {
				return nil, fmt.Errorf("patch hunk has an unprefixed empty line")
			}
			switch line[0] {
			case ' ':
				hunk.oldLines = append(hunk.oldLines, line[1:])
				hunk.newLines = append(hunk.newLines, line[1:])
			case '-':
				hunk.oldLines = append(hunk.oldLines, line[1:])
			case '+':
				hunk.newLines = append(hunk.newLines, line[1:])
			default:
				return nil, fmt.Errorf("patch hunk contains invalid prefix %q", line[0])
			}
			index++
		}
		if len(hunk.oldLines) != oldCount {
			return nil, fmt.Errorf("patch hunk declares %d old lines but contains %d", oldCount, len(hunk.oldLines))
		}
		hunks = append(hunks, hunk)
	}
	if len(hunks) == 0 {
		return nil, fmt.Errorf("patch contains no unified hunks")
	}
	return hunks, nil
}

func matchingPosition(lines, expectedLines []string, expected, fuzz int) int {
	matches := func(position int) bool {
		if position < 0 || position+len(expectedLines) > len(lines) {
			return false
		}
		for index := range expectedLines {
			if lines[position+index] != expectedLines[index] {
				return false
			}
		}
		return true
	}
	if matches(expected) {
		return expected
	}
	for distance := 1; distance <= fuzz; distance++ {
		if matches(expected - distance) {
			return expected - distance
		}
		if matches(expected + distance) {
			return expected + distance
		}
	}
	return -1
}

func splitTextLines(value string) ([]string, bool) {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	trailing := strings.HasSuffix(value, "\n")
	if trailing {
		value = strings.TrimSuffix(value, "\n")
	}
	if value == "" {
		return nil, trailing
	}
	return strings.Split(value, "\n"), trailing
}
