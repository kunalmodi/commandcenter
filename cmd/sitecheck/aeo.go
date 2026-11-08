package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
)

// AEO (answer engine optimisation, also called GEO): can AI assistants and
// AI search (ChatGPT, Claude, Perplexity, Google AI Overviews, Copilot) reach,
// read and quote this page? Only some of that is checkable. These are the
// parts that are mechanical:
//
//  1. Access: robots.txt rules and bot protection for each AI crawler. The
//     crawlers that fetch pages to answer questions matter most; blocking the
//     ones that only collect training data is a legitimate choice.
//  2. Readability without JavaScript: most AI crawlers don't run it.
//  3. Machine-readable facts: structured data, authorship, dates.
//  4. llms.txt and Content-Signal (newer conventions; reported, not required).
//
// Whether the content actually answers questions well is judged by reading
// content.md (see the site-check skill), not by rules.

type aiBot struct {
	token   string // robots.txt user-agent token (lowercase)
	name    string // display name
	vendor  string
	answers bool   // fetches pages to answer or cite in AI search; false = training only
	ua      string // user agent for a live fetch; "" = robots.txt check only
}

// From each vendor's crawler documentation. Googlebot and Bingbot are listed
// because Google AI Overviews and Copilot (and ChatGPT search, partly) are
// built on those indexes.
var aiBots = []aiBot{
	{"oai-searchbot", "OAI-SearchBot", "OpenAI (ChatGPT search)", true, "Mozilla/5.0 AppleWebKit/537.36 (KHTML, like Gecko; compatible; OAI-SearchBot/1.0; +https://openai.com/searchbot)"},
	{"chatgpt-user", "ChatGPT-User", "OpenAI (ChatGPT browsing)", true, "Mozilla/5.0 AppleWebKit/537.36 (KHTML, like Gecko; compatible; ChatGPT-User/1.0; +https://openai.com/bot)"},
	{"claude-searchbot", "Claude-SearchBot", "Anthropic (Claude search)", true, ""},
	{"claude-user", "Claude-User", "Anthropic (Claude fetching for a user)", true, "Mozilla/5.0 AppleWebKit/537.36 (KHTML, like Gecko; compatible; Claude-User/1.0; +Claude-User@anthropic.com)"},
	{"perplexitybot", "PerplexityBot", "Perplexity", true, "Mozilla/5.0 AppleWebKit/537.36 (KHTML, like Gecko; compatible; PerplexityBot/1.0; +https://perplexity.ai/perplexitybot)"},
	{"perplexity-user", "Perplexity-User", "Perplexity (fetching for a user)", true, ""},
	{"googlebot", "Googlebot", "Google (Search and AI Overviews)", true, ""},
	{"bingbot", "Bingbot", "Microsoft (Bing, Copilot)", true, ""},
	{"duckassistbot", "DuckAssistBot", "DuckDuckGo (DuckAssist)", true, ""},
	{"applebot", "Applebot", "Apple (Siri, Spotlight)", true, ""},
	{"gptbot", "GPTBot", "OpenAI (training)", false, "Mozilla/5.0 AppleWebKit/537.36 (KHTML, like Gecko; compatible; GPTBot/1.1; +https://openai.com/gptbot)"},
	{"claudebot", "ClaudeBot", "Anthropic (training)", false, "Mozilla/5.0 AppleWebKit/537.36 (KHTML, like Gecko; compatible; ClaudeBot/1.0; +claudebot@anthropic.com)"},
	{"google-extended", "Google-Extended", "Google (Gemini training; doesn't affect Search or AI Overviews)", false, ""},
	{"applebot-extended", "Applebot-Extended", "Apple (training)", false, ""},
	{"ccbot", "CCBot", "Common Crawl (used by many AI models)", false, ""},
	{"meta-externalagent", "meta-externalagent", "Meta (training)", false, ""},
	{"bytespider", "Bytespider", "ByteDance (training)", false, ""},
	{"amazonbot", "Amazonbot", "Amazon", false, ""},
}

