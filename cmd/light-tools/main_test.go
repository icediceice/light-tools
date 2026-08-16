package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/icediceice/light-tools/internal/config"
	"github.com/icediceice/light-tools/internal/mcp"
	"github.com/icediceice/light-tools/internal/state"
)

func TestToolSchemasContainRetainedSingleOperatorFields(t *testing.T) {
	expected := map[string][]string{
		"light_file": {"verb", "path", "target", "from", "to", "payload", "patch", "patch_path", "fuzz", "spans", "items", "reads", "context_epoch"},
		"light_bash": {"verb", "task_id", "command", "cwd", "async", "output_mode", "spill_id", "env_refs", "file_refs"},
		"light_ssh":  {"profile", "remote", "command", "key", "key_ref", "cert_ref", "port", "proxy_jump", "timeout_ms"},
		"light_scp":  {"profile", "src", "dst", "key", "key_ref", "cert_ref", "port", "proxy_jump", "timeout_ms"},
		"light_ops":  {"verb", "task_id", "async", "service", "services", "path", "pattern", "context", "since", "since_ts", "include", "exclude", "drill", "refresh"},
	}
	for tool, fields := range expected {
		schema := toolSchema(tool)
		properties := schema["properties"].(map[string]any)
		for _, field := range fields {
			if _, ok := properties[field]; !ok {
				t.Errorf("%s schema missing %s", tool, field)
			}
		}
		if schema["additionalProperties"] != false {
			t.Errorf("%s schema permits unknown fields", tool)
		}
	}
}

func TestCapabilityProfilesRegisterExpectedTools(t *testing.T) {
	root := t.TempDir()
	layout := state.Layout{
		Config: filepath.Join(root, "config"), Secrets: filepath.Join(root, "secrets"),
		Snapshots: filepath.Join(root, "snapshots"), Spills: filepath.Join(root, "spills"),
	}
	configuration := config.Config{AllowedRoots: []string{root}, Remote: map[string]config.RemoteProfile{}}
	for _, path := range []string{layout.Config, layout.Secrets, layout.Snapshots, layout.Spills} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	cases := []struct {
		name string
		opts options
		want []string
	}{
		{name: "default", want: []string{"light_file"}},
		{name: "all", opts: options{enableShell: true, enableRemote: true, enableOps: true}, want: []string{"light_bash", "light_file", "light_ops", "light_scp", "light_ssh"}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			server := mcp.New("test", "1")
			if err := registerTools(server, test.opts, layout, configuration); err != nil {
				t.Fatal(err)
			}
			var output strings.Builder
			input := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}` + "\n")
			if err := server.Serve(context.Background(), input, &output); err != nil {
				t.Fatal(err)
			}
			var response struct {
				Result struct {
					Tools []struct {
						Name string `json:"name"`
					} `json:"tools"`
				} `json:"result"`
			}
			if err := json.Unmarshal([]byte(output.String()), &response); err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, tool := range response.Result.Tools {
				got = append(got, tool.Name)
			}
			if strings.Join(got, ",") != strings.Join(test.want, ",") {
				t.Fatalf("got %v, want %v", got, test.want)
			}
		})
	}
}

func TestInitNeedsNoConfigAndPrintsMCPCommand(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(root, "runtime"))
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldStdout := os.Stdout
	os.Stdout = writer
	defer func() { os.Stdout = oldStdout }()
	if err := runInit(); err != nil {
		t.Fatal(err)
	}
	writer.Close()
	os.Stdout = oldStdout
	output, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(output), "claude mcp add light-tools -- ") {
		t.Fatalf("init output missing MCP command: %s", output)
	}
	for _, path := range []string{
		filepath.Join(root, "config", "light-tools"),
		filepath.Join(root, "data", "light-tools-secrets"),
		filepath.Join(root, "data", "light-tools-snapshots"),
		filepath.Join(root, "runtime", "light-tools-spills"),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("init did not create %s: %v", path, err)
		}
	}
}
