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

const version = "0.1.0-dev"

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
	remoteTransport := remote.New(configuration.Remote, configuration.AllowedRoots)
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
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"verb": map[string]any{"type": "string"}, "path": map[string]any{"type": "string"},
					"payload": map[string]any{"type": "string"},
				},
				"additionalProperties": true,
			},
		})
		if err != nil {
			return err
		}
	}
	return nil
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
