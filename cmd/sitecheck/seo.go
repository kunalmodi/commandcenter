package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

func checkSEO(ctx context.Context, r *report, f *fetcher, pg *page, d *doc) {
	const a = "seo"

	title, ok := d.title()
	n := utf8.RuneCountInString(title)
	switch {
	case !ok || title == "":
		r.add(a, fail, "Title", "missing <title>")
	case n < 10:
		r.add(a, warn, "Title", "%d characters, too short: %q", n, title)
	case n > 60:
		r.add(a, warn, "Title", "%d characters; Google cuts off around 60: %q", n, title)
	default:
		r.add(a, pass, "Title", "%q (%d characters)", title, n)
	}

	desc, ok := d.meta("description")
	n = utf8.RuneCountInString(desc)
	switch {
	case !ok || desc == "":
		r.add(a, warn, "Meta description", "missing (Google will pick a snippet itself)")
	case n < 50:
		r.add(a, warn, "Meta description", "%d characters, short: %q", n, desc)
	case n > 160:
		r.add(a, warn, "Meta description", "%d characters; cut off around 155-160", n)
	default:
		r.add(a, pass, "Meta description", "%d characters", n)
	}

	h1s := d.all("h1")
	switch len(h1s) {
	case 0:
		r.add(a, warn, "H1", "no <h1> on the page")
	case 1:
		r.add(a, pass, "H1", "%q", h1s[0].text)
	default:
		var ts []string
		for _, h := range h1s {
			ts = append(ts, fmt.Sprintf("%q", h.text))
		}
		r.add(a, info, "H1", "%d h1 headings: %s", len(h1s), strings.Join(ts, ", "))
	}

	if h, ok := d.first("html"); !ok || h.attr("lang") == "" {
		r.add(a, warn, "Language", "<html> has no lang attribute")
	} else {
		r.add(a, pass, "Language", h.attr("lang"))
	}
	if v, ok := d.meta("viewport"); !ok {
		r.add(a, fail, "Viewport", "no <meta name=viewport>; mobile renders zoomed out (and ranks worse)")
	} else if !strings.Contains(v, "width=device-width") {
		r.add(a, warn, "Viewport", "%q lacks width=device-width", v)
	}
	hasCharset := false
	for _, m := range d.all("meta") {
		if m.attr("charset") != "" || strings.EqualFold(m.attr("http-equiv"), "content-type") {
			hasCharset = true
		}
	}
	if !hasCharset && !strings.Contains(strings.ToLower(pg.header.Get("Content-Type")), "charset") {
		r.add(a, info, "Charset", "not declared in the header or a <meta charset>")
	}

	checkCanonical(r, pg, d)
	checkIndexable(r, pg, d)
	checkStructuredData(r, d)
	checkImagesAlt(r, d)

	if alts := d.links("alternate"); len(alts) > 0 {
		var langs []string
		for _, t := range alts {
			if hl := t.attr("hreflang"); hl != "" {
				langs = append(langs, hl)
			}
		}
		if len(langs) > 0 {
			r.add(a, info, "hreflang", strings.Join(langs, ", "))
		}
	}

	sitemaps := checkRobotsTxt(ctx, r, f, pg)
	checkSitemap(ctx, r, f, pg, sitemaps)
}

func checkCanonical(r *report, pg *page, d *doc) {
	const a = "seo"
	cs := d.links("canonical")
	if len(cs) == 0 {
		r.add(a, warn, "Canonical", "no <link rel=canonical> (helps when the page is reachable at several URLs)")
		return
	}
	if len(cs) > 1 {
		r.add(a, fail, "Canonical", "%d canonical tags; Google may ignore all of them", len(cs))
	}
	href := cs[0].attr("href")
	cu, err := url.Parse(href)
	if err != nil || href == "" {
		r.add(a, fail, "Canonical", "bad href %q", href)
		return
	}
	if !cu.IsAbs() {
		r.add(a, warn, "Canonical", "relative URL %q; use an absolute one", href)
		cu = pg.final.ResolveReference(cu)
	}
	r.fact("canonical", cu.String())
	if sameURL(cu, pg.final) {
		r.add(a, pass, "Canonical", "points at this page")
	} else {
		r.add(a, warn, "Canonical", "points elsewhere: %s (this page will not be indexed itself; fine if intended)", cu)
	}
}

