package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// bylineSite serves an article at / and an author page at /authors/ana/.
func bylineSite(t *testing.T, article, author string) (*page, *doc, *fetcher) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, article) })
	mux.HandleFunc("/authors/ana/", func(w http.ResponseWriter, r *http.Request) {
		if author == "" {
			http.NotFound(w, r)
			return
		}
		io.WriteString(w, author)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	f := newFetcher(5*time.Second, browserUA)
	pg, err := f.get(context.Background(), srv.URL+"/", "")
	if err != nil {
		t.Fatal(err)
	}
	return pg, parseDoc(pg.body), f
}

const articleLD = `<script type="application/ld+json">{"@type":"BlogPosting","headline":"H","author":{"@type":"Person","name":"Ana Ruiz","url":"/authors/ana/"}}</script>`

func runByline(t *testing.T, article, author string) map[string]finding {
	pg, d, f := bylineSite(t, article, author)
	r := &report{}
	art := checkAEOSchema(r, d, pg)
	checkByline(context.Background(), r, f, pg, d, art)
	return findings(r, "aeo")
}

func TestBylineVisibleWithAuthorPage(t *testing.T) {
	got := runByline(t,
		`<html><head>`+articleLD+`</head><body><nav>Home Shop Blog</nav><h1>How to stay hydrated</h1><p>By <a href="/authors/ana/">Ana Ruiz</a>, RD</p>`+longBody+`</body></html>`,
		`<html><head><script type="application/ld+json">{"@type":"Person","name":"Ana Ruiz","sameAs":["https://linkedin.com/in/ana"]}</script></head><body><img src="/img/ana-ruiz.jpg" alt="Ana Ruiz"><p>Ana is a dietitian.</p></body></html>`)
	if g := got["Byline"]; g.Level != pass || !strings.Contains(g.Detail, "Ana Ruiz") {
		t.Errorf("byline: %+v", g)
	}
	if g := got["Author page"]; g.Level != pass {
		t.Errorf("author page: %+v", g)
	}
}

func TestBylineOnlyInMarkup(t *testing.T) {
	got := runByline(t, `<html><head>`+articleLD+`</head><body><h1>How to stay hydrated</h1>`+longBody+`</body></html>`, "")
	if g := got["Byline"]; g.Level != warn || !strings.Contains(g.Detail, "markup names Ana Ruiz") {
		t.Errorf("byline: %+v", g)
	}
	if g := got["Author page"]; g.Level != warn || !strings.Contains(g.Detail, "HTTP 404") {
		t.Errorf("broken author page: %+v", g)
	}
}

func TestBylineFromText(t *testing.T) {
	got := runByline(t, `<html><head><meta property="og:type" content="article"></head><body><h1>Hydration tips</h1><p>By clicking you agree.</p><p>Written by Sam Lee</p>`+longBody+`</body></html>`, "")
	if g := got["Byline"]; g.Level != pass || !strings.Contains(g.Detail, "Sam Lee") {
		t.Errorf("byline from text: %+v", g)
	}
	if g := got["Author page"]; g.Level != info {
		t.Errorf("unlinked byline: %+v", g)
	}
}

func TestBylineSkipsNonArticles(t *testing.T) {
	got := runByline(t, `<html><body><h1>Shop</h1>`+longBody+`</body></html>`, "")
	if _, ok := got["Byline"]; ok {
		t.Error("product or home pages shouldn't get byline findings")
	}
}

func TestStockImages(t *testing.T) {
	_, d := pageFor(t, "https://shop.example/", `<html><head><meta property="og:image" content="https://images.unsplash.com/photo-1?w=1200"></head><body>
		<img src="https://media.istockphoto.com/id/1/photo.jpg">
		<img src="/uploads/shutterstock_123456789.jpg">
		<img src="/uploads/our-store.jpg">
		<img src="/logo.png" width="40">
		<img src="/icon.svg"></body></html>`)
	pg, _ := pageFor(t, "https://shop.example/", "")
	r := &report{}
	checkStockImages(r, pg, d)
	g := findings(r, "aeo")["Stock images"]
	if g.Level != warn || !strings.Contains(g.Detail, "3 of 4") || !strings.Contains(g.Detail, "istockphoto.com") || !strings.Contains(g.Detail, "file name shutterstock") {
		t.Errorf("%+v", g)
	}

	_, d = pageFor(t, "https://shop.example/", `<img src="/a.jpg"><img src="/b.jpg">`)
	r = &report{}
	checkStockImages(r, pg, d)
	if len(r.Findings) != 0 {
		t.Errorf("original images: %+v", r.Findings)
	}
}

func TestChallengeOnlyOnSmallPages(t *testing.T) {
	big := "<html><head><title>Electrolytes guide</title></head><body>" + strings.Repeat("<p>real content</p>", 5000) + `<script>{"googleRecaptchaSiteKey":"x"}</script></body></html>`
	if isChallenge(big) {
		t.Error("a reCAPTCHA key in a big real page isn't a bot check")
	}
	if !isChallenge("<html><head><title>Just a moment...</title></head><body>cf-chl</body></html>") {
		t.Error("Cloudflare challenge not spotted")
	}
}

func TestBylineIgnoresOffsiteProfiles(t *testing.T) {
	got := runByline(t, `<html><head><meta property="og:type" content="article"></head><body><h1>Post</h1><p>By Sam Lee</p><a href="https://bsky.app/profile/someone">a reply</a>`+longBody+`</body></html>`, "")
	if g := got["Author page"]; strings.Contains(g.Detail, "bsky.app") {
		t.Errorf("took a social profile for the author page: %+v", g)
	}
}
