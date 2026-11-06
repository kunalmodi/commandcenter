package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// Share previews are built by crawlers, not browsers, and sites sometimes
// serve them different HTML (or block them). So these checks refetch the
// page with each crawler's user agent and read the tags from what it got.
const (
	facebookUA = "facebookexternalhit/1.1 (+http://www.facebook.com/externalhit_uatext.php)"
	twitterUA  = "Twitterbot/1.0"
)

const maxImage = 16 << 20

func checkSocial(ctx context.Context, r *report, f *fetcher, pg *page, outDir string) {
	const a = "social"
	fb, err := f.get(ctx, pg.final.String(), facebookUA)
	if err != nil {
		r.add(a, fail, "Facebook crawler", "could not fetch the page as facebookexternalhit: %v", err)
		return
	}
	if fb.status != 200 {
		r.add(a, fail, "Facebook crawler", "page answers %d to facebookexternalhit (previews on Facebook, LinkedIn, iMessage, Slack will break)", fb.status)
	}
	tw, err := f.get(ctx, pg.final.String(), twitterUA)
	switch {
	case err != nil:
		r.add(a, warn, "X crawler", "could not fetch the page as Twitterbot: %v", err)
	case tw.status != 200:
		r.add(a, fail, "X crawler", "page answers %d to Twitterbot (no card on X)", tw.status)
	}
	d := parseDoc(fb.body)

	get := func(k string) string { v, _ := d.meta(k); return v }
	title, desc, img := get("og:title"), get("og:description"), get("og:image")
	pageTitle, _ := d.title()

	r.fact("og", map[string]string{
		"title": title, "description": desc, "image": img, "url": get("og:url"),
		"type": get("og:type"), "site_name": get("og:site_name"),
	})

	switch {
	case title == "" && pageTitle != "":
		r.add(a, warn, "og:title", "missing; Facebook falls back to <title> %q, others may show the bare URL", pageTitle)
	case title == "":
		r.add(a, fail, "og:title", "missing, and no <title> either")
	case utf8.RuneCountInString(title) > 70:
		r.add(a, warn, "og:title", "%d characters; previews usually cut off around 60-70: %q", utf8.RuneCountInString(title), title)
	default:
		r.add(a, pass, "og:title", "%q", title)
	}
	switch n := utf8.RuneCountInString(desc); {
	case desc == "":
		r.add(a, warn, "og:description", "missing (the preview shows no summary, or a random page snippet)")
	case n > 200:
		r.add(a, info, "og:description", "%d characters; only the first ~150 show in most previews", n)
	default:
		r.add(a, pass, "og:description", "%q", desc)
	}
	if v := get("og:url"); v == "" {
		r.add(a, info, "og:url", "missing (likes and shares are counted against whatever URL was shared)")
	} else if u, err := url.Parse(v); err == nil && !sameURL(u, fb.final) {
		r.add(a, info, "og:url", "is %s, not this page's URL; shares are attributed to it", v)
	}
	if get("og:type") == "" {
		r.add(a, info, "og:type", "missing (defaults to website)")
	}
	if get("og:site_name") == "" {
		r.add(a, info, "og:site_name", "missing (shown above the title on Facebook and in Slack/Discord)")
	}
	if d.metaCount("og:title") > 1 || d.metaCount("og:description") > 1 {
		r.add(a, warn, "Duplicate OG tags", "several og:title or og:description tags; crawlers pick one unpredictably")
	}

	// X card
	twd := d
	if tw != nil && tw.status == 200 {
		twd = parseDoc(tw.body)
	}
	tget := func(k string) string { v, _ := twd.meta(k); return v }
	card := tget("twitter:card")
	switch card {
	case "":
		r.add(a, warn, "twitter:card", "missing; X shows a small summary card at best. Use summary_large_image for a big image preview")
	case "summary", "summary_large_image", "app", "player":
		r.add(a, pass, "twitter:card", card)
	default:
		r.add(a, fail, "twitter:card", "unknown value %q", card)
	}
	if tget("twitter:title") == "" && title != "" {
		r.add(a, info, "twitter:title", "not set; X uses og:title")
	}
	if tget("twitter:site") == "" {
		r.add(a, info, "twitter:site", "not set (the @account shown on the card)")
	}

	// The image itself.
	timg := tget("twitter:image")
	if img == "" && timg == "" {
		r.add(a, fail, "og:image", "missing: links will share with no image, which gets far fewer clicks")
		return
	}
	if img == "" {
		r.add(a, warn, "og:image", "missing; only twitter:image is set, so Facebook, LinkedIn, iMessage and Slack get no image")
		img = timg
	}
	if timg != "" && timg != img {
		r.add(a, info, "twitter:image", "differs from og:image, so X shows a different image: %s", timg)
		defer checkShareImage(ctx, r, f, fb.final, timg, "twitter:image", card, true, twd, outDir)
	}
	if get("og:image:alt") == "" && tget("twitter:image:alt") == "" {
		r.add(a, info, "Image alt", "no og:image:alt or twitter:image:alt (screen readers describe the preview with it)")
	}
	checkShareImage(ctx, r, f, fb.final, img, "og:image", card, timg == "" || timg == img, d, outDir)
}

