// Package mcp implements the small JSON-RPC surface required by MCP stdio.
package mcp

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"runtime/debug"
	"sort"
	"sync"

	"github.com/icediceice/light-tools/internal/portable"
	"github.com/icediceice/light-tools/internal/telemetry"
	"github.com/icediceice/light-tools/internal/terse"
)

const ProtocolVersion = "2025-06-18"

type Content struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	Data     string `json:"data,omitempty"`
	MIMEType string `json:"mimeType,omitempty"`
}

func Text(value string) Content { return Content{Type: "text", Text: value} }

func Image(data []byte, mimeType string) Content {
	return Content{Type: "image", Data: base64.StdEncoding.EncodeToString(data), MIMEType: mimeType}
}

type Result struct {
	Content []Content `json:"content"`
	IsError bool      `json:"isError,omitempty"`
}

type Tool struct {
	Name        string
	Description string
	InputSchema map[string]any
	Handler     portable.Handler
}

type Server struct {
	name    string
	version string
	terse   bool
	mu      sync.RWMutex
	tools   map[string]Tool
	// recorder is consulted only through observe, never directly, so a broken
	// or missing recorder cannot alter a tool result. It is set before Serve.
	recorder telemetry.Recorder
}

// SetRecorder attaches the local telemetry sink. It must be called before Serve
// starts; a nil recorder (or none) records nothing.
func (s *Server) SetRecorder(recorder telemetry.Recorder) { s.recorder = recorder }

// observe runs one recorder callback behind a panic boundary. Telemetry is an
// observation of the result, never a participant in producing it: a recorder
// panic must not discard a result the handler already computed.
func (s *Server) observe(record func(telemetry.Recorder)) {
	defer func() { _ = recover() }()
	if s.recorder == nil {
		return
	}
	record(s.recorder)
}

func New(name, version string, terseOutput ...bool) *Server {
	enabled := len(terseOutput) > 0 && terseOutput[0]
	return &Server{name: name, version: version, terse: enabled, tools: make(map[string]Tool)}
}

func (s *Server) Register(tool Tool) error {
	if tool.Name == "" || tool.Handler == nil {
		return fmt.Errorf("invalid tool registration")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.tools[tool.Name]; exists {
		return fmt.Errorf("tool %q already registered", tool.Name)
	}
	s.tools[tool.Name] = tool
	return nil
}

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

func (s *Server) Serve(ctx context.Context, in io.Reader, out io.Writer) error {
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	encoder := json.NewEncoder(out)
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		var req request
		if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
			if err := encoder.Encode(response{JSONRPC: "2.0", Error: &rpcError{Code: -32700, Message: "parse error", Data: err.Error()}}); err != nil {
				return err
			}
			continue
		}
		if len(req.ID) == 0 {
			continue
		}
		resp := s.dispatch(ctx, req)
		if err := encoder.Encode(resp); err != nil {
			return err
		}
	}
	return scanner.Err()
}

func (s *Server) dispatch(ctx context.Context, req request) (resp response) {
	resp = response{JSONRPC: "2.0", ID: req.ID}
	defer func() {
		if recovered := recover(); recovered != nil {
			resp.Result = nil
			resp.Error = &rpcError{Code: -32603, Message: "internal error", Data: fmt.Sprintf("%v\n%s", recovered, debug.Stack())}
		}
	}()

	switch req.Method {
	case "initialize":
		resp.Result = map[string]any{
			"protocolVersion": ProtocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo":      map[string]any{"name": s.name, "version": s.version},
		}
	case "ping":
		resp.Result = map[string]any{}
	case "tools/list":
		s.mu.RLock()
		tools := make([]map[string]any, 0, len(s.tools))
		for _, tool := range s.tools {
			tools = append(tools, map[string]any{
				"name": tool.Name, "description": tool.Description, "inputSchema": tool.InputSchema,
			})
		}
		s.mu.RUnlock()
		sort.Slice(tools, func(i, j int) bool { return tools[i]["name"].(string) < tools[j]["name"].(string) })
		resp.Result = map[string]any{"tools": tools}
	case "tools/call":
		var params struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &params); err != nil {
			resp.Error = &rpcError{Code: -32602, Message: "invalid tools/call params", Data: err.Error()}
			return resp
		}
		s.mu.RLock()
		tool, ok := s.tools[params.Name]
		s.mu.RUnlock()
		if !ok {
			resp.Error = &rpcError{Code: -32602, Message: "unknown tool: " + params.Name}
			return resp
		}
		s.observe(func(recorder telemetry.Recorder) { recorder.RecordCall(tool.Name) })
		value, err := portable.Invoke(ctx, portable.Tool{Name: tool.Name, InputSchema: tool.InputSchema, Handler: tool.Handler}, params.Arguments)
		if err != nil {
			resp.Result = Result{Content: []Content{Text(err.Error())}, IsError: true}
			return resp
		}
		switch typed := value.(type) {
		case Result:
			resp.Result = s.formatResult(typed)
		case *Result:
			if typed == nil {
				resp.Result = typed
			} else {
				resp.Result = s.formatResult(*typed)
			}
		default:
			encoded, err := json.Marshal(value)
			if err != nil {
				resp.Result = Result{Content: []Content{Text(err.Error())}, IsError: true}
			} else {
				resp.Result = s.formatResult(Result{Content: []Content{Text(string(encoded))}})
			}
		}
	default:
		resp.Error = &rpcError{Code: -32601, Message: "method not found: " + req.Method}
	}
	return resp
}

func (s *Server) formatResult(result Result) Result {
	if !s.terse || result.IsError || len(result.Content) == 0 || result.Content[0].Type != "text" {
		return result
	}
	formatted, changed := terse.Format([]byte(result.Content[0].Text))
	if !changed {
		return result
	}
	cloned := result
	cloned.Content = append([]Content(nil), result.Content...)
	cloned.Content[0].Text = string(formatted)
	return cloned
}
