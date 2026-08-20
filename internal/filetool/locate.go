package filetool

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

const locateCap = 501

type locateMatch struct {
	Path  string `json:"path,omitempty"`
	Line  int    `json:"line"`
	Text  string `json:"text"`
	Start int    `json:"start,omitempty"`
	End   int    `json:"end,omitempty"`
}

func (h *Handler) locate(ctx context.Context, request Request) (any, error) {
	if strings.ContainsAny(request.Path, "*?[]{}") {
		return nil, fmt.Errorf("glob metacharacters are not allowed in locate paths")
	}
	path, err := h.resolve(request.Path)
	if err != nil {
		return nil, err
	}
	if request.Pattern == "" {
		return nil, fmt.Errorf("pattern is required")
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	fixed := false
	expression, compileErr := regexp.Compile(request.Pattern)
	warning := ""
	if info.IsDir() {
		fixed = true
		expression = regexp.MustCompile(regexp.QuoteMeta(request.Pattern))
		warning = "directory locate uses bounded fixed-string repository search"
	} else if compileErr != nil {
		fixed = true
		warning = "invalid regex retried once as fixed string"
		expression = regexp.MustCompile(regexp.QuoteMeta(request.Pattern))
	}
	if _, err := exec.LookPath("rg"); err == nil {
		matches, rgErr := locateRG(ctx, path, request.Pattern, fixed, request.Context, h.confiner.Permit)
		if rgErr == nil {
			return textJSON(map[string]any{"path": path, "matches": matches, "capped": len(matches) == locateCap, "warning": warning, "engine": "rg"})
		}
	}
	matches, err := locateGo(path, expression, request.Context, h.confiner.Permit)
	if err != nil {
		return nil, err
	}
	return textJSON(map[string]any{"path": path, "matches": matches, "capped": len(matches) == locateCap, "warning": warning, "engine": "go"})
}

func locateRG(ctx context.Context, path, pattern string, fixed bool, contextLines int, permit func(string) error) ([]locateMatch, error) {
	args := []string{"--json", "--line-number", "--max-count", fmt.Sprint(locateCap)}
	if contextLines > 0 {
		args = append(args, "--context", fmt.Sprint(contextLines))
	}
	if fixed {
		args = append(args, "--fixed-strings")
	}
	args = append(args, pattern, path)
	command := exec.CommandContext(ctx, "rg", args...)
	output, err := command.Output()
	if exit, ok := err.(*exec.ExitError); ok && exit.ExitCode() == 1 {
		return []locateMatch{}, nil
	}
	if err != nil {
		return nil, err
	}
	var matches []locateMatch
	scanner := bufio.NewScanner(bytes.NewReader(output))
	for scanner.Scan() && len(matches) < locateCap {
		var event struct {
			Type string `json:"type"`
			Data struct {
				Path struct {
					Text string `json:"text"`
				} `json:"path"`
				Lines struct {
					Text string `json:"text"`
				} `json:"lines"`
				LineNumber int `json:"line_number"`
				Submatches []struct {
					Start int `json:"start"`
					End   int `json:"end"`
				} `json:"submatches"`
			} `json:"data"`
		}
		if json.Unmarshal(scanner.Bytes(), &event) != nil || event.Type != "match" {
			continue
		}
		if err := permit(event.Data.Path.Text); err != nil {
			continue
		}
		match := locateMatch{Path: event.Data.Path.Text, Line: event.Data.LineNumber, Text: strings.TrimSuffix(event.Data.Lines.Text, "\n")}
		if len(event.Data.Submatches) > 0 {
			match.Start, match.End = event.Data.Submatches[0].Start, event.Data.Submatches[0].End
		}
		matches = append(matches, match)
	}
	return matches, scanner.Err()
}

func locateGo(path string, expression *regexp.Regexp, contextLines int, permit func(string) error) ([]locateMatch, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return locateGoFile(path, expression, contextLines, locateCap)
	}
	var matches []locateMatch
	err = filepath.WalkDir(path, func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if entry.IsDir() {
			if entry.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if len(matches) >= locateCap {
			return fs.SkipAll
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() || info.Size() > 8*1024*1024 {
			return nil
		}
		found, err := locateGoFile(current, expression, contextLines, locateCap-len(matches))
		if err == nil {
			matches = append(matches, found...)
		}
		return nil
	})
	return matches, err
}

func locateGoFile(path string, expression *regexp.Regexp, contextLines, remaining int) ([]locateMatch, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var lines []string
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	var matches []locateMatch
	for index, line := range lines {
		indices := expression.FindStringIndex(line)
		if indices == nil {
			continue
		}
		startLine, endLine := max(0, index-contextLines), min(len(lines)-1, index+contextLines)
		text := strings.Join(lines[startLine:endLine+1], "\n")
		matches = append(matches, locateMatch{Path: path, Line: index + 1, Text: text, Start: indices[0], End: indices[1]})
		if len(matches) >= remaining {
			break
		}
	}
	return matches, nil
}
