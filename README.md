# commandcenter

Small, dependency-free [MCP](https://modelcontextprotocol.io) servers for wiring
agents into a Mac. Each is a single Go binary using only the standard library.

| Server | What it does | Transport |
| --- | --- | --- |
| [`imsg-mcp`](cmd/imsg-mcp) | Read iMessage/SMS (one-time codes hidden), optionally send to an allowlist; a scoped token per agent | HTTP on localhost, or stdio |
| [`grok-mcp`](cmd/grok-mcp) | Ask xAI's Grok for a second opinion (CLI sign-in or API key) | stdio |
| [`codex-mcp`](cmd/codex-mcp) | Ask OpenAI's models via the Codex CLI (uses its sign-in) | stdio |

## Build

Needs Go 1.21+ (`brew install go`).

```bash
make build      # binaries in ./build
make test       # vet + unit tests
make install    # copy binaries to ~/.local/bin (override with PREFIX=...)
```

## Settings and secrets

Every server reads optional settings from `~/.config/commandcenter/<server>.env`
(override the folder with `COMMANDCENTER_CONFIG_DIR`). Environment variables win
over the file. Examples are in [`config/`](config). **Keep tokens, API keys and the
iMessage allowlist in that folder, never in this repo**; `.gitignore` blocks `*.env`
as a backstop.

## Register with Claude

Claude Code (stdio servers):

```bash
claude mcp add grok  -- ~/.local/bin/grok-mcp
claude mcp add codex -- ~/.local/bin/codex-mcp
```

Claude desktop (`claude_desktop_config.json`, Settings → Developer → Edit Config):

```json
{
  "mcpServers": {
    "grok":  { "command": "/Users/YOU/.local/bin/grok-mcp" },
    "codex": { "command": "/Users/YOU/.local/bin/codex-mcp" }
  }
}
```

`imsg-mcp` is different because of macOS Full Disk Access; see its
[README](cmd/imsg-mcp/README.md).

## Layout

```
cmd/<server>/      one folder per server (main package + README)
internal/mcp       JSON-RPC/MCP over stdio and HTTP
internal/jobs      background jobs + get_result for slow model calls
internal/env       settings file loading, PATH widening for GUI-launched apps
internal/proc      running external commands with timeouts
config/            example settings files
tasks/             the project's task list (see bin/task)
```

## Tasks

Work is tracked as one Markdown file per task in [`tasks/`](tasks):

```bash
bin/task                   # open tasks
bin/task add "Title" imsg  # new task in area "imsg"
bin/task start 7 / done 7 / note 7 "..."
```
