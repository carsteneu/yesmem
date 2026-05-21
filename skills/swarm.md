# /swarm — Multi-Agent Orchestration

> YesMem Skill: Autonomous agent swarms for research, analysis, and content creation.

## Invocation

### Guided Mode (Beginners)
```
/swarm
```
Interactively asks questions before the swarm starts:
1. **Task** — What needs to be done?
2. **Agents** — How many sub-agents, which perspectives/roles? Make sensible suggestions!
3. **Output** — Format (Markdown, JSON, Code, presentations e.g. with reveal.js or other office documents) AND where should the result go?
4. **Limits** — Token budget, max runtime per agent?

### Direct Mode (Experienced)
```
/swarm "3 Agents: banana economical, health, historical → Markdown Report"
```
Parses the task directly and spawns immediately.

---

## Roles

| Role | Identifier | Task |
|------|-----------|------|
| **Main Chat** | Agent 1 | User interface, starts/monitors the swarm |
| **Orchestrator** | Agent A | Coordinates sub-agents, collects results, spawns report agent |
| **Sub-Agents** | Agent B, C, D... | Execute sub-tasks (research, analysis, etc.) |
| **Report Agent** | Last Agent | Creates final artifact from all partial results |

---

## Protocol

### 1. Project Setup (Agent 1)

```
1. Choose project name (e.g. "fruit-report")
2. Determine working directory (see working directory modes)
3. Write task to scratchpad:
   scratchpad_write(project="{project}", section="task", content=...)
4. Spawn orchestrator:
   spawn_agent(project="{project}", section="orchestrator",
               caller_session=MY_SESSION, work_dir="{directory}")
5. DONE — return control to user (Fire & Forget)
```

**Fire & Forget is the default.** Agent 1 (main chat) does NOT wait for the swarm. After spawning, control returns immediately to the user. The orchestrator works fully autonomously. When the swarm finishes, a `send_to` notification arrives at Agent 1 — the user sees it on the next turn.

Optionally the user can check status at any time:
- `list_agents(project="{project}")` — which agents are still running?
- `scratchpad_list(project="{project}")` — what results are available?

- MCP permissions are auto-configured
- All sub-agents inherit the orchestrator's working directory

### Working Directory Modes

| Mode | When | work_dir |
|------|------|----------|
| **New Project** | Research, reports, standalone artifacts | `~/projects/{project}/` (auto-created) |
| **Existing Project** | Implement feature, build API, change code | Current project directory (e.g. `/home/user/projects/myapp/`) |
| **Custom** | Special cases | Any path via `work_dir` parameter |

**In Guided Mode** the skill asks:
```
Where should the work happen?
  1. New project directory (~/projects/{name}/) — for reports, research
  2. Here (current directory) — for features, code changes (Recommended when in project)
  3. Other path...
```

**In Direct Mode:**
```
/swarm "Feature: REST API for user management → Code in current project"
/swarm (dir:~/other/path) "Analyze log files → JSON Report"
```

**Important when working in existing project:**
- Agents create feature branches before changes
- Code agents use existing project structure (no new top-level directories)
- Tests are co-written (TDD if required by project CLAUDE.md)
- Results as commits, not loose files

### 2. Orchestrator Workflow (Agent A)

```
0. whoami() — get own session ID (ALWAYS first!)
1. scratchpad_read(project, section="task") — read task
2. For each sub-agent:
   a. scratchpad_write(project, section="task-{name}", content=...) — write task
   b. spawn_agent(project, section="{name}", caller_session=MY_SESSION)
3. Wait for send_to from all sub-agents
4. Read results from scratchpad
5. Stop finished sub-agents: stop_agent(project, to="{name}")
6. Write report task + spawn report agent
7. Wait for final result
8. Stop report agent
9. send_to(caller_session) — notify main chat, then **passively wait for stop_agent()** — NO ACK, no further action
```

### 3. Sub-Agent Workflow (Agent B, C, ...)

```
0. whoami() — get own session ID (for send_to callbacks)
1. scratchpad_read(project, section="task-{my-name}") — read task
2. Execute task (WebSearch, analysis, code, etc.)
3. Store result structured:
   scratchpad_write(project, section="result-{my-name}", content=...)
4. send_to(caller_session) — notify orchestrator: "Done"
5. Wait (orchestrator stops me)
```

### 4. Report Agent Workflow (Agent D)

