package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/kunalmodi/commandcenter/internal/env"
	"github.com/kunalmodi/commandcenter/internal/mcp"
)

// Every HTTP caller presents its own token, and each token carries scopes, so
// each agent gets only what you gave it: one can read everything, another only
// two chats, and only one can send. The scopes are enforced here, in the
// server, whatever client the caller uses (an MCP client, curl or a script).
//
// Clients file (IMSG_CLIENTS, default ~/.config/commandcenter/imsg-clients.txt),
// one client per line, managed with "imsg-mcp token add/list/remove":
//
//	# label         scopes     token hash         optional limits
//	claude-desktop  read,send  sha256:4f2a...
//	sandbox-agent   read       sha256:9c1e...     chats=12,40
//
// Only a SHA-256 hash of each token is stored, so a process that can read the
// file still cannot use it to connect. The file is re-read on every request:
// removing a line revokes that client at once, without a restart.
//
// Scopes:
//
//	read   list_chats, chat_detail, read_history, search_messages
//	send   send_message (still needs IMSG_ALLOW_SEND=1 and the recipient allowlist)
//
// chats=ID,ID limits every read tool to those chat ids. imsg_status is always
// available and reports what the caller may do.
//
// IMSG_TOKEN, if set, still works as one full-access client named
// "IMSG_TOKEN", so old setups keep working. Prefer per-client tokens.

type scope string

const (
	scopeRead scope = "read"
	scopeSend scope = "send"
)

var knownScopes = map[scope]bool{scopeRead: true, scopeSend: true}

type client struct {
	label  string
	scopes map[scope]bool
	chats  map[int]bool // nil means every chat
	hash   string       // hex SHA-256 of the token
}

func (c *client) has(s scope) bool { return c != nil && c.scopes[s] }

// canReadChat says whether c may read chat id.
func (c *client) canReadChat(id int) bool {
	return c.has(scopeRead) && (c.chats == nil || c.chats[id])
}

func (c *client) describe() string {
	var sc []string
	for s := range c.scopes {
		sc = append(sc, string(s))
	}
	sort.Strings(sc)
	d := c.label + ": " + strings.Join(sc, ",")
	if c.chats != nil {
		d += ", chats " + joinInts(c.chats)
	} else if c.has(scopeRead) {
		d += ", all chats"
	}
	return d
}

