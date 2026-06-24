# YesLoop

> Spawn a visible agent that runs a complete development task end-to-end without interaction: analyze, plan with TDD, execute in an isolated worktree, verify with tests, finish with a PR.

## What it is

YesLoop is the autonomous execution layer on top of YesMem. A normal agent session is interactive (you chat, the agent responds, you correct, it tries again). A YesLoop session is fire-and-forget: you describe the task once, the agent works alone, you review the result at the end.

YesLoop exists because interactive babysitting does not scale. Long tasks drift, context rots, agents get stuck in loops, and the human becomes the bottleneck. YesLoop replaces the human-in-the-loop with structural guards.

## Cross-backend — the differentiator

YesLoop runs on three agent backends. Bare model names auto-resolve against the auto-discovered provider map.

| Backend | Binary | Models | Example |
|---|---|---|---|
| `claude` | `claude` | Claude Opus, Sonnet, Haiku | `spawn_agent(backend="claude")` |
| `codex` | `codex` | GPT-5.x, o4-mini | `spawn_agent(backend="codex")` |
| `opencode` | `opencode` | Any OpenAI-compatible: Zai GLM-5.2, DeepSeek, Ollama, Mistral, local LLMs | `spawn_agent(backend="opencode", model="glm-5.2")` |

Claude Code's `/loop`, `/goal`, and `/workflows` are Claude-only. Codex has no equivalent orchestration. YesLoop is the only autonomous pipeline that works on all three — including fully open-source stacks (`opencode` + GLM-5.2 + Ollama) with no Anthropic or OpenAI dependency.

### Backend-mix examples

Because YesLoop agents coordinate via scratchpad + `send_to` regardless of backend, you can mix backends per role:

- **Plan + Implement + Review split**: Claude Opus plans the task, Codex implements it, GLM-5.2 reviews the diff — three yesloop agents in three worktrees, coordinated via scratchpad sections.
- **Local-only dev**: `opencode` + local Ollama model runs the whole pipeline without any paid API key.
- **Cost-optimized**: GLM-5.2 (via `opencode`) for routine yesloops, Claude Opus for cold-review subagents — pick the best model per phase, not per pipeline.

Backend-mix orchestration across many agents is the planned `yesloopultra` feature.

## The 6-Phase Pipeline

Every YesLoop run moves through the same six phases. The agent does this itself, no orchestration from your side.

1. **ANALYZE** — read the task brief, explore the codebase via code-intelligence tools
2. **PLAN** — write a concrete implementation plan, store it via `set_plan()` so it survives context collapse
3. **EXECUTE** — work through plan steps one at a time with per-step drift checks, commit after each logical unit
4. **VERIFY** — full test suite + type checks + lint + golden path + edge cases; loops back to Phase 3 on failure
5. **REVIEW** — two-stage: inline self-review, then cold review via `task()` subagent. All findings must be fixed autonomously
6. **FINISH** — merge to main, push, deploy (if Go code changed), sync bundled skill to live skill, `send_to` orchestrator

## Phase-Completion Guarantee

Six defense-in-depth layers cover the known failure modes of autonomous agents:

| Layer | Catches |
|---|---|
| 1 — Skill v3.2 structured phase blocks | LLM pattern mismatch |
| 2 — Idle-Self-Check (`yesloop_idle.go`) | Agent stalls with live PID, no DONE claim |
| 3 — Done-Verify (`yesloop_done_verify.go`) | DONE claimed but Phase 5 Cold Review skipped |
| 4 — DONE-Guard (`done_gate.go`) | DONE claim with malformed phase blocks |
| 5 — Dead-Detection (`crashRecovery`) | Agent crashes mid-pipeline |
| 6 — Hung-Detection (`detectHungAgents`) | Heartbeat stale + dead PID |

Layers 2 and 3 walk the agent through a relay protocol before escalating. Layer 2 fires when the stream goes idle for 10 min with a live PID; Layer 3 fires on DONE-claims and requires a `BEWEISEN` marker + `send_to orchestrator:` line + 6 COMPLETE phase blocks to terminate.

No auto-deploy. The agent commits and sends `DONE`; the orchestrator decides whether to merge, deploy, and restart.

## How to invoke

From an opencode or claude-code session:

```
/yesloop <task description>
```

From an orchestrator session:

```
yesmem_spawn_agent(
  backend="opencode",
  model="zai-coding-plan/glm-5.2",
  project="<project>",
  section="yesloop-<task-name>",
  work_dir="<worktree-path>"
)
```

Bare model names like `glm-5.2` resolve against the auto-discovered provider map — you do not need to prefix unless multiple providers carry the same model.

## How to write a task brief

A well-formed brief contains: Goal (one sentence), Context (why this is a problem), Hard constraints, Escalation triggers, Negative scope, DONE criteria, Code entry points, Relevant learning IDs.

Write the brief to the scratchpad section before spawning. The spawn relay is metachar-free — no markdown, backticks, parens, `$`, or semicolons — because opencode's TUI interprets them as shell input. Complex briefings go in the scratchpad, the relay is just a trigger.

## Worktree isolation

Every YesLoop run works in its own git worktree under `.worktrees/<branch-name>`. The main working tree is never touched until the merge. Multiple agents run in parallel without collision; failed runs are discarded by worktree removal.

## State management

- **Thread-scoped plan** (`set_plan` / `update_plan` / `get_plan`) — current implementation plan, survives context collapse
- **Per-agent state** (`agents` table) — phase, status, heartbeat, stream activity, token usage
- **Shared scratchpad** — brief at start, progress markers during execution, final DONE report

Agent IDs are date-based (`agent-YYYYMMDD-NN`) and never recycled — removing an agent from the DB does not free its ID for reuse.

## When to use YesLoop vs an interactive session

| Task shape | Use |
|---|---|
| Well-scoped bug fix or feature, clear entry points, ≤1 day work | YesLoop |
| Exploratory debugging, unclear root cause | Interactive |
| Refactor with known target shape | YesLoop |
| Multi-file change touching many domains | Interactive or YesLoopultra |
| Repetitive migration across similar files | YesLoop |

## See Also

- [YesLoop (full documentation)](../yesloop.md)
- [Features — Multi-Agent](multi-agent.md)
- [MCP Tools Reference](../mcp-tools-reference.md)
- [Caps vs Skills Rationale](../caps-vs-skills-rationale.md)
