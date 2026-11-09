# AEO: getting cited by ChatGPT and Claude

What the evidence actually supports, as of November 2026. Most AEO writing is
vendor marketing, so each point carries a confidence level and a source. Prefer
the high-confidence points. Say so when advice rests on a low one.

## The model: retrieval first, then selection

When ChatGPT or Claude answers with sources, it runs a web search, reads some of
the results, and cites a few. **A page that isn't in the search results can't be
cited**, however well it's written. So AEO is mostly SEO aimed at a different set
of indexes, plus writing that is easy to quote.

| Assistant | Where its search results come from | Confidence |
| --- | --- | --- |
| ChatGPT search | Mostly Bing's results, plus OpenAI's own crawler (OAI-SearchBot). One study matched 87% of ChatGPT citations to Bing's top 20 (small sample, ~500 citations). Only about 8% of ChatGPT's cited links were in Google's top 10 for the same query. | Medium (Seer Interactive, Feb 2025; Ahrefs) |
| Claude | Brave Search's index. Anthropic lists Brave as a subprocessor, and Claude's citations matched Brave's results exactly in an independent test. | High that Brave is used; details undocumented |
| Google AI Overviews / AI Mode | Google's own index. Google says the same SEO fundamentals apply and nothing AI-specific is needed. | High (Google's guidance, 2026) |
| Perplexity | Its own index (PerplexityBot) | Medium |

**What follows:**

1. **Bing matters for ChatGPT.** Verify the site in Bing Webmaster Tools,
   submit the sitemap, and use IndexNow so new pages get in fast. Then check
   where the page ranks on Bing for its target questions.
2. **Brave matters for Claude.** There's no webmaster console or submission
   route. Brave discovers pages through its crawler and through Brave users'
   browsing (the Web Discovery Project). The levers are: stay crawlable, get
   real traffic and links, and check where you rank on search.brave.com.
3. **Don't block the retrieval crawlers.** OpenAI says sites that block
   OAI-SearchBot aren't shown in ChatGPT search answers. Firewall rules matter
   as much as robots.txt; sitecheck tests both.

## Being chosen once retrieved

- **Statistics, quotations and cited sources.** In the GEO study (Aggarwal et
  al., Princeton, KDD 2024), adding these raised a source's visibility in
  generative answers by up to ~40% across ~10k benchmark queries, more in some
  domains than others. This is the best experimental evidence on content.
  *Confidence: medium-high. It's a lab benchmark, not live ChatGPT or Claude.*
  [arXiv 2311.09735](https://arxiv.org/abs/2311.09735)
- **A direct answer near the top, and passages that stand on their own.**
  Assistants quote sentences, not pages. This follows from how retrieval works
  and fits the GEO results, but there's no clean study isolating it.
  *Confidence: medium.* Google says rewriting content into "chunks" for AI gives
  no special advantage in its features. Write clearly; don't contort.
- **Freshness.** In Seer Interactive's analysis of 5,000+ cited URLs, about 79%
  had been published or updated in the last two years, and freshness counted
  most in fast-moving topics (finance, health). A later Seer write-up suggests
  that refreshed older pages get much of that benefit. *Confidence: medium.*
  Update pages for real when facts change, and keep `dateModified` honest.

## Being known without search

When an assistant answers from memory, or picks between brands, what the wider
web says about you counts.

- **Brand mentions correlated more with AI visibility than backlinks** in Ahrefs'
  study of 75,000 brands: 0.66 for branded web mentions against 0.22 for
  backlinks, and higher still for YouTube mentions. It's correlation, not proof,
  and summaries disagree on which AI surface it measured. *Confidence: medium.*
- **Third-party pages carry a large share of citations.** Reddit is about 17% of
  ChatGPT's US citations, and Wikipedia about 9% (Ahrefs Brand Radar, July
  2026). For "which brand should I buy" questions, review sites and comparison
  lists dominate. **The realistic move is to be mentioned on the pages that
  already get cited for your questions**, through real participation, reviews
  and PR. Not fake mentions, which Google explicitly calls out.

## What a field test showed

One structured test (November 2026; 20 runs across ChatGPT and Claude, 5
switching and how-to prompts for a niche software tool, about 440 pages
retrieved). *Confidence: anecdotal, one category*, but it fits the research
above and is concrete about what to build.

- **Fan-out queries check a few hard facts.** Nearly every search the
  assistants ran asked: is it free and what are the limits, is it compatible
  with the incumbent's format (here, "imports .fdx"), and what does it cost.
  They usually add the current year ("... 2026"). ChatGPT's answer tables used
  exactly those columns (Free plan, Imports X?, Cost, Best for). **So state
  those facts in plain text**, ideally as a comparison table, with a visible
  "updated <year>" date.
