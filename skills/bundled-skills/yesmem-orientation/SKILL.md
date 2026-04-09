---
name: yesmem-orientation
description: Use at session start (first user message), when switching projects, returning after a break, or when disoriented about project state. Trigger on "where were we?", "what's open?", "wo waren wir?", or when context about the current project is needed.
---

# Project Orientation

Get oriented at session start or when switching context.

## Workflow
1. `get_project_profile(project)` — auto-generated project portrait
2. `get_learnings(category="unfinished", project)` — open tasks and blocked items
3. `get_plan()` — active implementation plan (if any)
4. `get_persona()` — current user profile and traits

## Quick Context

| Need | Tool |
|------|------|
| "What is this project?" | `get_project_profile` |
| "What's open/blocked?" | `get_learnings(category="unfinished")` |
| "Is there an active plan?" | `get_plan` |
| "What projects exist?" | `list_projects` |
| "Project timeline" | `project_summary(project)` |
| "Which files were touched?" | `get_coverage(project)` |
| "How am I doing?" | `get_self_feedback(days=30)` |
| "Set user preference" | `set_persona(trait_key, value)` |

## Tips
- `get_learnings(category="unfinished")` returns tasks, ideas, blocked items, stale items
- Filter with `task_type`: task, idea, blocked, stale
- `project_summary` gives chronological session overview
- `get_coverage` shows which files were edited across sessions
- `set_persona` for manual trait overrides (highest priority)
