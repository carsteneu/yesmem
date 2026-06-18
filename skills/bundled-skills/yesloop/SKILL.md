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
2. Write the task to scratchpad with worktree path: `scratchpad_write(project="<project>", section="yesloop-<task-slug>", content="Worktree: <path>\nTask: <description>\n\nUse the yesloop autonomous pipeline (6 phases). Report to scratchpad.")`
3. Spawn TUI agent: `yesmem_spawn_agent(project="<project>", section="yesloop-<task-slug>", backend="opencode", work_dir="<worktree-path>")`
4. Wait 15s for opencode TUI to load + PTY injection to deliver the startup prompt
5. **Relay kick (backup):** `yesmem_relay_agent(to="<agent-id>", content="Read scratchpad and begin yesloop pipeline.")` — backup if PTY is slow
5. Confirm: "Agent spawned in worktree — sichtbar im Terminal."

**Only run inline if:** user explicitly says `--inline` or task is < 2 min trivial.

**"Pipeline" includes exploratory work.** Writing tests, spiking an approach, "just quickly trying the fix on main to see if it works" — if you'd commit it, it belongs in the worktree. Create the worktree BEFORE the first edit, not after you've verified the fix works. Migrating uncommitted changes from main into a worktree mid-session (stash → worktree add → stash apply) is error-prone and avoidable.

## Orchestrator Contract (what to prescribe vs. delegate)

When you spawn a yesloop agent, the scratchpad task defines the working relationship. Over-prescribing takes ANALYZE+PLAN away from the agent; under-prescribing sends it into orientation loops. Get the level right.

