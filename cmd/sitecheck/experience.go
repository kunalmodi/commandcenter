package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// Signs of who made a page and whether it shows first-hand work. Google's
// guidance asks "who created this, and how?", and SEO analyses of 2026's core
// updates found pages with first-hand experience gaining. None of this is a
// confirmed ranking factor, so findings stay INFO or a mild WARN.

var (
	authorPathRe = regexp.MustCompile(`(?i)/(author|authors|writer|writers|people|team|staff|contributor|contributors|profile|profiles|experts?)/[^/?#]+`)
	// "By Jane Doe" with capitalised names; up to four words.
	byRe       = regexp.MustCompile(`\b(?:By|BY|by|Written by|Reviewed by|Words by)\s+((?:Dr\.?\s+)?[A-Z][\p{L}'.-]+(?:\s+[A-Z][\p{L}'.-]+){0,3})`)
	byStopword = map[string]bool{"Clicking": true, "Using": true, "Continuing": true, "Signing": true, "The": true, "This": true, "Category": true, "Date": true}
	photoHint  = regexp.MustCompile(`(?i)avatar|author|headshot|portrait|profile|gravatar|bio|team`)
)

func sameSite(a, b *url.URL) bool {
	return strings.TrimPrefix(strings.ToLower(a.Hostname()), "www.") == strings.TrimPrefix(strings.ToLower(b.Hostname()), "www.")
}

type authorRef struct{ name, url string }

func ldAuthors(v any) []authorRef {
	var out []authorRef
	switch x := v.(type) {
	case string:
		out = append(out, authorRef{name: x})
	case map[string]any:
		a := authorRef{}
		a.name, _ = x["name"].(string)
		a.url, _ = x["url"].(string)
		if a.url == "" {
			a.url, _ = x["@id"].(string)
			if !strings.HasPrefix(a.url, "http") || strings.Contains(a.url, "#") {
				a.url = ""
			}
		}
		out = append(out, a)
	case []any:
		for _, e := range x {
			out = append(out, ldAuthors(e)...)
		}
	}
	return out
}

// wordOffset is how many words of text come before byte index i.
func wordOffset(text string, i int) int { return len(wordRe.FindAllString(text[:i], -1)) }

func checkByline(ctx context.Context, r *report, f *fetcher, pg *page, d *doc, art map[string]any) {
	const a = "aeo"
	ogType, _ := d.meta("og:type")
	if art == nil && !strings.EqualFold(ogType, "article") {
		return // bylines belong on articles, guides and reviews, not product or home pages
	}
	text := visibleText(pg.body)
	lower := strings.ToLower(text)

	var names []string
	var authorURL string
	if art != nil {
		for _, au := range ldAuthors(art["author"]) {
			if au.name != "" {
				names = append(names, au.name)
			}
			if authorURL == "" {
				authorURL = au.url
			}
		}
	}
	if m, ok := d.meta("author"); ok && m != "" {
		names = append(names, m)
	}
	for _, t := range append(d.all("a"), d.links("author")...) {
		href := t.attr("href")
		if href == "" || authorURL != "" {
			continue
		}
		u, err := pg.final.Parse(href)
		if err != nil {
			continue
		}
		// rel=author anywhere; an /author/-style path only on this site (an
		// off-site /profile/ link is a social account, not an author page).
		if strings.Contains(" "+strings.ToLower(t.attr("rel"))+" ", " author ") || (authorPathRe.MatchString(u.Path) && sameSite(u, pg.final)) {
			authorURL = href
		}
	}

	// Where on the page is the byline?
	shown, at := "", -1
	for _, n := range names {
		if i := strings.Index(lower, strings.ToLower(n)); i >= 0 && len(strings.Fields(n)) >= 1 {
			shown, at = n, wordOffset(text, i)
			break
		}
	}
	if shown == "" {
		for _, m := range byRe.FindAllStringSubmatchIndex(text, -1) {
			name := text[m[2]:m[3]]
			if byStopword[strings.Fields(name)[0]] {
				continue
			}
			shown, at = name, wordOffset(text, m[0])
			break
		}
	}
	top := 0
	if h, ok := d.first("h1"); ok && h.text != "" {
		if i := strings.Index(lower, strings.ToLower(cleanText(h.text))); i >= 0 {
			top = wordOffset(text, i)
		}
	}

	switch {
	case shown == "" && len(names) > 0:
		r.add(a, warn, "Byline", "the markup names %s but the page doesn't show it; put a visible byline by the headline (readers and Google's raters look for one)", names[0])
	case shown == "":
		r.add(a, warn, "Byline", "no visible author on an article-type page; add a byline with the writer's name, linked to a page about them")
	case at > top+150:
		r.add(a, info, "Byline", "%q appears %d words below the headline; move it next to the title", shown, at-top)
	default:
		r.add(a, pass, "Byline", "%q shown near the headline", shown)
	}

	if authorURL == "" {
		if shown != "" || len(names) > 0 {
			r.add(a, info, "Author page", "the byline doesn't link to an author page in the server HTML (crawlers don't see links added by JavaScript); a page with a photo, bio and credentials shows who stands behind the content")
		}
		return
	}
	u, err := pg.final.Parse(authorURL)
	if err != nil {
		return
	}
	checkAuthorPage(ctx, r, f, u, append(names, shown))
}

