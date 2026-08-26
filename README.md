# light-tools

Bounded file, shell, SSH/SCP, and service-log tools for AI coding agents,
spoken over MCP. One Go binary, stdio, no daemon and no database.

light-tools is one component of the Light stack, extracted to stand on its own.
What it does is context engineering on agent-native tools: the same operations
your agent already has, but windowed, deduplicated, snapshotted and bounded, so
results stay small enough that the agent can keep working instead of drowning in
its own output.

These are meant to REPLACE the file and shell tools your agent ships with, not
sit beside them, so they reach any path by default. Confine them to a boundary
with `allowed_roots` or the vault UI toggle — that bounds `light_file` paths,
local SCP endpoints and caller-supplied `light_ops` paths, while `light_bash`
only has its working directory bounded and its commands still reach anywhere.
See [Confinement](docs/REFERENCE.md#confinement).

Not sure? Ask your agent to read this README and compare light-tools against the
file/shell tools it currently has.

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

## Why a layer, and not a passthrough

Your agent already has a file reader and a terminal. light-tools does not replace
what they do — it replaces how much it costs to use them.

A native tool is built for a caller with unlimited attention. It reads a file by
returning the file. It runs a command by returning everything the command
printed. It edits by taking the new contents. None of that is wrong, and all of
it is fine for a human at a keyboard. For a model, each of those is a bill paid
out of the one budget that determines whether the task can be finished at all:
the context window. An agent that spends its window on output it has already
seen, or on the 19,800 lines it did not need, stops being able to reason about
the 200 it did.

The layer is the difference between those two callers:

| The native tool | The same operation here |
| --- | --- |
| Returns the whole file | Returns the window you asked for, plus an exact `[CONTINUE]` cursor |
| Re-sends bytes you already hold | Returns a `[dedup]` stub naming the path and content hash |
| Takes the whole new file to change a span | Takes the span, and keeps a pre-mutation snapshot |
| Has no undo | `vault_restore`, off a snapshot ring |
| Prints everything the command printed | Bounds the output, spills the rest to an indexed file, hands back a cursor |
| Expands `rm *.tmp` and runs it | Enumerates the surface first, captures it, and returns a revert handle |
| Reports a bad call as a failure | Repairs what is unambiguous and tells the model what it repaired |

Two consequences are worth naming because they are the actual product.

**Bounded by construction, not by convention.** Every result has a ceiling that
does not depend on the model choosing a sensible `limit`. A read is windowed
whether or not it was asked to be; a noisy command spills rather than floods.
The agent cannot blow its own context by being careless, which matters precisely
because carelessness is not the exception.

**One set of semantics across harnesses.** The five tools are one Go binary
behind a single invocation seam, so `light_file edit` means the same thing in
every harness that can speak MCP. Native tools differ per harness in name,
shape, and limits; anything you teach an agent about them is relearned when it
moves.

## Engineered against how models actually fail

The failure modes below are not hypothetical categories. They are the specific
things models across different families do to these tools, and each one is a
behaviour in the binary rather than an instruction in a prompt. Prompts are
advice a model may ignore under load; the tool is the thing it actually hits.

The governing rule for all of it: **repair, then say so.** A tool that silently
fixes a malformed call is worse than one that refuses, because the model never
learns the shape and makes the same call forever — the cost stops being one
wasted turn and becomes a permanent tax. Every repair below rides back with a
warning naming what changed.

**A field name that is one character off.** Every tool schema sets
`additionalProperties: false`, so `pth` instead of `path` used to be a hard
refusal — a whole turn spent to learn one character. Near-miss keys are now
repaired to the declared name. Distance is optimal string alignment rather than
plain Levenshtein, because an adjacent transposition — `raed` for `read`, the
most common typo there is — costs one edit, not two.

**Dispatching on the wrong key.** Models reach for `cmd`, `action`, `op` or
`mode` where the schema wants `verb`. Those fold onto `verb` when the tool
declares one and none was given. An explicitly supplied `verb` always wins.

**A verb that does not exist.** It is corrected to its nearest catalogue match —
*unless* that match mutates the filesystem. A typo one edit away from `write`,
`rename` or `vault_restore` is **refused, never coerced**, naming the candidate
so the model can send it deliberately. A repair layer that can silently start
writing files is worse than the refusal it replaced, so that branch is the one
that never guesses. Where nothing is close enough to correct, the error carries
the closest match *and* the full sorted vocabulary, so the next call is right
rather than another sample from the same distribution.

**Guessing when the answer is a coin flip.** A key equidistant from two declared
fields is dropped, not assigned. An ambiguous repair presented to the model as a
correction is worse than an honest one-line report of what was dropped.

**A value of the right meaning and the wrong type.** `"12"` where an integer
belongs is coerced; an optional null is omitted rather than rejected. A value
that genuinely cannot be coerced is still an error — with a caret, line and
column pointing at the offending character, not a restatement of the schema.

**Refusing without saying where, why, or what to do instead.** The caret envelope
is not a payload-parser flourish — it is what all five tools return whenever they
refuse. `error[CODE]` is followed by `at:`, `fix:` and `detail:`, always. `at:`
names the origin at whatever precision is available: `light_ops/probe_port` for
the call itself, `light_file.limit` for the field a schema check rejected, and
`payload, line 2, column 1 (byte 14)` for a position inside a sealed body, which
keeps its source line and `^` marker underneath. `fix:` is the remedy, and it
draws the one distinction the caller actually acts on — correct the arguments and
retry, versus a platform fault where sending the same call again is the wrong
move. A repair applied before the failure is folded into `detail:` rather than
discarded, so a call whose key was renamed and then refused reports both facts
instead of only the second.

**A payload too large to send in one call.** Multi-line writes go as a sealed
heredoc, and a truncated one can be staged and resumed from the line it stopped
at instead of being restarted from the top.

**Destroying a surface the model never enumerated.** Modeled mutations are
enumerated before execution whether they name explicit paths (`rm a.tmp b.tmp`)
or an unquoted glob (`rm *.tmp`). When the whole surface can be durably
captured, the command runs on first contact and the result carries a working
`vault_restore` handle. Explicit non-glob captures are limited to 64 MiB of
regular-file preimages measured as they are read; an explicit surface that is
unprotectable or exceeds that ceiling still runs, but reports
`protection:"unbacked"` and the reason. An unprotectable unquoted glob is the
only case that refuses: it names the blocker, shows the complete expanded
surface, and binds an unbacked retry to that exact surface with a digest.

**Reaching for a tool that was deliberately withheld.** A disabled tool is never
registered at all, so it cannot be called and then apologised for. An unknown
name in `--disable-tool` is refused at startup rather than silently ignored.

## The compression layer

Light tools are designed around JIT and dynamic-recompilation principles: expose
only the information required for the current execution path, as late as
possible, while preserving the semantics needed to continue safely.

Context is the scarce resource, and a tool that returns everything it knows
spends that resource on the caller's behalf without being asked. But the naive
fix — truncate, summarise, elide — trades a budget problem for a correctness
problem, because the caller cannot tell the difference between "this is all of
it" and "this is the part you were shown". So every reduction here is paired
with a way back, and refuses itself when it cannot prove the reduction is safe.

**Outbound encoding is lossless or it does not happen.** Structured results above
a size floor are re-rendered into a compact form — below that floor the
bookkeeping costs more than it saves, so the original is returned untouched. The
compact candidate is then *decoded back and compared to the original value for
exact structural equality*. If the round trip does not reproduce the input, or if
the "compact" rendering is not genuinely smaller in both bytes and estimated
tokens, the candidate is discarded and the raw payload goes out unchanged. There
is no lossy mode to fall back to, and no threshold at which fidelity is traded
for size. Errors are never re-encoded: a diagnostic is the one payload whose
exact shape the caller may be matching on.

**Oversized output is paged, not truncated.** A command that produces more than
the inline limit gets a summarised body plus a full indexed spill written to
disk. The complete bytes stay retrievable verbatim by line range, or searchable
by pattern, for the life of the spill. A caller that already knows what it is
looking for can declare anchors up front and have those lines kept in the inline
body. The distinction that matters: nothing was discarded, so the decision to
read more is made later, by the caller, on evidence — which is the point of
deferring it.

**Re-reads collapse against content, not against paths.** A ledger keyed on
context, path, and content hash returns a short stub when a read would deliver
bytes that were already delivered in the same context window. Keying on the hash
rather than the path is what makes it safe: an edited file always
re-materialises, because its hash no longer matches. It is disabled entirely when
no context key is supplied, and an explicit force flag always re-sends. The
invariant is narrow and deliberate — it can only ever elide content the caller
demonstrably already holds.

Taken together these are the same discipline as the argument repair described
above, pointed the other way. Inbound, the layer reconstructs the call the caller
meant and reports what it changed. Outbound, it emits the smallest form that
provably still means what the original meant, and keeps the remainder
addressable. Neither direction is allowed to guess, because a layer that quietly
guesses is worse than the raw surface it replaced: the failure it introduces is
invisible at the call site and surfaces later, in a decision made on incomplete
information the caller believed was complete.

## How much this is worth

One measurement, taken at two very different sample sizes.

**At scale, on the Light stack** — the numbers in [Telemetry](#telemetry) below,
across 319K tool calls. Of 2190.9M tokens considered, 345.0M were delivered:
the context ledger closes at **1845.9M tokens saved, about 84% of the corpus the
calls had in front of them.** The telemetry instruments tool calls and nothing
else, and what it measures is the same targeting-and-compression primitive
light-tools ships — so this is not a different claim scaled up, it is the same
quantity over a much larger sample. Coverage there is partial (36.8% / 15.2%)
and, as explained below, that can only under-state the saving.

**Locally, in light-tools itself** — 12,194 tokens from terse output, 164,087 B
from read dedup and 116,363 B from span writes, over 45 calls in three throwaway
sessions on this repository. A deliberately small sample from one machine,
reported as a persisted lower bound rather than extrapolated into a rate.

What separates the two figures is how much was measured, not what: 319K calls
against 45, on the same primitive.

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
2. Only once that guard works, take away native tools and write how to use light-tools into your agent.md or claude.md or which ever .md that is your start config :
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

## Telemetry

### At scale, on the Light stack

The Light stack's own telemetry, over 319K tool calls. It instruments tool calls
and nothing else, and the primitive it measures — targeting and compressing what
a tool hands back — is the one light-tools implements, so these numbers and the
local ones below measure the same thing on very different sample sizes:

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

Read dedup is on by default: the server scopes it per connection, so a client
that sends no context epoch still gets it. Set `LIGHT_NO_READ_DEDUP` to switch
it off. Totals are a persisted lower bound.

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
