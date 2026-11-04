package jobs

import (
	"context"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestInlineAndBackground(t *testing.T) {
	r := NewRunner()
	r.InlineWait = 50 * time.Millisecond

	out, err := r.Run("fast", func() (string, error) { return "done", nil })
	if err != nil || out != "done" {
		t.Fatalf("fast: %q %v", out, err)
	}

	release := make(chan struct{})
	out, err = r.Run("slow", func() (string, error) { <-release; return "late answer", nil })
	if err != nil || !strings.Contains(out, "job_id:") {
		t.Fatalf("slow: %q %v", out, err)
	}
	id := regexp.MustCompile(`job_id: (\S+)`).FindStringSubmatch(out)[1]

	get := r.GetResultTool("a test").Handler
	still, _ := get(context.Background(), map[string]any{"job_id": id, "wait_seconds": 0.0})
	if !strings.Contains(still, "Still running") {
		t.Fatalf("expected still running: %q", still)
	}
	close(release)
	got, err := get(context.Background(), map[string]any{"job_id": id, "wait_seconds": 5.0})
	if err != nil || got != "late answer" {
		t.Fatalf("collect: %q %v", got, err)
	}
	if _, err := get(context.Background(), map[string]any{"job_id": id}); err == nil {
		t.Fatal("second collect should fail")
	}
}

func TestPanicBecomesError(t *testing.T) {
	r := NewRunner()
	_, err := r.Run("p", func() (string, error) { panic("x") })
	if err == nil {
		t.Fatal("expected error")
	}
}