func checkAuthorPage(ctx context.Context, r *report, f *fetcher, u *url.URL, names []string) {
	const a = "aeo"
	ap, err := f.get(ctx, u.String(), "")
	if err != nil || ap.status != 200 {
		status := "no response"
		if err == nil {
			status = fmt.Sprintf("HTTP %d", ap.status)
		}
		r.add(a, warn, "Author page", "%s returns %s", u, status)
		return
	}
	ad := parseDoc(ap.body)
	var nameParts []string
	for _, n := range names {
		for _, w := range strings.Fields(strings.ToLower(n)) {
			if len(w) > 2 && w != "dr." {
				nameParts = append(nameParts, w)
			}
		}
	}
	photo := false
	for _, img := range ad.all("img") {
		blob := strings.ToLower(img.attr("alt") + " " + img.attr("src") + " " + img.attr("class"))
		if photoHint.MatchString(blob) {
			photo = true
		}
		for _, p := range nameParts {
			if strings.Contains(blob, p) {
				photo = true
			}
		}
	}
	person, sameAs := false, false
	for _, s := range ad.all("script") {
		if !strings.EqualFold(strings.TrimSpace(s.attr("type")), "application/ld+json") {
			continue
		}
		var v any
		if json.Unmarshal([]byte(strings.TrimSpace(s.text)), &v) != nil {
			continue
		}
		for _, o := range ldObjects(v) {
			if ldHasType(o, "Person") {
				person = true
				sameAs = sameAs || o["sameAs"] != nil
				if o["image"] != nil {
					photo = true
				}
			}
		}
	}
	var have, missing []string
	add := func(ok bool, what string) {
		if ok {
			have = append(have, what)
		} else {
			missing = append(missing, what)
		}
	}
	add(photo, "a photo")
	add(person, "Person markup")
	add(sameAs, "sameAs links to their profiles")
	if !photo {
		r.add(a, info, "Author page", "%s has no author photo that sitecheck can spot; a real photo, short bio and credentials make the writer checkable. Has: %s", u, orNone(have))
		return
	}
	if len(missing) > 0 {
		r.add(a, info, "Author page", "%s has %s; could add %s", u, strings.Join(have, ", "), strings.Join(missing, ", "))
		return
	}
	r.add(a, pass, "Author page", "%s has %s", u, strings.Join(have, ", "))
}

func orNone(s []string) string {
	if len(s) == 0 {
		return "none of photo, Person markup or sameAs"
	}
	return strings.Join(s, ", ")
}

// Stock photo hosts and the file-name patterns their downloads keep.
var (
	stockHosts = []string{"unsplash.com", "pexels.com", "pixabay.com", "shutterstock.com", "istockphoto.com", "gettyimages.com",
		"ftcdn.net", "stock.adobe.com", "freepik.com", "depositphotos.com", "dreamstime.com", "123rf.com", "stocksnap.io", "alamy.com"}
	stockNameRe = regexp.MustCompile(`(?i)(shutterstock_\d|istockphoto-\d|gettyimages-\d|adobestock_\d|depositphotos_\d|dreamstime_|pexels-photo|pexels-[a-z-]+-\d{5,}|unsplash|freepik|123rf|alamy-)`)
)

func stockSource(src string) string {
	u, err := url.Parse(src)
	if err != nil {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	for _, h := range stockHosts {
		if host == h || strings.HasSuffix(host, "."+h) {
			return h
		}
	}
	// Downloads re-hosted on the site's own CDN keep the stock file name.
	// Image CDNs often put the original URL in the query, so check it too.
	if m := stockNameRe.FindString(u.Path + " " + u.RawQuery); m != "" {
		return "file name " + strings.TrimRight(m, "_-0123456789")
	}
	return ""
}

func checkStockImages(r *report, pg *page, d *doc) {
	const a = "aeo"
	content, stock := 0, 0
	var ex []string
	consider := func(src string) {
		if src == "" || strings.HasPrefix(src, "data:") || strings.HasSuffix(strings.ToLower(strings.SplitN(src, "?", 2)[0]), ".svg") {
			return
		}
		content++
		if s := stockSource(src); s != "" {
			stock++
			if len(ex) < 3 && !contains(ex, s) {
				ex = append(ex, s)
			}
		}
	}
	for _, img := range d.all("img") {
		if w := atoiOr(img.attr("width"), 0); w > 0 && w < 100 {
			continue // icons and logos
		}
		src := img.attr("src")
		if src == "" || strings.HasPrefix(src, "data:") {
			src = img.attr("data-src")
		}
		if abs, err := pg.final.Parse(src); err == nil && src != "" {
			src = abs.String()
		}
		consider(src)
	}
	if og, ok := d.meta("og:image"); ok {
		consider(og)
	}
	switch {
	case stock == 0:
		return
	case stock >= 2 && stock*2 >= content:
		r.add(a, warn, "Stock images", "%d of %d images are stock photos (%s); original photos of the product, place or work are one of the few visible signs of first-hand experience", stock, content, strings.Join(ex, ", "))
	default:
		r.add(a, info, "Stock images", "%d of %d images look like stock photos (%s)", stock, content, strings.Join(ex, ", "))
	}
}
