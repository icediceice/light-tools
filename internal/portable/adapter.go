// Package portable contains the dependency-free invocation seam shared by the
// standalone MCP transport and tool handlers.
package portable

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Handler is the direct-return contract used by every registered tool.
type Handler func(context.Context, json.RawMessage) (any, error)

// Tool is a named MCP method and its implementation.
type Tool struct {
	Name        string
	InputSchema map[string]any
	Handler     Handler
}

// DiagnosticError is a stable, model-readable error envelope.
type DiagnosticError struct {
	Code       string
	Message    string
	At         string
	Fix        string
	Caret      int
	Line       int
	Column     int
	SourceLine string
}

// defaultFix is the standing remedy sentence for a code, rendered whenever a
// producer supplied no Fix of its own. Misuse codes tell the caller to change
// the call; platform codes tell it not to bother.
func defaultFix(code string) string {
	switch code {
	case "E_SCHEMA", "E_USAGE", "E_PAYLOAD", "E_VERB":
		return "correct the call arguments and retry"
	case "E_SYS", "E_INTERNAL":
		return "a platform fault, not a bad call — retry, and report it if it persists"
	default:
		return "read detail, then adjust the call"
	}
}

func (e *DiagnosticError) Error() string {
	code := e.Code
	if code == "" {
		code = "E_TOOL"
	}
	fix := e.Fix
	if fix == "" {
		fix = defaultFix(code)
	}
	if e.Line <= 0 || e.SourceLine == "" {
		at := e.At
		if at == "" {
			at = "unknown"
		}
		return fmt.Sprintf("error[%s]\n  at: %s\n  fix: %s\n  detail: %s", code, at, fix, e.Message)
	}
	prefix := e.SourceLine
	if e.Column > 1 {
		runes := []rune(e.SourceLine)
		end := e.Column - 1
		if end > len(runes) {
			end = len(runes)
		}
		prefix = string(runes[:end])
	} else {
		prefix = ""
	}
	var marker strings.Builder
	for _, character := range prefix {
		if character == '\t' {
			marker.WriteByte('\t')
		} else {
			marker.WriteByte(' ')
		}
	}
	marker.WriteByte('^')
	width := len(fmt.Sprint(e.Line))
	// A non-empty At prefixes the positional line rather than replacing it, so
	// the caller learns both WHICH source failed and WHERE inside it.
	position := fmt.Sprintf("line %d, column %d (byte %d)", e.Line, e.Column, e.Caret)
	if e.At != "" {
		position = e.At + ", " + position
	}
	return fmt.Sprintf(
		"error[%s]\n  at: %s\n  fix: %s\n  detail: %s\n  %*d | %s\n  %*s | %s",
		code, position, fix, e.Message, width, e.Line, e.SourceLine, width, "", marker.String(),
	)
}

// NewCaretError derives a source line and display column from a byte offset.
func NewCaretError(code, message, source string, offset int) *DiagnosticError {
	if offset < 0 {
		offset = 0
	}
	if offset > len(source) {
		offset = len(source)
	}
	before := source[:offset]
	line := strings.Count(before, "\n") + 1
	lineStart := strings.LastIndex(before, "\n") + 1
	lineEnd := strings.IndexByte(source[offset:], '\n')
	if lineEnd < 0 {
		lineEnd = len(source)
	} else {
		lineEnd += offset
	}
	sourceLine := source[lineStart:lineEnd]
	column := len([]rune(source[lineStart:offset])) + 1
	return &DiagnosticError{Code: code, Message: message, Caret: offset, Line: line, Column: column, SourceLine: sourceLine}
}

// inputVerb probes the call's verb so a decorated diagnostic can name the exact
// tool/verb pair. A malformed or verb-less call simply yields "".
func inputVerb(raw json.RawMessage) string {
	var probe struct {
		Verb string `json:"verb"`
	}
	if json.Unmarshal(raw, &probe) != nil {
		return ""
	}
	return probe.Verb
}

// resolveAt fills the envelope's at: field. Precedence: an explicit producer At
// wins verbatim, except a $-rooted schema path, which is re-rooted at the tool
// so the caller reads light_file.path rather than the anonymous $.path.
func resolveAt(tool, verb, at string) string {
	if at != "" {
		if strings.HasPrefix(at, "$") {
			return tool + strings.TrimPrefix(at, "$")
		}
		return at
	}
	if verb != "" {
		return tool + "/" + verb
	}
	return tool
}

// decorateDiagnostic is the single seam every Invoke error return passes
// through, so call attribution, repair history and wrapper context are filled
// in exactly once and identically for all five tools.
func decorateDiagnostic(tool, verb string, warnings []string, err error) error {
	if err == nil {
		return nil
	}
	var decorated *DiagnosticError
	var inner *DiagnosticError
	if errors.As(err, &inner) {
		// Copy-on-write: producers hand back long-lived *DiagnosticError values
		// (package-level sentinels, retried calls), so filling At in place would
		// let one call's attribution leak into the next.
		copied := *inner
		decorated = &copied
		// errors.As unwraps to the inner diagnostic and discards whatever
		// fmt.Errorf("...: %w") context wrapped it. Recover that prefix, but
		// trim the rendered inner envelope off first so detail never nests one
		// envelope inside another.
		outer := err.Error()
		if rendered := inner.Error(); outer != rendered && strings.HasSuffix(outer, rendered) {
			prefix := strings.TrimSuffix(outer, rendered)
			prefix = strings.TrimSuffix(strings.TrimRight(prefix, " \n"), ":")
			if prefix != "" {
				decorated.Message = prefix + ": " + decorated.Message
			}
		}
	} else {
		decorated = &DiagnosticError{Code: "E_TOOL", Message: err.Error()}
	}
	decorated.At = resolveAt(tool, verb, decorated.At)
	// A repaired-then-failed call must still report the repair: warnings ride
	// the success path only, so without this the caller never learns its key was
	// renamed before the handler rejected the call.
	if len(warnings) > 0 {
		decorated.Message = decorated.Message + " (repairs applied: " + strings.Join(warnings, "; ") + ")"
	}
	return decorated
}

// Invoke validates the portable seam and calls the handler directly.
func Invoke(ctx context.Context, tool Tool, input json.RawMessage) (any, error) {
	if tool.Name == "" {
		return nil, &DiagnosticError{Code: "E_SCHEMA", Message: "tool name is required"}
	}
	if tool.Handler == nil {
		return nil, &DiagnosticError{Code: "E_INTERNAL", At: tool.Name, Message: "tool handler is not registered"}
	}
	if len(input) == 0 {
		input = json.RawMessage("{}")
	}
	if bytes.Equal(bytes.TrimSpace(input), []byte("null")) {
		input = json.RawMessage("{}")
	}
	if !json.Valid(input) {
		return nil, &DiagnosticError{Code: "E_SCHEMA", At: tool.Name, Message: "arguments must be valid JSON"}
	}
	verb := inputVerb(input)
	repaired, warnings, err := Repair(tool.Name, tool.InputSchema, input)
	if err != nil {
		return nil, decorateDiagnostic(tool.Name, verb, warnings, err)
	}
	if repairedVerb := inputVerb(repaired); repairedVerb != "" {
		verb = repairedVerb
	}
	normalized, err := Normalize(tool.InputSchema, repaired)
	if err != nil {
		return nil, decorateDiagnostic(tool.Name, verb, warnings, err)
	}
	result, err := tool.Handler(ctx, normalized)
	if err == nil {
		return attachWarnings(result, warnings), nil
	}
	return nil, decorateDiagnostic(tool.Name, verb, warnings, err)
}
