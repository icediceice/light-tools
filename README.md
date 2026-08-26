# light-tools

**Better file and shell tools for coding agents. Read less, write less, waste fewer turns.**

Your coding agent should not need to read a 2,000-line file to change 5 lines.
It should not need five separate tool calls to read five files it already knows it needs.
And it should not dump thousands of log lines into context just because a command was noisy.

`light-tools` replaces the basic file and shell tools used by coding agents with versions designed for AI work.
It speaks MCP, runs as one Go binary, and needs no daemon or database.

## What changes?

### Read several files in one go

If the agent needs code from several files, it can read the relevant parts together in one tool call instead of fetching them one by one.

Without that, a simple investigation often looks like this:

```text
read file A
wait
read file B
wait
read file C
wait
```

With `light_file`, the agent can ask for the pieces it needs from A, B and C together.
That saves tool round trips and gives the model the related code at the same time.

Large files are also read in pieces. If the agent needs more, it gets an exact continuation cursor and can keep going from where it stopped.

### Change only the code that changed

Many old/new replacement tools make the model output both the code being replaced **and** the replacement.

`light_file` can edit by span instead: point at the lines or symbol to change, then send only the new content.

When the old and new blocks are about the same size, that can cut the edit payload roughly in half — on output tokens, which are usually the more expensive side of model usage.

Every file mutation is snapshotted first, so a bad edit can be restored.

### Do not send the same file twice

If the agent reads a file again and the content has not changed, `light_file` can return a short dedup notice instead of sending the same bytes back into context.

If the file changed, it is sent normally.

### Keep noisy commands out of the context window

Builds, tests and logs can produce thousands of lines.

`light_bash` keeps the useful part inline and spills oversized output to an indexed file. The full output is still available for search or paging later.

The goal is not to hide information. It is to stop paying the context cost before the agent knows whether it needs that information.

## The simple version

Without light-tools:

```text
read a huge file
read another huge file
read another file
receive the same content again
output old code + new code
print 5,000 log lines into context
retry when the session gets noisy
```

With light-tools:

```text
read the needed parts of several files together
continue only when more is needed
do not resend unchanged content
output only the replacement code
keep huge command output searchable instead of dumping it
restore a bad write if needed
```

**Read less. Write less. Repeat less.**

## The tools

| Tool | What it does |
| --- | --- |
| `light_file` | Read, search, inspect symbols and edit files without unnecessary full-file traffic |
| `light_bash` | Run local commands with bounded, searchable output |
| `light_ssh` | Run commands on a remote host through a named profile |
| `light_scp` | Copy files to or from a remote host through a named profile |
| `light_ops` | Read-only service, port and log inspection |

All five register by default. You can withhold tools completely:

```sh
light-tools --disable-tool light_ssh --disable-tool light_scp
```

A disabled tool is not registered, so the model cannot call it.

## Works with your existing code intelligence

`light-tools` is not a replacement for code search, indexing, language servers or repository intelligence.

Use whatever code-intelligence layer you prefer.

`light-tools` handles a different part of the problem: **how the agent reads, writes and operates on the machine after it knows what it wants to do.**

### Mutation safety

Modeled mutations are enumerated before execution whether they name explicit
paths (`rm a.tmp b.tmp`) or an unquoted glob (`rm *.tmp`). When the whole
surface can be durably captured, the command runs on first contact and returns
a working `vault_restore` handle. Explicit non-glob captures are limited to
64 MiB of regular-file preimages measured as they are read; an explicit surface
that is unprotectable or exceeds that ceiling still runs, but reports
`protection:"unbacked"` and the reason. An unprotectable unquoted glob is the
only case that refuses: it names the blocker, shows the complete expanded
surface, and binds an unbacked retry to that exact surface with a digest.

## Install

```sh
npm install --global @factor-i-o/light-tools
```

Requires Node 18.17+ and npm 10+.

Or build it directly:

```sh
go install -tags treesitter github.com/icediceice/light-tools/cmd/light-tools@latest
```

## Set up your agent

### 1. Install and register

For a known client:

```sh
light-tools init --client claude
```

You can also use `--client antigravity`, or print the MCP configuration without changing anything:

```sh
light-tools init --client print
```

For another MCP-capable agent, register `light-tools` as a stdio MCP server using the configuration your client expects.

Then restart the coding agent. `light-tools` is a stdio server, so the client starts it with the session; there is no background daemon to manage.

### 2. Verify before replacing native tools

After restart, the agent should see the enabled Light tools, normally:

```text
light_file
light_bash
light_ssh
light_scp
light_ops
```

> **Important:** do not disable the agent's native file and terminal tools until you know `light-tools` starts correctly and you have a fallback. If the MCP server is misconfigured or missing, an agent with no native tools may be unable to repair its own setup.

Once the fallback is verified, you can let the agent use `light_file` and `light_bash` in place of its native file and shell tools.

### Let the agent do the setup

You can paste this into your coding agent:

```text
Install light-tools with npm and register it as an MCP server for this coding environment.
Do not disable any native tools yet.
When registration is complete, stop and tell me to restart the session.
```

