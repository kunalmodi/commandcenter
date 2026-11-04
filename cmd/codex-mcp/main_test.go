package main

import (
	"strings"
	"testing"
)

func TestBuildArgs(t *testing.T) {
	got := strings.Join(buildArgs("/tmp/o", "gpt-x", "low"), " ")
	want := "exec --skip-git-repo-check --ephemeral --sandbox read-only --model gpt-x -c model_reasoning_effort=low -o /tmp/o -"
	if got != want {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
	if got := strings.Join(buildArgs("/tmp/o", "", ""), " "); got != "exec --skip-git-repo-check --ephemeral --sandbox read-only -o /tmp/o -" {
		t.Fatal(got)
	}
}
