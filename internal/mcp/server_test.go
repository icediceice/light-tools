package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestProtocolHandshakeAndDeterministicToolList(t *testing.T) {
	server := New("test-server", "1.2.3")
	handler := func(context.Context, json.RawMessage) (any, error) {
		return map[string]any{"ok": true}, nil
	}
	if err := server.Register(Tool{Name: "zeta", Description: "z", InputSchema: map[string]any{"type": "object"}, Handler: handler}); err != nil {
		t.Fatal(err)
	}
	if err := server.Register(Tool{Name: "alpha", Description: "a", InputSchema: map[string]any{"type": "object"}, Handler: handler}); err != nil {
		t.Fatal(err)
	}
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"alpha","arguments":{}}}`,
	}, "\n") + "\n"
	var output strings.Builder
	if err := server.Serve(context.Background(), strings.NewReader(input), &output); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("notification produced a response or response missing: %s", output.String())
	}
	var initialized struct {
		Result struct {
			ProtocolVersion string `json:"protocolVersion"`
			ServerInfo      struct {
				Name    string `json:"name"`
				Version string `json:"version"`
			} `json:"serverInfo"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &initialized); err != nil {
		t.Fatal(err)
	}
	if initialized.Result.ProtocolVersion != ProtocolVersion || initialized.Result.ServerInfo.Name != "test-server" || initialized.Result.ServerInfo.Version != "1.2.3" {
		t.Fatalf("bad initialize response: %s", lines[0])
	}
	var listed struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(lines[1]), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Result.Tools) != 2 || listed.Result.Tools[0].Name != "alpha" || listed.Result.Tools[1].Name != "zeta" {
		t.Fatalf("tools are not deterministic: %s", lines[1])
	}
	if !strings.Contains(lines[2], `"ok":true`) {
		t.Fatalf("tool result was not shaped: %s", lines[2])
	}
}

func TestDuplicateRegistrationRejected(t *testing.T) {
	server := New("test", "1")
	handler := func(context.Context, json.RawMessage) (any, error) { return nil, nil }
	tool := Tool{Name: "same", Handler: handler}
	if err := server.Register(tool); err != nil {
		t.Fatal(err)
	}
	if err := server.Register(tool); err == nil {
		t.Fatal("duplicate tool registration succeeded")
	}
}
