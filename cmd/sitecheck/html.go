package main

import (
	"html"
	"regexp"
	"strings"
)

// A deliberately small HTML scanner: enough to read the tags that matter for
// SEO and share previews (title, meta, link, h1, img, a, script, iframe, html)
// without a third-party parser. It skips comments, and treats script, style,
// title and textarea contents as raw text.

type tag struct {
	name  string
	attrs map[string]string
	text  string // inner text, only for raw-text elements and h1
}

func (t tag) attr(k string) string { return t.attrs[k] }

var rawText = map[string]bool{"script": true, "style": true, "title": true, "textarea": true}

func scanTags(doc string) []tag {
	var out []tag
	lower := strings.ToLower(doc)
	i := 0
	for i < len(doc) {
		lt := strings.IndexByte(doc[i:], '<')
		if lt < 0 {
			break
		}
		i += lt
		if strings.HasPrefix(doc[i:], "<!--") {
			end := strings.Index(doc[i+4:], "-->")
			if end < 0 {
				break
			}
			i += 4 + end + 3
			continue
		}
		j := i + 1
		if j < len(doc) && (doc[j] == '/' || doc[j] == '!' || doc[j] == '?') {
			// closing tag, doctype or processing instruction: skip
			gt := strings.IndexByte(doc[j:], '>')
			if gt < 0 {
				break
			}
			i = j + gt + 1
			continue
		}
		k := j
		for k < len(doc) && isNameChar(doc[k]) {
			k++
		}
		if k == j {
			i++
			continue
		}
		name := lower[j:k]
		attrs, end := parseAttrs(doc, k)
		t := tag{name: name, attrs: attrs}
		i = end
		switch {
		case rawText[name]:
			closeTag := "</" + name
			c := strings.Index(lower[i:], closeTag)
			if c < 0 {
				c = len(doc) - i
			}
			t.text = doc[i : i+c]
			if name == "title" {
				t.text = cleanText(t.text)
			}
			i += c
		case name == "h1":
			c := strings.Index(lower[i:], "</h1")
			if c >= 0 {
				t.text = cleanText(stripTags(doc[i : i+c]))
			}
		}
		out = append(out, t)
	}
	return out
}

func isNameChar(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == ':'
}

// parseAttrs reads attributes from s[i:] up to the closing '>', honouring
// quotes. It returns the attributes (lowercase keys, unescaped values) and the
// index just past '>'.
func parseAttrs(s string, i int) (map[string]string, int) {
	attrs := map[string]string{}
	n := len(s)
	for i < n {
		for i < n && (s[i] == ' ' || s[i] == '\t' || s[i] == '\n' || s[i] == '\r' || s[i] == '/') {
			i++
		}
		if i >= n {
			break
		}
		if s[i] == '>' {
			return attrs, i + 1
		}
		ks := i
		for i < n && !strings.ContainsRune(" \t\n\r=/>", rune(s[i])) {
			i++
		}
		key := strings.ToLower(s[ks:i])
		for i < n && (s[i] == ' ' || s[i] == '\t' || s[i] == '\n' || s[i] == '\r') {
			i++
		}
		val := ""
		if i < n && s[i] == '=' {
			i++
			for i < n && (s[i] == ' ' || s[i] == '\t' || s[i] == '\n' || s[i] == '\r') {
				i++
			}
			if i < n && (s[i] == '"' || s[i] == '\'') {
				q := s[i]
				e := strings.IndexByte(s[i+1:], q)
				if e < 0 {
					e = n - i - 1
				}
				val = s[i+1 : i+1+e]
				i += e + 2
			} else {
				vs := i
				for i < n && !strings.ContainsRune(" \t\n\r>", rune(s[i])) {
					i++
				}
				val = s[vs:i]
			}
		}
		if key != "" {
			if _, dup := attrs[key]; !dup {
				attrs[key] = html.UnescapeString(val)
			}
		} else {
			i++ // stray character
		}
	}
	return attrs, n
}

var tagRe = regexp.MustCompile(`<[^>]*>`)

func stripTags(s string) string { return tagRe.ReplaceAllString(s, " ") }

func cleanText(s string) string { return strings.Join(strings.Fields(html.UnescapeString(s)), " ") }

// doc is a parsed page.
type doc struct {
	tags []tag
}

func parseDoc(body string) *doc { return &doc{tags: scanTags(body)} }

func (d *doc) all(name string) []tag {
	var out []tag
	for _, t := range d.tags {
		if t.name == name {
			out = append(out, t)
		}
	}
	return out
}

func (d *doc) first(name string) (tag, bool) {
	for _, t := range d.tags {
		if t.name == name {
			return t, true
		}
	}
	return tag{}, false
}

// meta returns the content of the first <meta> whose property or name is key
// (case-insensitive), and whether one exists.
func (d *doc) meta(key string) (string, bool) {
	key = strings.ToLower(key)
	for _, t := range d.tags {
		if t.name != "meta" {
			continue
		}
		if strings.ToLower(t.attr("property")) == key || strings.ToLower(t.attr("name")) == key {
			return strings.TrimSpace(t.attr("content")), true
		}
	}
	return "", false
}

func (d *doc) metaCount(key string) int {
	key = strings.ToLower(key)
	n := 0
	for _, t := range d.all("meta") {
		if strings.ToLower(t.attr("property")) == key || strings.ToLower(t.attr("name")) == key {
			n++
		}
	}
	return n
}

// links returns <link> tags whose rel list contains rel.
func (d *doc) links(rel string) []tag {
	var out []tag
	for _, t := range d.all("link") {
		for _, r := range strings.Fields(strings.ToLower(t.attr("rel"))) {
			if r == rel {
				out = append(out, t)
				break
			}
		}
	}
	return out
}

func (d *doc) title() (string, bool) {
	t, ok := d.first("title")
	return t.text, ok
}
