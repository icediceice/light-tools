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

Windows PowerShell installs the same signed-by-checksum release assets:

```powershell
Invoke-WebRequest https://raw.githubusercontent.com/icediceice/light-tools/main/install.ps1 -OutFile install.ps1
./install.ps1
light-tools init
```

Pass `-Version 1.2.3` or `-Destination C:\Tools` to pin or relocate the
PowerShell install. The POSIX equivalents are `LIGHT_TOOLS_VERSION` and
`LIGHT_TOOLS_INSTALL_DIR`. Both installers refuse an asset that has no exact
entry in `checksums.txt`.

Release testing and trusted mirrors may override the candidate asset directory with
`LIGHT_TOOLS_BASE_URL` or PowerShell's `-BaseUrl`. The override must name the
directory containing the six archives and `checksums.txt`; checksum verification
remains mandatory. Leaving it unset preserves the GitHub Releases URL.

Published binaries cover:

| OS | amd64 | arm64 | Symbol extraction |
| --- | --- | --- | --- |
| Linux | native | native | tree-sitter |
| macOS | native | native | tree-sitter |
| Windows | native | native | tree-sitter on amd64; graceful no-symbol fallback on arm64 |

Windows ARM64 is deliberately built without CGo or the `treesitter` tag. All
five MCP tools remain available; only `light_file symbol`/outline extraction
degrades to its documented no-symbol response.

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

## Runtime argument contract

Every registered tool carries the same JSON schema returned by `tools/list`
into the invocation adapter. Validation is recursive: unknown top-level and
nested fields, non-nullable `null`, wrong containers, missing nested required
fields, enum misses, and range violations return `E_SCHEMA` before a handler
runs. Schema-valid JSON is passed through byte-for-byte, including integers
above 2^53.

Coercion is conservative and schema-directed: strings may become integers,
numbers, or booleans only when they parse exactly; numbers and booleans may
become strings. Arrays and objects are never invented from scalars. Coerced
numbers remain `json.Number` rather than passing through `float64`. Handler
errors and schema errors retain the normal MCP `isError` tool-result envelope.

The five request structs and schemas are checked field-for-field in tests, so a
tool cannot be advertised without its documented arguments reaching the
registered handler.

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

A single-path `offset`/`limit` read is bounded the same way a batch is: at most
5000 lines and one 128 KiB response budget, whichever binds first. A supplied
`limit` above that is clamped rather than honoured, with `continued` and
`next_offset` set so the caller can page. To page safely, echo the previous
response's `sha256` back as `expected_sha` — if the file changed between pages
the read is refused instead of silently duplicating or dropping lines. A single
logical line larger than the budget is still returned as progress and its full
page is written to the shared spill store; recover it verbatim with
`light_bash` `output_mode:read_block` and the returned `spill_id`. A file above
256 MiB is refused outright, since the file is read whole in order to slice it.

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

A narrow filename-wildcard guard catches shell arguments containing an active
`*` or `?`. The first request returns `dry_run:true` and
`wildcard_preview:true` without starting a process. Retrying the identical
full request once consumes its receipt and may execute; changing the command,
`cwd`, timeout, async mode, or secret-reference names previews again. Explicit
lists such as `rm a.tmp b.tmp` execute immediately and `light_file`
multi-file payloads are unrelated to this guard.

Receipts are process-local, one-shot, capped at 64, and expire after ten
minutes; expiry or eviction safely causes another preview. POSIX quoted or
backslash-escaped wildcards are literals and bypass. PowerShell provider
cmdlets still expand quoted wildcards, so Windows guards those patterns too.
Obvious URL query strings and shell assignments are excluded.

This is lexical accident protection, not a destructive-command parser or an OS
sandbox. It cannot see a wildcard introduced later through a variable, command
substitution, script, or a program's own pattern language (for example,
`find -name '*.tmp' -delete`).

A command killed by its timeout says so: the result carries `timed_out: true`,
the `timeout_ms` that applied, an `error` naming the timeout, and `exit_code`
of `-1` — and it keeps whatever partial `stdout`/`stderr` the command produced
before it was killed, which is usually the only evidence of where it hung. An
ordinary non-zero exit is never relabelled as a timeout, and cancellation is
reported as cancellation rather than as a deadline.

Output modes are `auto`, `head`, `tail`, `grep`, and `read_block`.
Large complete output is compressed behind a random opaque `spill_id`; use
`read_block` plus `line_range` to recover exact ranges. `light_file` shares
this same spill store, so an oversized read's `spill_id` is recoverable here.

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

Values can be written from stdin, so they never need to appear in process
arguments:

```sh
printf '%s' "$TOKEN" | light-tools vault set api-token
light-tools vault list
light-tools vault rm api-token
```

For a beginner-friendly local UI, run:

```sh
light-tools vault ui
```

