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

// aiSite serves a page plus robots.txt and llms.txt, and refuses the user
// agents listed in refuse (like a firewall's AI bot rule).
func aiSite(t *testing.T, html, robots, llms string, refuse ...string) (*page, *doc, *fetcher) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		for _, b := range refuse {
			if strings.Contains(r.UserAgent(), b) {
				w.WriteHeader(403)
				io.WriteString(w, "Forbidden")
				return
			}
		}
		w.Header().Set("Content-Type", "text/html")
		io.WriteString(w, html)
	})
	mux.HandleFunc("/robots.txt", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, robots) })
	mux.HandleFunc("/llms.txt", func(w http.ResponseWriter, r *http.Request) {
		if llms == "" {
			http.NotFound(w, r)
			return
		}
		io.WriteString(w, llms)
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

var longBody = "<p>" + strings.Repeat("Electrolytes help with hydration on GLP-1 medication. ", 40) + "</p>"

func TestAEOFirewallVsRobots(t *testing.T) {
	html := `<html><body><h2>What should I drink?</h2>` + longBody + `</body></html>`
	robots := "User-agent: GPTBot\nDisallow: /\n\nUser-agent: PerplexityBot\nDisallow: /\n\nUser-agent: *\nAllow: /\nContent-Signal: search=yes, ai-train=no\n"
	pg, d, f := aiSite(t, html, robots, "# Site\n\n- [Guide](https://x/guide): about\n", "OAI-SearchBot", "PerplexityBot", "GPTBot")
	r := &report{}
	checkAEO(context.Background(), r, f, pg, d)
	got := findings(r, "aeo")

	// PerplexityBot: blocked in robots.txt (an answer crawler) → FAIL.
	if g := got["AI search crawlers blocked"]; g.Level != fail || !strings.Contains(g.Detail, "PerplexityBot") || strings.Contains(g.Detail, "GPTBot") {
		t.Errorf("robots answer block: %+v", g)
	}
	// GPTBot is training-only → INFO, not a failure.
	if g := got["AI training crawlers"]; g.Level != info || !strings.Contains(g.Detail, "GPTBot") {
		t.Errorf("training: %+v", g)
	}
	// OAI-SearchBot: allowed by robots but refused by the "firewall" → FAIL.
	var surprise, deliberate finding
	for _, fd := range r.Findings {
		if fd.Check == "AI crawlers turned away" {
			if fd.Level == fail {
				surprise = fd
			} else if fd.Level == info {
				deliberate = fd
			}
		}
	}
	if !strings.Contains(surprise.Detail, "OAI-SearchBot: HTTP 403") || strings.Contains(surprise.Detail, "PerplexityBot") {
		t.Errorf("surprise block: %+v", surprise)
	}
	if !strings.Contains(deliberate.Detail, "PerplexityBot") {
		t.Errorf("deliberate block: %+v", deliberate)
	}
	if g := got["Content-Signal"]; !strings.Contains(g.Detail, "search=yes, ai-train=no") {
		t.Errorf("content signal: %+v", g)
	}
	if g := got["llms.txt"]; !strings.Contains(g.Detail, "present (1 links)") {
		t.Errorf("llms.txt: %+v", g)
	}
	if g := got["Text without JavaScript"]; g.Level != pass {
		t.Errorf("text: %+v", g)
	}
}

func TestAEOJavaScriptOnlyPage(t *testing.T) {
	html := `<html><body><div id="root"></div>` + strings.Repeat(`<script src="/x.js"></script>`, 20) + `<script>window.__DATA__ = "` + strings.Repeat("lots of words in a script ", 200) + `"</script></body></html>`
	pg, d, f := aiSite(t, html, "", "")
	r := &report{}
	checkAEO(context.Background(), r, f, pg, d)
	got := findings(r, "aeo")
	if g := got["Text without JavaScript"]; g.Level != fail {
		t.Errorf("JS-only page: %+v", g)
	}
	if g := got["llms.txt"]; !strings.Contains(g.Detail, "none") {
		t.Errorf("llms.txt: %+v", g)
	}
}

func TestAEOSkipsWhenWeAreBlocked(t *testing.T) {
	pg, d, f := aiSite(t, "", "", "", "sitecheck")
	r := &report{}
	checkAEO(context.Background(), r, f, pg, d)
	got := findings(r, "aeo")
	if _, ok := got["Text without JavaScript"]; ok {
		t.Error("judged page text even though our own request was refused")
	}
	if g := got["AI crawler comparison"]; !strings.Contains(g.Detail, "403") {
		t.Errorf("%+v", g)
	}
}

func TestAEOSchemaAndFAQ(t *testing.T) {
	old := time.Now().AddDate(-3, 0, 0).Format("2006-01-02")
	html := `<html><head><script type="application/ld+json">{"@context":"https://schema.org","@graph":[
		{"@type":"Organization","name":"X"},
		{"@type":"BlogPosting","headline":"H","dateModified":"` + old + `"}]}</script></head><body>
		<h2>What is it?</h2><p>A.</p><h2>How does it work?</h2><p>B.</p><h3>Is it safe?</h3><p>C.</p><ul><li>x</li></ul>` + longBody + `</body></html>`
	pg, d, f := aiSite(t, html, "", "")
	r := &report{}
	checkAEO(context.Background(), r, f, pg, d)
	got := findings(r, "aeo")
	if g := got["Entity markup"]; !strings.Contains(g.Detail, "no sameAs") {
		t.Errorf("entity: %+v", g)
	}
	if g := got["Author"]; g.Level != warn {
		t.Errorf("author: %+v", g)
	}
	if g := got["Freshness"]; g.Level != warn {
		t.Errorf("freshness: %+v", g)
	}
	if g := got["Answer-shaped structure"]; !strings.Contains(g.Detail, "3 question-style subheadings, 1 lists") {
		t.Errorf("shape: %+v", g)
	}
	if _, ok := got["FAQ markup"]; ok {
		t.Error("FAQ markup nudge should be gone (Google: no special markup needed)")
	}
}

func TestEvidence(t *testing.T) {
	bare := "<html><body><p>" + strings.Repeat("Our products are great and you will like them a lot. ", 40) + "</p></body></html>"
	pg, d, _ := aiSite(t, bare, "", "")
	r := &report{}
	checkEvidence(r, pg, d, 400)
	if g := findings(r, "aeo")["Evidence"]; g.Level != warn {
		t.Errorf("no evidence: %+v", g)
	}
	rich := `<html><body><p>Each stick has 500 mg sodium and 10 calories. About 40% of users report nausea.</p>
		<blockquote>Hydration matters more on GLP-1s.</blockquote>
		<p>“Drink before you feel thirsty, especially in the first weeks,” says Dr. Lee.</p>
		<a href="https://www.nih.gov/x">NIH</a><a href="https://x.com/brand">X</a>` + "<p>" + strings.Repeat("More text here. ", 60) + "</p></body></html>"
	pg, d, _ = aiSite(t, rich, "", "")
	r = &report{}
	checkEvidence(r, pg, d, 400)
	g := findings(r, "aeo")["Evidence"]
	if g.Level != info || !strings.Contains(g.Detail, "3 figures") || !strings.Contains(g.Detail, "2 quotations") || !strings.Contains(g.Detail, "1 outside sources") {
		t.Errorf("rich evidence: %+v", g)
	}
}
