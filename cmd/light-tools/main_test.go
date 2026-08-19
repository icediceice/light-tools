package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/icediceice/light-tools/internal/bash"
	"github.com/icediceice/light-tools/internal/config"
	"github.com/icediceice/light-tools/internal/filetool"
	"github.com/icediceice/light-tools/internal/mcp"
	"github.com/icediceice/light-tools/internal/ops"
	"github.com/icediceice/light-tools/internal/remote"
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

func captureStdout(t *testing.T, run func() error) string {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldStdout := os.Stdout
	os.Stdout = writer
	runErr := run()
	os.Stdout = oldStdout
	writer.Close()
	output, readErr := io.ReadAll(reader)
	if runErr != nil {
		t.Fatal(runErr)
	}
	if readErr != nil {
		t.Fatal(readErr)
	}
	return string(output)
}

// isolateHome points both the XDG state roots and $HOME at a scratch tree so
// init writes nothing into the developer's real configuration.
func isolateHome(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(root, "runtime"))
	return root
}

func TestInitNeedsNoConfigAndPrintsMCPCommand(t *testing.T) {
	root := isolateHome(t)
	output := captureStdout(t, func() error { return runInit(nil) })
	if !strings.Contains(output, "claude mcp add light-tools -- ") {
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

func TestInitClaudeCarriesCapabilityFlags(t *testing.T) {
	isolateHome(t)
	output := captureStdout(t, func() error {
		return runInit([]string{"--client", "claude", "--enable-shell", "--enable-ops"})
	})
	if !strings.Contains(output, " -- ") || !strings.Contains(output, "--enable-shell --enable-ops") {
		t.Fatalf("claude command lost capability flags: %s", output)
	}
}

func TestInitAntigravityMergesConfigAndWritesSkill(t *testing.T) {
	root := isolateHome(t)
	configPath := filepath.Join(root, ".gemini", "config", "mcp_config.json")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		t.Fatal(err)
	}
	existing := `{"someOtherKey":{"keep":true},"mcpServers":{"foreign":{"command":"other","args":["--x"]}}}`
	if err := os.WriteFile(configPath, []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}

	captureStdout(t, func() error {
		return runInit([]string{"--client", "antigravity", "--enable-shell", "--disable-tool", "light_scp"})
	})

	first, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		SomeOtherKey map[string]any            `json:"someOtherKey"`
		MCPServers   map[string]map[string]any `json:"mcpServers"`
	}
	if err := json.Unmarshal(first, &document); err != nil {
		t.Fatalf("generated config is not valid JSON: %v\n%s", err, first)
	}
	if document.SomeOtherKey == nil || document.SomeOtherKey["keep"] != true {
		t.Errorf("merge dropped an unrelated top-level key: %s", first)
	}
	foreign, ok := document.MCPServers["foreign"]
	if !ok || foreign["command"] != "other" {
		t.Errorf("merge dropped a foreign server: %s", first)
	}
	entry, ok := document.MCPServers["light-tools"]
	if !ok {
		t.Fatalf("merge did not add light-tools: %s", first)
	}
	if entry["command"] == "" || entry["command"] == nil {
		t.Errorf("light-tools entry has no command: %s", first)
	}
	args, _ := entry["args"].([]any)
	if len(args) != 1 || args[0] != "--enable-shell" {
		t.Errorf("light-tools entry lost its capability args: %v", entry["args"])
	}
	disabled, _ := entry["disabledTools"].([]any)
	if len(disabled) != 1 || disabled[0] != "light_scp" {
		t.Errorf("light-tools entry lost disabledTools: %v", entry["disabledTools"])
	}
	documented := map[string]bool{"command": true, "args": true, "env": true, "cwd": true, "disabled": true, "disabledTools": true}
	for field := range entry {
		if !documented[field] {
			t.Errorf("entry carries undocumented property %q", field)
		}
	}

	skillPath := filepath.Join(root, ".gemini", "config", "skills", "light-tools", "SKILL.md")
	skill, err := os.ReadFile(skillPath)
	if err != nil {
		t.Fatalf("init did not write the skill: %v", err)
	}
	text := string(skill)
	if !strings.HasPrefix(text, "---\nname: light-tools\ndescription: ") {
		t.Errorf("skill frontmatter does not match the documented fields:\n%s", text)
	}
	for _, tool := range []string{"light_file", "light_bash", "light_ssh", "light_scp", "light_ops"} {
		if !strings.Contains(text, tool) {
			t.Errorf("skill never mentions %s", tool)
		}
	}

	captureStdout(t, func() error {
		return runInit([]string{"--client", "antigravity", "--enable-shell", "--disable-tool", "light_scp"})
	})
	second, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Errorf("re-init was not idempotent:\nfirst:\n%s\nsecond:\n%s", first, second)
	}
}

func TestInitAntigravityWorkspaceTargetsAgentsDirectory(t *testing.T) {
	root := isolateHome(t)
	workspace := filepath.Join(root, "project")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	captureStdout(t, func() error {
		return runInit([]string{"--client", "antigravity", "--workspace", workspace})
	})
	for _, path := range []string{
		filepath.Join(workspace, ".agents", "mcp_config.json"),
		filepath.Join(workspace, ".agents", "skills", "light-tools", "SKILL.md"),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("workspace init did not create %s: %v", path, err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, ".gemini")); !os.IsNotExist(err) {
		t.Errorf("workspace init touched the global location: %v", err)
	}
}

