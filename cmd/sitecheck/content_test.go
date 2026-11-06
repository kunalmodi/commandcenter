package main

import (
	"net/url"
	"strings"
	"testing"
)

func TestParseFrontMatter(t *testing.T) {
	e := parseFrontMatter("---\ntitle: Hello | Site\nauthor: Ana\ndate: \"2026-01-01\"\n---\n# Heading\n\nBody text here.\n")
	if e.meta["title"] != "Hello | Site" || e.meta["author"] != "Ana" || e.meta["date"] != "2026-01-01" {
		t.Errorf("meta %v", e.meta)
	}
	if !strings.HasPrefix(e.text, "# Heading") || strings.Contains(e.text, "author") {
		t.Errorf("text %q", e.text)
	}
	if e := parseFrontMatter("just text"); e.text != "just text" || len(e.meta) != 0 {
		t.Errorf("no front matter: %+v", e)
	}
}

func pageFor(t *testing.T, raw, body string) (*page, *doc) {
	t.Helper()
	u, _ := url.Parse(raw)
	return &page{final: u, body: body}, parseDoc(body)
}

func TestLinkTextAndAlt(t *testing.T) {
	var b strings.Builder
	b.WriteString(`<html><head><title>Electrolytes for GLP-1 | Shop</title></head><body><h1>Best electrolytes</h1>`)
	for _, p := range []string{"a", "b", "c", "d"} {
		b.WriteString(`<div><img src="/` + p + `.webp" alt="" width="400"><h3>Product ` + p + `</h3><a href="/products/` + p + `/">See the details</a></div>`)
		b.WriteString(`<a href="/compare/` + p + `/">Compare</a>`)
	}
	b.WriteString(`<a href="/x"><img src="/icon.svg" alt=""></a><a href="/y" aria-label="Cart"><svg></svg></a></body></html>`)
	pg, d := pageFor(t, "https://shop.example/", b.String())

	r := &report{}
	checkLinkText(r, pg, d)
	checkImagesAlt(r, d)
	checkTitleH1(r, d)
	got := map[string]finding{}
	for _, f := range r.Findings {
		got[f.Check] = f
	}
	if g := got["Vague link text"]; g.Level != warn || !strings.Contains(g.Detail, "4 links") || !strings.Contains(g.Detail, "See the details") {
		t.Errorf("vague: %+v", g)
	}
	if g := got["Repeated link text"]; !strings.Contains(g.Detail, `"compare" → 4 pages`) {
		t.Errorf("repeated: %+v", g)
	}
	if g := got["Links with no text"]; g.Level != warn || !strings.Contains(g.Detail, "1 links") || !strings.Contains(g.Detail, "/x") {
		t.Errorf("empty links (aria-label one should be fine): %+v", g)
	}
	if g := got["Empty image alt"]; g.Level != warn || !strings.Contains(g.Detail, "4 images") {
		t.Errorf("empty alt: %+v", g)
	}
	if _, ok := got["Title vs h1"]; ok {
		t.Error("title and h1 share 'electrolytes'; should not warn")
	}
}

func TestTitleVsH1(t *testing.T) {
	_, d := pageFor(t, "https://x.example/p", `<title>Running shoes for flat feet | Shop</title><h1>Welcome!</h1>`)
	r := &report{}
	checkTitleH1(r, d)
	if len(r.Findings) != 1 || r.Findings[0].Level != warn {
		t.Errorf("%+v", r.Findings)
	}
}

func row(kv ...string) crawlRow {
	r := crawlRow{"resp_headers_Content-Type": "text/html; charset=utf-8", "status": float64(200)}
	for i := 0; i+1 < len(kv); i += 2 {
		if kv[i] == "status" {
			r["status"] = kv[i+1]
			continue
		}
		r[kv[i]] = kv[i+1]
	}
	return r
}

