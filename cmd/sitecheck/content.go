package main

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Content: what the page says, as opposed to how it's tagged. trafilatura
// extracts the main text (it's the standard open-source tool for this); the
// heading outline comes from our own parse. Both go to content.md so the
// reader of the report (the site-check skill) can judge topic, search intent
// and coverage, which rules can't. The rules here only catch the mechanical
// problems: thin text, missing subheadings, title and h1 disagreeing, link
// text that says nothing.

var wordRe = regexp.MustCompile(`[\p{L}\p{N}][\p{L}\p{N}'’-]*`)

type extracted struct {
	meta map[string]string // trafilatura front matter: title, author, date, sitename, ...
	text string            // main text as Markdown
}

// extractMain runs trafilatura on HTML we already fetched.
func extractMain(ctx context.Context, body string) (*extracted, error) {
	out, err := runTool(ctx, "uvx", []string{trafilaturaPin, "--markdown", "--with-metadata"}, strings.NewReader(body), 90*time.Second)
	if err != nil {
		return nil, err
	}
	return parseFrontMatter(out), nil
}

// parseFrontMatter splits trafilatura's "---\nkey: value\n---\nbody" output.
func parseFrontMatter(s string) *extracted {
	e := &extracted{meta: map[string]string{}}
	s = strings.TrimLeft(s, "\n")
	if rest, ok := strings.CutPrefix(s, "---\n"); ok {
		if i := strings.Index(rest, "\n---\n"); i >= 0 {
			for _, ln := range strings.Split(rest[:i], "\n") {
				if k, v, ok := strings.Cut(ln, ":"); ok {
					e.meta[strings.TrimSpace(k)] = strings.Trim(strings.TrimSpace(v), `"'`)
				}
			}
			s = rest[i+5:]
		}
	}
	e.text = strings.TrimSpace(s)
	return e
}

func checkContent(ctx context.Context, r *report, pg *page, d *doc, outDir string) {
	const a = "content"
	ex, err := extractMain(ctx, pg.body)
	if err != nil {
		r.add(a, warn, "Main text", "trafilatura failed, so content wasn't analysed: %v", err)
		ex = &extracted{meta: map[string]string{}}
	}
	words := len(wordRe.FindAllString(ex.text, -1))
	r.fact("word_count", words)
	if v := ex.meta["date"]; v != "" {
		r.fact("content_date", v)
	}
	if v := ex.meta["author"]; v != "" {
		r.fact("content_author", v)
	}

	home := pg.final.Path == "/" || pg.final.Path == ""
	if err == nil {
		switch {
		case words < 150 && !home:
			r.add(a, warn, "Amount of content", "%d words of main text; pages this thin rarely rank unless they're tools or listings", words)
		case words < 80 && home:
			r.add(a, warn, "Amount of content", "%d words of main text on the home page; search engines have little to go on (is the text rendered by JavaScript?)", words)
		case words < 300 && !home:
			r.add(a, info, "Amount of content", "%d words of main text (fine for a product or tool page; thin for an article)", words)
		default:
			r.add(a, info, "Amount of content", "%d words of main text", words)
		}
	}

	checkHeadings(r, d, words)
	checkTitleH1(r, d)
	checkLinkText(r, pg, d)

	if outDir != "" {
		path := filepath.Join(outDir, "content.md")
		if err := os.WriteFile(path, []byte(contentFile(pg, d, ex, words)), 0o644); err == nil {
			r.file("content", path)
		}
	}
}

func headings(d *doc) []tag {
	var hs []tag
	for _, t := range d.tags {
		if len(t.name) == 2 && t.name[0] == 'h' && t.name[1] >= '1' && t.name[1] <= '6' && t.text != "" {
			hs = append(hs, t)
		}
	}
	return hs
}

func checkHeadings(r *report, d *doc, words int) {
	const a = "content"
	hs := headings(d)
	var outline, skipped []string
	prev, h2 := 0, 0
	for _, h := range hs {
		lvl := int(h.name[1] - '0')
		if lvl == 2 {
			h2++
		}
		if prev > 0 && lvl > prev+1 {
			skipped = append(skipped, fmt.Sprintf("h%d→h%d before %q", prev, lvl, truncate(h.text, 40)))
		}
		prev = lvl
		if lvl <= 3 && len(outline) < 20 {
			outline = append(outline, "H"+string(h.name[1])+" "+truncate(h.text, 60))
		}
	}
	if len(outline) > 0 {
		r.add(a, info, "Outline", "%d headings: %s", len(hs), strings.Join(outline, " / "))
	}
	if h2 == 0 && words > 600 {
		r.add(a, warn, "Subheadings", "%d words with no h2; break it up so readers and Google can see the subtopics", words)
	}
	if len(skipped) > 0 {
		r.add(a, info, "Heading levels", "skipped levels: %s", strings.Join(skipped[:min(len(skipped), 3)], "; "))
	}
}

var titleStop = map[string]bool{"the": true, "and": true, "for": true, "your": true, "with": true, "you": true, "our": true, "from": true, "how": true, "what": true, "are": true}

func significant(s string) map[string]bool {
	out := map[string]bool{}
	for _, t := range wordRe.FindAllString(strings.ToLower(s), -1) {
		if len([]rune(t)) >= 3 && !titleStop[t] {
			out[t] = true
		}
	}
	return out
}

// titleCore drops a trailing " | Brand" or " - Brand".
func titleCore(t string) string {
	for _, sep := range []string{" | ", " – ", " — ", " - ", " · "} {
		if i := strings.LastIndex(t, sep); i > 0 {
			return t[:i]
		}
	}
	return t
}

