# light-tools — agent setup

You are an AI coding agent. Read this file and set yourself up from it. No human
needs to translate any of it.

`light-tools` is one Go binary speaking MCP over stdio. It gives you bounded
filesystem, shell, SSH/SCP and service-log tools: reads are windowed and
paginated, repeat reads come back as a stub instead of the bytes, edits are
snapshotted and reversible, shell output is bounded and spilled instead of
flooding your context, and secrets are injected by reference rather than pasted
into arguments.

## 1. Install

```sh
npm install --global @factor-i-o/light-tools
light-tools version
```

Node 18.17+ and npm 10+.

## 2. Register

```sh
light-tools init --client claude        # prints the exact `claude mcp add` line
light-tools init --client antigravity   # merges the config, writes the skill
light-tools init --client print         # prints everything, writes nothing
```

If your harness is not one of those, write this into whatever MCP config file it
reads:

```json
{
  "mcpServers": {
    "light-tools": {
      "command": "light-tools"
    }
  }
}
```

On Windows, npm installs a `light-tools.cmd` shim. A harness that starts
processes without a shell must go through Command Prompt:

```json
{
  "mcpServers": {
    "light-tools": {
      "command": "cmd",
      "args": ["/d", "/s", "/c", "light-tools"]
    }
  }
}
```

**Stop here.** Do not change any tool permissions yet. Tell your operator to
restart the harness, and wait.

light-tools is a stdio server with no daemon: the harness spawns it per session
and it exits with the harness. Until that restart happens it is not running,
there is nothing to list, and there is nothing to switch to. The binary and the
config persist — install and `init` are one-time — but the process does not.
The only exception is `light-tools vault ui`, a foreground command the operator
keeps running while the page is open.

After the restart, list your tools. You should see five.

## 3. The tools

| Tool | Use it for |
| --- | --- |
| `light_file` | Read, list, search, slice symbols, diff, write, edit, rename, undo |
| `light_bash` | Run a local command, sync or async, bounded output, secret refs |
| `light_ssh` | Run a command on a remote host through a named profile |
| `light_scp` | Copy files to or from a remote host through a named profile |
| `light_ops` | Read-only: discover services, probe ports, read and correlate logs |

`light_file` verbs: `read`, `list`, `symbol`, `outline`, `locate`, `diff`,
`identity`, `write`, `edit`, `sed`, `rename`, `rewrite`, `vault_list`,
`vault_restore`.

`light_ops` never changes service state. Use `light_bash` for that.

## 4. Use them instead of your native tools

This is the part that matters, and it is the part you have to do yourself.

### Build the fallback guard FIRST

> **⚠️ Operator, read this.** **Blocking an agent's native tools without a
> working fallback can strand it.** If light-tools is ever unavailable — not
> installed, failed to start, a bad config — an agent that has already denied its
> own file and terminal tools cannot inspect or repair anything, including the
> config that broke it. You are the only one who can recover it at that point.

Before you deny anything, add a guard for THIS harness using whatever mechanism
it supports — a hook, a permission rule, a startup check; your call. It must
re-enable the native file and terminal tools whenever light-tools is not
available, so a missing or crashed server can never leave you with no tools at
all. Show the operator what you wrote and how you tested it.

**If you cannot build a working fallback, leave the native tools enabled and say
why.** A degraded setup beats a stranded one.

### Then prefer light-tools

- Read, list, search or edit a file → `light_file`.
- Run a terminal command → `light_bash`.
- Touch a remote host → `light_ssh` or `light_scp`.
- Inspect a service, port, process or log → `light_ops`.

Your harness's own file and terminal tools usually stay visible in your tool
list. Treat them as unavailable anyway. If your harness has a permission engine,
deny them there too — `light-tools init --client antigravity` prints the exact
block to paste.

Do not route around a tool by asking the operator to run something by hand.

Pass a stable `context_epoch` on reads within one task. It is what enables
dedup: a repeat read of unchanged content returns a `[dedup]` stub instead of
the file body. Without it, dedup is disabled.

## 5. If a tool is missing

It was withheld deliberately, either by a `--disable-tool` launch flag or by the
operator's Settings view in `light-tools vault ui`. The two sources only ever add
up — the UI cannot re-enable what launch arguments withhold.

Say plainly that it was withheld. Do not emulate it with a native tool, and do
not ask the operator to run the work by hand.

## 6. Optional

`LIGHT_TERSE_OUTPUT=1` on the server process allows deterministic terse text for
large successful JSON results. Off by default; never applies to errors, images,
or any result that would not get strictly smaller.

State lives in private XDG directories, created on first run.