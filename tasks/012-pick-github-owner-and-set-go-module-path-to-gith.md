---
id: 012
title: Pick GitHub owner and set Go module path to github.com/OWNER/commandcenter
status: done
area: publish
created: 2026-11-04
---

- 2026-11-04: go.mod currently says 'module commandcenter', which builds fine locally but not with go install from GitHub.
- 2026-11-04: module github.com/kunalmodi/commandcenter