// checkShareImage fetches a share image and checks it. xUses says whether
// this is the image X will show (twitter:image if set, else og:image).
func checkShareImage(ctx context.Context, r *report, f *fetcher, base *url.URL, img, label, card string, xUses bool, d *doc, outDir string) {
	forX := label == "twitter:image"
	const a = "social"
	u, err := url.Parse(img)
	if err != nil {
		r.add(a, fail, label, "bad URL %q", img)
		return
	}
	if !u.IsAbs() {
		r.add(a, fail, label, "relative URL %q; crawlers need an absolute https:// URL", img)
		u = base.ResolveReference(u)
	} else if u.Scheme == "http" {
		r.add(a, warn, label, "http:// URL; some apps refuse non-https images (use og:image:secure_url or https)")
	}
	p, err := f.do(ctx, http.MethodGet, u.String(), facebookUA, maxImage, nil)
	if err != nil {
		r.add(a, fail, label, "could not fetch %s: %v", u, err)
		return
	}
	if p.status != 200 {
		r.add(a, fail, label, "%s answers %d", u, p.status)
		return
	}
	ct, _, _ := mime.ParseMediaType(p.header.Get("Content-Type"))
	size := len(p.body)
	if p.truncated {
		r.add(a, fail, label+" size", "over %d MB (Facebook's limit is 8 MB, X's 5 MB)", maxImage>>20)
		return
	}
	w, h, format := imageSize([]byte(p.body))
	if !strings.HasPrefix(ct, "image/") {
		r.add(a, warn, label+" type", "served as %q, not image/*", ct)
	}
	switch format {
	case "svg":
		r.add(a, fail, label+" format", "SVG is not supported by Facebook, X or LinkedIn; use PNG or JPEG")
	case "":
		r.add(a, warn, label+" format", "could not recognise the image (%s)", ct)
	case "avif":
		r.add(a, warn, label+" format", "AVIF is not supported by many preview crawlers; use PNG or JPEG")
	case "webp":
		r.add(a, info, label+" format", "WebP works on Facebook and X, but some apps (older LinkedIn, email clients) want PNG or JPEG")
	}
	switch {
	case size > 8<<20:
		r.add(a, fail, label+" size", "%.1f MB; Facebook's limit is 8 MB, X's 5 MB", float64(size)/(1<<20))
	case size > 5<<20:
		r.add(a, warn, label+" size", "%.1f MB; X's limit is 5 MB", float64(size)/(1<<20))
	case size > 1<<20:
		r.add(a, info, label+" size", "%.1f MB; under 1 MB loads faster in feeds and chats", float64(size)/(1<<20))
	}

	if outDir != "" && format != "" {
		ext := map[string]string{"jpeg": ".jpg", "png": ".png", "gif": ".gif", "webp": ".webp", "svg": ".svg", "avif": ".avif"}[format]
		key, name := "share_image", "share-image"
		if forX {
			key, name = "x_share_image", "x-share-image"
		}
		path := filepath.Join(outDir, name+ext)
		if err := os.WriteFile(path, []byte(p.body), 0o644); err == nil {
			r.file(key, path)
		}
	}

	if w == 0 || h == 0 {
		return
	}
	r.fact(strings.ReplaceAll(label, ":", "_")+"_size", fmt.Sprintf("%dx%d", w, h))
	ratio := float64(w) / float64(h)
	// Facebook, LinkedIn and chat apps show a large preview for any image
	// 600x315 or bigger; X only does with summary_large_image.
	large := !forX || card == "summary_large_image"
	switch {
	case w < 200 || h < 200:
		r.add(a, fail, label+" dimensions", "%dx%d; Facebook ignores images under 200x200", w, h)
	case large && (w < 600 || h < 315):
		r.add(a, warn, label+" dimensions", "%dx%d; below 600x315 shows as a small thumbnail, not a large preview. Use 1200x630", w, h)
	case large && (ratio < 1.6 || ratio > 2.1):
		r.add(a, warn, label+" aspect ratio", "%dx%d (%.2f:1); large previews are cropped to about 1.91:1, so use 1200x630", w, h, ratio)
	case w < 1200:
		r.add(a, info, label+" dimensions", "%dx%d works; 1200x630 looks sharper on high-density screens", w, h)
	default:
		r.add(a, pass, label+" dimensions", "%dx%d (%.2f:1)", w, h, ratio)
	}
	if xUses && card == "summary" && ratio > 1.2 {
		r.add(a, info, "twitter:card", "card is summary (square thumbnail) but the image is wide; summary_large_image shows it in full")
	}
	if forX {
		return
	}
	_, hasW := d.meta("og:image:width")
	_, hasH := d.meta("og:image:height")
	if !hasW || !hasH {
		r.add(a, info, "og:image:width/height", "not declared; without them Facebook may show no image the first time a link is shared")
	}
}

