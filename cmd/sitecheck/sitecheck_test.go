package main

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestScanTags(t *testing.T) {
	src := `<!doctype html><html lang="en"><head>
<!-- <meta name="description" content="commented out"> -->
<title> Hello &amp; welcome </title>
<meta property="og:title" content='It&#39;s "quoted"'>
<meta name=viewport content=width=device-width>
<META NAME="Description" CONTENT="Real description">
<link rel="canonical icon" href="/x">
<script type="application/ld+json">{"@type":"Thing","x":"<h1>not a heading</h1>"}</script>
<style>h1 { color: red } <meta name="robots" content="noindex"></style>
</head><body><h1>Main <b>title</b></h1><img src=a.png><img alt="" src=b.png></body></html>`
	d := parseDoc(src)
	if v, _ := d.title(); v != "Hello & welcome" {
		t.Errorf("title %q", v)
	}
	if v, _ := d.meta("og:title"); v != `It's "quoted"` {
		t.Errorf("og:title %q", v)
	}
	if v, _ := d.meta("description"); v != "Real description" {
		t.Errorf("description %q (comment should be skipped)", v)
	}
	if _, ok := d.meta("robots"); ok {
		t.Error("meta inside <style> was parsed")
	}
	if v, _ := d.meta("viewport"); v != "width=device-width" {
		t.Errorf("unquoted attr %q", v)
	}
	if len(d.links("canonical")) != 1 || len(d.links("icon")) != 1 {
		t.Error("rel list not split")
	}
	if h := d.all("h1"); len(h) != 1 || h[0].text != "Main title" {
		t.Errorf("h1 %+v", h)
	}
	if h, _ := d.first("html"); h.attr("lang") != "en" {
		t.Error("lang")
	}
	if len(d.all("img")) != 2 {
		t.Error("imgs")
	}
}

func TestRobots(t *testing.T) {
	groups, sitemaps := parseRobots(`
User-agent: *
Disallow: /private
Allow: /private/ok
Disallow: /*.pdf$

User-agent: Googlebot
User-agent: Bingbot
Disallow: /nogoogle

Sitemap: https://example.com/sitemap.xml
`)
	if len(sitemaps) != 1 {
		t.Fatalf("sitemaps %v", sitemaps)
	}
	cases := []struct {
		agent, path string
		want        bool
	}{
		{"facebookexternalhit", "/private/page", false},
		{"facebookexternalhit", "/private/ok/x", true},
		{"facebookexternalhit", "/doc.pdf", false},
		{"facebookexternalhit", "/doc.pdf?x=1", true},
		{"facebookexternalhit", "/public", true},
		{"googlebot", "/private/page", true}, // its own group replaces *
		{"googlebot", "/nogoogle/x", false},
		{"bingbot", "/nogoogle", false},
	}
	for _, c := range cases {
		if got := robotsAllowed(groups, c.agent, c.path); got != c.want {
			t.Errorf("%s %s: got %v", c.agent, c.path, got)
		}
	}
	if !robotsAllowed(nil, "googlebot", "/x") {
		t.Error("no robots.txt should allow")
	}
	g, _ := parseRobots("User-agent: *\nDisallow:\n")
	if !robotsAllowed(g, "googlebot", "/x") {
		t.Error("empty Disallow should allow")
	}
}

func TestImageSize(t *testing.T) {
	var buf bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 1200, 630))
	img.Set(0, 0, color.White)
	png.Encode(&buf, img)
	if w, h, f := imageSize(buf.Bytes()); w != 1200 || h != 630 || f != "png" {
		t.Errorf("png: %d %d %s", w, h, f)
	}
	// VP8X header for a 1200x630 WebP (canvas sizes are stored minus one).
	webp := []byte("RIFF\x00\x00\x00\x00WEBPVP8X\x0a\x00\x00\x00\x00\x00\x00\x00")
	webp = append(webp, byte(1199&0xff), byte(1199>>8), 0, byte(629&0xff), byte(629>>8), 0)
	if w, h, f := imageSize(webp); w != 1200 || h != 630 || f != "webp" {
		t.Errorf("webp: %d %d %s", w, h, f)
	}
	if _, _, f := imageSize([]byte(`<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg"/>`)); f != "svg" {
		t.Errorf("svg: %s", f)
	}
}

func pngBytes(w, h int) []byte {
	var buf bytes.Buffer
	png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, w, h)))
	return buf.Bytes()
}

// site serves a page whose HTML differs for crawlers, like some real sites.
func site(t *testing.T, html, crawlerHTML, robots string) (*httptest.Server, *page, *doc, *fetcher) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		ua := r.Header.Get("User-Agent")
		if crawlerHTML != "" && (strings.HasPrefix(ua, "facebookexternalhit") || strings.HasPrefix(ua, "Twitterbot")) {
			w.Write([]byte(crawlerHTML))
			return
		}
		w.Write([]byte(html))
	})
	mux.HandleFunc("/og.png", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write(pngBytes(400, 400))
	})
	mux.HandleFunc("/robots.txt", func(w http.ResponseWriter, r *http.Request) {
		if robots == "" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(robots))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	f := newFetcher(5*time.Second, browserUA)
	pg, err := f.get(context.Background(), srv.URL+"/", "")
	if err != nil {
		t.Fatal(err)
	}
	return srv, pg, parseDoc(pg.body), f
}

func findings(r *report, area string) map[string]finding {
	m := map[string]finding{}
	for _, f := range r.Findings {
		if f.Area == area {
			if old, ok := m[f.Check]; !ok || f.Level > old.Level {
				m[f.Check] = f
			}
		}
	}
	return m
}

