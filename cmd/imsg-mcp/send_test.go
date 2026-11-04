package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNormalizeHandle(t *testing.T) {
	cases := map[string]string{
		"(415) 555-1212":     "+14155551212",
		"415.555.1212":       "+14155551212",
		"+1 415 555 1212":    "+14155551212",
		"14155551212":        "+14155551212",
		"+44 20 7946 0958":   "+442079460958",
		"Sam@iCloud.com":     "sam@icloud.com",
		"tel:+14155551212":   "+14155551212",
		"mailto:a@b.co":      "a@b.co",
		"32665":              "32665",
		"Mom":                "",
		"415-555-1212; rm x": "",
	}
	for in, want := range cases {
		if got := normalizeHandle(in); got != want {
			t.Errorf("normalizeHandle(%q) = %q, want %q", in, got, want)
		}
	}
}

func writeAllowlist(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "allow.txt")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("IMSG_SEND_ALLOWLIST", p)
	t.Setenv("IMSG_SEND_LOG", filepath.Join(dir, "send.log"))
	return dir
}

func TestLoadAllowlistAndResolve(t *testing.T) {
	writeAllowlist(t, "# family\n(415) 555-1212  Mom\nsam@icloud.com Sam Lee  # friend\n\n+14155550001\n")
	list, err := loadAllowlist(allowlistPath())
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 3 {
		t.Fatalf("got %d entries: %+v", len(list), list)
	}
	for to, want := range map[string]string{
		"mom": "+14155551212", "+1 (415) 555-1212": "+14155551212",
		"Sam Lee": "sam@icloud.com", "SAM@ICLOUD.COM": "sam@icloud.com",
		"415-555-0001": "+14155550001",
	} {
		r, ok := resolve(list, to)
		if !ok || r.handle != want {
			t.Errorf("resolve(%q) = %+v %v, want %s", to, r, ok, want)
		}
	}
	for _, to := range []string{"Dad", "+14155559999", "sam", ""} {
		if _, ok := resolve(list, to); ok {
			t.Errorf("resolve(%q) should fail", to)
		}
	}
}

func TestLoadAllowlistErrors(t *testing.T) {
	writeAllowlist(t, "Mom +14155551212\n")
	if _, err := loadAllowlist(allowlistPath()); err == nil {
		t.Fatal("expected an error for a label in the handle column")
	}
	t.Setenv("IMSG_SEND_ALLOWLIST", filepath.Join(t.TempDir(), "missing.txt"))
	if list, err := loadAllowlist(allowlistPath()); err != nil || len(list) != 0 {
		t.Fatalf("missing file should be empty, got %v %v", list, err)
	}
}

func TestSendGuards(t *testing.T) {
	dir := writeAllowlist(t, "+14155551212 Mom\n")
	t.Setenv("IMSG_ALLOW_SEND", "1")
	var sent []string
	s := &sender{limiter: &rateLimiter{max: 2}, send: func(_ context.Context, h, text string) error {
		sent = append(sent, h+"|"+text)
		return nil
	}}
	call := func(to, text string) (string, error) {
		return s.handle(context.Background(), map[string]any{"to": to, "text": text})
	}

	if _, err := call("+14155559999", "hi"); err == nil || !strings.Contains(err.Error(), "not on the send allowlist") {
		t.Fatalf("stranger: %v", err)
	}
	if _, err := call("Mom", "your code is 123456"); err == nil || !strings.Contains(err.Error(), "one-time code") {
		t.Fatalf("otp forward: %v", err)
	}
	if _, err := call("Mom", strings.Repeat("a", maxSendChars+1)); err == nil {
		t.Fatal("too long should fail")
	}
	if _, err := call("Mom", "  "); err == nil {
		t.Fatal("empty should fail")
	}
	if out, err := call("mom", "running late"); err != nil || !strings.Contains(out, "Sent to Mom") {
		t.Fatalf("send: %q %v", out, err)
	}
	if _, err := call("Mom", "second"); err != nil {
		t.Fatal(err)
	}
	if _, err := call("Mom", "third"); err == nil || !strings.Contains(err.Error(), "per hour") {
		t.Fatalf("rate limit: %v", err)
	}
	if len(sent) != 2 || sent[0] != "+14155551212|running late" {
		t.Fatalf("sent = %v", sent)
	}

	// Removing someone takes effect without a restart.
	os.WriteFile(os.Getenv("IMSG_SEND_ALLOWLIST"), []byte("# nobody\n"), 0o600)
	s.limiter = &rateLimiter{max: 10}
	if _, err := call("Mom", "hi"); err == nil {
		t.Fatal("removed recipient should be refused")
	}

	// Turning the switch off stops sending even with the tool registered.
	os.WriteFile(os.Getenv("IMSG_SEND_ALLOWLIST"), []byte("+14155551212 Mom\n"), 0o600)
	t.Setenv("IMSG_ALLOW_SEND", "")
	if _, err := call("Mom", "hi"); err == nil {
		t.Fatal("send with IMSG_ALLOW_SEND unset should fail")
	}

	log, _ := os.ReadFile(filepath.Join(dir, "send.log"))
	for _, want := range []string{"REFUSED-not-allowlisted", "REFUSED-looks-like-code", "SENT", "REFUSED-rate-limit"} {
		if !strings.Contains(string(log), want) {
			t.Errorf("log missing %s:\n%s", want, log)
		}
	}
}

func TestDryRun(t *testing.T) {
	writeAllowlist(t, "+14155551212 Mom\n")
	t.Setenv("IMSG_ALLOW_SEND", "1")
	t.Setenv("IMSG_SEND_DRY_RUN", "1")
	s := &sender{limiter: &rateLimiter{max: 5}, send: func(context.Context, string, string) error {
		t.Fatal("dry run must not send")
		return nil
	}}
	out, err := s.handle(context.Background(), map[string]any{"to": "Mom", "text": "hi"})
	if err != nil || !strings.Contains(out, "Dry run") {
		t.Fatalf("%q %v", out, err)
	}
}

func TestRateLimiterWindow(t *testing.T) {
	l := &rateLimiter{max: 1}
	now := time.Now()
	if !l.allow(now) || l.allow(now.Add(time.Minute)) {
		t.Fatal("first allowed, second refused")
	}
	if !l.allow(now.Add(61 * time.Minute)) {
		t.Fatal("allowed again after an hour")
	}
}

func TestSendToolOnlyWhenEnabled(t *testing.T) {
	has := func() bool {
		s, _ := newServer()
		for _, tl := range s.Tools() {
			if tl.Name == "send_message" {
				return true
			}
		}
		return false
	}
	writeAllowlist(t, "+14155551212 Mom\n")
	t.Setenv("IMSG_ALLOW_SEND", "")
	if has() {
		t.Fatal("send_message listed with IMSG_ALLOW_SEND unset")
	}
	t.Setenv("IMSG_ALLOW_SEND", "1")
	if !has() {
		t.Fatal("send_message missing when enabled")
	}
	writeAllowlist(t, "# empty\n")
	if has() {
		t.Fatal("send_message listed with empty allowlist")
	}
}
