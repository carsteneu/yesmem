# YesLoop — Autonomous Task Loop

> Spawn a visible agent that runs a complete development task end-to-end without interaction: analyze, plan with TDD, execute in an isolated worktree, verify with tests, finish with a PR.

## Overview

YesLoop is the autonomous execution layer on top of YesMem. Where a normal agent session is interactive (you chat, the agent responds, you correct, it tries again), a yesloop session is fire-and-forget: you describe the task once, the agent works alone, and you review the result at the end.

YesLoop exists because interactive babysitting does not scale. Long tasks drift, context rots, agents get stuck in loops, and the human becomes the bottleneck. YesLoop replaces the human-in-the-loop with structural guards: isolated worktrees, convergence gates, drift checks, verification phases, and token budgets.

## Cross-Backend Support

YesLoop runs on three agent backends, and that is the key difference from vendor-native autonomous pipelines:

| Backend | Binary | Models | Use case |
|---|---|---|---|
| `claude` | `claude` (Anthropic) | Claude Opus, Sonnet, Haiku | Anthropic API access, Claude-optimized tasks |
| `codex` | `codex` (OpenAI) | GPT-5.x, o4-mini, etc. | OpenAI API access, Codex-style implementation |
| `opencode` | `opencode` | Any OpenAI-compatible: Zai GLM-5.2, DeepSeek, Ollama, Mistral, local LLMs | Local LLMs, free models, non-Anthropic/non-OpenAI providers |

Bare model names auto-resolve against the auto-discovered provider map (e.g. `glm-5.2` → `zai-coding-plan/glm-5.2`), so callers do not need to know provider prefixes. Slash-qualified names (`zai-coding-plan/glm-5.2`) are passed through verbatim.

### Why this matters

Claude Code's `/loop`, `/goal`, and `/workflows` run on Claude only. Codex's workflows run on Codex only. YesLoop runs on all three — including fully open-source stacks like `opencode + GLM-5.2 + Ollama` with no Anthropic or OpenAI dependency.

This makes YesLoop the only autonomous pipeline that works for:

- **Mixed fleets** — Claude Opus plans, Codex implements, GLM-5.2 reviews (coordinated via `yesloopultra`)
- **FOSS-only setups** — `opencode` + local LLM, no paid API required
- **Provider flexibility** — switch backends per task without changing the pipeline tooling
- **Cross-provider agent dialogs** — agents of different backends exchange messages via `send_to` and `relay_agent`

## The 6-Phase Pipeline

Every yesloop run moves through the same six phases. The agent does this itself, no orchestration from your side.

### Phase 1: ANALYZE

The agent reads the task brief from the scratchpad and explores the codebase on its own. It uses the code intelligence tools (`search_code_index`, `get_file_symbols`, `get_code_snippet`, `graph_traverse`) to understand the affected files, calls, tests, and dependencies. Output: the agent knows what it is looking at.

### Phase 2: PLAN

The agent writes a concrete implementation plan with bite-sized steps, each independently verifiable. Stores the plan via `set_plan()` so it survives context collapse. The plan is persisted in the thread-scoped plan field and re-injected on every turn.

The plan phase applies the `KEIN OVERENGINEERING` rule: if a step looks complicated, the agent asks whether a boolean flag and ten lines would suffice. Anti-patterns to refuse: per-agent configuration, persistence layers for ephemeral state, abstract Strategy interfaces, stats/metrics export, retry mechanisms with exponential backoff.

### Phase 3: EXECUTE

The agent works through the plan steps one at a time. Each step is marked `in_progress` then `completed` via `todowrite`. Commits happen after each logical unit. Before each step, a drift check: am I still on the original goal?

Blocker resolution replaces user interaction:
- Known pattern: apply workaround, document in scratchpad
- Unknown pattern but low-risk: proceed with note
- Unknown pattern and risky: stop, post to scratchpad, set agent status to `blocked`
- Plan is wrong: stop, post what is wrong, set status to `blocked`

### Phase 4: VERIFY

The agent runs the full test suite and verifies the change matches the goal. Type checks, lint, golden path, edge cases. If verification fails, the agent loops back to Phase 3 with a corrected approach.

### Phase 5: REVIEW (Two-Stage: Self + Cold)

Code review is a work phase, not an advisory step. All findings must be fixed autonomously — no user feedback, no escalation for fixable issues.

**Stage 1 — Self-Review** against the plan from Phase 2. Check each requirement. Review operates on the full delta (`git diff origin/main`, committed + uncommitted).

Checklist:
- Plan alignment — every plan item implemented, no extras
- Code quality — error handling, naming, dead code, comments
- Architecture — fits existing patterns, scope discipline, no overengineering
- Testing — coverage adequate, edge cases, golden path + regressions
- Production readiness — logging, config, migrations, deploy

