package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCLIArgsOnlyKnownFlags(t *testing.T) {
	help := "Usage: grok [options]\n  -p, --print\n  --output-format <fmt>\n  --reasoning-effort <level>\n  --no-memory"
	got := strings.Join(cliArgs(help, "hi", "/tmp/w", "", "high"), " ")
	want := "-p hi --output-format plain --no-memory --reasoning-effort high"
	if got != want {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
}

func TestAskAPI(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer k" || r.URL.Path != "/chat/completions" {
			http.Error(w, "bad", 401)
			return
		}
		json.NewDecoder(r.Body).Decode(&body)
		w.Write([]byte(`{"choices":[{"message":{"content":" hello there "}}]}`))
	}))
	defer srv.Close()
	t.Setenv("GROK_API_BASE", srv.URL)
	t.Setenv("GROK_API_KEY", "k")
	out, err := askAPI("hi", "", "medium")
	if err != nil || out != "Model: grok-4.7 (API) (effort: medium)\nhello there" {
		t.Fatalf("%q %v", out, err)
	}
	if body["reasoning_effort"] != "medium" || body["model"] != "grok-4.7" {
		t.Fatalf("payload %v", body)
	}
	t.Setenv("GROK_API_KEY", "wrong")
	_, err = askAPI("hi", "", "")
	if err == nil || !strings.Contains(err.Error(), "HTTP 401") {
		t.Fatalf("want 401 error, got %v", err)
	}
	if strings.Contains(err.Error(), "wrong") {
		t.Fatal("error leaked the key")
	}
}
