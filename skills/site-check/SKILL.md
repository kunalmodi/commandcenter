---
name: site-check
description: Audit a web page for launch quality. Covers PageSpeed / Core Web Vitals, how its link preview looks when shared (Facebook, X, LinkedIn, iMessage, Slack), SEO basics, and HTTPS, security headers and broken links. Use when the user asks to check, audit or review a URL or website, asks "is this page ready to launch/share", asks why a link preview looks wrong, or wants SEO or performance feedback on a live page.
---

# Site check

Audit one live URL and give the user a short, prioritized list of what to fix.
The `sitecheck` command does the fetching and checking. Your job is to run it,
look at what a person would see (the share image), and turn the findings into
advice.

## 1. Run the check

```bash
sitecheck https://example.com/page
```

If `sitecheck` is not on PATH, build it from the commandcenter repo
(`make install` puts it in `~/.local/bin`), or run `go run ./cmd/sitecheck` there.

Useful flags: `-json` (structured output), `-pass` (also list passing checks),
`-no-psi` (skip performance, about 1s instead of about 30s), `-strategy mobile`,
`-links 0` (skip the broken-link check), `-lighthouse always|off`.

It prints findings grouped by area, each marked FAIL, WARN, INFO or PASS:

| Area | What it checks |
| --- | --- |
| basics | status, HTTPS and the http→https redirect, redirect chains, TLS expiry, compression, HTTP/2, soft 404s, favicon |
| performance | PageSpeed Insights mobile and desktop: Lighthouse scores, real-user Core Web Vitals (LCP, INP, CLS), top opportunities, failing accessibility, SEO and best-practice audits |
| social | Open Graph and X card tags *as the crawlers see them* (the page is refetched with the facebookexternalhit and Twitterbot user agents), plus the share image's reachability, format, size, dimensions and aspect ratio |
| seo | title, meta description, h1, lang, viewport, canonical, noindex, robots.txt (also for the preview crawlers), sitemap, JSON-LD, image alt text |
| security | HSTS, CSP, nosniff, clickjacking, referrer policy, leaked server versions, mixed content |
| links | broken links on the page (the first 40, same-site first) |

**Performance source.** It uses the PageSpeed Insights API when it can. The
keyless quota is shared and often exhausted (HTTP 429). It then falls back to a
local Lighthouse run in headless Chrome (via `npx lighthouse@13`), and says so
in a `source` line. Local runs have **no real-user data**, and their scores
depend on this Mac's network, so present them as rough lab numbers. If PageSpeed
keeps failing, tell the user to add a free key to
`~/.config/commandcenter/sitecheck.env`:

```
PAGESPEED_API_KEY=...   # Google Cloud console: enable "PageSpeed Insights API", then Credentials → API key
```

## 2. Look at the share image

The report's `files` section has the share image path (`share_image`, and
`x_share_image` if X gets a different one). **Open it with the Read tool and
look at it.** The checks can't judge:

- Is it the right image for this page, and not a placeholder, default logo or
  blank frame?
- Is any text readable at thumbnail size (about 500px wide in a feed, smaller in
  iMessage or Slack)?
- Does it survive cropping? Large previews crop to about 1.91:1. Some apps
  (WhatsApp, iMessage on narrow screens, X `summary` cards) crop to a square, so
  key content should sit in the center.
- Does it look fine on both light and dark backgrounds? Transparent PNGs often
  don't.

If the user wants to *see* the previews, sketch them as a small HTML artifact
using the real title, description, site name and image. Label it as an
approximation: each platform's exact layout changes often.

## 3. Interpret, don't just relay

- **Field data beats lab data.** Google ranks with real-user Core Web Vitals
  (the "real users (p75)" line, from the Chrome UX Report). Lighthouse scores are
  diagnostics. A 70 performance score with passing field data is fine; failing
  field data is a real problem whatever the score says. Data marked "the whole
  origin" means this URL lacked traffic of its own.
- **Some FAILs are intentional.** `noindex`, or a canonical pointing elsewhere,
  is correct on staging, previews and pagination. If the URL looks like staging
  (`staging.`, `preview`, `*.vercel.app`, `localhost`), say so instead of
  sounding the alarm, or ask whether it's meant to be public.
- **Crawler-specific results matter.** If the facebookexternalhit or Twitterbot
  fetch gets a 403, a different page, or tags the browser fetch doesn't, it's
  usually bot protection (Cloudflare, Vercel firewall) or client-side-only meta
  tags. Previews break everywhere then, so rank it high.
- **Client-rendered sites.** sitecheck reads the HTML the server sends, like the
  preview crawlers do. Tags that appear only after JavaScript runs are invisible
  to Facebook, X, LinkedIn and Slack. Google renders JS, but slowly. If title, OG
  tags or the h1 are missing yet the page clearly has them in a browser, that's
  the cause. The fix is server-side rendering or static meta tags.
- Treat INFO lines as optional polish. Mention a few only if they're cheap wins.

## 4. Report back

Keep it short and ordered by impact:

1. **One-line verdict**, e.g. "Ready to share. Two SEO fixes are worth doing
   first" or "Link previews are broken: Facebook's crawler gets a 403".
2. **Fix first**: the FAILs and important WARNs, each with *why it matters* and
   *the concrete fix* (the tag to add, the header to set, the image size to
   export). Group duplicates; mobile and desktop often report the same thing.
3. **Scores**: one line per strategy, plus field Core Web Vitals if present.
4. **Worth doing later**: a few cheap polish items.
5. **Not checked**: anything that failed to run, and the manual checks below
   that matter for this page.

Don't paste the raw report unless asked.

## Manual checks to suggest

These need the user's own login, or a person looking at the result:

- **Facebook Sharing Debugger** (developers.facebook.com/tools/debug): needs a
  Facebook login. After fixing tags, click **Scrape Again**. Facebook caches
  previews for about 30 days, so fixes don't show until it rescrapes.
- **LinkedIn Post Inspector** (linkedin.com/post-inspector): also refreshes
  LinkedIn's cache.
- **X**: the Card Validator no longer shows previews. Paste the URL into a new
  post (don't send it) to see the card.
- **Google Rich Results Test** (search.google.com/test/rich-results) when the page
  has JSON-LD, and **Search Console → URL Inspection** for indexing status if the
  user has access.
- For a site rather than a page, run sitecheck on one URL per template (home, a
  blog post, a product page) rather than just the homepage.