**Prescribe (orchestrator's job):**
- **Goal** in 1-2 sentences — what success looks like, not how to get there
- **Dense context** — facts, file paths, relevant learning IDs, what's already been tried, what failed. The agent starts cold; you don't.
- **Hard constraints** — schema-breaking changes, backfills, new dependencies, destructive ops, files off-limits. "No backfill in this PR" is a constraint; "here's the migration SQL" is over-prescription.
- **Escalation triggers** — decisions that belong to the user (product direction, API shape changes, breaking compatibility). The agent stops and asks, doesn't decide alone.

**Delegate (agent's job):**
- **ANALYZE** — reading code, grepping, finding the call sites, identifying edge cases. Even if you know the answer, let the agent verify. Its plan will be better grounded.
- **PLAN** — commit structure, file selection, test strategy, step ordering. A scratchpad that reads like an implementation guide ("step 1: edit foo.go line 42, step 2: ...") has crossed the line. Shorten to goals + constraints.
- **EXECUTE/VERIFY/REVIEW** — the full 6-phase pipeline runs on the agent's plan, not yours.

**Anti-pattern — the implementation-guide scratchpad:** If your scratchpad reads like a step-by-step tutorial, you've done ANALYZE+PLAN for the agent. The agent will either follow it mechanically (losing the chance to catch what you missed) or ignore it (wasting your work). Write the goal + context + constraints, then stop. Let ANALYZE+PLAN be the agent's first deliverable.

**Anti-pattern — bare goal:** Conversely, "fix the model_used bug" with no context sends the agent into 200k-token orientation spirals (see learning #53077, #73503). The agent earns its keep by planning, but it can't plan without your prior context.

**Calibration test:** Before writing the scratchpad, ask: "If the agent came back with a plan, would I be surprised?" If yes for the right reasons (it found a better approach), you prescribed the right amount. If yes for the wrong reasons (it misunderstood the goal), add context. If you'd be bored by its plan (because it matches what you'd have written), you over-prescribed — shorten and re-test.

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
- **Before each step:** quick DRIFT CHECK — am I still on the original goal?
- **Blocker resolution** (replace user interaction):
  - Known pattern → apply workaround, document in scratchpad
  - Uncertain → 2 self-debug rounds, then decide and document
  - Architecture decision needed → list options, pick best, justify, proceed
  - Only irreversible/destructive actions → escalate via send_to (TUI) or ask user (inline)
- → scratchpad_write: progress update after each step

### Phase 4: VERIFY
- Run tests, linters, type checks
- Review own diff: correctness, simplification, no scope creep
- If issues found → fix and re-verify (**max 3 cycles** — see CONVERGENCE GATE)
- → scratchpad_write: verification results

### Phase 5: REVIEW (Two-Stage: Self + Cold)

**Stage 1 — Self-Review** (catches mechanical issues: dead code, error handling, style):
- **Get the full delta** (committed + uncommitted): `git diff origin/main` + `git log origin/main..HEAD --oneline`
- **Review against the plan** from Phase 2. Check each requirement.
- **Checklist** (derived from superpowers code-review structure):
  - **Plan alignment:** Does implementation match the plan? All requirements covered? Deviations justified?
  - **Code quality:** Clean separation of concerns? Proper error handling? Type safety? Edge cases handled? No dead code?
  - **Architecture:** Sound design? Integrates cleanly with surrounding code? No unnecessary abstraction?
  - **Testing:** Tests verify real behavior? Edge cases covered? All tests passing? (VERIFY already ran them — check coverage gaps)
  - **Production readiness:** Migration needed? Backward compat? Documentation? Obvious bugs?
  - **Second-order effects:** If this change ships, what happens NEXT? Trace 2+ levels of consequence — downstream consumers, migration ripple, feedback loops. Ask: "Then what? And after that?"
  - **Assumption surfacing:** What must be TRUE for this change to work? List load-bearing assumptions. Which are verified, which are unverified but treated as fact? Flag the risky ones before proceeding.
- **Output format** (scratchpad_write as structured block):
  - **Strengths** — what's well done, specific file:line references
  - **Issues** — Critical (bugs, security, data loss) / Important (architecture, missing features, test gaps) / Minor (style, polish). Prefix: [2nd-order] / [assumption] when applicable.
  - **Recommendations** — improvements for code quality, architecture, or process
  - **Assessment** — Ready to merge? [Yes | With fixes | No] + 1-2 sentence reasoning

**Stage 2 — Cold Review via task()** (fresh eyes, catches architectural blind spots, scope creep):
- Dispatch a focused task()-subagent with the code-reviewer template (derived from superpowers requesting-code-review):
  - **Description:** "Review code changes"
  - **Input:** git diff origin/main + Phase 2 plan (no exploration — diff + plan only for speed)
  - **Checklist:** Plan alignment, Code quality, Architecture, Testing, Production readiness, Second-order effects, Assumption surfacing + End-to-End perspective
  - **Calibration:** Categorize by actual severity. Not everything is Critical. Acknowledge strengths first.
  - **Output format:** Strengths, Issues (Critical/Important/Minor with file:line references + reasoning), Recommendations, Assessment
- **Fallback:** If task() fails or times out → Self-Review result stands. The cold review is additive, not a blocker.

**Merging & Resolution:**
- Merge findings from both stages into a unified issues list (deduplicate, preserve highest severity)
- **Unified GATES:**
  - **ALL findings must be fixed autonomously** — no user feedback, no escalation for fixable issues
  - Critical + Important issues → fix immediately, max 3 cycles per issue (CONVERGENCE GATE)
  - Minor issues → fix if < 2 min each, otherwise document and proceed
  - After fixes → loop back to Phase 4 VERIFY to re-run tests, then re-review changed files (max 3 REVIEW→VERIFY cycles total, see CONVERGENCE GATE)
  - Assessment "No" (unfixable, architectural dead-end) → **STOP**, escalate via send_to: "REVIEW BLOCKED: <reasons>"
  - Assessment "With fixes" → apply all fixes, re-run VERIFY to confirm tests pass, re-review final diff, then proceed
  - **Never leave fixable issues for the user** — the review is a work phase, not an advisory step
- **Git diff verification:** After reading `git show` or `git diff` output, ALWAYS `Read` the current file to confirm — diffs show what changed, not what's there NOW
- → scratchpad_write: unified review result in structured format (note which stage found each issue)
- → update_agent_status(phase="Phase 5/6 REVIEW: <assessment>")

### Phase 6: FINISH
- **Default: Create PR** from worktree branch to main
- **`--merge` flag:** If user explicitly requested auto-merge, merge PR after all checks pass
- Without `--merge`: send_to caller_session: "DONE: <summary>. PR: <url> — ready for review." — user merges manually
- **Completion (all three):** scratchpad_write final status + send_to orchestrator + set_plan complete
- **Do NOT delete worktree** — keep it until user confirms merge
- If scheduled: schedule next iteration

## Guardrails (Prevent Agent Drift)

### DRIFT CHECK — Every phase transition

Before moving to the next phase, verify you're still on track:

1. Re-read the original goal from `scratchpad_read(project, section)` or `get_plan()`
2. Compare current state against original scope:
   - **Still on track?** → proceed
   - **Minor drift?** → document in scratchpad, correct course, proceed
   - **Major divergence?** → **STOP.** scratchpad_write("⚠️ DRIFT: <what changed>") + send_to orchestrator: "DRIFT: <details>. Continue or abort?"

What counts as drift:
- Touching files outside the original scope (scope creep)
- Solving a different problem than the one given
- Adding features not requested ("while I'm here, I'll also...")
- Changing architecture without justification

### CONVERGENCE GATE — When progress stalls

If you're cycling without making progress, recognize it and stop:

| Pattern | Detection | Action |
|---|---|---|
| Edit-Test loop | Same test fails 3+ times with different fixes | **STOP.** You don't understand the problem. scratchpad_write + send_to: "STUCK: <test> fails after 3 attempts. Need human guidance." |
| Rewrite loop | Reverting your own changes and trying again | **STOP after 2 rewrites.** The approach is wrong, not the implementation. |
| Search loop | Searching for the same information repeatedly | Cache results in scratchpad. If found nothing after 3 searches, document as unknown and move on. |
| Fix-then-break | Each fix breaks something else | **STOP after 2 cascades.** The code is too coupled for safe autonomous changes. |

**Hard limit:** If you make no forward progress (no completed steps in todowrite) for 5 consecutive turns → escalate and stop.

### BEWEISLAST BEI SCRATCHPAD-CLAIMS

Before writing "DONE", "completed", or "verified" to scratchpad or `send_to`, confirm the artifact matches the claim — not just that the command exited 0.

| Claim | Required independent check |
|---|---|
| "build succeeded" | `go build ./...` exit + binary exists with new mtime |
| "tests green" | show last 5 lines of test output, not just "PASS" |
| "deployed" | curl/GET against the running service shows new version |
| "bundle rolled out" | `diff source installed && echo identical` (exit 0) |
| "commit pushed" | `git log origin/<branch> --oneline -1` shows the hash |
| "file edited" | `Read` the file (not just `git show` on the diff — see gotcha #66986) |

**Why:** command-exit-0 is not state-change-propagated. Operations with hidden prerequisites (binary rebuild before embed-rollout via `yesmem migrate`, daemon restart before config reload, prefix-cache cooldown before measurement) silently no-op if the prerequisite is missing. The agent that ran `yesmem migrate` without prior `make deploy` saw an exit-0 and claimed "bundle rolled out" — but the SHA256 had nothing to compare against because the embedded binary was stale. The scratchpad said DONE; the live skill file was unchanged.

**Rule:** if verification was skipped (not just run and passing), say so explicitly in the scratchpad entry: `<claim> (not independently verified)`. The orchestrator reading the scratchpad knows to treat the claim as reported, not confirmed. If verification was run, paste the observable evidence (diff exit code, command output snippet, file mtime) into the scratchpad entry — one line is enough.

**Apply only to consequential claims:** build, deploy, bundle rollout, merge, migrate, config change, destructive ops. Routine step-completion in todowrite does not need this. Over-applying pollutes the scratchpad. The cost of a false DONE is paid by the orchestrator or the user downstream; the cost of explicit "not verified" is one extra line.



Your state lives in yesmem, not context. On every wake-up:
1. `get_plan()` — restore active goal and progress
2. `scratchpad_read(project, section)` — restore detailed context
3. **`check_messages`** — poll for messages from orchestrator or other agents (openCode has no push, DB-poll is the only reliable path)
4. Reconstruct current phase and next step
5. Continue where you left off

## TUI Agent Mode (You were spawned in a worktree)

You are a **subagent** — spawned by the main session via yesmem_spawn_agent. You run in an isolated git worktree, visible in a terminal window.

### ⛔ WORKTREE GUARDRAIL — Execute BEFORE any file modification

**You MUST verify you are in the correct worktree before touching ANY file.** Agents have been observed working in main instead of their worktree — this is a hard failure.

On startup, before ANY other action:

```
1. pwd                           → must match the worktree path from scratchpad
2. git rev-parse --show-toplevel → must be the worktree, NOT the main repo
3. git branch --show-current     → must be yesloop/<task-slug>, NOT main
4. git status --short            → must be clean (no uncommitted changes from main)
```

**IF ANY CHECK FAILS:**
- **STOP immediately.** Do not create, edit, or delete any file.
- scratchpad_write + send_to orchestrator: "⛔ WORKTREE GUARD FAILED: pwd=<actual>, branch=<actual>. Expected worktree at <expected>. I will NOT proceed until this is fixed."
- **Do NOT proceed.** Wait for the orchestrator to respawn you correctly.

**IF ALL CHECKS PASS:**
- scratchpad_write: "✅ Worktree verified: <path>, branch yesloop/<slug>, clean."
- Proceed with the pipeline.

**Identify yourself on startup:**
1. `whoami` → reveals your `session_id` and `is_agent: true`
2. Your spawn prompt contains the caller_session — the orchestrator session that spawned you
3. Use `get_agent` with your session_id to see your own agent record (ID, project, section)

**On receiving the PTY prompt:**
1. `scratchpad_read(project, section)` — get your task + worktree path (written by spawner before your spawn)
2. `get_plan()` — restore any previous progress
3. `update_agent_status(phase="Phase 1/6 ANALYZE")` — visible in agent dashboard
4. **First action:** `scratchpad_write(content="▶ Phase 1/6 ANALYZE: <summary>")`
5. Follow the 6-phase yesloop pipeline

**Completion — MUST do all three:**
1. `scratchpad_write(content="✅ DONE: <summary>. PR: <url>")` — final write to your section
2. `send_to(target=<caller_session>, content="DONE: <summary>. PR: <url>")` — notify orchestrator (stored in DB, orchestrator must poll)
3. `set_plan(...)` mark as completed — survives context collapse

**NOTE:** `send_to` stores the message in the DB but push delivery is unreliable for OpenCode targets. The orchestrator polls `check_messages` periodically. Use scratchpad as the primary completion channel — it's DB-based and always readable.

**Periodic polling:** Every 5 turns, call `check_messages` to see if the orchestrator sent new instructions or cancellation.

**Self-directed research:** code tools, memory, docs, web. Never wait for user.

## Decision Gates (Autonomous)

| Situation | Action |
|---|---|
| Simple fix, clear solution | Execute directly |
| 2-3 approaches, unclear best | Pick one, document rationale, proceed |
| Need to change scope | Document, proceed if minor; escalate if major |
| Tests fail repeatedly | Debug max 3 cycles → if still failing, activate CONVERGENCE GATE |
| Irreversible action (force-push, drop table) | Pause, request confirmation |
| 3 consecutive ticks with nothing to do | End the loop, report idle |
| Scope creep detected | Activate DRIFT CHECK, correct course or escalate |
| Same approach failed 3x | Activate CONVERGENCE GATE — stop and escalate |

## Communication

- **inline mode:** short progress updates to user between phases
- **TUI agent mode:** scratchpad_write for detailed state, send_to for completion
- **scheduled mode:** scratchpad_write for state, broadcast on completion
- Never ask "soll ich weitermachen?" — just continue unless blocked

## Anti-Patterns

- Do NOT write design documents unless the task is architectural
- Do NOT ask clarifying questions — infer from context, document assumptions
- Do NOT scope-creep beyond the given task — see DRIFT CHECK
- Do NOT run endlessly — 3 idle ticks = stop. See CONVERGENCE GATE for stall detection
- Do NOT modify agent config files (.claude/, SYSTEM.md, yesloop.md, etc.)
- Do NOT keep trying the same approach — if 3 attempts fail, the approach is wrong, not the implementation
- **⛔ Do NOT work in main.** Always verify worktree before touching files — see WORKTREE GUARDRAIL. Working in main is a hard failure that contaminates the user's repo.