func TestInitPrintWritesNothing(t *testing.T) {
	root := isolateHome(t)
	output := captureStdout(t, func() error { return runInit([]string{"--client", "print"}) })
	for _, want := range []string{"mcp_config.json", "\"mcpServers\"", "SKILL.md", "read_file(*)", "write_file(*)", "command(*)", "mcp(light-tools/*)"} {
		if !strings.Contains(output, want) {
			t.Errorf("print output missing %q:\n%s", want, output)
		}
	}
	if strings.Contains(output, "httpUrl") || strings.Contains(output, "\"timeout\"") {
		t.Errorf("print output emits retired Antigravity properties:\n%s", output)
	}
	if _, err := os.Stat(filepath.Join(root, ".gemini")); !os.IsNotExist(err) {
		t.Errorf("print mode wrote to disk: %v", err)
	}
}

func TestInitRejectsUnknownClient(t *testing.T) {
	isolateHome(t)
	if err := runInit([]string{"--client", "cursor"}); err == nil {
		t.Fatal("expected an error for an unknown client")
	}
}

func TestMergeAntigravityConfigRefusesMalformedJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp_config.json")
	if err := os.WriteFile(path, []byte("{ // a comment\n}"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := mergeAntigravityConfig(path, "light-tools", map[string]any{"command": "x"})
	if err == nil {
		t.Fatal("expected malformed JSON to be refused, not overwritten")
	}
	preserved, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !strings.Contains(string(preserved), "// a comment") {
		t.Errorf("refused merge still modified the file: %s", preserved)
	}
}

func TestMergeAntigravityConfigRefusesNonObjectServers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp_config.json")
	original := []byte(`{"mcpServers":"broken"}`)
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := mergeAntigravityConfig(path, "light-tools", map[string]any{"command": "x"}); err == nil {
		t.Fatal("expected a non-object mcpServers value to be refused, not replaced")
	}
	preserved, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(preserved) != string(original) {
		t.Errorf("refused merge modified the file: %s", preserved)
	}
}

// goldenExecutable mirrors the path runInit resolves so a golden string can
// carry the same absolute binary path the emitted command line will.
func goldenExecutable(t *testing.T) string {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		executable = "light-tools"
	}
	absolute, err := filepath.Abs(executable)
	if err != nil {
		t.Fatal(err)
	}
	return absolute
}

// assertStateRoots asserts the four XDG state roots either all exist or none
// do — a preview must leave the whole layout uncreated.
func assertStateRoots(t *testing.T, root string, want bool) {
	t.Helper()
	for _, path := range []string{
		filepath.Join(root, "config", "light-tools"),
		filepath.Join(root, "data", "light-tools-secrets"),
		filepath.Join(root, "data", "light-tools-snapshots"),
		filepath.Join(root, "runtime", "light-tools-spills"),
	} {
		_, err := os.Stat(path)
		if want && err != nil {
			t.Errorf("init did not create state root %s: %v", path, err)
		}
		if !want && !os.IsNotExist(err) {
			t.Errorf("preview created state root %s: %v", path, err)
		}
	}
}

func TestInitClaudeOutputGolden(t *testing.T) {
	for _, testCase := range []struct {
		name  string
		args  []string
		want  string
		state bool
	}{
		{
			name:  "default",
			args:  nil,
			want:  "State initialized.\nclaude mcp add light-tools -- %s\n",
			state: true,
		},
		{
			name:  "capabilities",
			args:  []string{"--client", "claude", "--enable-shell", "--enable-remote", "--enable-ops"},
			want:  "State initialized.\nclaude mcp add light-tools -- %s --enable-shell --enable-remote --enable-ops\n",
			state: true,
		},
		{
			name:  "dry-run",
			args:  []string{"--client", "claude", "--dry-run"},
			want:  "claude mcp add light-tools -- %s\n",
			state: false,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			root := isolateHome(t)
			args := testCase.args
			output := captureStdout(t, func() error { return runInit(args) })
			if want := fmt.Sprintf(testCase.want, goldenExecutable(t)); output != want {
				t.Errorf("claude output\n got: %q\nwant: %q", output, want)
			}
			assertStateRoots(t, root, testCase.state)
		})
	}
}

func TestInitAntigravityPreviewGolden(t *testing.T) {
	for _, testCase := range []struct {
		name string
		args []string
	}{
		{name: "print", args: []string{"--client", "print"}},
		{name: "dry-run", args: []string{"--client", "antigravity", "--dry-run"}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			root := isolateHome(t)
			args := testCase.args
			output := captureStdout(t, func() error { return runInit(args) })

			command, err := json.Marshal(goldenExecutable(t))
			if err != nil {
				t.Fatal(err)
			}
			snippet := fmt.Sprintf("{\n  \"mcpServers\": {\n    \"light-tools\": {\n      \"command\": %s\n    }\n  }\n}", command)
			want := fmt.Sprintf(
				"# %s\n%s\n\n# %s\n%s\n# permissions\n%s\n",
				filepath.Join(root, ".gemini", "config", "mcp_config.json"),
				snippet,
				filepath.Join(root, ".gemini", "config", "skills", "light-tools", "SKILL.md"),
				antigravitySkill(),
				antigravityPermissions(),
			)
			if output != want {
				t.Errorf("preview output\n got: %q\nwant: %q", output, want)
			}
			for _, line := range []string{"read_file(*)", "write_file(*)", "command(*)", "mcp(light-tools/*)", "deny plus steer"} {
				if !strings.Contains(output, line) {
					t.Errorf("preview lost %q from the permission block:\n%s", line, output)
				}
			}
			assertStateRoots(t, root, false)
			if _, err := os.Stat(filepath.Join(root, ".gemini")); !os.IsNotExist(err) {
				t.Errorf("preview wrote the Antigravity configuration: %v", err)
			}
		})
	}
}
