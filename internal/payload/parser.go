// Package payload parses the sealed mutation payload format.
package payload

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	fileop "github.com/icediceice/light-tools/internal/file"
	"github.com/icediceice/light-tools/internal/portable"
)

type parsedMutation struct {
	value fileop.Mutation
	seen  map[string]bool
}

type PartialError struct {
	Diagnostic *portable.DiagnosticError
	GotLines   int
}

func (e *PartialError) Error() string { return e.Diagnostic.Error() }
func (e *PartialError) Unwrap() error { return e.Diagnostic }

func Parse(input string) ([]fileop.Mutation, error) {
	normalized := strings.ReplaceAll(input, "\r\n", "\n")
	lines := strings.Split(normalized, "\n")
	// At="payload" names the source, so a staged partial and a terminal parse
	// error render the same at: prefix ahead of their line/column.
	diagnostic := func(caret int, message string) error {
		caretError := portable.NewCaretError("E_PAYLOAD", message, normalized, caret)
		caretError.At = "payload"
		return caretError
	}
	var (
		result     []fileop.Mutation
		current    *parsedMutation
		bodyField  string
		body       []string
		terminator = "<<LF-END>>"
		offset     int
	)

	flush := func() error {
		if current == nil {
			return nil
		}
		if current.value.Verb == "" && current.value.Path == "" {
			return nil
		}
		if err := current.value.Validate(); err != nil {
			return err
		}
		result = append(result, current.value)
		current = nil
		return nil
	}
	ensure := func() *parsedMutation {
		if current == nil {
			current = &parsedMutation{seen: make(map[string]bool)}
		}
		return current
	}
	assignBody := func() error {
		value := strings.Join(body, "\n")
		target := ensure()
		switch bodyField {
		case "content":
			target.value.Content = &value
		case "new_string":
			target.value.NewString = &value
		case "find":
			target.value.Find = &value
		case "replace":
			target.value.Replace = &value
		case "spans":
			if err := json.Unmarshal([]byte(value), &target.value.Spans); err != nil {
				return fmt.Errorf("@spans requires a JSON array: %w", err)
			}
			if len(target.value.Spans) == 0 {
				return fmt.Errorf("@spans cannot be empty")
			}
		}
		target.seen[bodyField] = true
		bodyField = ""
		body = nil
		terminator = "<<LF-END>>"
		return nil
	}

	for index, line := range lines {
		lineOffset := offset
		offset += len(line) + 1
		if bodyField != "" {
			if line == terminator {
				if err := assignBody(); err != nil {
					return nil, diagnostic(lineOffset, err.Error())
				}
			} else {
				body = append(body, line)
			}
			continue
		}
		if line == "" && index == len(lines)-1 {
			continue
		}
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, "@") {
			return nil, diagnostic(lineOffset, "expected @header")
		}
		header := strings.TrimPrefix(line, "@")
		key, value, hasValue := strings.Cut(header, " ")
		if key == "" || strings.TrimSpace(key) != key {
			return nil, diagnostic(lineOffset, "invalid header")
		}
		value = strings.TrimSpace(value)
		if key == "file" {
			if !hasValue || value == "" {
				return nil, diagnostic(lineOffset, "@file requires a path")
			}
			if err := flush(); err != nil {
				return nil, diagnostic(lineOffset, err.Error())
			}
			current = &parsedMutation{seen: map[string]bool{"path": true}}
			current.value.Path = value
			continue
		}
		target := ensure()
		if key == "until" {
			if !hasValue || value == "" {
				return nil, diagnostic(lineOffset, "@until requires a token")
			}
			terminator = value
			continue
		}
		if key == "content" || key == "new_string" || key == "find" || key == "replace" || key == "spans" {
			if hasValue {
				return nil, diagnostic(lineOffset, "@"+key+" must be a bare body header")
			}
			if target.seen[key] {
				return nil, diagnostic(lineOffset, "duplicate @"+key)
			}
			bodyField = key
			body = nil
			continue
		}
		if target.seen[key] {
			return nil, diagnostic(lineOffset, "duplicate @"+key)
		}
		target.seen[key] = true
		if err := assignScalar(&target.value, key, value, hasValue); err != nil {
			return nil, diagnostic(lineOffset, err.Error())
		}
	}
	if bodyField != "" {
		caret := len(normalized)
		unterminated := portable.NewCaretError("E_PAYLOAD", "unterminated @"+bodyField+" body; expected exact "+terminator, normalized, caret)
		unterminated.At = "payload"
		return nil, &PartialError{
			Diagnostic: unterminated,
			GotLines:   len(lines),
		}
	}
	if err := flush(); err != nil {
		return nil, diagnostic(len(normalized), err.Error())
	}
	if len(result) == 0 {
		return nil, diagnostic(0, "payload contains no mutation")
	}
	return result, nil
}

func assignScalar(m *fileop.Mutation, key, value string, hasValue bool) error {
	requireValue := func() error {
		if !hasValue || value == "" {
			return fmt.Errorf("@%s requires a value", key)
		}
		return nil
	}
	parseBool := func() (bool, error) {
		if err := requireValue(); err != nil {
			return false, err
		}
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			return false, fmt.Errorf("@%s requires true or false", key)
		}
		return parsed, nil
	}
	parseInt := func() (int, error) {
		if err := requireValue(); err != nil {
			return 0, err
		}
		parsed, err := strconv.Atoi(value)
		if err != nil {
			return 0, fmt.Errorf("@%s requires an integer", key)
		}
		return parsed, nil
	}

	switch key {
	case "payload_version":
		if value != "1" {
			return fmt.Errorf("unsupported payload_version %q", value)
		}
	case "verb":
		if err := requireValue(); err != nil {
			return err
		}
		m.Verb = fileop.Verb(value)
	case "path":
		if err := requireValue(); err != nil {
			return err
		}
		m.Path = value
	case "target", "to":
		if err := requireValue(); err != nil {
			return err
		}
		m.Target = value
	case "start_line":
		parsed, err := parseInt()
		m.StartLine = parsed
		return err
	case "end_line":
		parsed, err := parseInt()
		m.EndLine = parsed
		return err
	case "count":
		parsed, err := parseInt()
		m.Count = parsed
		return err
	case "version":
		parsed, err := parseInt()
		m.Version = parsed
		return err
	case "start_guard":
		m.StartGuard = value
	case "end_guard":
		m.EndGuard = value
	case "expected_sha":
		m.ExpectedSHA = value
	case "all":
		parsed, err := parseBool()
		m.All = parsed
		return err
	case "regex":
		parsed, err := parseBool()
		m.Regex = parsed
		return err
	case "dry_run":
		parsed, err := parseBool()
		m.DryRun = parsed
		return err
	case "overwrite":
		parsed, err := parseBool()
		m.Overwrite = parsed
		return err
	case "allow_unbalanced":
		parsed, err := parseBool()
		m.AllowUnbalanced = parsed
		return err
	case "force":
		parsed, err := parseBool()
		m.Force = parsed
		return err
	default:
		return fmt.Errorf("unknown header @%s", key)
	}
	return nil
}
