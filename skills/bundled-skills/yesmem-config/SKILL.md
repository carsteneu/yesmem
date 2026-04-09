---
name: yesmem-config
description: Use when managing pins, scratchpad, runtime config, session settings, or persona traits. Trigger on "pin this"/"merk dir als Regel", persistent instructions, shared agent state, persona overrides, or configuration changes like token_threshold.
---

# Configuration & State

Manage pins, scratchpad, persona, and runtime settings.

## Pins (Persistent Instructions)
Instructions visible in EVERY turn — survive collapse and stubbing.

| Action | Tool |
|--------|------|
| Pin instruction | `pin(content, scope="session\|permanent")` |
| Remove pin | `unpin(id, scope)` |
| List pins | `get_pins(project)` |

- `session` pins last until /clear
- `permanent` pins survive across sessions

## Scratchpad (Shared State)
Key-value sections for inter-agent or cross-session data.

| Action | Tool |
|--------|------|
| Write section | `scratchpad_write(project, section, content)` |
| Read section(s) | `scratchpad_read(project, section)` |
| List sections | `scratchpad_list(project)` |
| Delete | `scratchpad_delete(project, section)` |

## Runtime Config

| Action | Tool |
|--------|------|
| Read setting | `get_config(key)` |
| Change setting | `set_config(key, value)` |

Currently supported: `token_threshold` (per-session or global).

## Persona

| Action | Tool |
|--------|------|
| Set trait | `set_persona(trait_key, value)` — user override, highest priority |

Dimensions: communication, workflow, expertise, context, boundaries, learning_style.

## Session Control

| Action | Tool |
|--------|------|
| Skip indexing | `skip_indexing(session_id)` — session won't be extracted |
| Quarantine session | `quarantine_session(session_id)` — exclude all learnings |
| Who am I? | `whoami()` — get session ID and agent metadata |
