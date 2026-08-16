package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/icediceice/light-tools/internal/bash"
	"github.com/icediceice/light-tools/internal/config"
	"github.com/icediceice/light-tools/internal/filetool"
	"github.com/icediceice/light-tools/internal/mcp"
	"github.com/icediceice/light-tools/internal/ops"
	"github.com/icediceice/light-tools/internal/portable"
	"github.com/icediceice/light-tools/internal/remote"
	"github.com/icediceice/light-tools/internal/secret"
	"github.com/icediceice/light-tools/internal/state"
)

var version = "0.1.0-dev"

type options struct {
	enableShell  bool
	enableRemote bool
	enableOps    bool
}

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "init":
			if err := runInit(); err != nil {
				fatal(err)
			}
			return
		case "vault":
			if err := runVault(os.Args[2:]); err != nil {
				fatal(err)
			}
			return
		case "version", "--version", "-version":
			fmt.Println(version)
			return
		}
	}

	var opts options
	flag.BoolVar(&opts.enableShell, "enable-shell", false, "register light_bash")
	flag.BoolVar(&opts.enableRemote, "enable-remote", false, "register light_ssh and light_scp")
	flag.BoolVar(&opts.enableOps, "enable-ops", false, "register read-only light_ops")
	flag.Parse()

	layout, err := state.Resolve()
	if err != nil {
		fatal(err)
	}
	workingDirectory, err := os.Getwd()
	if err != nil {
		fatal(err)
	}
	configuration, err := config.Load(filepath.Join(layout.Config, "config.toml"), workingDirectory)
	if err != nil {
		fatal(err)
	}
	server := mcp.New("light-tools", version)
	if err := registerTools(server, opts, layout, configuration); err != nil {
		fatal(err)
	}
	if err := server.Serve(context.Background(), os.Stdin, os.Stdout); err != nil {
		fatal(err)
	}
}

