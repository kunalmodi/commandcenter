package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseClients(t *testing.T) {
	h := hashToken("x")
	good := "# comment\nalpha read,send sha256:" + h + "\nbeta read sha256:" + hashToken("y") + " chats=3,1 # note\n"
	cs, err := parseClients(strings.NewReader(good))
	if err != nil || len(cs) != 2 {
		t.Fatalf("%v %v", cs, err)
	}
	if !cs[0].has(scopeSend) || cs[1].has(scopeSend) || !cs[1].canReadChat(3) || cs[1].canReadChat(2) {
		t.Fatalf("scopes wrong: %+v %+v", cs[0], cs[1])
	}
	for _, bad := range []string{
		"alpha read",                                  // no hash
		"alpha admin sha256:" + h,                     // unknown scope
		"alpha read sha256:abc",                       // short hash
		"alpha read " + h,                             // missing prefix
		"al pha!x read sha256:" + h,                   // bad label
		"a read sha256:" + h + "\na read sha256:" + h, // duplicate
		"a read sha256:" + h + " chats=",              // empty chats
		"a read sha256:" + h + " chats=x",             // bad id
		"a read sha256:" + h + " color=red",           // unknown option
	} {
		if _, err := parseClients(strings.NewReader(bad)); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestAllowTool(t *testing.T) {
	reader := withClient(context.Background(), &client{label: "r", scopes: map[scope]bool{scopeRead: true}})
	sender := withClient(context.Background(), &client{label: "s", scopes: map[scope]bool{scopeSend: true}})
	cases := []struct {
		ctx  context.Context
		tool string
		want bool
	}{
		{reader, "read_history", true},
		{reader, "send_message", false},
		{reader, "imsg_status", true},
		{sender, "read_history", false},
		{sender, "send_message", true},
		{reader, "made_up_tool", false},
		{context.Background(), "imsg_status", false},
	}
	for _, c := range cases {
		if got := allowTool(c.ctx, c.tool); got != c.want {
			t.Errorf("%s: got %v", c.tool, got)
		}
	}
}

func TestChatLimitsOnTools(t *testing.T) {
	a := &app{otp: otpRedact}
	limited := withClient(context.Background(), &client{label: "l", scopes: map[scope]bool{scopeRead: true}, chats: map[int]bool{7: true}})
	for _, h := range []func(context.Context, map[string]any) (string, error){a.readHistory, a.chatDetail} {
		if _, err := h(limited, map[string]any{"chat_id": float64(8)}); err == nil || !strings.Contains(err.Error(), "not available") {
			t.Fatalf("other chat readable: %v", err)
		}
	}
	noRead := withClient(context.Background(), &client{label: "s", scopes: map[scope]bool{scopeSend: true}})
	if _, err := a.listChats(noRead, map[string]any{}); err == nil {
		t.Fatal("send-only client listed chats")
	}
}

func writeClients(t *testing.T, body string, mode os.FileMode) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "imsg-clients.txt")
	if err := os.WriteFile(p, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
	os.Chmod(p, mode)
	return p
}

func TestLoadClientsRefusesWritableFile(t *testing.T) {
	p := writeClients(t, "a read sha256:"+hashToken("x")+"\n", 0o666)
	if _, err := loadClients(p); err == nil {
		t.Fatal("group/world-writable file accepted")
	}
	if cs, err := loadClients(filepath.Join(t.TempDir(), "missing")); err != nil || cs != nil {
		t.Fatalf("missing file: %v %v", cs, err)
	}
}

func TestAuthenticator(t *testing.T) {
	t.Setenv("IMSG_TOKEN", "legacy")
	p := writeClients(t, "agent read sha256:"+hashToken("good")+"\n", 0o600)
	auth := authenticator(p)
	try := func(h string) *client {
		r := httptest.NewRequest(http.MethodPost, "/mcp", nil)
		if h != "" {
			r.Header.Set("Authorization", h)
		}
		ctx, err := auth(r)
		if err != nil {
			return nil
		}
		return clientFrom(ctx)
	}
	if c := try("Bearer good"); c == nil || c.label != "agent" || c.has(scopeSend) {
		t.Fatalf("good token: %+v", c)
	}
	if c := try("Bearer legacy"); c == nil || !c.has(scopeSend) {
		t.Fatalf("legacy token: %+v", c)
	}
	for _, h := range []string{"", "Bearer bad", "Bearer " + hashToken("good"), "good"} {
		if c := try(h); c != nil {
			t.Errorf("%q let in as %s", h, c.label)
		}
	}
	// Revocation takes effect on the next request.
	os.WriteFile(p, []byte("# empty\n"), 0o600)
	if c := try("Bearer good"); c != nil {
		t.Fatal("removed client still accepted")
	}
}

func TestTokenCommand(t *testing.T) {
	t.Setenv("IMSG_CLIENTS", filepath.Join(t.TempDir(), "sub", "imsg-clients.txt"))
	var out, errb bytes.Buffer
	if code := tokenCmd([]string{"add", "bot", "-scopes", "read,send", "-chats", "5,2"}, &out, &errb); code != 0 {
		t.Fatalf("add: %d %s", code, errb.String())
	}
	tok := strings.TrimSpace(out.String())
	data, _ := os.ReadFile(clientsPath())
	if strings.Contains(string(data), tok) {
		t.Fatal("plaintext token written to disk")
	}
	if st, _ := os.Stat(clientsPath()); st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", st.Mode().Perm())
	}
	cs, err := loadClients(clientsPath())
	if err != nil || len(cs) != 1 || match(cs, tok) == nil || !cs[0].canReadChat(5) || cs[0].canReadChat(3) {
		t.Fatalf("after add: %v %v", cs, err)
	}
	if code := tokenCmd([]string{"add", "bot"}, &out, &errb); code == 0 {
		t.Fatal("duplicate label accepted")
	}
	if code := tokenCmd([]string{"remove", "bot"}, &out, &errb); code != 0 {
		t.Fatalf("remove: %s", errb.String())
	}
	if cs, _ := loadClients(clientsPath()); len(cs) != 0 {
		t.Fatalf("still there: %v", cs)
	}
}
