// codex-mcp is a small MCP server (stdio) that sends a prompt to OpenAI's
// models through the Codex CLI and returns the reply. It uses whatever
// sign-in the Codex CLI already has, so no API key is needed.
//
// Each call runs `codex exec` in a fresh, empty temporary folder with a
// read-only sandbox, so Codex sees only the prompt, not your files.
//
// Tools:
//
//	ask_codex(prompt, model?, effort?)  reply from Codex
//	get_result(job_id, wait_seconds?)   collect a reply that took too long
//	codex_status()                      is the CLI installed and where
//
// Settings (environment, or ~/.config/commandcenter/codex.env):
//
//	CODEX_BIN    path to the codex binary (default: found on PATH)
//	CODEX_MODEL  default model (default: the CLI's own default)
//	CODEX_EFFORT default reasoning effort (default: the CLI's own default)
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kunalmodi/commandcenter/internal/ask"
	"github.com/kunalmodi/commandcenter/internal/env"
	"github.com/kunalmodi/commandcenter/internal/jobs"
	"github.com/kunalmodi/commandcenter/internal/mcp"
	"github.com/kunalmodi/commandcenter/internal/proc"
)

const version = "0.1.0"

var efforts = []string{"minimal", "low", "medium", "high", "xhigh"}

const askTimeout = 10 * time.Minute

func codexBin() (string, error) {
	home, _ := os.UserHomeDir()
	return proc.Find(os.Getenv("CODEX_BIN"), "codex",
		"/opt/homebrew/bin/codex", "/usr/local/bin/codex", filepath.Join(home, ".local", "bin", "codex"))
}

// buildArgs returns the codex exec arguments for one call.
func buildArgs(outFile, model, effort string) []string {
	args := []string{"exec", "--skip-git-repo-check", "--ephemeral", "--sandbox", "read-only"}
	if model != "" {
		args = append(args, "--model", model)
	}
	if effort != "" {
		args = append(args, "-c", "model_reasoning_effort="+effort)
	}
	return append(args, "-o", outFile, "-")
}

func askCodex(prompt, model, effort string) (string, error) {
	bin, err := codexBin()
	if err != nil {
		return "", fmt.Errorf("%v. Install the Codex CLI and sign in by running: codex", err)
	}
	dir, err := os.MkdirTemp("", "codex-mcp-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	// The reply file lives outside the work folder so Codex never sees it.
	f, err := os.CreateTemp("", "codex-mcp-reply-*.txt")
	if err != nil {
		return "", err
	}
	out := f.Name()
	f.Close()
	defer os.Remove(out)

	_, err = proc.Run(context.Background(), proc.Cmd{
		Name: bin, Args: buildArgs(out, model, effort), Dir: dir,
		Stdin: strings.NewReader(prompt), Timeout: askTimeout,
	})
	reply, _ := os.ReadFile(out)
	if err != nil || len(strings.TrimSpace(string(reply))) == 0 {
		msg := "codex exec returned no reply"
		if err != nil {
			msg = "codex exec failed: " + err.Error()
		}
		return "", fmt.Errorf("%s\nCheck that you are signed in (run: codex) and see: codex exec --help", msg)
	}
	label := model
	if label == "" {
		label = "default model"
	}
	return fmt.Sprintf("Model: Codex CLI (%s)\n%s", label, strings.TrimSpace(string(reply))), nil
}

func main() {
	flag.Parse()
	cfg := env.Load("codex")
	env.WidenPath()

	runner := jobs.NewRunner()
	s := mcp.New("codex", version)
	s.Instructions = "Ask OpenAI's models (via the Codex CLI) for an independent answer. Codex sees only the prompt you send, never local files, so include all the context it needs."

	s.Add(mcp.Tool{
		Name:        "ask_codex",
		Description: "Send a prompt to OpenAI's models through the Codex CLI and return the reply. Useful as a second opinion from a different model family. Codex runs in an empty folder and sees only the prompt. Slow: if it takes longer than about 40 seconds you get a job_id; collect the answer with get_result.",
		InputSchema: mcp.Object(map[string]any{
			"prompt": mcp.Prop("string", "The full prompt, including any context Codex needs."),
			"model":  mcp.Prop("string", "Optional model id. Omit to use the default."),
			"effort": map[string]any{"type": "string", "enum": efforts, "description": "Optional reasoning effort."},
		}, "prompt"),
		Handler: func(_ context.Context, a map[string]any) (string, error) {
			prompt, model, effort := mcp.Str(a, "prompt"), mcp.Str(a, "model"), mcp.Str(a, "effort")
			if model == "" {
				model = os.Getenv("CODEX_MODEL")
			}
			if effort == "" {
				effort = os.Getenv("CODEX_EFFORT")
			}
			if err := ask.Prompt(prompt); err != nil {
				return "", err
			}
			if err := ask.Model(model); err != nil {
				return "", err
			}
			if err := ask.Effort(effort, efforts...); err != nil {
				return "", err
			}
			return runner.Run("codex", func() (string, error) { return askCodex(prompt, model, effort) })
		},
	})
	s.Add(runner.GetResultTool("an ask_codex"))
	s.Add(mcp.Tool{
		Name:        "codex_status",
		Description: "Report whether the Codex CLI is installed and which settings are in effect.",
		Handler: func(context.Context, map[string]any) (string, error) {
			bin, err := codexBin()
			where := "found at " + bin
			if err != nil {
				where = "NOT FOUND (" + err.Error() + ")"
			}
			lines := []string{
				"codex-mcp " + version,
				"codex command: " + where,
				"default model: " + orDefault(os.Getenv("CODEX_MODEL")),
				"default effort: " + orDefault(os.Getenv("CODEX_EFFORT")),
				"settings file: " + cfg,
			}
			return strings.Join(lines, "\n"), nil
		},
	})

	if err := s.ServeStdio(context.Background(), os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "codex-mcp:", err)
		os.Exit(1)
	}
}

func orDefault(v string) string {
	if v == "" {
		return "(CLI default)"
	}
	return v
}
