package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/icediceice/light-tools/internal/bash"
	"github.com/icediceice/light-tools/internal/config"
	"github.com/icediceice/light-tools/internal/filetool"
	"github.com/icediceice/light-tools/internal/mcp"
	"github.com/icediceice/light-tools/internal/ops"
	"github.com/icediceice/light-tools/internal/remote"
	"github.com/icediceice/light-tools/internal/secret"
	"github.com/icediceice/light-tools/internal/state"
	"github.com/icediceice/light-tools/internal/terse"
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

func TestToolSchemaFieldsMatchHandlerRequests(t *testing.T) {
	requests := map[string]any{
		"light_file": filetool.Request{},
		"light_bash": bash.Request{},
		"light_ssh":  remote.SSHRequest{},
		"light_scp":  remote.SCPRequest{},
		"light_ops":  ops.Request{},
	}
	for name, request := range requests {
		properties := toolSchema(name)["properties"].(map[string]any)
		got := make([]string, 0, len(properties))
		for field := range properties {
			got = append(got, field)
		}
		sort.Strings(got)
		want := jsonFieldNames(reflect.TypeOf(request))
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("%s schema fields mismatch\n got: %v\nwant: %v", name, got, want)
		}
	}
}

func jsonFieldNames(request reflect.Type) []string {
	var names []string
	for index := 0; index < request.NumField(); index++ {
		tag := strings.Split(request.Field(index).Tag.Get("json"), ",")[0]
		if tag != "" && tag != "-" {
			names = append(names, tag)
		}
	}
	sort.Strings(names)
	return names
}

