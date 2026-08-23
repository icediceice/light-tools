# light-tools

Bounded file, shell, SSH/SCP, and service-log tools for AI coding agents,
spoken over MCP. One Go binary, stdio, no daemon and no database.

light-tools is one component of the Light stack, extracted to stand on its own.
What it does is context engineering on agent-native tools: the same operations
your agent already has, but windowed, deduplicated, snapshotted and bounded, so
results stay small enough that the agent can keep working instead of drowning in
its own output.

## Install

```sh
npm install --global @factor-i-o/light-tools
```

Node 18.17+ and npm 10+. The native binary arrives as an exact-version optional
dependency, so installs work with lifecycle scripts disabled and through a
registry mirror.

Or build it:

```sh
go install -tags treesitter github.com/icediceice/light-tools/cmd/light-tools@latest
```

## Set up your agent

Two prompts, in order. The restart between them is required: light-tools is a
stdio server with no daemon, so your harness does not spawn it until it
restarts, and until then there is nothing to list and nothing to switch to.

**Prompt 1 — install and register.** Paste this to your coding agent:

```text
Install and wire up light-tools for yourself, then stop and tell me to restart you.

1. Run: npm install --global @factor-i-o/light-tools
2. Run: light-tools init --client claude
   (or --client antigravity, or --client print to see the config without
   writing anything). If your harness is not one of those, add this to its MCP
   config yourself:
     {"mcpServers":{"light-tools":{"command":"light-tools"}}}
   On Windows a harness that starts processes without a shell needs:
     {"command":"cmd","args":["/d","/s","/c","light-tools"]}
3. Do NOT change any tool permissions yet. Stop here and tell me to restart you.
```

Now restart your harness.

> **⚠️ Read this before prompt 2. Blocking your agent's native tools without a
> working fallback can strand it.** If light-tools is ever unavailable — not
> installed, failed to start, a bad config — an agent that has already denied its
> own file and terminal tools has no way to inspect or repair anything, including
> the config that broke it. You are the only one who can recover it at that point.
> Prompt 2 therefore asks the agent to build the fallback *first* and verify it,
> and to leave the native tools alone if it cannot.

**Prompt 2 — verify, then hand over.** Paste this after the restart:

```text
List your tools. You should see five: light_file, light_bash, light_ssh,
light_scp, light_ops.

If they are all there:

1. First, add a fallback guard for THIS harness, using whatever mechanism it
   supports (a hook, a permission rule, a startup check — your call). It must
   re-enable my native file and terminal tools whenever light-tools is not
   available, so that a missing or crashed server can never leave you with no
   tools at all. Show me what you wrote and how you tested it.
2. Only once that guard works, prefer light-tools over your built-in tools:
   read, list, search or edit a file with light_file; run a command with
   light_bash; touch a remote host with light_ssh or light_scp; inspect a
   service, port or log with light_ops.
3. If one of the five is missing, it was withheld on purpose. Say so instead of
   falling back to a native tool or asking me to run it by hand.

If you cannot build a working fallback, leave my native tools enabled and tell
me why.
```

`light-tools init` writes the configuration a known client expects; `--dry-run`
prints instead of writing.

**What persists:** the binary and the config do — install and `init` are
one-time. The server process does not: your harness spawns it per session and it
exits with the harness, so there is no daemon to manage. The one exception is
`light-tools vault ui`, a foreground command you keep running while the page is
open.

## The tools

| Tool | Use it for |
| --- | --- |
| `light_file` | Read, list, search, slice symbols, diff, write, edit, rename, undo |
| `light_bash` | Run a local command, sync or async, bounded output, secret refs |
| `light_ssh` | Run a command on a remote host through a named profile |
| `light_scp` | Copy files to or from a remote host through a named profile |
| `light_ops` | Read-only: discover services, probe ports, read and correlate logs |

All five register by default. Withhold one by name, repeatably:

```sh
light-tools --disable-tool light_ssh --disable-tool light_scp
```

A withheld tool is never registered, so the model cannot call it at all. An
unknown name is refused at startup rather than ignored.

## What it looks like in use

