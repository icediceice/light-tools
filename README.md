# light-tools

A standalone, single-operator MCP server that ports the useful local behavior of
Light's file, shell, SSH/SCP, and operations tools without its fleet control
plane. One Go binary, MCP over stdio, no database and no daemon.

**Every tool is registered by default.** Withhold one by name with
`--disable-tool`.

## Install

```sh
npm install --global @factor-i-o/light-tools
```

Node 18.17+ and npm 10+. The native binary arrives as an exact-version optional
dependency, so installs work with lifecycle scripts disabled and through a
registry mirror; nothing is fetched from GitHub at install time. Linux packages
target glibc, and Alpine/musl is rejected during installation rather than
failing later with a dynamic-loader `ENOENT`.

## Register it with any MCP client

```json
{
  "mcpServers": {
    "light-tools": {
      "command": "light-tools"
    }
  }
}
```

On Windows, npm creates `light-tools.cmd`. A client that starts processes
without a shell must go through Command Prompt:

```json
{
  "command": "cmd",
  "args": ["/d", "/s", "/c", "light-tools"]
}
```

`light-tools init` writes the configuration a known client expects:

| Client | What `init` does |
| --- | --- |
| `claude` (default) | Prints the exact `claude mcp add` line |
| `antigravity` | Merges `mcpServers["light-tools"]` into Antigravity's config and writes the suppression skill |
| `print` | Prints the Antigravity config, skill, and permission block; writes nothing |

`--dry-run` prints instead of writing, and `--client print` never writes.
`--workspace <dir>` targets `<dir>/.agents/` instead of the global location.
`init` creates private XDG state directories and is optional: the server
creates them on first run.

## Point your agent at it

Hand your agent [AGENT-SETUP.md](AGENT-SETUP.md). It is a self-contained
prompt — the agent reads it and sets itself up: install, registration, the tool
table, and the instruction to stop reaching for its own native file and terminal
tools. That file ships inside the npm package.

## The tools

| Tool | Use it for |
| --- | --- |
| `light_file` | Read, list, search, slice symbols, diff, write, edit, rename, and undo file changes |
| `light_bash` | Run a local command, sync or async, with bounded output and secret references |
| `light_ssh` | Run a command on a remote host through a named profile |
| `light_scp` | Copy files to or from a remote host through a named profile |
| `light_ops` | Read-only: discover services, probe ports, read and correlate logs |

## Withholding a tool

```sh
light-tools --disable-tool light_bash
light-tools --disable-tool light_ssh --disable-tool light_scp
```

Repeatable, and takes one of `light_bash`, `light_file`, `light_ops`,
`light_scp`, `light_ssh`. An unknown name is refused at startup rather than
ignored. `init` carries the same flags into the launch arguments it writes:

```sh
light-tools init --client claude --disable-tool light_ssh --disable-tool light_scp
```

A withheld tool is never registered, so the model cannot call it at all.

The retired `--enable-shell`, `--enable-remote`, and `--enable-ops` flags still
parse so existing configurations keep launching, but they gate nothing and warn
on stderr.

## Platforms

| OS | amd64 | arm64 | Symbol extraction |
| --- | --- | --- | --- |
| Linux | native | native | tree-sitter |
| macOS | native | native | tree-sitter |
| Windows | native | native | tree-sitter on amd64; graceful no-symbol fallback on arm64 |

Windows ARM64 is deliberately built without CGo or the `treesitter` tag. All
five tools remain available; only `light_file symbol`/outline extraction
degrades to its documented no-symbol response.

Build from source with Go 1.23+ and a C toolchain:

```sh
go install -tags treesitter github.com/icediceice/light-tools/cmd/light-tools@latest
```

### Native archive fallback

The checksum-verifying POSIX and PowerShell installers fetch their release
assets anonymously over HTTPS, so they are supported once this repository is
public. While it remains private these commands cannot authenticate and will
fail before checksum verification; install from npm above instead, which carries
the same checksum-bound binaries and needs no GitHub access:

```sh
curl -fsSL https://raw.githubusercontent.com/icediceice/light-tools/main/install.sh | sh
```

```powershell
Invoke-WebRequest https://raw.githubusercontent.com/icediceice/light-tools/main/install.ps1 -OutFile install.ps1
./install.ps1
```

Pin or relocate with `-Version` / `-Destination` (PowerShell) or
`LIGHT_TOOLS_VERSION` / `LIGHT_TOOLS_INSTALL_DIR` (POSIX). Both installers
require an exact asset entry in `checksums.txt`.

## More

- [AGENT-SETUP.md](AGENT-SETUP.md) — the agent onboarding prompt
- [SECURITY.md](SECURITY.md) — the exact security claim
- [docs/REFERENCE.md](docs/REFERENCE.md) — per-verb semantics, configuration, terse output, symbol extraction
- [docs/PORTING.md](docs/PORTING.md) — stable edge-case semantics
- [docs/PARITY-REPORT.md](docs/PARITY-REPORT.md) — what was kept and dropped versus Light
- [RELEASING.md](RELEASING.md) — release process