// sameURL compares ignoring a trailing slash and the fragment.
func sameURL(a, b *url.URL) bool {
	norm := func(u *url.URL) string {
		p := strings.TrimSuffix(u.Path, "/")
		return strings.ToLower(u.Scheme+"://"+u.Host) + p + "?" + u.RawQuery
	}
	return norm(a) == norm(b)
}

func checkIndexable(r *report, pg *page, d *doc) {
	const a = "seo"
	var blocked []string
	for _, k := range []string{"robots", "googlebot"} {
		if v, ok := d.meta(k); ok && strings.Contains(strings.ToLower(v), "noindex") {
			blocked = append(blocked, fmt.Sprintf("<meta name=%s content=%q>", k, v))
		}
	}
	for _, v := range pg.header.Values("X-Robots-Tag") {
		if strings.Contains(strings.ToLower(v), "noindex") {
			blocked = append(blocked, "X-Robots-Tag: "+v)
		}
	}
	if len(blocked) > 0 {
		r.add(a, fail, "Indexable", "page asks not to be indexed: %s", strings.Join(blocked, "; "))
	} else {
		r.add(a, pass, "Indexable", "no noindex")
	}
}

func checkStructuredData(r *report, d *doc) {
	const a = "seo"
	var types []string
	bad := 0
	for _, s := range d.all("script") {
		if !strings.EqualFold(strings.TrimSpace(s.attr("type")), "application/ld+json") {
			continue
		}
		var v any
		if err := json.Unmarshal([]byte(strings.TrimSpace(s.text)), &v); err != nil {
			bad++
			continue
		}
		types = append(types, ldTypes(v)...)
	}
	if bad > 0 {
		r.add(a, fail, "Structured data", "%d JSON-LD block(s) are not valid JSON", bad)
	}
	switch {
	case len(types) > 0:
		sort.Strings(types)
		r.add(a, pass, "Structured data", "JSON-LD types: %s (check eligibility at search.google.com/test/rich-results)", strings.Join(dedupe(types), ", "))
	case bad == 0:
		r.add(a, info, "Structured data", "no JSON-LD (Organization, WebSite, Article or Product markup can earn rich results)")
	}
}

func ldTypes(v any) []string {
	var out []string
	switch x := v.(type) {
	case []any:
		for _, e := range x {
			out = append(out, ldTypes(e)...)
		}
	case map[string]any:
		switch t := x["@type"].(type) {
		case string:
			out = append(out, t)
		case []any:
			for _, e := range t {
				if s, ok := e.(string); ok {
					out = append(out, s)
				}
			}
		}
		if g, ok := x["@graph"]; ok {
			out = append(out, ldTypes(g)...)
		}
	}
	return out
}

func dedupe(s []string) []string {
	var out []string
	for i, v := range s {
		if i == 0 || v != s[i-1] {
			out = append(out, v)
		}
	}
	return out
}

func checkImagesAlt(r *report, d *doc) {
	imgs := d.all("img")
	missing := 0
	var example string
	for _, t := range imgs {
		if _, ok := t.attrs["alt"]; !ok {
			missing++
			if example == "" {
				example = t.attr("src")
			}
		}
	}
	if missing > 0 {
		r.add("seo", warn, "Image alt text", "%d of %d <img> have no alt attribute (use alt=\"\" for decorative ones), e.g. %s", missing, len(imgs), example)
	} else if len(imgs) > 0 {
		r.add("seo", pass, "Image alt text", "all %d images have alt", len(imgs))
	}
}

// checkRobotsTxt reports whether robots.txt blocks this page for Googlebot
// or everyone, and returns any Sitemap: lines.
func checkRobotsTxt(ctx context.Context, r *report, f *fetcher, pg *page) []string {
	const a = "seo"
	u := pg.final.ResolveReference(&url.URL{Path: "/robots.txt"})
	p, err := f.get(ctx, u.String(), "")
	if err != nil {
		r.add(a, info, "robots.txt", "could not fetch: %v", err)
		return nil
	}
	if p.status == 404 {
		r.add(a, info, "robots.txt", "none (everything may be crawled)")
		return nil
	}
	if p.status != 200 {
		r.add(a, warn, "robots.txt", "answers %d; 5xx makes Google stop crawling the site", p.status)
		return nil
	}
	groups, sitemaps := parseRobots(p.body)
	path := pg.final.EscapedPath()
	if pg.final.RawQuery != "" {
		path += "?" + pg.final.RawQuery
	}
	blockedAny := false
	for _, agent := range []string{"googlebot", "bingbot", "facebookexternalhit", "twitterbot"} {
		if !robotsAllowed(groups, agent, path) {
			blockedAny = true
			lvl := fail
			if agent == "facebookexternalhit" || agent == "twitterbot" {
				lvl = warn
			}
			r.add(a, lvl, "robots.txt", "blocks %s from %s", agent, path)
		}
	}
	if !blockedAny {
		r.add(a, pass, "robots.txt", "allows this page")
	}
	return sitemaps
}

