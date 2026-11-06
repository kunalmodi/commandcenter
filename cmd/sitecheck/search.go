package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

// The "search" area reports what the search engines themselves know about a
// page, for sites you own: Google Search Console (index status, the searches
// the page shows up for, sitemaps) and Bing Webmaster Tools (backlinks).
// Both are optional and free; each needs the site verified with that engine.

var (
	gscBase  = "https://www.googleapis.com/webmasters/v3"
	gscIndex = "https://searchconsole.googleapis.com/v1/urlInspection/index:inspect"
	bingBase = "https://ssl.bing.com/webmaster/api.svc/json"
)

// checkSearch also settles whether the site is the user's own: it is if any
// configured account has it verified. Everything else in the report works on
// any public site.
func checkSearch(ctx context.Context, r *report, page *url.URL, now time.Time) {
	ts, why := googleTokens()
	key := strings.TrimSpace(os.Getenv("BING_WEBMASTER_API_KEY"))
	if ts != nil || key != "" {
		// Only judge ownership when there is an account to check against.
		r.mu.Lock()
		r.Owned = new(bool)
		r.mu.Unlock()
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		if ts == nil {
			r.add("search", info, "Google Search Console", "%s", why)
			return
		}
		checkGSC(ctx, r, ts, page, now)
	}()
	go func() {
		defer wg.Done()
		if key == "" {
			r.add("search", info, "Bing Webmaster Tools", "not set up (set BING_WEBMASTER_API_KEY to see backlinks)")
			return
		}
		checkBing(ctx, r, key, page)
	}()
	wg.Wait()
}

// --- Google Search Console ---

type gscClient struct {
	ts tokenSource
}

func (c *gscClient) call(ctx context.Context, method, u string, body, out any) error {
	tok, err := c.ts.token(ctx)
	if err != nil {
		return err
	}
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, method, u, rd)
	req.Header.Set("Authorization", "Bearer "+tok)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode != 200 {
		var e struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		json.Unmarshal(data, &e)
		msg := e.Error.Message
		if msg == "" {
			msg = resp.Status
		}
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, msg)
	}
	return json.Unmarshal(data, out)
}

type gscSite struct {
	SiteURL         string `json:"siteUrl"`
	PermissionLevel string `json:"permissionLevel"`
}

// pickProperty chooses the Search Console property covering page: a domain
// property (sc-domain:example.com) if there is one, else the longest
// URL-prefix property.
func pickProperty(sites []gscSite, page *url.URL) (string, bool) {
	host := strings.ToLower(page.Hostname())
	best, bestLen := "", -1
	for _, s := range sites {
		if s.PermissionLevel == "siteUnverifiedUser" {
			continue
		}
		if d, ok := strings.CutPrefix(s.SiteURL, "sc-domain:"); ok {
			d = strings.ToLower(d)
			if host == d || strings.HasSuffix(host, "."+d) {
				return s.SiteURL, true
			}
			continue
		}
		if strings.HasPrefix(strings.ToLower(page.String()), strings.ToLower(s.SiteURL)) && len(s.SiteURL) > bestLen {
			best, bestLen = s.SiteURL, len(s.SiteURL)
		}
	}
	return best, bestLen >= 0
}

func checkGSC(ctx context.Context, r *report, ts tokenSource, page *url.URL, now time.Time) {
	const a = "search"
	c := &gscClient{ts: ts}
	var sites struct {
		SiteEntry []gscSite `json:"siteEntry"`
	}
	if err := c.call(ctx, http.MethodGet, gscBase+"/sites", nil, &sites); err != nil {
		r.add(a, warn, "Google Search Console", "could not list properties: %v", err)
		return
	}
	prop, ok := pickProperty(sites.SiteEntry, page)
	if !ok {
		// Not your site (or not shared with you): that's normal, not a problem.
		r.add(a, info, "Google Search Console", "%s is not one of your verified properties, so there's no Google index or query data (the owner can add you as a user)", page.Host)
		return
	}
	r.fact("gsc_property", prop)
	r.verifiedIn("Google Search Console (" + prop + ")")

	var wg sync.WaitGroup
	wg.Add(3)
	go func() { defer wg.Done(); gscInspect(ctx, r, c, prop, page) }()
	go func() { defer wg.Done(); gscPerformance(ctx, r, c, prop, page, now) }()
	go func() { defer wg.Done(); gscSitemaps(ctx, r, c, prop) }()
	wg.Wait()
}

