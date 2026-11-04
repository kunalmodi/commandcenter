// grok-mcp is a small MCP server (stdio) that sends a prompt to xAI's Grok
// and returns the reply. It has two backends:
//
//	cli  xAI's `grok` command, using its existing sign-in (run `grok login`)
//	api  xAI's HTTP API, using GROK_API_KEY
//
// With GROK_BACKEND unset it uses cli when the grok command can be found and
// api otherwise. In cli mode Grok runs in an empty temporary folder with web
// search, memory, subagents and planning turned off where the CLI supports
// those switches, so it sees only the prompt.
//
// Tools:
//
//	ask_grok(prompt, model?, effort?)  reply from Grok
//	get_result(job_id, wait_seconds?)  collect a reply that took too long
//	grok_status()                      which backend is ready (never shows the key)
//
// Settings (environment, or ~/.config/commandcenter/grok.env — keep the API
// key there, never in a repo):
//
//	GROK_BACKEND  cli | api
//	GROK_BIN      path to the grok binary
//	GROK_API_KEY  xAI API key (api backend)
//	GROK_API_BASE API base URL (default https://api.x.ai/v1)
//	GROK_MODEL    default model (cli: the CLI's default; api: grok-4.7)
//	GROK_EFFORT   default reasoning effort: low | medium | high | xhigh
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/kunalmodi/commandcenter/internal/ask"
	"github.com/kunalmodi/commandcenter/internal/env"
	"github.com/kunalmodi/commandcenter/internal/jobs"
	"github.com/kunalmodi/commandcenter/internal/mcp"
	"github.com/kunalmodi/commandcenter/internal/proc"
)

const (
	version         = "0.1.0"
	defaultAPIModel = "grok-4.7"
	defaultAPIBase  = "https://api.x.ai/v1"
	askTimeout      = 10 * time.Minute
)

var efforts = []string{"low", "medium", "high", "xhigh"}

func grokBin() (string, error) {
	home, _ := os.UserHomeDir()
	return proc.Find(os.Getenv("GROK_BIN"), "grok",
		filepath.Join(home, ".grok", "bin", "grok"), filepath.Join(home, ".local", "bin", "grok"),
		"/opt/homebrew/bin/grok", "/usr/local/bin/grok")
}

func backend() (string, error) {
	switch b := os.Getenv("GROK_BACKEND"); b {
	case "cli", "api":
		return b, nil
	case "":
		if _, err := grokBin(); err == nil {
			return "cli", nil
		}
		return "api", nil
	default:
		return "", errors.New("GROK_BACKEND must be cli or api")
	}
}

// cliArgs builds grok CLI arguments, passing only flags this version of the
// CLI lists in its --help so older or newer releases do not fail.
func cliArgs(help, prompt, workdir, model, effort string) []string {
	has := func(f string) bool {
		// Whole-flag match, so "--effort" does not match "--reasoning-effort".
		return regexp.MustCompile(`(^|[^\w-])` + regexp.QuoteMeta(f) + `([^\w-]|$)`).MatchString(help)
	}
	args := []string{"-p", prompt}
	add := func(flag string, extra ...string) {
		if has(flag) {
			args = append(args, flag)
			args = append(args, extra...)
		}
	}
	add("--output-format", "plain")
	add("--cwd", workdir)
	add("--no-auto-update")
	add("--no-alt-screen")
	add("--verbatim")
	add("--disable-web-search")
	add("--no-memory")
	add("--no-subagents")
	add("--no-plan")
	add("--sandbox", "read-only")
	if model != "" {
		args = append(args, "--model", model)
	}
	if effort != "" {
		if has("--effort") {
			args = append(args, "--effort", effort)
		} else if has("--reasoning-effort") {
			args = append(args, "--reasoning-effort", effort)
		}
	}
	return args
}

