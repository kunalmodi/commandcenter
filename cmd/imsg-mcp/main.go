// imsg-mcp is a small, standard-library-only MCP server that exposes the
// imsg CLI (https://imsg.sh) so an agent can read your iMessage/SMS history,
// and optionally send to a short allowlist of people.
//
// Transport: HTTP on localhost by default (see README for why: it keeps Full
// Disk Access on a process you start deliberately). Pass -stdio to speak MCP
// over stdin/stdout instead, for clients that launch the server themselves.
//
// Read tools: list_chats, chat_detail, read_history, search_messages.
// Messages that look like one-time codes are hidden from all of them
// (IMSG_OTP_FILTER, see otp.go).
//
// Send tool: send_message, only when IMSG_ALLOW_SEND=1 and the allowlist has
// recipients (see send.go).
//
// Settings (environment, or ~/.config/commandcenter/imsg.env):
//
//	IMSG_BIN     path to the imsg binary (default: "imsg" on PATH)
//	IMSG_DB      optional chat.db snapshot to read instead of the live one
//	IMSG_ADDR    HTTP listen address (default 127.0.0.1:8765; keep it on loopback)
//	IMSG_CLIENTS clients file of scoped, hashed tokens (see clients.go)
//	IMSG_TOKEN   legacy single full-access token
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/kunalmodi/commandcenter/internal/env"
	"github.com/kunalmodi/commandcenter/internal/mcp"
	"github.com/kunalmodi/commandcenter/internal/proc"
)

const version = "0.4.0"

func imsgBin() string {
	if b := os.Getenv("IMSG_BIN"); b != "" {
		return b
	}
	return "imsg"
}

// runImsg runs a read command and parses its newline-delimited JSON.
func runImsg(ctx context.Context, args ...string) ([]map[string]any, error) {
	if db := os.Getenv("IMSG_DB"); db != "" {
		args = append(args, "--db", db)
	}
	res, err := proc.Run(ctx, proc.Cmd{Name: imsgBin(), Args: args, Timeout: 60 * time.Second})
	if err != nil {
		return nil, fmt.Errorf("imsg %s failed: %v", args[0], err)
	}
	return parseNDJSON(res.Stdout)
}

func parseNDJSON(s string) ([]map[string]any, error) {
	rows := []map[string]any{}
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var obj map[string]any
		if err := json.Unmarshal([]byte(line), &obj); err != nil {
			return nil, fmt.Errorf("bad JSON line from imsg: %v", err)
		}
		rows = append(rows, obj)
	}
	return rows, nil
}

func toJSON(v any) (string, error) {
	b, err := json.Marshal(v)
	return string(b), err
}

type app struct {
	otp    otpMode
	sender *sender
}

// recentChats lists up to limit recent chats the caller may read. A caller
// limited to some chats gets those that are among the most recent 500.
func recentChats(ctx context.Context, limit int) ([]map[string]any, error) {
	c := clientFrom(ctx)
	if !c.has(scopeRead) {
		return nil, errors.New("this client may not read messages")
	}
	fetch := limit
	if c.chats != nil && fetch < 500 {
		fetch = 500
	}
	rows, err := runImsg(ctx, "chats", "--json", "--limit", strconv.Itoa(fetch))
	if err != nil || c.chats == nil {
		return rows, err
	}
	out := []map[string]any{}
	for _, r := range rows {
		if id, ok := r["id"].(float64); ok && c.canReadChat(int(id)) && len(out) < limit {
			out = append(out, r)
		}
	}
	return out, nil
}

func checkChat(ctx context.Context, id int) error {
	if !clientFrom(ctx).canReadChat(id) {
		return fmt.Errorf("chat %d is not available to this client", id)
	}
	return nil
}

func (a *app) listChats(ctx context.Context, args map[string]any) (string, error) {
	rows, err := recentChats(ctx, mcp.Int(args, "limit", 20))
	if err != nil {
		return "", err
	}
	// Chats are never dropped, only their previews redacted.
	mode := a.otp
	if mode == otpDrop {
		mode = otpRedact
	}
	return toJSON(filterRows(mode, rows))
}

func (a *app) chatDetail(ctx context.Context, args map[string]any) (string, error) {
	id := mcp.Int(args, "chat_id", 0)
	if id == 0 {
		return "", errors.New("chat_id is required")
	}
	if err := checkChat(ctx, id); err != nil {
		return "", err
	}
	rows, err := runImsg(ctx, "group", "--chat-id", strconv.Itoa(id), "--json")
	if err != nil {
		return "", err
	}
	return toJSON(rows)
}

func (a *app) readHistory(ctx context.Context, args map[string]any) (string, error) {
	id := mcp.Int(args, "chat_id", 0)
	if id == 0 {
		return "", errors.New("chat_id is required")
	}
	if err := checkChat(ctx, id); err != nil {
		return "", err
	}
	cmd := []string{"history", "--chat-id", strconv.Itoa(id), "--json", "--limit", strconv.Itoa(mcp.Int(args, "limit", 50))}
	for _, k := range []string{"start", "end", "participants"} {
		if v := mcp.Str(args, k); v != "" {
			cmd = append(cmd, "--"+k, v)
		}
	}
	if mcp.Bool(args, "attachments") {
		cmd = append(cmd, "--attachments")
	}
	rows, err := runImsg(ctx, cmd...)
	if err != nil {
		return "", err
	}
	return toJSON(filterRows(a.otp, rows))
}

