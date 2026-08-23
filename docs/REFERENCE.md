# light-tools reference

Full per-tool semantics. This file is repository-only: it is not shipped in the
npm package. For setup, see [AGENT-SETUP.md](../AGENT-SETUP.md); for the
security claim, see [SECURITY.md](../SECURITY.md).

## Scope

| Area | Retained for one operator | Deliberately excluded |
| --- | --- | --- |
| Files | Reads, batches, cursors, symbols, images, locate, unified diff/patch preview, guarded transactional edits, snapshots | EDCR gates, plan attribution, Git checkpoints |
| Shell | Sync and local async tasks, cancellation, bounded output, recoverable spills, secret refs | Host/node dispatch, fleet queues |
| Remote | SSH/SCP profiles and overrides, shared minimal child environment, key/cert refs, SSH at-most-once execution, one timeout overwrite retry for SCP | Host registry, cross-host fan-out, credentialed live-host testing |
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

## Opt-in terse output

Set `LIGHT_TERSE_OUTPUT=1` on the MCP server process to allow deterministic
terse text for large successful JSON tool results. The default is off; unset,
empty, and every value other than exactly `1` preserve the handler's raw JSON
text.

The formatter touches only `content[0]` when it is text and the result is not
an error. Images, later content blocks, plain text, malformed JSON, unsupported
or empty shapes, and any result that does not get strictly smaller in both bytes
and the internal punctuation-aware token estimate pass through byte-for-byte.
The formatter parses numbers with `json.Number`, sorts object keys, renders
supported scalar, nested, array, and homogeneous-row shapes, decodes its own
output in production, and compares the reconstructed value with the original
before emitting it. This preserves strings such as `"8080"`, multiline log
content, null/empty values, and exact numeric spelling.

The terse swap estimate is internal and deliberately separate from
`light_file read`'s public `tokens` field; changing the latter would change a
tool contract. Release tests exercise raw and terse modes through real MCP
stdio for all five registered tools, including image passthrough and SSH/SCP
pre-execution refusals.

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
to the seal `<<LF-END>`+`>`. Every body has its own seal. Use `@until TOKEN`
before a body when its content contains the default seal — this very file is
written that way.

An unterminated body writes nothing and returns
`{status:"partial", stage, got_lines}`. Resume it by sending `@stage STAGE_ID`
and `@from_line GOT_LINES_PLUS_ONE` followed by the remaining body.

Stages are process-local, bounded, and expire. Parser failures include an
`E_*` diagnostic with line, column, byte offset, source line, and caret.

### Snapshot recovery and rewrite

Every successful write/edit/sed captures the preimage in a separate snapshot
root. `vault_list` lists up to three versions for a path and
`vault_restore` restores one with normal race guards; `force` bypasses the
current-file hash check. `rewrite` starts from the newest pre-mutation
snapshot and applies a corrected edit in one commit, without creating another
snapshot of the mistaken intermediate state.

## `light_bash`

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
directory. `light_bash` remains arbitrary same-user code and can access files
the account itself can read; output scrubbing is best-effort.

## `light_ssh` and `light_scp`

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

## `light_ops` (read-only)

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

Configuration, encrypted secrets, snapshots, runtime spills, and local-only
telemetry have separate XDG roots. Parent directories are mode 0700 and private
files are mode 0600 where Unix permissions exist.

## Settings persistence

Tool withholding is the UNION of two sources, and both only ever add:

1. Launch arguments: repeatable `--disable-tool <name>`.
2. UI-owned markers: one zero-byte mode-0600 file per withheld tool, named
   exactly for the tool, under `$XDG_CONFIG_HOME/light-tools/disabled-tools/`.

At startup the server loads the markers, refuses an unrecognized marker name
exactly as it refuses an unknown flag, and registers every tool not withheld by
either source. A flag-withheld tool therefore cannot be re-enabled through the
UI: removing its marker changes nothing while the flag remains. The machine
never writes `config.toml`, and no `disabled_tools` key exists there. Markers
take effect at the next MCP start, never mid-process.

Vault UI endpoints (behind pairing plus password, Host-pinned, same as the
vault itself):

- `GET /api/settings` → `{"tools":[{"name","disabled"}...],"ui_disabled":[...],`
  `"launch_withholding_observable":false}`. `disabled` reflects ONLY the
  marker state.
