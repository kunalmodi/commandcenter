---
id: 008
title: Build on the Mac (make build) and smoke test against real imsg, codex and grok
status: todo
area: setup
created: 2026-11-04
---

- 2026-11-04: Built with go1.26.4. grok-mcp and codex-mcp smoke-tested over stdio: status ok, ask_* returned 'pong'. imsg-mcp tested end-to-end over HTTP with a stub imsg (scoped tokens, chat limits, OTP hiding, revocation). Real chat.db not tested: Claude's shell has no Full Disk Access. Remaining: start ~/.local/bin/imsg-mcp in the FDA terminal and call imsg_status from Claude desktop.
