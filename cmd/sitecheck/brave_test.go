package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func fakeBrave(t *testing.T, results map[string][]string) *[]string {
	t.Helper()
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Subscription-Token") != "k" {
			w.WriteHeader(401)
			return
		}
		q := r.URL.Query().Get("q")
		asked = append(asked, q)
		var res []braveResult
		for _, u := range results[q] {
			res = append(res, braveResult{URL: u})
		}
		json.NewEncoder(w).Encode(map[string]any{"web": map[string]any{"results": res}})
	}))
	t.Cleanup(srv.Close)
	old, oldGap := braveBase, braveGap
	braveBase, braveGap = srv.URL, 0
	t.Cleanup(func() { braveBase, braveGap = old, oldGap })
	t.Setenv("BRAVE_SEARCH_API_KEY", "k")
	return &asked
}

func TestBraveRanks(t *testing.T) {
	fakeBrave(t, map[string][]string{
		"best electrolytes glp-1": {"https://a.com/x", "https://www.shop.example/guide/", "https://b.com/"},
		"glp-1 nausea":            {"https://a.com/", "https://b.com/", "https://a.com/y", "https://c.com/", "https://d.com/", "https://e.com/", "https://shop.example/other"},
		"hydration":               {"https://a.com/"},
	})
	page, _ := url.Parse("https://shop.example/guide")
	r := &report{}
	checkBrave(context.Background(), r, page, parseDoc(""), []string{"best electrolytes glp-1", "glp-1 nausea", "hydration"})
	var got []finding
	for _, f := range r.Findings {
		got = append(got, f)
	}
	if len(got) != 3 {
		t.Fatalf("%+v", got)
	}
	if got[0].Level != pass || !strings.Contains(got[0].Detail, "#2; ahead: a.com") {
		t.Errorf("page found: %+v", got[0])
	}
	if got[1].Level != info || !strings.Contains(got[1].Detail, "site is at #7") || !strings.Contains(got[1].Detail, "ahead: a.com, b.com, c.com") {
		t.Errorf("site only: %+v", got[1])
	}
	if got[2].Level != warn || !strings.Contains(got[2].Detail, "not in Brave's top 20") {
		t.Errorf("missing (given query → WARN): %+v", got[2])
	}
}

func TestBraveQuerySources(t *testing.T) {
	asked := fakeBrave(t, nil)
	page, _ := url.Parse("https://shop.example/")
	d := parseDoc(`<title>Electrolytes for GLP-1 users | Shop</title><h1>Stay hydrated on GLP-1s</h1>`)

	r := &report{}
	checkBrave(context.Background(), r, page, d, nil)
	if strings.Join(*asked, ";") != "Electrolytes for GLP-1 users;Stay hydrated on GLP-1s" {
		t.Errorf("guessed queries: %q", *asked)
	}
	for _, f := range r.Findings {
		if f.Level != info {
			t.Errorf("a guessed query that misses should be INFO: %+v", f)
		}
	}

	*asked = nil
	r = &report{}
	r.fact("gsc_top_queries", []gscRow{{Keys: []string{"q1"}}, {Keys: []string{"q2"}}, {Keys: []string{"q3"}}, {Keys: []string{"q4"}}})
	checkBrave(context.Background(), r, page, d, nil)
	if strings.Join(*asked, ";") != "q1;q2;q3" {
		t.Errorf("Search Console queries: %q", *asked)
	}
}

func TestBraveNoKey(t *testing.T) {
	t.Setenv("BRAVE_SEARCH_API_KEY", "")
	page, _ := url.Parse("https://shop.example/")
	r := &report{}
	checkBrave(context.Background(), r, page, parseDoc(""), nil)
	if len(r.Findings) != 0 {
		t.Errorf("no key and no -q should be silent: %+v", r.Findings)
	}
}
