# light-tools

A standalone, single-operator MCP server that ports the useful local behavior of
Light's file, shell, SSH/SCP, and operations tools without its fleet control
plane. It is one Go binary, speaks MCP over stdio, needs no database or daemon,
and registers only `light_file` by default.

## Install in three commands

Release binaries include the tagged CGo tree-sitter runtime and Go, JavaScript,
and Python grammars.

```sh
curl -fsSL https://raw.githubusercontent.com/icediceice/light-tools/main/install.sh | sh
light-tools init
claude mcp add light-tools -- light-tools
```

`init` creates private XDG state directories and prints the exact MCP command.
It is optional because the server initializes those directories on first run.
For a client other than Claude Code, pass `--client` — see
[MCP clients](#mcp-clients).

Build from source with Go 1.23+ and a C toolchain:

```sh
go install -tags treesitter github.com/icediceice/light-tools/cmd/light-tools@latest
```

## MCP clients

`light-tools init --client <name>` writes the configuration each client expects.
`--dry-run` prints it instead, and `--client print` always prints and never
writes. Capability flags passed to `init` are recorded as the server's launch
arguments, so the client starts the same surface you asked for.

| Client | What `init` does |
| --- | --- |
| `claude` (default) | Prints the exact `claude mcp add` line |
| `antigravity` | Merges `mcpServers["light-tools"]` into Antigravity's config and writes the suppression skill |
| `print` | Prints the Antigravity config, skill, and permission block; writes nothing |

### Google Antigravity

```sh
light-tools init --client antigravity --enable-shell --enable-ops
```

This writes the global pair shared by Antigravity CLI, IDE, and 2.0:

- `~/.gemini/config/mcp_config.json` — the `mcpServers` entry, merged in place.
  Foreign servers and unrelated top-level keys are preserved, a malformed
  existing file is refused rather than overwritten, and only the documented
  properties (`command`, `args`, `env`, `cwd`, `disabled`, `disabledTools`) are
  emitted. The retired `httpUrl` and the top-level `timeout` are never written,
  and Antigravity does not accept comments in this file.
- `~/.gemini/config/skills/light-tools/SKILL.md` — a skill telling the agent to
  route every file, shell, remote, and log action through the light-tools tools.

Pass `--workspace <dir>` to write the workspace pair instead:
`<dir>/.agents/mcp_config.json` and `<dir>/.agents/skills/light-tools/SKILL.md`.

Use `--disable-tool <name>` (repeatable) to withhold one of light-tools' own
tools from the model through `disabledTools`. Capability flags are the blunter
control: a tool the server never registers cannot be called at all.

#### Suppressing the native tools

Antigravity exposes **no documented switch that hides its built-in tools from
the model**. What it does expose is a permission engine, so the honest mechanism
is deny plus steer, and it needs both halves:

1. **Deny** the native action families in Settings → Global Permissions (or the
   project-level Permissions):

   ```
   Deny:   read_file(*)    write_file(*)    command(*)
   Allow:  mcp(light-tools/*)
   ```

   In Project Settings → Agent Settings, also set *Outside of Folder File Access
   Policy* to **Always Deny**.

2. **Steer** with the skill `init` wrote, which tells the agent to treat the
   native file and terminal tools as unavailable.

The native tools stay visible in the model's tool list. Denying them makes the
calls fail; the skill is what stops the agent from spending turns on them.

## Capability profiles

The default invocation exposes the bounded file tool only. Every broader
capability is explicit:

```sh
light-tools
light-tools --enable-shell
light-tools --enable-remote
light-tools --enable-ops
light-tools --enable-shell --enable-remote --enable-ops
```

| Area | Retained for one operator | Deliberately excluded |
| --- | --- | --- |
| Files | Reads, batches, cursors, symbols, images, locate, unified diff/patch preview, guarded transactional edits, snapshots | EDCR gates, plan attribution, Git checkpoints |
| Shell | Sync and local async tasks, cancellation, bounded output, recoverable spills, secret refs | Host/node dispatch, fleet queues |
| Remote | SSH/SCP profiles and overrides, agent inheritance, key/cert refs, timeout-only retry | Host registry, cross-host fan-out |
| Ops | Local systemd/PM2/Docker discovery, probes, file logs, search/correlation/investigation, local async scans | Cross-host joins, shared telemetry/database state, service mutation |
| Runtime | Direct stdio MCP, deterministic schemas, `E_*` caret diagnostics | Hub, WebSocket routing, RBAC, board/Discord, deploy orchestration |

## `light_file`

Supported verbs are `read`, `list`, `symbol`, `outline`, `locate`,
`diff`, `identity`, `write`, `edit`, `sed`, `rename`, `rewrite`,
`vault_list`, and `vault_restore`.

Reads are numbered and report total lines, bytes, estimated tokens, SHA-256,
and continuation state. A bare file read returns an outline; pass
`offset`/`limit` for bytes. Mixed `items` (or `reads`) share one 128 KiB
response budget and return an exact opaque `[CONTINUE ...]` cursor. Supplying
an opaque `context_epoch` enables content-hash deduplication for that client
context; it is disabled by default. A single image read emits a real MCP image
block up to 9 MiB, while image items in a batch become text descriptions.

Example batch:

```json
{
  "verb": "read",
  "items": [
    {"path": "/work/app/main.go", "offset": 0, "limit": 80},
    {"path": "/work/app/server.go", "name": "Serve"}
  ]
}
```

Mutations are confined to configured roots and use per-path locking, optional
`expected_sha` compare-and-swap, mode-preserving atomic replacement, identity
rechecks, file and directory fsync, and a three-version pre-mutation snapshot
ring.

JSON edits support `start_line`, optional `end_line`, stale
`start_guard`/`end_guard` relocation, bounded ±3 closer-only auto-snap,
`allow_unbalanced`, and `spans`. Same-path spans are applied bottom-up in
one commit and overlaps are refused. Same-path payload sed operations cascade
in order; mixing edit and sed for one path is refused. Sed supports literal or
regex matching, ambiguity refusal, `all`, exact `count`, CRLF preservation,
and `dry_run`.

`diff` compares `path` with `target` (aliases `a`/`b`) and emits
context-bounded unified hunks. Supplying `patch` or `patch_path` previews a
unified patch in memory; `fuzz` bounds line displacement. It never writes.

### Sealed payloads and resume

Large and multi-file mutations use payload format 1. Headers are bare
`@key value` lines. Bodies begin after `@content`, `@new_string`,
`@find`, `@replace`, or `@spans`, and end only on a line exactly equal
to `<<LF-END>>`. Every body has its own seal. Use `@until TOKEN` before a
body when its content contains the default seal.

```text
@file /work/app/a.txt
@verb sed
@find
old
<<LF-END>>
@replace
new
<<LF-END>>
@file /work/app/b.txt
@verb edit
@start_line 12
@new_string
replacement
<<LF-END>>
```

An unterminated body writes nothing and returns
`{status:"partial", stage, got_lines}`. Resume it with the opaque local stage:

```text
@stage STAGE_ID
@from_line GOT_LINES_PLUS_ONE
remaining body
<<LF-END>>
```

Stages are process-local, bounded, and expire. Parser failures include an
`E_*` diagnostic with line, column, byte offset, source line, and caret.

### Snapshot recovery and rewrite

Every successful write/edit/sed captures the preimage in a separate snapshot
root. `vault_list` lists up to three versions for a path and
`vault_restore` restores one with normal race guards; `force` bypasses the
current-file hash check. `rewrite` starts from the newest pre-mutation
snapshot and applies a corrected edit in one commit, without creating another
snapshot of the mistaken intermediate state.

## `light_bash` (opt-in)

Commands run under the requested root-confined `cwd`, a minimal inherited
environment, and a timeout that terminates the whole process group. Results
preserve `stdout`, `stderr`, and `exit_code`.

Output modes are `auto`, `head`, `tail`, `grep`, and `read_block`.
Large complete output is compressed behind a random opaque `spill_id`; use
`read_block` plus `line_range` to recover exact ranges.

Local async flow:

```json
{"command":"go test ./...","cwd":"/work/app","async":true}
{"verb":"status","task_id":"TASK_ID"}
{"verb":"collect","task_id":"TASK_ID"}
{"verb":"cancel","task_id":"TASK_ID"}
```

Task and spill IDs are random, process-local, capacity-bounded, and expire.
The full output is captured before preview filtering.

## Secret vault

Secret writes are CLI-only and values are read from stdin, never argv:

```sh
printf '%s' "$TOKEN" | light-tools vault set api-token
light-tools vault list
light-tools vault rm api-token
```

The AES-GCM vault stores only encrypted values in `vault.enc`; its local
32-byte key and ciphertext are mode 0600. The threat boundary is model context,
not a compromised user account: a process running as the same OS user can read
the key.

MCP calls can resolve names only through `env_refs`, `file_refs`,
`key_ref`, or `cert_ref`. Values are not returned. File, key, and
certificate refs are materialized as mode-0600 temporary files and
best-effort overwritten and removed after use. Output scrubbing is
best-effort.

There is currently no vault web UI. This intentionally avoids adding a
network-facing secret surface to the base server.

## `light_ssh` and `light_scp` (opt-in)

Remote calls support named TOML profiles and per-call `remote`, `key`,
`key_ref`, `cert_ref`, `port`, `proxy_jump`, and `timeout_ms`
overrides. They use strict host-key checking, noninteractive batch mode,
inherit `SSH_AUTH_SOCK`, and retry exactly once only after timeout.

```toml
[remote.production]
host = "example.internal"
user = "deploy"
port = 22
proxy_jump = "bastion.internal"
key_path = "/home/me/.ssh/id_ed25519"
```

```json
{"profile":"production","command":"uname -a","key_ref":"deploy-key"}
{"profile":"production","src":"/work/app/release.tgz","dst":"deploy@example.internal:/tmp/release.tgz"}
```

SCP requires exactly one remote endpoint, confines the local endpoint to an
allowed root, distinguishes SSH `-p` from SCP `-P`, and reports transferred
bytes for regular local files.

## `light_ops` (opt-in, read-only)

Local service IDs are source-qualified, such as `systemd:api`,
`pm2:worker`, and `docker:web`; ambiguous bare names return candidates.
Supported verbs are:

- `list_services`, `probe_service`, `probe_port`, `probe_process`,
  `probe_file`
- `log_window`, `log_trace`, `log_search`, `log_grep`, `log_errors`,
  `log_since`, `log_correlate`, `log_investigate`
- `status`, `collect`, and `cancel` for local async scans

Log filters support regex with fixed-string fallback, context, `since`,
`since_ts`, `include`, `exclude`, normal/drill caps, pool scans,
timestamp-ordered correlation, and identifier tracing. An explicit path is a
file log; correlation also accepts `file:/absolute/path` service IDs.
`light_ops` has no start/stop/restart verb.

## Configuration and state

No config file is required. The default allowed root is the server process
working directory. Optional overrides live at
`$XDG_CONFIG_HOME/light-tools/config.toml`:

```toml
allowed_roots = ["/work/project"]
```

Configuration, encrypted secrets, snapshots, and runtime spills have separate
XDG roots. Parent directories are mode 0700 and private files are mode 0600
where Unix permissions exist.

## Development and release

```sh
go test ./...
go test -race ./...
go test -tags treesitter ./...
go build -tags treesitter ./...
```

Standard `go mod vendor` omits parent-relative C source trees used by the
tree-sitter bindings. After refreshing dependencies, run:

```sh
go mod vendor
sh scripts/vendor-tree-sitter.sh
```

CI builds and tests natively on Linux amd64/arm64, macOS amd64/arm64, and
Windows amd64, then runs an MCP Inspector `tools/list` smoke against an
installed artifact. Releases are also built on native runners.

See [SECURITY.md](SECURITY.md) for the exact security claim and
[PORTING.md](PORTING.md) for stable edge-case semantics.
