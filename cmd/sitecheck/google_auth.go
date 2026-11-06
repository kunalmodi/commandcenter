package main

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/kunalmodi/commandcenter/internal/env"
)

// Google Search Console access, read-only, in one of two ways:
//
//  1. A service account (recommended for agents: no browser, never expires).
//     Create one in Google Cloud, download its JSON key, and add its email as
//     a user on the Search Console property. Set GSC_SERVICE_ACCOUNT_FILE.
//  2. Your own Google login through an OAuth "Desktop app" client. Set
//     GOOGLE_OAUTH_CLIENT_FILE to the downloaded client JSON and run
//     "sitecheck auth google" once; the refresh token is saved (mode 600) in
//     ~/.config/commandcenter/sitecheck-google-token.json.

const gscScope = "https://www.googleapis.com/auth/webmasters.readonly"

var (
	googleTokenURL = "https://oauth2.googleapis.com/token"
	googleAuthURL  = "https://accounts.google.com/o/oauth2/v2/auth"
)

// tokenSource hands out a valid access token, refreshing as needed.
type tokenSource interface {
	token(ctx context.Context) (string, error)
}

type cachedToken struct {
	mu      sync.Mutex
	access  string
	expiry  time.Time
	refresh func(ctx context.Context) (string, time.Duration, error)
}

func (c *cachedToken) token(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.access != "" && time.Until(c.expiry) > time.Minute {
		return c.access, nil
	}
	tok, ttl, err := c.refresh(ctx)
	if err != nil {
		return "", err
	}
	c.access, c.expiry = tok, time.Now().Add(ttl)
	return tok, nil
}

func googleTokenPath() string {
	if p := os.Getenv("GOOGLE_TOKEN_FILE"); p != "" {
		return p
	}
	return filepath.Join(env.ConfigDir(), "sitecheck-google-token.json")
}

// googleTokens returns a token source from whatever is configured, or nil
// with a human-readable reason when nothing is.
func googleTokens() (tokenSource, string) {
	if p := os.Getenv("GSC_SERVICE_ACCOUNT_FILE"); p != "" {
		sa, err := loadServiceAccount(p)
		if err != nil {
			return nil, err.Error()
		}
		return &cachedToken{refresh: sa.exchange}, ""
	}
	if p := os.Getenv("GOOGLE_OAUTH_CLIENT_FILE"); p != "" {
		cl, err := loadOAuthClient(p)
		if err != nil {
			return nil, err.Error()
		}
		rt, err := os.ReadFile(googleTokenPath())
		if err != nil {
			return nil, "not signed in yet: run sitecheck auth google"
		}
		var saved struct {
			RefreshToken string `json:"refresh_token"`
		}
		if json.Unmarshal(rt, &saved) != nil || saved.RefreshToken == "" {
			return nil, "saved Google token is unreadable: run sitecheck auth google again"
		}
		return &cachedToken{refresh: func(ctx context.Context) (string, time.Duration, error) {
			return postToken(ctx, cl.TokenURI, url.Values{
				"grant_type": {"refresh_token"}, "refresh_token": {saved.RefreshToken},
				"client_id": {cl.ClientID}, "client_secret": {cl.ClientSecret},
			})
		}}, ""
	}
	return nil, "not set up (set GSC_SERVICE_ACCOUNT_FILE, or GOOGLE_OAUTH_CLIENT_FILE and run sitecheck auth google)"
}

// postToken calls a Google token endpoint and returns the access token.
func postToken(ctx context.Context, endpoint string, form url.Values) (string, time.Duration, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", 0, err
	}
	defer resp.Body.Close()
	var out struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
		Error        string `json:"error"`
		Description  string `json:"error_description"`
	}
	json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out)
	if out.AccessToken == "" {
		msg := out.Error
		if out.Description != "" {
			msg += ": " + out.Description
		}
		if out.Error == "invalid_grant" {
			msg += " (the sign-in expired or was revoked: run sitecheck auth google again)"
		}
		if msg == "" {
			msg = resp.Status
		}
		return "", 0, errors.New("Google token request failed: " + msg)
	}
	ttl := time.Duration(out.ExpiresIn) * time.Second
	if ttl <= 0 {
		ttl = 30 * time.Minute
	}
	return out.AccessToken, ttl, nil
}

// --- service account (JWT bearer grant) ---

type serviceAccount struct {
	ClientEmail string `json:"client_email"`
	PrivateKey  string `json:"private_key"`
	TokenURI    string `json:"token_uri"`
	key         *rsa.PrivateKey
}

func loadServiceAccount(path string) (*serviceAccount, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("service account file: %v", err)
	}
	var sa serviceAccount
	if err := json.Unmarshal(b, &sa); err != nil || sa.ClientEmail == "" || sa.PrivateKey == "" {
		return nil, errors.New("service account file is not a Google service account JSON key")
	}
	block, _ := pem.Decode([]byte(sa.PrivateKey))
	if block == nil {
		return nil, errors.New("service account private_key is not PEM")
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		if k1, err1 := x509.ParsePKCS1PrivateKey(block.Bytes); err1 == nil {
			k, err = k1, nil
		}
	}
	rk, ok := k.(*rsa.PrivateKey)
	if err != nil || !ok {
		return nil, errors.New("service account private_key is not an RSA key")
	}
	sa.key = rk
	if sa.TokenURI == "" {
		sa.TokenURI = googleTokenURL
	}
	return &sa, nil
}

