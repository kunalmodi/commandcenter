---
name: site-check
description: Audit a web page for launch quality. Covers PageSpeed / Core Web Vitals, how its link preview looks when shared (Facebook, X, LinkedIn, iMessage, Slack), SEO basics, and HTTPS, security headers and broken links. For the user's own sites it adds Google Search Console data (indexing, search queries and positions) and Bing backlinks. Use when the user asks to check, audit or review a URL or website, asks "is this page ready to launch/share", asks why a link preview looks wrong, or wants SEO or performance feedback on a live page.
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
`-links 0` (skip the broken-link check), `-lighthouse always|off`,
`-no-search` (skip Search Console and Bing), `-q "question"` (repeatable, up to 3: the questions to check the Brave rank for), `-no-brave`, `-crawl N` (also crawl up to N pages
of the site; see below).

It prints findings grouped by area, each marked FAIL, WARN, INFO or PASS:

| Area | What it checks |
| --- | --- |
| basics | status, HTTPS and the http→https redirect, redirect chains, TLS expiry, compression, HTTP/2, soft 404s, favicon |
| performance | PageSpeed Insights mobile and desktop: Lighthouse scores, real-user Core Web Vitals (LCP, INP, CLS), top opportunities, failing accessibility, SEO and best-practice audits |
| social | Open Graph and X card tags *as the crawlers see them* (the page is refetched with the facebookexternalhit and Twitterbot user agents), plus the share image's reachability, format, size, dimensions and aspect ratio |
| seo | title, meta description, h1, lang, viewport, canonical, noindex, robots.txt (also for the preview crawlers), sitemap, JSON-LD, image alt text (including content photos marked `alt=""`) |
| content | main text extracted by **trafilatura** (saved to `content.md`), word count, heading outline, title vs h1, vague or repeated link text, links with no text |
| aeo | answer engines and AI search (read [aeo.md](aeo.md) first) (ChatGPT, Claude, Perplexity, AI Overviews, Copilot): robots.txt rules for each AI crawler, split into ones that answer or cite (blocking them hides you) and ones that only collect training data (a choice); live fetches with AI crawlers' user agents to catch firewall or Cloudflare bot blocking that contradicts robots.txt; whether the text is in the server HTML (most AI crawlers don't run JavaScript); entity, author and date markup; FAQ-style structure; evidence density; Content-Signal; llms.txt; and, with a Brave key, the page's rank on Brave Search (Claude's index) |
| site | only with `-crawl N`: **advertools** crawls the site (sitemap first, obeys robots.txt). Covers broken internal URLs with the page linking to them, duplicate titles and descriptions, missing tags, sitemap entries that redirect, error, are noindexed or canonicalise elsewhere, orphan pages, hreflang return links, and site-wide alt text and link text. The rows are saved to `crawl.jl` |
| security | HSTS, CSP, nosniff, clickjacking, referrer policy, leaked server versions, mixed content |
| links | broken links on the page (the first 40, same-site first) |
| search | **your sites only:** Google Search Console (whether the page is indexed, Google's chosen canonical, last crawl, rich results, the last 28 days of clicks, impressions and positions, top and "striking-distance" queries, submitted sitemaps) and Bing Webmaster Tools (backlinks and linking pages) |

### Your site or someone else's

You don't have to say which. The report's header decides it from the accounts
that are set up:

- `your site: verified in Google Search Console (...)`: the site is a verified
  property in the user's Search Console or Bing account, so the search section
  has real data from the engines.
- `not verified as your site`: the accounts are set up, but this site isn't in
  them. It's a competitor, a client, or something the user is just curious
  about. Every other area still works, because it only uses what any visitor
  can see.
- No such line: neither account is set up yet. Mention once that setting one up
  adds index and query data for the user's own sites (see the README). Don't
  repeat it every run.

Adjust the report to match:

- **Your site:** lead with the search data when it's there. A page that isn't
  indexed, or where Google picked a different canonical, outranks anything a
  public check found. Striking-distance queries (ranking 5-20 with real demand)
  are the cheapest SEO wins. Name them, and say where on the page to use those
  words.
- **Someone else's site:** don't present the missing search data as a gap or a
  problem. Phrase fixes as observations ("their previews would break because
  …"). For a client, say they can add the user's Google account (or the service
  account's email) as a user in Search Console, and verify the site in Bing, to
  get the full picture. Their backlinks and keyword rankings aren't available
  from any free source; that needs a paid data API (DataForSEO is the cheapest).
  Say so if the user asks.
- **Be polite to sites you don't own.** One run makes about 50 light requests,
  which is fine. Don't loop sitecheck over many pages of someone else's site. Use
  `-links 0` for repeat runs.
- **Crawl size.** On the user's own site, crawl it all: use `-crawl` a bit above
  the sitemap size. It runs about 70 pages a second at 4 connections, so 1,300
  pages took 19s. Orphan pages are only judged when the crawl covers 90% of the
  sitemap. On someone else's site, keep `-crawl` to 50 or less, and only when the
  user asks for a site-level look.

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

## 3. Read the content

The `content` file (`content.md`) holds the heading outline and the main text as
trafilatura extracted it. **Read it.** That is where the SEO judgment the rules
can't make happens:

- **What search is this page trying to win?** Say it as a query a person would
  type. If you can't tell, neither can Google. That's the finding.
- **Do the title, h1, opening paragraph and subheadings all aim at that query**,
  in the words searchers use? A clever tagline in the h1 ("Thoughtful finds") is
  weaker than a plain one ("Electrolytes for GLP-1 users").
- **Coverage.** What would someone searching that query expect to find that's
  missing? Comparisons, sizes and prices, who it's for, how to use it, sources.
- **Trust, for health, money and safety topics.** Google holds these to a
  higher standard ("Your Money or Your Life"). Is there an author or reviewer,
  dates, sources and disclosures?
- trafilatura is tuned for articles. On listing pages (home, categories) it
  keeps the card blurbs but may drop names. Judge those pages from the outline
  instead.

Give two or three concrete content suggestions, not a generic checklist.

**For AEO** (getting cited by ChatGPT, Claude and the rest), first read
[aeo.md](aeo.md). It says what the evidence supports and how strongly. The short
version:

1. **Retrieval decides everything.** ChatGPT cites mostly from Bing's results,
   and Claude from Brave's. A page outside those results can't be cited. So the
   first AEO advice is usually: get into Bing (Webmaster Tools, sitemap,
   IndexNow), stay crawlable for Brave, and don't block the retrieval crawlers.
2. **Then make it quotable.** Give a direct answer early, and passages that
   stand on their own. Above all, use **statistics, quotations and cited
   sources**: they raised visibility by up to ~40% in the GEO study, the best
   evidence there is. The `Evidence` line counts them; you judge whether they're
   real and relevant.
3. **Keep it fresh**, especially on health and money topics. Most cited pages
   were updated within two years.
4. **Off-page counts.** Brand mentions on Reddit, review sites, comparison lists
   and YouTube correlate with AI visibility more than backlinks do. Suggest being
   present where the user's questions are already being answered.

Weighing the `aeo` rules:

- **An AI *search* crawler refused by the firewall while robots.txt allows it
  is the most important AEO finding.** It's usually Cloudflare's AI bot
  blocking or a Vercel/WAF rule, and the owner rarely knows. sitecheck only
  spoofs the user agent, and real crawlers come from published IP ranges. So
  say "check the bot settings", not "you are blocked".
- **Training crawlers blocked (GPTBot, ClaudeBot, Google-Extended, CCBot) is a
  legitimate choice.** Never call it an error. Google-Extended has no effect on
  AI Overviews.
- **Don't recommend llms.txt or AI-specific schema as fixes.** The evidence for
  both is nil, and Google says neither is needed. Entity markup is worth adding
  for ordinary search reasons only.
- **Don't promise citations.** Assistants' answers vary between runs, and
  nobody controls selection. Frame advice as raising the odds.

## Checking AI visibility by hand

The rules can't tell whether assistants actually cite the site. Suggest these
checks, which are cheap and real:

- **Prompt sampling.** Ask ChatGPT (with search), Claude, Perplexity and Google
  (to trigger an AI Overview) the two or three questions the page should win,
  from step 3. Note who gets cited, several times over, since answers vary.
- **Retrieval rank.** sitecheck's `Brave rank (Claude's search)` lines cover
  Brave when `BRAVE_SEARCH_API_KEY` is set. They use Search Console's top
  queries for the user's own sites, else phrases from the title and h1 (a guess,
  so a miss there is only INFO). When you know the questions the page should
  win, pass them with `-q`. "ahead" names the sites Claude would likely cite
  instead; look at what they do better. Bing has no public search API any more,
  so check bing.com (ChatGPT's main source) by hand. If the page isn't in the
  top results there, fix that before anything else.
- **Analytics and logs.** Filter for `utm_source=chatgpt.com`, and look for
  `ChatGPT-User`, `Claude-User` and `Perplexity-User` in the server logs.
  Those are live fetches made to answer someone's question.

## Tools underneath

sitecheck calls pinned open-source tools: **trafilatura 2.3.1** (`uvx`),
**advertools 0.18.0** (`uvx`), and **Lighthouse 13** (`npx`, when PageSpeed is
unavailable). The first run downloads each one. If Claude Code blocks one, the
user needs an allow rule for that exact command (see the README). Don't look for
a way around the block.

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
- For a site rather than a page, use `-crawl` for the site-wide checks. Then
  run sitecheck (without `-crawl`) on one URL per template (a blog post, a
  product page) to read their content too.
