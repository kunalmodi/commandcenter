# imsg-mcp

Exposes the [`imsg`](https://imsg.sh) CLI to an MCP client so an agent can read your
iMessage/SMS history, and optionally text a short allowlist of people.

## Tools

| Tool | Purpose |
| --- | --- |
| `list_chats` | Recent conversations (id, name, service, last activity) |
| `chat_detail` | Identifiers and participants for one chat |
| `read_history` | Messages in a chat, with date and sender filters |
| `search_messages` | Find messages containing a substring across recent chats |
| `imsg_status` | Current settings, what this client may do, and who it may text |
| `send_message` | Text one allowlisted recipient. **Only listed when sending is turned on** |

## One-time codes are hidden

Messages that look like they carry a login or verification code are hidden from
every read tool and never match a search, so an agent cannot read or forward them.
The check is a heuristic that leans toward hiding: a code-shaped token
(`482913`, `G-583920`, `093 214`, `A7K9Q2`) plus a strong signal ("verification",
"one-time", "do not share", "your code is ...", or an SMS short-code sender).

`IMSG_OTP_FILTER` sets the behaviour:

- `redact` (default): the message stays but its text becomes a placeholder and it gets
  `"otp_hidden": true`
- `drop`: the message is left out (chat previews are still only redacted)
- `off`: no filtering

To check the filter against your own messages without showing any codes, run
`imsg-mcp otp-audit` in your Full Disk Access terminal. It masks every digit and
lists what it hides (look for false positives) and the code-like messages it lets
through (look for leaks).

## Sending (off by default)

`send_message` exists only when **both** are true at startup:

1. `IMSG_ALLOW_SEND=1`
2. `~/.config/commandcenter/imsg-allowlist.txt` lists at least one recipient

```
# phone number or email first, then an optional label
+1 (555) 010-0000   Mom
someone@example.com Sam
```

Guards on every send:

- The recipient must be on the allowlist (matched by normalized number or email, or
  by exact label). The file is re-read on each send, so removing someone takes effect
  immediately. The agent has no way to add people.
- One recipient per message, text only, at most 2,000 characters.
- Messages that themselves look like a one-time code are refused.
- At most `IMSG_SEND_MAX_PER_HOUR` sends (default 10).
- Every attempt, sent or refused, is appended to
  `~/.config/commandcenter/logs/imsg-send.log`.
- `IMSG_SEND_DRY_RUN=1` logs and reports without sending; use it to try things out.

Under the hood this runs `imsg send --to <handle> --text <text>` (plus `--service`
if `IMSG_SEND_SERVICE` is set). Sending through Messages may need macOS Automation
permission for your terminal the first time.

## Why HTTP, and Full Disk Access

macOS grants Full Disk Access (FDA) to the *responsible process*. Start this server
from a terminal that has FDA and it inherits read access to
`~/Library/Messages/chat.db`. Claude then only makes HTTP calls to `127.0.0.1` and
needs no disk permission of its own. Close the terminal and access ends.

`-stdio` is available for clients that launch servers themselves, but then the
*client* (e.g. Claude) needs FDA, which is a much bigger grant.

## Who can connect: one token per agent, each with its own scopes

Every HTTP caller sends its own bearer token, and each token says what that
caller may do. The server enforces it, so it holds however the caller connects
(an MCP client, `curl`, or a script an agent writes):

| Scope | Tools |
| --- | --- |
| `read` | `list_chats`, `chat_detail`, `read_history`, `search_messages` |
| `send` | `send_message` (sending must also be on, and the recipient on the allowlist) |
| `chats=ID,ID` | limits every read tool to those chats |

`imsg_status` is always there and tells the caller what it may do. Tools a token
can't use are left out of its tool list, and calling them fails as if they
didn't exist.

```bash
imsg-mcp token add claude-desktop -scopes read,send
imsg-mcp token add sandbox-agent  -scopes read -chats 12,40
imsg-mcp token list
imsg-mcp token remove sandbox-agent       # takes effect on the next request
```

`token add` prints the token once. Only its SHA-256 hash goes into
`~/.config/commandcenter/imsg-clients.txt` (mode 600; the server refuses the file
if other users can write it). If that file has no clients and `IMSG_TOKEN` isn't
set, the server won't start, because there would be no safe way to connect.

`IMSG_TOKEN` still works as a single full-access client, so older setups keep
running. Switch to per-agent tokens when you can.

### What the tokens protect, and what they don't

- **Without a token, any process running as you can read every message.** That
  means other apps, a browser extension's helper, or an agent's shell. Port
  binding is the only check on loopback, and it isn't access control. The token
  is what makes this a server *you* hand out access to.
- **The token is the agent's identity, so keep each one with its agent.** An
  agent gets the scopes of the strongest token it can *read*. Claude Code stores
  `--header` values in `~/.claude.json`, and Claude desktop stores them in
  `claude_desktop_config.json`. A sandboxed agent that can read either file can
  use that token. To actually limit a sandboxed agent:
  - give it its own read-only token, and leave any `send` token out of every
    config file it can read, or
  - keep it from reaching `127.0.0.1:8765` at all (most sandboxes block local
    network by default), and expose imsg only through the client you trust.
- **Some limits apply to every token, even full-access ones:** one-time codes stay
  hidden, sends are allowlisted and rate-limited, and every send attempt is
  audit-logged. A leaked `send` token can text only your allowlist, at most
  `IMSG_SEND_MAX_PER_HOUR` times, and the log shows each attempt.

## Run

```bash
brew install steipete/tap/imsg          # and give your terminal Full Disk Access
cp config/imsg.env.example ~/.config/commandcenter/imsg.env
build/imsg-mcp token add claude-desktop -scopes read,send
build/imsg-mcp
```

It refuses to listen on anything but loopback.

`-stdio` takes `-scopes` and `-chats` the same way (default `-scopes read`). The
process that launches it is the client.

## Register with Claude

Claude Code:

```bash
claude mcp add --transport http imsg http://127.0.0.1:8765/mcp \
  --header "Authorization: Bearer <token from imsg-mcp token add>"
```

Claude desktop, through the `mcp-remote` bridge:

```json
{
  "mcpServers": {
    "imsg": {
      "command": "npx",
      "args": ["-y", "mcp-remote", "http://127.0.0.1:8765/mcp",
               "--header", "Authorization: Bearer YOUR_IMSG_TOKEN"]
    }
  }
}
```

## Settings

| Var | Default | Purpose |
| --- | --- | --- |
| `IMSG_ADDR` | `127.0.0.1:8765` | Listen address (loopback only) |
| `IMSG_CLIENTS` | `~/.config/commandcenter/imsg-clients.txt` | Scoped client tokens (hashed) |
| `IMSG_TOKEN` | none | Legacy single full-access token |
| `IMSG_BIN` | `imsg` | Path to the imsg binary |
| `IMSG_DB` | none | Read a chat.db snapshot instead of the live one |
| `IMSG_OTP_FILTER` | `redact` | `redact`, `drop` or `off` |
| `IMSG_ALLOW_SEND` | off | `1` to enable `send_message` |
| `IMSG_SEND_ALLOWLIST` | `~/.config/commandcenter/imsg-allowlist.txt` | Who may be texted |
| `IMSG_SEND_MAX_PER_HOUR` | `10` | Send rate cap |
| `IMSG_SEND_DRY_RUN` | off | `1` to log without sending |
| `IMSG_SEND_SERVICE` | none | `imessage`, `sms` or `auto` |
| `IMSG_SEND_LOG` | `~/.config/commandcenter/logs/imsg-send.log` | Audit log |
