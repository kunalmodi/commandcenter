package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"

	"github.com/kunalmodi/commandcenter/internal/proc"
)

// sitecheck leans on open-source tools for the heavy lifting and keeps only
// the rules and glue. Each tool is pinned to an exact version and run through
// uvx (Python) or npx (Node), which fetch it on first use and cache it.
//
//	trafilatura  main-text extraction (content.go)
//	advertools   site crawler built on Scrapy (crawl.go)
//	lighthouse   performance/accessibility/SEO audits (psi.go fallback)
//
// The same pins are in the site-check skill's permission rules; change both
// together.
const (
	trafilaturaPin = "trafilatura@2.3.1"
	advertoolsPin  = "advertools==0.18.0"
)

// runTool runs an external tool and returns its stdout.
func runTool(ctx context.Context, name string, args []string, stdin io.Reader, timeout time.Duration) (string, error) {
	if _, err := exec.LookPath(name); err != nil {
		hint := map[string]string{"uvx": "install uv (brew install uv)", "npx": "install Node.js"}[name]
		return "", fmt.Errorf("%s not found; %s", name, hint)
	}
	res, err := proc.Run(ctx, proc.Cmd{Name: name, Args: args, Stdin: stdin, Timeout: timeout})
	if err != nil {
		msg := err.Error()
		if len(msg) > 300 {
			msg = msg[:300] + "…"
		}
		return "", errors.New(msg)
	}
	return res.Stdout, nil
}

// splitMulti splits advertools' multi-value cells ("a@@b@@c").
func splitMulti(v any) []string {
	s, _ := v.(string)
	if s == "" {
		return nil
	}
	return strings.Split(s, "@@")
}