func TestAnalyzeCrawl(t *testing.T) {
	start, _ := url.Parse("https://ex.com/")
	words := strings.Repeat("word ", 300)
	rows := []crawlRow{
		row("url", "https://ex.com/", "title", "Home", "meta_desc", "Welcome", "h1", "Home", "body_text", words,
			"links_url", "https://ex.com/a/@@https://ex.com/b/@@https://ex.com/missing/", "links_text", "A@@See the details@@Gone",
			"alt_href", "https://ex.com/@@https://ex.com/de/", "img_src", "/1.png@@/2.png", "img_alt", "@@Logo"),
		row("url", "https://ex.com/a/", "title", "Same title", "meta_desc", "Desc A", "h1", "A", "body_text", words, "canonical", "https://ex.com/a/"),
		row("url", "https://ex.com/b/", "title", "Same title", "meta_desc", "", "h1", "B1@@B2", "body_text", "short", "meta_robots", "noindex, follow"),
		row("url", "https://ex.com/missing/", "status", "404", "request_headers_Referer", "https://ex.com/"),
		row("url", "https://ex.com/new/", "redirect_urls", "https://ex.com/old/", "title", "New", "meta_desc", "N", "h1", "N", "body_text", words),
		row("url", "https://ex.com/de/", "title", "Startseite", "meta_desc", "D", "h1", "Start", "body_text", words, "alt_href", "https://ex.com/de/"),
		row("url", "https://other.com/", "title", "Ignored"),
	}
	sitemap := []string{"https://ex.com/", "https://ex.com/a/", "https://ex.com/b/", "https://ex.com/old/", "https://ex.com/de/", "https://ex.com/orphan/"}
	r := &report{}
	analyzeCrawl(r, start, rows, sitemap, 100)
	got := findings(r, "site")
	expect := map[string]level{
		"Broken internal URLs":       fail,
		"Duplicate titles":           warn,
		"Missing meta descriptions":  warn,
		"Several h1s":                info,
		"Noindexed pages":            info,
		"Little text":                info,
		"Sitemap entries":            warn,
		"hreflang return links":      warn,
		"Vague link text, site-wide": warn,
		"Image alt text, site-wide":  warn,
		"Redirected URLs":            info,
	}
	for k, lv := range expect {
		if got[k].Level != lv {
			t.Errorf("%s: got %v %q, want %v", k, got[k].Level, got[k].Detail, lv)
		}
	}
	if d := got["Broken internal URLs"].Detail; !strings.Contains(d, "/missing/ (404, linked from /)") {
		t.Errorf("broken detail: %s", d)
	}
	if d := got["Sitemap entries"].Detail; !strings.Contains(d, "/old/ redirects") || !strings.Contains(d, "/b/ is noindex") {
		t.Errorf("sitemap detail: %s", d)
	}
	if d := got["hreflang return links"].Detail; !strings.Contains(d, "/de/ doesn't link back to /") {
		t.Errorf("hreflang: %s", d)
	}
	// 5 pages crawled vs 6 in the sitemap is under 90%: orphans not judged.
	if g := got["Orphan pages"]; g.Level != info || !strings.Contains(g.Detail, "not checked") {
		t.Errorf("orphans should be skipped on a partial crawl: %+v", g)
	}
	if strings.Contains(got["Crawl"].Detail, "other.com") {
		t.Error("other hosts counted")
	}
}

func TestOrphansOnFullCrawl(t *testing.T) {
	start, _ := url.Parse("https://ex.com/")
	rows := []crawlRow{
		row("url", "https://ex.com/", "title", "H", "links_url", "https://ex.com/a"),
		row("url", "https://ex.com/a/", "title", "A"),
		row("url", "https://ex.com/lonely/", "title", "L"),
	}
	r := &report{}
	analyzeCrawl(r, start, rows, []string{"https://ex.com/", "https://ex.com/a/", "https://ex.com/lonely/"}, 100)
	if g := findings(r, "site")["Orphan pages"]; g.Level != warn || !strings.Contains(g.Detail, "/lonely/") || strings.Contains(g.Detail, "/a/") {
		t.Errorf("orphans: %+v", g)
	}
}