// imageSize returns width, height and format for PNG, JPEG, GIF and WebP,
// and recognises SVG and AVIF by their headers.
func imageSize(b []byte) (int, int, string) {
	if cfg, format, err := image.DecodeConfig(bytes.NewReader(b)); err == nil {
		return cfg.Width, cfg.Height, format
	}
	if len(b) >= 30 && string(b[0:4]) == "RIFF" && string(b[8:12]) == "WEBP" {
		switch string(b[12:16]) {
		case "VP8X":
			w := 1 + (int(b[24]) | int(b[25])<<8 | int(b[26])<<16)
			h := 1 + (int(b[27]) | int(b[28])<<8 | int(b[29])<<16)
			return w, h, "webp"
		case "VP8 ":
			if b[23] == 0x9d && b[24] == 0x01 && b[25] == 0x2a {
				return int(binary.LittleEndian.Uint16(b[26:28]) & 0x3fff), int(binary.LittleEndian.Uint16(b[28:30]) & 0x3fff), "webp"
			}
		case "VP8L":
			if b[20] == 0x2f {
				bits := binary.LittleEndian.Uint32(b[21:25])
				return int(bits&0x3fff) + 1, int((bits>>14)&0x3fff) + 1, "webp"
			}
		}
		return 0, 0, "webp"
	}
	if len(b) >= 12 && string(b[4:8]) == "ftyp" && (string(b[8:12]) == "avif" || string(b[8:12]) == "avis") {
		return 0, 0, "avif"
	}
	head := strings.ToLower(string(b[:min(len(b), 512)]))
	if strings.Contains(head, "<svg") {
		return 0, 0, "svg"
	}
	return 0, 0, ""
}