type robotsGroup struct {
	agents []string
	rules  []robotsRule
}

type robotsRule struct {
	allow bool
	path  string
}

func parseRobots(body string) ([]robotsGroup, []string) {
	var groups []robotsGroup
	var sitemaps []string
	var cur *robotsGroup
	lastWasAgent := false
	sc := bufio.NewScanner(strings.NewReader(body))
	for sc.Scan() {
		line := sc.Text()
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		k = strings.ToLower(strings.TrimSpace(k))
		v = strings.TrimSpace(v)
		switch k {
		case "user-agent":
			if cur == nil || !lastWasAgent {
				groups = append(groups, robotsGroup{})
				cur = &groups[len(groups)-1]
			}
			cur.agents = append(cur.agents, strings.ToLower(v))
			lastWasAgent = true
			continue
		case "allow", "disallow":
			if cur != nil {
				cur.rules = append(cur.rules, robotsRule{allow: k == "allow", path: v})
			}
		case "sitemap":
			sitemaps = append(sitemaps, v)
		}
		lastWasAgent = false
	}
	return groups, sitemaps
}

// robotsAllowed follows Google's rules: the most specific matching group
// wins, then the longest matching rule; Allow wins ties.
func robotsAllowed(groups []robotsGroup, agent, path string) bool {
	var chosen []robotsRule
	best := -1
	for _, g := range groups {
		for _, a := range g.agents {
			score := -1
			if a == "*" {
				score = 0
			} else if strings.HasPrefix(agent, a) {
				score = len(a)
			}
			if score > best {
				best, chosen = score, g.rules
			} else if score == best && score >= 0 {
				chosen = append(append([]robotsRule(nil), chosen...), g.rules...)
			}
		}
	}
	allowed, length := true, -1
	for _, rl := range chosen {
		if rl.path == "" {
			continue // "Disallow:" with no path allows everything
		}
		if robotsMatch(rl.path, path) {
			if l := len(rl.path); l > length || (l == length && rl.allow) {
				allowed, length = rl.allow, l
			}
		}
	}
	return allowed
}

// robotsMatch supports * wildcards and a trailing $ anchor.
func robotsMatch(pattern, path string) bool {
	anchored := strings.HasSuffix(pattern, "$")
	pattern = strings.TrimSuffix(pattern, "$")
	re := "^" + strings.ReplaceAll(regexp.QuoteMeta(pattern), `\*`, ".*")
	if anchored {
		re += "$"
	}
	ok, _ := regexp.MatchString(re, path)
	return ok
}

func checkSitemap(ctx context.Context, r *report, f *fetcher, pg *page, fromRobots []string) {
	const a = "seo"
	cands := fromRobots
	if len(cands) == 0 {
		cands = []string{pg.final.ResolveReference(&url.URL{Path: "/sitemap.xml"}).String()}
	}
	for _, c := range cands {
		p, err := f.get(ctx, c, "")
		if err != nil || p.status != 200 {
			continue
		}
		body := p.body
		if !strings.Contains(body, "<urlset") && !strings.Contains(body, "<sitemapindex") {
			r.add(a, warn, "Sitemap", "%s does not look like a sitemap", c)
			return
		}
		src := "found at " + c
		if len(fromRobots) == 0 {
			src += " (add a Sitemap: line to robots.txt)"
		}
		r.add(a, pass, "Sitemap", src)
		if strings.Contains(body, "<urlset") && !strings.Contains(body, pg.final.String()) && !strings.Contains(body, strings.TrimSuffix(pg.final.String(), "/")) {
			r.add(a, info, "Sitemap", "this page's URL does not appear in %s", c)
		}
		return
	}
	r.add(a, warn, "Sitemap", "none found (looked at %s)", strings.Join(cands, ", "))
}