Output: Strengths, Issues (Critical / Important / Minor), Recommendations, Assessment (Yes / No).

**Stage 2 — Cold Review** with fresh context. Input is `git diff origin/main` + Phase 2 plan only, no prior exploration. Catches structural issues the self-review misses (see learning #71127 — independent review consistently finds 2-3 structural issues same-session review misses).

After fixes: loop back to Phase 4 VERIFY to re-run tests, then re-review changed files. Maximum 3 REVIEW → VERIFY cycles total (Convergence Gate).

Assessment "No" (unfixable architectural dead-end) → STOP, escalate via `send_to`: "REVIEW BLOCKED: <reasons>". This is the only valid reason to leave REVIEW without fixing.

### Phase 6: FINISH

Commit message follows repo style. Push the branch. Open a PR if configured. Post a `DONE` message to the orchestrator session via `send_to`. Resolve any unfinished tasks via `resolve_by_text`. Update the agent status to `completed`.

## Execution Modes

### TUI agent (default)

`/yesloop` spawns a visible agent in a new gnome-terminal window. You can watch it work in real time: phase transitions, file edits, tool calls, commits. The agent runs autonomously, no interaction expected.

### Inline mode

`/yesloop --inline` runs the pipeline in the current session without spawning a new terminal. Useful for short tasks or when you want the work to happen in your existing context.

### Scheduled mode

YesLoop can be triggered via `yesmem_schedule` with `mode='agent'`. The scheduler spawns an agent at cron intervals or one-shot. Useful for nightly refactors, dependency updates, or recurring maintenance tasks.

## Orchestrator Contract

When you (the orchestrator) spawn a yesloop agent via `yesmem_spawn_agent`, you follow a contract:

1. Create the worktree before the first edit, not after
2. Write the task to the scratchpad before spawning
3. Spawn the agent with `backend`, `model`, `project`, `section`, `work_dir` parameters
4. Wait 15 seconds for the TUI to load, then send a relay kick: `relay_agent(to=<agent-id>, content="Read scratchpad <section>. Then Read ~/.claude/skills/yesloop/SKILL.md for pipeline, worktree guardrail, beweislast. Start Phase 1 ANALYZE.")`
5. Monitor via `get_agent` (check `status`, `phase`, `stream_active`)
6. Receive `DONE` via `send_to` from the spawned agent

The contract is asymmetric: the orchestrator plans and reviews, the agent executes autonomously.

## Task Definition Format

Tasks live in the shared scratchpad under a section named after the worktree:

```
scratchpad_write(
  project="<project>",
  section="<worktree-name>",
  content="..."
)
```

A well-formed task brief contains:

- **Goal** in one sentence
- **Context** explaining why this is a problem
- **Hard constraints** that are not negotiable
- **Escalation triggers** that should prompt the agent to ask the user
- **What not to do** (negative scope)
- **DONE criteria** that define completion
- **Code entry points** where the agent should start reading
- **Learnings to know** with learning IDs relevant to the task

## Worktree Isolation

Every yesloop run works in its own git worktree under `.worktrees/<branch-name>`. The main working tree is never touched until the merge. This means:

- Multiple agents can run in parallel without collision
- A failed run can be discarded without cleanup
- The orchestrator's working tree stays clean for review
- Worktree removal is the cleanup primitive (one command, everything gone)

Create the worktree before the first edit:

```
git worktree add -b yesloop/<task-name> .worktrees/<task-name> origin/main
```

## Temp File Discipline

Autonomous agents need a reliable project-scoped temp directory. `/tmp/` is unreliable across sandbox/container contexts. Global paths collide between parallel agents.

Use `<worktree>/.yesmem/tmp/` instead. Project-local, always writable from any agent context, cleaned up with worktree removal. Ensure the directory exists at session start: `mkdir -p .yesmem/tmp`.

The directory is excluded from git via `.gitignore` and from public repo sync via `sync-public.sh`.

## State Management

YesLoop state is split across three layers:

### Thread-scoped plan (`set_plan` / `update_plan` / `get_plan`)

The current implementation plan. Survives context collapse, re-injected on every turn. The only context-loss-proof anchor for the active task.

### Per-agent state (`agents` table)

Phase, status, heartbeat, stream activity, token usage. Updated by the agent itself via `update_agent_status`. Visible to the orchestrator via `get_agent`.

### Shared scratchpad

Task definition, progress notes, blockers, DONE message. Shared between orchestrator and agent. Read at agent start, written throughout execution, read by orchestrator for review.

## Multi-Model Support

YesLoop agents can run on any backend that `yesmem_spawn_agent` supports:

- `claude` (Anthropic API directly)
- `opencode` (OpenCode TUI with any configured provider)
- `codex` (OpenAI Codex CLI)

Recommended model for code tasks: `zai-coding-plan/glm-5.2` via opencode backend. Other options: DeepSeek V4, Claude Sonnet, Claude Opus.

The orchestrator can spawn multiple agents on different models in parallel, each in its own worktree, each working on a slice of a larger task.

## Guards and Safety

YesLoop replaces human approval gates with structural guards:

### Convergence Gate

Before declaring DONE, the agent must show evidence: test output, type check results, diff summary. Claims without proof are rejected by the orchestrator on review.

### Drift Check

Before each step, the agent asks: am I still on the original goal? Side quests are parked, not pursued. The plan is the anchor.

### Token Budget

Each agent has a `token_budget` parameter. When exceeded, the agent freezes and reports. No runaway token spend.

### Max Runtime

Each agent has a `max_runtime` parameter (default 48h). Before freezing on max_runtime, the daemon checks if the PID is still alive via `isPIDAlive()`. Live PIDs are not frozen, only truly-dead ones. This eliminates false-positive freezes on long-running tasks.

### Heartbeat

Every agent sends a heartbeat every second. If the heartbeat goes stale for more than 10 minutes AND the PID is dead, the agent is marked frozen. Live PIDs with stale heartbeats (long bash calls blocking the heartbeat thread) are not frozen.

### Worktree Guard

The first thing the agent does is verify `pwd` returns the worktree path and `git branch` returns the expected branch. If not, the agent refuses to proceed. This prevents accidental main-branch edits.

## Failure Modes

### Agent gets stuck in a loop

Drift check catches it. Agent posts blocker to scratchpad, sets status to `blocked`. Orchestrator intervenes manually or revises the task.

### Agent produces wrong code

Phase 5 REVIEW catches it before DONE. The agent fixes the findings autonomously and loops back to Phase 4 VERIFY. Maximum 3 REVIEW → VERIFY cycles (Convergence Gate).

If REVIEW misses it, the orchestrator catches it on review. The worktree is discarded, the task is revised, a new agent is spawned.

### Agent runs out of tokens

Token budget gate freezes the agent. Orchestrator can raise the budget or split the task into smaller pieces.

### Agent crashes

Crash recovery via PID monitoring. The daemon detects dead PIDs and marks the agent frozen. The orchestrator can respawn with the same task definition from the scratchpad.

### Plan is wrong

Agent stops itself, posts what is wrong, sets status to `blocked`. Orchestrator revises the task brief.

## Phase-Completion Guarantee

YesLoop runs autonomously, so the system needs structural guards against silent failures — agents that claim DONE without finishing, skip Phase 5 Cold Review, or idle forever with a live process. Six defense-in-depth layers cover the known failure modes:

| Layer | Mechanism | Catches |
|---|---|---|
| 1 — Skill v3.2 | Structured phase blocks, DISMISSAL-PATTERN table, inline markers | LLM pattern mismatch |
| 2 — Idle-Self-Check | State machine in `yesloop_idle.go`, fires on `stream_active=false` for 10 min + live PID | Agent stalls with live process, no DONE claim (agent-234 pattern) |
| 3 — Done-Verify | State machine in `yesloop_done_verify.go`, fires on DONE-claim (Phase 6 header / `send_to DONE`) | Agent claims DONE but Phase 5 Cold Review silently skipped |
| 4 — DONE-Guard | `validatePhaseBlocks` regex on scratchpad when DONE is claimed | DONE claim with malformed phase blocks |
| 5 — Dead-Detection | `crashRecovery()` emits `DEAD_AGENT` escalation for crashed yesloop agents with partial phases | Agent crashes mid-pipeline |
| 6 — Hung-Detection | `detectHungAgents()` + `isPIDAlive` guard, 10-min heartbeat stale + dead PID → freeze | Agent process died but daemon has stale status |

Layers 2 and 3 walk the agent through a relay protocol before escalating:

**Layer 2 (Idle-Self-Check)** — when a yesloop agent's stream goes idle for 10 min while its PID is still alive, four states transition via relay:

```
WORKING → SELF_CHECK → REMARK_REQUEST → COMMIT_REQUEST → DONE
```

Relays ask the agent to prove all phases ran, mark each `[x]` in the scratchpad, then commit + `send_to`. After 2 re-fires without progress, the daemon escalates `DEAD_AGENT` to the orchestrator.

**Layer 3 (Done-Verify)** — when a yesloop agent emits a DONE-claim (Phase 6 header, `send_to ... DONE`, or `Phase 6/6` marker), a separate 5-min heartbeat tick asks the agent to **BEWEISEN** all phases ran — especially Phase 5 Cold Review:

```
NOT_DONE → VERIFY_REQUESTED → DONE_VERIFIED (or DEAD_AGENT_ESCALATION after 3 re-fires)
```

Transition to `DONE_VERIFIED` requires three things together: the literal `BEWEISEN` marker in the scratchpad, a `send_to orchestrator:` line, and all 6 phase blocks marked COMPLETE. Anything less triggers another relay.

### Scratchpad Markers

The automated layers read the agent's scratchpad section to detect progress:

- `### Phase N: NAME` headers with `**Status:** COMPLETE [x]` — counted by `CountCompletedPhases()`; Layer 2 transitions when all 6 are done
- `BEWEISEN` (word-boundary, case-insensitive) — signals the agent has verified its own work; Layer 3 requires it
- `send_to orchestrator:` — signals Phase 6 FINISH actually ran; both Layer 2 and Layer 3 check for it
- `^DONE:\s` (line-anchored) — Layer 3's trigger to start verifying

A scratchpad with `Status: COMPLETE` and `Phase 6 ✓` in prose but no structured headers fails the DONE-Guard (Layer 4) and never triggers Layer 3 — agents must use the template from the skill.

### No Auto-Deploy

YesLoop never auto-deploys or auto-merges. The agent does commit + `send_to`; the orchestrator decides whether to merge to main, deploy, and restart the daemon. Deploy is the human (or orchestrator) step because it kills the agent's own process — an agent that deploys is an agent that dies mid-DONE.

| Aspect | Interactive session | YesLoop |
|---|---|---|
| Human attention | Every few minutes | Once at start, once at end |
| Context drift | Common, hard to detect | Drift check every step |
| Long tasks | Token-expensive, error-prone | Bounded by plan, verified |
| Parallel work | Awkward | Native, one worktree per agent |
| Recovery from failure | Manual | Structural (worktree discard, respawn) |
| Cost control | Implicit | Explicit via token_budget |
| Phase-completion safety | None | 6 defense-in-depth layers |

## YesLoop vs Claude Code's /loop, /goal, and /workflows

The name is a coincidence, not a derivation. Claude Code ships three bundled features that overlap with YesLoop in different ways: `/loop` (timer-based polling inside one session), `/goal` (completion-condition loop inside one session), and `/workflows` (JS-script orchestrating many Claude subagents). YesLoop is neither, and all.

| Aspect | `/loop` | `/goal` | `/workflows` | YesLoop |
|---|---|---|---|---|
| Trigger | Time interval | Completion condition | User prompt → Claude writes JS script | Task brief in scratchpad, then fire-and-forget |
| Scope | Single session | Single session | Background runtime in one session | Isolated worktree + own agent process |
| Parallelism | None (session-bound) | None (session-bound) | Up to 16 concurrent subagents | Native, one agent per worktree |
| Backend | Claude only | Claude only | Claude only | **Claude, Codex, OpenCode (any LLM)** |
| Plan holder | Timer | Haiku evaluator | JS script | Agent itself (scratchpad + set_plan) |
| Pipeline | None (one prompt per tick) | None (any work that satisfies condition) | Free-form (script defines phases) | 6 phases: ANALYZE→PLAN→EXECUTE→VERIFY→REVIEW→FINISH |
| Completion check | Timer fires next tick | Small fast model judges condition after each turn | Script returns | 6 defense-in-depth layers |
| Failure recovery | Manual stop or 7-day expiry | Manual `/goal clear` | Resumable in same session | Crash recovery, dead-agent escalation, idle relay protocol, hung freeze |
| Use when | Poll CI, PR, deploy status | Continue until a test/build/queue condition holds | Orchestrate dozens of Claude subagents from a script | Complete development task end-to-end without interaction, on any backend |

Timer-only polling (the `/loop` use case) is a degenerate form of YesLoop. Completion-condition loops (the `/goal` use case) are the primary mode. Multi-agent orchestration (the `/workflows` use case) becomes `yesloopultra` (planned, see [yesdocs/plans/2026-06-12-yesloopultra-design.md](../yesdocs/plans/2026-06-12-yesloopultra-design.md)).

## Invocation

From an opencode/claude-code session:

```
/yesloop <task description>
```

The skill body at `~/.claude/skills/yesloop/SKILL.md` is the authority on the current pipeline. This document describes the stable interface; the skill describes the current implementation.

From an orchestrator session (programmatic):

```
yesmem_spawn_agent(
  backend="opencode",
  model="zai-coding-plan/glm-5.2",
  project="<project>",
  section="<worktree-name>",
  work_dir="<worktree-path>"
)
```

## See Also

- [Features — YesLoop](features/yesloop.md)
- [Features — Multi-Agent](features/multi-agent.md)
- [Features — Operations](features/operations.md)
- [MCP Tools Reference](mcp-tools-reference.md)
- [Caps vs Skills Rationale](caps-vs-skills-rationale.md)
