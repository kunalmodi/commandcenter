// Package proc runs external commands with a timeout and readable errors.
package proc

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Cmd describes one command to run.
type Cmd struct {
	Name    string
	Args    []string
	Dir     string    // working directory; empty means current
	Env     []string  // extra KEY=VALUE entries on top of os.Environ()
	Stdin   io.Reader // optional
	Timeout time.Duration
}

// Result is what a command printed.
type Result struct {
	Stdout string
	Stderr string
}

// Run executes c. On failure the error carries stderr (or stdout) so the
// model gets the tool's own explanation.
func Run(ctx context.Context, c Cmd) (Result, error) {
	if c.Timeout <= 0 {
		c.Timeout = time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, c.Timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, c.Name, c.Args...)
	cmd.Dir = c.Dir
	cmd.Env = append(os.Environ(), c.Env...)
	if c.Stdin != nil {
		cmd.Stdin = c.Stdin
	}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	res := Result{Stdout: out.String(), Stderr: errb.String()}
	if ctx.Err() == context.DeadlineExceeded {
		return res, fmt.Errorf("%s timed out after %s", c.Name, c.Timeout)
	}
	if err != nil {
		msg := strings.TrimSpace(res.Stderr)
		if msg == "" {
			msg = strings.TrimSpace(res.Stdout)
		}
		if msg == "" {
			msg = err.Error()
		}
		return res, errors.New(msg)
	}
	return res, nil
}

// Find returns the path to a binary: override (an env var value) if set and
// executable, else the first match on PATH, else the first existing fallback.
func Find(override, name string, fallbacks ...string) (string, error) {
	if override != "" {
		if isExec(override) {
			return override, nil
		}
		return "", fmt.Errorf("%s is set to %s, but nothing executable is there", name, override)
	}
	if p, err := exec.LookPath(name); err == nil {
		return p, nil
	}
	for _, p := range fallbacks {
		if isExec(p) {
			return p, nil
		}
	}
	return "", fmt.Errorf("the %s command was not found on PATH", name)
}

func isExec(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir() && st.Mode()&0o111 != 0
}
