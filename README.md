# light-tools

A standalone, single-user MCP server that brings bounded file operations and
optional local shell, SSH/SCP, and read-only operations diagnostics to any MCP
client. It is one Go binary, speaks MCP over stdio, requires no database or
daemon, and starts with only `light_file` enabled.

## Three-command setup

Prebuilt binaries include the tagged CGo tree-sitter runtime and Go,
JavaScript, and Python grammars.

```sh
curl -fsSL https://raw.githubusercontent.com/icediceice/light-tools/main/install.sh | sh
light-tools init
claude mcp add light-tools -- light-tools
```

The middle command initializes private XDG state directories and prints the
exact `claude mcp add` command; it is optional because the server initializes
those directories on first run.

To build from source, install Go and a C toolchain:

```sh
go install -tags treesitter github.com/icediceice/light-tools/cmd/light-tools@latest
```

## Capability profiles

The default invocation registers only `light_file`. Broader capabilities are
explicit:

```sh
light-tools --enable-shell
light-tools --enable-remote
light-tools --enable-ops
light-tools --enable-shell --enable-remote --enable-ops
```

- `light_file`: `read`, `list`, `symbol`, `outline`, `locate`,
  `diff`, `identity`, `write`, `edit`, `sed`, `rename`, `rewrite`,
  `vault_list`, and `vault_restore`. Mutations share root confinement,
  expected-SHA conflict detection, mode-preserving atomic replacement, and a
  three-version pre-mutation snapshot ring. A single image read returns an MCP
  image block up to 9 MiB.
- `light_bash` (opt-in): synchronous commands with cwd confinement, timeout
  and process-group cleanup, `auto|head|tail|grep|read_block` output modes,
  opaque compressed spills, and secret references.
- `light_ssh` / `light_scp` (opt-in): named TOML profiles, proxy jumps,
  port overrides, strict host-key checking, `SSH_AUTH_SOCK` inheritance, and
  one retry only after a timeout. Key paths are referenced, never copied.
- `light_ops` (opt-in): source-qualified systemd/PM2/Docker discovery, local
  probes, and bounded log inspection. It cannot start, stop, or restart
  services.

Builds without `-tags treesitter` remain functional; symbol extraction
returns a graceful unavailable response and outlines use fixed-size chunks.

## Configuration

No config file is required. The default allowed root is the process working
directory. Optional overrides live at
`$XDG_CONFIG_HOME/light-tools/config.toml`:

```toml
allowed_roots = ["/work/project"]

[remote.production]
host = "example.internal"
user = "deploy"
port = 22
proxy_jump = "bastion.internal"
# This path is passed to ssh; light-tools never reads or copies the key.
key_path = "/home/me/.ssh/id_ed25519"
```

State stores use separate XDG roots for configuration, encrypted secrets,
snapshots, and runtime spills. Parent directories are mode 0700 and secret
material is mode 0600 where the platform supports Unix permissions.

## Sealed mutation payloads

Large or multi-file mutations can use `payload` format 1. Headers are bare
`@key value` lines. Bodies begin after `@content`, `@new_string`,
`@find`, or `@replace` and end only on a line exactly equal to
`<<LF-END>>`. Every body in a batch needs its own seal. Use `@until TOKEN`
when the body itself contains that exact line.

```text
@file /work/project/a.txt
@verb sed
@find
old
<<LF-END>>
@replace
new
<<LF-END>>
```

## Secret vault

Secret writes are CLI-only and values are read from stdin, not argv:

```sh
printf '%s' "$TOKEN" | light-tools vault set api-token
light-tools vault list
light-tools vault rm api-token
```

MCP calls refer to names through `env_refs` or `file_refs`. The server does
not expose a value-reading or value-listing MCP method. SSH private keys are
deliberately excluded; use an agent or a referenced key path.

See [SECURITY.md](SECURITY.md) for the exact security claim and
[PORTING.md](PORTING.md) for stable edge-case semantics.