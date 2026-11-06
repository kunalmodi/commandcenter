package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

func checkBasics(ctx context.Context, r *report, f *fetcher, pg *page, d *doc) {
	const a = "basics"
	switch {
	case pg.status == 200:
		r.add(a, pass, "HTTP status", "200")
	case pg.status >= 200 && pg.status < 300:
		r.add(a, warn, "HTTP status", "%d (expected 200)", pg.status)
	default:
		r.add(a, fail, "HTTP status", "%d", pg.status)
	}
	if ct := pg.header.Get("Content-Type"); !strings.Contains(strings.ToLower(ct), "text/html") {
		r.add(a, warn, "Content-Type", "%q, not text/html", ct)
	}

	if pg.final.Scheme != "https" {
		r.add(a, fail, "HTTPS", "page is served over plain http")
	} else {
		r.add(a, pass, "HTTPS", "")
	}
	switch n := len(pg.redirects); {
	case n == 0:
	case n == 1:
		r.add(a, info, "Redirects", "%s → %s", pg.redirects[0], pg.final)
	default:
		r.add(a, warn, "Redirect chain", "%d hops (each costs a round trip): %s → %s", n, strings.Join(pg.redirects, " → "), pg.final)
	}

	r.fact("response_time_ms", pg.elapsed.Milliseconds())
	r.fact("html_bytes", len(pg.body))
	switch {
	case pg.elapsed > 1800*time.Millisecond:
		r.add(a, warn, "Response time", "%dms for the HTML (aim for under 800ms)", pg.elapsed.Milliseconds())
	case pg.elapsed > 800*time.Millisecond:
		r.add(a, info, "Response time", "%dms for the HTML (aim for under 800ms)", pg.elapsed.Milliseconds())
	default:
		r.add(a, pass, "Response time", "%dms", pg.elapsed.Milliseconds())
	}
	if pg.truncated {
		r.add(a, warn, "HTML size", "over %d MB; only the first part was checked", maxBody>>20)
	} else if len(pg.body) > 500<<10 {
		r.add(a, warn, "HTML size", "%d KB of HTML (large; often inlined data or CSS)", len(pg.body)>>10)
	}

	if strings.HasPrefix(pg.proto, "HTTP/2") || strings.HasPrefix(pg.proto, "HTTP/3") {
		r.add(a, pass, "HTTP version", pg.proto)
	} else {
		r.add(a, info, "HTTP version", "%s (HTTP/2 or later is faster for pages with many assets)", pg.proto)
	}

	if pg.tls != nil && len(pg.tls.PeerCertificates) > 0 {
		cert := pg.tls.PeerCertificates[0]
		days := int(time.Until(cert.NotAfter).Hours() / 24)
		r.fact("tls_expires", cert.NotAfter.Format("2006-01-02"))
		switch {
		case days < 0:
			r.add(a, fail, "TLS certificate", "expired on %s", cert.NotAfter.Format("2006-01-02"))
		case days < 21:
			r.add(a, warn, "TLS certificate", "expires in %d days (%s); is auto-renewal working?", days, cert.NotAfter.Format("2006-01-02"))
		default:
			r.add(a, pass, "TLS certificate", "valid for %d more days", days)
		}
	}

	var wg sync.WaitGroup
	wg.Add(4)
	go func() { defer wg.Done(); checkHTTPRedirect(ctx, r, f, pg) }()
	go func() { defer wg.Done(); checkCompression(ctx, r, f, pg) }()
	go func() { defer wg.Done(); checkSoft404(ctx, r, f, pg) }()
	go func() { defer wg.Done(); checkFavicon(ctx, r, f, pg, d) }()
	wg.Wait()
}

// checkHTTPRedirect makes sure http:// sends people to https://.
func checkHTTPRedirect(ctx context.Context, r *report, f *fetcher, pg *page) {
	if pg.final.Scheme != "https" {
		return
	}
	u := *pg.final
	u.Scheme = "http"
	p, err := f.status(ctx, u.String())
	switch {
	case err != nil:
		r.add("basics", info, "http:// → https://", "could not reach %s: %v", u.String(), err)
	case p.final.Scheme == "https":
		r.add("basics", pass, "http:// → https://", "redirects")
	default:
		r.add("basics", fail, "http:// → https://", "%s answers %d without redirecting to https", u.String(), p.status)
	}
}

func checkCompression(ctx context.Context, r *report, f *fetcher, pg *page) {
	p, err := f.do(ctx, http.MethodGet, pg.final.String(), "", 0, map[string]string{"Accept-Encoding": "br, gzip, zstd"})
	if err != nil {
		return
	}
	enc := p.header.Get("Content-Encoding")
	if enc == "" {
		if len(pg.body) > 2048 {
			r.add("basics", warn, "Compression", "HTML is sent uncompressed (enable brotli or gzip)")
		}
		return
	}
	r.add("basics", pass, "Compression", enc)
	if cc := p.header.Get("Cache-Control"); cc != "" {
		r.fact("html_cache_control", cc)
	}
}

