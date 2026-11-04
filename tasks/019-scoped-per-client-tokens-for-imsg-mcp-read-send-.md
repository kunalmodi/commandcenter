---
id: 019
title: Scoped per-client tokens for imsg-mcp (read/send/chats)
status: done
area: imsg
created: 2026-11-04
---

- 2026-11-04: imsg-mcp token add/list/remove; hashed tokens in imsg-clients.txt (0600, re-read per request); server refuses to start with no clients; IMSG_TOKEN kept as a legacy full-access client; README documents the threat model, incl. that a sandboxed agent gets the strongest token it can read.
