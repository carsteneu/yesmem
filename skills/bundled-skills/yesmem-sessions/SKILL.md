---
name: yesmem-sessions
description: Use when exploring past sessions, asking "what happened last week/yesterday", needing full conversation details, or when the user references a specific past conversation. Trigger on "letzte Session", "last time we...", session history investigation.
---

# Session Exploration

Navigate past conversation history and archived content.

## Workflow
1. `search(query, project)` — find sessions by content
2. `get_session(session_id, mode)` — load session details
3. `get_compacted_stubs(session_id)` — see archived conversation blocks
4. `expand_context(query)` — expand collapsed/archived content

## get_session Modes

| Mode | Returns | Use for |
|------|---------|---------|
| `summary` | Overview + metadata | Quick check |
| `recent` | Last N messages (truncated 500 chars) | Recent activity |
| `paginated` | Full messages with offset | Deep dive |
| `full` | Complete untruncated content | Full analysis |

## Tips
- `related_to_file(path)` — find which sessions touched a specific file
- `expand_context(query)` — re-expand archived blocks matching your search
- `get_compacted_stubs` shows stub summaries of collapsed conversation parts
- Use `search` across all projects, `hybrid_search` within one project
