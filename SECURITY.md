# Security

**light-tools is not a security boundary.** It is a context-engineering layer
over agent-native tools. The server and every command it runs have the full
access of your user account, and `light_bash` is arbitrary same-user code — it
can read the vault files directly whenever your account can. Nothing here
sandboxes an agent, and nothing here should be relied on to contain one.

Read the rest of this file as a description of what the mechanisms actually do,
not as a list of guarantees.

## The vault

Secret values are stored with AES-GCM under a random local 32-byte key, mode
0600 where Unix permissions exist. Values are not accepted as ordinary MCP
arguments and are not returned by vault metadata or HTTP responses; they are
resolved by name into command environments or mode-0600 temporary files. Output
scrubbing replaces exact secret values on a best-effort basis.

What that buys you is protection against *accidental* plaintext storage and
against a value drifting into the model's normal context. It does not protect
against another process running as the same user, root, memory inspection,
backups, or a compromised machine. Encoded, transformed or fragmented
disclosure defeats scrubbing.

If the key is missing beside existing ciphertext, light-tools refuses to
generate a replacement rather than silently starting over.

## Roots

**light-tools is unconfined by default** — every path on the machine is
reachable by `light_file`, `light_bash` and `light_ops` unless you confine it.
Set `allowed_roots` in `config.toml`, or flip the confinement toggle in
`light-tools vault ui`, to pin the tools to a boundary. The config file is
authoritative and the toggle applies only when it is silent, so the UI can
never widen or replace a boundary an operator set in `config.toml`. Both take
effect at the next MCP start.

Confinement does not mean the same thing for every tool, and the difference
matters before you rely on it. `light_file` paths, the local endpoint of an SCP
transfer, and caller-supplied `light_ops` paths are each resolved through the
boundary and refused outside it. `light_bash` is not: only its working
directory is resolved through the boundary, and the command that then runs has
the full filesystem access of your account, as the first line of this file
says. No path check could make it otherwise, so read the boundary as a bound on
what the *tools* address, never as a bound on what a shell command touches.

This default is more permissive than `@modelcontextprotocol/server-filesystem`,
which refuses to start without at least one allowed directory. The reasoning:
that server is typically an agent's only filesystem access, so a boundary there
removes a capability, whereas light-tools sits alongside the agent's own
unconfined file and shell tools. Confining light-tools does not stop an agent
reaching a path — it only sends it back to the tool that has no snapshots, no
locking and no audit trail. Given the first line of this file, a boundary that
merely reroutes an agent is not worth the safety it appears to offer.

If that trade is wrong for your setup, confine it. The mechanism is there and
it is one line.

The file, SCP and operations handlers deny the secrets, snapshot, spill and
telemetry roots at the direct, rename-target, registry-log, directory-list and
recursive-locate seams, **in every posture including unconfined**. The telemetry root is denied so a tool call cannot
fabricate the aggregates the vault UI renders as measured data. These are
path-based checks — defense in depth against accidental escape, not isolation.
Externally created hardlinks are outside the boundary.

## The local UI

`light-tools vault ui` is a foreground loopback server on `127.0.0.1`. A
single-use code printed in the terminal must be exchanged within five minutes
before password setup or login. The Argon2id verifier protects UI login only; it
does not wrap the vault key. Sessions stay server-side with idle and absolute
expiry. Exact Host and mutating Origin checks, JSON-only bounded bodies, no
CORS, and no-store/referrer/framing/MIME/CSP headers constrain the browser
surface.

The UI never reveals a saved value, but a browser extension with page or
localhost access can read one while it is being typed. Use
`light-tools vault set NAME` over stdin when the browser is not trusted.

The password cannot be changed in the browser or recovered. `light-tools vault
ui-reset` removes only `Secrets/ui.json`; the key and ciphertext survive.

## Tool exposure

Every tool registers by default, including `light_bash`, `light_ssh` and
`light_scp`. Starting the server is the whole gesture — there is no second
opt-in for local or remote execution. Withhold a tool by name with
`--disable-tool <name>`, repeatable; a withheld tool is never registered and an
unrecognised name is refused at startup.

Withholding has two sources and they only ever add up: launch arguments, and
zero-byte marker files written by the UI's Settings view. The UI can never
re-enable what launch arguments withhold. Treat the launch arguments in your MCP
client configuration as the boundary they are.

The shell mutation guard has three outcomes: capture-backed, unbacked, and
refused. It applies only to simple invocations of the mutators it models
(`rm`, `unlink`, single-source `mv`, `sed -i`, `gofmt -w`, and
`go fmt`) whose pathname operands it can enumerate.

A protectable surface is snapshotted before anything runs, executes on first
contact, and returns a capture id that restores every pathname in that
snapshot. Explicit non-glob surfaces use a bounded capture entry point: at most
64 MiB of regular-file preimages are retained, with the limit enforced on the
bytes actually read rather than on earlier file-size observations.

A capture is a pre-execution observation, not an atomic filesystem
transaction. It records the pathname state seen before the command starts. An
unrelated process can replace a pathname between capture and execution, so the
capture is not a claim that it contains the exact object the command later
consumed. On POSIX systems, an unquoted glob is pinned to the captured paths to
remove the shell's second expansion of that pattern. On Windows the POSIX pin
is unavailable; the result carries `pinned:false` and the shell expands the
pattern itself, so a pathname created after capture can be affected without
being restorable.

An explicit surface that cannot be protected, has no snapshot vault, or
exceeds the 64 MiB bounded-capture ceiling still runs on first contact. Its
result says `protection:"unbacked"`, gives the reason, and advertises no
capture id or revert.

An unprotectable unquoted glob does not run on first contact. It returns the
complete expanded surface, the reason protection was unavailable, and an
8-hex digest binding that command, working directory, and surface. Only an
identical call carrying that digest runs, labelled
`protection:"unprotected_confirmed"`. A digest naming a different surface is
refused rather than reinterpreted.

Quoted patterns, pipelines, variables, command substitutions, and a program's
own pattern language are outside this guard. It does not classify general
danger or infer effects from scripts.

## Telemetry

Local-only aggregates: per-tool call counts, terse tokens saved, read-dedup
bytes saved, write-payload bytes saved. No network component of any kind —
nothing is transmitted, and there is no crash reporter, symbol upload or update
check. It never records a path, argument, command, hostname or username; the
only identifiers are tool names. Snapshots live in a separate XDG root (0700,
files 0600) and are pruned by retention and a session cap. Opt out with
`DO_NOT_TRACK=1` or a non-empty `LIGHT_NO_TELEMETRY`.

## Scope

light-tools is one component of the Light stack, extracted to stand alone. It
deliberately ships none of the platform's approval, RBAC, fleet-dispatch or
audit machinery. Those omissions are not isolation from the account running the
process — they are simply absent.

Report vulnerabilities privately through GitHub's security-advisory feature.