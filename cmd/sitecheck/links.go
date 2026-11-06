package main

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"sync"
)

// checkLinks requests the first limit unique http(s) links on the page and
// reports the broken ones. Same-site links are checked first.
func checkLinks(ctx context.Context, r *report, f *fetcher, pg *page, d *doc, limit int) {
	const a = "links"
	seen := map[string]bool{}
	var internal, external []string
	for _, t := range d.all("a") {
		href := strings.TrimSpace(t.attr("href"))
		if href == "" || strings.HasPrefix(href, "#") {
			continue
		}
		u, err := pg.final.Parse(href)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
			continue
		}
		u.Fragment = ""
		s := u.String()
		if seen[s] || sameURL(u, pg.final) {
			continue
		}
		seen[s] = true
		if strings.EqualFold(u.Hostname(), pg.final.Hostname()) {
			internal = append(internal, s)
		} else {
			external = append(external, s)
		}
	}
	all := append(internal, external...)
	total := len(all)
	if total == 0 {
		return
	}
	if len(all) > limit {
		all = all[:limit]
	}

	type result struct {
		url    string
		status int
		err    error
	}
	results := make([]result, len(all))
	sem := make(chan struct{}, 8)
	var wg sync.WaitGroup
	for i, u := range all {
		wg.Add(1)
		go func(i int, u string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			p, err := f.status(ctx, u)
			res := result{url: u, err: err}
			if p != nil {
				res.status = p.status
			}
			results[i] = res
		}(i, u)
	}
	wg.Wait()

	var broken, blocked []string
	for _, res := range results {
		switch {
		case res.err != nil:
			broken = append(broken, fmt.Sprintf("%s (%v)", res.url, shortErr(res.err)))
		case res.status == 404 || res.status == 410 || res.status >= 500:
			broken = append(broken, fmt.Sprintf("%s (%d)", res.url, res.status))
		case res.status == 401 || res.status == 403 || res.status == 429 || res.status == 999:
			blocked = append(blocked, fmt.Sprintf("%s (%d)", res.url, res.status))
		case res.status >= 400:
			broken = append(broken, fmt.Sprintf("%s (%d)", res.url, res.status))
		}
	}
	sort.Strings(broken)
	scope := fmt.Sprintf("checked %d of %d links", len(all), total)
	if len(broken) > 0 {
		r.add(a, fail, "Broken links", "%d: %s", len(broken), strings.Join(broken, ", "))
	} else {
		r.add(a, pass, "Broken links", "none (%s)", scope)
	}
	if len(blocked) > 0 {
		r.add(a, info, "Links that refused a bot", "probably fine in a browser: %s", strings.Join(blocked, ", "))
	}
	r.add(a, info, "Link check scope", "%s (%d on this site)", scope, len(internal))
}

func shortErr(err error) string {
	s := err.Error()
	if e, ok := err.(*url.Error); ok {
		s = e.Err.Error()
	}
	if len(s) > 80 {
		s = s[:80] + "…"
	}
	return s
}
