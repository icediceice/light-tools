package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
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
	"github.com/icediceice/light-tools/internal/security"
	"github.com/icediceice/light-tools/internal/state"
	"github.com/icediceice/light-tools/internal/vaultui"
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
			if err := runInit(os.Args[2:]); err != nil {
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
	server := mcp.New("light-tools", version, os.Getenv("LIGHT_TERSE_OUTPUT") == "1")
	if err := registerTools(server, opts, layout, configuration); err != nil {
		fatal(err)
	}
	if err := server.Serve(context.Background(), os.Stdin, os.Stdout); err != nil {
		fatal(err)
	}
}

func registerTools(server *mcp.Server, opts options, layout state.Layout, configuration config.Config) error {
	secretVault := secret.New(layout.Secrets)
	deniedRoots := []string{layout.Secrets, layout.Snapshots, layout.Spills}
	confiner, err := security.NewConfiner(configuration.AllowedRoots, deniedRoots)
	if err != nil {
		return err
	}
	// The runner is built first so light_file can share its spill store: an
	// oversized read then comes back as a spill_id readable via light_bash.
	bashRunner, err := bash.NewRunner(configuration.AllowedRoots, layout.Spills, secretVault)
	if err != nil {
		return err
	}
	fileHandler, err := filetool.New(filetool.Options{
		Confiner: confiner, SnapshotRoot: layout.Snapshots, Spills: bashRunner.Spills(),
	})
	if err != nil {
		return err
	}
	remoteTransport := remote.New(configuration.Remote, confiner, secretVault)
	opsHandler, err := ops.New(configuration.AllowedRoots, configuration.LogRoots, deniedRoots)
	if err != nil {
		return err
	}
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
		return fmt.Errorf("usage: light-tools vault set|rm|list|ui|ui-reset [name]")
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
		reader := &io.LimitedReader{R: os.Stdin, N: int64(secret.MaxValueBytes + 3)}
		data, err := io.ReadAll(reader)
		if err != nil {
			return err
		}
		value := strings.TrimSuffix(strings.TrimSuffix(string(data), "\n"), "\r")
		if len([]byte(value)) > secret.MaxValueBytes {
			return fmt.Errorf("secret value exceeds %d bytes", secret.MaxValueBytes)
		}
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
	case "ui":
		if len(args) != 1 {
			return fmt.Errorf("usage: light-tools vault ui")
		}
		return runVaultUI(layout, vault)
	case "ui-reset":
		if len(args) != 1 {
			return fmt.Errorf("usage: light-tools vault ui-reset")
		}
		if err := secret.NewPasswordAuth(layout.Secrets).Reset(); err != nil {
			return err
		}
		fmt.Println("UI password cleared. Run `light-tools vault ui` and choose a new one. Secrets were not touched.")
		return nil
	default:
		return fmt.Errorf("unknown vault command %q", args[0])
	}
}

