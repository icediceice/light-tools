// Package portable contains the dependency-free invocation seam shared by the
// standalone MCP transport and tool handlers.
package portable

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	Code    string
	Message string
	Caret   int
}

func (e *DiagnosticError) Error() string {
	if e.Caret <= 0 {
		return fmt.Sprintf("%s: %s", e.Code, e.Message)
	}
	return fmt.Sprintf("%s at byte %d: %s", e.Code, e.Caret, e.Message)
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
