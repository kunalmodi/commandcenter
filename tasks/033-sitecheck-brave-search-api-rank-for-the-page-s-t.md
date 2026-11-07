---
id: 033
title: sitecheck: Brave Search API rank for the page's target questions (proxy for Claude citations)
status: done
area: skills
created: 2026-11-06
---

- 2026-11-06: Built: brave.go. Queries from -q, else GSC top queries, else title/h1 (misses INFO). Top 20 results, PASS at #1-5, reports up to 3 sites ahead. Bing rank stays manual (no public search API).
