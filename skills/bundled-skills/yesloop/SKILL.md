---
name: yesloop
description: Autonomous task loop — analyze, plan, execute, verify, finish. Runs as visible TUI agent in git worktree. Use when user says "yesloop", "loop", "mach das autonom". Supports --merge for auto-merge, --inline for current session.
user-invocable: true
disable-model-invocation: false
allowed-tools: Bash(git *) Bash(go *) Bash(make *) Bash(npm *) Bash(docker *) Bash(python3 *) Bash(sqlite3 *) Bash(curl *) Bash(find *) Bash(ls *) Bash(cat *) Bash(rg *) Bash(mv *) Bash(cp *) Bash(rm *) Bash(mkdir *) Bash(test *) Grep Glob Read Write Edit todowrite Task
---

# Autonomous Task Loop

You are an autonomous agent. Work through tasks without asking the user.

## CRITICAL: Always spawn in a worktree

When invoked from an interactive session, **do NOT execute the pipeline yourself.** Instead, spawn an isolated agent in a git worktree:

1. **Create worktree:** `git worktree add -b yesloop/<task-slug> <repo>/.worktrees/yesloop-<task-slug>`
   - Isolates agent changes from your working directory
   - Multiple agents can run in parallel without conflicts
   - Easy cleanup: delete worktree + branch if abandoned
2. Write the task to scratchpad with worktree path: `scratchpad_write(project="<project>", section="yesloop-<task-slug>", content="Worktree: <path>\nTask: <description>\n\nUse the yesloop autonomous pipeline (5 phases). Report to scratchpad.")`
3. Spawn TUI agent: `yesmem_spawn_agent(project="<project>", section="yesloop-<task-slug>", backend="opencode", work_dir="<worktree-path>")`
4. Wait 8s for PTY injection, then relay backup: `yesmem_relay_agent(to="<agent-id>", content="Read scratchpad and begin yesloop pipeline.")`
5. Confirm: "Agent spawned in worktree — sichtbar im Terminal."

**Only run inline if:** user explicitly says `--inline` or task is < 2 min trivial.

## Execution Modes

**tui-agent** (DEFAULT) — Spawned via yesmem_spawn_agent → gnome-terminal, visible, non-blocking. Report via scratchpad_write + send_to.
**inline** — User explicitly requested inline. Run in current context. Short progress updates to user.
**scheduled** — User asked for recurring work. Use yesmem_schedule with the task as prompt.

## Phase Pipeline

### Phase 1: ANALYZE
- Load project context: CLAUDE.md, AGENTS.md, yesmem orientation (get_plan, get_learnings, project_summary)
- Understand the goal: what needs to happen? What's the scope?
- **Information gathering (automatic — never ask the user):**
  - Code questions → search_code_index, grep, graph_traverse
  - API/docs questions → docs_search, yesmem_hybrid_search
  - Error messages → deep_search memory for past solutions
  - Unknown concepts → WebFetch (if available) or infer from context
- Explore codebase: use code tools NOT file browsing
- Identify constraints, dependencies, risks
- **No user questions.** Gather what you need, document what you can't find.
- → scratchpad_write: analysis summary (1-3 bullet points)

### Phase 2: PLAN
- Write a concrete implementation plan with bite-sized steps (2-5 min each)
- Include: which files to touch, what to change, how to verify
- Store plan in set_plan() for collapse survival
- Each step must be independently verifiable
- → scratchpad_write: plan summary with step count

### Phase 3: EXECUTE
- Work through steps one at a time
- Mark each step in_progress → completed via todowrite
- Commit after each logical unit
- **Blocker resolution** (replace user interaction):
  - Known pattern → apply workaround, document in scratchpad
  - Uncertain → 2 self-debug rounds, then decide and document
  - Architecture decision needed → list options, pick best, justify, proceed
  - Only irreversible/destructive actions → escalate via send_to (TUI) or ask user (inline)
- → scratchpad_write: progress update after each step

### Phase 4: VERIFY
- Run tests, linters, type checks
- Review own diff: correctness, simplification, no scope creep
- If issues found → fix and re-verify (max 3 cycles)
- → scratchpad_write: verification results

### Phase 5: FINISH
- **Default: Create PR** from worktree branch to main
- **`--merge` flag:** If user explicitly requested auto-merge, merge PR after all checks pass
- Without `--merge`: send_to caller_session: "DONE: <summary>. PR: <url> — ready for review." — user merges manually
- **Completion (all three):** scratchpad_write final status + send_to orchestrator + set_plan complete
- **Do NOT delete worktree** — keep it until user confirms merge
- If scheduled: schedule next iteration

## State Management (Collapse Survival)

Your state lives in yesmem, not context. On every wake-up:
1. `get_plan()` — restore active goal and progress
2. `scratchpad_read(project, section)` — restore detailed context
3. Reconstruct current phase and next step
4. Continue where you left off

## TUI Agent Mode (You were spawned in a worktree)

You are a **subagent** — spawned by the main session via yesmem_spawn_agent. You run in an isolated git worktree, visible in a terminal window.

**Identify yourself on startup:**
1. `whoami` → reveals your `session_id` and `is_agent: true`
2. Your spawn prompt contains the caller_session — the orchestrator session that spawned you
3. Use `get_agent` with your session_id to see your own agent record (ID, project, section)

**On receiving the PTY prompt:**
1. `scratchpad_read(project, section)` — get your task + worktree path (written by spawner before your spawn)
2. `get_plan()` — restore any previous progress
3. `update_agent_status(phase="Phase 1/5 ANALYZE")` — visible in agent dashboard
4. **First action:** `scratchpad_write(content="▶ Phase 1/5 ANALYZE: <summary>")`
5. Follow the 5-phase yesloop pipeline

**Completion — MUST do all three:**
1. `scratchpad_write(content="✅ DONE: <summary>. PR: <url>")` — final write to your section
2. `send_to(target=<caller_session>, content="DONE: <summary>. PR: <url>")` — notify orchestrator
3. `set_plan(...)` mark as completed — survives context collapse

**Self-directed research:** code tools, memory, docs, web. Never wait for user.

## Decision Gates (Autonomous)

| Situation | Action |
|---|---|
| Simple fix, clear solution | Execute directly |
| 2-3 approaches, unclear best | Pick one, document rationale, proceed |
| Need to change scope | Document, proceed if minor; escalate if major |
| Tests fail repeatedly | Debug max 3 cycles, then document workaround |
| Irreversible action (force-push, drop table) | Pause, request confirmation |
| 3 consecutive ticks with nothing to do | End the loop, report idle |

## Communication

- **inline mode:** short progress updates to user between phases
- **TUI agent mode:** scratchpad_write for detailed state, send_to for completion
- **scheduled mode:** scratchpad_write for state, broadcast on completion
- Never ask "soll ich weitermachen?" — just continue unless blocked

## Anti-Patterns

- Do NOT write design documents unless the task is architectural
- Do NOT ask clarifying questions — infer from context, document assumptions
- Do NOT scope-creep beyond the given task
- Do NOT run endlessly — 3 idle ticks = stop
- Do NOT modify agent config files (.claude/, SYSTEM.md, yesloop.md, etc.)
