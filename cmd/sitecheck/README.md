# sitecheck

Audits one web page and prints a findings report (FAIL / WARN / INFO / PASS).
It's the engine behind the [`site-check` skill](../../skills/site-check/SKILL.md),
and works fine on its own.

```bash
sitecheck https://example.com/            # everything, about 30s
sitecheck -no-psi https://example.com/    # skip performance, about 1s
sitecheck -json https://example.com/      # machine-readable
```

| Area | Checks |
| --- | --- |
| basics | status, HTTPS, http→https, redirect chain, TLS expiry, compression, HTTP/2, soft 404, favicon |
| performance | PageSpeed Insights (mobile and desktop): Lighthouse scores, real-user Core Web Vitals, top opportunities, failing accessibility, SEO and best-practice audits |
| social | OG and X card tags fetched as `facebookexternalhit` and `Twitterbot`; share image reachability, format, size, dimensions and aspect ratio (saved to disk so you can look at it) |
| seo | title, description, h1, lang, viewport, canonical, noindex (meta and header), robots.txt per crawler, sitemap, JSON-LD validity, alt text |
| security | HSTS, CSP, nosniff, frame protection, referrer policy, version-leaking headers, mixed content |
| links | broken links (the first `-links` N, same-site first) |

Performance uses the PageSpeed Insights API. Set `PAGESPEED_API_KEY` in
`~/.config/commandcenter/sitecheck.env` (see `config/sitecheck.env.example`),
since the keyless quota is shared and often exhausted. When the API fails,
`-lighthouse auto` (the default) runs Lighthouse locally in headless Chrome,
either from PATH or via `npx -y lighthouse@13`. That gives lab data only.

Flags: `-json`, `-pass`, `-no-psi`, `-strategy mobile,desktop`,
`-lighthouse auto|always|off`, `-links 40`, `-out DIR`, `-timeout 20s`.
