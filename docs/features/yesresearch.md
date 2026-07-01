# YesResearch

> Spawn an orchestrator agent that decomposes a research topic into clusters, dispatches parallel research agents, and integrates results into a cited wiki — autonomous, no babysitting.

## What it is

YesResearch is the research counterpart to YesLoop. Where YesLoop runs a coding task end-to-end (analyze → plan → execute → verify → review → finish), YesResearch runs a research task end-to-end: read a master plan, decompose into clusters, dispatch research agents, integrate results, deliver a cited wiki.

YesResearch exists because research work has different failure modes than coding:
- **Hallucination instead of bugs** — solved by mandatory inline citations, not tests
- **Source saturation, not convergence** — 3 empty searches = stop, not 3 failing test cycles
- **Long running, sparse progress** — research agents take 10-15 min/file; the orchestrator must be patient, not anxious
- **Multi-source synthesis, not incremental commits** — output is a wiki with cross-links, not a PR

## Two-tier architecture

```
User's session (setup only)
  │
  └─ yesmem_spawn_agent(orchestrator)  ──── TUI agent, owns the pipeline
       │
       ├─ READ       — master plan (## headings = clusters)
       ├─ DECOMPOSE  — 5-8 file groups per Bereichs-Agent
       ├─ DISPATCH   — yesmem_spawn_agent × ≤6 Bereichs-Agents (parallel TUI)
       ├─ MONITOR    — get_agent + scratchpad polling, stream_active as primary signal
       ├─ INTEGRATE  — cross-links, INDEX.md, citation audit, bibliography
       └─ DELIVER    — commit + scratchpad DONE
                │
                ▼
       Bereichs-Agent (per cluster, full TUI, max_runtime 2h default / 12h cap)
         ├─ CONTEXT — read master plan + cluster file list
         ├─ PLAN    — set_plan, per-file strategy
         ├─ EXECUTE — task(general) for fetch+search+write
         │           task(explore) for read-only citation review
         │           bash+curl+pdftotext for PDFs (webfetch cannot parse PDFs)
         ├─ VERIFY  — min 2 sources/file, every claim cited, conflicts dual
         ├─ COMMIT  — write files
         └─ DELIVER — scratchpad DONE + send_to orchestrator
```

Sawtooth cannot compress the master plan because the plan lives in the DB (scratchpad + set_plan), not in any single agent's context.

## How to invoke

From an opencode or claude-code session:

```
/yesresearch <topic> [path/to/master-plan.md]
```

The user's session only does setup: create worktree, write master plan, write orchestrator briefing to scratchpad, spawn the orchestrator agent. The orchestrator then owns all 6 phases and spawns its own Bereichs-Agents.

```
yesmem_spawn_agent(
  backend="opencode",
  model="zai-coding-plan/glm-5.2",
  project="<project>",
  section="yesresearch-<topic-slug>-orchestrator",
  work_dir="<worktree-path>"
)
```

## Master plan format

Plain Markdown. `##` headings define clusters, bullet points under each heading are file targets:

```markdown
# <Topic>

Success criteria: <what good output looks like>

## Cluster 1: <Name>
- file-1.md — <one-line description>
- file-2.md — <one-line description>

## Cluster 2: <Name>
- file-3.md — <one-line description>
```

The orchestrator parses `##` as cluster boundaries. Files land in `yesdocs/<topic>/wiki/<cluster-slug>/<file>.md`.

## Model rule (MANDATORY)

For z.ai/zhipuai coding-plan models, always use the full slash-qualified string:

- ✅ `zai-coding-plan/glm-5.2` — routes to Coding-Plan-Endpoint
- ❌ `zai/glm-5.2` — regular z.ai endpoint does not carry glm-5.2
- ❌ `glm-5.2` (bare) — filtered from auto-discovery, unreliable passthrough

Coding variants are excluded from the auto-discovered provider map upstream (`spawn_model.go:21-22`), so bare names do not resolve to the coding-plan endpoint. Always use `zai-coding-plan/<model>`.

## Citation rule (replaces Cold Review)

Research has no code review. The primary quality gate is sourcing.

- **Format:** `[Title](URL, accessed YYYY-MM-DD)` inline at the claim
- **Min 2 sources per file** (default; override via master plan)
- **Every factual claim needs a citation.** If unsourced: strike it, or mark `*[unkenntlich]*`
- **Conflicts:** present both positions, do not resolve
- **PDFs:** via `bash` + `curl -sL <url>` + `pdftotext` (webfetch cannot parse PDFs)

Citation check via `task(explore)` — read-only subagent, perfect for verification.

## Patience rule (MONITOR phase)

Research agents work slower than coding agents — often by an order of magnitude. Single file: 10-15 min. 5-file cluster: 30-90 min. Large cluster: 2-4h. This is normal, not a stall.

**Monitoring cadence:**
- `get_agent` every 5 min (NOT `list_agents` — get_agent returns full field set including `stream_active`)
- `scratchpad_read` every 15 min per Bereichs-Agent
- Polling faster does not make agents faster

**Primary liveness signal:** `stream_active` from `get_agent`. Set true by proxy at SSE-stream start, false at stream end . `last_activity_at` alone is unreliable — agents blocking on long tool calls may not update it for 10+ min while still working.

**Stalled criteria (all three):**
- `stream_active == false`
- No activity (turns, commits, word counts) for 20+ min
- `subagent_streams == 0` (no task() dispatches in flight)

Recovery: `relay_agent` kick, wait 10 min, respawn if still flat. Max 3 respawns per agent, then abort cluster.

## Output target structure

```
yesdocs/<topic>/wiki/
├── INDEX.md                          # Orchestrator-generated, navigation + status
├── <cluster-1-slug>/
│   └── <file>.md
├── <cluster-2-slug>/
│   └── <file>.md
└── 99-sources/  (or BIBLIOGRAPHY.md)
    └── bibliography.md               # Orchestrator-aggregated
```

## When to use YesResearch vs YesLoop

| Task shape | Use |
|---|---|
| Bug fix, feature, refactor with clear entry points | YesLoop |
| Multi-file research with master plan, citations needed | YesResearch |
| Single-question lookup | Interactive (no skill needed) |
| Mixed research + code (e.g. "evaluate library X, then integrate") | YesResearch then YesLoop |
| Recurring monitor/topic watch | yesmem_schedule with headless mode |

## First live test

Memory-Market Wiki (2026-06-30 → 2026-07-01):
- 16 files across 4 clusters (Exploration, Technik, Teilnehmer, Anwendung)
- 301 source references, min 10/file (requirement was 2)
- 3.041 lines, 9 commits, 64 min total runtime
- 2 waves: 1st wave stopped externally, 2nd wave completed clean except 1 Bad-Gateway retry loop (re-spawned with narrow scope)
- Output: `yesdocs/memory-market/wiki/` with INDEX.md + BIBLIOGRAPHY.md

## See Also

- [YesLoop](yesloop.md) — coding counterpart
- [Features — Multi-Agent](multi-agent.md)
- [MCP Tools Reference](../mcp-tools-reference.md)
- [Caps vs Skills Rationale](../caps-vs-skills-rationale.md)
