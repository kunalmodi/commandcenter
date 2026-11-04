---
id: 002
title: Split grok out of the old combined server into grok-mcp
status: done
area: grok
created: 2026-11-04
---

- 2026-11-04: Native Go now: no grok.sh, curl or jq. CLI flags still detected from grok --help. GROK_REASONING_EFFORT still accepted as an alias for GROK_EFFORT.
- 2026-11-04: Settings file moved from the old server's settings file to ~/.config/commandcenter/grok.env.
