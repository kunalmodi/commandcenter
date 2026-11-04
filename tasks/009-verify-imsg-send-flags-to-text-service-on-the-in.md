---
id: 009
title: Verify imsg send flags (--to, --text, --service) on the installed imsg version
status: done
area: imsg
created: 2026-11-04
---

- 2026-11-04: Not tested against real imsg: the code assumes 'imsg send --to <handle> --text <text> [--service ...]'. Run 'imsg send --help' and adjust imsgSend() in cmd/imsg-mcp/send.go if needed. Try with IMSG_SEND_DRY_RUN=1 first, then a real send to yourself.
- 2026-11-04: Verified against imsg 0.11.1: 'imsg send --to/--text/--service' matches imsgSend(). It also offers --chat-id (useful for 017) and --no-sms-fallback.