func runVaultUI(layout state.Layout, vault *secret.Vault) error {
	server, err := vaultui.New(vault, secret.NewPasswordAuth(layout.Secrets))
	if err != nil {
		return err
	}
	listener, err := vaultui.Listen()
	if err != nil {
		return err
	}
	url := vaultui.URL(listener)
	fmt.Printf("Vault UI:     %s\n", url)
	fmt.Printf("Secrets root: %s\n", layout.Secrets)
	fmt.Printf("Pairing code: %s (single use, expires in 5 minutes)\n", server.PairingCode())
	if err := openBrowser(url); err != nil {
		fmt.Fprintf(os.Stderr, "Could not open a browser automatically: %v\nCopy the Vault UI URL above into your browser.\n", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	return server.Serve(ctx, listener)
}

func openBrowser(url string) error {
	name, args, err := browserCommand(runtime.GOOS, url)
	if err != nil {
		return err
	}
	command := exec.Command(name, args...)
	if err := command.Start(); err != nil {
		return err
	}
	return command.Process.Release()
}

func browserCommand(goos, url string) (string, []string, error) {
	switch goos {
	case "darwin":
		return "open", []string{url}, nil
	case "windows":
		return "rundll32", []string{"url.dll,FileProtocolHandler", url}, nil
	case "linux":
		return "xdg-open", []string{url}, nil
	default:
		return "", nil, fmt.Errorf("automatic browser opening is unsupported on %s", goos)
	}
}

// stringList collects a repeatable string flag.
type stringList []string

func (list *stringList) String() string { return strings.Join(*list, ",") }

func (list *stringList) Set(value string) error {
	if value == "" {
		return fmt.Errorf("empty value")
	}
	*list = append(*list, value)
	return nil
}

func runInit(args []string) error {
	set := flag.NewFlagSet("init", flag.ContinueOnError)
	set.SetOutput(os.Stderr)
	client := set.String("client", "claude", "target MCP client: claude|antigravity|print")
	workspace := set.String("workspace", "", "write the Antigravity configuration into this workspace instead of the global location")
	dryRun := set.Bool("dry-run", false, "print what would be written without touching disk")
	var caps options
	set.BoolVar(&caps.enableShell, "enable-shell", false, "launch the server with light_bash registered")
	set.BoolVar(&caps.enableRemote, "enable-remote", false, "launch the server with light_ssh and light_scp registered")
	set.BoolVar(&caps.enableOps, "enable-ops", false, "launch the server with light_ops registered")
	var disabledTools stringList
	set.Var(&disabledTools, "disable-tool", "withhold one light-tools tool from the model (repeatable, Antigravity only)")
	if err := set.Parse(args); err != nil {
		return err
	}
	switch *client {
	case "claude", "antigravity", "print":
	default:
		return fmt.Errorf("unknown --client %q (want claude, antigravity, or print)", *client)
	}
	// A preview must not touch the disk, so the state layout is created only
	// once the run is known to be a real init for a known client.
	preview := *dryRun || *client == "print"
	if !preview {
		if _, err := state.Resolve(); err != nil {
			return err
		}
	}
	executable, err := os.Executable()
	if err != nil {
		executable = "light-tools"
	}
	executable, _ = filepath.Abs(executable)

	if *client == "claude" {
		command := append([]string{"claude", "mcp", "add", "light-tools", "--", executable}, capabilityArgs(caps)...)
		if preview {
			fmt.Printf("%s\n", strings.Join(command, " "))
			return nil
		}
		fmt.Printf("State initialized.\n%s\n", strings.Join(command, " "))
		return nil
	}
	return initAntigravity(executable, caps, disabledTools, *workspace, preview)
}

// initAntigravity writes, or previews, both halves of the Antigravity setup:
// the mcpServers entry and the native-tool suppression profile.
func initAntigravity(executable string, caps options, disabledTools []string, workspace string, preview bool) error {
	if workspace != "" {
		absolute, err := filepath.Abs(workspace)
		if err != nil {
			return err
		}
		workspace = absolute
	}
	home := ""
	if workspace == "" {
		resolved, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		home = resolved
	}
	configPath, skillPath := antigravityPaths(home, workspace)
	entry := antigravityServer(executable, caps, disabledTools)
	snippet, err := json.MarshalIndent(map[string]any{"mcpServers": map[string]any{antigravityServerName: entry}}, "", "  ")
	if err != nil {
		return err
	}
	if preview {
		fmt.Printf("# %s\n%s\n\n# %s\n%s\n# permissions\n%s\n", configPath, snippet, skillPath, antigravitySkill(), antigravityPermissions())
		return nil
	}
	if err := mergeAntigravityConfig(configPath, antigravityServerName, entry); err != nil {
		return err
	}
	if err := writePrivateFile(skillPath, []byte(antigravitySkill())); err != nil {
		return err
	}
	fmt.Printf("State initialized.\nwrote %s\nwrote %s\n\nApply these permissions in Antigravity:\n%s\n", configPath, skillPath, antigravityPermissions())
	return nil
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "light-tools:", err)
	os.Exit(1)
}
