// sitecheck audits one web page for performance, share previews, SEO and the
// basics a launch checklist would cover, and prints a findings report. It is
// the engine behind the site-check skill (skills/site-check).
//
//	sitecheck [flags] https://example.com/page
//
// Checks:
//
//	performance  Google PageSpeed Insights (Lighthouse lab scores, Core Web
//	             Vitals field data, top opportunities), mobile and desktop.
//	             Falls back to local Lighthouse (headless Chrome) if the API fails
//	social       Open Graph and X card tags as crawlers see them (fetched with
//	             facebookexternalhit and Twitterbot user agents), and the share
//	             image itself: reachable, type, size, dimensions, aspect ratio
//	seo          title, description, h1, canonical, robots/noindex, robots.txt,
//	             sitemap, structured data, lang, viewport, image alt text
//	basics       status, HTTPS and redirects, TLS expiry, compression, HTTP/2,
//	             soft 404s, favicon
//	security     common security headers, mixed content
//	search       for sites you own: Google Search Console (index status,
//	             queries, sitemaps) and Bing Webmaster Tools (backlinks)
//	content      main text via trafilatura (saved to content.md), outline,
//	             title/h1 agreement, link text
//	aeo          answer engines / AI search: AI crawler access (robots.txt and
//	             live fetches), text without JavaScript, entity/author/date
//	             markup, answer-shaped structure, llms.txt
//	site         with -crawl N: advertools crawls the site; broken links,
//	             duplicates, sitemap health, orphans, hreflang return links
//	links        broken links on the page (first -links of them)
//
// Settings (env or ~/.config/commandcenter/sitecheck.env): PAGESPEED_API_KEY,
// GSC_SERVICE_ACCOUNT_FILE or GOOGLE_OAUTH_CLIENT_FILE, BING_WEBMASTER_API_KEY.
// Without a key, PageSpeed Insights often answers 429 (shared quota).
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/kunalmodi/commandcenter/internal/env"
)

const version = "0.1.0"

type level int

const (
	pass level = iota
	info
	warn
	fail
)

func (l level) String() string { return [...]string{"PASS", "INFO", "WARN", "FAIL"}[l] }

func (l level) MarshalText() ([]byte, error) { return []byte(l.String()), nil }

type finding struct {
	Area   string `json:"area"`
	Level  level  `json:"level"`
	Check  string `json:"check"`
	Detail string `json:"detail,omitempty"`
}

// report collects findings from checks running in parallel.
type report struct {
	mu       sync.Mutex
	URL      string            `json:"url"`
	Final    string            `json:"final_url,omitempty"`
	Findings []finding         `json:"findings"`
	Files    map[string]string `json:"files,omitempty"` // e.g. share image saved for viewing
	Facts    map[string]any    `json:"facts,omitempty"`
	// Owned is set when search data was looked up: true if the site is
	// verified in one of the user's Search Console or Bing accounts.
	Owned      *bool    `json:"owned,omitempty"`
	VerifiedIn []string `json:"verified_in,omitempty"`
}

func (r *report) add(area string, l level, check, detail string, args ...any) {
	if len(args) > 0 {
		detail = fmt.Sprintf(detail, args...)
	}
	r.mu.Lock()
	r.Findings = append(r.Findings, finding{Area: area, Level: l, Check: check, Detail: detail})
	r.mu.Unlock()
}

func (r *report) file(k, path string) {
	r.mu.Lock()
	if r.Files == nil {
		r.Files = map[string]string{}
	}
	r.Files[k] = path
	r.mu.Unlock()
}

func (r *report) fact(k string, v any) {
	r.mu.Lock()
	if r.Facts == nil {
		r.Facts = map[string]any{}
	}
	r.Facts[k] = v
	r.mu.Unlock()
}

var areaOrder = []string{"basics", "performance", "search", "seo", "content", "aeo", "site", "social", "security", "links"}

func (r *report) sort() {
	rank := map[string]int{}
	for i, a := range areaOrder {
		rank[a] = i
	}
	sort.SliceStable(r.Findings, func(i, j int) bool {
		a, b := r.Findings[i], r.Findings[j]
		if a.Area != b.Area {
			return rank[a.Area] < rank[b.Area]
		}
		return a.Level > b.Level
	})
}

func (r *report) writeText(w io.Writer, showPass bool) {
	counts := map[level]int{}
	for _, f := range r.Findings {
		counts[f.Level]++
	}
	fmt.Fprintf(w, "sitecheck %s: %s\n", version, r.URL)
	if r.Final != "" && r.Final != r.URL {
		fmt.Fprintf(w, "final URL: %s\n", r.Final)
	}
	if r.Owned != nil {
		if *r.Owned {
			fmt.Fprintf(w, "your site: verified in %s\n", strings.Join(r.VerifiedIn, " and "))
		} else {
			fmt.Fprintln(w, "not verified as your site: public checks only, no search-engine data")
		}
	}
	fmt.Fprintf(w, "%d FAIL, %d WARN, %d INFO, %d PASS\n", counts[fail], counts[warn], counts[info], counts[pass])
	area := ""
	for _, f := range r.Findings {
		if f.Level == pass && !showPass && !strings.HasSuffix(f.Check, "Lighthouse scores") {
			continue
		}
		if f.Area != area {
			area = f.Area
			fmt.Fprintf(w, "\n## %s\n", area)
		}
		line := fmt.Sprintf("%-4s  %s", f.Level, f.Check)
		if f.Detail != "" {
			line += ": " + f.Detail
		}
		fmt.Fprintln(w, line)
	}
	if len(r.Files) > 0 {
		fmt.Fprintln(w, "\n## files")
		keys := make([]string, 0, len(r.Files))
		for k := range r.Files {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(w, "%s: %s\n", k, r.Files[k])
		}
	}
}