func joinInts(m map[int]bool) string {
	ids := make([]int, 0, len(m))
	for id := range m {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	s := make([]string, len(ids))
	for i, id := range ids {
		s[i] = strconv.Itoa(id)
	}
	return strings.Join(s, ",")
}

type clientKey struct{}

func withClient(ctx context.Context, c *client) context.Context {
	return context.WithValue(ctx, clientKey{}, c)
}

// clientFrom returns the caller; nil (no access at all) if there is none.
func clientFrom(ctx context.Context) *client {
	c, _ := ctx.Value(clientKey{}).(*client)
	return c
}

// toolScope is the scope each tool needs; "" means any authenticated caller.
var toolScope = map[string]scope{
	"list_chats":      scopeRead,
	"chat_detail":     scopeRead,
	"read_history":    scopeRead,
	"search_messages": scopeRead,
	"send_message":    scopeSend,
	"imsg_status":     "",
}

// allowTool is the mcp.Server Allow hook. Unknown tools are denied.
func allowTool(ctx context.Context, tool string) bool {
	c := clientFrom(ctx)
	need, ok := toolScope[tool]
	if c == nil || !ok {
		return false
	}
	return need == "" || c.has(need)
}

func clientsPath() string {
	if p := os.Getenv("IMSG_CLIENTS"); p != "" {
		return p
	}
	return filepath.Join(env.ConfigDir(), "imsg-clients.txt")
}

var labelRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

func parseScopes(s string) (map[scope]bool, error) {
	out := map[scope]bool{}
	for _, f := range strings.Split(s, ",") {
		f = strings.ToLower(strings.TrimSpace(f))
		if f == "" {
			continue
		}
		if !knownScopes[scope(f)] {
			return nil, fmt.Errorf("unknown scope %q (use read, send)", f)
		}
		out[scope(f)] = true
	}
	if len(out) == 0 {
		return nil, errors.New("no scopes given (use read, send)")
	}
	return out, nil
}

func parseChats(s string) (map[int]bool, error) {
	out := map[int]bool{}
	for _, f := range strings.Split(s, ",") {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		id, err := strconv.Atoi(f)
		if err != nil || id <= 0 {
			return nil, fmt.Errorf("bad chat id %q", f)
		}
		out[id] = true
	}
	if len(out) == 0 {
		return nil, errors.New("chats= lists no chat ids")
	}
	return out, nil
}

// parseClients reads the clients file format. Any bad line fails the whole
// file: a typo should lock everyone out, not silently widen access.
func parseClients(r io.Reader) ([]*client, error) {
	var out []*client
	seen := map[string]bool{}
	sc := bufio.NewScanner(r)
	n := 0
	for sc.Scan() {
		n++
		line := strings.TrimSpace(sc.Text())
		if i := strings.Index(line, "#"); i >= 0 {
			line = strings.TrimSpace(line[:i])
		}
		if line == "" {
			continue
		}
		f := strings.Fields(line)
		if len(f) < 3 {
			return nil, fmt.Errorf("line %d: want: label scopes sha256:HASH [chats=ID,...]", n)
		}
		c := &client{label: f[0]}
		if !labelRE.MatchString(c.label) {
			return nil, fmt.Errorf("line %d: bad label %q", n, c.label)
		}
		if seen[c.label] {
			return nil, fmt.Errorf("line %d: duplicate label %q", n, c.label)
		}
		seen[c.label] = true
		var err error
		if c.scopes, err = parseScopes(f[1]); err != nil {
			return nil, fmt.Errorf("line %d: %v", n, err)
		}
		h, ok := strings.CutPrefix(f[2], "sha256:")
		if b, err := hex.DecodeString(h); !ok || err != nil || len(b) != sha256.Size {
			return nil, fmt.Errorf("line %d: token hash must be sha256:<64 hex digits>", n)
		}
		c.hash = strings.ToLower(h)
		for _, opt := range f[3:] {
			k, v, _ := strings.Cut(opt, "=")
			switch k {
			case "chats":
				if c.chats, err = parseChats(v); err != nil {
					return nil, fmt.Errorf("line %d: %v", n, err)
				}
			default:
				return nil, fmt.Errorf("line %d: unknown option %q", n, opt)
			}
		}
		out = append(out, c)
	}
	return out, sc.Err()
}

// loadClients reads the clients file. A missing file is no clients. A file
// that others can write is refused, since anyone who can add a line can
// grant themselves access.
func loadClients(path string) ([]*client, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if st.Mode().Perm()&0o022 != 0 {
		return nil, fmt.Errorf("%s is writable by other users; run: chmod 600 %s", path, path)
	}
	return parseClients(f)
}

func hashToken(tok string) string {
	sum := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(sum[:])
}

// match finds the client a token belongs to. Every entry is compared in
// constant time so timing does not reveal which hashes are close.
func match(clients []*client, tok string) *client {
	h := []byte(hashToken(tok))
	var found *client
	for _, c := range clients {
		if subtle.ConstantTimeCompare(h, []byte(c.hash)) == 1 && found == nil {
			found = c
		}
	}
	return found
}

func legacyClient() *client {
	tok := os.Getenv("IMSG_TOKEN")
	if tok == "" {
		return nil
	}
	return &client{label: "IMSG_TOKEN", scopes: map[scope]bool{scopeRead: true, scopeSend: true}, hash: hashToken(tok)}
}

// authenticator checks each request's bearer token against the clients file
// (re-read every time) and IMSG_TOKEN, and attaches the matching client.
func authenticator(path string) mcp.Authenticator {
	return func(r *http.Request) (context.Context, error) {
		tok, ok := mcp.BearerFrom(r)
		if !ok {
			return nil, mcp.ErrUnauthorized
		}
		clients, err := loadClients(path)
		if err != nil {
			fmt.Fprintln(os.Stderr, "imsg-mcp: ignoring the clients file until it is fixed:", err)
			clients = nil // only IMSG_TOKEN, if set, still works
		}
		if l := legacyClient(); l != nil {
			clients = append(clients, l)
		}
		c := match(clients, tok)
		if c == nil {
			return nil, mcp.ErrUnauthorized
		}
		return withClient(r.Context(), c), nil
	}
}

func newToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return "imsg_" + hex.EncodeToString(b)
}

