---
name: yesmem-remember
description: Use after ANY decision, correction, gotcha, or discovery worth preserving. Also on task completion — resolve open tasks via resolve_by_text() after commits, "fertig", or confirmed fixes. Trigger on "remember this"/"merk dir das", confirmed approaches, debugging surprises, and relate_learnings() for connecting insights.
---

# Saving Knowledge

Store learnings with structured metadata for better retrieval.

## Workflow
1. Identify what to save (decision, gotcha, pattern, preference, teaching)
2. Call `remember()` with rich metadata
3. For completed tasks: `resolve_by_text(text)` or `resolve(learning_id, reason)`
4. For connected insights: `relate_learnings(id_a, id_b, relation_type)`

## remember() Parameters

| Parameter | Purpose | Example |
|-----------|---------|---------|
| `text` | What to remember | "CGO is disabled in yesmem Makefile" |
| `category` | gotcha, decision, pattern, preference, explicit_teaching, strategic | "decision" |
| `entities` | Files, systems, people | ["Makefile", "CGO"] |
| `actions` | Commands, operations | ["go build", "make deploy"] |
| `trigger` | When to surface this | "When discussing CGO or cross-compilation" |
| `context` | Why this matters | "Breaks tree-sitter integration" |
| `anticipated_queries` | 3-5 search phrases | ["CGO disabled", "cross compile yesmem"] |
| `source` | user_stated, agreed_upon, claude_suggested | "agreed_upon" |
| `project` | Project scope | "yesmem" |
| `supersedes` | ID of learning this replaces | 12345 |

## relate_learnings()

Connect two learnings with semantic edges:
- `supports` — one learning confirms another
- `contradicts` — learnings conflict
- `depends_on` — one requires the other
- `relates_to` — general connection

## Tips
- `anticipated_queries`: vary between short keywords and full questions
- `source` trust hierarchy: user_stated > agreed_upon > claude_suggested
- Use `supersedes` when updating outdated knowledge — don't create duplicates
- `resolve_by_text` finds matching open tasks by text search