type options struct {
	psi        bool
	strategy   []string
	links      int
	outDir     string
	timeout    time.Duration
	psiKey     string
	lighthouse string
	search     bool
	crawl      int
	userAgent  string
}

const browserUA = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0 Safari/537.36 sitecheck/" + version

func main() {
	env.Load("sitecheck")
	if len(os.Args) > 2 && os.Args[1] == "auth" && os.Args[2] == "google" {
		if err := authGoogle(context.Background(), os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, "sitecheck:", err)
			os.Exit(1)
		}
		return
	}
	var o options
	asJSON := flag.Bool("json", false, "print the report as JSON")
	showPass := flag.Bool("pass", false, "also list checks that passed")
	noPSI := flag.Bool("no-psi", false, "skip PageSpeed Insights")
	strategy := flag.String("strategy", "mobile,desktop", "PageSpeed strategies: mobile, desktop or both")
	flag.StringVar(&o.lighthouse, "lighthouse", "auto", "local Lighthouse: auto (if PageSpeed fails), always (skip PageSpeed) or off")
	noSearch := flag.Bool("no-search", false, "skip Google Search Console and Bing Webmaster data")
	flag.IntVar(&o.crawl, "crawl", 0, "also crawl up to N pages of the site (sitemap first) with advertools and run cross-page checks")
	flag.IntVar(&o.links, "links", 40, "how many links on the page to check (0 skips)")
	flag.StringVar(&o.outDir, "out", "", "folder to save the share image in (default: a new temp folder)")
	flag.DurationVar(&o.timeout, "timeout", 20*time.Second, "timeout per HTTP request (PageSpeed gets 2 minutes)")
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: sitecheck [flags] URL\n       sitecheck auth google   (one-time Search Console sign-in)")
		flag.PrintDefaults()
	}
	flag.Parse()
	if flag.NArg() != 1 {
		flag.Usage()
		os.Exit(2)
	}
	o.psi = !*noPSI
	o.search = !*noSearch
	o.strategy = splitList(*strategy)
	o.psiKey = os.Getenv("PAGESPEED_API_KEY")
	o.userAgent = browserUA

	target, err := normalizeURL(flag.Arg(0))
	if err != nil {
		fmt.Fprintln(os.Stderr, "sitecheck:", err)
		os.Exit(2)
	}
	if o.outDir == "" {
		o.outDir, _ = os.MkdirTemp("", "sitecheck-")
	}

	r, err := run(context.Background(), target, o)
	if err != nil {
		fmt.Fprintln(os.Stderr, "sitecheck:", err)
		os.Exit(1)
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(r)
		return
	}
	r.writeText(os.Stdout, *showPass)
}

func splitList(s string) []string {
	var out []string
	for _, f := range strings.Split(s, ",") {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}

func normalizeURL(s string) (*url.URL, error) {
	s = strings.TrimSpace(s)
	if !strings.Contains(s, "://") {
		s = "https://" + s
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, fmt.Errorf("not a web URL: %q", s)
	}
	if u.Path == "" {
		u.Path = "/"
	}
	return u, nil
}

// run fetches the page once, then runs every check area in parallel.
func run(ctx context.Context, target *url.URL, o options) (*report, error) {
	r := &report{URL: target.String()}
	f := newFetcher(o.timeout, o.userAgent)
	pg, err := f.get(ctx, target.String(), o.userAgent)
	if err != nil {
		return nil, fmt.Errorf("could not fetch %s: %v", target, err)
	}
	r.Final = pg.final.String()
	d := parseDoc(pg.body)

	var wg sync.WaitGroup
	do := func(fn func()) {
		wg.Add(1)
		go func() { defer wg.Done(); fn() }()
	}
	do(func() { checkBasics(ctx, r, f, pg, d) })
	do(func() { checkSEO(ctx, r, f, pg, d) })
	do(func() { checkSocial(ctx, r, f, pg, o.outDir) })
	do(func() { checkContent(ctx, r, pg, d, o.outDir) })
	do(func() { checkAEO(ctx, r, f, pg, d) })
	do(func() { checkSecurity(r, pg, d) })
	if o.search {
		do(func() { checkSearch(ctx, r, pg.final, time.Now()) })
	}
	if o.crawl > 0 {
		do(func() { checkSite(ctx, r, f, pg, o.crawl, o.outDir) })
	}
	if o.links > 0 {
		do(func() { checkLinks(ctx, r, f, pg, d, o.links) })
	}
	if o.psi {
		for i, s := range o.strategy {
			s, first := s, i == 0
			do(func() { checkPerformance(ctx, r, pg.final.String(), s, o.psiKey, o.lighthouse, first) })
		}
	}
	wg.Wait()
	r.sort()
	return r, nil
}