// tokenCmd implements "imsg-mcp token add|list|remove".
func tokenCmd(args []string, stdout, stderr io.Writer) int {
	usage := func() int {
		fmt.Fprintln(stderr, `usage:
  imsg-mcp token add <label> [-scopes read|read,send] [-chats ID,ID]
  imsg-mcp token list
  imsg-mcp token remove <label>`)
		return 2
	}
	if len(args) == 0 {
		return usage()
	}
	path := clientsPath()
	clients, err := loadClients(path)
	if err != nil {
		fmt.Fprintln(stderr, "imsg-mcp:", err)
		return 1
	}
	switch args[0] {
	case "list":
		if len(clients) == 0 {
			fmt.Fprintln(stdout, "no clients in", path)
			return 0
		}
		for _, c := range clients {
			fmt.Fprintln(stdout, c.describe())
		}
		return 0

	case "add":
		if len(args) < 2 || strings.HasPrefix(args[1], "-") {
			return usage()
		}
		label := args[1]
		fs := flag.NewFlagSet("token add", flag.ContinueOnError)
		fs.SetOutput(stderr)
		scopes := fs.String("scopes", "read", "comma-separated: read, send")
		chats := fs.String("chats", "", "comma-separated chat ids this client may read (default: all)")
		if err := fs.Parse(args[2:]); err != nil {
			return 2
		}
		if !labelRE.MatchString(label) {
			fmt.Fprintln(stderr, "imsg-mcp: label must be letters, digits, '.', '_' or '-'")
			return 2
		}
		for _, c := range clients {
			if c.label == label {
				fmt.Fprintf(stderr, "imsg-mcp: %q already exists; remove it first to issue a new token\n", label)
				return 1
			}
		}
		sc, err := parseScopes(*scopes)
		if err != nil {
			fmt.Fprintln(stderr, "imsg-mcp:", err)
			return 2
		}
		var scList []string
		for s := range sc {
			scList = append(scList, string(s))
		}
		sort.Strings(scList)
		tok := newToken()
		line := fmt.Sprintf("%s\t%s\tsha256:%s", label, strings.Join(scList, ","), hashToken(tok))
		if *chats != "" {
			ids, err := parseChats(*chats)
			if err != nil {
				fmt.Fprintln(stderr, "imsg-mcp:", err)
				return 2
			}
			line += "\tchats=" + joinInts(ids)
		}
		if err := appendClientLine(path, line); err != nil {
			fmt.Fprintln(stderr, "imsg-mcp:", err)
			return 1
		}
		fmt.Fprintf(stderr, "added %q to %s\nThis token is shown once and stored only as a hash:\n\n", label, path)
		fmt.Fprintln(stdout, tok)
		fmt.Fprintf(stderr, "\nUse it as:  Authorization: Bearer <token>\n")
		return 0

	case "remove":
		if len(args) != 2 {
			return usage()
		}
		removed, err := removeClientLine(path, args[1])
		if err != nil {
			fmt.Fprintln(stderr, "imsg-mcp:", err)
			return 1
		}
		if !removed {
			fmt.Fprintf(stderr, "imsg-mcp: no client %q\n", args[1])
			return 1
		}
		fmt.Fprintf(stderr, "removed %q; its token stops working on the next request\n", args[1])
		return 0
	}
	return usage()
}

const clientsHeader = `# imsg-mcp clients. Manage with: imsg-mcp token add|list|remove
# label  scopes  sha256:<token hash>  [chats=ID,ID]
`

func appendClientLine(path, line string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	_, statErr := os.Stat(path)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if errors.Is(statErr, os.ErrNotExist) {
		if _, err := io.WriteString(f, clientsHeader); err != nil {
			return err
		}
	}
	_, err = io.WriteString(f, line+"\n")
	return err
}

func removeClientLine(path, label string) (bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var keep []string
	removed := false
	for _, line := range strings.SplitAfter(string(data), "\n") {
		if f := strings.Fields(line); len(f) > 0 && f[0] == label {
			removed = true
			continue
		}
		keep = append(keep, line)
	}
	if !removed {
		return false, nil
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(strings.Join(keep, "")), 0o600); err != nil {
		return false, err
	}
	return true, os.Rename(tmp, path)
}
