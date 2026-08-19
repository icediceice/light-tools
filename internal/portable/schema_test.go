package portable

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func testSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"offset":  map[string]any{"type": "integer", "minimum": 0},
			"enabled": map[string]any{"type": "boolean"},
			"label":   map[string]any{"type": "string"},
			"mode":    map[string]any{"type": "string", "enum": []any{"read", "write"}},
			"items": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path":  map[string]any{"type": "string"},
						"limit": map[string]any{"type": "integer"},
					},
					"required":             []string{"path"},
					"additionalProperties": false,
				},
			},
		},
		"additionalProperties": false,
	}
}

func TestNormalizePreservesValidBytesAndLargeIntegers(t *testing.T) {
	input := json.RawMessage("{ \"offset\": 9007199254740993, \"enabled\": true }")
	got, err := Normalize(testSchema(), input)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(input) {
		t.Fatalf("valid input was rewritten: got %q want %q", got, input)
	}
}

func TestNormalizeCoercesDeclaredScalarTypesRecursively(t *testing.T) {
	input := json.RawMessage("{\"offset\":\"42\",\"enabled\":\"true\",\"label\":7,\"items\":[{\"path\":9,\"limit\":\"3\"}]}")
	got, err := Normalize(testSchema(), input)
	if err != nil {
		t.Fatal(err)
	}
	const want = "{\"enabled\":true,\"items\":[{\"limit\":3,\"path\":\"9\"}],\"label\":\"7\",\"offset\":42}"
	if string(got) != want {
		t.Fatalf("got %s, want %s", got, want)
	}
}

func TestNormalizeRejectsSchemaViolations(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{name: "top-level unknown", input: "{\"bogus\":1}", want: "$.bogus is not allowed"},
		{name: "nested unknown", input: "{\"items\":[{\"path\":\"a\",\"bogus\":1}]}", want: "$.items[0].bogus is not allowed"},
		{name: "nested required", input: "{\"items\":[{\"limit\":1}]}", want: "$.items[0].path is required"},
		{name: "required null", input: "{\"items\":[{\"path\":null}]}", want: "$.items[0].path must not be null"},
		{name: "null array item", input: "{\"items\":[null]}", want: "$.items[0] must not be null"},
		{name: "wrong shape", input: "{\"items\":\"a\"}", want: "$.items must be an array"},
		{name: "fractional integer", input: "{\"offset\":1.5}", want: "$.offset must be an integer"},
		{name: "exponent integer", input: "{\"offset\":1e3}", want: "$.offset must be an integer"},
		{name: "minimum", input: "{\"offset\":-1}", want: "$.offset must satisfy minimum"},
		{name: "enum", input: "{\"mode\":\"delete\"}", want: "$.mode is not one of the allowed values"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			_, err := Normalize(testSchema(), json.RawMessage(test.input))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("got %v, want error containing %q", err, test.want)
			}
			var diagnostic *DiagnosticError
			if !errors.As(err, &diagnostic) || diagnostic.Code != "E_SCHEMA" {
				t.Fatalf("got %#v, want E_SCHEMA DiagnosticError", err)
			}
		})
	}
}

func TestInvokeNormalizesBeforeHandlerWithoutChangingErrorChannel(t *testing.T) {
	var received json.RawMessage
	tool := Tool{
		Name:        "test",
		InputSchema: testSchema(),
		Handler: func(_ context.Context, raw json.RawMessage) (any, error) {
			received = append(received[:0], raw...)
			return map[string]any{"ok": true}, nil
		},
	}
	if _, err := Invoke(context.Background(), tool, json.RawMessage("{\"offset\":\"9\"}")); err != nil {
		t.Fatal(err)
	}
	if string(received) != "{\"offset\":9}" {
		t.Fatalf("handler received %s", received)
	}
	_, err := Invoke(context.Background(), tool, json.RawMessage("{\"unknown\":true}"))
	var diagnostic *DiagnosticError
	if !errors.As(err, &diagnostic) || diagnostic.Code != "E_SCHEMA" {
		t.Fatalf("got %#v, want E_SCHEMA DiagnosticError", err)
	}
}