- `POST /api/settings/tools` with exactly `{"tool":"<name>","disabled":<bool>}`
  mutates ONE marker. Creating an existing marker and removing an absent one
  are both success, so two processes (MCP server and UI) commute. Unknown
  fields are refused; a whole replacement set is not expressible.

## Local-only savings telemetry

`$XDG_DATA_HOME/light-tools-telemetry/` holds cumulative per-session snapshots
named `session-v1-<128-bit-session>-<generation>.json`, written by a background
writer through temp+fsync+rename onto a name that did not exist, after which the
prior generation is removed. Each generation carries the session's FULL
cumulative totals, so a reader takes the highest generation per session — an
interrupted flush can never double-count, and a session id is random 128-bit so
PIDs being reused or two processes sharing the directory cannot collide.
Reading (`GET /api/telemetry`, same auth gate) never mutates the store and
never races a live writer. A fresh root carrying only `SCHEMA` and `.lock`
reads as a clean zero. Snapshot files matching the name grammar but failing to
decode are skipped and reported as health warnings.

Retention and pruning belong to the writer: a session whose newest snapshot is
older than 30 days is removed, and at most 50 sessions are kept (oldest evicted
first; the live session is never pruned). Snapshots flush periodically, so the
UI's totals are a persisted lower bound on true activity.

Exact metric definitions:

- **Terse tokens** — recorded only when terse formatting actually swaps a
  result (`LIGHT_TERSE_OUTPUT=1`, non-error, strictly smaller): the internal
  punctuation-aware token estimate of the original `content[0]` minus the
  estimate of the terse replacement.
- **Read-dedup bytes** — recorded only when the dedup ledger elides a repeated
  read: the bounded response payload the caller would have received minus the
  dedup stub, measured on the response, never the source file. For a batch item
  the baseline is the rendered item section; in the rare oversized-single-line
  case the baseline is the truncated content, so the credit is conservative.
- **Writing bytes** — measured once per COMMIT for `write`, `edit`, `sed`, and
  `rewrite`: `max(0, len(postimage) - payload bytes that commit actually
  carried)` — the counterfactual bytes a full rewrite would have had to send to
  reach the identical on-disk state; the UI labels it "vs. sending a full
  rewrite". A grouped same-path batch sums its members' payloads against its
  single commit. `rename` commits no content and `vault_restore` restores vault
  content, so neither records.
- **Call counts** — one per `tools/call` dispatched to a registered tool.
  Counts are usage, not savings, and the UI renders them in a separate section.

Opt out entirely with `DO_NOT_TRACK=1` or a non-empty `LIGHT_NO_TELEMETRY`; the
check runs once at construction, and with it set no telemetry code records or
writes anything. Recording is in-memory and non-blocking on the tool path; a
recorder panic or a stalled filesystem cannot alter or delay a tool result.

## Symbol extraction

`light_file` uses a deterministic extension registry. Grammar-backed release
builds support:

| Language | Extensions |
| --- | --- |
| Go | `.go` |
| JavaScript | `.js`, `.jsx`, `.mjs`, `.cjs` |
| TypeScript / TSX | `.ts` / `.tsx` (dedicated TSX grammar) |
| Python / Java / Rust | `.py`, `.java`, `.rs` |
| C / C++ / C# | `.c`, `.h` / `.cpp`, `.cc`, `.cxx`, `.hpp` / `.cs` |
| Ruby / PHP | `.rb`, `.php` |
| Bash / Lua | `.sh`, `.bash`, `.lua` |
| Scala / Kotlin / Dart | `.scala`, `.kt`, `.kts`, `.dart` |
| HTML | `.html` |

CSS (`.css`), Markdown (`.md`, `.markdown`), YAML (`.yaml`,
`.yml`), and TOML (`.toml`) use pure-Go deterministic extractors and are
available on every release platform. Windows ARM64 intentionally remains a
CGo-free build: these four structured-text lanes work there, while
grammar-backed files return the documented no-symbol fallback.

Symbols have a closed kind vocabulary, exact line and byte ranges,
UTF-8-safe signatures/comments, parent attribution where the grammar exposes
it, and deterministic source ordering. Parsing rejects lines above 8000 bytes,
uses tree-sitter's native timeout, and has a single-flight hard circuit so a
pathological CGo parse cannot accumulate workers. Unsupported `.htm` files
use the ordinary fixed-size outline.

## Development

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

Release process: [RELEASING.md](../RELEASING.md). Stable edge-case semantics:
[PORTING.md](PORTING.md).