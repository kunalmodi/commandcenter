package mcp

import (
	"context"
	"crypto/subtle"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// Authenticator decides whether an HTTP request may talk to the server. It
// returns the context the request runs in, which may carry who the caller is
// (see Server.Allow), or an error to refuse it with 401.
type Authenticator func(r *http.Request) (context.Context, error)

// ErrUnauthorized is what the built-in authenticators return.
var ErrUnauthorized = errors.New("unauthorized")

// BearerToken accepts requests that send "Authorization: Bearer <token>".
func BearerToken(token string) Authenticator {
	return func(r *http.Request) (context.Context, error) {
		got, ok := BearerFrom(r)
		if !ok || subtle.ConstantTimeCompare([]byte(got), []byte(token)) != 1 {
			return nil, ErrUnauthorized
		}
		return r.Context(), nil
	}
}

// BearerFrom returns the bearer token a request carries, if any.
func BearerFrom(r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	if len(h) < 7 || !strings.EqualFold(h[:7], "bearer ") {
		return "", false
	}
	t := strings.TrimSpace(h[7:])
	return t, t != ""
}

// HTTPHandler serves MCP on a single POST endpoint (the request/response part
// of the Streamable HTTP transport; no server-initiated stream). If auth is
// non-nil, every request must pass it. Browser requests from non-localhost
// origins are refused to block DNS rebinding.
func (s *Server) HTTPHandler(auth Authenticator) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !originAllowed(r.Header.Get("Origin")) {
			http.Error(w, "forbidden origin", http.StatusForbidden)
			return
		}
		ctx := r.Context()
		if auth != nil {
			c, err := auth(r)
			if err != nil {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			ctx = c
		}
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 16<<20))
		if err != nil {
			http.Error(w, "read error", http.StatusBadRequest)
			return
		}
		resp := s.Handle(ctx, body)
		if resp == nil {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(resp)
	})
}

func originAllowed(origin string) bool {
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return true // native clients, e.g. app:// or file://
	}
	h := u.Hostname()
	return h == "localhost" || h == "127.0.0.1" || h == "::1"
}
