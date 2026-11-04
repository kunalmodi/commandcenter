# grok-mcp

Ask xAI's Grok for an independent answer from a different model family.

## Tools

| Tool | Purpose |
| --- | --- |
| `ask_grok(prompt, model?, effort?)` | Send a prompt, get Grok's reply |
| `get_result(job_id, wait_seconds?)` | Collect a reply that took longer than ~40s |
| `grok_status()` | Which backend is ready; never shows the key |

## Backends

- **cli**: xAI's `grok` command with its existing sign-in (`grok login`). No key needed.
  Runs in an empty temporary folder with web search, memory, subagents and planning
  turned off (where the installed CLI supports those flags; it checks `grok --help`
  first), so Grok sees only the prompt.
- **api**: `POST /chat/completions` on xAI's API with `GROK_API_KEY`.

With `GROK_BACKEND` unset it uses `cli` when the grok command is found, else `api`.

## Slow replies

Some MCP clients drop a tool call after about 60 seconds. `ask_grok` waits up to
40 seconds; if Grok is still thinking it returns a `job_id`, and `get_result`
collects the answer. Jobs are kept in memory for an hour.

## Settings

In the environment or `~/.config/commandcenter/grok.env`
(see [`config/grok.env.example`](../../config/grok.env.example)):

| Var | Purpose |
| --- | --- |
| `GROK_BACKEND` | `cli` or `api` |
| `GROK_BIN` | Path to the grok binary |
| `GROK_API_KEY` | API key for the `api` backend |
| `GROK_API_BASE` | API base URL (default `https://api.x.ai/v1`) |
| `GROK_MODEL` | Default model (CLI default, or `grok-4.7` for the API) |
| `GROK_EFFORT` | Default effort: `low`, `medium`, `high`, `xhigh` |