func (a *app) searchMessages(ctx context.Context, args map[string]any) (string, error) {
	query := strings.ToLower(strings.TrimSpace(mcp.Str(args, "query")))
	if query == "" {
		return "", errors.New("query is required")
	}
	chats, err := recentChats(ctx, mcp.Int(args, "max_chats", 20))
	if err != nil {
		return "", err
	}
	perChat := strconv.Itoa(mcp.Int(args, "per_chat_limit", 200))
	matches := []map[string]any{}
	for _, c := range chats {
		idF, ok := c["id"].(float64)
		if !ok {
			continue
		}
		msgs, err := runImsg(ctx, "history", "--chat-id", strconv.Itoa(int(idF)), "--json", "--limit", perChat)
		if err != nil {
			continue // skip chats we cannot read rather than failing the search
		}
		for _, m := range msgs {
			// Filter first so codes can never be found by searching for them.
			if keep, hidden := filterRow(a.otp, m); !keep || hidden {
				continue
			}
			text, _ := m["text"].(string)
			if text != "" && strings.Contains(strings.ToLower(text), query) {
				matches = append(matches, m)
			}
		}
	}
	return toJSON(matches)
}

func (a *app) status(ctx context.Context, _ map[string]any) (string, error) {
	lines := []string{"imsg-mcp " + version}
	c := clientFrom(ctx)
	if c == nil {
		return "", errors.New("unknown client")
	}
	lines = append(lines, "this client: "+c.describe())
	if p, err := proc.Find(os.Getenv("IMSG_BIN"), "imsg"); err != nil {
		lines = append(lines, "imsg command: NOT FOUND ("+err.Error()+")")
	} else {
		lines = append(lines, "imsg command: found at "+p)
	}
	if db := os.Getenv("IMSG_DB"); db != "" {
		lines = append(lines, "database: snapshot "+db)
	} else {
		lines = append(lines, "database: live chat.db")
	}
	lines = append(lines, "one-time-code filter: "+string(a.otp))

	if !c.has(scopeSend) {
		lines = append(lines, "sending: not allowed for this client")
		return strings.Join(lines, "\n"), nil
	}
	if a.sender == nil {
		lines = append(lines, "sending: OFF (needs IMSG_ALLOW_SEND=1 and a non-empty allowlist at "+allowlistPath()+", then a restart)")
		return strings.Join(lines, "\n"), nil
	}
	list, err := loadAllowlist(allowlistPath())
	if err != nil {
		lines = append(lines, "sending: ON, but the allowlist cannot be read: "+err.Error())
		return strings.Join(lines, "\n"), nil
	}
	mode := "ON"
	if dryRun() {
		mode = "ON (dry run: nothing is actually sent)"
	}
	lines = append(lines, fmt.Sprintf("sending: %s, at most %d per hour", mode, a.sender.limiter.max))
	lines = append(lines, "allowed recipients:")
	for _, r := range list {
		if r.label != "" {
			lines = append(lines, "  - "+r.label+" ("+r.handle+")")
		} else {
			lines = append(lines, "  - "+r.handle)
		}
	}
	return strings.Join(lines, "\n"), nil
}