func checkAEO(ctx context.Context, r *report, f *fetcher, pg *page, d *doc) {
	const a = "aeo"
	path := pg.final.EscapedPath()
	if pg.final.RawQuery != "" {
		path += "?" + pg.final.RawQuery
	}

	// 1. robots.txt
	var robotsBody string
	if p, err := f.get(ctx, pg.final.ResolveReference(&url.URL{Path: "/robots.txt"}).String(), ""); err == nil && p.status == 200 {
		robotsBody = p.body
	}
	groups, _ := parseRobots(robotsBody)
	var blockedAnswer, blockedTrain []string
	for _, b := range aiBots {
		if !robotsAllowed(groups, b.token, path) {
			if b.answers {
				blockedAnswer = append(blockedAnswer, b.name+" ("+b.vendor+")")
			} else {
				blockedTrain = append(blockedTrain, b.name)
			}
		}
	}
	if len(blockedAnswer) > 0 {
		r.add(a, fail, "AI search crawlers blocked", "robots.txt blocks %s from this page, so those assistants can't read or cite it", strings.Join(blockedAnswer, ", "))
	} else {
		r.add(a, pass, "AI search crawlers", "robots.txt lets the answer and AI-search crawlers in (OpenAI, Anthropic, Perplexity, Google, Bing)")
	}
	if len(blockedTrain) > 0 {
		r.add(a, info, "AI training crawlers", "robots.txt blocks %s. That's a choice about training data and doesn't stop AI search from citing you", strings.Join(blockedTrain, ", "))
	}
	if sig := contentSignals(robotsBody); sig != "" {
		r.add(a, info, "Content-Signal", "robots.txt declares %s (Cloudflare's convention for search / ai-input / ai-train preferences)", sig)
	}

	// If the site turned our own browser-like request away, comparisons with
	// AI crawlers and the page text mean nothing.
	if pg.status != 200 || isChallenge(pg.body) {
		r.add(a, info, "AI crawler comparison", "skipped: the site answered our own request with HTTP %d or a bot check, so there's nothing to compare against", pg.status)
		checkLLMSTxt(ctx, r, f, pg)
		return
	}

	// 2. Bot protection: fetch as the AI crawlers that have known user agents.
	checkAIFetch(ctx, r, f, pg, groups, path)

	// 3. Text readable without JavaScript.
	words := len(wordRe.FindAllString(visibleText(pg.body), -1))
	scripts := len(d.all("script"))
	switch {
	case words < 80:
		r.add(a, fail, "Text without JavaScript", "the HTML the server sends has only %d words. Most AI crawlers don't run JavaScript, so to them the page is nearly empty. Render the content on the server (SSR or static generation)", words)
	case words < 200 && scripts > 15:
		r.add(a, warn, "Text without JavaScript", "only %d words in the server HTML alongside %d scripts; check that the main content isn't rendered by JavaScript", words, scripts)
	default:
		r.add(a, pass, "Text without JavaScript", "%d words in the server HTML", words)
	}

	// 4. Structured facts: who wrote it, when, what entity this is.
	art := checkAEOSchema(r, d, pg)

	// Who wrote it, visibly, and whether the images are the site's own.
	checkByline(ctx, r, f, pg, d, art)
	checkStockImages(r, pg, d)

	// 5. Answer-shaped structure (judgment of quality is left to the reader).
	checkAnswerShape(r, d)
	checkEvidence(r, pg, d, words)

	// 6. llms.txt
	checkLLMSTxt(ctx, r, f, pg)
}

var contentSignalRe = regexp.MustCompile(`(?im)^\s*content-signal\s*:\s*(.+)$`)

func contentSignals(robots string) string {
	var out []string
	for _, m := range contentSignalRe.FindAllStringSubmatch(robots, -1) {
		out = append(out, strings.TrimSpace(m[1]))
	}
	return strings.Join(dedupeStrings(out), "; ")
}

var challengeRe = regexp.MustCompile(`(?i)just a moment\.\.\.|cf-chl|challenge-platform|attention required|access denied|captcha|verify you are human|vercel security checkpoint`)

// isChallenge spots bot-check pages. They are small, so a match deep inside a
// big real page (a reCAPTCHA key for a newsletter form) doesn't count unless
// it's in the title.
func isChallenge(body string) bool {
	if len(body) < 64<<10 {
		return challengeRe.MatchString(body)
	}
	t, _ := parseDoc(body[:min(len(body), 16<<10)]).title()
	return challengeRe.MatchString(t)
}

