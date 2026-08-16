package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"

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

	if _, err := state.Resolve(); err != nil {
		fatal(err)
	}
	server := mcp.New("light-tools", version)
	if err := registerTools(server, opts); err != nil {
		fatal(err)
	}
	if err := server.Serve(context.Background(), os.Stdin, os.Stdout); err != nil {
		fatal(err)
	}
}

func registerTools(server *mcp.Server, opts options) error {
	definitions := []struct {
		enabled     bool
		name        string
		description string
	}{
		{true, "light_file", "Bounded filesystem reads, searches, symbols, diffs, and transactional mutations."},
		{opts.enableShell, "light_bash", "Opt-in local shell execution with bounded output and secret references."},
		{opts.enableRemote, "light_ssh", "Opt-in SSH execution through explicit profiles."},
		{opts.enableRemote, "light_scp", "Opt-in SCP transfer through explicit profiles."},
		{opts.enableOps, "light_ops", "Opt-in read-only service discovery, probes, and log inspection."},
	}
	sort.SliceStable(definitions, func(i, j int) bool { return definitions[i].name < definitions[j].name })
	for _, definition := range definitions {
		if !definition.enabled {
			continue
		}
		name := definition.name
		err := server.Register(mcp.Tool{
			Name: name, Description: definition.description,
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"verb":    map[string]any{"type": "string"},
					"path":    map[string]any{"type": "string"},
					"payload": map[string]any{"type": "string"},
				},
				"additionalProperties": true,
			},
			Handler: func(context.Context, json.RawMessage) (any, error) {
				return nil, &portable.DiagnosticError{Code: "E_NOT_IMPLEMENTED", Message: name + " handler is not wired"}
			},
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func runInit() error {
	layout, err := state.Resolve()
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
