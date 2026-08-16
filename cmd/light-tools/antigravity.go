package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// antigravityServerName is the key light-tools claims inside mcpServers.
const antigravityServerName = "light-tools"

// antigravityPaths resolves the documented Antigravity locations. An empty
// workspace selects the global pair shared by Antigravity CLI, IDE, and 2.0.
func antigravityPaths(home, workspace string) (configPath string, skillPath string) {
	if workspace != "" {
		return filepath.Join(workspace, ".agents", "mcp_config.json"),
			filepath.Join(workspace, ".agents", "skills", antigravityServerName, "SKILL.md")
	}
	return filepath.Join(home, ".gemini", "config", "mcp_config.json"),
		filepath.Join(home, ".gemini", "config", "skills", antigravityServerName, "SKILL.md")
}

// antigravityServer builds the stdio entry from the documented property set
// only: command, args, env, cwd, disabled, disabledTools. serverUrl, headers,
// and oauth are remote-transport fields, the retired httpUrl is never emitted,
// and a top-level timeout is no longer accepted by Antigravity.
func antigravityServer(executable string, caps options, disabledTools []string) map[string]any {
	entry := map[string]any{"command": executable}
	if args := capabilityArgs(caps); len(args) > 0 {
		entry["args"] = args
	}
	if len(disabledTools) > 0 {
		entry["disabledTools"] = disabledTools
	}
	return entry
}

// capabilityArgs mirrors the opt-in server flags into the launch arguments so
// the client starts light-tools with the same surface the operator asked for.
func capabilityArgs(caps options) []string {
	var args []string
	if caps.enableShell {
		args = append(args, "--enable-shell")
	}
	if caps.enableRemote {
		args = append(args, "--enable-remote")
	}
	if caps.enableOps {
		args = append(args, "--enable-ops")
	}
	return args
}

// mergeAntigravityConfig writes entry under mcpServers[name] while preserving
// every other server and every unrelated top-level key. A malformed existing
// file is an error, never something to overwrite.
func mergeAntigravityConfig(path string, name string, entry map[string]any) error {
	// Every value except our own entry is carried through as raw JSON: decoding
	// into map[string]any would round-trip numbers via float64 and silently
	// rewrite an operator's large integer literals.
	root := map[string]json.RawMessage{}
	existing, err := os.ReadFile(path)
	switch {
	case err == nil && len(strings.TrimSpace(string(existing))) > 0:
		if err := json.Unmarshal(existing, &root); err != nil {
			return fmt.Errorf("%s is not valid JSON (Antigravity does not accept comments): %w", path, err)
		}
	case err != nil && !os.IsNotExist(err):
		return err
	}
	servers := map[string]json.RawMessage{}
	if raw, ok := root["mcpServers"]; ok && len(strings.TrimSpace(string(raw))) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &servers); err != nil {
			return fmt.Errorf("%s has a non-object mcpServers value; refusing to replace it: %w", path, err)
		}
	}
	encodedEntry, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	servers[name] = encodedEntry
	encodedServers, err := json.Marshal(servers)
	if err != nil {
		return err
	}
	root["mcpServers"] = encodedServers
	encoded, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return err
	}
	return writePrivateFile(path, append(encoded, '\n'))
}

// writePrivateFile creates the parent chain and replaces path atomically.
func writePrivateFile(path string, data []byte) error {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".light-tools-*")
	if err != nil {
		return err
	}
	name := temporary.Name()
	defer os.Remove(name)
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

// antigravitySkill is the steering half of the suppression profile. Antigravity
// has no documented switch that removes native tools from the model's tool
// list, so the agent is told to stop reaching for them.
func antigravitySkill() string {
	return `---
name: light-tools
description: Routes every file read, edit, search, symbol lookup, shell command, remote copy, and log inspection through the light-tools MCP server instead of the native tools. Use for all filesystem, terminal, SSH, and service-log work.
---

# light-tools

This environment runs the ` + "`light-tools`" + ` MCP server. Its tools are the only
sanctioned way to touch the filesystem, the shell, remote hosts, and service logs.
The native file and terminal tools are denied by the permission engine.

## When to use this skill

- Any time you would read, list, search, or edit a file.
- Any time you would run a terminal command.
- Any time you would execute on a remote host or copy files to one.
- Any time you would inspect a service, port, process, or log.

## How to use it

| You want to | Call |
| --- | --- |
| Read a file, list a directory, view an image | ` + "`light_file`" + ` verb read / list |
| Slice a symbol or outline a file | ` + "`light_file`" + ` verb symbol / outline |
| Search inside a file or a tree | ` + "`light_file`" + ` verb locate |
| Create, overwrite, patch, or rename a file | ` + "`light_file`" + ` verb write / edit / sed / rename |
| Undo a bad edit | ` + "`light_file`" + ` verb rewrite / vault_list / vault_restore |
| Run a command | ` + "`light_bash`" + ` |
| Run a command on another machine, or copy files | ` + "`light_ssh`" + ` / ` + "`light_scp`" + ` |
| Check a service, probe a port, read logs | ` + "`light_ops`" + ` |

## Rules

- Treat the native file and terminal tools as unavailable. They stay visible in
  your tool list, but the permission engine denies them, so calling one only
  costs a turn.
- Do not route around a denial by asking the operator to run a command by hand.
  Use ` + "`light_bash`" + `.
- ` + "`light_bash`" + `, ` + "`light_ssh`" + `, ` + "`light_scp`" + `, and ` + "`light_ops`" + ` exist only when the server
  was started with the matching capability flag. If a tool is absent, say so
  instead of falling back to a native tool.
- ` + "`light_ops`" + ` is read-only. Use ` + "`light_bash`" + ` to change service state.
`
}

// antigravityPermissions is the enforcing half. Antigravity evaluates every
// sensitive action against Deny, then Ask, then Allow, so denying the native
// action families is what actually stops them.
func antigravityPermissions() string {
	return `Deny list (Settings -> Global Permissions, or project-level Permissions):
  read_file(*)
  write_file(*)
  command(*)

Allow list:
  mcp(light-tools/*)

Project Settings -> Agent Settings:
  Outside of Folder File Access Policy: Always Deny
  Terminal Execution Policy: leave the agent unable to run commands directly

This is deny plus steer, not tool hiding: Antigravity exposes no documented
switch that removes its built-in tools from the model's tool list.`
}
