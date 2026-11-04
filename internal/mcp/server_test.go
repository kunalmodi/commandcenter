package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func testServer() *Server {
	s := New("test", "1.0")
	s.Add(Tool{Name: "echo", Handler: func(_ context.Context, a map[string]any) (string, error) { return Str(a, "x"), nil }})
	s.Add(Tool{Name: "fail", Handler: func(context.Context, map[string]any) (string, error) { return "", errors.New("boom") }})
	s.Add(Tool{Name: "panic", Handler: func(context.Context, map[string]any) (string, error) { panic("oops") }})
	return s
}

func call(t *testing.T, s *Server, msg string) map[string]any {
	t.Helper()
	out := s.Handle(context.Background(), []byte(msg))
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("bad reply %q: %v", out, err)
	}
	return m
}

func TestInitializeNegotiates(t *testing.T) {
	s := testServer()
	r := call(t, s, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05"}}`)
	if r["result"].(map[string]any)["protocolVersion"] != "2024-11-05" {
		t.Fatalf("%v", r)
	}
	r = call(t, s, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"1999-01-01"}}`)
	if r["result"].(map[string]any)["protocolVersion"] != LatestProtocol {
		t.Fatalf("%v", r)
	}
}

func TestToolsCall(t *testing.T) {
	s := testServer()
	r := call(t, s, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"echo","arguments":{"x":"hi"}}}`)
	res := r["result"].(map[string]any)
	if res["isError"] != false || res["content"].([]any)[0].(map[string]any)["text"] != "hi" {
		t.Fatalf("%v", r)
	}
	for _, name := range []string{"fail", "panic"} {
		r = call(t, s, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"`+name+`"}}`)
		if r["result"].(map[string]any)["isError"] != true {
			t.Fatalf("%s: %v", name, r)
		}
	}
	r = call(t, s, `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"nope"}}`)
	if r["error"] == nil {
		t.Fatalf("%v", r)
	}
}

func TestNotificationsAndBatch(t *testing.T) {
	s := testServer()
	if out := s.Handle(context.Background(), []byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)); out != nil {
		t.Fatalf("notification got reply %s", out)
	}
	out := s.Handle(context.Background(), []byte(`[{"jsonrpc":"2.0","id":1,"method":"ping"},{"jsonrpc":"2.0","method":"notifications/initialized"},{"jsonrpc":"2.0","id":2,"method":"tools/list"}]`))
	var arr []any
	if err := json.Unmarshal(out, &arr); err != nil || len(arr) != 2 {
		t.Fatalf("batch: %s", out)
	}
	r := call(t, s, `not json`)
	if r["error"] == nil {
		t.Fatal("parse error expected")
	}
}

func TestStdio(t *testing.T) {
	s := testServer()
	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}` + "\n\n" + `{"jsonrpc":"2.0","id":2,"method":"tools/list"}` + "\n")
	var out bytes.Buffer
	if err := s.ServeStdio(context.Background(), in, &out); err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(strings.TrimSpace(out.String()), "\n") + 1; n != 2 {
		t.Fatalf("want 2 replies, got %d: %s", n, out.String())
	}
}

func TestHTTPAuthAndOrigin(t *testing.T) {
	h := testServer().HTTPHandler(BearerToken("secret"))
	do := func(method, auth, origin, body string) int {
		req := httptest.NewRequest(method, "/mcp", strings.NewReader(body))
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	ping := `{"jsonrpc":"2.0","id":1,"method":"ping"}`
	if c := do("POST", "", "", ping); c != http.StatusUnauthorized {
		t.Fatalf("no token: %d", c)
	}
	if c := do("POST", "Bearer wrong", "", ping); c != http.StatusUnauthorized {
		t.Fatalf("wrong token: %d", c)
	}
	if c := do("POST", "Bearer secret", "https://evil.example", ping); c != http.StatusForbidden {
		t.Fatalf("evil origin: %d", c)
	}
	if c := do("POST", "Bearer secret", "http://localhost:3000", ping); c != http.StatusOK {
		t.Fatalf("ok: %d", c)
	}
	if c := do("GET", "Bearer secret", "", ""); c != http.StatusMethodNotAllowed {
		t.Fatalf("GET: %d", c)
	}
	if c := do("POST", "Bearer secret", "", `{"jsonrpc":"2.0","method":"notifications/initialized"}`); c != http.StatusAccepted {
		t.Fatalf("notification: %d", c)
	}
}

type callerKey struct{}

func TestAllowFiltersTools(t *testing.T) {
	s := testServer()
	s.Allow = func(ctx context.Context, tool string) bool {
		return tool == "echo" || ctx.Value(callerKey{}) == "admin"
	}
	list := func(ctx context.Context) int {
		var m map[string]any
		json.Unmarshal(s.Handle(ctx, []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)), &m)
		return len(m["result"].(map[string]any)["tools"].([]any))
	}
	if n := list(context.Background()); n != 1 {
		t.Fatalf("limited caller sees %d tools", n)
	}
	if n := list(context.WithValue(context.Background(), callerKey{}, "admin")); n != 3 {
		t.Fatalf("admin sees %d tools", n)
	}
	r := call(t, s, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"fail"}}`)
	if r["error"] == nil {
		t.Fatalf("hidden tool was callable: %v", r)
	}
}

func TestBearerFrom(t *testing.T) {
	for h, want := range map[string]string{"Bearer abc": "abc", "bearer  abc ": "abc", "Basic abc": "", "Bearer ": "", "": ""} {
		req := httptest.NewRequest("POST", "/mcp", nil)
		req.Header.Set("Authorization", h)
		if got, _ := BearerFrom(req); got != want {
			t.Errorf("%q: got %q want %q", h, got, want)
		}
	}
}
