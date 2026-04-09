---
name: yesmem-recall
description: ALWAYS check memory BEFORE answering questions about past work, architecture, or prior decisions. Also before proposing fixes or solutions — check if similar issues were solved before. Use when encountering errors with possible prior context, when the user references previous sessions, or when working with unfamiliar components. Search first, respond second.
---

# Memory Recall

Search yesmem knowledge base for past decisions, patterns, gotchas, and context.

## Workflow
1. `hybrid_search(query, project)` — best default, combines BM25 keyword + vector semantic
2. `query_facts(entity, action, keyword)` — structured metadata search (files, commands, tags)
3. `deep_search(query)` — full conversation history with reasoning and command outputs
4. `search(query)` — full-text across all sessions (cross-project)

## Which Tool When

| Need | Tool |
|------|------|
| General knowledge recall | `hybrid_search` |
| "What do we know about file X?" | `query_facts(entity="filename")` |
| "When did we decide Y?" | `hybrid_search` + filter by date |
| Raw conversation context | `deep_search` |
| Cross-project knowledge | `search` (no project filter) |
| Learnings by category | `get_learnings(category="gotcha\|decision\|pattern")` |

## Tips
- Search with terms the **source** used, not your summary
- 0 results? Reformulate with concrete identifiers (filenames, functions, error messages)
- `deep_search` returns full untruncated content including command outputs
- `query_facts` uses LIKE-matching on entities, actions, keywords