- **Four kinds of page got cited:**
  1. **Vendor help docs** that answer one question ("How to import a Final
     Draft file"). One competitor's import article was cited in every run on
     that question. Title it as the question and answer it in the first line.
  2. **Pricing pages**, fetched to confirm free-tier limits. Limits and prices
     hidden behind toggles or images don't get read.
  3. **"X alternatives" listicles, often on tiny sites** (pages.dev and
     onrender.com subdomains, AlternativeTo). The bar is low. Being listed
     there is often easier than ranking yourself.
  4. **Single-purpose tool pages** ("PDF to Fountain converter"). A small
     dedicated converter site was cited in every run on its question.
- **Comparison pages ("A vs B") mattered less**: assistants mostly cited each
  vendor's own pages for those.
- **Some questions weren't searched at all.** Claude answered "how do I open
  file type Y" from memory in both runs (ChatGPT searched). For those, only
  being well known on the wider web helps.
- **An existing page that answers the question exactly was never retrieved.**
  The cause was discoverability, not content: check the index (Search Console
  and Bing), server rendering, and a title in the searchers' words ("Free PDF
  to Fountain converter, online, no signup") before rewriting.

**Typical priority list that comes out of a test like this:** make existing
answer pages discoverable; add the sibling tool pages people search for; a
"switching from <incumbent>" page that leads with the facts table; a help doc
per compatibility question; plain-text pricing and limits; listings on
AlternativeTo and the small listicles that get cited; comparison pages last.

## Low value or myths

- **llms.txt.** In Ahrefs' 2026 data (137k domains), 97% of published llms.txt
  files got zero requests. SE Ranking (300k domains) found no citation effect.
  Google says it doesn't use them. Perplexity reportedly reads them. Fine as a
  five-minute extra; never a priority.
- **AI-specific schema.** Google: "there's no special schema.org markup you need
  to add" for its AI features. Organization, Article and Product markup still
  earn rich results in normal search and help engines identify the entity, so
  add them for that reason.
- **AI-rewritten or "chunked" content, and seeded brand mentions.** Google lists
  these as giving no advantage.
- **Ranking well in Google alone.** That doesn't carry over: ChatGPT mostly
  cites pages outside Google's top 10.

## Measuring it

- **Referral traffic:** ChatGPT links carry `utm_source=chatgpt.com`, so filter
  for it in analytics. Claude and Perplexity referrals show as referrers
  (claude.ai, perplexity.ai).
- **Server logs:** visits from `ChatGPT-User`, `Claude-User` or
  `Perplexity-User` are fetches made while answering a real person's question.
  They're the most direct sign that you're being retrieved. Training-crawler
  hits (GPTBot, ClaudeBot) say nothing about citations.
- **Prompt sampling:** ask ChatGPT (with search), Claude and Perplexity the
  questions the page should win, several times each, since answers vary. Use
  temporary or incognito chats, and discount mentions that come from the
  user's own memory or installed connectors. Log the assistant's search queries
  (Claude shows them), what it retrieved and what it cited.
- **Retrieval rank:** where the page ranks on Bing and Brave for those questions
  is the closest measurable proxy for "can be cited".

## Sources

- [GEO: Generative Engine Optimization (Aggarwal et al., KDD 2024)](https://arxiv.org/abs/2311.09735)
- [Seer Interactive: AI brand visibility and content recency](https://www.seerinteractive.com/insights/study-ai-brand-visibility-and-content-recency)
- [Simon Willison: Anthropic uses Brave for Claude's web search](https://feeds.simonwillison.net/2025/Mar/21/anthropic-use-brave)
- [Search Engine Journal: Google's AI search guide calls AEO and GEO still SEO](https://www.searchenginejournal.com/googles-new-ai-search-guide-calls-aeo-and-geo-still-seo/575026/)
- [Search Engine Journal: 97% of llms.txt files got no requests (Ahrefs data)](https://searchenginejournal.com/97-of-llms-txt-files-got-no-requests-ahrefs-data-shows/579478)
- [OpenAI crawler documentation](https://developers.openai.com/docs/bots)
- Summaries of Ahrefs' brand-mention and citation studies: [Onely](https://www.onely.com/blog/what-influences-brand-visibility-in-ai-search-a-practical-guide-for-2026/), [saasylinks (7 studies)](https://saasylinks.com/blog/most-cited-websites-chatgpt/)
