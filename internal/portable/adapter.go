// Package portable contains the dependency-free invocation seam shared by the
// standalone MCP transport and tool handlers.
package portable

import (
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
	Name    string
	Handler Handler
}

// DiagnosticError is a stable, model-readable error envelope.
type DiagnosticError struct {
	Code       string
	Message    string
	Caret      int
	Line       int
	Column     int
	SourceLine string
}

func (e *DiagnosticError) Error() string {
	code := e.Code
	if code == "" {
		code = "E_TOOL"
	}
	if e.Line <= 0 || e.SourceLine == "" {
		return fmt.Sprintf("error[%s]\n  detail: %s", code, e.Message)
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
	return fmt.Sprintf(
		"error[%s]\n  at: line %d, column %d (byte %d)\n  detail: %s\n  %*d | %s\n  %*s | %s",
		code, e.Line, e.Column, e.Caret, e.Message, width, e.Line, e.SourceLine, width, "", marker.String(),
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

// Invoke validates the portable seam and calls the handler directly.
func Invoke(ctx context.Context, tool Tool, input json.RawMessage) (any, error) {
	if tool.Name == "" {
		return nil, &DiagnosticError{Code: "E_SCHEMA", Message: "tool name is required"}
	}
	if tool.Handler == nil {
		return nil, &DiagnosticError{Code: "E_INTERNAL", Message: "tool handler is not registered"}
	}
	if len(input) == 0 {
		input = json.RawMessage("{}")
	}
	if !json.Valid(input) {
		return nil, &DiagnosticError{Code: "E_SCHEMA", Message: "arguments must be valid JSON"}
	}
	result, err := tool.Handler(ctx, input)
	if err == nil {
		return result, nil
	}
	var diagnostic *DiagnosticError
	if errors.As(err, &diagnostic) {
		return nil, diagnostic
	}
	return nil, &DiagnosticError{Code: "E_TOOL", Message: err.Error()}
}