// checkSoft404 asks for a page that cannot exist; a 200 means search engines
// may index error pages as real ones.
func checkSoft404(ctx context.Context, r *report, f *fetcher, pg *page) {
	b := make([]byte, 6)
	rand.Read(b)
	u := pg.final.ResolveReference(&url.URL{Path: "/sitecheck-missing-" + hex.EncodeToString(b)})
	p, err := f.status(ctx, u.String())
	if err != nil {
		return
	}
	switch {
	case p.status == 404 || p.status == 410:
		r.add("basics", pass, "Missing pages return 404", "")
	case p.status == 200:
		r.add("basics", warn, "Soft 404", "a made-up URL (%s) answers 200; missing pages should return 404", u.Path)
	case p.status >= 300 && p.status < 400 || len(p.redirects) > 0:
		r.add("basics", info, "Missing pages", "a made-up URL redirects to %s (status %d)", p.final, p.status)
	default:
		r.add("basics", info, "Missing pages", "a made-up URL answers %d", p.status)
	}
}

func checkFavicon(ctx context.Context, r *report, f *fetcher, pg *page, d *doc) {
	icons := append(d.links("icon"), d.links("apple-touch-icon")...)
	var href string
	for _, t := range icons {
		if h := t.attr("href"); h != "" {
			href = h
			break
		}
	}
	if href == "" {
		href = "/favicon.ico"
	}
	u, err := pg.final.Parse(href)
	if err != nil {
		r.add("basics", warn, "Favicon", "bad href %q", href)
		return
	}
	if u.Scheme == "data" {
		r.add("basics", pass, "Favicon", "inline data URL")
		return
	}
	p, err := f.status(ctx, u.String())
	if err != nil || p.status != 200 {
		status := "unreachable"
		if p != nil {
			status = fmt.Sprint(p.status)
		}
		r.add("basics", warn, "Favicon", "%s: %s (browsers and Google results show it)", u, status)
		return
	}
	if len(d.links("apple-touch-icon")) == 0 {
		r.add("basics", info, "Apple touch icon", "none declared (used for iOS home screen and some link previews)")
	}
	r.add("basics", pass, "Favicon", u.String())
}

func checkSecurity(r *report, pg *page, d *doc) {
	const a = "security"
	h := pg.header
	if pg.final.Scheme == "https" {
		if v := h.Get("Strict-Transport-Security"); v == "" {
			r.add(a, warn, "HSTS", "no Strict-Transport-Security header")
		} else {
			r.add(a, pass, "HSTS", v)
		}
	}
	csp := h.Get("Content-Security-Policy")
	if csp == "" {
		r.add(a, info, "Content-Security-Policy", "none (limits the damage of injected scripts)")
	} else {
		r.add(a, pass, "Content-Security-Policy", "set")
	}
	if strings.ToLower(h.Get("X-Content-Type-Options")) != "nosniff" {
		r.add(a, info, "X-Content-Type-Options", "not nosniff")
	}
	if h.Get("X-Frame-Options") == "" && !strings.Contains(csp, "frame-ancestors") {
		r.add(a, info, "Clickjacking protection", "no X-Frame-Options or CSP frame-ancestors")
	}
	if h.Get("Referrer-Policy") == "" {
		r.add(a, info, "Referrer-Policy", "not set (browsers default to strict-origin-when-cross-origin)")
	}
	if s := h.Get("Server"); s != "" && strings.ContainsAny(s, "0123456789") {
		r.add(a, info, "Server header", "reveals version: %s", s)
	}
	if h.Get("X-Powered-By") != "" {
		r.add(a, info, "X-Powered-By", "reveals stack: %s", h.Get("X-Powered-By"))
	}

	if pg.final.Scheme == "https" {
		var mixed []string
		for _, t := range d.tags {
			var src string
			switch t.name {
			case "script", "img", "iframe", "audio", "video", "source", "embed":
				src = t.attr("src")
			case "link":
				if rel := strings.ToLower(t.attr("rel")); strings.Contains(rel, "stylesheet") || strings.Contains(rel, "icon") || strings.Contains(rel, "preload") {
					src = t.attr("href")
				}
			}
			if strings.HasPrefix(strings.ToLower(src), "http://") {
				mixed = append(mixed, src)
			}
		}
		if len(mixed) > 0 {
			r.add(a, warn, "Mixed content", "%d http:// resource(s) on an https page, e.g. %s", len(mixed), mixed[0])
		}
	}
}
