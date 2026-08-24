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
It touches only `content[0]` when it is text and the result is not an error, and
only when the result gets strictly smaller in both bytes and items. Images,
later content blocks, plain text, malformed JSON and errors are untouched.

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

Mutations are confined to configured roots and use per-path locking, optional
`expected_sha` compare-and-swap, mode-preserving atomic replacement, identity
rechecks, file and directory fsync, and a three-version pre-mutation snapshot
ring.

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

Commands run under a root-confined `cwd`, a minimal inherited environment, and a
timeout that terminates the whole process group. Results preserve `stdout`,
`stderr` and `exit_code`.

A timed-out command carries `timed_out: true`, the `timeout_ms` that applied, an
`error` naming the timeout, `exit_code` `-1`, and whatever partial output it
produced before the kill — usually the only evidence of where it hung. An
ordinary non-zero exit is never relabelled as a timeout.

Output modes: `auto`, `head`, `tail`, `grep`, `read_block`. Large output is
compressed behind an opaque `spill_id`; recover exact ranges with `read_block`
plus `line_range`. `light_file` shares the same spill store.

Async:

```json
{"command":"go test ./...","cwd":"/work/app","async":true}
{"verb":"status","task_id":"TASK_ID"}
{"verb":"collect","task_id":"TASK_ID"}
{"verb":"cancel","task_id":"TASK_ID"}
```

A filename-wildcard guard catches shell arguments containing an active `*` or
`?`: the first request returns `dry_run:true` without starting a process, and
one identical retry may execute. Explicit lists such as `rm a.tmp b.tmp` run
immediately. This is lexical accident protection only — it cannot see a wildcard
arriving through a variable, command substitution, or a program's own pattern
language such as `find -name '*.tmp' -delete`.

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

```toml
[remote.production]
host = "example.internal"
user = "deploy"
port = 22
proxy_jump = "bastion.internal"
key_path = "/home/me/.ssh/id_ed25519"
```

SCP requires exactly one remote endpoint, confines the local endpoint to an
allowed root, and distinguishes SSH `-p` from SCP `-P`.

## `light_ops` (read-only)

Service IDs are source-qualified: `systemd:api`, `pm2:worker`, `docker:web`.
Ambiguous bare names return candidates.

- `list_services`, `probe_service`, `probe_port`, `probe_process`, `probe_file`
- `log_window`, `log_trace`, `log_search`, `log_grep`, `log_errors`,
  `log_since`, `log_correlate`, `log_investigate`
- `status`, `collect`, `cancel` for local async scans

There is no start/stop/restart verb.

Two kinds of path are governed differently on purpose. **Caller-supplied paths**
(a `path` argument, or a `file:/absolute/path` service ID) are confined to
`allowed_roots` plus `log_roots`. **Registry-discovered service logs** — what
`journalctl`, `docker` or `pm2` reports for a discovered service — are not
confined; they live wherever the service manager puts them, and reading them is
the point of the tool.

## Configuration

No config file is required; the default allowed root is the server's working
directory. Optional overrides at `$XDG_CONFIG_HOME/light-tools/config.toml`:

```toml
allowed_roots = ["/work/project"]
log_roots     = ["/var/log", "/srv/myapp/logs"]
```

`allowed_roots` is the filesystem boundary for `light_file` and `light_bash`.
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
  "launch_withholding_observable":false}`. `disabled` reflects marker state only.
- `POST /api/settings/tools` with exactly `{"tool":"<name>","disabled":<bool>}`
  mutates one marker. Creating an existing marker and removing an absent one are
  both success, so the MCP server and UI commute.

## Telemetry

`$XDG_DATA_HOME/light-tools-telemetry/` holds cumulative per-session snapshots
named `session-v1-<session>-<generation>.json`, written by a background writer
through temp+fsync+rename onto a name that did not exist, after which the prior
generation is removed. Each generation carries the session's full cumulative
totals, so a reader takes the highest generation per session.

Metrics: per-tool call counts, `terse_tokens_saved`, `dedup_bytes_saved`,
`write_bytes_saved`. Totals are a persisted lower bound — the most recent
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