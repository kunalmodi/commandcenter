package main

import (
	"strings"
	"testing"
)

func TestLooksLikeOTP(t *testing.T) {
	hide := []struct{ text, sender string }{
		{"Your Apple Account code is: 482913. Don't share it with anyone.", "+12345"},
		{"G-583920 is your Google verification code.", "22000"},
		{"123456 is your Instagram code. Don't share it.", "32665"},
		{"Your Chase one-time passcode is 77812934. It expires in 10 minutes.", "24273"},
		{"Coinbase: your 2FA code is 093 214", "+18885551234"},
		{"Use verification code 4471 to sign in", "+14155550000"},
		{"Your login code: 551902", "+14155550000"},
		{"Your Uber code is 4821", "+14155550000"},
		{"Your code is A7K9Q2", "+14155550000"},
		{"Do not share this: 8812", "+14155550000"},
		{"Your security code is 902-114", "+14155550000"},
		{"8291 is your code", "+14155550000"},
		{"Here is your code 662211", "72975"}, // short-code sender + "code"
	}
	for _, c := range hide {
		if !looksLikeOTP(c.text, c.sender) {
			t.Errorf("should hide: %q", c.text)
		}
	}

	keep := []struct{ text, sender string }{
		{"want to grab dinner at 7?", "+14155551212"},
		{"Flight lands 2026-11-07, gate B12", "+14155551212"},
		{"call me at 415 555 1212 when you're free", "+14155551212"},
		{"the code review went fine", "+14155551212"},
		{"Can you verify the restaurant takes reservations?", "+14155551212"},
		{"just got the iPhone17 lol", "+14155551212"},
		{"", "+14155551212"},
		{"Your package was delivered", "24273"},
	}
	for _, c := range keep {
		if looksLikeOTP(c.text, c.sender) {
			t.Errorf("should keep: %q", c.text)
		}
	}
}

func TestFilterRowsModes(t *testing.T) {
	mk := func() []map[string]any {
		return []map[string]any{
			{"text": "dinner tonight?", "sender": "+14155551212"},
			{"text": "Your verification code is 123456", "sender": "22000", "attributedBody": "Your verification code is 123456"},
		}
	}

	rows := filterRows(otpRedact, mk())
	if len(rows) != 2 || rows[1]["text"] != otpPlaceholder || rows[1]["otp_hidden"] != true {
		t.Fatalf("redact: %+v", rows)
	}
	if _, ok := rows[1]["attributedBody"]; ok {
		t.Fatal("redact should remove attributedBody copy")
	}
	if rows[0]["text"] != "dinner tonight?" {
		t.Fatal("redact touched a normal message")
	}

	if rows := filterRows(otpDrop, mk()); len(rows) != 1 || rows[0]["text"] != "dinner tonight?" {
		t.Fatalf("drop: %+v", rows)
	}
	if rows := filterRows(otpOff, mk()); len(rows) != 2 || rows[1]["text"] == otpPlaceholder {
		t.Fatalf("off: %+v", rows)
	}
}

func TestOTPModeFromEnv(t *testing.T) {
	for v, want := range map[string]otpMode{"": otpRedact, "redact": otpRedact, "drop": otpDrop, "off": otpOff, "OFF": otpOff, "junk": otpRedact} {
		t.Setenv("IMSG_OTP_FILTER", v)
		if got := otpModeFromEnv(); got != want {
			t.Errorf("%q: got %s want %s", v, got, want)
		}
	}
}

func TestAuditMasksDigits(t *testing.T) {
	line := auditLine("HIDDEN", "+15551234567", "Your code is 482913")
	if strings.ContainsAny(line, "0123456789") {
		t.Fatalf("digits leaked: %q", line)
	}
	if auditVerdict("Your code is 482913", "12345") != "HIDDEN" || auditVerdict("promo code SAVE20 at 1234 Main", "+15551234567") != "CHECK" || auditVerdict("see you at 5", "+15551234567") != "" {
		t.Fatal("verdicts wrong")
	}
}