func newServer() (*mcp.Server, *app) {
	a := &app{otp: otpModeFromEnv()}
	s := mcp.New("imsg", version)
	s.Allow = allowTool
	s.Instructions = "Read the user's iMessage/SMS history. Messages that look like one-time codes are hidden on purpose; do not try to work around that. Use list_chats first to get chat ids."

	s.Add(mcp.Tool{
		Name:        "list_chats",
		Description: "List recent iMessage/SMS conversations, most recent first: chat id, name, service, participants, last_message_at. Use the returned id as chat_id for other tools.",
		InputSchema: mcp.Object(map[string]any{"limit": mcp.Prop("integer", "Max chats to return (default 20).")}),
		Handler:     a.listChats,
	})
	s.Add(mcp.Tool{
		Name:        "chat_detail",
		Description: "Inspect one chat by chat_id: identifiers, service, participants, and whether it is a group.",
		InputSchema: mcp.Object(map[string]any{"chat_id": mcp.Prop("integer", "The chat id from list_chats.")}, "chat_id"),
		Handler:     a.chatDetail,
	})
	s.Add(mcp.Tool{
		Name:        "read_history",
		Description: "Read messages from one chat in chronological order, with optional date window and sender filter. Each message has sender, sender_name, is_from_me, text and created_at. Messages that look like one-time codes are hidden.",
		InputSchema: mcp.Object(map[string]any{
			"chat_id":      mcp.Prop("integer", "The chat id from list_chats."),
			"limit":        mcp.Prop("integer", "Max messages (default 50), applied after filters."),
			"start":        mcp.Prop("string", "ISO 8601 lower bound, e.g. 2026-06-01T00:00:00Z."),
			"end":          mcp.Prop("string", "ISO 8601 upper bound."),
			"participants": mcp.Prop("string", "Comma-separated raw handles to filter by sender (group chats)."),
			"attachments":  mcp.Prop("boolean", "Include attachment metadata (default false)."),
		}, "chat_id"),
		Handler: a.readHistory,
	})
	s.Add(mcp.Tool{
		Name:        "search_messages",
		Description: "Find messages whose text contains a query (case-insensitive), scanning the most recent chats and their recent history. Good for finding action items. Messages that look like one-time codes are never returned.",
		InputSchema: mcp.Object(map[string]any{
			"query":          mcp.Prop("string", "Substring to search for (case-insensitive)."),
			"max_chats":      mcp.Prop("integer", "How many recent chats to scan (default 20)."),
			"per_chat_limit": mcp.Prop("integer", "How many recent messages to scan per chat (default 200)."),
		}, "query"),
		Handler: a.searchMessages,
	})
	s.Add(mcp.Tool{
		Name:        "imsg_status",
		Description: "Report the server's settings: imsg binary, database, one-time-code filter, and whether sending is on and to whom.",
		Handler:     a.status,
	})

	if sendRequested() {
		if list, err := loadAllowlist(allowlistPath()); err != nil {
			fmt.Fprintln(os.Stderr, "imsg-mcp: sending stays off, allowlist error:", err)
		} else if len(list) == 0 {
			fmt.Fprintln(os.Stderr, "imsg-mcp: sending stays off, the allowlist is empty:", allowlistPath())
		} else {
			a.sender = &sender{limiter: newRateLimiter(), send: imsgSend}
			s.Add(sendTool(a.sender))
		}
	}
	return s, a
}

func main() {
	cfg := env.Load("imsg")
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "token":
			os.Exit(tokenCmd(os.Args[2:], os.Stdout, os.Stderr))
		case "otp-audit":
			env.WidenPath()
			os.Exit(auditCmd(os.Args[2:], os.Stdout, os.Stderr))
		}
	}
	stdio := flag.Bool("stdio", false, "speak MCP over stdin/stdout instead of HTTP")
	stdioScopes := flag.String("scopes", "read", "with -stdio: what the client may do (read, send)")
	stdioChats := flag.String("chats", "", "with -stdio: chat ids the client may read (default: all)")
	flag.Parse()
	env.WidenPath()

	s, a := newServer()

	if *stdio {
		c := &client{label: "stdio"}
		var err error
		if c.scopes, err = parseScopes(*stdioScopes); err == nil && *stdioChats != "" {
			c.chats, err = parseChats(*stdioChats)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "imsg-mcp:", err)
			os.Exit(2)
		}
		if err := s.ServeStdio(withClient(context.Background(), c), os.Stdin, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, "imsg-mcp:", err)
			os.Exit(1)
		}
		return
	}

	addr := os.Getenv("IMSG_ADDR")
	if addr == "" {
		addr = "127.0.0.1:8765"
	}
	if host, _, err := net.SplitHostPort(addr); err == nil {
		if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
			fmt.Fprintf(os.Stderr, "imsg-mcp: refusing to listen on %s; keep IMSG_ADDR on loopback (127.0.0.1)\n", addr)
			os.Exit(1)
		}
	}
	clients, err := loadClients(clientsPath())
	if err != nil {
		fmt.Fprintln(os.Stderr, "imsg-mcp:", err)
		os.Exit(1)
	}
	legacy := legacyClient()
	if len(clients) == 0 && legacy == nil {
		fmt.Fprintf(os.Stderr, "imsg-mcp: no clients set up, so nothing could connect. Add one first:\n  %s token add claude-desktop -scopes read\n", os.Args[0])
		os.Exit(1)
	}

	mux := http.NewServeMux()
	mux.Handle("/mcp", s.HTTPHandler(authenticator(clientsPath())))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok\n") })

	fmt.Fprintf(os.Stderr, "imsg-mcp %s listening on http://%s/mcp\n", version, addr)
	fmt.Fprintln(os.Stderr, "settings file:", cfg)
	fmt.Fprintln(os.Stderr, "clients file:", clientsPath())
	for _, c := range clients {
		fmt.Fprintln(os.Stderr, "  client", c.describe())
	}
	if legacy != nil {
		fmt.Fprintln(os.Stderr, "  client IMSG_TOKEN: read,send (legacy full-access token; prefer imsg-mcp token add)")
	}
	fmt.Fprintln(os.Stderr, "one-time-code filter:", a.otp)
	if a.sender != nil {
		fmt.Fprintln(os.Stderr, "sending: ON, allowlist", allowlistPath())
	} else {
		fmt.Fprintln(os.Stderr, "sending: off")
	}

	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	if err := srv.ListenAndServe(); err != nil {
		fmt.Fprintln(os.Stderr, "server error:", err)
		os.Exit(1)
	}
}