func registerTools(server *mcp.Server, opts options, layout state.Layout, configuration config.Config) error {
	fileHandler, err := filetool.New(filetool.Options{Roots: configuration.AllowedRoots, SnapshotRoot: layout.Snapshots})
	if err != nil {
		return err
	}
	secretVault := secret.New(layout.Secrets)
	bashRunner, err := bash.NewRunner(configuration.AllowedRoots, layout.Spills, secretVault)
	if err != nil {
		return err
	}
	remoteTransport := remote.New(configuration.Remote, configuration.AllowedRoots, secretVault)
	opsHandler := ops.New()
	bashHandler := func(ctx context.Context, raw json.RawMessage) (any, error) {
		var request bash.Request
		if err := json.Unmarshal(raw, &request); err != nil {
			return nil, err
		}
		return bashRunner.Run(ctx, request)
	}
	definitions := []struct {
		enabled     bool
		name        string
		description string
		handler     portable.Handler
	}{
		{true, "light_file", "Bounded filesystem reads, searches, symbols, diffs, and transactional mutations.", fileHandler.Portable()},
		{opts.enableShell, "light_bash", "Opt-in local shell execution with bounded output and secret references.", bashHandler},
		{opts.enableRemote, "light_ssh", "Opt-in SSH execution through explicit profiles.", remoteTransport.SSH},
		{opts.enableRemote, "light_scp", "Opt-in SCP transfer through explicit profiles.", remoteTransport.SCP},
		{opts.enableOps, "light_ops", "Opt-in read-only service discovery, probes, and log inspection.", opsHandler.Handle},
	}
	sort.SliceStable(definitions, func(i, j int) bool { return definitions[i].name < definitions[j].name })
	for _, definition := range definitions {
		if !definition.enabled {
			continue
		}
		err := server.Register(mcp.Tool{
			Name: definition.name, Description: definition.description, Handler: definition.handler,
			InputSchema: toolSchema(definition.name),
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func toolSchema(name string) map[string]any {
	stringType := func() map[string]any { return map[string]any{"type": "string"} }
	integerType := func() map[string]any { return map[string]any{"type": "integer"} }
	booleanType := func() map[string]any { return map[string]any{"type": "boolean"} }
	stringMap := func() map[string]any {
		return map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}}
	}
	properties := map[string]any{}
	switch name {
	case "light_file":
		for _, field := range []string{"verb", "path", "target", "from", "to", "payload", "patch", "patch_path", "content", "new_string", "find", "replace", "start_guard", "end_guard", "cursor", "name", "symbol", "pattern", "a", "b", "expected_sha", "context_epoch"} {
			properties[field] = stringType()
		}
		for _, field := range []string{"start_line", "end_line", "offset", "limit", "context", "diff_context", "fuzz", "count", "version"} {
			properties[field] = integerType()
		}
		for _, field := range []string{"all", "regex", "dry_run", "overwrite", "allow_unbalanced", "force"} {
			properties[field] = booleanType()
		}
		readItem := map[string]any{"type": "object", "properties": map[string]any{"path": stringType(), "offset": integerType(), "limit": integerType(), "name": stringType()}, "required": []string{"path"}, "additionalProperties": false}
		properties["items"] = map[string]any{"type": "array", "items": readItem}
		properties["reads"] = map[string]any{"type": "array", "items": readItem}
		properties["spans"] = map[string]any{"type": "array", "items": map[string]any{"type": "object", "properties": map[string]any{"start_line": integerType(), "end_line": integerType(), "start_guard": stringType(), "end_guard": stringType(), "new_string": stringType()}, "required": []string{"start_line", "new_string"}, "additionalProperties": false}}
	case "light_bash":
		for _, field := range []string{"verb", "task_id", "command", "cwd", "output_mode", "filter", "spill_id", "spill", "line_range"} {
			properties[field] = stringType()
		}
		properties["async"], properties["timeout_ms"], properties["lines"] = booleanType(), integerType(), integerType()
		properties["env_refs"], properties["file_refs"] = stringMap(), stringMap()
	case "light_ssh":
		for _, field := range []string{"profile", "remote", "command", "key", "key_ref", "cert_ref", "proxy_jump"} {
			properties[field] = stringType()
		}
		properties["port"], properties["timeout_ms"] = integerType(), integerType()
	case "light_scp":
		for _, field := range []string{"profile", "src", "dst", "key", "key_ref", "cert_ref", "proxy_jump"} {
			properties[field] = stringType()
		}
		properties["port"], properties["timeout_ms"] = integerType(), integerType()
	case "light_ops":
		for _, field := range []string{"verb", "task_id", "service", "path", "pattern", "since", "since_ts", "include", "exclude"} {
			properties[field] = stringType()
		}
		for _, field := range []string{"context", "lines", "port", "pid"} {
			properties[field] = integerType()
		}
		for _, field := range []string{"async", "drill", "refresh"} {
			properties[field] = booleanType()
		}
		properties["services"] = map[string]any{"type": "array", "items": stringType()}
	}
	return map[string]any{"type": "object", "properties": properties, "additionalProperties": false}
}

func runVault(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: light-tools vault set|rm|list [name]")
	}
	layout, err := state.Resolve()
	if err != nil {
		return err
	}
	vault := secret.New(layout.Secrets)
	switch args[0] {
	case "set":
		if len(args) != 2 {
			return fmt.Errorf("usage: light-tools vault set NAME (value is read from stdin)")
		}
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			return err
		}
		value := strings.TrimSuffix(strings.TrimSuffix(string(data), "\n"), "\r")
		return vault.Set(args[1], value)
	case "rm":
		if len(args) != 2 {
			return fmt.Errorf("usage: light-tools vault rm NAME")
		}
		return vault.Remove(args[1])
	case "list":
		names, err := vault.List()
		if err != nil {
			return err
		}
		for _, name := range names {
			fmt.Println(name)
		}
		return nil
	default:
		return fmt.Errorf("unknown vault command %q", args[0])
	}
}

func runInit() error {
	_, err := state.Resolve()
	if err != nil {
		return err
	}
	executable, err := os.Executable()
	if err != nil {
		executable = "light-tools"
	}
	executable, _ = filepath.Abs(executable)
	fmt.Printf("State initialized.\nclaude mcp add light-tools -- %s\n", executable)
	return nil
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "light-tools:", err)
	os.Exit(1)
}
