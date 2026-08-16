# Light Tools portability contract

This document is the extraction reference for the standalone implementation.
The port is intentionally single-process and single-user. It must not import or
recreate a WebSocket hub, EDCR/plan gate, Git checkpoints, pipeline sockets,
router databases, telemetry, board/Discord integration, or multi-host routing.

## Registration seam

Every tool is registered through one direct-return adapter. JSON schema failures
use an `E_SCHEMA` diagnostic, parser failures include a byte caret where one is
known, and handler failures are shaped as `E_TOOL`. There is no one-slot channel
shim and no post-call governance accounting.

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

## Stable limits and edge cases

- Sed refuses zero matches as `not_found` and more than one match as
  `ambiguous` unless `all:true` or an exact `count` is supplied. Regex mode
  is opt-in. A dry run emits a unified diff and writes nothing.
- Locate uses `rg --json` when present, rejects glob metacharacters in the
  single-file path, retries an invalid regular expression once as a fixed
  string with a warning, and stops after 501 matches. A pure-Go scanner is the
  fallback when ripgrep is unavailable.
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
- Secret values enter commands only through named environment or temporary-file
  references. They never appear in tool arguments or normal results; output
  scrubbing is best-effort. SSH key_ref and cert_ref values are materialized
  only as mode-0600 temporary files and removed after use.
- Operations service discovery uses source-qualified IDs such as
  `systemd:api` and `docker:api`; a bare ambiguous name fails with the
  candidate IDs.

## Mutation transaction invariant

Every write goes through one typed mutation IR and one commit primitive:
resolve beneath an allowed root, lock by canonical path, read the preimage,
verify an optional SHA-256 CAS, capture a snapshot, atomically replace in the
same directory while preserving mode, recheck identity, fsync the file and
directory, and return the post-write SHA. Unchanged content is a no-op.