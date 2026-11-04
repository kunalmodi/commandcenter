package main

import (
	"os"
	"regexp"
	"strings"
)

// One-time codes (2FA, login, verification) are a poor thing to hand an
// agent: anything that can read them can take over accounts. This filter
// hides messages that look like they carry one, before any tool returns
// them or searches them.
//
// IMSG_OTP_FILTER chooses what happens:
//
//	redact (default)  keep the message, replace its text with a placeholder
//	drop              leave the message out entirely
//	off               no filtering
//
// The check is a heuristic and leans toward hiding: a message is treated
// as a code when it has a code-shaped token AND either a strong keyword
// ("verification", "one-time", "do not share", ...) or a "your code is"
// phrase, or when it comes from an SMS short code and mentions a code.

const otpPlaceholder = "[hidden by imsg-mcp: this message looks like it contains a one-time code]"

type otpMode string

const (
	otpRedact otpMode = "redact"
	otpDrop   otpMode = "drop"
	otpOff    otpMode = "off"
)

func otpModeFromEnv() otpMode {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("IMSG_OTP_FILTER"))) {
	case "off", "none", "0", "false":
		return otpOff
	case "drop":
		return otpDrop
	default:
		return otpRedact
	}
}

var (
	// Code-shaped tokens: 4–8 digits standing alone; a 3+3 split ("123 456",
	// "123-456"); Google-style "G-123456"; all-caps letters+digits of length
	// 6–8 ("A7K9Q2").
	otpDigitsRe   = regexp.MustCompile(`(?:^|[^\d])\d{4,8}(?:[^\d]|$)`)
	otpSplitRe    = regexp.MustCompile(`\b\d{3}[- ]\d{3}\b`)
	otpPrefixedRe = regexp.MustCompile(`\b[A-Z]{1,3}-\d{4,8}\b`)
	otpAlnumRe    = regexp.MustCompile(`\b[A-Z0-9]{6,8}\b`)

	otpStrongRe = regexp.MustCompile(`(?i)` + strings.Join([]string{
		`one[- ]?time`, `\botp\b`, `passcode`, `pass code`, `verification`, `verify`,
		`authentication`, `\bauth\b`, `\b2fa\b`, `\bmfa\b`, `two[- ]factor`, `2[- ]step`,
		`security code`, `login code`, `log[- ]in code`, `sign[- ]in code`, `access code`,
		`confirmation code`, `\bpin\b`, `do not share`, `don'?t share`, `never share`,
		`not share this`, `expires? in`, `valid for \d+ min`, `reset your password`,
	}, "|"))

	// "your code is 1234", "code: 1234", "1234 is your ... code".
	otpPhraseRe = regexp.MustCompile(`(?i)\bcode\b\s*(?:is|:|=)\s*[A-Z0-9-]{4,10}|\b[A-Z0-9-]{4,10}\s+is\s+your\b`)

	otpCodeWordRe = regexp.MustCompile(`(?i)\bcodes?\b`)

	// SMS short codes are 3–6 digit senders (plus optional leading +).
	shortCodeRe = regexp.MustCompile(`^\+?\d{3,6}$`)
)

// hasCodeToken reports whether text holds something shaped like a code.
func hasCodeToken(text string) bool {
	if otpDigitsRe.MatchString(text) || otpSplitRe.MatchString(text) || otpPrefixedRe.MatchString(text) {
		return true
	}
	for _, m := range otpAlnumRe.FindAllString(text, -1) {
		if strings.ContainsAny(m, "0123456789") && strings.ContainsAny(m, "ABCDEFGHIJKLMNOPQRSTUVWXYZ") {
			return true
		}
	}
	return false
}

// looksLikeOTP decides whether a message text (from sender) carries a code.
func looksLikeOTP(text, sender string) bool {
	if text == "" || !hasCodeToken(text) {
		return false
	}
	if otpStrongRe.MatchString(text) || otpPhraseRe.MatchString(text) {
		return true
	}
	if shortCodeRe.MatchString(strings.TrimSpace(sender)) && otpCodeWordRe.MatchString(text) {
		return true
	}
	return false
}

// Fields in imsg's JSON that can hold message text.
var textFields = []string{"text", "body", "last_message", "last_message_text", "snippet", "preview", "subject"}

// senderOf finds the sender handle on a message object, if any.
func senderOf(m map[string]any) string {
	for _, k := range []string{"sender", "handle", "from", "identifier"} {
		if s, ok := m[k].(string); ok && s != "" {
			return s
		}
	}
	return ""
}

// filterRow applies the filter to one object from imsg. It returns false if
// the row should be dropped, and whether it was changed.
func filterRow(mode otpMode, m map[string]any) (keep bool, hidden bool) {
	if mode == otpOff {
		return true, false
	}
	sender := senderOf(m)
	for _, k := range textFields {
		s, ok := m[k].(string)
		if !ok || !looksLikeOTP(s, sender) {
			continue
		}
		if mode == otpDrop {
			return false, true
		}
		m[k] = otpPlaceholder
		hidden = true
	}
	if hidden {
		m["otp_hidden"] = true
		// attributedBody-style duplicates of the text would leak it too.
		for _, k := range []string{"attributed_body", "attributedBody"} {
			delete(m, k)
		}
	}
	return true, hidden
}

// filterRows applies the filter to a list of rows.
func filterRows(mode otpMode, rows []map[string]any) []map[string]any {
	if mode == otpOff {
		return rows
	}
	out := rows[:0]
	for _, r := range rows {
		if keep, _ := filterRow(mode, r); keep {
			out = append(out, r)
		}
	}
	return out
}
