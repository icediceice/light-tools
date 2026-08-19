package mcp

import (
	"context"
	"encoding/json"
	"testing"
)

// A tools/call whose arguments member is an explicit JSON null must reach the
// handler as an empty object, exactly as an omitted arguments member does.
// Clients that serialise "no arguments" as null are otherwise rejected with
// E_SCHEMA by the new schema pass.
func TestVerifyToolCallAcceptsNullArguments(t *testing.T) {
	schema := map[string]any{
		"type":                 "object",
		"properties":           map[string]any{"verb": map[string]any{"type": "string"}},
		"additionalProperties": false,
	}
	cases := map[string]string{
		"omitted": `{"name":"sample"}`,
		"null":    `{"name":"sample","arguments":null}`,
		"empty":   `{"name":"sample","arguments":{}}`,
	}
	for name, params := range cases {
		t.Run(name, func(t *testing.T) {
			server := New("test", "1")
			var received string
			handler := func(_ context.Context, raw json.RawMessage) (any, error) {
				received = string(raw)
				return map[string]any{"ok": true}, nil
			}
			if err := server.Register(Tool{Name: "sample", InputSchema: schema, Handler: handler}); err != nil {
				t.Fatal(err)
			}
			response := server.dispatch(context.Background(), request{
				JSONRPC: "2.0", ID: json.RawMessage("1"), Method: "tools/call",
				Params: json.RawMessage(params),
			})
			if response.Error != nil {
				t.Fatalf("protocol error: %#v", response.Error)
			}
			result, ok := response.Result.(Result)
			if !ok {
				t.Fatalf("unexpected result type %#v", response.Result)
			}
			if result.IsError {
				t.Fatalf("arguments %s rejected: %s", params, result.Content[0].Text)
			}
			if received != "{}" {
				t.Fatalf("handler received %q, want {}", received)
			}
		})
	}
}

// An optional member serialised as an explicit JSON null (the default output of
// most client SDKs for an unset optional field) was previously ignored by
// encoding/json and must stay accepted.
func TestVerifyToolCallAcceptsNullOptionalMembers(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"verb":   map[string]any{"type": "string"},
			"path":   map[string]any{"type": "string"},
			"offset": map[string]any{"type": "integer"},
		},
		"additionalProperties": false,
	}
	server := New("test", "1")
	var received string
	handler := func(_ context.Context, raw json.RawMessage) (any, error) {
		received = string(raw)
		return map[string]any{"ok": true}, nil
	}
	if err := server.Register(Tool{Name: "sample", InputSchema: schema, Handler: handler}); err != nil {
		t.Fatal(err)
	}
	response := server.dispatch(context.Background(), request{
		JSONRPC: "2.0", ID: json.RawMessage("1"), Method: "tools/call",
		Params: json.RawMessage(`{"name":"sample","arguments":{"verb":"read","path":"/x","offset":null}}`),
	})
	result, ok := response.Result.(Result)
	if !ok {
		t.Fatalf("unexpected result type %#v (error %#v)", response.Result, response.Error)
	}
	if result.IsError {
		t.Fatalf("null optional member rejected: %s", result.Content[0].Text)
	}
	var decoded struct {
		Verb   string `json:"verb"`
		Path   string `json:"path"`
		Offset int    `json:"offset"`
	}
	if err := json.Unmarshal([]byte(received), &decoded); err != nil {
		t.Fatalf("handler payload not decodable: %v (%s)", err, received)
	}
	if decoded.Verb != "read" || decoded.Path != "/x" || decoded.Offset != 0 {
		t.Fatalf("handler received %s", received)
	}
}