```
1. scratchpad_read(project, section="task-report") — read task + sources
2. Read all result sections
3. Create final artifact
4. Save as FILE in project directory:
   Write(~/projects/{project}/{filename}.{format})
5. Additionally to scratchpad:
   scratchpad_write(project, section="final-report", content=...)
6. send_to(caller_session) — notify orchestrator
```

---

## Result Formats

| Format | Extension | When to use |
|--------|-----------|-------------|
| **Markdown** | `.md` | Reports, analysis, documentation |
| **JSON** | `.json` | Structured data, API responses |
| **Code** | `.go`, `.py`, etc. | Implementations, scripts |
| **HTML** | `.html` | Web content, presentations |

The user defines the format upfront. The report agent adheres to it.

---

## DAG Mode (Execution Dependencies)

Not all agents can start in parallel. Sometimes agent C needs the results of A and B before starting:

```
Planner → Research (parallel) → Implementer → Reviewer
        → Design   (parallel) ↗
```

Research and Design start in parallel after Planner. Implementer waits until BOTH are done. Reviewer waits on Implementer.

### Implementation: Emergent via Broker (no code needed)

The orchestrator writes the execution plan to scratchpad:

```
scratchpad_write(project, section="execution-order", content="""
## Execution Order
1. research + design (parallel, start immediately)
2. implement (waits for DONE from research AND design)
3. review (waits for DONE from implement)
""")
```

Every agent reads the plan on startup, sees its prerequisites, and waits via send_to for the corresponding DONE signals. **No code change needed** — purely prompt-based.

### Why emergent instead of static?

- Uses the existing heartbeat broker (message delivery)
- More dynamic than a static DAG — agent can decide at runtime whether it really needs to wait
- No `depends_on` field needed in DB
- Debugging via `list_agents()` + scratchpad: see which agent is waiting on whom

### Optional Fallback: Explicit Dependencies

For deterministic workflows the user can specify dependencies on invocation:

```
/swarm --project yesmem \
  --tasks "research,design,implement,review" \
  --depends "implement:research+design, review:implement"
```

The orchestrator parses the dependencies and writes them as `execution-order` to scratchpad. Agents behave identically — the only difference is who writes the plan (user vs. orchestrator).

---

## Reliable Message Delivery (Daemon-Enforced)

Delivery of `send_to` messages is **guaranteed** — not behavioral, but daemon-enforced.

**Flow:**
1. `send_to()` stores message in DB → returns `message_id`
2. Heartbeat (every 10s) fetches all `delivered=0` messages for running agents
3. Socket-Inject → on success: `delivered=1`, `delivered_at` set
4. On failure: `delivery_retries++`, next heartbeat cycle retries
5. After **5 failures**: `delivery_failed=1`, sender is notified:
   `"DELIVERY_FAILED: message to {section} could not be delivered after 5 attempts."`

**For the orchestrator this means:**
- No manual retry logic needed — daemon handles it
- On `DELIVERY_FAILED`: agent is likely dead → crash recovery engages in parallel
- `message_id` from `send_to` response can be used for tracking

---

## Scratchpad — The Shared Noteboard

The scratchpad is the central communication channel between all agents. It is a persistent key-value structure per project, organized in named sections.

