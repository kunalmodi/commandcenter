# codex-mcp

Ask OpenAI's models for an independent answer, through the
[Codex CLI](https://github.com/openai/codex) and whatever sign-in it already has
(a ChatGPT account works; no API key needed).

## Tools

| Tool | Purpose |
| --- | --- |
| `ask_codex(prompt, model?, effort?)` | Send a prompt, get the reply |
| `get_result(job_id, wait_seconds?)` | Collect a reply that took longer than ~40s |
| `codex_status()` | Is the CLI installed, which defaults apply |

Each call runs

```
codex exec --skip-git-repo-check --ephemeral --sandbox read-only [--model M] [-c model_reasoning_effort=E] -o <file> -
```

in a fresh, empty temporary folder, so Codex sees only the prompt, never your files.

## Slow replies

`ask_codex` waits up to 40 seconds; if Codex is still working it returns a `job_id`
for `get_result`. Jobs are kept in memory for an hour.

## Settings

In the environment or `~/.config/commandcenter/codex.env`:

| Var | Purpose |
| --- | --- |
| `CODEX_BIN` | Path to the codex binary |
| `CODEX_MODEL` | Default model (the CLI's default if unset) |
| `CODEX_EFFORT` | Default effort: `minimal`, `low`, `medium`, `high`, `xhigh` |
