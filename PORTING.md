# Light Tools portability contract

This document is the extraction reference for the standalone implementation.
The port is intentionally single-process and single-user. It must not import or
recreate a WebSocket hub, EDCR/plan gate, Git checkpoints, pipeline sockets,
router databases, telemetry, board/Discord integration, or multi-host routing.

## Registration seam

Every tool is registered through one direct-return adapter. The schema
advertised by `tools/list` travels with the handler into that adapter. It
recursively rejects unknown fields and invalid shapes and performs only
schema-directed scalar coercion. Valid payload bytes are retained unchanged;
coerced numbers use `json.Number`, so integers above 2^53 never round-trip
through `float64`. JSON schema failures use an `E_SCHEMA` diagnostic, parser
failures include a byte caret where one is known, and handler failures are
shaped as `E_TOOL`. All remain ordinary MCP tool-result errors. There is no
one-slot channel shim and no post-call governance accounting.

## Sealed mutation payload (format 1)

- Headers are bare `@key value` lines. Mutation bodies begin only after a bare
  `@content`, `@new_string`, `@find`, or `@replace` header.
- The default terminator is a line exactly equal to `<<LF-END>>`. Leading or
  trailing whitespace and near-miss text are body bytes, never terminators.
- `@until TOKEN` changes the next body terminator to an exact `TOKEN` line.
- A top-level `@file PATH` starts one operation in a multi-file batch. Every
  body in every section has its own terminator.
- Unterminated bodies, unknown headers, duplicate scalar headers, and invalid
  selectors fail before any mutation is committed.
- `payload_version` is reserved; the current grammar is format 1.

## Deterministic outbound formatter

The standalone port retains only the strict, deterministic formatter semantics
that do not depend on Light's hub retaining a raw telemetry copy:

- `LIGHT_TERSE_OUTPUT` is read once at startup and enabled only by exactly
  `1`; default output remains raw JSON text.
- A punctuation-aware estimate and 100-token input floor avoid spending work on
  small responses. A transformed value is used only when both estimated tokens
  and UTF-8 bytes strictly decrease.
- JSON is one complete document decoded with `UseNumber`. Object keys are
  sorted. Supported grammar is scalar values, non-empty objects with recursively
  supported values, scalar arrays, and arrays of non-empty objects with an
  identical key set.
- The production decoder reconstructs `map[string]any`, `[]any`,
  `json.Number`, strings, booleans, and null. Every candidate is decoded and
  `reflect.DeepEqual` compared with the original parsed value before emission.
  Unsafe keys, empty containers, heterogeneous shapes, malformed/trailing JSON,
  and ambiguous string values fall back to exact raw bytes.
- Only a successful text block at `content[0]` is eligible. The content slice
  is cloned; handler-owned `Result` pointers, `content[1:]`, images, errors,
  and non-JSON text are never mutated.

Deliberately excluded are `stripRedundant`, `looseToTerse`, the Light
smart-index renderer and budget/dedup layer, grouped locate output,
`factorRows`, and hub telemetry/F3/raw-copy machinery. Those mechanisms are
lossy or control-plane-specific in the source environment. The terse token
estimate also stays separate from `light_file read`'s public `tokens` field,
which is a consumer-visible contract.

## Stable limits and edge cases

- Sed refuses zero matches as `not_found` and more than one match as
  `ambiguous` unless `all:true` or an exact `count` is supplied. Regex mode
  is opt-in. A dry run emits a unified diff and writes nothing.
- Locate uses `rg --json` when present, rejects glob metacharacters in the
  single-file path, retries an invalid regular expression once as a fixed
  string with a warning, and stops after 501 matches. A pure-Go scanner is the
  fallback when ripgrep is unavailable. The engines do not yet claim parity:
  ripgrep follows ignore/hidden rules and currently drops JSON context events;
  the Go walker skips only `.git` and joins context into `text`. Match
  offsets identify the matching line even when the Go `text` contains context.
- A single png/jpg/jpeg/webp/gif read returns an MCP image block only when the
  decoded file is at most 9 MiB. Larger images degrade to a text description.
- Remote execution and transfer retry exactly once, and only after a timeout.
- Read windows are one-based `cat -n` lines. Batch reads share an output budget
  and return exact continuation cursors rather than silently dropping content.
- Symbol extraction deduplicates captures by body-start byte, filters low-value
  symbols, preserves leading comments, derives Go receivers and class
  ancestors, and watchdogs each parse. Builds without tree-sitter return a
  graceful no-symbols response.
