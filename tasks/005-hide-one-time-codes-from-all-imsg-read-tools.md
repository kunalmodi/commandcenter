---
id: 005
title: Hide one-time codes from all imsg read tools
status: done
area: imsg
created: 2026-11-04
---

- 2026-11-04: Heuristic: code-shaped token + strong keyword / 'your code is' phrase / short-code sender. Unit tests in cmd/imsg-mcp/otp_test.go. Modes: redact (default), drop, off.