// checkAIFetch requests the page with AI crawler user agents and compares
// with what a browser gets. Real crawlers come from published IP ranges, so a
// firewall may treat them differently from us in either direction; a block
// here is a strong hint to check the bot settings, not proof.
func checkAIFetch(ctx context.Context, r *report, f *fetcher, pg *page, groups []robotsGroup, path string) {
	const a = "aeo"
	type res struct {
		bot    aiBot
		status int
		why    string
	}
	var bots []aiBot
	for _, b := range aiBots {
		if b.ua != "" {
			bots = append(bots, b)
		}
	}
	out := make([]res, len(bots))
	done := make(chan int)
	for i, b := range bots {
		go func(i int, b aiBot) {
			defer func() { done <- i }()
			p, err := f.get(ctx, pg.final.String(), b.ua)
			out[i].bot = b
			if err != nil {
				out[i].why = shortErr(err)
				return
			}
			out[i].status = p.status
			switch {
			case p.status == 403 || p.status == 401 || p.status == 429 || p.status >= 500:
				out[i].why = fmt.Sprintf("HTTP %d", p.status)
			case isChallenge(p.body) && !isChallenge(pg.body):
				out[i].why = "got a bot challenge page"
			case p.status == 200 && len(pg.body) > 5000 && len(p.body) < len(pg.body)/4:
				out[i].why = fmt.Sprintf("got a much smaller page (%d vs %d bytes)", len(p.body), len(pg.body))
			}
		}(i, b)
	}
	for range bots {
		<-done
	}
	var surprise, deliberate, blockedTrain []string
	for _, o := range out {
		if o.why == "" {
			continue
		}
		s := o.bot.name + ": " + o.why
		switch {
		case !o.bot.answers:
			blockedTrain = append(blockedTrain, s)
		case robotsAllowed(groups, o.bot.token, path):
			surprise = append(surprise, s) // robots.txt says yes, the server says no
		default:
			deliberate = append(deliberate, s)
		}
	}
	if len(surprise) > 0 {
		r.add(a, fail, "AI crawlers turned away", "robots.txt allows them, but requests with their user agents are refused (%s). That's usually bot protection (Cloudflare's AI bot blocking, a Vercel or WAF rule) and probably unintended; check the firewall's bot settings", strings.Join(surprise, "; "))
	}
	if len(deliberate) > 0 {
		r.add(a, info, "AI crawlers turned away", "also refused at the server, matching robots.txt (deliberate): %s", strings.Join(deliberate, "; "))
	}
	if len(surprise) > 0 || len(deliberate) > 0 {
		// reported above
	} else {
		r.add(a, pass, "AI crawlers turned away", "none: OpenAI, Anthropic and Perplexity user agents get the same page as a browser")
	}
	if len(blockedTrain) > 0 {
		r.add(a, info, "AI training crawlers turned away", "%s (a choice; doesn't affect AI search)", strings.Join(blockedTrain, "; "))
	}
}

var (
	scriptStyleRe = regexp.MustCompile(`(?is)<(script|style|noscript|template|svg)\b.*?</(script|style|noscript|template|svg)>`)
	bodyRe        = regexp.MustCompile(`(?is)<body\b[^>]*>(.*)</body>`)
)

// visibleText is the text a non-JS crawler sees in the body.
func visibleText(html string) string {
	if m := bodyRe.FindStringSubmatch(html); m != nil {
		html = m[1]
	}
	html = scriptStyleRe.ReplaceAllString(html, " ")
	return cleanText(stripTags(html))
}

// ldObjects flattens JSON-LD (arrays, @graph) into its objects.
func ldObjects(v any) []map[string]any {
	var out []map[string]any
	switch x := v.(type) {
	case []any:
		for _, e := range x {
			out = append(out, ldObjects(e)...)
		}
	case map[string]any:
		out = append(out, x)
		if g, ok := x["@graph"]; ok {
			out = append(out, ldObjects(g)...)
		}
	}
	return out
}

// ldHasType checks the object's own @type (not types nested in @graph).
func ldHasType(o map[string]any, types ...string) bool {
	var own []string
	switch t := o["@type"].(type) {
	case string:
		own = []string{t}
	case []any:
		for _, e := range t {
			if s, ok := e.(string); ok {
				own = append(own, s)
			}
		}
	}
	for _, t := range own {
		for _, want := range types {
			if strings.EqualFold(t, want) {
				return true
			}
		}
	}
	return false
}