type inspection struct {
	InspectionResult struct {
		IndexStatusResult struct {
			Verdict         string `json:"verdict"`
			CoverageState   string `json:"coverageState"`
			RobotsTxtState  string `json:"robotsTxtState"`
			IndexingState   string `json:"indexingState"`
			LastCrawlTime   string `json:"lastCrawlTime"`
			PageFetchState  string `json:"pageFetchState"`
			GoogleCanonical string `json:"googleCanonical"`
			UserCanonical   string `json:"userCanonical"`
			CrawledAs       string `json:"crawledAs"`
		} `json:"indexStatusResult"`
		RichResultsResult *struct {
			Verdict       string `json:"verdict"`
			DetectedItems []struct {
				RichResultType string `json:"richResultType"`
				Items          []struct {
					Issues []struct {
						IssueMessage string `json:"issueMessage"`
						Severity     string `json:"severity"`
					} `json:"issues"`
				} `json:"items"`
			} `json:"detectedItems"`
		} `json:"richResultsResult"`
	} `json:"inspectionResult"`
}

func gscInspect(ctx context.Context, r *report, c *gscClient, prop string, page *url.URL) {
	const a = "search"
	var res inspection
	err := c.call(ctx, http.MethodPost, gscIndex, map[string]string{"inspectionUrl": page.String(), "siteUrl": prop}, &res)
	if err != nil {
		r.add(a, warn, "Google index status", "URL inspection failed: %v", err)
		return
	}
	ix := res.InspectionResult.IndexStatusResult
	crawl := ""
	if t, err := time.Parse(time.RFC3339, ix.LastCrawlTime); err == nil {
		crawl = fmt.Sprintf("; last crawled %s", t.Format("2006-01-02"))
		if ix.CrawledAs != "" {
			crawl += " as " + strings.ToLower(ix.CrawledAs)
		}
	}
	switch ix.Verdict {
	case "PASS":
		r.add(a, pass, "Google index status", "%s%s", ix.CoverageState, crawl)
	case "FAIL":
		r.add(a, fail, "Google index status", "%s%s", ix.CoverageState, crawl)
	default:
		// NEUTRAL covers "URL is unknown to Google", "Discovered - currently
		// not indexed", "Crawled - currently not indexed", excluded by noindex...
		r.add(a, warn, "Google index status", "not indexed: %s%s", ix.CoverageState, crawl)
	}
	if ix.RobotsTxtState == "DISALLOWED" {
		r.add(a, fail, "Google crawl", "robots.txt blocks Googlebot from this URL")
	}
	if ix.PageFetchState != "" && ix.PageFetchState != "SUCCESSFUL" && ix.PageFetchState != "PAGE_FETCH_STATE_UNSPECIFIED" {
		r.add(a, fail, "Google crawl", "Google's last fetch failed: %s", strings.ToLower(strings.ReplaceAll(ix.PageFetchState, "_", " ")))
	}
	if ix.GoogleCanonical != "" && ix.UserCanonical != "" && ix.GoogleCanonical != ix.UserCanonical {
		r.add(a, warn, "Google canonical", "Google chose %s instead of your canonical %s", ix.GoogleCanonical, ix.UserCanonical)
	}
	if rr := res.InspectionResult.RichResultsResult; rr != nil {
		var types, issues []string
		for _, d := range rr.DetectedItems {
			types = append(types, d.RichResultType)
			for _, it := range d.Items {
				for _, is := range it.Issues {
					issues = append(issues, d.RichResultType+": "+is.IssueMessage)
				}
			}
		}
		switch {
		case rr.Verdict == "FAIL":
			r.add(a, warn, "Rich results", "not eligible: %s", strings.Join(issues, "; "))
		case len(issues) > 0:
			r.add(a, info, "Rich results", "%s eligible, with warnings: %s", strings.Join(types, ", "), strings.Join(issues, "; "))
		case len(types) > 0:
			r.add(a, pass, "Rich results", "eligible: %s", strings.Join(types, ", "))
		}
	}
}

type gscRow struct {
	Keys        []string `json:"keys"`
	Clicks      float64  `json:"clicks"`
	Impressions float64  `json:"impressions"`
	CTR         float64  `json:"ctr"`
	Position    float64  `json:"position"`
}

func (c *gscClient) query(ctx context.Context, prop string, body map[string]any) ([]gscRow, error) {
	var out struct {
		Rows []gscRow `json:"rows"`
	}
	err := c.call(ctx, http.MethodPost, gscBase+"/sites/"+url.PathEscape(prop)+"/searchAnalytics/query", body, &out)
	return out.Rows, err
}