func TestRegistrationWithholdsOnlyDisabledTools(t *testing.T) {
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
		name     string
		disabled []string
		want     []string
	}{
		{name: "default", want: []string{"light_bash", "light_file", "light_ops", "light_scp", "light_ssh"}},
		{name: "no_bash", disabled: []string{"light_bash"}, want: []string{"light_file", "light_ops", "light_scp", "light_ssh"}},
		{name: "no_file", disabled: []string{"light_file"}, want: []string{"light_bash", "light_ops", "light_scp", "light_ssh"}},
		{name: "no_ops", disabled: []string{"light_ops"}, want: []string{"light_bash", "light_file", "light_scp", "light_ssh"}},
		{name: "no_scp", disabled: []string{"light_scp"}, want: []string{"light_bash", "light_file", "light_ops", "light_ssh"}},
		{name: "no_ssh", disabled: []string{"light_ssh"}, want: []string{"light_bash", "light_file", "light_ops", "light_scp"}},
		{name: "no_remote", disabled: []string{"light_ssh", "light_scp"}, want: []string{"light_bash", "light_file", "light_ops"}},
		{name: "everything_withheld", disabled: toolNames, want: nil},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			opts, err := newOptions(test.disabled, nil)
			if err != nil {
				t.Fatal(err)
			}
			server := mcp.New("test", "1")
			if err := registerTools(server, opts, layout, configuration, nil); err != nil {
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

// A withheld tool is named rather than implied by a flag, so a typo must fail
// loudly at startup instead of quietly registering the surface the operator
// meant to withhold.
func TestNewOptionsRejectsUnknownToolName(t *testing.T) {
	if _, err := newOptions([]string{"light_bash", "light_shell"}, nil); err == nil {
		t.Fatal("newOptions accepted an unknown tool name")
	}
	if _, err := newOptions(toolNames, nil); err != nil {
		t.Fatalf("newOptions rejected the full known set: %v", err)
	}
	if _, err := newOptions(nil, map[string]bool{"light_shell": true}); err == nil {
		t.Fatal("newOptions accepted an unknown tool name from a persisted marker")
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
	t.Setenv("USERPROFILE", root)
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

func TestInitClaudeCarriesDisableFlags(t *testing.T) {
	isolateHome(t)
	output := captureStdout(t, func() error {
		return runInit([]string{"--client", "claude", "--disable-tool", "light_bash", "--disable-tool", "light_ops"})
	})
	if !strings.Contains(output, " -- ") || !strings.Contains(output, "--disable-tool light_bash --disable-tool light_ops") {
		t.Fatalf("claude command lost the disable flags: %s", output)
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
		return runInit([]string{"--client", "antigravity", "--disable-tool", "light_scp"})
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
	if len(args) != 2 || args[0] != "--disable-tool" || args[1] != "light_scp" {
		t.Errorf("light-tools entry lost its disable args: %v", entry["args"])
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
		return runInit([]string{"--client", "antigravity", "--disable-tool", "light_scp"})
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
			name:  "withheld",
			args:  []string{"--client", "claude", "--disable-tool", "light_bash", "--disable-tool", "light_ssh"},
			want:  "State initialized.\nclaude mcp add light-tools -- %s --disable-tool light_bash --disable-tool light_ssh\n",
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

func TestVaultBareCommandKeepsUsageError(t *testing.T) {
	isolateHome(t)
	if err := runVault(nil); err == nil || !strings.Contains(err.Error(), "vault set|rm|list|ui") {
		t.Fatalf("bare vault command = %v", err)
	}
}

func TestBrowserCommandIsCrossPlatformAndUsesBareURL(t *testing.T) {
	url := "http://127.0.0.1:43210"
	cases := map[string]struct {
		name string
		args []string
	}{
		"linux":   {name: "xdg-open", args: []string{url}},
		"darwin":  {name: "open", args: []string{url}},
		"windows": {name: "rundll32", args: []string{"url.dll,FileProtocolHandler", url}},
	}
	for goos, want := range cases {
		name, args, err := browserCommand(goos, url)
		if err != nil {
			t.Fatal(err)
		}
		if name != want.name || !reflect.DeepEqual(args, want.args) {
			t.Errorf("%s command = %s %#v, want %s %#v", goos, name, args, want.name, want.args)
		}
		for _, argument := range args {
			if strings.Contains(argument, "#") || strings.Contains(argument, "token") {
				t.Fatalf("%s browser argument contains launch authority: %q", goos, argument)
			}
		}
	}
	if _, _, err := browserCommand("plan9", url); err == nil {
		t.Fatal("unsupported platform accepted")
	}
}

func TestVaultSetBoundsStandardInput(t *testing.T) {
	isolateHome(t)
	input, err := os.CreateTemp(t.TempDir(), "vault-input-*")
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	if _, err := input.WriteString(strings.Repeat("x", secret.MaxValueBytes+1)); err != nil {
		t.Fatal(err)
	}
	if _, err := input.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	previous := os.Stdin
	os.Stdin = input
	defer func() { os.Stdin = previous }()
	if err := runVault([]string{"set", "large"}); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized stdin = %v", err)
	}
}

func TestVaultUIResetClearsOnlyPasswordVerifier(t *testing.T) {
	isolateHome(t)
	layout, err := state.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	auth := secret.NewPasswordAuth(layout.Secrets)
	if err := auth.Setup("forgotten password"); err != nil {
		t.Fatal(err)
	}

	output := captureStdout(t, func() error { return runVault([]string{"ui-reset"}) })
	if !strings.Contains(output, "Secrets were not touched") {
		t.Fatalf("reset output omitted vault safety statement: %q", output)
	}
	configured, err := auth.Configured()
	if err != nil || configured {
		t.Fatalf("Configured after CLI reset = %t, %v", configured, err)
	}
	if err := auth.Setup("replacement password"); err != nil {
		t.Fatalf("setup after CLI reset: %v", err)
	}
	if err := runVault([]string{"ui-reset", "extra"}); err == nil || !strings.Contains(err.Error(), "vault ui-reset") {
		t.Fatalf("ui-reset accepted extra argument: %v", err)
	}
}

func TestAllFiveToolsThroughServeRawAndTerse(t *testing.T) {
	root := t.TempDir()
	layout := state.Layout{
		Config: filepath.Join(root, "config"), Secrets: filepath.Join(root, "secrets"),
		Snapshots: filepath.Join(root, "snapshots"), Spills: filepath.Join(root, "spills"),
	}
	for _, path := range []string{layout.Config, layout.Secrets, layout.Snapshots, layout.Spills} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	probe := filepath.Join(root, "serve-probe.log")
	content := strings.Repeat("word ", 120)
	if err := os.WriteFile(probe, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	configuration := config.Config{
		AllowedRoots: []string{root},
		LogRoots:     []string{root},
		Remote:       map[string]config.RemoteProfile{},
	}

	raw := runAllFiveTranscript(t, layout, configuration, probe, content, false)
	firstTerse := runAllFiveTranscript(t, layout, configuration, probe, content, true)
	secondTerse := runAllFiveTranscript(t, layout, configuration, probe, content, true)

	for _, name := range []string{"light_file", "light_bash", "light_ops"} {
		rawResult, terseResult := raw[name], firstTerse[name]
		if rawResult.IsError || terseResult.IsError {
			t.Fatalf("%s success probe returned error: raw=%#v terse=%#v", name, rawResult, terseResult)
		}
		if len(rawResult.Content) != 1 || len(terseResult.Content) != 1 {
			t.Fatalf("%s returned unexpected content: raw=%#v terse=%#v", name, rawResult, terseResult)
		}
		want := decodeJSONResult(t, rawResult.Content[0].Text)
		if terseResult.Content[0].Text == rawResult.Content[0].Text {
			t.Fatalf("%s representative payload did not exercise terse output", name)
		}
		if len(terseResult.Content[0].Text) >= len(rawResult.Content[0].Text) {
			t.Fatalf("%s terse output did not shrink: %d >= %d", name, len(terseResult.Content[0].Text), len(rawResult.Content[0].Text))
		}
		got, err := terse.Decode([]byte(terseResult.Content[0].Text))
		if err != nil {
			t.Fatalf("%s emitted invalid terse: %v", name, err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%s terse semantics drifted\nwant %#v\n got %#v", name, want, got)
		}
		if !reflect.DeepEqual(terseResult, secondTerse[name]) {
			t.Fatalf("%s terse output was not deterministic\nfirst %#v\nsecond %#v", name, terseResult, secondTerse[name])
		}
	}

	for _, name := range []string{"light_ssh", "light_scp"} {
		if !raw[name].IsError || !firstTerse[name].IsError {
			t.Fatalf("%s refusal probe did not return a tool error: raw=%#v terse=%#v", name, raw[name], firstTerse[name])
		}
		if !reflect.DeepEqual(raw[name], firstTerse[name]) || !reflect.DeepEqual(firstTerse[name], secondTerse[name]) {
			t.Fatalf("%s error envelope was formatted or unstable", name)
		}
	}
}

func runAllFiveTranscript(t *testing.T, layout state.Layout, configuration config.Config, probe, content string, terseOutput bool) map[string]mcp.Result {
	t.Helper()
	server := mcp.New("test", "1", terseOutput)
	if err := registerTools(server, options{}, layout, configuration, nil); err != nil {
		t.Fatal(err)
	}
	command := "printf '%s' '" + content + "'"
	if runtime.GOOS == "windows" {
		command = "[Console]::Out.Write('" + content + "')"
	}
	calls := []struct {
		name      string
		arguments map[string]any
	}{
		{name: "light_file", arguments: map[string]any{"verb": "read", "path": probe, "offset": 0, "limit": 20}},
		{name: "light_bash", arguments: map[string]any{"command": command, "cwd": filepath.Dir(probe), "timeout_ms": 30000}},
		{name: "light_ops", arguments: map[string]any{"verb": "log_window", "path": probe}},
		{name: "light_ssh", arguments: map[string]any{"command": "must-not-execute"}},
		{name: "light_scp", arguments: map[string]any{"src": probe, "dst": filepath.Join(filepath.Dir(probe), "also-local")}},
	}

	requests := []map[string]any{
		{"jsonrpc": "2.0", "id": 1, "method": "tools/list", "params": map[string]any{}},
	}
	for index, call := range calls {
		requests = append(requests, map[string]any{
			"jsonrpc": "2.0", "id": index + 2, "method": "tools/call",
			"params": map[string]any{"name": call.name, "arguments": call.arguments},
		})
	}
	var input strings.Builder
	for _, request := range requests {
		encoded, err := json.Marshal(request)
		if err != nil {
			t.Fatal(err)
		}
		input.Write(encoded)
		input.WriteByte('\n')
	}
	var output strings.Builder
	if err := server.Serve(context.Background(), strings.NewReader(input.String()), &output); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != len(requests) {
		t.Fatalf("got %d responses for %d requests: %s", len(lines), len(requests), output.String())
	}

	var listed struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &listed); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range listed.Result.Tools {
		names = append(names, tool.Name)
	}
	wantNames := []string{"light_bash", "light_file", "light_ops", "light_scp", "light_ssh"}
	if !reflect.DeepEqual(names, wantNames) {
		t.Fatalf("all-five list drifted: got %v want %v", names, wantNames)
	}

	results := make(map[string]mcp.Result, len(calls))
	for index, call := range calls {
		var envelope struct {
			Result mcp.Result `json:"result"`
			Error  any        `json:"error"`
		}
		if err := json.Unmarshal([]byte(lines[index+1]), &envelope); err != nil {
			t.Fatal(err)
		}
		if envelope.Error != nil {
			t.Fatalf("%s returned protocol error: %#v", call.name, envelope.Error)
		}
		results[call.name] = envelope.Result
	}
	return results
}

func decodeJSONResult(t *testing.T, text string) any {
	t.Helper()
	decoder := json.NewDecoder(strings.NewReader(text))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		t.Fatalf("raw result is not JSON: %v\n%s", err, text)
	}
	return value
}