**Read a large file without flooding the context.** A windowed read returns the
slice you asked for plus an exact `[CONTINUE]` cursor, so a 20k-line file costs
one page, not twenty thousand lines.

**Read the same file twice in one task.** The second read comes back as a
`[dedup]` stub naming the path and content hash. The agent already has the
bytes; re-sending them buys nothing.

**Edit instead of rewrite.** `light_file edit` sends the changed span, not the
whole file, and captures a pre-mutation snapshot. `vault_restore` puts it back.

**Run something noisy.** `light_bash` bounds the output, spills the rest to an
indexed file, and hands back a cursor to grep or page through.

## Telemetry

### At scale, on the Light stack

These are the **Light stack's** measured numbers, not this binary's — light-tools
is one component of it, and the context-engineering approach is the shared part.
Reported by the platform's own telemetry over 319K tool calls:

| | |
| --- | --- |
| Calls | 319K |
| Tokens in | 37.0M |
| Bytes saved | 6.7 GB (exec compaction) |
| Compress ratio | 12.0× raw → compacted |
| Avg latency | 3.2s across all tools |

The context ledger, which is the part that matters for an agent's working memory:

| Stage | Tokens | |
| --- | --- | --- |
| Considered | 2190.9M | the corpus the call had in front of it |
| | −1840.4M | targeting |
| Offered | 350.5M | what the handler produced |
| | −5.4M | compression |
| Delivered | **345.0M** | what actually reached the model |

**Saved 1845.9M tokens.** That figure is `considered − delivered`, not a
separately accumulated counter, so the three points and the two deltas close by
construction rather than by agreement.

Coverage is partial and stated honestly: corpus instrumented on 36.8% of calls,
delivered size measured on 15.2%. An uninstrumented call falls back to the point
above it, so missing telemetry can only **under**-state the saving — it can
never manufacture one.

### Locally, in light-tools itself

`light-tools vault ui` has its own Telemetry view for the machine it runs on.
Measured over 45 tool calls in three throwaway sessions on this repository:

| Metric | Saved |
| --- | --- |
| Terse output | 12,194 tokens |
| Read dedup | 164,087 B |
| Writing (vs. full rewrite) | 116,363 B |

Read dedup only engages when the client sends a context epoch; without one it is
disabled and records zero. Totals are a persisted lower bound.

That local data never leaves the machine: aggregates only, no paths, arguments,
commands, hostnames or usernames, and no network component of any kind. Opt out
with `DO_NOT_TRACK=1` or a non-empty `LIGHT_NO_TELEMETRY`.

## Platforms

| OS | amd64 | arm64 | Symbol extraction |
| --- | --- | --- | --- |
| Linux | native | native | tree-sitter |
| macOS | native | native | tree-sitter |
| Windows | native | native | tree-sitter on amd64; no-symbol fallback on arm64 |

Windows ARM64 is built without CGo. All five tools work; only symbol and
outline extraction degrades.

Checksum-verifying installers. **Both need this repository to be public and to
have a published release to fetch assets from — install from npm above until
that is true.** They download release assets anonymously over HTTPS, so while
the repository is private they cannot authenticate and fail before checksum
verification. npm carries the same checksum-bound binaries and needs no GitHub
access.

```sh
curl -fsSL https://raw.githubusercontent.com/icediceice/light-tools/main/install.sh | sh
```

```powershell
Invoke-WebRequest https://raw.githubusercontent.com/icediceice/light-tools/main/install.ps1 -OutFile install.ps1
./install.ps1
```

Pin or relocate with `-Version` / `-Destination` (PowerShell) or
`LIGHT_TOOLS_VERSION` / `LIGHT_TOOLS_INSTALL_DIR` (POSIX). Both require an exact
asset entry in `checksums.txt`.

## More

- [AGENT-SETUP.md](AGENT-SETUP.md) — the setup prompt, self-contained
- [SECURITY.md](SECURITY.md) — what is and is not protected
- [docs/REFERENCE.md](docs/REFERENCE.md) — per-verb semantics
- [RELEASING.md](RELEASING.md) — release process

---

Created by **[Factor I/O Studio](https://studio.factor-io.com)**. light-tools is
one component of the Light stack, extracted to stand alone.

GPL-3.0-or-later.