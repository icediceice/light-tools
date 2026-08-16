package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/icediceice/light-tools/internal/filetool"
	"github.com/icediceice/light-tools/internal/mcp"
	"github.com/icediceice/light-tools/internal/portable"
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
			fatal(fmt.Errorf("vault command is not available in this build yet"))
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
	server := mcp.New("light-tools", version)
	if err := registerTools(server, opts, layout); err != nil {
		fatal(err)
	}
	if err := server.Serve(context.Background(), os.Stdin, os.Stdout); err != nil {
		fatal(err)
	}
}

func registerTools(server *mcp.Server, opts options, layout state.Layout) error {
	workingDirectory, err := os.Getwd()
	if err != nil {
		return err
	}
	fileHandler, err := filetool.New(filetool.Options{Roots: []string{workingDirectory}, SnapshotRoot: layout.Snapshots})
	if err != nil {
		return err
	}
	definitions := []struct {
		enabled     bool
		name        string
		description string
		handler     portable.Handler
	}{
		{true, "light_file", "Bounded filesystem reads, searches, symbols, diffs, and transactional mutations.", fileHandler.Portable()},
		{opts.enableShell, "light_bash", "Opt-in local shell execution with bounded output and secret references.", unavailable("light_bash")},
		{opts.enableRemote, "light_ssh", "Opt-in SSH execution through explicit profiles.", unavailable("light_ssh")},
		{opts.enableRemote, "light_scp", "Opt-in SCP transfer through explicit profiles.", unavailable("light_scp")},
		{opts.enableOps, "light_ops", "Opt-in read-only service discovery, probes, and log inspection.", unavailable("light_ops")},
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

func unavailable(name string) portable.Handler {
	return func(context.Context, json.RawMessage) (any, error) {
		return nil, &portable.DiagnosticError{Code: "E_NOT_IMPLEMENTED", Message: name + " handler is not wired"}
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
