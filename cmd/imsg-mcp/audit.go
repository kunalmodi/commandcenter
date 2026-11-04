package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
)

// "imsg-mcp otp-audit" checks the one-time-code filter against your real
// messages, locally, without showing any codes: every digit is masked.
// Run it in the terminal that has Full Disk Access. It lists:
//
//	HIDDEN     messages the filter hides (look for normal texts here: false positives)
//	CHECK      code-shaped messages it lets through that mention a code or come
//	           from a short code (look for real codes here: leaks)
//
// Add any mistakes as cases in otp_test.go (with made-up digits).

var digitRe = regexp.MustCompile(`\d`)

func maskDigits(s string) string { return digitRe.ReplaceAllString(s, "#") }

func auditLine(kind, sender, text string) string {
	t := strings.Join(strings.Fields(maskDigits(text)), " ")
	if r := []rune(t); len(r) > 140 {
		t = string(r[:140]) + "…"
	}
	return fmt.Sprintf("%-7s %-16s %s", kind, maskDigits(sender), t)
}

// auditVerdict classifies one message for the audit; "" means unremarkable.
func auditVerdict(text, sender string) string {
	if text == "" {
		return ""
	}
	if looksLikeOTP(text, sender) {
		return "HIDDEN"
	}
	if hasCodeToken(text) && (otpCodeWordRe.MatchString(text) || shortCodeRe.MatchString(strings.TrimSpace(sender))) {
		return "CHECK"
	}
	return ""
}

func auditCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("otp-audit", flag.ContinueOnError)
	fs.SetOutput(stderr)
	maxChats := fs.Int("chats", 50, "how many recent chats to scan")
	perChat := fs.Int("per-chat", 300, "how many recent messages per chat")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	ctx := context.Background()
	chats, err := runImsg(ctx, "chats", "--json", "--limit", strconv.Itoa(*maxChats))
	if err != nil {
		fmt.Fprintln(stderr, "imsg-mcp:", err, "(does this terminal have Full Disk Access?)")
		return 1
	}
	var scanned, hidden, check int
	for _, c := range chats {
		idF, ok := c["id"].(float64)
		if !ok {
			continue
		}
		msgs, err := runImsg(ctx, "history", "--chat-id", strconv.Itoa(int(idF)), "--json", "--limit", strconv.Itoa(*perChat))
		if err != nil {
			fmt.Fprintf(stderr, "chat %d: %v\n", int(idF), err)
			continue
		}
		for _, m := range msgs {
			scanned++
			text, _ := m["text"].(string)
			sender := senderOf(m)
			switch v := auditVerdict(text, sender); v {
			case "HIDDEN":
				hidden++
				fmt.Fprintln(stdout, auditLine(v, sender, text))
			case "CHECK":
				check++
				fmt.Fprintln(stdout, auditLine(v, sender, text))
			}
		}
	}
	fmt.Fprintf(stdout, "\nscanned %d messages in %d chats: %d hidden, %d to check\n", scanned, len(chats), hidden, check)
	return 0
}
