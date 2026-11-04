---
id: 006
title: Allowlisted send_message for imsg
status: done
area: imsg
created: 2026-11-04
---

- 2026-11-04: Off unless IMSG_ALLOW_SEND=1 and allowlist non-empty. Allowlist re-read per send, label or number match, no OTP forwarding, 10/hour cap, audit log, dry-run mode.