After restart:

```text
Verify that the enabled light-tools are available.
Before disabling any native file or terminal tool, create and test a fallback for this harness so native tools are re-enabled if light-tools is unavailable.
If you cannot build a working fallback, leave the native tools enabled and tell me why.
```

For the full setup procedure, see [AGENT-SETUP.md](AGENT-SETUP.md).

## Why this saves context and turns

### Multi-file reads

One call can request windows or symbols from several files. This matters because tool calls are not free: reading five known files one at a time creates five separate agent/tool round trips.

### Windowed reads

Large files are paged instead of dumped. The agent gets an exact `[CONTINUE]` cursor when more content exists.

### Read dedup

Unchanged content already delivered in the current connection can collapse to a small `[dedup]` response instead of being sent again.

### Span edits

The model can send only the replacement span rather than reproducing an entire file or an old/new pair. A snapshot is captured before mutation.

### Searchable command output

Oversized command output spills to disk and stays addressable by search or line range instead of filling the active model context.

None of these require the model to remember to "be efficient." The behavior lives in the tools.

## Safety and reliability

Coding agents make ordinary mistakes: wrong field names, misspelled verbs, malformed values and overly broad shell commands.

`light-tools` tries to make those mistakes cheap without silently guessing about dangerous mutations.

- obvious argument mistakes can be repaired and reported back to the model
- ambiguous or dangerous mutations are refused instead of guessed
- file writes are snapshotted before mutation
- destructive wildcard operations are inspected before execution when their surface can be determined safely
- disabled tools are not registered at all
- errors point at the failing field or payload location and tell the caller what to fix
- filesystem access can be limited with `allowed_roots`

**Confinement is not a shell sandbox.** `allowed_roots` bounds `light_file` paths, local SCP endpoints and caller-supplied `light_ops` paths. `light_bash` has its working directory bounded, but the commands it runs can still reach outside that directory.

Read [SECURITY.md](SECURITY.md) before treating confinement as a security boundary.

The detailed argument-repair, continuation, spill, snapshot and confinement semantics live in [docs/REFERENCE.md](docs/REFERENCE.md).

## Telemetry

### At scale on the broader Light stack

The broader Light stack uses the same targeting and output-reduction approach. Across **319K tool calls** in its telemetry:

| Stage | Tokens |
| --- | ---: |
| Considered | 2190.9M |
| Delivered | 345.0M |
| Difference | **1845.9M** |

That is about **84% less of the considered corpus delivered to model context** in that dataset.

This is not a claim that every `light-tools` call saves 84%. It is a larger Light-stack measurement of the same targeting/compression primitive, with partial instrumentation: corpus size was measured on 36.8% of calls and delivered size on 15.2%. Uninstrumented stages fall back to the previous measured point, so the published total is intended as a lower bound.

Other aggregate measurements from that dataset:

| Metric | Value |
| --- | ---: |
| Calls | 319K |
| Tokens in | 37.0M |
| Exec bytes saved | 6.7 GB |
| Raw → compacted | 12.0× |
| Average latency | 3.2s across all tools |

### Small local light-tools sample

A local measurement over **45 tool calls** in three throwaway sessions on this repository recorded:

| Source | Saved |
| --- | ---: |
| Terse output | 12,194 tokens |
| Read dedup | 164,087 B |
| Writing vs. full rewrite | 116,363 B |

That is a small sample, not a benchmark or extrapolated rate.

Local telemetry is aggregate-only: no paths, arguments, commands, hostnames or usernames are sent anywhere. Disable it with `DO_NOT_TRACK=1` or a non-empty `LIGHT_NO_TELEMETRY`.

## Platforms

| OS | amd64 | arm64 | Symbol extraction |
| --- | --- | --- | --- |
| Linux | native | native | tree-sitter |
| macOS | native | native | tree-sitter |
| Windows | native | native | tree-sitter on amd64; no-symbol fallback on arm64 |

Windows ARM64 is built without CGo. All five tools still work; only symbol and outline extraction degrade.

## Other install options

Checksum-verifying installers are also available from published GitHub releases:

```sh
curl -fsSL https://raw.githubusercontent.com/icediceice/light-tools/main/install.sh | sh
```

```powershell
Invoke-WebRequest https://raw.githubusercontent.com/icediceice/light-tools/main/install.ps1 -OutFile install.ps1
./install.ps1
```

Pin or relocate with `-Version` / `-Destination` on PowerShell or `LIGHT_TOOLS_VERSION` / `LIGHT_TOOLS_INSTALL_DIR` on POSIX. Both require an exact asset entry in `checksums.txt`.

## More

- [AGENT-SETUP.md](AGENT-SETUP.md) — full setup procedure
- [SECURITY.md](SECURITY.md) — what is and is not protected
- [docs/REFERENCE.md](docs/REFERENCE.md) — exact tool and verb semantics
- [RELEASING.md](RELEASING.md) — release process

---

Created by **[Factor I/O Studio](https://studio.factor-io.com)**. `light-tools` is one component of the Light stack, extracted to stand alone.

GPL-3.0-or-later.
