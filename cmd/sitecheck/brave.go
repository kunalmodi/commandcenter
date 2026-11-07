package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
)

// Claude's web search uses Brave Search's index, so where a page ranks on
// Brave for the questions it should answer is the closest measurable proxy
// for "Claude can find and cite this". It works for any site, not just your
// own. Needs BRAVE_SEARCH_API_KEY (api-dashboard.search.brave.com).

var braveBase = "https://api.search.brave.com/res/v1/web/search"

// braveGap is the pause between queries; the free plan allows 1 request a second.
var braveGap = 1100 * time.Millisecond

const (
	braveDepth      = 20 // results per query (the API's maximum per request)
	braveMaxQueries = 3
)

type braveResult struct {
	Title string `json:"title"`
	URL   string `json:"url"`
}

func braveSearch(ctx context.Context, key, q string) ([]braveResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	p := url.Values{"q": {q}, "count": {fmt.Sprint(braveDepth)}, "safesearch": {"off"}}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, braveBase+"?"+p.Encode(), nil)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Subscription-Token", key)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, errors.New(strings.ReplaceAll(err.Error(), key, "REDACTED"))
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode != 200 {
		var e struct {
			Error struct {
				Detail string `json:"detail"`
			} `json:"error"`
		}
		json.Unmarshal(data, &e)
		msg := e.Error.Detail
		if msg == "" {
			msg = resp.Status
		}
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, msg)
	}
	var out struct {
		Web struct {
			Results []braveResult `json:"results"`
		} `json:"web"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	return out.Web.Results, nil
}

// rankKey normalizes a URL for matching search results to the page: no
// scheme, www, query, fragment or trailing slash.
func rankKey(raw string) (host, key string) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", raw
	}
	host = strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
	return host, host + strings.TrimSuffix(u.EscapedPath(), "/")
}

var titleSep = regexp.MustCompile(`\s+[|–—·•-]\s+|\s*::\s*`)

// guessQueries derives search phrases from the page itself when no better
// source exists: the title without the site name, and the h1 if it differs.
func guessQueries(d *doc) []string {
	var out []string
	seen := map[string]bool{}
	add := func(s string) {
		s = cleanText(s)
		k := strings.ToLower(s)
		if len(strings.Fields(s)) < 2 || len(s) > 120 || seen[k] {
			return
		}
		seen[k] = true
		out = append(out, s)
	}
	if t, ok := d.title(); ok {
		parts := titleSep.Split(cleanText(t), -1)
		best := parts[0]
		for _, p := range parts {
			if len(p) > len(best) {
				best = p
			}
		}
		add(best)
	}
	if h, ok := d.first("h1"); ok {
		add(h.text)
	}
	return out
}

// braveQueries picks what to search for: -q flags, else the page's top Search
// Console queries (your own sites), else phrases from the title and h1.
func braveQueries(r *report, d *doc, given []string) ([]string, string) {
	if len(given) > 0 {
		return given[:min(len(given), braveMaxQueries)], "given"
	}
	r.mu.Lock()
	rows, _ := r.Facts["gsc_top_queries"].([]gscRow)
	r.mu.Unlock()
	var qs []string
	for _, q := range rows {
		if len(q.Keys) > 0 && len(qs) < braveMaxQueries {
			qs = append(qs, q.Keys[0])
		}
	}
	if len(qs) > 0 {
		return qs, "Search Console"
	}
	qs = guessQueries(d)
	return qs[:min(len(qs), braveMaxQueries)], "title/h1"
}

func checkBrave(ctx context.Context, r *report, page *url.URL, d *doc, given []string) {
	const a, check = "aeo", "Brave rank (Claude's search)"
	key := strings.TrimSpace(os.Getenv("BRAVE_SEARCH_API_KEY"))
	if key == "" {
		if len(given) > 0 {
			r.add(a, warn, check, "-q given but BRAVE_SEARCH_API_KEY isn't set (free key at api-dashboard.search.brave.com)")
		}
		return
	}
	qs, source := braveQueries(r, d, given)
	if len(qs) == 0 {
		r.add(a, info, check, "no queries to try: pass -q \"the question this page answers\"")
		return
	}
	pageHost, pageKey := rankKey(page.String())
	var ranks []map[string]any
	for i, q := range qs {
		if i > 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(braveGap):
			}
		}
		res, err := braveSearch(ctx, key, q)
		if err != nil {
			r.add(a, warn, check, "%q: search failed: %v", q, err)
			continue
		}
		pageAt, siteAt, siteURL := 0, 0, ""
		var above []string
		for j, x := range res {
			h, k := rankKey(x.URL)
			if k == pageKey && pageAt == 0 {
				pageAt = j + 1
			}
			if h == pageHost && siteAt == 0 {
				siteAt, siteURL = j+1, x.URL
			}
			if pageAt == 0 && siteAt == 0 && len(above) < 3 && h != pageHost && !contains(above, h) {
				above = append(above, h)
			}
		}
		ranks = append(ranks, map[string]any{"query": q, "page_rank": pageAt, "site_rank": siteAt, "results": len(res)})
		top := ""
		if len(above) > 0 {
			top = "; ahead: " + strings.Join(above, ", ")
		}
		switch {
		case pageAt > 0 && pageAt <= 5:
			r.add(a, pass, check, "%q: #%d%s", q, pageAt, top)
		case pageAt > 0:
			r.add(a, info, check, "%q: #%d, outside the top 5 Claude usually reads%s", q, pageAt, top)
		case siteAt > 0:
			r.add(a, info, check, "%q: this page not in the top %d, but the site is at #%d (%s)%s", q, braveDepth, siteAt, siteURL, top)
		default:
			lv := warn
			if source == "title/h1" {
				lv = info // our guess at the query may simply be wrong
			}
			r.add(a, lv, check, "%q: site not in Brave's top %d%s", q, braveDepth, top)
		}
	}
	if len(ranks) > 0 {
		r.fact("brave_ranks", ranks)
		r.fact("brave_query_source", source)
	}
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
