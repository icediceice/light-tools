# light-tools parity report

Session date: 2026-08-16 · binary: `0.1.0-dev` built `-tags treesitter` · host: light-worker

Every row below is backed by a build verdict, a test verdict, or a live JSON-RPC
transcript captured this session. Rows that could not be exercised say so
explicitly rather than assuming a pass.

Method: the server was driven over real stdio JSON-RPC (newline-delimited, no
framing) with the fixture tree as its launch cwd, so the fixture directory was
also its allowed root. Stateful cases (spill ids, async task ids, payload
stages) were driven through a single persistent process via a shell coprocess,
because that state is process-local.

## Verdict

The port is **functionally faithful on almost every documented contract** — the
protocol, the read/mutation surface, the payload grammar, snapshots, secrets and
the async lifecycle all behave as `PORTING.md` promises. Three defects are worth
fixing before anyone relies on it, one of which is a security boundary that does
not hold.

## Build and test

| Check | Verdict |
| --- | --- |
| `go build ./...` | exit 0 |
| `go build -tags treesitter ./...` | exit 0 (CGo tree-sitter compiles) |
| `go test ./...` | exit 0 — 13 packages ok, 3 no test files |
| `go test -race -count=1 ./...` | exit 0 — no data races |
| `go test -tags treesitter -count=1 ./...` | exit 0 — `internal/symbol` genuinely executes |

`-count=1` was used deliberately: the first runs reported `(cached)` for every
package, and a cache hit is not a test run.

## Protocol and registration — PASS

| Case | Observed |
| --- | --- |
| `initialize` | `protocolVersion 2025-06-18`, `serverInfo{light-tools, 0.1.0-dev}` |
| id-less notification | silently dropped, no response line |
| `ping` | `{}` |
| unknown method | `-32601 method not found` |
| malformed JSON | `-32700 parse error`, **server survives and keeps serving** |
| default profile | `tools/list` returns `light_file` only |
| `--enable-shell --enable-remote --enable-ops` | all five tools, sorted, schemas populated (file 40 props, bash 14, ops 17, ssh/scp 9) |

Capability gating is by **absence**: a disabled tool is not registered, so it
cannot be called rather than being denied.

DIVERGE (cosmetic): the parse-error response omits `id` entirely rather than
sending `"id": null`. JSON-RPC 2.0 requires null when the id is undeterminable.

## `light_file` — PASS with one gap

| Case | Observed | Verdict |
| --- | --- | --- |
| bare read | returns tree-sitter outline (`symbols[]` with kind/signature/comment/lines) | PASS |
| windowed read | one-based `cat -n`, meta `total_lines/bytes/tokens/sha256/continued/next_offset` | PASS |
| `offset:397` | yields lines 398+ — offset 0-based, numbering 1-based | PASS |
| batch `items[]` | shares 128 KiB budget, emits exact `[CONTINUE <b64>]` cursor at 131,116 B | PASS |
| `locate` | `{path,line,text,end}` matches plus `warning` field | PASS |
| single read, huge `limit` | **1,109,187 bytes returned, no cap** | **GAP** |
| image, single read | `type:image`, `mimeType:image/png`, valid base64 | PASS |
| image > 9 MiB | degrades to `"image exceeds 9 MiB MCP image limit"` | PASS |
| image in batch | `[image description] … (batch reads do not emit image blocks)` | PASS |
| `total_lines` | **401 for a 400-line file; phantom empty line rendered** | **BUG** |
| sed, 2 matches | `E_TOOL … ambiguous: find text matched 2 times` | PASS |
| sed, 0 matches | `not_found: find text did not match` | PASS |
| sed `dry_run` | unified diff, writes nothing (proven: later sed still matched both) | PASS |
| sed `count:2` | `replacements:2` + `sha_after` | PASS |
| bad `expected_sha` | `CAS conflict: expected … found …` | PASS |
| `vault_list` | ring capped at 3 versions, mode 384 (0600), sha + timestamp each | PASS |
| `vault_restore version:1` | restored the newest preimage correctly | PASS |
| payload multi-file batch | both sections committed in one call | PASS |
| unterminated body | `{"status":"partial","stage":"…","got_lines":6}`, wrote nothing | PASS |
| stage resume | `@stage`/`@from_line 7` completed the file exactly | PASS |
| unknown header | `E_PAYLOAD` with line, column, byte offset, source line and caret | PASS |
| payload `@find`/`@replace` sections | **not exercised** — the literal token trips this host's search-intent ban, so the harness could not send it | UNPROVEN |

## `light_bash` — PASS with one bug

| Case | Observed | Verdict |
| --- | --- | --- |
| sync execution | `stdout`/`stderr` separated, `exit_code:3` preserved | PASS |
| root confinement (cwd) | `path "/etc" escapes allowed roots` | PASS |
| minimal environment | only `HOME LANG PATH PWD SHLVL SSH_AUTH_SOCK` survive | PASS (by design) |
| timeout | **`{"exit_code":-1}` after 307 ms, no error at all** | **BUG** |
| spill | opaque random `spill_id`, `truncated:true`, tail preview | PASS |
| `read_block` + `line_range` | recovers exact ranges | PASS |
| async lifecycle | `queued` → `running` → `collect` → `done` with full result | PASS |
| cancel | `cancelling` → `cancelled` | PASS |
| `env_refs` | exact value reached the process and was redacted on direct output; transformed/encoded disclosure remains possible | PASS within documented best-effort boundary |
| missing secret | `secret "nonexistent-name" not found` | PASS |

