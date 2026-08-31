//go:build light_acceptance_pending_t1543971702625669169

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/icediceice/light-tools/internal/config"
	"github.com/icediceice/light-tools/internal/mcp"
	"github.com/icediceice/light-tools/internal/state"
)

func TestVerifySinglePathFirstByteContractSurvivesTersePostprocessing(t *testing.T) {
	base := t.TempDir()
	deep := base
	segment := strings.Repeat("segment", 16)
	for index := 0; index < 8; index++ {
		deep = filepath.Join(deep, fmt.Sprintf("%d-%s", index, segment))
		if err := os.Mkdir(deep, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(deep, "subject.go")
	if err := os.WriteFile(path, []byte("package subject\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	layout := state.Layout{
		Config:    filepath.Join(base, "config"),
		Secrets:   filepath.Join(base, "secrets"),
		Snapshots: filepath.Join(base, "snapshots"),
		Spills:    filepath.Join(base, "spills"),
	}
	for _, directory := range []string{layout.Config, layout.Secrets, layout.Snapshots, layout.Spills} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	configuration := config.Config{
		AllowedRoots: []string{base},
		LogRoots:     []string{base},
		Remote:       map[string]config.RemoteProfile{},
	}
	server := mcp.New("test", "1", true)
	if err := registerTools(server, options{}, layout, configuration, nil); err != nil {
		t.Fatal(err)
	}

	request, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/call",
		"params": map[string]any{
			"name": "light_file",
			"arguments": map[string]any{
				"verb": "read",
				"path": path,
				"name": "DoesNotExist",
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var output strings.Builder
	if err := server.Serve(context.Background(), strings.NewReader(string(request)+"\n"), &output); err != nil {
		t.Fatal(err)
	}
	var response struct {
		Result mcp.Result `json:"result"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(output.String())), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Result.Content) != 1 || response.Result.Content[0].Text == "" {
		t.Fatalf("unexpected response: %#v", response.Result)
	}
	delivered := response.Result.Content[0].Text
	if delivered[0] != '{' && delivered[0] != '=' {
		t.Fatalf("single-path read escaped the documented first-byte grammar: got %q in %q", delivered[0], delivered[:min(len(delivered), 80)])
	}
}
