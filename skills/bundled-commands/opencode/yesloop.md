---
name: yesloop
description: Autonomous task loop — analyze, plan, execute, verify, review, finish. Runs as visible TUI agent in git worktree. Use when user says "yesloop", "loop", "mach das autonom". Supports --merge for auto-merge, --inline for current session.
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
6. Confirm: "Agent spawned in worktree — sichtbar im Terminal."

**Only run inline if:** user explicitly says `--inline` or task is < 2 min trivial.

**"Pipeline" includes exploratory work.** Writing tests, spiking an approach, "just quickly trying the fix on main to see if it works" — if you'd commit it, it belongs in the worktree. Create the worktree BEFORE the first edit, not after you've verified the fix works.

## Model selection (opencode backend)

`yesmem_spawn_agent(model=...)` accepts three forms, resolved by `resolveSpawnModel` in `internal/daemon/spawn_model.go`:

- **Empty** → falls back to `zai/deepseek-v4-pro` (the yesloop default, per learning #76240).
- **Slash-qualified** (e.g. `zai/glm-5.2`, `zai-coding-plan/glm-5.2`) → passed through verbatim.
- **Bare model name** (e.g. `glm-5.2`, `deepseek-v4-pro`) → resolved against the auto-discovered provider map (built from `~/.cache/opencode/models.json` + `~/.config/opencode/opencode.json`). The daemon prepends the matching providerID, e.g. `glm-5.2` becomes `zai/glm-5.2`.

**Coding-optimized variants** (any provider whose ID contains "coding", e.g. `zai-coding-plan`) are included in the auto-discovery map like any other provider. When multiple providers carry the same bare modelID, the alphabetically-first ProviderID wins deterministically; resolve ambiguity explicitly via `model="provider/model"`. Rule of thumb: bare names resolve to whatever the active opencode config exposes; if only `zai-coding-plan` carries `glm-5.2`, bare `glm-5.2` resolves to `zai-coding-plan/glm-5.2`.

Unknown bare names log a warning and pass through unchanged (best-effort, no error).

## Orchestrator Contract (what to prescribe vs. delegate)

When you spawn a yesloop agent, the scratchpad task defines the working relationship. Over-prescribing takes ANALYZE+PLAN away from the agent; under-prescribing sends it into orientation loops. Get the level right.

**Prescribe (orchestrator's job):**
- **Goal** in 1-2 sentences — what success looks like, not how to get there
- **Dense context** — facts, file paths, relevant learning IDs, what's already been tried, what failed. The agent starts cold; you don't.
- **Hard constraints** — schema-breaking changes, backfills, new dependencies, destructive ops, files off-limits
- **Escalation triggers** — decisions that belong to the user (product direction, API shape changes, breaking compatibility)

**Delegate (agent's job):**
- **ANALYZE** — reading code, grepping, finding the call sites, identifying edge cases
- **PLAN** — commit structure, file selection, test strategy, step ordering, milestone breakdown
- **EXECUTE/VERIFY/REVIEW** — the full 6-phase pipeline on the agent's plan, not yours

**Calibration test:** Before writing the scratchpad, ask: "If the agent came back with a plan, would I be surprised?" If yes for the right reasons (better approach), you prescribed right. If yes for wrong reasons (misunderstood goal), add context. If bored (matches what you'd write), over-prescribed — shorten.

## Temp file discipline

NEVER write to `/tmp/` or `~/.claude/yesmem/tmp/` in autonomous operations. `/tmp/` is unreliable across sandbox/container contexts (logs vanish, see #72938). Global paths collide when multiple agents run in parallel.

Use `<worktree>/.yesmem/tmp/` instead. Project-local, always writable from any agent context, cleaned up with worktree removal. `mkdir -p .yesmem/tmp` at session start.

## Execution Modes

**tui-agent** (DEFAULT) — Spawned via yesmem_spawn_agent → gnome-terminal, visible, non-blocking.
**inline** — User explicitly requested inline. Run in current context.
**scheduled** — User asked for recurring work. Use yesmem_schedule.

## Definition of DONE

DONE is a contract, not a feeling. Status is DONE only when ALL six phase sections exist in scratchpad and each carries `**Status:** COMPLETE`.

**Valid Status values per phase:**
- `IN PROGRESS` — actively working
- `COMPLETE` — phase finished, evidence present
- `BLOCKED — <reason>` — cannot proceed, needs orchestrator input

**Phase status update is mandatory at every transition:**
```
update_agent_status(phase="Phase N/6 NAME")
update_agent_status(phase="Phase N/6 NAME (milestone M/K)")   # EXECUTE with milestones
update_agent_status(phase="Phase N/6 NAME (blocked: <why>)")
```

Free-form string, kanonical format. Single source of truth for live progress.

**Orchestrator validates DONE:**
- DONE-guard (auto, every 30s) checks phase block compliance via regex
- Manual fallback: Read scratchpad section
- Check `### Phase 1` through `### Phase 6` headers exist
- Each has `**Status:** COMPLETE` (or `BLOCKED — <reason>` for genuinely blocked phases)
- Phase 5 Stage 2 has `task() dispatched: yes` or `REVIEW BLOCKED: <reason>`
- Phase 6 has `Deploy executed: yes` or `Deploy required: no`

Missing evidence → guard fires automatically (freeze + notify orchestrator).

## Automated DONE-Guard (v3)

Since yesmem v2.1.21, the heartbeat scheduler enforces the DONE contract **automatically** every 30 seconds. The DONE-guard (`internal/daemon/done_gate.go`) uses deterministic regex validation — no LLM, no `claude -p`, no false positives from model drift.

**What the guard checks:**

1. All 6 phase blocks (`### Phase 1` through `### Phase 6`) must be present in scratchpad
2. Each block must have `**Status:**` on its own line with valid value (COMPLETE|BLOCKED|IN PROGRESS)
3. Per-phase required fields (see Phase Pipeline below)
4. Phase 5 must contain `**Stage 2: Cold Review` subsection header AND `task() dispatched: yes|blocked`
5. Phase 6 must contain `send_to orchestrator:` and deploy evidence

**When the guard fires:**
- Agent is frozen with reason `DONE-GUARD: phase validation failed — <errors>`
- Orchestrator receives `⛔ DONE-GUARD:` notification via `send_to`
- Agent must fix phase blocks and be resumed

**Phases missing from scratchpad = still in progress.** The guard only escalates when all 6 phase blocks exist but their content is non-compliant.

### DISMISSAL-PATTERN

These patterns cause the guard to reject the DONE claim:

| Pattern | Detection | Why it fails |
|---|---|---|
| Status on same line as header | `### Phase N: → **Status:**` inline | Status must be on its own `**Status:**` line |
| Missing Stage 2 header | No `**Stage 2: Cold Review` in Phase 5 | Stage 2 is mandatory per Learning #75412 |
| Missing orchestrator notification | No `send_to orchestrator:` in Phase 6 | Orchestrator must be notified for DONE |
| Missing **Security:** field in Phase 5 | No `**Security:**` line in Phase 5 block | Security review (item 6) is mandatory per security-review skill integration |
| Partial fields in Phase 5 | No issue breakdown, no Subagent ID | Self-Review must be structured |
| No deploy evidence in Phase 6 | No `Deploy executed:` or `Deploy required:` | Must document deployment outcome |
| Missing status line | Phase block exists but no `**Status:**` | Every phase needs a status |

### Findings-Table

When the DONE-guard rejects a claim, its findings are structured as:

| Phase | Missing/Invalid Field | Detail |
|---|---|---|
| 5 | `**Stage 2: Cold Review` | required field not found in phase block |
| 6 | `send_to orchestrator:` | required field not found in phase block |

**DISMISSAL → DONE is rejected. Fix fields → resume agent.**

## Phase Pipeline

Each phase writes ONE structured block to scratchpad. Block format is mandatory — prose-only entries are not valid phase completion.

### CRITICAL — scratchpad project string

All `scratchpad_write` and `scratchpad_read` calls MUST use the `project` value returned by `whoami()`. Call `whoami()` once at Phase 1 start and use its `project` field verbatim for every scratchpad call afterwards. Do NOT guess, do NOT use the worktree basename, do NOT hardcode a short name. The orchestrator and the idle/done-verify state machines read from `agent.Project` (set by `yesmem_spawn_agent`); using a different string means your writes and reads land in a different scope and the orchestrator never sees your progress.

### Phase 1: ANALYZE
```
update_agent_status(phase="Phase 1/6 ANALYZE")

### Phase 1: ANALYZE
**Status:** COMPLETE
**Goal understood:** <1 sentence>
**Codebase explored:** <files/packages inspected>
**Constraints identified:** <list>
**Dense context from memory:** <learning IDs relevant, or "none">
**Risks:** <list>
**Open questions:** none (must be inferrable, no user questions)
```

Information gathering is automatic — never ask the user. Code questions → search_code_index, grep, graph_traverse. API/docs → docs_search, hybrid_search. Errors → deep_search. Unknown concepts → WebFetch or infer.

### Phase 2: PLAN
```
update_agent_status(phase="Phase 2/6 PLAN")

### Phase 2: PLAN
**Status:** COMPLETE
**Plan stored via set_plan:** yes
**Decisions resolved:** all — files, schema, API, blast radius
  - File selection: <which files, why not others>
  - Schema decisions: <data structures, interfaces, types>
  - API surface: <public interface, signature choices>
  - Blast radius: <what else could break, dependencies affected>
**Milestones:** N total (see criteria below)
  - Milestone 1: <name> (<step count> steps)
  - Milestone 2: <name> (<step count> steps)
**Files in scope:** <list>
**Test strategy:** <approach>
**Verification gates between milestones:** <compile/test/deploy checks>
```

**Milestone criteria (agent decides):**
- ≤5 steps: single milestone, no sub-structure needed
- 6-15 steps: 2-3 milestones à 3-6 steps recommended
- 16+ steps: 4-6 milestones à 3-5 steps (max ~6 per milestone for reset granularity)

Milestones are a recovery aid for context-collapse, not a bureaucracy. If the plan is small, skip them.

**KEIN OVERENGINEERING.** If a step looks complicated, ask: "Can I solve this with a boolean flag and 10 lines?" Usually yes. Resist: per-agent configuration, persistence layers, abstract Strategy interfaces, retry mechanisms with backoff. Prefer: global constants, single struct + map, simple if/return.

### Phase 3: EXECUTE
```
update_agent_status(phase="Phase 3/6 EXECUTE")
# or with milestones:
update_agent_status(phase="Phase 3/6 EXECUTE (milestone 1/3)")

### Phase 3: EXECUTE
**Status:** IN PROGRESS
**Plan items:** N total

#### Milestone 1: <name>
**Status:** COMPLETE
**Commits:** <hash> <msg>, <hash> <msg>
**Steps completed:** N/N
**todowrite synced:** yes

#### Milestone 2: <name>
**Status:** IN PROGRESS
**Current step:** N/M
**Last commit:** <hash> <msg>

**Total progress:** M/N milestones, X/Y steps
**Uncommitted work:** none | <list with reason>
```

**Per-milestone discipline:**
- `update_plan(completed=["milestone M"])` after each milestone finishes — persistence for collapse recovery
- `scratchpad_write` per milestone, not per step — controls bloat
- DRIFT CHECK before each milestone (not each step): re-read goal, compare scope

**Context-collapse recovery:** `get_plan()` → see current milestone → `update_agent_status` → read milestone block from scratchpad → continue.

**Commit cadence:** one commit per logical unit. Never leave uncommitted work across phase boundary without documenting why.

### Phase 4: VERIFY
```
update_agent_status(phase="Phase 4/6 VERIFY")

### Phase 4: VERIFY
**Status:** COMPLETE
**Tests run:** <command> → exit <code>, last 5 lines: <output>
**Lint/type-check:** <command> → <result>
**Build:** <command> → <binary mtime if applicable>
**Coverage gaps:** none | <list>
**VERIFY cycles used:** N/3
```

If issues found → fix and re-verify (max 3 cycles, see CONVERGENCE GATE).

### Phase 5: REVIEW (Two-Stage: Self + Cold)
```
update_agent_status(phase="Phase 5/6 REVIEW")

### Phase 5: REVIEW
**Status:** COMPLETE
**Stage 1: Self-Review**
- Strengths: <list with file:line refs>
- Issues: Critical (N) / Important (N) / Minor (N), with file:line + reasoning
- Recommendations: <list>
- Assessment: Yes | With fixes | No

**Stage 2: Cold Review via task()**
**task() dispatched:** yes | blocked — <error>
**Subagent ID:** <id>
**Findings:** <list or "none">
**Merged assessment:** Yes | With fixes | No — <reasoning>
**Fix commits:** <hashes or "none needed">

**Security:** <findings list with NEW/MODIFIED distinction per security-review skill, OR "none — diff reviewed, no findings", OR "skipped — diff is docs-only">

**REVIEW→VERIFY cycles used:** N/3
```

**Stage 1 — Self-Review** (catches mechanical issues): Get full delta `git diff origin/main` + `git log origin/main..HEAD --oneline`. Checklist: Plan alignment, Code quality, Architecture, Testing, Production readiness, **Security (item 6 — MANDATORY)**, Second-order effects ("if this ships, what happens next? trace 2+ levels"), Assumption surfacing ("what must be TRUE for this to work?").

**Stage 1 Item 6 — Security (MANDATORY):** INVOKE the `security-review` skill via the Skill tool when the diff contains ANY executable code (`.go/.py/.js/.ts/.tsx/.jsx/.java/.rs/.php/.rb`). Apply the NEW/MODIFIED doctrine: for NEW code (diff-added), fix ALL findings HIGH/MEDIUM/LOW; for MODIFIED code (existing function touched), fix issues the diff introduces and document pre-existing issues as OUT OF SCOPE with a Learning reference. Skip ONLY if the diff is docs/config/comments-only — then record `skipped — diff is docs-only` in `**Security:**`. Every finding line carries either a fix-commit-hash, a "not exploitable because X" note, or an OUT-OF-SCOPE annotation.

**Stage 2 — Cold Review via task()** (fresh eyes, catches architectural blind spots):
- Dispatch focused task()-subagent with code-reviewer template (superpowers requesting-code-review)
- Input: `git diff origin/main` + Phase 2 plan only (no exploration, for speed)
- **MANDATORY — Cold Review is NOT optional.** Phase 5 is only complete when Stage 2 has actually run. Empirically verified (2026-06-20, Learning #75412): agents skip Stage 2 silently if framed as additive. Required evidence: `task() dispatched: yes` + Subagent ID in scratchpad.
- **If task() truly fails:** status must be `REVIEW BLOCKED: task() unavailable — <error>`, NOT `DONE`. Orchestrator spawns separate TUI reviewer as fallback.

**Merging & Resolution:**
- Merge findings from both stages (deduplicate, preserve highest severity)
- **ALL findings must be fixed autonomously** — no user feedback, no escalation for fixable issues
- Critical + Important → fix immediately, max 3 cycles per issue
- Minor → fix if <2 min each, otherwise document and proceed
- After fixes → loop back to Phase 4 VERIFY, then re-review (max 3 REVIEW→VERIFY cycles total)
- Assessment "No" (unfixable) → STOP, escalate via send_to: "REVIEW BLOCKED: <reasons>"
- **Never leave fixable issues for the user** — review is a work phase, not advisory
- After `git show`/`git diff` always `Read` the current file — diffs show what changed, not what's there NOW

### Phase 6: FINISH
```
update_agent_status(phase="Phase 6/6 FINISH")

### Phase 6: FINISH
**Status:** COMPLETE
**Deploy required:** yes (binary/skill/cap/code change) | no (docs/config only)
**Deploy executed:** yes — <evidence: make build output, md5sum diff>
  | no — <REASON. Default: BLOCKED, escalate to orchestrator>
**PR created:** yes — <url> | no — <reason>
**Worktree:** kept (pending merge confirmation) | deleted
**send_to orchestrator:** yes — <timestamp>
**set_plan complete:** yes
```

- **Default: Create PR** from worktree branch to main
- **`--merge` flag:** merge PR after all checks pass (only if user requested)
- Without `--merge`: send_to caller_session: "DONE: <summary>. PR: <url>"
- **Do NOT delete worktree** — keep until user confirms merge

## Mini Example (filled Phase 3, others stubbed)

```
### Phase 1: ANALYZE
**Status:** COMPLETE
**Goal understood:** Add config.yaml read/write to set_config/get_config
**Codebase explored:** internal/daemon/handler_state.go, internal/config/config.go
**Constraints:** no breaking MCP API, proxy_state overrides must still work
**Risks:** concurrent writes, type coercion
**Open questions:** none

### Phase 2: PLAN
**Status:** COMPLETE
**Plan stored via set_plan:** yes
**Decisions resolved:** all
  - File selection: config.go, handler_state.go, cmd_config.go, main.go
  - Schema decisions: yaml config struct with Get/Set methods
  - API surface: Save/GetValue/SetValue on Config type
  - Blast radius: proxy_state override precedence, concurrent write
**Milestones:** 2 total
  - Milestone 1: config.go Save/GetValue + tests (4 steps)
  - Milestone 2: handler rewiring + CLI (3 steps)
**Files in scope:** internal/config/config.go, internal/daemon/handler_state.go, cmd_config.go, main.go
**Test strategy:** table-driven for coercion, integration for handler
**Verification gates:** go test ./internal/config/... between milestones

### Phase 3: EXECUTE
**Status:** COMPLETE
**Plan items:** 7 total

#### Milestone 1: config.go Save/GetValue + tests
**Status:** COMPLETE
**Commits:** a9ba499 feat(config): add Save/GetValue/SetValue + 11 tests
**Steps completed:** 4/4
**todowrite synced:** yes

#### Milestone 2: handler rewiring + CLI
**Status:** COMPLETE
**Commits:** a9ba499 feat(config): wire set/get_config to yaml, add CLI
**Steps completed:** 3/3

**Total progress:** 2/2 milestones, 7/7 steps
**Uncommitted work:** none

### Phase 4: VERIFY
**Status:** COMPLETE
**Tests run:** go test ./internal/config/... ./internal/daemon/... → exit 0
**Build:** go build ./... → success
**VERIFY cycles used:** 1/3

### Phase 5: REVIEW
**Status:** COMPLETE
**Stage 1:** Strengths: clean API, good test coverage
  Issues: Important (1) type coercion, Important (1) proxy_state dual-write
  Assessment: With fixes
**Stage 2:** task() dispatched: yes, Subagent ID: agent-234
  Findings: same 2 Important + 4 Minor
  Merged assessment: With fixes — fix both IMPORTANTs before merge
  Fix commits: cd2ba04 fix(config): proxy_state dual-write + type coercion
  **Security:** none — diff reviewed, no HIGH/MEDIUM/LOW findings

### Phase 6: FINISH
**Status:** COMPLETE
**Deploy required:** yes (Go code change)
**Deploy executed:** yes — make build → binary mtime 2026-06-20T00:40
**PR created:** no — merged directly per orchestrator (merge commit 27e35ca)
**send_to orchestrator:** yes — 2026-06-20T00:47
**set_plan complete:** yes
```

## Guardrails (Prevent Agent Drift)

### DRIFT CHECK — Before each MILESTONE (not each step)

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

| Pattern | Detection | Action |
|---|---|---|
| Edit-Test loop | Same test fails 3+ times with different fixes | **STOP.** scratchpad_write + send_to: "STUCK: <test> fails after 3 attempts." |
| Rewrite loop | Reverting your own changes and trying again | **STOP after 2 rewrites.** |
| Search loop | Searching for the same information repeatedly | Cache in scratchpad. After 3 empty searches, document as unknown. |
| Fix-then-break | Each fix breaks something else | **STOP after 2 cascades.** Code too coupled. |

**Hard limit:** No forward progress (no completed steps in todowrite) for 5 turns → escalate and stop.

### BEWEISLAST BEI SCRATCHPAD-CLAIMS

Before writing "DONE", "completed", "verified" to scratchpad, confirm the artifact matches the claim — not just that the command exited 0.

| Claim | Required independent check |
|---|---|
| "build succeeded" | `go build ./...` exit + binary exists with new mtime |
| "tests green" | show last 5 lines of test output, not just "PASS" |
| "deployed" | curl/GET against running service shows new version, OR md5sum live vs bundled exit 0 |
| "bundle rolled out" | `diff source installed && echo identical` (exit 0) |
| "commit pushed" | `git log origin/<branch> --oneline -1` shows the hash |
| "PR created" | `gh pr view <url>` exit 0 |
| "cold review ran" | task()-subagent ID present in scratchpad |
| "file edited" | `Read` the file (not just `git show`) |
| "DONE-guard passed" | `ValidatePhaseBlocks(content).Compliant == true` (auto-checked every 30s) |

**Rule:** if verification was skipped, say so explicitly: `<claim> (not independently verified)`. If run, paste observable evidence (one line is enough).

**Apply only to consequential claims:** build, deploy, bundle rollout, merge, migrate, config change, destructive ops. Routine todowrite step-completion does not need this.

## State Recovery (every wake-up)

Your state lives in yesmem, not context. On every wake-up:
1. `get_plan()` — restore active goal and progress
2. `scratchpad_read(project, section)` — restore detailed context
3. **`check_messages`** — poll for orchestrator messages (OpenCode has no push, DB-poll is the only reliable path)
4. Reconstruct current phase + milestone
5. `update_agent_status(phase="Phase N/6 NAME (milestone M/K)")` — re-assert current state
6. Continue where you left off

## TUI Agent Mode (You were spawned in a worktree)

You are a **subagent** — spawned via yesmem_spawn_agent. You run in an isolated git worktree.

### ⛔ WORKTREE GUARDRAIL — Execute BEFORE any file modification

On startup, before ANY other action:
```
1. pwd                           → must match worktree path from scratchpad
2. git rev-parse --show-toplevel → must be the worktree, NOT main repo
3. git branch --show-current     → must be yesloop/<task-slug>, NOT main
4. git status --short            → must be clean
```

**IF ANY CHECK FAILS:** STOP. Do not touch files. `scratchpad_write + send_to orchestrator: "⛔ WORKTREE GUARD FAILED: pwd=<actual>, branch=<actual>. Expected <expected>."` Wait for orchestrator.

**IF ALL PASS:** Identify via `whoami`, `scratchpad_read(project, section)`, `get_plan()`, `update_agent_status(phase="Phase 1/6 ANALYZE")`, then begin pipeline.

### Completion — MUST do all three:
1. `scratchpad_write(content="✅ DONE: <summary>. PR: <url>")` — final write with all 6 phase blocks COMPLETE
2. `send_to(target=<caller_session>, content="DONE: <summary>. PR: <url>")` — notify orchestrator (DB-stored, orchestrator polls)
3. `set_plan(...)` mark as completed

**NOTE:** `send_to` stores in DB but push delivery is unreliable for OpenCode targets. Orchestrator polls `check_messages`. Scratchpad is primary completion channel.

**Periodic polling:** Every 5 turns, `check_messages` for new instructions or cancellation.

## Decision Gates (Autonomous)

| Situation | Action |
|---|---|
| Simple fix, clear solution | Execute directly |
| 2-3 approaches, unclear best | Pick one, document rationale, proceed |
| Need to change scope | Document, proceed if minor; escalate if major |
| Tests fail repeatedly | Debug max 3 cycles → CONVERGENCE GATE |
| Irreversible action (force-push, drop table) | Pause, request confirmation |
| 3 consecutive ticks with nothing to do | End the loop, report idle |
| Scope creep detected | DRIFT CHECK, correct or escalate |
| Same approach failed 3x | CONVERGENCE GATE — stop and escalate |

## Communication

- **inline mode:** short progress updates to user between phases
- **TUI agent mode:** scratchpad_write for detailed state, send_to for completion
- **scheduled mode:** scratchpad_write for state, broadcast on completion
- Never ask "soll ich weitermachen?" — just continue unless blocked

## Automated Idle-Trigger

Layer 2 of the yesloop guarantee: **Idle Detection** for yesloop agents with a live PID but inactive SSE stream for >10 minutes. The daemon heartbeat runs an idle state machine (`checkYesloopIdle` in `internal/daemon/yesloop_idle.go`) that pushes agents through a 4-state escalation protocol.

### State Machine

| State | Condition | Action |
|---|---|---|
| 0 WORKING | stream_active=true or idle <10min | No action |
| 1 SELF_CHECK | stream_active=false for 10min, PID alive | Relay 1: ask agent to prove completion via scratchpad marker |
| 2 REMARK_REQUEST | PROVEN marker found | Relay 2: ask agent to mark all 6 phases as done |
| 3 COMMIT_REQUEST | 6 phases marked done | Relay 3: ask agent to commit and send_to orchestrator |
| 4 DONE | send_to evidence in scratchpad | Terminal — notify orchestrator |

### Relay Messages

The daemon sends these exact messages (metachar-free, no markdown, no backticks):

1. **State 0 → 1:** `Have you completed all 6 phases? If not do it now. For each phase prove you have done each, IF you have proven mark each phase in scratchpad with [x] showing it is done. MANDATORY: Make sure that you have also done phase 5 with all code reviews including Stage 2 cold review via task subagent. REVIEW BLOCKED without subagent trace is not acceptable. Mandatory: only mark as PROVEN if it IS proven.`
2. **State 1 → 2:** `Mark all 6 phases as done with x in scratchpad.`
3. **State 2 → 3:** `If 1 through 6 are ok commit and send_to to caller.`

### Scratchpad Markers

The idle checker reads the agent's scratchpad section to detect progress:

- **PROVEN** (case-insensitive): signals that the agent has verified all 6 phases are complete → triggers State 1 → 2
- **6x `**Status:** COMPLETE`**: when all 6 phase blocks are completed (detected by `CountCompletedPhases()`) → triggers State 2 → 3
- **send_to orchestrator:** evidence in scratchpad → triggers State 3 → 4

### Re-fire and Escalation

- Each state re-fires the relay after 90 seconds if no progress
- After 2 re-fires (3 total attempts), the agent is **frozen** as DEAD_AGENT
- The orchestrator (caller_session) is notified via send_to

### What triggers idle detection

- Section prefix: `yesloop-`
- Agent status: `running`
- PID: alive (`isPIDAlive()` returns true)
- Stream state: `stream_active` false for >10 consecutive minutes

## Automated Done-Verify (v3.2)

Layer 3 of the yesloop guarantee: **Done-Verify** for yesloop agents that emit a DONE-claim (Phase 6 header, `send_to ... DONE`, or `Phase 6/6` marker). The heartbeat runs a 4-state verify machine (`checkYesloopDoneVerify` in `internal/daemon/yesloop_done_verify.go`, called every 5 minutes) that asks the agent to **BEWEISEN** all 6 phases ran — especially Phase 5 Cold Review — before marking DONE.

### State Machine

| State | Condition | Action |
|---|---|---|
| 0 NOT_DONE | No DONE-claim in scratchpad | Not tracked, cron inactive |
| 1 VERIFY_REQUESTED | DONE-claim detected | Relay: "Hast du wirklich alle Phasen abgehakt..." |
| 2 DONE_VERIFIED | BEWEISEN marker + send_to orchestrator + 6 phases COMPLETE | Terminal — cron stops for this agent |
| 3 DEAD_AGENT_ESCALATION | 3 re-fires without progress | Freeze + notify orchestrator |

### DONE-Claim Indicators (any one triggers)

- `### Phase 6:` header (case-insensitive)
- `send_to ... DONE` pattern
- `Phase 6/6` marker
- `DONE: ` prefix in content

### Relay Message (metachar-free)

`Hast du wirklich alle Phasen abgehakt. Vor allem Phase 5 Code Review. Falls nicht bitte durchfuehren und BEWEISEN dass alle 5 Phasen durch sind und DANN Phase 6 Finish durchfuehren mit commit und send_to. KEIN auto-deploy.`

### Scratchpad Markers

The done-verify checker reads the agent's scratchpad section to detect progress:

- **DONE-claim** (any indicator above): triggers NOT_DONE → VERIFY_REQUESTED
- **BEWEISEN** (case-insensitive) + `send_to orchestrator:` line + 6 phases COMPLETE: triggers VERIFY_REQUESTED → DONE_VERIFIED
- Missing evidence after 3 re-fires: triggers VERIFY_REQUESTED → DEAD_AGENT_ESCALATION

### Re-fire and Escalation

- Re-fires the relay every 5 minutes if no progress
- After 3 re-fires (total 4 relay attempts), the agent is **frozen** as DEAD_AGENT
- The orchestrator (caller_session) is notified via send_to

### What triggers done-verify

- Section prefix: `yesloop-`
- Agent status: `running`
- Scratchpad content matches any DONE-claim indicator
- Agent not already in DONE_VERIFIED or DEAD_AGENT_ESCALATION state

### Boundary with other layers

- **Idle Detection (Layer 2)** triggers on stream inactivity; Done-Verify (Layer 3) triggers on DONE-claims. They are complementary and can both fire for the same agent.
- **DONE-Guard (Layer 3 regex validator)** freezes agents that claim DONE with malformed phase blocks. Done-Verify catches the case where phase blocks look valid but Phase 5 Cold Review was silently skipped — the agent must actively BEWEISEN it ran Stage 2.

## Anti-Patterns

- Do NOT write design documents unless the task is architectural
- Do NOT ask clarifying questions — infer from context, document assumptions
- Do NOT scope-creep beyond the given task — see DRIFT CHECK
- Do NOT run endlessly — 3 idle ticks = stop
- Do NOT modify agent config files (.claude/, SYSTEM.md, yesloop.md, etc.)
- Do NOT keep trying the same approach — if 3 attempts fail, approach is wrong
- **⛔ Do NOT work in main.** Always verify worktree before touching files.
- Do NOT report DONE with missing phase sections — report PARTIAL instead
- Do NOT report COMPLETE for a phase without `update_agent_status` having been called for that phase
