# light-tools reference

Per-tool semantics. Repository-only — not shipped in the npm package. Setup is
in [AGENT-SETUP.md](../AGENT-SETUP.md); what is and is not protected is in
[SECURITY.md](../SECURITY.md).

## Argument contract

Every registered tool carries the schema returned by `tools/list` into the
invocation adapter. Validation is recursive: unknown top-level or nested fields,
non-nullable `null`, wrong containers, missing nested required fields, enum
misses and range violations return `E_SCHEMA` before a handler runs. Valid JSON
passes through byte-for-byte, including integers above 2^53.

Coercion is conservative and schema-directed: strings become numbers or booleans
only when they parse exactly; numbers and booleans may become strings. Arrays
and objects are never invented from scalars.

## Terse output

`LIGHT_TERSE_OUTPUT=1` allows deterministic terse text for large successful JSON
results. Off by default; every value other than exactly `1` preserves raw JSON.
It compacts EVERY text block of a successful result, not just the first, and
only when the result gets strictly smaller in bytes and estimated tokens.
Images, plain text, malformed JSON and errors are untouched.

## `light_file`

Verbs: `read`, `list`, `symbol`, `outline`, `locate`, `diff`, `identity`,
`write`, `edit`, `sed`, `rename`, `rewrite`, `vault_list`, `vault_restore`.

Reads are windowed and paginated under a shared budget, returning an exact
`[CONTINUE]` cursor rather than truncating. A repeat read of the same window of
unchanged content returns a `[dedup]` stub naming the path, content hash and
line range instead of the bytes. Dedup is keyed on the window, not the file, so
a different offset or limit is always served in full.

Dedup needs no client cooperation: the server mints one epoch per process at
startup and uses it whenever a request omits `context_epoch`, which scopes the
ledger to a single connection because the stdio loop serves a single client.
Send `context_epoch` to choose that scope yourself, `force: true` to re-serve
bytes the caller no longer holds, and set `LIGHT_NO_READ_DEDUP` to a non-empty
value to switch dedup off entirely — that overrides a client-supplied epoch too.

