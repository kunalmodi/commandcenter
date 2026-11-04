# Working in this repo

- Go, standard library only. No third-party modules without a good reason.
- `make test` must pass before calling anything done (`go vet` + unit tests).
- Each server lives in `cmd/<name>-mcp/` with its own README; shared code goes in `internal/`.
- Secrets and personal data (tokens, API keys, phone numbers, the iMessage allowlist,
  absolute paths under a home folder) never go in the repo. They live in
  `~/.config/commandcenter/`. Examples go in `config/*.example*` with placeholder values.
- Nothing writing-room specific belongs here; this repo is the shared, general servers.

## Tasks

Track work with `bin/task` (one Markdown file per task in `tasks/`):

- `bin/task` lists open tasks; `bin/task list all` shows everything.
- When you find follow-up work, `bin/task add "Title" <area>` rather than leaving a TODO comment.
- Mark progress with `bin/task start <id>` / `bin/task done <id>`, and record decisions
  or findings with `bin/task note <id> "..."`.