- Snapshot rings live only below the snapshots root. Reaping never receives a
  broader state root. Rewrite restores the newest pre-mutation snapshot and
  applies the correction in one hash-guarded commit.
- Shell spill files are selected only by opaque in-memory IDs. Caller-supplied
  spill paths and symlinks are rejected.
- The shell wildcard guard is intentionally narrow and process-local. An active
  filename `*` or `?` previews without execution, and one byte-equivalent
  full request retry consumes the receipt. Receipts expire after ten minutes
  and the map is capped at 64; expiry and eviction re-arm the preview. Explicit
  filename lists and file-tool batches are not fenced. POSIX quoting/escaping
  suppresses shell expansion; PowerShell provider wildcards remain active even
  when quoted and are guarded. Variables, substitutions, scripts, and
  program-internal pattern languages are outside this lexical guard.
- Secret values enter commands only through named environment or temporary-file
  references. They never appear in tool arguments or normal results; output
  scrubbing is best-effort. SSH key_ref and cert_ref values are materialized
  only as mode-0600 temporary files and removed after use.
- Operations service discovery uses source-qualified IDs such as
  `systemd:api` and `docker:api`; a bare ambiguous name fails with the
  candidate IDs.
- `light_ops` confines CALLER-SUPPLIED log paths (a `path` argument or a
  `file:/absolute/path` service ID, plus `probe_file`) to `allowed_roots` +
  `log_roots`, and deliberately does NOT confine registry-discovered service
  logs. Service managers put logs where they like; confining that branch would
  break the tool's main purpose. `grepPool` swallows per-service fetch errors
  with a bare `continue`, so an accidental confinement of the registry branch
  would surface as "no matches" rather than an error — there is a regression
  test pinning this.
- The caller-path root union is compiled ONCE at startup with absent roots
  dropped. `security.ResolveBeneath` canonicalizes every root on each call and
  errors on the first one that does not exist, so a single missing root would
  otherwise disable every other root on every request.
- Config authority is XDG-only. `LIGHT_TOOLS_LOG_ROOTS` is read from the
  process environment and from `$XDG_CONFIG_HOME/light-tools/.env`, never from
  a `.env` in the process working directory — that tree is writable by the
  agent being served, so a repo-local file must not widen the boundary.
- A single-path read is bounded by BOTH a 5000-line ceiling and the 128 KiB
  response budget, and a supplied `limit` above that is clamped, not honoured.
  A page always emits at least one line so progress is monotonic; an oversized
  logical line goes to the shared spill store rather than being dropped or
  returned unbounded. Files above 256 MiB are refused, because the file is read
  whole in order to slice it.
- A terminal newline is a line DELIMITER, not an extra logical line, in both
  `readWindow` and `renderItem`; an empty file reports zero lines. The
  read-dedup ledger keys on the whole-file hash PLUS the requested span, so a
  continuation page of an unchanged file is not elided as an already-seen read.
- Paging identity is opt-in via `expected_sha`: when supplied it must match the
  file's current hash or the page is refused, so a file mutated between pages
  cannot silently duplicate or drop lines.
- A timed-out shell command is reported as a timeout (`timed_out`, `timeout_ms`,
  `error`) with partial output preserved. The deadline check MUST precede the
  `*exec.ExitError` branch: the timeout kills the process group, so the kill
  matches as an ordinary exit first and the timeout would otherwise be reported
  as a bare `exit_code: -1` with empty output.

## Release portability

The release matrix is Linux, macOS, and Windows on amd64 and arm64. Five lanes
use CGo plus tree-sitter. Windows ARM64 is a separate CGo-free build and keeps
the complete MCP/tool surface while returning the graceful no-symbol response.
Each native CI lane executes an `initialize` plus `tools/list` transcript
against its built binary. Release packaging remains workflow-owned; both
installers verify an exact checksum row before extraction.

This standalone server deliberately omits fleet routing, EDCR plans, RBAC,
telemetry, shared databases, and multi-operator file-surface fences. Those are
control-plane concerns, not dependencies of the five local tools.

## Mutation transaction invariant

Every write goes through one typed mutation IR and one commit primitive:
resolve beneath an allowed root, lock by canonical path, read the preimage,
verify an optional SHA-256 CAS, capture a snapshot, atomically replace in the
same directory while preserving mode, recheck identity, fsync the file and
directory, and return the post-write SHA. Unchanged content is a no-op.