**How it works:**
- Each section has a name (e.g. `task-economy`), an owner (writer's session ID), and arbitrary text content
- Sections are readable by ALL agents in the same project — no access control
- `scratchpad_write()` creates or overwrites a section (upsert)
- `scratchpad_read()` reads one or all sections
- `scratchpad_list()` shows all sections with size and timestamp
- `scratchpad_delete()` deletes a section or the entire project

**When scratchpad, when file?**
- **Scratchpad** for coordination: tasks, status updates, intermediate results, short texts
- **Files in project directory** for final artifacts: reports, code, presentations, everything the user ultimately receives

**Important:** The scratchpad is not a filesystem. It is a transient message board. After swarm completion the sections can be deleted — final results exist as files in the project directory.

---

## Communication

### Scratchpad Sections (Convention)

| Section | Writer | Content |
|---------|--------|---------|
| `task` | Agent 1 | Overall task for orchestrator |
| `task-{name}` | Orchestrator A | Sub-task for sub-agent |
| `result-{name}` | Sub-Agent | Research result |
| `task-report` | Orchestrator A | Task for report agent |
| `final-report` | Report Agent | Final report (copy) |
| `{agent-name}` | Any Agent | Status updates |

### Notifications (send_to + ACK)

Messages between agents go via `send_to()`. The heartbeat relays them every 2 seconds to the recipient.

**CRITICAL: Every send_to / relay_agent content MUST end with `\n`.** Without trailing newline, the prompt stays in the tmux input line and is never submitted. The agent stops responding to further pushes. Always: `relay_agent(to, "instruction\n")`.

**Protocol:**
1. Sender: `send_to(target=RECIPIENT_SESSION, content="RESULT: result-economy done\n")`
2. Heartbeat relays message to recipient terminal
3. Recipient reads, processes, confirms: `send_to(target=SENDER_SESSION, content="ACK: result-economy received\n")`

**Rules:**
- Every send_to message that expects an action MUST be answered with ACK
- Prefix convention: `RESULT:`, `STATUS:`, `ERROR:`, `ACK:`
- If no ACK after 60s: resend (max 2 retries)
- Status updates (informational only) do not need ACK
- **`ACK:` messages are NEVER confirmed** — no ACK on an ACK, never
- **Messages from main chat (Agent 1) never trigger an ACK** — main chat sends no ACK, and the orchestrator does not respond to it; after the DONE signal the orchestrator passively waits for `stop_agent()`

**Example Flow:**
```
B → A: "RESULT: result-economy done, 3.8KB"
A → B: "ACK: result-economy received"
A calls stop_agent(B)
```

---

## Patience — The Orchestrator's Most Important Trait

Agents need time. WebSearch, analysis, report creation — this takes minutes, not seconds. The orchestrator (Agent A) MUST be patient.

**Rules:**
- **No premature conclusions.** If a sub-agent hasn't delivered after 60 seconds, that does NOT mean it's stuck. It's working.
- **Don't check in before 3 minutes have passed.** Only after 3 minutes without any scratchpad activity OR send_to may the orchestrator follow up.
- **Check timestamps, don't estimate.** The `created_at` and `heartbeat_at` fields in `list_agents()` show real runtime. Don't use your own perception as a measure.
- **No abort out of impatience.** Only intervene on real errors (crash, freeze, timeout) — not because "it's taking long".
- **Work in parallel.** While B and C are researching, A can already prepare the report task or write status updates to scratchpad.

**Typical Runtimes:**
| Task | Expected Duration |
|------|-------------------|
| WebSearch + summary | 1–3 minutes |
| Code analysis | 2–5 minutes |
| Report creation from sources | 1–2 minutes |
| Complex multi-source research | 3–5 minutes |

---

## Proactive Download

Agents may — and should — proactively download relevant documents, sources, and materials to the project directory when useful for the result.

**Allowed:**
- Save web pages as reference (`WebFetch` → file)
- Store research sources as a source collection
- Save intermediate results, raw data, statistics as files
- Images, diagrams, charts when relevant to the report

**Convention:**
```
~/projects/{project}/
├── sources/          ← Source documents, downloaded references
├── data/             ← Raw data, statistics, JSON
├── assets/           ← Images, diagrams, media
└── {report}.{format} ← Final artifact
```

**Rules:**
- Create subdirectories independently when needed
- Descriptive filenames: `sources/fairtrade-banana-statistics-2024.md`, not `source1.txt`
- Don't download sensitive data or credentials
- Reference downloaded sources in the final report

---

## Lifecycle Management

### Cleanup Obligation

The orchestrator is responsible for cleanup:

1. Sub-agent reports "done" → orchestrator calls `stop_agent(to="{name}")`
2. Report agent reports "done" → orchestrator calls `stop_agent(to="report-writer")`
3. Orchestrator terminates itself last (or is stopped by main chat)

**No zombie agents.** Every started agent is explicitly stopped.

### Crash Recovery (Automatic)

The daemon monitors all running agents automatically via health check (every 30s PID check). **No agent needs to trigger this.**

**Flow on crash:**
1. Health check detects dead process (PID no longer exists)
2. **Immediate quarantine**: All learnings from the crashed session are isolated (`quarantine_session`) — prevents contamination via briefing and hybrid_search
3. **Scratchpad taint**: Result sections of the agent are marked with `[TAINTED]` prefix — other agents immediately see the data is unreliable
4. Daemon cleans up socket files, sets `status=crashed`
5. **Auto-retry** with clean session (new session_id, no contaminated briefing)
6. After **3 failed attempts**: `status=failed`, caller receives crash context:
   - Agent runtime
   - Last scratchpad status (if available)
   - Message: `"FAILED: Agent 'X' crashed after 3 attempts. Runtime: 2m15s."`

**Why quarantine before retry?**
Agents share state via yesmem — learnings, briefing, scratchpad. Without quarantine:
- The retry gets the briefing with the crashed session's learnings → same error → crash loop
- Other agents find poisoned learnings via hybrid_search → contamination spreads
- Scratchpad sections may be incomplete → report agent works with broken data

**What the orchestrator must do:**
- On `FAILED` message, decide: **Skip** (continue without this part) or **Abort** (`stop_all_agents(project)`)
- Retries happen automatically — the orchestrator does NOT need to handle respawns
- The orchestrator should NOT try to respawn a crashed agent itself

### Emergency Abort

On critical errors the orchestrator or main chat can immediately terminate the entire swarm:

```
stop_all_agents(project="{project}")
```

Stops ALL running, frozen, and spawning agents in the project. Sends `/exit` to each, cleans up sockets and DB entries. After this the project is clean.

### Freeze Handling

- Agent gets frozen (budget/runtime exceeded) → orchestrator checks
- Enough results present → `stop_agent()`, continue
- Too few results → `resume_agent()` with increased budget, or skip

---

## Limits (Defaults)

| Parameter | Default | Description |
|-----------|---------|-------------|
| `max_runtime` | 30m | Max runtime per agent |
| `max_turns` | 300 | Max interactions per agent |
| `max_depth` | 3 | Max spawn depth (A→B→C) |
| `token_budget` | 500000 | Max tokens (input+output) per agent |
| `model` | (inherit) | Model per agent (see model selection) |

Overridable via `spawn_agent(token_budget=..., model=...)` or config.

### Backend Selection (Claude vs Codex vs Opencode)

Sub-agents can use different backends:

```
spawn_agent(project="{project}", section="research", backend="opencode", model="deepseek-v4-pro")
```

| Backend | CLI | Strengths | Limitations |
|---------|-----|-----------|-------------|
| `claude` (default) | `claude` | YesMem proxy integration, prompt cache, full MCP access | Only Anthropic models (sonnet, opus, haiku). NEVER use with DeepSeek — silent failure (0 turns). |
| `codex` | `codex` | DeepSeek models, different provider, second opinion | No prompt cache. No proxy channel inject. |
| `opencode` | `opencode` | Same capabilities as codex, different binary name | No prompt cache. No proxy channel inject. |

**Codex/Opencode agents communicate exclusively via MCP tools** (scratchpad, send_to, remember) — not via proxy injection. YesMem is registered as MCP server.

**Important with DeepSeek models:** Always `backend: "opencode"` or `backend: "codex"` + `model: "deepseek-v4-pro"`. Never `backend: "claude"` with DeepSeek — the `claude` binary doesn't know the endpoint.

### Model Selection

Not every agent needs the most expensive model. **Agent A (orchestrator) decides** which model fits each sub-agent best — based on the budget strategy the user specifies.

**Budget Strategies:**

| Strategy | Orchestrator A | Sub-Agents | Report Agent D |
|----------|---------------|------------|----------------|
| **Frugal** | Sonnet | Haiku | Sonnet |
| **Balanced** (Default) | Opus | Sonnet | Opus |
| **Quality** | Opus | Opus | Opus |

Agent A may deviate from the strategy if the task requires it — e.g. upgrade a single sub-agent to Opus if research is complex, or downgrade to Haiku if it's just simple data extraction.

**In Guided Mode** the skill asks:
```
Budget strategy?
  1. Frugal — Haiku where possible, Sonnet for core
  2. Balanced — Sonnet default, Opus for report (Recommended)
  3. Quality — Opus everywhere
```

**In Direct Mode** optional inline:
```
/swarm (frugal) "3 Agents: banana economical, health, historical → Markdown"
```
Without specification, "Balanced" applies.

**Available models:** `opus`, `sonnet`, `haiku`

---

## Examples

### Research Swarm
```
/swarm "Research e-mobility: Agent B tech, Agent C market, Agent D policy → Markdown Report"
```

### Code Review Swarm
```
/swarm "Review auth module: Agent B security audit, Agent C performance, Agent D best practices → JSON Report"
```

### Content Swarm
```
/swarm "Blog post about AI in medicine: Agent B fact research, Agent C expert quotes, Agent D writes article → HTML"
```

---

## Anti-Patterns

- **No agent spawns itself** — only the orchestrator decides on retries
- **No direct agent-to-agent communication** — always via scratchpad + send_to to orchestrator
- **No results only in scratchpad** — final artifacts ALWAYS as file in project directory
- **No swarm without orchestrator** — even with 1 sub-agent everything goes through A
- **No orchestrator doing research itself** — A only coordinates, never works on content
