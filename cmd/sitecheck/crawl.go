package main

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Site crawl (-crawl N): advertools (Scrapy) crawls up to N pages, seeded
// with the sitemap and following links, obeying robots.txt. sitecheck then
// runs the checks that only make sense across pages: broken internal links,
// duplicate titles and descriptions, sitemap entries that redirect, error or
// are noindexed, language versions that don't link back, orphan pages, and
// site-wide image alt and link text.

type crawlRow map[string]any

func (c crawlRow) str(k string) string {
	switch v := c[k].(type) {
	case string:
		return v
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	}
	return ""
}

func (c crawlRow) status() int {
	n, _ := strconv.Atoi(c.str("status"))
	return n
}

// urlKey normalises a URL for comparisons: lowercase scheme and host, no
// fragment, no trailing slash.
func urlKey(s string) string {
	u, err := url.Parse(strings.TrimSpace(s))
	if err != nil {
		return s
	}
	return strings.ToLower(u.Scheme+"://"+u.Host) + strings.TrimSuffix(u.EscapedPath(), "/") + qs(u.RawQuery)
}

func qs(q string) string {
	if q == "" {
		return ""
	}
	return "?" + q
}

func pathOf(s string) string {
	if u, err := url.Parse(s); err == nil {
		p := u.EscapedPath()
		if p == "" {
			p = "/"
		}
		return p + qs(u.RawQuery)
	}
	return s
}

// --- sitemap ---

type sitemapXML struct {
	XMLName xml.Name
	URLs    []struct {
		Loc string `xml:"loc"`
	} `xml:"url"`
	Sitemaps []struct {
		Loc string `xml:"loc"`
	} `xml:"sitemap"`
}

// sitemapURLs collects page URLs from robots.txt Sitemap: lines (or
// /sitemap.xml), following sitemap indexes one level, up to max.
func sitemapURLs(ctx context.Context, f *fetcher, base *url.URL, max int) []string {
	var roots []string
	if p, err := f.get(ctx, base.ResolveReference(&url.URL{Path: "/robots.txt"}).String(), ""); err == nil && p.status == 200 {
		_, roots = parseRobots(p.body)
	}
	if len(roots) == 0 {
		roots = []string{base.ResolveReference(&url.URL{Path: "/sitemap.xml"}).String()}
	}
	var out []string
	seen := map[string]bool{}
	var visit func(u string, depth int)
	visit = func(u string, depth int) {
		if len(out) >= max || seen[u] || depth > 2 {
			return
		}
		seen[u] = true
		p, err := f.get(ctx, u, "")
		if err != nil || p.status != 200 {
			return
		}
		body := []byte(p.body)
		if bytes.HasPrefix(body, []byte{0x1f, 0x8b}) {
			if zr, err := gzip.NewReader(bytes.NewReader(body)); err == nil {
				var buf bytes.Buffer
				buf.ReadFrom(zr)
				body = buf.Bytes()
			}
		}
		var sm sitemapXML
		if xml.Unmarshal(body, &sm) != nil {
			return
		}
		for _, x := range sm.URLs {
			if len(out) >= max {
				return
			}
			if l := strings.TrimSpace(x.Loc); l != "" {
				out = append(out, l)
			}
		}
		for _, x := range sm.Sitemaps {
			visit(strings.TrimSpace(x.Loc), depth+1)
		}
	}
	for _, r := range roots {
		visit(r, 0)
	}
	return out
}

// --- crawl ---

func runCrawl(ctx context.Context, seeds []string, limit int, out string) ([]crawlRow, error) {
	args := []string{"--from", advertoolsPin, "advertools", "crawl", out, "--follow-links", "1",
		"--custom-settings",
		"CLOSESPIDER_PAGECOUNT=" + strconv.Itoa(limit),
		"CONCURRENT_REQUESTS_PER_DOMAIN=4",
		"ROBOTSTXT_OBEY=True",
		"LOG_LEVEL=ERROR",
	}
	os.Remove(out) // advertools appends to an existing file
	// The advertools CLI reads start URLs from stdin when it isn't a terminal.
	if _, err := runTool(ctx, "uvx", args, strings.NewReader(strings.Join(seeds, "\n")+"\n"), 15*time.Minute); err != nil {
		return nil, err
	}
	fh, err := os.Open(out)
	if err != nil {
		return nil, fmt.Errorf("advertools wrote no output: %v", err)
	}
	defer fh.Close()
	var rows []crawlRow
	sc := bufio.NewScanner(fh)
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20)
	for sc.Scan() {
		var row crawlRow
		if json.Unmarshal(sc.Bytes(), &row) == nil {
			rows = append(rows, row)
		}
	}
	return rows, sc.Err()
}

