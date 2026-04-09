---
name: yesmem-docs
description: Use when writing code and unsure about API behavior, function signatures, or idiomatic patterns. Use when debugging errors that might stem from incorrect API usage. Use when managing indexed documentation sources. Check docs_search() before guessing — indexed docs exist for a reason.
---

# Documentation Search

Search indexed documentation. Run `list_docs()` to see currently indexed sources.

## Workflow
1. `list_docs()` — see all indexed documentation sources
2. `docs_search(query)` — search all indexed docs
3. `docs_search(query, source="<name>")` — filter by specific source

## docs_search Parameters

| Parameter | Purpose | Example |
|-----------|---------|---------|
| `query` | Search text | "context cancellation" |
| `source` | Filter by doc source | see list_docs() for names |
| `section` | Filter by heading | "Concurrency" |
| `doc_type` | "reference" or "style" | "reference" |
| `exact` | BM25 only, no semantic | true |
| `limit` | Max results (default 5) | 10 |

## Managing Docs

| Action | Tool |
|--------|------|
| Index new docs | `ingest_docs(name, path, version, trigger_extensions)` |
| List sources | `list_docs(project)` |
| Remove source | `remove_docs(name)` |

## Tips
- `trigger_extensions` (e.g. ".go", ".twig") enable auto-injection when editing matching files
- `doc_type="reference"` = auto-injected, `"style"` = only via explicit search
- `rules=true` condenses CLAUDE.md into behavioral rules for periodic re-injection
