# Security

light-tools makes one narrow claim: secret values stored through its CLI or local
vault UI are kept out of the model's normal context. Values are never accepted
as ordinary MCP arguments, never returned by vault metadata or HTTP responses,
and are resolved only by name into command environments or mode-0600 temporary
files. Best-effort output scrubbing replaces exact secret values before normal
results or spills are shaped.

The vault uses AES-GCM with a random local 32-byte key. The key, ciphertext, and
UI-password verifier are stored beneath the separate secrets state root with
mode 0600 where Unix permissions exist. The file, SCP, and operations handlers
deny the secrets, snapshot, spill, and telemetry roots at direct, rename-target,
registry-log, directory-list, and recursive-locate seams. The telemetry root is
denied for scorecard integrity too: a writable telemetry root would let a tool
call fabricate the aggregate snapshots the vault UI renders as measured data.
Externally created hardlinks are outside this path-based boundary.

Encryption protects against accidental plaintext storage and model-context
exposure. It does not protect against another process running as the same user,
root, memory inspection, backups, or a compromised machine. If the key is
missing beside existing ciphertext, light-tools refuses to generate a
replacement. Vault mutations use a local-filesystem lock across load through
atomic save; network filesystems are unsupported.

`light-tools vault ui` is a separate foreground loopback server. It opens only a
bare `127.0.0.1` URL. A random single-use code printed in the terminal must be
exchanged within five minutes before password setup or login. The Argon2id
verifier protects UI login only; it does not wrap the vault key. Bearer sessions
stay server-side with idle and absolute expiry, while the browser keeps its
token in tab-scoped `sessionStorage` so refresh works and closing the tab drops
it. Exact Host and mutating Origin checks, JSON-only bounded bodies, no CORS,
and no-store, referrer, framing, MIME, and content-security headers constrain
the browser surface. The UI password cannot be changed in the browser or
recovered. If it is forgotten, `light-tools vault ui-reset` removes only
`Secrets/ui.json`; the vault key and ciphertext remain intact, and the next UI
launch can choose a new password.

The UI never reveals a saved value, but a browser extension with page or
localhost access may inspect a value while it is being typed. Use stdin-based
`light-tools vault set NAME` when the browser environment is not trusted.

Temporary `file_refs`, SSH `key_ref`, and `cert_ref` files are created with
mode 0600 and best-effort overwritten and removed after the child process exits.
Filesystem journaling, snapshots, backups, and flash translation layers mean
overwrite is not a forensic-erasure guarantee.

This is not an OS sandbox. The server and every shell or remote command run with
the access of your user account. `light_bash` is arbitrary same-user code and
can read the vault files directly when the account can. Output scrubbing cannot
prevent transformed, encoded, fragmented, or side-channel disclosure. Securing
the machine, account, SSH agent, known-hosts file, allowed roots, browser,
configuration, and commands remains the operator's job.

**Every tool is registered by default, including `light_bash`, `light_ssh`, and
`light_scp`.** Starting the server is the whole gesture: local shell execution
and remote execution are exposed without a second opt-in. That decision now
lives at launch time. Withhold a tool by name with `--disable-tool <name>`,
repeatable; a withheld tool is never registered, so it cannot be called at all,
and an unrecognised name is refused at startup rather than ignored. Treat the
launch arguments in your MCP client configuration as the boundary they now are.

Root checks are defense in depth against accidental path escape, not a
substitute for OS isolation.

The shell wildcard guard prevents a first lexical filename-glob request
from executing and requires one identical retry. It does not classify command
danger, inspect scripts, expand variables, or understand a program's internal
pattern syntax. A wildcard introduced by `$VAR`, command substitution, or a
tool such as `find` is outside its view. The receipt is process-local, expires,
and is not an authorization token. Explicit filenames are intentionally not
fenced.

Savings telemetry is local-only aggregates: per-tool call counts, terse tokens
saved, read-dedup bytes saved, and write-payload bytes saved. It has no network
component of any kind — nothing is transmitted, and no crash reporter, symbol
upload, or update check exists. It never records a path, argument, command,
hostname, or username; the only identifiers are tool names. Snapshots live in a
separate XDG root (mode 0700, files 0600) as cumulative per-session JSON and are
pruned by retention and a session cap. The vault UI reads them read-only behind
the same pairing-plus-password gate as the vault. Opt out entirely with
`DO_NOT_TRACK=1` or a non-empty `LIGHT_NO_TELEMETRY`; with either set, no
telemetry code records or writes anything.

Tool-withholding markers are zero-byte files under the config root named for the
tool. They only ever ADD withholding, unioned with the launch flags, so the UI
can never re-enable what launch arguments withhold — see
[docs/REFERENCE.md](docs/REFERENCE.md) for the exact union rule and metric
definitions.

This is a single-user server. It does not include Light's EDCR approval,
multi-file surface confirmation, RBAC, fleet dispatch, or audit machinery.
Those omissions must not be interpreted as isolation from the account running
the process.

Report vulnerabilities privately through GitHub's security-advisory feature.
Do not include live credentials, private keys, or sensitive logs.