---
id: 010
title: Tune the one-time-code filter against real messages
status: todo
area: imsg
created: 2026-11-04
---

- 2026-11-04: Start the server with IMSG_OTP_FILTER=redact, call search_messages / read_history on chats with known code senders, and look for leaks (codes shown) or false positives (normal texts hidden). Add any misses as cases in otp_test.go.
- 2026-11-04: Added 'imsg-mcp otp-audit' (digits masked) so this can be done without exposing codes. Run it in the FDA terminal, check HIDDEN for false positives and CHECK for leaks, add misses to otp_test.go.
