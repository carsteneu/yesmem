# Caps vs. Skills — Why a separate format

## Summary

Capabilities (Caps) use `CAP.md` in `~/.claude/caps/` instead of `SKILL.md` in `~/.claude/skills/`. This document explains the decision.

## What are Caps, what are Skills?

| | Skills | Caps |
|---|---|---|
| **Purpose** | Instructions — how to do something | Tools — do something |
| **Mechanism** | Markdown instructions, read by Claude | `registerTool()` code, executed in the REPL |
| **Example** | "How to write good commits" | `reddit_fetch({url})` → structured data |
| **Lifecycle** | Loaded on trigger, then discarded | Registered, remains available as a tool |
| **Persistence** | None (file is read each time) | DB-backed (survives sessions) |

## Experiment: Cap as SKILL.md

A cap (reddit_fetch) was stored as `SKILL.md` in `~/.claude/skills/`. Result:

**What works:**
- Claude Code discovers the file automatically
- Description appears in the skill list
- Invocation via `/reddit_fetch` loads the content
- Non-standard frontmatter fields (`auto_active`, `runtime`, `tags`) are tolerated

**Why it is still the wrong approach:**

### 1. Scaling — the killer argument

Claude Code injects **all skill descriptions into every turn** as a system reminder. With 50 skills that is ~12,500 characters. Add 100 caps on top of that:

- ~37,500 characters per turn → 18x over the `skillListingBudgetFraction` budget (1% of the context window)
- Massive truncation of descriptions
- Skill-eval per turn: evaluating 150 entries instead of 50

The `<caps-available>` catalogue in the proxy is built for this: a compact table (~1.5k for all caps), once in the system block, not listed per skill. 100 caps in the catalogue = ~5k tokens. 100 caps as skills = unaffordable overhead per turn.

### 2. Semantics — Caps are not Skills

Skills are cross-project techniques (commit standards, TDD workflow, debugging methodology). Caps are executable tools (fetch Reddit, analyse data, deploy). Mixing these two concerns in one directory creates:

- Noise in the skill list (tools that are not instructions)
- Confusion during skill-eval (is this a technique or a tool?)
- Wrong expectations for users ("why is reddit_fetch a skill?")

### 3. Future-proofing — Caps will grow

CAP.md can accommodate features that SKILL.md does not support:

- **Versioning** — v1, v2, v3 with supersede chains
- **Database-Section** — SQL schema for per-cap tables
- **Dependencies** — `requires: [cap_store, blob_put]` with provider mapping
- **Testing-Metadata** — `tested: true`, `test_date`, verification status
- **Multi-Handler** — `handler_repl` + `handler_bash` in a single file

SKILL.md is deliberately kept simple (name + description in frontmatter). That simplicity is a strength for skills, but a constraint for caps.

### 4. Provider-agnostic adapter layer

Caps use generic functions (`cap_store`, `blob_put`) instead of provider-specific MCP tools. Each provider (yesmem, others) registers its own adapters:

```
Cap code:     await cap_store({capability: "mein_tool", action: "upsert", ...})
                     │
         ┌───────────┼───────────┐
         ▼           ▼           ▼
    yesmem-Adapter  Provider-X  Fallback
    → mcp__yesmem__ → mcp__x__  → in-memory
      cap_store       store       (session-only)
```

This pattern requires an interchange format that declares `requires` dependencies — SKILL.md has no standard mechanism for that.

## Decision

- **Interchange format:** `CAP.md` in `~/.claude/caps/<name>/CAP.md`
- **Runtime persistence:** yesmem DB (learnings, category='cap')
- **Discovery:** Proxy-injected `<caps-available>` catalogue, not skill listing
- **Activation:** `mcp__yesmem__activate_cap` MCP tool + `eval(result.code)` in the REPL
- **Portability:** Generic adapter layer (`cap_store`, `blob_put`) with provider mapping

## References

- Design decision: [ID:54523]
- CAP.md format specification: [ID:54333]
- Scanner design: [ID:54339]
- Skill-listing overhead: [ID:52067]
- Adapter pattern discussion: Session 2026-04-22