var articleTypes = []string{"Article", "BlogPosting", "NewsArticle", "TechArticle", "MedicalWebPage", "HowTo", "Recipe", "Review"}

// checkAEOSchema returns the page's article-like JSON-LD object, if any.
func checkAEOSchema(r *report, d *doc, pg *page) map[string]any {
	const a = "aeo"
	var objs []map[string]any
	for _, s := range d.all("script") {
		if !strings.EqualFold(strings.TrimSpace(s.attr("type")), "application/ld+json") {
			continue
		}
		var v any
		if json.Unmarshal([]byte(strings.TrimSpace(s.text)), &v) == nil {
			objs = append(objs, ldObjects(v)...)
		}
	}

	// Entity: does the site say who it is, with links that tie it to known profiles?
	var org map[string]any
	for _, o := range objs {
		if ldHasType(o, "Organization", "Corporation", "LocalBusiness", "Person", "OnlineStore", "NewsMediaOrganization") {
			org = o
			break
		}
	}
	switch {
	case org == nil:
		r.add(a, info, "Entity markup", "no Organization or Person JSON-LD. Cheap to add and helps search engines tie the site to its brand, though Google says no special markup is needed for its AI features")
	case org["sameAs"] == nil:
		r.add(a, info, "Entity markup", "Organization JSON-LD has no sameAs; listing the brand's profiles (LinkedIn, X, Wikipedia or Wikidata) helps engines connect mentions of the brand to this site")
	default:
		r.add(a, pass, "Entity markup", "Organization JSON-LD with sameAs links")
	}

	// Authorship and dates on article-like pages.
	var art map[string]any
	for _, o := range objs {
		if ldHasType(o, articleTypes...) {
			art = o
			break
		}
	}
	modified := ""
	if art != nil {
		if art["author"] == nil {
			r.add(a, warn, "Author", "the article markup has no author; AI answers and Google weigh who wrote it, especially on health, money and safety topics")
		}
		for _, k := range []string{"dateModified", "datePublished"} {
			if s, ok := art[k].(string); ok && s != "" {
				modified = s
				break
			}
		}
	}
	if modified == "" {
		for _, k := range []string{"article:modified_time", "article:published_time", "og:updated_time"} {
			if v, ok := d.meta(k); ok && v != "" {
				modified = v
				break
			}
		}
	}
	if modified != "" {
		if t, ok := parseLooseDate(modified); ok {
			age := time.Since(t)
			r.fact("content_modified", t.Format("2006-01-02"))
			if age > 540*24*time.Hour && art != nil {
				r.add(a, warn, "Freshness", "last updated %s (%d months ago); AI search favours recent sources, so refresh it and update dateModified", t.Format("2006-01-02"), int(age.Hours()/24/30))
			} else {
				r.add(a, pass, "Freshness", "updated %s", t.Format("2006-01-02"))
			}
		}
	} else if art != nil {
		r.add(a, info, "Freshness", "no dateModified or datePublished; dates help assistants judge how current an answer is")
	}

	var types []string
	for _, o := range objs {
		types = append(types, ldTypes(o)...)
	}
	sort.Strings(types)
	types = dedupe(types)
	answerTypes := []string{}
	for _, t := range types {
		switch t {
		case "FAQPage", "QAPage", "HowTo", "Product", "Recipe", "Review", "AggregateRating", "ItemList", "DefinedTerm":
			answerTypes = append(answerTypes, t)
		}
	}
	if len(answerTypes) > 0 {
		r.add(a, info, "Answer-friendly markup", "%s", strings.Join(answerTypes, ", "))
	}
	return art
}

