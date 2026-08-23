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

The file, SCP and operations handlers deny the secrets, snapshot, spill and
telemetry roots at the direct, rename-target, registry-log, directory-list and
recursive-locate seams. The telemetry root is denied so a tool call cannot
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

The shell wildcard guard runs in two lanes, decided by whether the expanded
surface can be backed up.

A surface it can fully protect is snapshotted before anything runs, executes on
first contact, and returns a capture id that restores every path it touched.
The executed command is pinned to the captured paths, so the shell cannot
re-expand the pattern onto a different set between the snapshot and the effect.

A surface it cannot protect — a directory, an invalid-UTF-8 name, a
multi-source `mv`, or a missing snapshot vault — does not run at all. It returns
the fully expanded surface, the reason protection was unavailable, and an 8-hex
digest binding that command, that working directory and those paths together.
Only an identical call carrying that digest runs, and it runs unprotected: no
snapshot is taken, and the shell expands the pattern itself rather than
receiving pinned paths. A digest naming a different surface is refused rather
than reinterpreted.

The guard sees only an unquoted, lexical filename glob on a mutator it models.
It does not classify danger, inspect scripts, expand variables, or understand a
program's own pattern syntax; a wildcard from `$VAR`, command substitution or
`find` is outside its view.

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