func checkSite(ctx context.Context, r *report, f *fetcher, pg *page, limit int, outDir string) {
	const a = "site"
	start := pg.final
	sm := sitemapURLs(ctx, f, start, 50000)
	var smSame []string
	for _, u := range sm {
		if pu, err := url.Parse(u); err == nil && strings.EqualFold(pu.Hostname(), start.Hostname()) {
			smSame = append(smSame, u)
		}
	}
	seeds := []string{start.String()}
	for _, u := range smSame {
		if len(seeds) >= limit {
			break
		}
		seeds = append(seeds, u)
	}
	out := filepath.Join(outDir, "crawl.jl")
	rows, err := runCrawl(ctx, seeds, limit, out)
	if err != nil {
		r.add(a, warn, "Site crawl", "advertools crawl failed: %v", err)
		return
	}
	r.file("crawl", out)
	analyzeCrawl(r, start, rows, smSame, limit)
}

// analyzeCrawl is separate from the crawl itself so it can be tested on
// recorded rows.
func analyzeCrawl(r *report, start *url.URL, rows []crawlRow, sitemap []string, limit int) {
	const a = "site"
	host := strings.ToLower(start.Hostname())
	var pages []crawlRow // HTML pages on this host with a 200
	byKey := map[string]crawlRow{}
	statusCount := map[int]int{}
	for _, row := range rows {
		u := row.str("url")
		pu, err := url.Parse(u)
		if err != nil || strings.ToLower(pu.Hostname()) != host {
			continue
		}
		st := row.status()
		statusCount[st]++
		byKey[urlKey(u)] = row
		for _, ru := range splitMulti(row["redirect_urls"]) {
			byKey[urlKey(ru)] = row
		}
		if st == 200 && strings.Contains(strings.ToLower(row.str("resp_headers_Content-Type")), "html") {
			pages = append(pages, row)
		}
	}
	var codes []string
	for c, n := range statusCount {
		codes = append(codes, fmt.Sprintf("%d×%d", n, c))
	}
	sort.Strings(codes)
	cover := fmt.Sprintf("crawled %d URLs (limit %d)", len(byKey), limit)
	if len(sitemap) > 0 {
		cover += fmt.Sprintf("; the sitemap lists %d", len(sitemap))
	}
	r.add(a, info, "Crawl", "%s; status codes: %s", cover, strings.Join(codes, ", "))
	r.fact("crawl_pages", len(pages))

	// Broken internal links: crawled URLs that errored, with where they're linked from.
	var broken []string
	for _, row := range rows {
		if st := row.status(); st >= 400 {
			from := pathOf(row.str("request_headers_Referer"))
			if from == "" || row.str("request_headers_Referer") == "" {
				from = "the sitemap or start list"
			}
			broken = append(broken, fmt.Sprintf("%s (%d, linked from %s)", pathOf(row.str("url")), st, from))
		}
	}
	if len(broken) > 0 {
		sort.Strings(broken)
		r.add(a, fail, "Broken internal URLs", "%d: %s", len(broken), strings.Join(broken[:min(len(broken), 8)], "; "))
	} else {
		r.add(a, pass, "Broken internal URLs", "none among crawled pages")
	}

	// Internal links that go through redirects.
	var redirected []string
	for _, row := range rows {
		if rs := splitMulti(row["redirect_urls"]); len(rs) > 0 {
			redirected = append(redirected, fmt.Sprintf("%s → %s", pathOf(rs[0]), pathOf(row.str("url"))))
		}
	}
	if len(redirected) > 0 {
		r.add(a, info, "Redirected URLs", "%d links or sitemap entries go through a redirect (link to the final URL instead), e.g. %s", len(redirected), strings.Join(redirected[:min(len(redirected), 3)], "; "))
	}

	// Per-page tag problems, and duplicates across pages.
	titles, descs := map[string][]string{}, map[string][]string{}
	var noTitle, noDesc, noH1, multiH1, noindex, canonElsewhere, thin []string
	for _, p := range pages {
		path := pathOf(p.str("url"))
		t, d := strings.TrimSpace(p.str("title")), strings.TrimSpace(p.str("meta_desc"))
		if t == "" {
			noTitle = append(noTitle, path)
		} else {
			titles[strings.ToLower(t)] = append(titles[strings.ToLower(t)], path)
		}
		if d == "" {
			noDesc = append(noDesc, path)
		} else {
			descs[strings.ToLower(d)] = append(descs[strings.ToLower(d)], path)
		}
		switch h := splitMulti(p["h1"]); len(h) {
		case 0:
			noH1 = append(noH1, path)
		case 1:
		default:
			multiH1 = append(multiH1, path)
		}
		robots := strings.ToLower(p.str("meta_robots") + " " + p.str("resp_headers_X-Robots-Tag"))
		if strings.Contains(robots, "noindex") {
			noindex = append(noindex, path)
		}
		if c := p.str("canonical"); c != "" && urlKey(c) != urlKey(p.str("url")) {
			canonElsewhere = append(canonElsewhere, path+" → "+pathOf(c))
		}
		if n := len(wordRe.FindAllString(p.str("body_text"), -1)); n < 200 {
			thin = append(thin, fmt.Sprintf("%s (%d)", path, n))
		}
	}
	dupes := func(m map[string][]string) []string {
		var out []string
		for txt, ps := range m {
			if len(ps) > 1 {
				sort.Strings(ps)
				out = append(out, fmt.Sprintf("%q on %d pages (%s)", truncate(txt, 60), len(ps), strings.Join(ps[:min(len(ps), 3)], ", ")))
			}
		}
		sort.Strings(out)
		return out
	}
	if d := dupes(titles); len(d) > 0 {
		r.add(a, warn, "Duplicate titles", "%d titles are shared by several pages; each page should have its own: %s", len(d), strings.Join(d[:min(len(d), 4)], "; "))
	} else if len(pages) > 1 {
		r.add(a, pass, "Duplicate titles", "none")
	}
	if d := dupes(descs); len(d) > 0 {
		r.add(a, warn, "Duplicate meta descriptions", "%d descriptions are shared by several pages: %s", len(d), strings.Join(d[:min(len(d), 4)], "; "))
	}
	list := func(lvl level, check, why string, items []string) {
		if len(items) > 0 {
			r.add(a, lvl, check, "%d pages%s: %s", len(items), why, strings.Join(items[:min(len(items), 6)], ", "))
		}
	}
	list(fail, "Missing titles", "", noTitle)
	list(warn, "Missing meta descriptions", "", noDesc)
	list(warn, "Missing h1", "", noH1)
	list(info, "Several h1s", "", multiH1)
	list(info, "Noindexed pages", " ask not to be indexed (fine for search results, account and thank-you pages)", noindex)
	list(info, "Canonical points elsewhere", "", canonElsewhere)
	list(info, "Little text", " have under 200 words in total, navigation included", thin)

	// Sitemap health: every entry should be a 200, indexable, self-canonical page.
	if len(sitemap) > 0 {
		var bad []string
		for _, s := range sitemap {
			row, ok := byKey[urlKey(s)]
			if !ok {
				continue
			}
			switch {
			case len(splitMulti(row["redirect_urls"])) > 0:
				bad = append(bad, pathOf(s)+" redirects")
			case row.status() != 200:
				bad = append(bad, fmt.Sprintf("%s returns %d", pathOf(s), row.status()))
			case strings.Contains(strings.ToLower(row.str("meta_robots")+row.str("resp_headers_X-Robots-Tag")), "noindex"):
				bad = append(bad, pathOf(s)+" is noindex")
			case row.str("canonical") != "" && urlKey(row.str("canonical")) != urlKey(s):
				bad = append(bad, pathOf(s)+" canonicalises elsewhere")
			}
		}
		if len(bad) > 0 {
			r.add(a, warn, "Sitemap entries", "%d entries shouldn't be in the sitemap (it should list only final, indexable URLs): %s", len(bad), strings.Join(bad[:min(len(bad), 6)], "; "))
		} else {
			r.add(a, pass, "Sitemap entries", "crawled entries are all 200, indexable and self-canonical")
		}

		// Orphans: in the sitemap but not linked from any crawled page. Only
		// meaningful when the crawl saw (nearly) the whole site.
		if len(pages)*10 < len(sitemap)*9 {
			r.add(a, info, "Orphan pages", "not checked: the crawl covered %d of %d sitemap pages (run with -crawl %d to check)", len(pages), len(sitemap), len(sitemap)+50)
		} else {
			linked := map[string]bool{}
			for _, p := range pages {
				for _, l := range splitMulti(p["links_url"]) {
					linked[urlKey(l)] = true
				}
			}
			var orphans []string
			for _, s := range sitemap {
				if !linked[urlKey(s)] && urlKey(s) != urlKey(start.String()) {
					orphans = append(orphans, pathOf(s))
				}
			}
			if len(orphans) > 0 {
				r.add(a, warn, "Orphan pages", "%d sitemap pages aren't linked from any other page; Google finds and values pages through links: %s", len(orphans), strings.Join(orphans[:min(len(orphans), 6)], ", "))
			} else {
				r.add(a, pass, "Orphan pages", "every sitemap page is linked from somewhere")
			}
		}
	}

	// hreflang: if A lists B as an alternate, B must list A back.
	alts := map[string]map[string]bool{}
	display := map[string]string{} // urlKey → URL as written
	for _, p := range pages {
		display[urlKey(p.str("url"))] = p.str("url")
		set := map[string]bool{}
		for _, h := range splitMulti(p["alt_href"]) {
			set[urlKey(h)] = true
			if display[urlKey(h)] == "" {
				display[urlKey(h)] = h
			}
		}
		if len(set) > 0 {
			alts[urlKey(p.str("url"))] = set
		}
	}
	var noReturn []string
	for from, set := range alts {
		for to := range set {
			if to == from {
				continue
			}
			back, crawled := alts[to]
			if _, seen := byKey[to]; crawled || seen {
				if !back[from] {
					noReturn = append(noReturn, pathOf(display[to])+" doesn't link back to "+pathOf(display[from]))
				}
			}
		}
	}
	if len(noReturn) > 0 {
		sort.Strings(noReturn)
		r.add(a, warn, "hreflang return links", "%d missing (Google ignores one-way hreflang): %s", len(noReturn), strings.Join(noReturn[:min(len(noReturn), 5)], "; "))
	} else if len(alts) > 0 {
		r.add(a, pass, "hreflang return links", "all language versions crawled link back to each other")
	}

	// Site-wide images and link text.
	imgs, emptyAlt := 0, 0
	generic := map[string]int{}
	for _, p := range pages {
		srcs := splitMulti(p["img_src"])
		altsList := splitMulti(p["img_alt"])
		imgs += len(srcs)
		for i := range srcs {
			if i >= len(altsList) || strings.TrimSpace(altsList[i]) == "" {
				emptyAlt++
			}
		}
		for _, t := range splitMulti(p["links_text"]) {
			if low := strings.ToLower(strings.TrimSpace(t)); genericAnchors[low] {
				generic[strings.TrimSpace(t)]++
			}
		}
	}
	if imgs > 0 && emptyAlt*4 >= imgs {
		r.add(a, warn, "Image alt text, site-wide", "%d of %d images have empty or missing alt", emptyAlt, imgs)
	}
	if len(generic) > 0 {
		var g []string
		total := 0
		for t, n := range generic {
			g = append(g, fmt.Sprintf("%q ×%d", t, n))
			total += n
		}
		sort.Strings(g)
		r.add(a, warn, "Vague link text, site-wide", "%d links: %s", total, strings.Join(g[:min(len(g), 5)], ", "))
	}
}