Mutations reach any path by default (see [Confinement](#confinement)) and use
per-path locking, optional `expected_sha` compare-and-swap, mode-preserving
atomic replacement, identity rechecks, file and directory fsync, and a
three-version pre-mutation snapshot ring.

Edits take `start_line`, optional `end_line`, stale `start_guard`/`end_guard`
relocation, bounded ±3 closer-only auto-snap, `allow_unbalanced` and `spans`.
Same-path spans apply bottom-up in one commit; overlaps are refused. Sed
supports literal or regex matching, ambiguity refusal, `all`, exact `count`,
CRLF preservation and `dry_run`. Mixing edit and sed for one path is refused.

`diff` emits context-bounded unified hunks and never writes.

### Sealed payloads

Large and multi-file mutations use payload format 1. Headers are bare
`@key value` lines. Bodies begin after `@content`, `@new_string`, `@find`,
`@replace` or `@spans` and end only on a line exactly equal to the seal. Use
`@until TOKEN` when a body contains the default seal. An unterminated body
writes nothing and returns `{status:"partial", stage, got_lines}`; resume with
`@stage` and `@from_line`.

### Snapshots

Every successful write/edit/sed captures the preimage in a separate snapshot
root. `vault_list` lists up to three versions for a path; `vault_restore`
restores one with race guards (`force` bypasses the hash check). `rewrite`
starts from the newest pre-mutation snapshot and applies a corrected edit in one
commit.

## `light_bash`

Commands run under a `cwd` resolved by the same confinement policy as
`light_file` (unconfined by default), a minimal inherited environment, and a
timeout that terminates the whole process group. The policy governs where `cwd`
may point; it does not bound what a command writes once running, which no path
check could do. Results preserve `stdout`,
`stderr` and `exit_code`.

A timed-out command carries `timed_out: true`, the `timeout_ms` that applied, an
`error` naming the timeout, `exit_code` `-1`, and whatever partial output it
produced before the kill — usually the only evidence of where it hung. An
ordinary non-zero exit is never relabelled as a timeout.

Output modes: `auto`, `head`, `tail`, `grep`, `read_block`. Oversized combined
output is still stored whole behind an opaque `source_spill_id`; recover exact
ranges with `read_block` plus `line_range`. `light_file` shares the same spill
store.

### Output compaction

`stdout` and `stderr` are compacted INDEPENDENTLY. Lines that differ only by a
varying token — a counter, a pid, a uuid, a duration — collapse into one
template row carrying the values that actually changed, so a climbing restart
counter stays visible instead of hiding inside a repeated line. Every distinct
line kind is rendered, INCLUDING one that occurs exactly once: a verdict
(`BUILD FAILED`, `panic:`, `exit status 1`) is by nature a singleton, and it is
the line you came for.

Compaction runs AFTER your own `head`/`tail`/`grep` filter, so a rendered range
and the bytes behind it address the same text.

When a stream's view is not the bytes the command produced, that stream gets its
OWN spill and reports:

| Key | Meaning |
| --- | --- |
| `stdout_spill_id` / `stderr_spill_id` | recovers THAT stream, numbered from its own line 1 |
| `stdout_recover` / `stderr_recover` | the ready-made `read_block` call for it |
| `truncated` | at least one stream was compacted |
| `stdout_compaction_skipped` | the view would have elided, but no spill could back it, so the EXACT output was returned instead |

Elision implies recovery, and the failure mode is fail-open. The spill store
holds 64 live records; when it is full the exact output comes back untouched
with `compaction_skipped` — never an outline whose pointer does not resolve, and
never a lost `exit_code`, because the command has already run by then.

Small output passes through untouched rather than spending a spill record on
something already legible.

`LIGHT_NO_COMPACT=1` is a compatibility hatch, not just an off switch: it
restores the pre-compaction result shape exactly. Output within the size limit
comes back as the exact bytes with none of the keys above; oversized output
comes back the way it did before compaction existed — the combined aggregate
under `spill_id` (not `source_spill_id`), both streams cut to their last 80
lines, and `truncated: true`. The one deliberate difference is that a failed
spill still returns the command's output and `exit_code` rather than an error,
because the command has already run.

Recovery for `light_ssh` and `light_ops` runs through `light_bash`'s
`read_block`, so withholding `light_bash` (via `--disable-tool` or the vault UI)
also turns compaction off for those two tools: they return exact output rather
than an outline pointing at a tool that is not registered.

Async:

```json
{"command":"go test ./...","cwd":"/work/app","async":true}
{"verb":"status","task_id":"TASK_ID"}
{"verb":"collect","task_id":"TASK_ID"}
{"verb":"cancel","task_id":"TASK_ID"}
```

The mutation guard models `rm`, `unlink`, single-source `mv`, `sed -i`,
`gofmt -w`, and `go fmt`. It enumerates both explicit operands such as
`rm a.tmp b.tmp` and unquoted filename globs such as `rm *.tmp`. A
protectable surface is captured before execution and returns a
`vault_restore` handle. Explicit non-glob captures are bounded to 64 MiB of
regular-file preimages, measured while the bytes are read; an explicit surface
that is unprotectable or over that ceiling still runs and reports
`protection:"unbacked"` with the reason. Only an unprotectable unquoted glob
is refused and shown with a confirmation digest. Quoted patterns, pipelines,
variables, command substitutions, and program-specific patterns such as
`find -name '*.tmp' -delete` stay outside this guard.

## Secret vault

```sh
printf '%s' "$TOKEN" | light-tools vault set api-token
light-tools vault list
light-tools vault rm api-token
light-tools vault ui
```

`vault ui` prints a loopback URL and a single-use pairing code that expires in
five minutes; first use sets a password of at least eight characters. The
foreground command must keep running while the page is open.

Names, groups and update times are readable; values are write-only and never
returned by an HTTP or MCP response. MCP calls resolve values only through
`env_refs`, `file_refs`, `key_ref` or `cert_ref`.

CLI and browser import do not normalise text identically: the CLI strips one
trailing LF then one trailing CR, while browser import keeps a terminal newline.
Use browser import when a key's terminating newline matters. The picker accepts
files up to 1 MiB that decode as strict UTF-8 — PEM/OpenSSH keys and textual
certificates fit; binary DER, PKCS#12 and PuTTY PPK do not.

`vault ui-reset` removes only `ui.json`; `master.key` and `vault.enc` are
untouched. If `master.key` is missing while `vault.enc` remains, light-tools
refuses to mint a replacement.

## `light_ssh` and `light_scp`

Named TOML profiles with per-call `remote`, `key`, `key_ref`, `cert_ref`,
`port`, `proxy_jump` and `timeout_ms` overrides. Strict host-key checking,
noninteractive batch mode, inherits `SSH_AUTH_SOCK`, retries exactly once and
only after a timeout.

`light_ssh` compacts its output the same way `light_bash` does, with the same
per-stream spill keys, and recovery runs through `light_bash` `read_block`
because the two share one spill store.

The `compact` field is the exact-output valve, and it has three states:

| `compact` | Behaviour |
| --- | --- |
| omitted | auto — compact prose, leave JSON and NDJSON payloads byte-exact |
| `false` | never compact; `stdout` is guaranteed verbatim |
| `true` | compact even a payload auto would have left alone |

Auto errs toward exactness because `light_ssh` callers decode `stdout` — NDJSON
from a log query, base64, tar bytes — and an outline would break that decode far
downstream from here. Use `compact: false` whenever you intend to parse the
output and want the guarantee in writing.

```toml
[remote.production]
host = "example.internal"
user = "deploy"
port = 22
proxy_jump = "bastion.internal"
key_path = "/home/me/.ssh/id_ed25519"
```

SCP requires exactly one remote endpoint, resolves the local endpoint through
the effective confinement policy — unconfined by default, so any path outside
the denied private roots — and distinguishes SSH `-p` from SCP `-P`.

## `light_ops` (read-only)

Service IDs are source-qualified: `systemd:api`, `pm2:worker`, `docker:web`.
Ambiguous bare names return candidates.

- `list_services`, `probe_service`, `probe_port`, `probe_process`, `probe_file`
- `log_window`, `log_trace`, `log_search`, `log_grep`, `log_errors`,
  `log_since`, `log_correlate`, `log_investigate`
- `status`, `collect`, `cancel` for local async scans

There is no start/stop/restart verb.

Log bodies — `log_window` and its siblings' `content`, and `log_correlate`'s
`timeline` — are compacted the same way `light_bash` output is, and a compacted
body carries `spill_id` plus a ready-made `recover` call that resolves through
`light_bash` `read_block`. `capped: true` still means the byte ceiling dropped
the oldest lines before compaction ever saw them; those bytes were never stored
and are not recoverable. `log_investigate` is deliberately NOT compacted: it
returns an assembled structured result, not a log body.

Two kinds of path are governed differently on purpose. **Caller-supplied paths**
(a `path` argument, or a `file:/absolute/path` service ID) follow the
confinement policy, widened by `log_roots` when confined.
**Registry-discovered service logs** — what `journalctl`, `docker` or `pm2`
reports for a discovered service — are never confined; they live wherever the
service manager puts them, and reading them is the point of the tool.

## Confinement

**light-tools is unconfined by default: every path on the machine is reachable.**
That is deliberate. light-tools exists to replace the file and shell tools your
agent already has, and those are unconfined — an editor restricted to the spawn
directory does not replace one that edits anywhere, it just sends the agent back
to the unbounded tool for everything else.

This is more permissive than `@modelcontextprotocol/server-filesystem`, which
starts with no allowed directories and errors during initialization if it
receives none. The difference is intentional and worth stating plainly: that
server is the agent's *only* filesystem access, so a boundary there removes a
capability. Here it does not — your agent keeps its native tools either way.

Three postures. `config.toml` is consulted first and settles the question by
itself; the UI marker is consulted only when the config file is silent:

| Setting | Effect |
| --- | --- |
| `allowed_roots` in `config.toml` | Confined to those roots. Operator-owned and authoritative. |
| No `allowed_roots`, vault UI toggle on | Confined to the server's working directory. |
| Neither | **Unconfined** — the default. |

The order is by authority, not by breadth: an `allowed_roots` that is *wider*
than the working directory still wins over the UI toggle, because the config
file is the operator's and the UI is reachable by anyone who can already reach
the vault. What the toggle can never do is widen a boundary the config file
set. When `config.toml` sets `allowed_roots`, the switch renders inert and says
so, because a toggle that appears to work while a config file silently
overrides it is worse than no toggle. Like the tool withholding markers, it
takes effect **at the next MCP start** — the confiner is built once per process
and is immutable.

**A boundary bounds the tools, not the shell.** `light_file` paths, the local
endpoint of a `light_scp` transfer, and caller-supplied `light_ops` paths are
resolved through the boundary and refused outside it. `light_bash` resolves
only its working directory through the boundary; the command that then runs has
the full same-user filesystem access, which no path check could take away.

**Denied roots hold in every posture.** light-tools' own secrets, snapshots,
spills and telemetry are refused whether confined or not; widening the boundary
never widens that. Canonicalization and symlink evaluation also run in every
posture. See [SECURITY.md](../SECURITY.md).

## Configuration

No config file is required. Optional overrides at
`$XDG_CONFIG_HOME/light-tools/config.toml`:

```toml
allowed_roots = ["/work/project"]
log_roots     = ["/var/log", "/srv/myapp/logs"]
```

`allowed_roots` confines `light_file`, `light_scp` local endpoints and
caller-supplied `light_ops` paths, and confines the working directory
`light_bash` runs in (not the paths its command touches — see
[Confinement](#confinement)); omit the key to run unconfined. An empty list is
refused rather than treated as "no boundary" — that reading inverts the
operator's evident intent.
`log_roots` widens caller-supplied `light_ops` log paths and nothing else.

`log_roots` precedence, highest first: `LIGHT_TOOLS_LOG_ROOTS` in the
environment, then in `$XDG_CONFIG_HOME/light-tools/.env`, then `log_roots` in
`config.toml`, then the built-in defaults `/var/log`, `~/.local/log`,
`~/.pm2/logs`. The `.env` is read **only** from the XDG config directory, never
from the working directory — that tree is writable by the agent being served, so
a repo-local file must not be able to widen the boundary.

Built-in defaults that do not exist are dropped; a root you configured yourself
fails at startup rather than silently narrowing what is readable.

Two environment switches turn behaviour off rather than configure it:
`LIGHT_NO_READ_DEDUP` disables read dedup entirely, including for a client that
sends its own `context_epoch`; `DO_NOT_TRACK` or `LIGHT_NO_TELEMETRY` disables
telemetry. Both take any non-empty value.

Configuration, secrets, snapshots, spills and telemetry have separate XDG roots
(directories 0700, private files 0600).

## Tool withholding

The union of two sources, both of which only ever add:

1. Launch arguments: repeatable `--disable-tool <name>`.
2. UI markers: one zero-byte 0600 file per withheld tool under
   `$XDG_CONFIG_HOME/light-tools/disabled-tools/`.

A flag-withheld tool cannot be re-enabled through the UI — removing its marker
changes nothing while the flag remains. An unrecognised marker name is refused
exactly as an unknown flag is. Markers take effect at the next MCP start, never
mid-process. No `disabled_tools` key exists in `config.toml`.

Endpoints (behind pairing plus password, Host-pinned):

- `GET /api/settings` → `{"tools":[{"name","disabled"}...],"ui_disabled":[...],
  "launch_withholding_observable":false,"confine":<bool>,
  "config_roots_authoritative":<bool>}`. `disabled` reflects marker state only.
  `confine` is the UI confinement marker; `config_roots_authoritative` reports
  that `config.toml` set `allowed_roots`, so the UI renders the switch inert.
- `POST /api/settings/tools` with exactly `{"tool":"<name>","disabled":<bool>}`
  mutates one marker. Creating an existing marker and removing an absent one are
  both success, so the MCP server and UI commute.
- `POST /api/settings/confine` with exactly `{"confine":<bool>}` sets or clears
  the confinement marker, with the same idempotence. It takes effect at the next
  MCP start and cannot reach a running server; it never overrides
  `allowed_roots`.

## Telemetry

`$XDG_DATA_HOME/light-tools-telemetry/` holds cumulative per-session snapshots
named `session-v1-<session>-<generation>.json`, written by a background writer
through temp+fsync+rename onto a name that did not exist, after which the prior
generation is removed. Each generation carries the session's full cumulative
totals, so a reader takes the highest generation per session.

Metrics: per-tool call counts, `terse_tokens_saved`, `dedup_bytes_saved`,
`write_bytes_saved`, and the output-compaction pair
`compact_bytes_considered` / `compact_bytes_delivered`. The compaction pair is
stored as two absolute totals rather than one difference, because a difference
cannot say what it was a difference OF — keeping both is what makes the ratio
measured rather than asserted. Totals are a persisted lower bound — the most recent
activity may not be on disk yet, and old sessions are pruned by retention and a
session cap.

Opt out with `DO_NOT_TRACK=1` or a non-empty `LIGHT_NO_TELEMETRY`; with either
set, no telemetry code records or writes anything.

## Symbol extraction

tree-sitter, behind the `treesitter` build tag with CGo. Windows ARM64 ships
without it: `symbol` and `outline` return the documented no-symbol response and
every other verb is unaffected.

## Development

```sh
go build -tags treesitter ./...
go test -tags treesitter ./...
```