package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/kunalmodi/commandcenter/internal/env"
	"github.com/kunalmodi/commandcenter/internal/mcp"
	"github.com/kunalmodi/commandcenter/internal/proc"
)

// Sending is off unless IMSG_ALLOW_SEND=1 AND the allowlist file has at
// least one recipient. When it is off, the send_message tool is not even
// listed. The allowlist is re-read on every send, so removing someone takes
// effect immediately, without a restart.
//
// Allowlist file (IMSG_SEND_ALLOWLIST, default
// ~/.config/commandcenter/imsg-allowlist.txt), one recipient per line:
//
//	+14155551212   Mom
//	someone@icloud.com   Sam
//	# comments are fine
//
// The first field is the phone number or email; the rest is an optional
// label the agent may use instead ("send to Mom").
//
// Other guards: a per-hour cap (IMSG_SEND_MAX_PER_HOUR, default 10), a
// length cap, no attachments, no messages that themselves look like a
// one-time code, and an append-only audit log of every attempt
// (IMSG_SEND_LOG, default ~/.config/commandcenter/logs/imsg-send.log).
// IMSG_SEND_DRY_RUN=1 logs and reports without sending.

const maxSendChars = 2000

type recipient struct {
	handle string // normalized
	label  string
}

func allowlistPath() string {
	if p := os.Getenv("IMSG_SEND_ALLOWLIST"); p != "" {
		return p
	}
	return filepath.Join(env.ConfigDir(), "imsg-allowlist.txt")
}

func sendLogPath() string {
	if p := os.Getenv("IMSG_SEND_LOG"); p != "" {
		return p
	}
	return filepath.Join(env.ConfigDir(), "logs", "imsg-send.log")
}

func sendRequested() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("IMSG_ALLOW_SEND")))
	return v == "1" || v == "true" || v == "yes"
}

func dryRun() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("IMSG_SEND_DRY_RUN")))
	return v == "1" || v == "true" || v == "yes"
}

// normalizeHandle puts a phone number or email in one canonical form so
// "(415) 555-1212", "415.555.1212" and "+1 415 555 1212" all match.
// Ten-digit numbers are assumed to be US/Canada (+1).
func normalizeHandle(h string) string {
	h = strings.TrimSpace(h)
	h = strings.TrimPrefix(strings.TrimPrefix(h, "tel:"), "mailto:")
	if strings.Contains(h, "@") {
		return strings.ToLower(h)
	}
	plus := strings.HasPrefix(h, "+")
	var digits strings.Builder
	for _, r := range h {
		switch {
		case unicode.IsDigit(r):
			digits.WriteRune(r)
		case r == ' ' || r == '-' || r == '.' || r == '(' || r == ')' || r == '+':
		default:
			return "" // letters or other symbols: not a phone number
		}
	}
	d := digits.String()
	switch {
	case d == "":
		return ""
	case plus:
		return "+" + d
	case len(d) == 10:
		return "+1" + d
	case len(d) == 11 && d[0] == '1':
		return "+" + d
	default:
		return d // short codes and other local forms stay as typed
	}
}

func isPhoneChunk(s string) bool {
	hasDigit := false
	for _, r := range s {
		switch {
		case unicode.IsDigit(r):
			hasDigit = true
		case strings.ContainsRune("+-.()", r):
		default:
			return false
		}
	}
	return hasDigit
}

// loadAllowlist reads the allowlist file. A missing file is an empty list.
func loadAllowlist(path string) ([]recipient, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []recipient
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		line := sc.Text()
		if i := strings.Index(line, "#"); i >= 0 {
			line = line[:i]
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		// The handle is the first field for an email, or the leading run of
		// phone-number fields ("(415) 555-1212"); the rest is the label.
		take := 1
		if !strings.Contains(fields[0], "@") {
			for take < len(fields) && isPhoneChunk(fields[take]) {
				take++
			}
		}
		raw := strings.Join(fields[:take], " ")
		h := normalizeHandle(raw)
		if h == "" {
			return nil, fmt.Errorf("%s line %d: %q is not a phone number or email (put the number or email first, then the label)", path, n, raw)
		}
		out = append(out, recipient{handle: h, label: strings.Join(fields[take:], " ")})
	}
	return out, sc.Err()
}

// resolve finds the allowlisted recipient matching to (a handle or label).
func resolve(list []recipient, to string) (recipient, bool) {
	if h := normalizeHandle(to); h != "" {
		for _, r := range list {
			if r.handle == h {
				return r, true
			}
		}
	}
	want := strings.ToLower(strings.TrimSpace(to))
	var found []recipient
	for _, r := range list {
		if r.label != "" && strings.ToLower(r.label) == want {
			found = append(found, r)
		}
	}
	if len(found) == 1 {
		return found[0], true
	}
	return recipient{}, false
}

