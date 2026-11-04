// Package mcp is a minimal, standard-library-only Model Context Protocol
// server: JSON-RPC 2.0 over stdio (one message per line) or over HTTP on a
// single POST endpoint. It supports exactly what these servers need: the
// initialize handshake, ping, tools/list and tools/call.
package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
)

// LatestProtocol is the newest MCP protocol revision this package speaks.
const LatestProtocol = "2025-06-18"

var knownProtocols = map[string]bool{
	"2024-11-05": true,
	"2025-03-26": true,
	"2025-06-18": true,
}

// Handler runs one tool call. A returned error becomes a tool result with
// isError set, so the model sees the message rather than a protocol failure.
type Handler func(ctx context.Context, args map[string]any) (string, error)

// Tool is one tool the server offers.
type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
	Handler     Handler        `json:"-"`
}

// Server holds the tools and answers JSON-RPC requests.
type Server struct {
	Name         string
	Version      string
	Instructions string

	// Allow, if set, decides per request which tools the caller may see and
	// call. Tools it rejects are left out of tools/list and refused by
	// tools/call as if they did not exist. ctx is the request's context, so
	// it can carry who the caller is (see Authenticator).
	Allow func(ctx context.Context, tool string) bool

	mu    sync.RWMutex
	tools []Tool
}

// New returns an empty server.
func New(name, version string) *Server { return &Server{Name: name, Version: version} }

// Add registers a tool.
func (s *Server) Add(t Tool) {
	if t.InputSchema == nil {
		t.InputSchema = Object(nil)
	}
	s.mu.Lock()
	s.tools = append(s.tools, t)
	s.mu.Unlock()
}

// Tools returns a copy of the registered tools.
func (s *Server) Tools() []Tool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]Tool(nil), s.tools...)
}

// ToolsFor returns the tools this request's caller may use.
func (s *Server) ToolsFor(ctx context.Context) []Tool {
	all := s.Tools()
	if s.Allow == nil {
		return all
	}
	out := all[:0]
	for _, t := range all {
		if s.Allow(ctx, t.Name) {
			out = append(out, t)
		}
	}
	return out
}

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// handleOne answers a single request; nil means it was a notification.
func (s *Server) handleOne(ctx context.Context, req request) *response {
	isRequest := len(req.ID) > 0 && string(req.ID) != "null"
	reply := func(result any) *response {
		if !isRequest {
			return nil
		}
		return &response{JSONRPC: "2.0", ID: req.ID, Result: result}
	}
	fail := func(code int, msg string) *response {
		if !isRequest {
			return nil
		}
		return &response{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: code, Message: msg}}
	}

	switch req.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(req.Params, &p)
		version := LatestProtocol
		if knownProtocols[p.ProtocolVersion] {
			version = p.ProtocolVersion
		}
		result := map[string]any{
			"protocolVersion": version,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": s.Name, "version": s.Version},
		}
		if s.Instructions != "" {
			result["instructions"] = s.Instructions
		}
		return reply(result)
	case "ping":
		return reply(map[string]any{})
	case "tools/list":
		return reply(map[string]any{"tools": s.ToolsFor(ctx)})
	case "tools/call":
		var p struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return fail(-32602, "invalid params: "+err.Error())
		}
		if p.Arguments == nil {
			p.Arguments = map[string]any{}
		}
		for _, t := range s.ToolsFor(ctx) {
			if t.Name != p.Name {
				continue
			}
			text, err := safeCall(ctx, t.Handler, p.Arguments)
			if err != nil {
				text = err.Error()
			}
			if text == "" {
				text = "(no output)"
			}
			return reply(map[string]any{
				"content": []map[string]any{{"type": "text", "text": text}},
				"isError": err != nil,
			})
		}
		return fail(-32602, "unknown tool: "+p.Name)
	default:
		if strings.HasPrefix(req.Method, "notifications/") {
			return nil
		}
		return fail(-32601, "method not found: "+req.Method)
	}
}

// safeCall keeps one misbehaving tool from taking the whole server down.
func safeCall(ctx context.Context, h Handler, args map[string]any) (text string, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("internal error: %v", r)
		}
	}()
	return h(ctx, args)
}

// Handle answers one raw JSON-RPC message (single or batch). It returns nil
// when there is nothing to send back.
func (s *Server) Handle(ctx context.Context, raw []byte) []byte {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return nil
	}
	if raw[0] == '[' {
		var reqs []request
		if err := json.Unmarshal(raw, &reqs); err != nil {
			return mustJSON(response{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcError{Code: -32700, Message: "parse error"}})
		}
		var out []*response
		for _, rq := range reqs {
			if r := s.handleOne(ctx, rq); r != nil {
				out = append(out, r)
			}
		}
		if len(out) == 0 {
			return nil
		}
		return mustJSON(out)
	}
	var req request
	if err := json.Unmarshal(raw, &req); err != nil {
		return mustJSON(response{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcError{Code: -32700, Message: "parse error"}})
	}
	if r := s.handleOne(ctx, req); r != nil {
		return mustJSON(r)
	}
	return nil
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		return []byte(`{"jsonrpc":"2.0","id":null,"error":{"code":-32603,"message":"encode error"}}`)
	}
	return b
}

// ServeStdio reads one message per line from in and writes replies to out.
// Each request runs on its own goroutine so a slow tool never blocks others.
func (s *Server) ServeStdio(ctx context.Context, in io.Reader, out io.Writer) error {
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 0, 64*1024), 16<<20)
	var writeMu sync.Mutex
	var wg sync.WaitGroup
	for sc.Scan() {
		line := append([]byte(nil), sc.Bytes()...)
		wg.Add(1)
		go func() {
			defer wg.Done()
			if resp := s.Handle(ctx, line); resp != nil {
				writeMu.Lock()
				out.Write(append(resp, '\n'))
				writeMu.Unlock()
			}
		}()
	}
	wg.Wait()
	return sc.Err()
}

// Object builds a JSON Schema object with the given properties.
func Object(props map[string]any, required ...string) map[string]any {
	if props == nil {
		props = map[string]any{}
	}
	m := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		m["required"] = required
	}
	return m
}

// Prop builds one JSON Schema property.
func Prop(typ, description string) map[string]any {
	return map[string]any{"type": typ, "description": description}
}

// Str, Int and Bool read loosely typed arguments.
func Str(args map[string]any, key string) string {
	s, _ := args[key].(string)
	return s
}

func Int(args map[string]any, key string, def int) int {
	switch v := args[key].(type) {
	case float64:
		return int(v)
	case string:
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil {
			return n
		}
	}
	return def
}

func Bool(args map[string]any, key string) bool {
	b, _ := args[key].(bool)
	return b
}