Note: `line_range` addresses the **spill file's** lines, which include a leading
`STDOUT` header, so `10-13` returned command output values 9-12. Documented
behaviour, but easy to misread.

## `light_ops` — PASS with one known lines divergence

| Case | Observed | Verdict |
| --- | --- | --- |
| `list_services` | real discovery, source-qualified ids (`docker:firecrawl-api-1`) | PASS |
| `probe_port` / `probe_process` | `{listening:true,port:22}`, `{alive:true,pid:1}` | PASS |
| `probe_file` | exists/mode/size/modified | PASS |
| `log_window` / `log_search` / `log_trace` | filters and identifier tracing work; `service:"file:app.log"` | PASS |
| `restart_service` | `unsupported read-only ops verb` — no mutation verbs exist | PASS |
| `lines:3` on `log_window` | **ignored — returned all 5 lines, `capped:false`** | DIVERGE |
| path outside allowed root | `log_window path:/etc/hostname` → `path is outside the configured log roots`; `/var/log/dpkg.log` still reads | **FIXED** |

## Parity against the real Light tools

| Behaviour | Real `light_*` | light-tools | Verdict |
| --- | --- | --- | --- |
| oversized single read | `[WARN: limit clamped to transport ceiling 5000]` + `[CONTINUE]` cursor | clamped to 5000 lines / 128 KiB, `continued:true`, `next_offset:5000` | PASS |
| batch item with `limit:0` | clamped to 1 with a warning | defaults to 120 lines | DIVERGE |
| read dedup | UNVERIFIED — a byte-identical re-read this session returned full content, no dedup stub | opt-in, only when `context_epoch` is supplied; image reads never consult the ledger (read from source) | UNPROVEN |
| write outside repo | refused (git checkpoint required) | allowed anywhere beneath an allowed root | DIVERGE (by design) |
| shell command policy | search-intent bans, governance gate, plan manifest | **no command inspection whatsoever** | DIVERGE (by design) |
| spill recovery | `[full: …]` footer with `.indexed` path | opaque `spill_id` + `truncated:true` | DIVERGE (equivalent) |
| multimodal image read | image block, 9 MiB ceiling, batch degrades | identical | PASS |

The port is deliberately ungoverned: no phase gate, no write budget, no plan
approval, no banned commands. `portable.Invoke` validates only tool name, handler
presence and JSON validity; `Runner.runSync` never inspects the command string
before handing it to `/bin/bash -c`. That matches `PORTING.md`'s intent, and the
code genuinely backs the claim.

## Not covered

- `light_ssh` / `light_scp`: registration and schemas verified; no live
  round-trip, which would require a reachable host and a real key. UNPROVEN.
- Payload `@find`/`@replace` sections (see above). UNPROVEN.
- Windows/macOS behaviour; everything here is Linux amd64.

## Fixes applied

All three defects are closed, re-verified against a freshly built binary over
real stdio, and covered by tests.

1. **Unified path confinement** — one explicit `security.Confiner` applies
   caller-facing allowed roots and private-state denied roots across file
   transactions, rename targets, SCP, and operations paths. Directory listing
   and both recursive locate engines filter every visited or returned path.
   Registry-discovered PM2 files may remain outside `log_roots`, but the
   secrets, snapshot, and spill roots are still denied. `log_roots` is
   configurable via `config.toml`, an XDG-only `.env`, or
   `LIGHT_TOOLS_LOG_ROOTS`.
2. **Timeout made observable** — the deadline check now precedes the
   `*exec.ExitError` branch, and the result carries `timed_out`, `timeout_ms`
   and `error` while preserving partial `stdout`/`stderr`.
3. **Single-path read bounded** — 5000-line ceiling plus the shared 128 KiB
   budget, with `continued`/`next_offset` set. An oversized logical line goes
   to the shared spill store; a file above 256 MiB is refused before it is read.
4. **Phantom trailing line dropped** in both `readWindow` and `renderItem`; an
   empty file now reports zero lines.

Two further hardening changes came out of peer review and are included:

- **`expected_sha` continuation identity** — paging a file that changed between
  pages is refused rather than silently duplicating or dropping lines.
- **Span-aware read dedup** — the ledger keyed only on the whole-file hash, so
  once clamping forced multi-page reads, page 2 of an unchanged file would have
  returned a `[dedup]` stub and made the continuation contract unusable.

Root resolution was also corrected: `~` now expands against the home directory
(it was being joined to the working directory), and absent built-in default
roots are dropped before constructing the canonical confiner, so shipping
`~/.local/log` does not disable every `light_ops` call on a machine without
that directory.

## Still open

- `lines` is still ignored by `log_window` (row above), tracked separately.
- Batch item `limit:0` defaults to 120 rather than clamping to 1.
- `readWindow` still reads the whole file before slicing; the 256 MiB refusal
  bounds it in practice, but streaming would remove the exposure. Logged as a
  todo.
- The vault has no cross-process write lock, and the write-only vault web UI is
  split into its own plan. Both logged as todos.