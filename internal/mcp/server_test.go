package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/icediceice/light-tools/internal/terse"
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
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal([]byte(lines[2]), &envelope); err != nil {
		t.Fatal(err)
	}
	var called Result
	if err := json.Unmarshal(envelope["result"], &called); err != nil {
		t.Fatal(err)
	}
	if len(called.Content) != 1 || called.Content[0].Text != "{\"ok\":true}" {
		t.Fatalf("tool result was not shaped: %s", lines[2])
	}
}

func TestToolCallCoercesAndReportsSchemaErrorsAsToolResults(t *testing.T) {
	server := New("test", "1")
	var received string
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"limit": map[string]any{"type": "integer"},
		},
		"additionalProperties": false,
	}
	handler := func(_ context.Context, raw json.RawMessage) (any, error) {
		received = string(raw)
		return map[string]any{"ok": true}, nil
	}
	if err := server.Register(Tool{Name: "sample", InputSchema: schema, Handler: handler}); err != nil {
		t.Fatal(err)
	}

	call := request{
		JSONRPC: "2.0",
		ID:      json.RawMessage("1"),
		Method:  "tools/call",
		Params:  json.RawMessage("{\"name\":\"sample\",\"arguments\":{\"limit\":\"12\"}}"),
	}
	response := server.dispatch(context.Background(), call)
	if response.Error != nil {
		t.Fatalf("coercion became a protocol error: %#v", response.Error)
	}
	if received != "{\"limit\":12}" {
		t.Fatalf("handler received %s", received)
	}

	call.Params = json.RawMessage("{\"name\":\"sample\",\"arguments\":{\"unknown\":true}}")
	response = server.dispatch(context.Background(), call)
	if response.Error != nil {
		t.Fatalf("schema failure became a protocol error: %#v", response.Error)
	}
	result, ok := response.Result.(Result)
	if !ok || !result.IsError || len(result.Content) != 1 || !strings.Contains(result.Content[0].Text, "error[E_SCHEMA]") {
		t.Fatalf("schema failure lost tool-result envelope: %#v", response.Result)
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

func TestTerseFormattingAtDispatchBoundary(t *testing.T) {
	raw := `{"content":"` + strings.Repeat("word ", 120) + `","status":"ok","port":"8080"}`
	original := &Result{Content: []Content{Text(raw), Text("second-content")}}
	server := New("test", "1", true)
	handlers := map[string]func(context.Context, json.RawMessage) (any, error){
		"value":   func(context.Context, json.RawMessage) (any, error) { return *original, nil },
		"pointer": func(context.Context, json.RawMessage) (any, error) { return original, nil },
		"default": func(context.Context, json.RawMessage) (any, error) {
			return map[string]any{"content": strings.Repeat("word ", 120), "status": "ok", "port": "8080"}, nil
		},
	}
	for name, handler := range handlers {
		if err := server.Register(Tool{Name: name, Handler: handler}); err != nil {
			t.Fatal(err)
		}
	}

	var first string
	for _, name := range []string{"value", "pointer", "default"} {
		response := server.dispatch(context.Background(), request{
			JSONRPC: "2.0", ID: json.RawMessage("1"), Method: "tools/call",
			Params: json.RawMessage(`{"name":"` + name + `","arguments":{}}`),
		})
		result, ok := response.Result.(Result)
		if !ok {
			t.Fatalf("%s returned %#v", name, response.Result)
		}
		if len(result.Content) == 0 || result.Content[0].Text == raw {
			t.Fatalf("%s did not format content[0]: %#v", name, result)
		}
		decoded, err := terse.Decode([]byte(result.Content[0].Text))
		if err != nil {
			t.Fatalf("%s emitted invalid terse: %v", name, err)
		}
		var want any
		decoder := json.NewDecoder(strings.NewReader(raw))
		decoder.UseNumber()
		if err := decoder.Decode(&want); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(decoded, want) {
			t.Fatalf("%s changed semantics\nwant %#v\n got %#v", name, want, decoded)
		}
		if name != "default" {
			if len(result.Content) != 2 || result.Content[1].Text != "second-content" {
				t.Fatalf("%s changed content[1+]: %#v", name, result.Content)
			}
		}
		if first == "" {
			first = result.Content[0].Text
		} else if result.Content[0].Text != first {
			t.Fatalf("%s emitted nondeterministic terse\nfirst %q\n  got %q", name, first, result.Content[0].Text)
		}
	}
	if original.Content[0].Text != raw || original.Content[1].Text != "second-content" {
		t.Fatalf("dispatch mutated handler-owned result: %#v", original)
	}
}

func TestTerseFormattingPassthroughCases(t *testing.T) {
	raw := `{"content":"` + strings.Repeat("word ", 120) + `"}`
	cases := []struct {
		name    string
		enabled bool
		handler func(context.Context, json.RawMessage) (any, error)
		want    Result
	}{
		{name: "disabled", handler: func(context.Context, json.RawMessage) (any, error) {
			return Result{Content: []Content{Text(raw)}}, nil
		}, want: Result{Content: []Content{Text(raw)}}},
		{name: "tool-error", enabled: true, handler: func(context.Context, json.RawMessage) (any, error) {
			return nil, errors.New(raw)
		}, want: Result{Content: []Content{Text(raw)}, IsError: true}},
		{name: "error-result", enabled: true, handler: func(context.Context, json.RawMessage) (any, error) {
			return Result{Content: []Content{Text(raw)}, IsError: true}, nil
		}, want: Result{Content: []Content{Text(raw)}, IsError: true}},
		{name: "image-first", enabled: true, handler: func(context.Context, json.RawMessage) (any, error) {
			return Result{Content: []Content{Image([]byte("image"), "image/png"), Text(raw)}}, nil
		}, want: Result{Content: []Content{Image([]byte("image"), "image/png"), Text(raw)}}},
		{name: "plain-text", enabled: true, handler: func(context.Context, json.RawMessage) (any, error) {
			return Result{Content: []Content{Text(strings.Repeat("plain text ", 120))}}, nil
		}, want: Result{Content: []Content{Text(strings.Repeat("plain text ", 120))}}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			server := New("test", "1", test.enabled)
			if err := server.Register(Tool{Name: "sample", Handler: test.handler}); err != nil {
				t.Fatal(err)
			}
			response := server.dispatch(context.Background(), request{
				JSONRPC: "2.0", ID: json.RawMessage("1"), Method: "tools/call",
				Params: json.RawMessage(`{"name":"sample","arguments":{}}`),
			})
			result, ok := response.Result.(Result)
			if !ok {
				t.Fatalf("got %#v, want a Result", response.Result)
			}
			if test.name == "tool-error" {
				if !result.IsError || len(result.Content) != 1 ||
					!strings.Contains(result.Content[0].Text, raw) || strings.HasPrefix(result.Content[0].Text, "~") {
					t.Fatalf("wrapped tool error was reformatted or lost detail: %#v", result)
				}
				return
			}
			if !reflect.DeepEqual(result, test.want) {
				t.Fatalf("got %#v, want %#v", response.Result, test.want)
			}
		})
	}
}

