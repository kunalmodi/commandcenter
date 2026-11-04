---
id: 011
title: Point Claude configs at the new servers; retire the old combined ask_codex/ask_grok server and old imsg-mcp
status: todo
area: setup
created: 2026-11-04
---

- 2026-11-04: codex/grok: claude mcp add grok -- ~/.local/bin/grok-mcp (same for codex). imsg: new binary is a drop-in on the same port and token. If grok used the old server's settings file, copy GROK_* lines to ~/.config/commandcenter/grok.env.
- 2026-11-04: Installed binaries to ~/.local/bin. Issued imsg client 'claude-desktop' (read only) and put its token in claude_desktop_config.json (backup: claude_desktop_config.json.bak-commandcenter-20261104). Added grok/codex to Claude desktop and to Claude Code user scope. writersroom kept as asked. Remaining: Ctrl-C the old server (Projects/tasks/imsg-mcp) in its FDA terminal, run ~/.local/bin/imsg-mcp there, then restart Claude desktop.
