// Package jobs runs slow tool calls in the background. Some MCP clients give
// up on a tool call after about 60 seconds, and a model can take longer than
// that. A call waits up to InlineWait; if the work finishes in time the
// answer comes back directly, otherwise the caller gets a job_id to collect
// later with the get_result tool. Jobs live in memory only.
package jobs

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kunalmodi/commandcenter/internal/mcp"
)

type job struct {
	id       string
	started  time.Time
	finished time.Time
	done     chan struct{}
	text     string
	err      error
}

// Runner tracks background jobs.
type Runner struct {
	InlineWait time.Duration // how long a call waits before returning a job_id
	MaxWait    time.Duration // longest get_result may wait
	Keep       time.Duration // how long an uncollected answer is kept

	mu   sync.Mutex
	jobs map[string]*job
	seq  int
}

// NewRunner returns a Runner with defaults kept well under a 60 second
// client limit. JOBS_INLINE_WAIT (seconds) overrides InlineWait.
func NewRunner() *Runner {
	r := &Runner{InlineWait: 40 * time.Second, MaxWait: 50 * time.Second, Keep: time.Hour, jobs: map[string]*job{}}
	if v, err := strconv.Atoi(os.Getenv("JOBS_INLINE_WAIT")); err == nil && v > 0 {
		r.InlineWait = time.Duration(v) * time.Second
	}
	return r
}

// Run starts fn and waits up to InlineWait for it.
func (r *Runner) Run(prefix string, fn func() (string, error)) (string, error) {
	j := r.start(prefix, fn)
	if wait(j, r.InlineWait) {
		return r.collect(j)
	}
	return stillRunning(j), nil
}

func (r *Runner) start(prefix string, fn func() (string, error)) *job {
	r.mu.Lock()
	for id, j := range r.jobs {
		if !j.finished.IsZero() && time.Since(j.finished) > r.Keep {
			delete(r.jobs, id)
		}
	}
	r.seq++
	j := &job{
		id:      fmt.Sprintf("%s-%d-%d", prefix, time.Now().Unix()%100000, r.seq),
		started: time.Now(),
		done:    make(chan struct{}),
	}
	r.jobs[j.id] = j
	r.mu.Unlock()

	go func() {
		var text string
		var err error
		func() {
			defer func() {
				if p := recover(); p != nil {
					err = fmt.Errorf("internal error: %v", p)
				}
			}()
			text, err = fn()
		}()
		r.mu.Lock()
		j.text, j.err, j.finished = text, err, time.Now()
		r.mu.Unlock()
		close(j.done)
	}()
	return j
}

func wait(j *job, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-j.done:
		return true
	case <-t.C:
		return false
	}
}

func (r *Runner) collect(j *job) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.jobs, j.id)
	return j.text, j.err
}

func stillRunning(j *job) string {
	return fmt.Sprintf("Still running (%ds so far). job_id: %s\nCall get_result with this job_id to collect the answer. It can take a few minutes.",
		int(time.Since(j.started).Seconds()), j.id)
}

// GetResultTool is the tool that collects a job's answer.
func (r *Runner) GetResultTool(what string) mcp.Tool {
	return mcp.Tool{
		Name:        "get_result",
		Description: "Collect the answer to " + what + " call that returned a job_id because it was still running. Waits up to wait_seconds (default 40, at most 50); if it is still not done, call again.",
		InputSchema: mcp.Object(map[string]any{
			"job_id":       mcp.Prop("string", "The job_id from the earlier reply."),
			"wait_seconds": mcp.Prop("number", "How long to wait, 1 to 50 seconds. Default 40."),
		}, "job_id"),
		Handler: func(_ context.Context, args map[string]any) (string, error) {
			id := strings.TrimSpace(mcp.Str(args, "job_id"))
			if id == "" {
				return "", errors.New("job_id is required")
			}
			d := r.InlineWait
			if v, ok := args["wait_seconds"].(float64); ok {
				d = time.Duration(v * float64(time.Second))
			}
			if d < time.Second {
				d = time.Second
			}
			if d > r.MaxWait {
				d = r.MaxWait
			}
			r.mu.Lock()
			j, ok := r.jobs[id]
			r.mu.Unlock()
			if !ok {
				return "", fmt.Errorf("unknown job_id %q: it was already collected, or the server restarted (then run the request again)", id)
			}
			if wait(j, d) {
				return r.collect(j)
			}
			return stillRunning(j), nil
		},
	}
}