// rateLimiter allows at most max sends in any rolling hour.
type rateLimiter struct {
	mu   sync.Mutex
	max  int
	sent []time.Time
}

func (l *rateLimiter) allow(now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	cut := now.Add(-time.Hour)
	kept := l.sent[:0]
	for _, t := range l.sent {
		if t.After(cut) {
			kept = append(kept, t)
		}
	}
	l.sent = kept
	if len(l.sent) >= l.max {
		return false
	}
	l.sent = append(l.sent, now)
	return true
}

func newRateLimiter() *rateLimiter {
	max := 10
	if v, err := strconv.Atoi(os.Getenv("IMSG_SEND_MAX_PER_HOUR")); err == nil && v > 0 {
		max = v
	}
	return &rateLimiter{max: max}
}

var logMu sync.Mutex

func audit(outcome string, r recipient, to, text string) {
	logMu.Lock()
	defer logMu.Unlock()
	p := sendLogPath()
	_ = os.MkdirAll(filepath.Dir(p), 0o700)
	f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		fmt.Fprintln(os.Stderr, "imsg-mcp: cannot write send log:", err)
		return
	}
	defer f.Close()
	who := r.handle
	if who == "" {
		who = "requested=" + strconv.Quote(to)
	}
	fmt.Fprintf(f, "%s\t%s\t%s\t%s\t%s\n", time.Now().Format(time.RFC3339), outcome, who, r.label, strconv.Quote(text))
}

type sender struct {
	limiter *rateLimiter
	// send actually delivers; replaced in tests.
	send func(ctx context.Context, handle, text string) error
}

func imsgSend(ctx context.Context, handle, text string) error {
	args := []string{"send", "--to", handle, "--text", text}
	if svc := os.Getenv("IMSG_SEND_SERVICE"); svc != "" {
		args = append(args, "--service", svc)
	}
	// Never pass --db here: sending goes through Messages, not a snapshot.
	_, err := proc.Run(ctx, proc.Cmd{Name: imsgBin(), Args: args, Timeout: 60 * time.Second})
	return err
}

func (s *sender) handle(ctx context.Context, args map[string]any) (string, error) {
	to := strings.TrimSpace(mcp.Str(args, "to"))
	text := mcp.Str(args, "text")
	if to == "" {
		return "", errors.New("to is required")
	}
	if strings.TrimSpace(text) == "" {
		return "", errors.New("text is required")
	}
	if n := len([]rune(text)); n > maxSendChars {
		return "", fmt.Errorf("text is too long (%d characters; limit %d)", n, maxSendChars)
	}
	if !sendRequested() {
		return "", errors.New("sending is turned off (IMSG_ALLOW_SEND is not set)")
	}
	list, err := loadAllowlist(allowlistPath())
	if err != nil {
		return "", fmt.Errorf("cannot read the allowlist: %v", err)
	}
	r, ok := resolve(list, to)
	if !ok {
		audit("REFUSED-not-allowlisted", recipient{}, to, text)
		return "", fmt.Errorf("%q is not on the send allowlist, so nothing was sent. Only the user can add recipients, by editing %s", to, allowlistPath())
	}
	if looksLikeOTP(text, "") {
		audit("REFUSED-looks-like-code", r, to, text)
		return "", errors.New("refused: the message looks like it contains a one-time code, and forwarding codes is not allowed")
	}
	if !s.limiter.allow(time.Now()) {
		audit("REFUSED-rate-limit", r, to, text)
		return "", fmt.Errorf("refused: the limit of %d messages per hour has been reached", s.limiter.max)
	}
	name := r.handle
	if r.label != "" {
		name = r.label + " (" + r.handle + ")"
	}
	if dryRun() {
		audit("DRY-RUN", r, to, text)
		return "Dry run (IMSG_SEND_DRY_RUN is set): would have sent to " + name + ". Nothing was sent.", nil
	}
	if err := s.send(ctx, r.handle, text); err != nil {
		audit("FAILED", r, to, text)
		return "", fmt.Errorf("imsg send failed: %v", err)
	}
	audit("SENT", r, to, text)
	return "Sent to " + name + ".", nil
}

func sendTool(s *sender) mcp.Tool {
	return mcp.Tool{
		Name:        "send_message",
		Description: "Send an iMessage/SMS text to ONE recipient on the user's allowlist. `to` is a phone number, email, or the allowlist label (e.g. a name). Anyone not on the allowlist is refused; you cannot add people. No attachments. Call imsg_status to see who is allowed. Always confirm the exact text with the user before sending unless they already told you what to send.",
		InputSchema: mcp.Object(map[string]any{
			"to":   mcp.Prop("string", "Recipient: an allowlisted phone number, email, or label."),
			"text": mcp.Prop("string", "The message text (plain text, at most 2000 characters)."),
		}, "to", "text"),
		Handler: s.handle,
	}
}