The command prints a bare loopback URL and a single-use pairing code, then opens
the system browser when possible. Paste the code into the page, and on first use
choose a password of at least eight characters. The pairing code expires after
five minutes; the browser session survives refresh in that tab but is removed
when the tab is closed or the UI is locked. The foreground command must keep
running while the page is in use.

The UI password cannot be changed in the browser and cannot be recovered. If you
forget it, run `light-tools vault ui-reset`, then run `light-tools vault ui` and
choose a new password. Reset removes only `ui.json`; `master.key` and `vault.enc`
are untouched, so no saved secret is lost.

The UI supports explicit empty groups, group assignment, rename, and deletion.
Deleting a group keeps its secrets and unassigns them; renaming refuses to merge
into an existing group. Secret names, groups, and update times can be read by the
UI, but values are write-only: a saved value is never returned by an HTTP or MCP
response.

Textual SSH key and certificate material can be added through the CLI or the
browser:

```bash
# Safer when browser extensions or page access are not trusted.
light-tools vault set deploy-key < ~/.ssh/id_ed25519
```

The paths do not normalize text identically: the CLI removes one trailing LF and
then one trailing CR, while browser import keeps a terminal newline (browser
UTF-8 decoding ignores a leading BOM). Use browser import when preserving a
key's terminating newline matters.

In the browser, use **Import a key or certificate file**, confirm the displayed
file name and byte count, then press **Save value**. Selection reads the file
locally and does not send it to the server before Save. The picker accepts files
up to 1 MiB that decode as strict UTF-8 text; common PEM/OpenSSH private keys,
OpenSSH public keys, and textual certificates fit this contract. Binary DER,
PKCS#12, and PuTTY PPK containers are not supported. The picker does not prove
that the text is the right key type or unlock passphrase-protected keys. Remote
commands use noninteractive batch mode, so use an SSH agent or a dedicated
unencrypted deployment key when prompting would otherwise be required. Refer to
the saved name later with `key_ref` or `cert_ref`.

The AES-GCM vault keeps the existing `vault.enc` format. Its random local
32-byte `master.key`, ciphertext, and UI-password verifier are mode 0600 where
Unix permissions exist. The password protects entry to the loopback UI; it does
not wrap the encryption key or protect against a process running as the same OS
user. Browser extensions with localhost/page access may inspect a value while
you type it, so `vault set` is the safer entry path on an untrusted browser.

Mutations hold a local-filesystem lock across the complete load/change/save
transaction. Network filesystems are not supported for the secrets state root.
If `master.key` is missing while `vault.enc` remains, light-tools refuses to
mint a replacement and asks you to restore the key.

MCP calls resolve values only through `env_refs`, `file_refs`, `key_ref`,
or `cert_ref`. The file, SCP, and operations tools deny the secrets, snapshot,
and spill state roots even when an allowed root contains the whole home
directory. Opt-in `light_bash` remains arbitrary same-user code and can access
files the account itself can read; output scrubbing is best-effort.

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

### Which log paths are confined

`light_ops` reads two different kinds of path, and they are governed
differently on purpose:

- **Caller-supplied paths** — a `path` argument, or a `file:/absolute/path`
  service ID — are **confined** to `allowed_roots` plus `log_roots`. A path
  outside both is refused. `probe_file` is confined the same way and refuses
  rather than returning stat metadata for an arbitrary path.
- **Registry-discovered service logs** — whatever `journalctl`, `docker` or
  `pm2` reports for a discovered service — are **not** confined. Those logs
  live wherever the service manager puts them, and reading them is what the
  tool is for. Confining them would break `light_ops` for its main purpose.

## Configuration and state

No config file is required. The default allowed root is the server process
working directory. Optional overrides live at
`$XDG_CONFIG_HOME/light-tools/config.toml`:

```toml
allowed_roots = ["/work/project"]
log_roots     = ["/var/log", "/srv/myapp/logs"]
```

`allowed_roots` is the filesystem boundary for `light_file` and `light_bash`.
`log_roots` additionally widens caller-supplied `light_ops` log paths, and
nothing else.

### Log roots and the `.env` front door

`log_roots` can also be set without editing `config.toml`. Precedence, highest
first:

1. `LIGHT_TOOLS_LOG_ROOTS` in the process environment
2. `LIGHT_TOOLS_LOG_ROOTS` in `$XDG_CONFIG_HOME/light-tools/.env`
3. `log_roots` in `config.toml`
4. built-in defaults: `/var/log`, `~/.local/log`, `~/.pm2/logs`

Entries are separated by the OS path list separator (`:` on Linux and macOS)
and a leading `~` is expanded. See `.env.example` in the repository.

The `.env` is read **only** from the XDG config directory, never from the
process working directory. That tree is writable by the agent the server is
serving, so a repo-local file must not be able to widen the boundary.

Built-in defaults are optional: any that do not exist on the machine are
dropped. Roots you configure yourself are not — a missing one fails at startup
rather than silently narrowing what is readable.

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