// captureRecorder records what the server observed, for assertions.
type captureRecorder struct {
	calls       []string
	terseTokens []int
}

func (c *captureRecorder) RecordCall(tool string)          { c.calls = append(c.calls, tool) }
func (c *captureRecorder) RecordTerseTokens(saved int)     { c.terseTokens = append(c.terseTokens, saved) }
func (c *captureRecorder) RecordDedupBytes(int)            {}
func (c *captureRecorder) RecordWriteBytes(int)            {}

// panicRecorder fails like a broken sink: every method panics.
type panicRecorder struct{}

func (panicRecorder) RecordCall(string)        { panic("recorder exploded") }
func (panicRecorder) RecordTerseTokens(int)    { panic("recorder exploded") }
func (panicRecorder) RecordDedupBytes(int)     { panic("recorder exploded") }
func (panicRecorder) RecordWriteBytes(int)     { panic("recorder exploded") }

// A recorder panic and a failing (blocked) writer must both leave the RPC
// result byte-identical to a server with no recorder at all.
func TestRecorderFailuresLeaveResultsByteIdentical(t *testing.T) {
	blockedDir := filepath.Join(t.TempDir(), "blocked")
	store := telemetry.New(blockedDir)
	if store == nil {
		t.Skip("telemetry disabled by environment")
	}
	defer store.Close()
	// Break the store's directory: every flush now fails, while Record paths
	// stay in-memory and non-blocking.
	if err := os.RemoveAll(blockedDir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(blockedDir, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}

	build := func(recorder telemetry.Recorder) string {
		server := New("test", "1", true)
		server.SetRecorder(recorder)
		handler := func(context.Context, json.RawMessage) (any, error) {
			return Result{Content: []Content{Text(`{"content":"` + strings.Repeat("word ", 120) + `","status":"ok"}`)}}, nil
		}
		if err := server.Register(Tool{Name: "sample", Handler: handler}); err != nil {
			t.Fatal(err)
		}
		var output strings.Builder
		input := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"sample","arguments":{}}}` + "\n")
		if err := server.Serve(context.Background(), input, &output); err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(output.String())
	}

	want := build(nil)
	for name, recorder := range map[string]telemetry.Recorder{
		"panic":  panicRecorder{},
		"blocked": store,
	} {
		if got := build(recorder); got != want {
			t.Fatalf("%s recorder changed the RPC result\nwant %s\n got %s", name, want, got)
		}
	}
}

func TestServerRecordsCallCountsAndTerseDelta(t *testing.T) {
	recorder := &captureRecorder{}
	server := New("test", "1", true)
	server.SetRecorder(recorder)
	handler := func(context.Context, json.RawMessage) (any, error) {
		return Result{Content: []Content{Text(`{"content":"` + strings.Repeat("word ", 120) + `","status":"ok"}`)}}, nil
	}
	if err := server.Register(Tool{Name: "sample", Handler: handler}); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		response := server.dispatch(context.Background(), request{
			JSONRPC: "2.0", ID: json.RawMessage("1"), Method: "tools/call",
			Params: json.RawMessage(`{"name":"sample","arguments":{}}`),
		})
		if response.Error != nil {
			t.Fatalf("call failed: %#v", response.Error)
		}
	}
	if len(recorder.calls) != 2 || recorder.calls[0] != "sample" || recorder.calls[1] != "sample" {
		t.Fatalf("call counts = %v", recorder.calls)
	}
	if len(recorder.terseTokens) != 2 || recorder.terseTokens[0] <= 0 || recorder.terseTokens[0] != recorder.terseTokens[1] {
		t.Fatalf("terse deltas = %v", recorder.terseTokens)
	}
	// An unformatted result records no terse delta.
	handler2 := func(context.Context, json.RawMessage) (any, error) {
		return Result{Content: []Content{Text("small")}}, nil
	}
	if err := server.Register(Tool{Name: "tiny", Handler: handler2}); err != nil {
		t.Fatal(err)
	}
	before := len(recorder.terseTokens)
	server.dispatch(context.Background(), request{
		JSONRPC: "2.0", ID: json.RawMessage("1"), Method: "tools/call",
		Params: json.RawMessage(`{"name":"tiny","arguments":{}}`),
	})
	if len(recorder.terseTokens) != before {
		t.Fatalf("unchanged result recorded a delta: %v", recorder.terseTokens)
	}
}

func TestTerseFormattingHandlesNilResultPointer(t *testing.T) {
	server := New("test", "1", true)
	if err := server.Register(Tool{Name: "nil", Handler: func(context.Context, json.RawMessage) (any, error) {
		var result *Result
		return result, nil
	}}); err != nil {
		t.Fatal(err)
	}
	response := server.dispatch(context.Background(), request{
		JSONRPC: "2.0", ID: json.RawMessage("1"), Method: "tools/call",
		Params: json.RawMessage(`{"name":"nil","arguments":{}}`),
	})
	result, ok := response.Result.(*Result)
	if !ok || result != nil {
		t.Fatalf("typed nil result changed: %#v", response.Result)
	}
}