func parseLooseDate(s string) (time.Time, bool) {
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05Z0700", "2006-01-02T15:04:05", "2006-01-02"} {
		if t, err := time.Parse(layout, strings.TrimSpace(s)); err == nil {
			return t, true
		}
	}
	if len(s) >= 10 {
		if t, err := time.Parse("2006-01-02", s[:10]); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

var questionStart = regexp.MustCompile(`(?i)^(what|how|why|when|where|which|who|can|does|do|is|are|should|will|could)\b`)

// checkAnswerShape counts the structures assistants quote easily: headings
// phrased as questions, lists and tables.
func checkAnswerShape(r *report, d *doc) {
	const a = "aeo"
	q := 0
	var examples []string
	for _, h := range headings(d) {
		if h.name == "h1" {
			continue
		}
		t := strings.TrimSpace(h.text)
		if strings.HasSuffix(t, "?") || questionStart.MatchString(t) {
			q++
			if len(examples) < 3 {
				examples = append(examples, fmt.Sprintf("%q", truncate(t, 60)))
			}
		}
	}
	lists := len(d.all("ul")) + len(d.all("ol"))
	tables := len(d.all("table"))
	detail := fmt.Sprintf("%d question-style subheadings, %d lists, %d tables", q, lists, tables)
	if len(examples) > 0 {
		detail += "; e.g. " + strings.Join(examples, ", ")
	}
	r.add(a, info, "Answer-shaped structure", "%s. Assistants quote passages that answer a question directly under a heading that asks it", detail)
}

var (
	statRe  = regexp.MustCompile(`\b\d[\d,.]*\s?(%|percent|mg|g|kg|ml|oz|lb|mmol|kcal|calories|hours?|days?|weeks?|months?|years?|\$|usd|€|£)|[$€£]\s?\d`)
	quoteRe = regexp.MustCompile(`[“"][^”"]{25,300}[”"]`)
)

// checkEvidence counts what the GEO study (Aggarwal et al., KDD 2024) found
// raises visibility in generative answers: statistics, quotations and cited
// sources. A count, not a verdict: the reader judges whether they're real.
func checkEvidence(r *report, pg *page, d *doc, words int) {
	const a = "aeo"
	if words < 150 {
		return
	}
	text := visibleText(pg.body)
	stats := len(statRe.FindAllString(strings.ToLower(text), -1))
	quotes := len(quoteRe.FindAllString(text, -1)) + len(d.all("blockquote"))
	sources := map[string]bool{}
	for _, t := range d.all("a") {
		u, err := pg.final.Parse(strings.TrimSpace(t.attr("href")))
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || strings.EqualFold(u.Hostname(), pg.final.Hostname()) {
			continue
		}
		h := strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
		if socialHosts[h] {
			continue
		}
		sources[h] = true
	}
	r.fact("evidence", map[string]int{"statistics": stats, "quotes": quotes, "source_domains": len(sources)})
	detail := fmt.Sprintf("%d figures with units, %d quotations, links to %d outside sources", stats, quotes, len(sources))
	if stats == 0 && len(sources) == 0 && words > 300 {
		r.add(a, warn, "Evidence", "%s. In the GEO study, adding statistics, quotations and cited sources raised visibility in AI answers by up to ~40%%; this page has none of the first and last", detail)
		return
	}
	r.add(a, info, "Evidence", "%s (statistics, quotations and cited sources are what raised visibility in AI answers in the GEO study)", detail)
}

var socialHosts = map[string]bool{
	"facebook.com": true, "x.com": true, "twitter.com": true, "instagram.com": true, "linkedin.com": true,
	"youtube.com": true, "tiktok.com": true, "pinterest.com": true, "threads.net": true, "github.com": true,
}

func checkLLMSTxt(ctx context.Context, r *report, f *fetcher, pg *page) {
	const a = "aeo"
	u := pg.final.ResolveReference(&url.URL{Path: "/llms.txt"})
	p, err := f.get(ctx, u.String(), "")
	if err != nil {
		return
	}
	ct := strings.ToLower(p.header.Get("Content-Type"))
	looksRight := p.status == 200 && !strings.Contains(ct, "html") && strings.HasPrefix(strings.TrimSpace(p.body), "#")
	switch {
	case looksRight:
		links := strings.Count(p.body, "](")
		r.add(a, info, "llms.txt", "present (%d links). Low value: in Ahrefs' 2026 data 97%% of llms.txt files got no requests, no study has found a citation effect, and Google says it doesn't use them (Perplexity reportedly reads them)", links)
	case p.status == 200:
		r.add(a, info, "llms.txt", "%s answers 200 but isn't a Markdown llms.txt (probably the site's HTML or a soft 404)", u.Path)
	default:
		r.add(a, info, "llms.txt", "none. Not worth prioritising: no measured citation benefit, and Google says it doesn't use them")
	}
}