// gscPerformance reports the last 28 days of Google search traffic for the
// page and the whole property, top queries, and "striking distance" queries
// (ranking 5-20: a small improvement moves them onto page one or up it).
func gscPerformance(ctx context.Context, r *report, c *gscClient, prop string, page *url.URL, now time.Time) {
	const a = "search"
	// Search Console data lags 2-3 days.
	end := now.AddDate(0, 0, -3)
	start := end.AddDate(0, 0, -27)
	window := fmt.Sprintf("%s to %s", start.Format("Jan 2"), end.Format("Jan 2"))
	base := func() map[string]any {
		return map[string]any{"startDate": start.Format("2006-01-02"), "endDate": end.Format("2006-01-02"), "dataState": "final"}
	}
	pageFilter := []map[string]any{{"filters": []map[string]any{{"dimension": "page", "operator": "equals", "expression": page.String()}}}}

	site := base()
	siteRows, err := c.query(ctx, prop, site)
	if err != nil {
		r.add(a, warn, "Google search traffic", "query failed: %v", err)
		return
	}
	if len(siteRows) > 0 {
		s := siteRows[0]
		r.add(a, info, "Google search, whole site", "%s: %.0f clicks, %.0f impressions, %.1f%% CTR, avg position %.1f", window, s.Clicks, s.Impressions, s.CTR*100, s.Position)
	} else {
		r.add(a, info, "Google search, whole site", "no impressions %s (new or unindexed site, or the data hasn't arrived yet)", window)
	}

	pq := base()
	pq["dimensions"] = []string{"query"}
	pq["dimensionFilterGroups"] = pageFilter
	pq["rowLimit"] = 250
	rows, err := c.query(ctx, prop, pq)
	if err != nil {
		r.add(a, warn, "Google search, this page", "query failed: %v", err)
		return
	}
	if len(rows) == 0 {
		r.add(a, info, "Google search, this page", "no impressions %s", window)
		return
	}
	var clicks, imps, posW float64
	for _, q := range rows {
		clicks += q.Clicks
		imps += q.Impressions
		posW += q.Position * q.Impressions
	}
	r.add(a, info, "Google search, this page", "%s: %.0f clicks, %.0f impressions across %d queries, avg position %.1f", window, clicks, imps, len(rows), posW/imps)

	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Clicks != rows[j].Clicks {
			return rows[i].Clicks > rows[j].Clicks
		}
		return rows[i].Impressions > rows[j].Impressions
	})
	var top []string
	for i, q := range rows {
		if i == 8 {
			break
		}
		top = append(top, fmt.Sprintf("%q (%.0f clicks, %.0f impr, pos %.1f)", q.Keys[0], q.Clicks, q.Impressions, q.Position))
	}
	r.add(a, info, "Top queries", strings.Join(top, "; "))
	r.fact("gsc_top_queries", rows[:min(len(rows), 25)])

	var near []gscRow
	for _, q := range rows {
		if q.Position >= 5 && q.Position <= 20 && q.Impressions >= 10 {
			near = append(near, q)
		}
	}
	sort.Slice(near, func(i, j int) bool { return near[i].Impressions > near[j].Impressions })
	if len(near) > 0 {
		var s []string
		for i, q := range near {
			if i == 6 {
				break
			}
			s = append(s, fmt.Sprintf("%q (pos %.1f, %.0f impr)", q.Keys[0], q.Position, q.Impressions))
		}
		r.add(a, info, "Striking-distance queries", "ranking 5-20 with real demand; mention them in the title, headings or copy: %s", strings.Join(s, "; "))
	}
	for _, q := range rows {
		if q.Position <= 3 && q.Impressions >= 200 && q.CTR < 0.02 {
			r.add(a, warn, "Low click-through", "%q ranks %.1f with %.0f impressions but %.1f%% CTR; the title or description may not match what searchers want", q.Keys[0], q.Position, q.Impressions, q.CTR*100)
			break
		}
	}
}

func gscSitemaps(ctx context.Context, r *report, c *gscClient, prop string) {
	const a = "search"
	var out struct {
		Sitemap []struct {
			Path           string `json:"path"`
			LastDownloaded string `json:"lastDownloaded"`
			IsPending      bool   `json:"isPending"`
			Errors         string `json:"errors"`
			Warnings       string `json:"warnings"`
		} `json:"sitemap"`
	}
	if err := c.call(ctx, http.MethodGet, gscBase+"/sites/"+url.PathEscape(prop)+"/sitemaps", nil, &out); err != nil {
		r.add(a, info, "Search Console sitemaps", "could not list: %v", err)
		return
	}
	if len(out.Sitemap) == 0 {
		r.add(a, warn, "Search Console sitemaps", "none submitted (Sitemaps → Add a new sitemap; helps new pages get found faster)")
		return
	}
	for _, s := range out.Sitemap {
		if s.Errors != "" && s.Errors != "0" {
			r.add(a, warn, "Search Console sitemaps", "%s has %s errors", s.Path, s.Errors)
		} else {
			when := "not downloaded yet"
			if t, err := time.Parse(time.RFC3339, s.LastDownloaded); err == nil {
				when = "last read " + t.Format("2006-01-02")
			}
			r.add(a, pass, "Search Console sitemaps", "%s (%s)", s.Path, when)
		}
	}
}

