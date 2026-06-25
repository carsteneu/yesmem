# Multi-Agent System

## 1. Multi-Agent Communication & Memory Safety

YesMem enables multiple Claude Code sessions to communicate and share long-term memory safely.

### 22.1 Agent-to-Agent Messaging

Direct messaging between Claude Code sessions via Channel system:

| Tool | Parameters | Description |
|------|-----------|-------------|
| `send_to` | `target` (session ID), `content`, `msg_type?` (command\|response\|ack\|status) | Send message to another session |
| `broadcast` | `content`, `project` | Send message to all sessions on a project |

### 22.2 Message Delivery (Proxy-Based)

Messages are delivered via the proxy's think-reminder injection system:

```
Claude Code → API Request → Proxy extracts session_id from metadata.user_id
                          → Proxy uses session_id directly as threadID
                          → Proxy calls check_channel(sessionID, project)
                          → Channel content injected as <channel> tag in last user message
                          → Proxy calls check_broadcasts(sessionID, project)
                          → Broadcast content injected alongside channel messages
                          → API forwarded to Anthropic
```

**Key mechanisms:**
- **Session ID extraction:** `metadata.user_id` in every API request contains `{"device_id":"...","account_uuid":"...","session_id":"uuid"}`. No hook dependency — the proxy extracts it directly from the request (`extractSessionID()` in `proxy_helpers.go`)
- **Thread ID:** Session UUID from `metadata.user_id` is used directly as threadID. No mapping layer — MCP and Proxy share the same key. Fallback to SHA256-hash for requests without metadata (Codex, legacy).
- **Injection format:** `<channel source="yesmem-dialog">` tags (matches Claude Code's Channel format for forward compatibility)
- **Idle state:** Active dialog with no new messages shows `[KEINE NEUEN NACHRICHTEN]`
- **Session ID display:** `DEINE_SESSION_ID: uuid` injected in every proxy think-reminder so Claude always knows its own identity

### 22.3 Polling (CronCreate)

The proxy can only inject content when a request passes through. For idle sessions, the proxy provides a best-effort polling mechanism via Claude Code's CronCreate:

- **Limitation:** CronCreate depends on Claude remembering to start it. If the session doesn't actively run the CronCreate command, polling stops — this is a behavioral limitation, not a technical bug. Polling is unreliable for multi-hour idle intervals.
- When active, dialog invitations include a DIREKTIVE instructing Claude to start a CronCreate (`*/1 * * * *`)
- CronCreate fires a prompt → triggers API request → proxy injects pending messages
- The CronCreate prompt checks for `📨 DIALOG` blocks in the context, NOT via MCP check_messages (avoids echo issues)

### 22.4 Session Identity Resolution

Three-tier resolution for mapping tool calls to sessions:

| Priority | Method | Source | Reliability |
|----------|--------|--------|-------------|
| 1 | Direct `session_id` parameter | Proxy passes to daemon | 100% |
| 2 | Direct `_session_id` parameter | MCP server passes to daemon | 100% |
| 3 | PID reverse-lookup | `_caller_pid` → pidMap | High (fails after daemon restart) |
| 4 | `activeSessionID` fallback | Last registered session | Low (wrong with concurrent sessions) |

**PID persistence:** `hook-think` writes PID→session_id files to `$dataDir/sessions/$PID` on every `UserPromptSubmit`. Survives daemon restarts — MCP server can read PID files on startup.

### 22.5 Broadcast (1:n)

One agent sends a message to all sessions on the same project:

- **Dedicated `agent_broadcasts` table** — project column, `read_by` tracking per session
- **`filepath.Base()` matching** — handles both full paths and short project names
- **24h TTL** — broadcasts auto-expire, no accumulation
- **Auto-broadcast:** Gotchas and high-importance decisions (`importance ≥ 4`) are automatically broadcast to all project sessions when saved via `remember()`

### 22.6 Memory Safety (Multi-Agent Mitigations)

When multiple agents share the same long-term memory, specific protections prevent knowledge corruption:

| Risk | Mitigation | Implementation |
|------|-----------|----------------|
| **Dialog content floods learnings** | Dialog-Extract-Block | `PreFilterMessages` detects dialog injection markers (`send_to`, `Dialog-Partner`, `BROADCAST`) and skips both injection + assistant response |
| **Duplicate learnings across agents** | Cross-session dedup | `BigramJaccard` in pre-admission checks against last 50 learnings of the project. Agent-role aware: same topic from different roles = kept (divergence), same topic from same role = deduped |
| **Conflicting learnings** | Conflict detection | Pre-admission logs cross-agent divergence when similar content (TokenSimilarity ≥ 0.5) comes from different `agent_role`s — both learnings kept, conflict logged |
| **Associative context pollution** | Agent-role scoping | `hybrid_search` enriched results include `agent_role` field. Score-boost infrastructure ready for role-based filtering when agents set their role |
| **Persona drift** | Base persona only | Persona directive loaded as base (`user_id=default`). Describes the **user**, not Claude's role. No role-overlays — subagents get role context via their prompt |
| **No accountability** | Learning lineage | `dialog_id` on learnings tracks which dialog context a learning originated from. `agent_role` tracks which type of agent created it |
| **Echo in dialogs** | Sender filter | `check_messages` SQL: `sender != forSession`. Session ID must match exactly — `activeSessionID` fallback disabled for concurrent sessions |

### 22.7 Agent Roles

Sessions and learnings carry an `agent_role` field (e.g., `code`, `marketing`, `design`, `debug`, `review`). This enables:

- **Conflict detection:** Same topic from different roles is divergence (kept), not duplication
- **Associative context:** Role-matching learnings score higher in injection budget
- **Accountability:** Every learning traces back to which role produced it

Note: The Persona system describes the **user** (traits, preferences, expertise), not Claude's role. Subagents get their role via the Agent tool prompt. Role-persona overlays were removed as a design error — the `agent_role` field is purely for provenance tracking and conflict detection.

### 22.8 Channel-Ready Architecture (Future)

The dialog system is designed for seamless migration to Claude Code Channels when available:

- **Current:** Proxy-based injection with `<channel source="yesmem-dialog">` tags + CronCreate polling
- **Future:** MCP Channel server (`channel/index.mjs`) sends `notifications/claude/channel` push notifications
- **Channel requirements (Research Preview):** `--dangerously-load-development-channels` flag + claude.ai login (not API keys)
- **Channel advantages:** Push as User-Turn (not content block), idle-only delivery (buffered), no polling needed
- **Migration path:** Replace proxy injection with channel notification push — same daemon, same DB, same tools

### 22.9 Design Principles

- **`remember()` is the broadcast** — explicit saves surface via associative context to all agents automatically
- **Dialog is for questions, not knowledge transfer** — use `remember()` to persist, dialog to discuss
- **No auto-extract from dialogs** — only explicit `remember()` from dialog context becomes a learning (prevents agents from burning false info into each other)
- **Proxy over hooks** — proxy sees every request with correct session_id from metadata; hooks have timing issues and daemon restart fragility
- **Shared Scratchpad** — structured whiteboard for n:n collaboration. Each agent writes its own section (`scratchpad_write`) and reads its own section each turn (`scratchpad_read`); the orchestrator reads the full document for cross-agent visibility. CRUD via MCP tools: `scratchpad_write`, `scratchpad_read`, `scratchpad_list`, `scratchpad_delete`.

### 22.10 Agent Orchestrator (Daemon-Managed Agents)

Full lifecycle management for sub-agents spawned as PTY subprocesses:

| Tool | Parameters | Description |
|------|-----------|-------------|
| `spawn_agent` | `project`, `section`, `backend?`, `caller_session?`, `token_budget?`, `work_dir?`, `max_turns?`, `model?` | Spawn a new agent process with PTY bridge |
| `list_agents` | `project?` | List all agents with status, PID, heartbeat |
| `get_agent` | `to`, `project?` | Detailed agent info (progress, relay count, errors) |
| `relay_agent` | `to`, `content`, `project?`, `caller_session?` | Inject content into agent's terminal via PTY |
| `stop_agent` | `to`, `project?` | Graceful shutdown (sends /exit) |
| `stop_all_agents` | `project` | Stop ALL running agents in a project |
| `resume_agent` | `to`, `project?` | Resume a frozen agent (reset relay count, restart heartbeat) |

**PTY Bridge Architecture:**
```
Daemon → spawns `claude` or `codex` CLI as PTY subprocess
       → Unix socket pair: main.sock (output) + main.sock.inject (input)
       → Terminal opened via gnome-terminal/xterm for visual monitoring
       → Heartbeat monitors liveness + delivers pending messages
       → On exit: agent record deleted, sockets cleaned up
```

**Agent Lifecycle:**
```
pending → spawning → running → [frozen] → stopped/error
                         ↑          ↓
                         └── resume ─┘
```

**Spawn parameters:** `max_turns` (number) caps agent turns before auto-freeze (default 30). `model` (string) overrides the default model for the agent backend (e.g. `claude-sonnet-4-6`).

### 22.11 Multi-Backend Support

Sub-agents can use different LLM backends:

| Backend | CLI | Prompt Injection | MCP Tools | Status |
|---------|-----|-----------------|-----------|--------|
| `claude` (default) | Claude Code | PTY inject after 7s delay | Full YesMem proxy integration | Live |
| `codex` | OpenAI Codex CLI | CLI argument (no PTY inject) | MCP-only (no proxy channel) | Live (MCP-only) |
| `opencode` | OpenCode CLI | PTY inject | Full YesMem integration | Live |

```
spawn_agent(project="yesmem", section="recherche", backend="codex")
```

**Codex-specific:**
- `--full-auto --no-alt-screen` flags
- Per-tool MCP approvals configured via `yesmem setup` (no global `approval_policy` override)
- Communicates exclusively via MCP tools (scratchpad, send_to, remember)
- YesMem registered as MCP server in Codex config

**Backend abstraction in `handler_agents.go`:**
- `buildAgentCommand()` switch on backend for CLI args
- `ensureAgentPermissions()` only for Claude (pre-approve MCP tools in `.claude/settings.json`)
- Prompt injection goroutine conditional (Claude: PTY inject after delay, Codex: CLI arg)

### 22.12 Heartbeat & Message Delivery

Two-stage delivery system ensures reliable agent-to-agent messaging:

```
Agent B sends send_to(target=C) → message stored in agent_messages table
                                → heartbeat ping injects "[Message pending]" hint
                                → next proxy request from C delivers actual content
                                → message marked as delivered
```

- **Heartbeat interval:** 1s ticker, monitors agent liveness (heartbeat.go:23: `1 * time.Second`)
- **Freeze detection:** agents frozen after N unread relay messages
- **Delivery tracking:** per-message `delivered`, `delivered_at`, `delivery_retries`, `delivery_failed`

### 22.12b Agent Supervision

Automatic lifecycle management via the heartbeat system (`internal/daemon/heartbeat.go`):

- **Dead PID detection:** `detectDeadPIDs()` uses `os.FindProcess()` + zero-signal probe (`Signal(0)`) to detect dead processes, marks agent status as "error"
- **Orphan detection:** `detectOrphanedAgents()` checks `liveness_ping_at` with 5-minute grace period (`livenessPingGrace`), triggers cascade stop after grace expires
- **Cascade stop:** When a parent agent stops, child agents are stopped automatically
- **Limit enforcement:** `enforceAgentLimits()` freezes (not kills) agents exceeding `max_turns` (default 30), `token_budget`, or `max_runtime` (default 30 minutes) — frozen agents can be resumed later
- **Auto-restart:** `attemptRestart()` with configurable strategies: `temporary` (capped at `max_restarts`, default 3) and `permanent` (unlimited restarts). 30s guard between restarts to prevent race conditions

### 22.13 Crash Recovery

Strategy-based recovery for crashed agents:

- **Restart strategies:** `temporary` (capped by `max_restarts`, default 3) and `permanent` (unlimited restarts)
- **Crash quarantine:** `quarantine_session()` isolates all learnings from crashed session, taints scratchpad section
- **Crash context:** error message and stack trace stored in agent record
- **Graceful daemon restart:** OTP-style hot reload — running agents recovered on daemon restart

### 22.13b Agent Telemetry

Automatic tracking of agent resource usage via proxy SSE interception (`internal/proxy/telemetry.go`):

**Tracked fields** (on `agents` table):

| Field | Type | Source |
|-------|------|--------|
| `turns_used` | int | Incremented per API roundtrip |
| `input_tokens` | int | From SSE `message_start` event |
| `output_tokens` | int | From SSE `message_delta` event |
| `last_activity_at` | timestamp | Updated on each API call |
| `phase` | text | Set via `update_agent_status` MCP tool (e.g., "implementing", "testing", "idle") |

- **SSE interception:** `parseUsageFromSSE()` captures token usage from streaming events in real-time (not post-hoc)
- **Atomic updates:** `AgentUpdateTelemetry()` atomically increments counters and updates `last_activity_at`
- **Stale detection:** Orchestrators monitor `last_activity_at` — agents inactive for 5+ minutes flagged as potentially stuck
- **Budget enforcement:** `enforceAgentLimits()` compares `input_tokens + output_tokens` against `token_budget`

### 22.14 /swarm Orchestration Protocol

The `/swarm` skill provides a structured protocol for multi-agent orchestration:

- **Agent A (Orchestrator):** reads plan, spawns sub-agents, monitors progress, writes report
- **Communication:** scratchpad (primary) + send_to (secondary)
- **DAG mode:** execution dependencies between agents (B must finish before C starts)
- **Budget strategies:** quality (all Opus), balanced (Opus orchestrator + Sonnet workers), economy (all Sonnet)
- **Status ping:** orchestrator checks agent status every 5 minutes

### 22.14b YesLoop

Fire-and-forget autonomous task loop for single-agent scoped work. See [YesLoop](../yesloop.md) for full documentation and [YesLoop feature](yesloop.md) for the compact summary.

- **Single agent, single worktree** — no inter-agent coordination overhead
- **6-phase pipeline** — ANALYZE → PLAN → EXECUTE → VERIFY → REVIEW → FINISH, all autonomous
- **6 defense-in-depth layers** for phase-completion guarantee (idle self-check, done-verify, done-guard, dead detection, hung detection, structured skill blocks)
- **Scratchpad-driven** — brief at start, progress markers during execution, DONE report at end
- **Differs from /swarm:** YesLoop is one agent in one worktree; /swarm dispatches multiple agents with DAG dependencies
- **Differs from persistent-orchestrator:** YesLoop is a fresh spawn per task; persistent-orchestrator resumes existing sessions through sequential Implement → Review → Commit stages

### 22.15 Persistent-Orchestrator Skill

Resume-based multi-agent pipeline for structured Implement → Review → Commit workflows (`skills/persistent-orchestrator.md`):

- **Resume-centric:** Agents are always resumed via scratchpad state, not respawned from scratch (except on error)
- **Sequential stages:** Implementer (code writing) → Reviewer (one-shot code review) → Committer — strict ordering with manual approval handoffs
- **Scratchpad state:** JSON arrays in sections (`task-queue`, `current-task`, `auftrag-implementer`) survive session restarts
- **Patience rules:** 3-minute minimum wait before checking sub-agents; never stops from impatience
- **Telemetry-based health checks:** Uses `get_agent` to monitor turns_used, tokens, and phase before decisions
- **Differs from /swarm:** /swarm does parallel dispatch with DAG dependencies; persistent-orchestrator enforces strict sequential stages with resume-first semantics

---


---

### Crash Recovery (Enhanced)

Automatic daemon-monitored recovery with contamination containment:

1. **Health check** every 5s — PID existence check for all running agents (heartbeat.go:34: crashRecovery runs every 5 ticks × 1s)
2. **Immediate quarantine** on crash — all learnings from the crashed session are isolated (`quarantine_session`) before any retry
3. **Scratchpad taint** — result sections marked with `[TAINTED]` prefix, visible to other agents
4. **Auto-retry** — clean session (new session_id, no contaminated briefing) spawns automatically
5. **3-attempt limit** — after 3 failed attempts: `status=failed`, caller gets crash context (runtime, last scratchpad status)
6. **Orchestrator decides** — on FAILED message: skip or abort (`stop_all_agents`)

**Why quarantine before retry:** Without quarantine, the retry gets the crashed session's learnings in the briefing → same error → crash loop. Other agents find poisoned learnings via `hybrid_search` → contamination spreads.

### Scheduler Gating

Concurrent execution prevention via `running`-map:

- `dueJobs()` checks `running` map before starting any job
- Same job cannot start twice (prevents Telegram poll/reply race conditions)
- Callback resets on completion (`JobDone` wiring into scheduler)
- Heartbeat-driven auto-correct loop for failed jobs (bash mode)

### Shared Scratchpad

Persistent key-value communication between all agents in a project:

- **Named sections:** `task`, `task-{name}`, `result-{name}`, `task-report`, `final-report`
- **Concurrent writers:** Each section owned by one agent's session ID
- **All agents can read:** No access control — transparent information sharing
- **Transient, not filesystem:** After swarm completion, sections can be deleted — final artifacts exist as files in the project directory
