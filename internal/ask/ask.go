// Package ask holds the argument checks shared by the model-calling servers.
package ask

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// MaxPromptChars caps prompt size so one call cannot run away.
const MaxPromptChars = 200000

var modelRe = regexp.MustCompile(`^[A-Za-z0-9._:/-]{1,80}$`)

// Prompt checks a prompt argument.
func Prompt(p string) error {
	if strings.TrimSpace(p) == "" {
		return errors.New("prompt is required")
	}
	if n := len([]rune(p)); n > MaxPromptChars {
		return fmt.Errorf("prompt is too long (%d characters; limit %d)", n, MaxPromptChars)
	}
	return nil
}

// Model checks an optional model id; empty is fine.
func Model(m string) error {
	if m != "" && !modelRe.MatchString(m) {
		return errors.New("model contains characters that are not allowed")
	}
	return nil
}

// Effort checks an optional effort value against the allowed set.
func Effort(e string, allowed ...string) error {
	if e == "" {
		return nil
	}
	for _, a := range allowed {
		if e == a {
			return nil
		}
	}
	return fmt.Errorf("effort must be one of: %s", strings.Join(allowed, ", "))
}