func TestSocialUsesCrawlerHTML(t *testing.T) {
	browser := `<html><head><title>Browser page</title></head></html>`
	crawler := `<html><head>
<meta property="og:title" content="Crawler title">
<meta property="og:description" content="Shown in previews">
<meta property="og:image" content="/og.png">
<meta name="twitter:card" content="summary_large_image">
</head></html>`
	_, pg, _, f := site(t, browser, crawler, "")
	r := &report{}
	checkSocial(context.Background(), r, f, pg, t.TempDir())
	got := findings(r, "social")
	if got["og:title"].Level != pass || !strings.Contains(got["og:title"].Detail, "Crawler title") {
		t.Errorf("og:title from crawler HTML: %+v", got["og:title"])
	}
	if got["og:image"].Level != fail || !strings.Contains(got["og:image"].Detail, "relative") {
		t.Errorf("relative og:image not failed: %+v", got["og:image"])
	}
	// 400x400 is fine for Facebook's minimum but too small for a large preview.
	if d := got["og:image dimensions"]; d.Level != warn {
		t.Errorf("dimensions: %+v", d)
	}
	if r.Files["share_image"] == "" {
		t.Error("share image not saved")
	}
}

func TestSocialMissingImage(t *testing.T) {
	_, pg, _, f := site(t, `<html><head><title>T</title></head></html>`, "", "")
	r := &report{}
	checkSocial(context.Background(), r, f, pg, "")
	got := findings(r, "social")
	if got["og:image"].Level != fail || got["twitter:card"].Level != warn || got["og:title"].Level != warn {
		t.Errorf("%+v", got)
	}
}

func TestSEO(t *testing.T) {
	html := `<html><head><title>A good title for this page</title>
<meta name="robots" content="noindex, follow">
<link rel="canonical" href="/other">
<script type="application/ld+json">{not json}</script>
</head><body><h1>One</h1><h1>Two</h1><img src="x.png"></body></html>`
	_, pg, d, f := site(t, html, "", "User-agent: Googlebot\nDisallow: /\n")
	r := &report{}
	checkSEO(context.Background(), r, f, pg, d)
	got := findings(r, "seo")
	want := map[string]level{
		"Title": pass, "Meta description": warn, "H1": info, "Language": warn,
		"Viewport": fail, "Canonical": warn, "Indexable": fail, "Structured data": fail,
		"Image alt text": warn, "robots.txt": fail, "Sitemap": warn,
	}
	for k, lv := range want {
		if got[k].Level != lv {
			t.Errorf("%s: got %v (%s), want %v", k, got[k].Level, got[k].Detail, lv)
		}
	}
}

func TestReportPSI(t *testing.T) {
	fixture := `{
 "loadingExperience": {"id": "https://example.com/", "metrics": {
   "LARGEST_CONTENTFUL_PAINT_MS": {"percentile": 3100, "category": "AVERAGE"},
   "INTERACTION_TO_NEXT_PAINT": {"percentile": 120, "category": "FAST"},
   "CUMULATIVE_LAYOUT_SHIFT_SCORE": {"percentile": 5, "category": "FAST"}}},
 "lighthouseResult": {
  "categories": {
   "performance": {"title": "Performance", "score": 0.42, "auditRefs": [
     {"id": "render-blocking-resources", "weight": 0},
     {"id": "largest-contentful-paint", "weight": 25, "group": "metrics"}]},
   "seo": {"title": "SEO", "score": 0.95, "auditRefs": [{"id": "document-title", "weight": 1}]}},
  "audits": {
   "render-blocking-resources": {"title": "Eliminate render-blocking resources", "score": 0.3, "displayValue": "Potential savings of 900 ms", "details": {"type": "opportunity", "overallSavingsMs": 900}},
   "largest-contentful-paint": {"title": "Largest Contentful Paint", "score": 0.2, "displayValue": "5.1 s"},
   "document-title": {"title": "Document has a <title> element", "score": 0, "scoreDisplayMode": "binary"}}}}`
	var res psiResult
	if err := json.Unmarshal([]byte(fixture), &res); err != nil {
		t.Fatal(err)
	}
	r := &report{}
	reportPSI(r, "mobile", &res, true, true)
	got := findings(r, "performance")
	if s := got["Mobile Lighthouse scores"]; s.Level != fail || !strings.Contains(s.Detail, "performance 42") {
		t.Errorf("scores: %+v", s)
	}
	cwv := got["Mobile real users (p75, this URL)"]
	if cwv.Level != warn || !strings.Contains(cwv.Detail, "LCP 3100ms (average)") || !strings.Contains(cwv.Detail, "CLS 0.05") {
		t.Errorf("field data: %+v", cwv)
	}
	if o := got["Mobile opportunity"]; !strings.Contains(o.Detail, "render-blocking") {
		t.Errorf("opportunity: %+v", o)
	}
	if a := got["Lighthouse SEO audits failing"]; !strings.Contains(a.Detail, "<title>") {
		t.Errorf("seo audits: %+v", a)
	}
}

func TestSameURLAndNormalize(t *testing.T) {
	a, _ := url.Parse("https://Example.com/a/")
	b, _ := url.Parse("https://example.com/a#top")
	if !sameURL(a, b) {
		t.Error("trailing slash / fragment / host case should not matter")
	}
	if u, err := normalizeURL("example.com"); err != nil || u.String() != "https://example.com/" {
		t.Errorf("%v %v", u, err)
	}
	if _, err := normalizeURL("ftp://x"); err == nil {
		t.Error("ftp accepted")
	}
}
