package main

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net/http"
	"net/url"
	"time"
)

const maxBody = 8 << 20

type page struct {
	requested string
	final     *url.URL
	status    int
	header    http.Header
	body      string
	truncated bool
	redirects []string // every URL visited before the final one
	proto     string
	tls       *tls.ConnectionState
	elapsed   time.Duration
}

type fetcher struct {
	client  *http.Client
	timeout time.Duration
	ua      string
}

func newFetcher(timeout time.Duration, ua string) *fetcher {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.ForceAttemptHTTP2 = true
	return &fetcher{
		timeout: timeout,
		ua:      ua,
		client: &http.Client{
			Transport: tr,
			// Redirects are followed by hand so the chain can be reported.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
}

// do sends one request, following up to 10 redirects, and reads at most
// limit bytes of the final body.
func (f *fetcher) do(ctx context.Context, method, rawURL, ua string, limit int64, hdr map[string]string) (*page, error) {
	if ua == "" {
		ua = f.ua
	}
	ctx, cancel := context.WithTimeout(ctx, f.timeout)
	defer cancel()
	p := &page{requested: rawURL}
	start := time.Now()
	cur := rawURL
	for hop := 0; ; hop++ {
		if hop > 10 {
			return nil, errors.New("more than 10 redirects")
		}
		req, err := http.NewRequestWithContext(ctx, method, cur, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", ua)
		req.Header.Set("Accept", "text/html,application/xhtml+xml,*/*;q=0.8")
		req.Header.Set("Accept-Language", "en-US,en;q=0.9")
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		resp, err := f.client.Do(req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode >= 300 && resp.StatusCode < 400 && resp.Header.Get("Location") != "" {
			resp.Body.Close()
			next, err := req.URL.Parse(resp.Header.Get("Location"))
			if err != nil {
				return nil, err
			}
			p.redirects = append(p.redirects, cur)
			cur = next.String()
			continue
		}
		defer resp.Body.Close()
		p.final = req.URL
		p.status = resp.StatusCode
		p.header = resp.Header
		p.proto = resp.Proto
		p.tls = resp.TLS
		if limit > 0 {
			b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
			if err != nil {
				return nil, err
			}
			if int64(len(b)) > limit {
				b, p.truncated = b[:limit], true
			}
			p.body = string(b)
		}
		p.elapsed = time.Since(start)
		return p, nil
	}
}

func (f *fetcher) get(ctx context.Context, u, ua string) (*page, error) {
	return f.do(ctx, http.MethodGet, u, ua, maxBody, nil)
}

// status checks a URL cheaply: HEAD, falling back to a small GET for
// servers that refuse HEAD.
func (f *fetcher) status(ctx context.Context, u string) (*page, error) {
	p, err := f.do(ctx, http.MethodHead, u, "", 0, nil)
	if err == nil && p.status != http.StatusMethodNotAllowed && p.status != http.StatusNotImplemented && p.status != http.StatusForbidden {
		return p, nil
	}
	return f.do(ctx, http.MethodGet, u, "", 64<<10, nil)
}