func checkTitleH1(r *report, d *doc) {
	title, _ := d.title()
	core := titleCore(title)
	h, ok := d.first("h1")
	if core == "" || !ok || h.text == "" {
		return
	}
	tw, hw := significant(core), significant(h.text)
	for w := range tw {
		if hw[w] {
			return
		}
	}
	r.add("content", warn, "Title vs h1", "they share no words (title %q, h1 %q); both should name the page's main topic", core, h.text)
}

// Link text that tells neither readers nor Google where the link goes.
var genericAnchors = map[string]bool{
	"click here": true, "here": true, "read more": true, "learn more": true, "more": true, "link": true,
	"this": true, "details": true, "continue": true, "go": true, "this link": true, "more info": true,
	"see more": true, "see details": true, "see the details": true, "view details": true, "view": true,
	"shop now": true, "buy now": true, "view product": true, "read": true, "info": true, "discover": true,
}

var (
	altRe       = regexp.MustCompile(`(?i)\balt\s*=\s*["']\s*[^"'\s]`)
	ariaLabelRe = regexp.MustCompile(`(?i)\baria-label\s*=\s*["']\s*[^"'\s]`)
)

func checkLinkText(r *report, pg *page, d *doc) {
	const a = "content"
	internal, external, nofollow := 0, 0, 0
	byText := map[string]map[string]bool{} // link text → distinct targets
	var generic, empty []string
	for _, t := range d.all("a") {
		href := strings.TrimSpace(t.attr("href"))
		if href == "" || strings.HasPrefix(href, "#") || strings.HasPrefix(strings.ToLower(href), "javascript:") {
			continue
		}
		u, err := pg.final.Parse(href)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
			continue
		}
		u.Fragment = ""
		isInternal := strings.EqualFold(u.Hostname(), pg.final.Hostname())
		if isInternal {
			internal++
			if strings.Contains(strings.ToLower(t.attr("rel")), "nofollow") {
				nofollow++
			}
		} else {
			external++
		}
		label := strings.TrimSpace(t.text)
		if label == "" {
			label = strings.TrimSpace(t.attr("aria-label"))
		}
		low := strings.ToLower(strings.Trim(label, " .!→»›>"))
		switch {
		case low == "" && !altRe.MatchString(t.inner) && !ariaLabelRe.MatchString(t.inner) && t.attr("title") == "":
			empty = append(empty, shortURL(u))
		case genericAnchors[low]:
			generic = append(generic, fmt.Sprintf("%q → %s", label, shortURL(u)))
		}
		if isInternal && low != "" {
			if byText[low] == nil {
				byText[low] = map[string]bool{}
			}
			byText[low][u.String()] = true
		}
	}
	r.fact("links_internal", internal)
	r.fact("links_external", external)
	r.add(a, info, "Links on the page", "%d internal, %d external", internal, external)
	if nofollow > 0 {
		r.add(a, warn, "Internal nofollow", "%d internal links are rel=nofollow, which tells Google not to follow your own pages", nofollow)
	}
	if len(generic) > 0 {
		r.add(a, warn, "Vague link text", "%d links use text like %s; name the destination instead (e.g. the product or article title), since link text tells Google what the target page is about", len(generic), generic[0])
	}
	// The same text pointing at several different pages is ambiguous.
	var same []string
	for txt, targets := range byText {
		if len(targets) >= 3 && !genericAnchors[txt] {
			same = append(same, fmt.Sprintf("%q → %d pages", txt, len(targets)))
		}
	}
	if len(same) > 0 {
		r.add(a, info, "Repeated link text", "the same text links to different pages: %s", strings.Join(same[:min(len(same), 3)], "; "))
	}
	if len(empty) > 0 {
		e := dedupeStrings(empty)
		r.add(a, warn, "Links with no text", "%d links have no text, image alt or aria-label (screen readers and Google see nothing), e.g. %s", len(empty), strings.Join(e[:min(len(e), 3)], ", "))
	}
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

func shortURL(u *url.URL) string {
	s := u.Path
	if u.RawQuery != "" {
		s += "?" + u.RawQuery
	}
	if s == "" {
		s = "/"
	}
	return truncate(s, 70)
}

func dedupeStrings(s []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, v := range s {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

// contentFile renders the page for a reader to review.
func contentFile(pg *page, d *doc, ex *extracted, words int) string {
	var b strings.Builder
	title, _ := d.title()
	desc, _ := d.meta("description")
	fmt.Fprintf(&b, "# Content of %s\n\n", pg.final)
	fmt.Fprintf(&b, "- Title: %s\n- Meta description: %s\n- Main text: %d words (extracted by trafilatura)\n", title, desc, words)
	for _, k := range []string{"author", "date", "sitename"} {
		if v := ex.meta[k]; v != "" {
			fmt.Fprintf(&b, "- %s: %s\n", k, v)
		}
	}
	b.WriteString("\n## Heading outline\n\n")
	for _, h := range headings(d) {
		lvl := int(h.name[1] - '0')
		fmt.Fprintf(&b, "%s- %s %s\n", strings.Repeat("  ", lvl-1), strings.ToUpper(h.name), h.text)
	}
	b.WriteString("\n## Main text\n\n")
	text := ex.text
	if r := []rune(text); len(r) > 40000 {
		text = string(r[:40000]) + "\n\n[truncated]"
	}
	b.WriteString(text)
	b.WriteString("\n")
	return b.String()
}