func askCLI(prompt, model, effort string) (string, error) {
	bin, err := grokBin()
	if err != nil {
		return "", fmt.Errorf("%v. Install xAI's Grok CLI and run: grok login (or set GROK_BACKEND=api with GROK_API_KEY)", err)
	}
	helpRes, _ := proc.Run(context.Background(), proc.Cmd{Name: bin, Args: []string{"--help"}, Timeout: 20 * time.Second})
	dir, err := os.MkdirTemp("", "grok-mcp-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)

	res, err := proc.Run(context.Background(), proc.Cmd{
		Name: bin, Args: cliArgs(helpRes.Stdout+helpRes.Stderr, prompt, dir, model, effort),
		Dir: dir, Stdin: strings.NewReader(""), Timeout: askTimeout,
	})
	out := strings.TrimSpace(res.Stdout)
	if err != nil || out == "" {
		msg := "grok returned no reply"
		if err != nil {
			msg = "grok -p failed: " + err.Error()
		}
		hint := "Check that you are signed in (run: grok login). To use the API instead set GROK_BACKEND=api."
		if model != "" {
			hint += " If the model id is the problem, run: grok models"
		}
		return "", fmt.Errorf("%s\n%s", msg, hint)
	}
	return fmt.Sprintf("Model: Grok CLI (%s)%s\n%s", orDefault(model, "default model"), effortNote(effort), out), nil
}

func askAPI(prompt, model, effort string) (string, error) {
	key := os.Getenv("GROK_API_KEY")
	if key == "" {
		return "", errors.New("no grok command found and GROK_API_KEY is not set. Either install xAI's Grok CLI and run: grok login, or put GROK_API_KEY=... in ~/.config/commandcenter/grok.env")
	}
	if model == "" {
		model = defaultAPIModel
	}
	base := strings.TrimRight(orDefault(os.Getenv("GROK_API_BASE"), defaultAPIBase), "/")
	payload := map[string]any{
		"model":    model,
		"messages": []map[string]string{{"role": "user", "content": prompt}},
	}
	if effort != "" {
		payload["reasoning_effort"] = effort
	}
	body, _ := json.Marshal(payload)

	ctx, cancel := context.WithTimeout(context.Background(), askTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("request to xAI failed: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("xAI returned HTTP %d for model %s: %s", resp.StatusCode, model, strings.TrimSpace(string(raw)))
	}
	var parsed struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil || len(parsed.Choices) == 0 {
		return "", fmt.Errorf("could not read xAI's reply: %s", strings.TrimSpace(string(raw)))
	}
	return fmt.Sprintf("Model: %s (API)%s\n%s", model, effortNote(effort), strings.TrimSpace(parsed.Choices[0].Message.Content)), nil
}

// defaultEffort reads GROK_EFFORT, or GROK_REASONING_EFFORT as older setups named it.
func defaultEffort() string {
	return orDefault(os.Getenv("GROK_EFFORT"), os.Getenv("GROK_REASONING_EFFORT"))
}

func effortNote(e string) string {
	if e == "" {
		return ""
	}
	return " (effort: " + e + ")"
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func main() {
	flag.Parse()
	cfg := env.Load("grok")
	env.WidenPath()

	runner := jobs.NewRunner()
	s := mcp.New("grok", version)
	s.Instructions = "Ask xAI's Grok for an independent answer. Grok sees only the prompt you send, never local files, so include all the context it needs."

	s.Add(mcp.Tool{
		Name:        "ask_grok",
		Description: "Send a prompt to xAI's Grok (through the grok CLI, or the API if the CLI is not installed) and return the reply. Useful as a second opinion from a different model family. Grok sees only the prompt. Slow: if it takes longer than about 40 seconds you get a job_id; collect the answer with get_result.",
		InputSchema: mcp.Object(map[string]any{
			"prompt": mcp.Prop("string", "The full prompt, including any context Grok needs."),
			"model":  mcp.Prop("string", "Optional model id. Omit to use the default."),
			"effort": map[string]any{"type": "string", "enum": efforts, "description": "Optional reasoning effort."},
		}, "prompt"),
		Handler: func(_ context.Context, a map[string]any) (string, error) {
			prompt := mcp.Str(a, "prompt")
			model := orDefault(mcp.Str(a, "model"), os.Getenv("GROK_MODEL"))
			effort := orDefault(mcp.Str(a, "effort"), defaultEffort())
			if err := ask.Prompt(prompt); err != nil {
				return "", err
			}
			if err := ask.Model(model); err != nil {
				return "", err
			}
			if err := ask.Effort(effort, efforts...); err != nil {
				return "", err
			}
			b, err := backend()
			if err != nil {
				return "", err
			}
			return runner.Run("grok", func() (string, error) {
				if b == "cli" {
					return askCLI(prompt, model, effort)
				}
				return askAPI(prompt, model, effort)
			})
		},
	})
	s.Add(runner.GetResultTool("an ask_grok"))
	s.Add(mcp.Tool{
		Name:        "grok_status",
		Description: "Report which Grok backend is ready (CLI or API key) and what is missing. Never shows secret values.",
		Handler: func(context.Context, map[string]any) (string, error) {
			bin, err := grokBin()
			cli := "found at " + bin
			if err != nil {
				cli = "NOT FOUND (" + err.Error() + ")"
			}
			b, berr := backend()
			if berr != nil {
				b = berr.Error()
			} else if os.Getenv("GROK_BACKEND") == "" {
				b += " (auto)"
			}
			key := "NOT SET"
			if os.Getenv("GROK_API_KEY") != "" {
				key = "set"
			}
			lines := []string{
				"grok-mcp " + version,
				"backend: " + b,
				"grok command: " + cli,
				"GROK_API_KEY: " + key,
				"default model: " + orDefault(os.Getenv("GROK_MODEL"), "(CLI default, or "+defaultAPIModel+" for the API)"),
				"default effort: " + orDefault(defaultEffort(), "(default)"),
				"settings file: " + cfg,
			}
			return strings.Join(lines, "\n"), nil
		},
	})

	if err := s.ServeStdio(context.Background(), os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "grok-mcp:", err)
		os.Exit(1)
	}
}
