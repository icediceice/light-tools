package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/icediceice/light-tools/internal/config"
	"github.com/icediceice/light-tools/internal/mcp"
	"github.com/icediceice/light-tools/internal/state"
	"github.com/icediceice/light-tools/internal/telemetry"
)

// The precedence is the whole contract: operator config outranks the UI marker,
// and the UI marker outranks the unconfined default. A UI toggle that could
// override config.toml would let a switch silently contradict the file the
// operator wrote.
func TestConfinementPrecedence(t *testing.T) {
	cwd := "/srv/spawn"
	operator := config.Config{AllowedRoots: []string{"/work/project"}, RootsConfigured: true}

	if got := resolveConfinement(operator, true, cwd); len(got.AllowedRoots) != 1 || got.AllowedRoots[0] != "/work/project" {
		t.Fatalf("the UI marker overrode operator config: %v", got.AllowedRoots)
	}
	if got := resolveConfinement(config.Config{}, true, cwd); len(got.AllowedRoots) != 1 || got.AllowedRoots[0] != cwd {
		t.Fatalf("the UI marker did not confine to the working directory: %v", got.AllowedRoots)
	}
	if got := resolveConfinement(config.Config{}, false, cwd); len(got.AllowedRoots) != 0 {
		t.Fatalf("the default posture is not unconfined: %v", got.AllowedRoots)
	}
}

// registerTools is the composition root. A handler built directly in a test
// never exercises what main wires, so the posture has to be asserted through
// THIS path: with no configured roots, every tool must register and light_file
// must reach a path outside the server's working directory.
func TestRegisterToolsUnconfinedReachesOutsideTheWorkingDirectory(t *testing.T) {
	root := t.TempDir()
	layout := state.Layout{
		Config: filepath.Join(root, "config"), Secrets: filepath.Join(root, "secrets"),
		Snapshots: filepath.Join(root, "snapshots"), Spills: filepath.Join(root, "spills"),
		Telemetry: filepath.Join(root, "telemetry"),
	}
	for _, path := range []string{layout.Config, layout.Secrets, layout.Snapshots, layout.Spills, layout.Telemetry} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}

	// Deliberately OUTSIDE root: this is the edit that the old cwd-confined
	// default made impossible, and the reason light-tools could not replace the
	// agent's native edit tool.
	outside := filepath.Join(t.TempDir(), "elsewhere.txt")
	if err := os.WriteFile(outside, []byte("original\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	server := mcp.New("light-tools", "test", false)
	opts, err := newOptions(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	configuration := config.Config{Remote: map[string]config.RemoteProfile{}}
	if err := registerTools(server, opts, layout, configuration, telemetry.New(layout.Telemetry)); err != nil {
		t.Fatalf("registerTools refused to start unconfined: %v", err)
	}

	result := callTool(t, server, "light_file", map[string]any{
		"verb": "read", "path": outside, "offset": 0, "limit": 10,
	})
	if !strings.Contains(result, "original") {
		t.Fatalf("unconfined light_file could not read outside the working directory: %s", result)
	}
}

// The counterpart, and the reason the test above is not vacuous: with roots
// configured, registerTools must REFUSE the same read. If this ever passes for
// the same reason the unconfined case does, neither is measuring anything.
func TestRegisterToolsConfinedRefusesOutsideTheWorkingDirectory(t *testing.T) {
	root := t.TempDir()
	layout := state.Layout{
		Config: filepath.Join(root, "config"), Secrets: filepath.Join(root, "secrets"),
		Snapshots: filepath.Join(root, "snapshots"), Spills: filepath.Join(root, "spills"),
		Telemetry: filepath.Join(root, "telemetry"),
	}
	for _, path := range []string{layout.Config, layout.Secrets, layout.Snapshots, layout.Spills, layout.Telemetry} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	outside := filepath.Join(t.TempDir(), "elsewhere.txt")
	if err := os.WriteFile(outside, []byte("original\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	server := mcp.New("light-tools", "test", false)
	opts, err := newOptions(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	configuration := config.Config{
		AllowedRoots: []string{root}, RootsConfigured: true,
		Remote: map[string]config.RemoteProfile{},
	}
	if err := registerTools(server, opts, layout, configuration, telemetry.New(layout.Telemetry)); err != nil {
		t.Fatal(err)
	}

	result := callTool(t, server, "light_file", map[string]any{
		"verb": "read", "path": outside, "offset": 0, "limit": 10,
	})
	if strings.Contains(result, "original") {
		t.Fatalf("a confined light_file read outside its roots: %s", result)
	}
	if !strings.Contains(result, "escapes allowed roots") {
		t.Fatalf("confined refusal did not name the boundary: %s", result)
	}
}

// Widening the boundary must not widen the denied private-state roots. The
// telemetry root is denied so a tool call cannot fabricate the aggregates the
// vault UI renders as measured data; that has to hold in BOTH postures.
func TestUnconfinedStillRefusesTheTelemetryRoot(t *testing.T) {
	root := t.TempDir()
	layout := state.Layout{
		Config: filepath.Join(root, "config"), Secrets: filepath.Join(root, "secrets"),
		Snapshots: filepath.Join(root, "snapshots"), Spills: filepath.Join(root, "spills"),
		Telemetry: filepath.Join(root, "telemetry"),
	}
	for _, path := range []string{layout.Config, layout.Secrets, layout.Snapshots, layout.Spills, layout.Telemetry} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	server := mcp.New("light-tools", "test", false)
	opts, err := newOptions(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := registerTools(server, opts, layout, config.Config{Remote: map[string]config.RemoteProfile{}}, telemetry.New(layout.Telemetry)); err != nil {
		t.Fatal(err)
	}

	forged := filepath.Join(layout.Telemetry, "session-v1-forged.json")
	result := callTool(t, server, "light_file", map[string]any{
		"verb": "write", "path": forged, "content": "{}",
	})
	if !strings.Contains(result, "private state root") {
		t.Fatalf("unconfined light_file was allowed into the telemetry root: %s", result)
	}
	if _, err := os.Stat(forged); err == nil {
		t.Fatal("a forged telemetry snapshot was actually written")
	}
}

// callTool drives the REAL stdio dispatch path rather than reaching into a
// handler, so what it exercises is what a client gets.
func callTool(t *testing.T, server *mcp.Server, name string, arguments map[string]any) string {
	t.Helper()
	request, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": name, "arguments": arguments},
	})
	if err != nil {
		t.Fatal(err)
	}
	var output strings.Builder
	if err := server.Serve(context.Background(), strings.NewReader(string(request)+"\n"), &output); err != nil {
		t.Fatal(err)
	}
	return output.String()
}
