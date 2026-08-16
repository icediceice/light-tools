package filetool

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
)

const locateCap = 501

type locateMatch struct {
	Line   int    `json:"line"`
	Text   string `json:"text"`
	Start  int    `json:"start,omitempty"`
	End    int    `json:"end,omitempty"`
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
	fixed := false
	expression, compileErr := regexp.Compile(request.Pattern)
	warning := ""
	if compileErr != nil {
		fixed = true
		warning = "invalid regex retried once as fixed string"
		expression = regexp.MustCompile(regexp.QuoteMeta(request.Pattern))
	}
	if _, err := exec.LookPath("rg"); err == nil {
		matches, rgErr := locateRG(ctx, path, request.Pattern, fixed)
		if rgErr == nil {
			return textJSON(map[string]any{"path": path, "matches": matches, "capped": len(matches) == locateCap, "warning": warning})
		}
	}
	matches, err := locateGo(path, expression)
	if err != nil {
		return nil, err
	}
	return textJSON(map[string]any{"path": path, "matches": matches, "capped": len(matches) == locateCap, "warning": warning, "engine": "go"})
}

func locateRG(ctx context.Context, path, pattern string, fixed bool) ([]locateMatch, error) {
	args := []string{"--json", "--line-number", "--max-count", fmt.Sprint(locateCap)}
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
				Lines struct{ Text string `json:"text"` } `json:"lines"`
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
		match := locateMatch{Line: event.Data.LineNumber, Text: strings.TrimSuffix(event.Data.Lines.Text, "\n")}
		if len(event.Data.Submatches) > 0 {
			match.Start, match.End = event.Data.Submatches[0].Start, event.Data.Submatches[0].End
		}
		matches = append(matches, match)
	}
	return matches, scanner.Err()
}

func locateGo(path string, expression *regexp.Regexp) ([]locateMatch, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var matches []locateMatch
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for line := 1; scanner.Scan() && len(matches) < locateCap; line++ {
		indices := expression.FindStringIndex(scanner.Text())
		if indices != nil {
			matches = append(matches, locateMatch{Line: line, Text: scanner.Text(), Start: indices[0], End: indices[1]})
		}
	}
	return matches, scanner.Err()
}