// --- Bing Webmaster Tools ---

func bingCall(ctx context.Context, key, method string, params url.Values, out any) error {
	params.Set("apikey", key)
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, bingBase+"/"+method+"?"+params.Encode(), nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return errors.New(strings.ReplaceAll(err.Error(), key, "REDACTED"))
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode != 200 {
		var e struct {
			Message   string `json:"Message"`
			ErrorCode int    `json:"ErrorCode"`
		}
		json.Unmarshal(data, &e)
		msg := e.Message
		if msg == "" {
			msg = resp.Status
		}
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, msg)
	}
	var wrap struct {
		D json.RawMessage `json:"d"`
	}
	if err := json.Unmarshal(data, &wrap); err != nil {
		return err
	}
	return json.Unmarshal(wrap.D, out)
}

func pickBingSite(sites []struct {
	URL        string `json:"Url"`
	IsVerified bool   `json:"IsVerified"`
}, page *url.URL) (string, bool) {
	best := ""
	for _, s := range sites {
		if !s.IsVerified {
			continue
		}
		u, err := url.Parse(s.URL)
		if err != nil {
			continue
		}
		if strings.EqualFold(u.Hostname(), page.Hostname()) && len(s.URL) > len(best) {
			best = s.URL
		}
	}
	return best, best != ""
}

func checkBing(ctx context.Context, r *report, key string, page *url.URL) {
	const a = "search"
	var sites []struct {
		URL        string `json:"Url"`
		IsVerified bool   `json:"IsVerified"`
	}
	if err := bingCall(ctx, key, "GetUserSites", url.Values{}, &sites); err != nil {
		r.add(a, warn, "Bing Webmaster Tools", "could not list sites: %v", err)
		return
	}
	site, ok := pickBingSite(sites, page)
	if !ok {
		r.add(a, info, "Bing Webmaster Tools", "%s is not a verified site on your Bing account, so no backlink data (if it's yours, import it from Search Console)", page.Host)
		return
	}
	r.verifiedIn("Bing Webmaster Tools")

	type linkCounts struct {
		Links []struct {
			Count int    `json:"Count"`
			URL   string `json:"Url"`
		} `json:"Links"`
		TotalPages int `json:"TotalPages"`
	}
	total, pages, here := 0, 0, 0
	for p := 0; p < 10; p++ {
		var lc linkCounts
		if err := bingCall(ctx, key, "GetLinkCounts", url.Values{"siteUrl": {site}, "page": {fmt.Sprint(p)}}, &lc); err != nil {
			r.add(a, warn, "Backlinks (Bing)", "GetLinkCounts failed: %v", err)
			return
		}
		for _, l := range lc.Links {
			total += l.Count
			pages++
			if u, err := url.Parse(l.URL); err == nil && sameURL(u, page) {
				here = l.Count
			}
		}
		if p+1 >= lc.TotalPages {
			break
		}
	}
	if total == 0 {
		r.add(a, info, "Backlinks (Bing)", "Bing knows of no inbound links to %s yet (normal for a new site; Bing updates slowly)", site)
		return
	}
	r.fact("bing_inbound_links", total)
	r.add(a, info, "Backlinks (Bing)", "%d inbound links to %d pages of the site; %d to this page", total, pages, here)

	var ul struct {
		Details []struct {
			AnchorText string `json:"AnchorText"`
			URL        string `json:"Url"`
		} `json:"Details"`
	}
	if err := bingCall(ctx, key, "GetUrlLinks", url.Values{"siteUrl": {site}, "link": {page.String()}, "page": {"0"}}, &ul); err != nil || len(ul.Details) == 0 {
		return
	}
	domains := map[string]bool{}
	var ex []string
	for _, d := range ul.Details {
		if u, err := url.Parse(d.URL); err == nil {
			domains[strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")] = true
		}
		if len(ex) < 6 {
			s := d.URL
			if t := strings.TrimSpace(d.AnchorText); t != "" {
				s += fmt.Sprintf(" (%q)", t)
			}
			ex = append(ex, s)
		}
	}
	r.add(a, info, "Linking pages (Bing)", "%d referring domains in the first page of results, e.g. %s", len(domains), strings.Join(ex, ", "))
}

func (r *report) verifiedIn(where string) {
	r.mu.Lock()
	*r.Owned = true
	r.VerifiedIn = append(r.VerifiedIn, where)
	r.mu.Unlock()
}