func (sa *serviceAccount) assertion(now time.Time) (string, error) {
	enc := base64.RawURLEncoding
	header := enc.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	claims, _ := json.Marshal(map[string]any{
		"iss": sa.ClientEmail, "scope": gscScope, "aud": sa.TokenURI,
		"iat": now.Unix(), "exp": now.Add(time.Hour).Unix(),
	})
	unsigned := header + "." + enc.EncodeToString(claims)
	sum := sha256.Sum256([]byte(unsigned))
	sig, err := rsa.SignPKCS1v15(rand.Reader, sa.key, crypto.SHA256, sum[:])
	if err != nil {
		return "", err
	}
	return unsigned + "." + enc.EncodeToString(sig), nil
}

func (sa *serviceAccount) exchange(ctx context.Context) (string, time.Duration, error) {
	jwt, err := sa.assertion(time.Now())
	if err != nil {
		return "", 0, err
	}
	return postToken(ctx, sa.TokenURI, url.Values{
		"grant_type": {"urn:ietf:params:oauth:grant-type:jwt-bearer"}, "assertion": {jwt},
	})
}

// --- OAuth desktop flow (loopback redirect + PKCE) ---

type oauthClient struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	TokenURI     string `json:"token_uri"`
}

func loadOAuthClient(path string) (*oauthClient, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("OAuth client file: %v", err)
	}
	var wrap struct {
		Installed *oauthClient `json:"installed"`
		Web       *oauthClient `json:"web"`
	}
	if json.Unmarshal(b, &wrap) != nil || wrap.Installed == nil {
		if wrap.Web != nil {
			return nil, errors.New(`OAuth client is a "Web application"; create a "Desktop app" client instead`)
		}
		return nil, errors.New("OAuth client file is not a Google Desktop app client JSON")
	}
	c := wrap.Installed
	if c.TokenURI == "" {
		c.TokenURI = googleTokenURL
	}
	return c, nil
}

func randomString(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// authGoogle runs the one-time browser sign-in and saves the refresh token.
func authGoogle(ctx context.Context, stdout io.Writer) error {
	p := os.Getenv("GOOGLE_OAUTH_CLIENT_FILE")
	if p == "" {
		return errors.New("set GOOGLE_OAUTH_CLIENT_FILE in ~/.config/commandcenter/sitecheck.env first (or use a service account; see the README)")
	}
	cl, err := loadOAuthClient(p)
	if err != nil {
		return err
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	defer ln.Close()
	redirect := "http://" + ln.Addr().String()
	verifier, state := randomString(48), randomString(16)
	challenge := sha256.Sum256([]byte(verifier))
	authURL := googleAuthURL + "?" + url.Values{
		"client_id": {cl.ClientID}, "redirect_uri": {redirect}, "response_type": {"code"},
		"scope": {gscScope}, "access_type": {"offline"}, "prompt": {"consent"}, "state": {state},
		"code_challenge": {base64.RawURLEncoding.EncodeToString(challenge[:])}, "code_challenge_method": {"S256"},
	}.Encode()

	codes := make(chan string, 1)
	errs := make(chan error, 1)
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		switch {
		case q.Get("state") != state:
			http.Error(w, "state mismatch", http.StatusBadRequest)
			return
		case q.Get("error") != "":
			io.WriteString(w, "Sign-in cancelled. You can close this tab.")
			errs <- errors.New("sign-in failed: " + q.Get("error"))
		default:
			io.WriteString(w, "Signed in. You can close this tab and return to the terminal.")
			codes <- q.Get("code")
		}
	})}
	go srv.Serve(ln)
	defer srv.Close()

	fmt.Fprintln(stdout, "Opening your browser to sign in to Google (read-only Search Console access).")
	fmt.Fprintln(stdout, "If it doesn't open, visit:\n\n  "+authURL+"\n")
	exec.Command("open", authURL).Start()

	var code string
	select {
	case code = <-codes:
	case err := <-errs:
		return err
	case <-time.After(5 * time.Minute):
		return errors.New("timed out waiting for the sign-in")
	case <-ctx.Done():
		return ctx.Err()
	}

	form := url.Values{
		"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {redirect},
		"client_id": {cl.ClientID}, "client_secret": {cl.ClientSecret}, "code_verifier": {verifier},
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, cl.TokenURI, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var out struct {
		RefreshToken string `json:"refresh_token"`
		Error        string `json:"error"`
		Description  string `json:"error_description"`
	}
	json.NewDecoder(resp.Body).Decode(&out)
	if out.RefreshToken == "" {
		return fmt.Errorf("no refresh token from Google: %s %s", out.Error, out.Description)
	}
	path := googleTokenPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, _ := json.Marshal(map[string]string{"refresh_token": out.RefreshToken})
	if err := os.WriteFile(path, b, 0o600); err != nil {
		return err
	}
	fmt.Fprintln(stdout, "Saved to", path)
	fmt.Fprintln(stdout, `Note: if the OAuth app's publishing status is "Testing", Google expires this sign-in after 7 days. Set it to "In production" to keep it; for your own account you can click past Google's "unverified app" warning.`)
	return nil
}
