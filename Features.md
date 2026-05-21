# YesMem — Features

> Long-term memory system for Claude Code.
> Not memory FOR an agent, but memory AS agent.

YesMem adds permanent memory, self-configuring routing, and multi-agent orchestration to Claude Code, OpenCode, and Codex. Works with API keys and subscription plans.

---

## Architecture

```
LLM Client  ──→  Proxy (:9099)  ──→  Upstream API
                    │
              Daemon (sqlite + vector DB)
                    │
         ┌─────────┼──────────┐
    MCP Tools   Hooks    Scheduled Agents
```

- **Proxy:** HTTP middleware. Context collapsing (Sawtooth), prompt cache optimization, system prompt rewrite, provider auto-discovery. Optional — YesMem works without it in MCP-only mode.
- **Daemon:** Persistent backend. SQLite for structured data, vector store for semantic search. Single process, local-first, zero cloud.
- **MCP Server:** Thin stdio interface for Claude Code. All 67 memory tools exposed.
- **Hooks:** Event-driven Claude Code integration (SessionStart, PreToolUse, PostToolUseFailure, UserPromptSubmit).

---

## Feature Overview

### Memory & Knowledge Engine
[→ Full documentation](docs/features/memory.md)

- **Session indexing:** Every conversation searchable by content, file, or time
- **Knowledge lifecycle:** Facts, decisions, gotchas, patterns — with progressive decay
- **Emotional memory:** Sentiment tracking across sessions
- **MEMORY.md auto-generation:** Weekly memory dumps for Claude's context
- **Fact search:** `query_facts` for structured metadata queries
- **Context expansion:** Archived conversations pulled back on demand
- **Knowledge gaps:** Learned gaps tracked and re-evaluated

### Prompt Injection & Briefing
[→ Full documentation](docs/features/briefing.md)

- **Briefing system:** Session-start context injection (what happened, what's open)
- **Associative context:** Relevant memories injected mid-conversation
- **Rules re-injection:** CLAUDE.md rules re-injected every 40k tokens
- **Plan checkpoints:** Active work plans survive context collapse
- **Timestamp hints:** Per-message temporal markers for long sessions
- **Skill evaluation:** Skills auto-activated based on task detection
- **System prompt rewrite:** Default prompt replaced with self-constitution template (SYSTEM.md)
- **Bidirectional memory:** Open tasks pushed back to future sessions

### Proxy Engine
[→ Full documentation](docs/features/proxy.md)

- **Infinite thread:** Lossless context collapsing (Sawtooth algorithm)
- **Provider auto-discovery:** 84 models across 3 providers auto-routed
- **Prompt cache optimization:** Cache TTL upgrades, breakpoint enforcement
- **LLM backend flexibility:** Claude, DeepSeek, OpenAI, Mistral — transparent routing
- **System prompt injection:** SYSTEM.md template across all pipelines
- **Loop detection:** Runtime loop detection with escalation warnings
- **MCP Tools Reference:** 67 memory tools via stdio

### Multi-Agent System
[→ Full documentation](docs/features/multi-agent.md)

- **Agent spawning:** Fire-and-forget sub-agents with heartbeat monitoring
- **Crash recovery:** Automatic quarantine + retry with clean state
- **Memory safety:** Session isolation prevents cross-contamination
- **Shared scratchpad:** Persistent key-value communication between agents
- **Orchestrator pattern:** Long-running coordinator via `/swarm` skill

### Developer Tools
[→ Full documentation](docs/features/developer-tools.md)

- **Code intelligence:** Graph-based code navigation — `search_code_index`, `get_file_symbols`, `get_code_snippet`
- **openCode plugin:** Code-nav hooks, rule_guard, automatic session identification
- **Hook system:** Claude Code hooks for PreToolUse, PostToolUseFailure, UserPromptSubmit, etc.
- **Documentation management:** Indexed docs searchable on demand via `docs_search()`

### Cognition & Persona
[→ Full documentation](docs/features/cognition.md)

- **Persona engine:** 50+ traits across 6 dimensions, evolving from how you work
- **Pinned learnings:** Persistent instructions visible every turn
- **Skill evaluation:** Automatic skill activation from task context
- **Cognitive signals:** Reflection, gap review, narrative generation
- **Learning clustering:** Related facts and decisions grouped by topic

### Operations & Deployment
[→ Full documentation](docs/features/operations.md)

- **Cost tracking:** Per-model, per-session cost with budget limits
- **LoCoMo benchmark:** 0.87 recall score across 1M+ parameter experiments
- **Sync-public:** Automated public repo sync with security scanning
- **Auto-update:** Self-updating daemon and proxy
- **Scheduled agents:** Cron-based recurring jobs (headless or agent-based)
- **Capabilities system:** Reusable sandboxed JS/Bash tools
- **Sandbox execution:** `ai-jail`-isolated code execution
- **Password sanitization:** Auto-redaction of secrets in logs and code

---

## Reference

| Document | Description |
|----------|-------------|
| [MCP Tools Reference](docs/mcp-tools-reference.md) | 70 tools — full list with descriptions |
| [CLI Commands](docs/cli-reference.md) | 64 CLI commands |
| [Daemon RPC](docs/daemon-rpc.md) | 130 RPC methods |
| [Database Schema](docs/database-schema.md) | 48 tables + 4 FTS5 virtual tables |
| [Config Reference](docs/config-reference.md) | Full config.yaml reference |
| [CAP Spec](docs/CAPS-md-spec.md) | Capability specification v1.1 |
| [Cap Features](docs/CapFeatures.md) | Capabilities system architecture |
| [Benchmark](docs/BENCHMARK.md) | LoCoMo benchmark results |
| [Internal Packages](docs/features/operations.md#internal-packages) | 30 internal Go packages |

---

## Differentiators

- **Not a wrapper — a layer.** YesMem sits between your LLM and its context, not around it. The proxy modifies the conversation stream in-flight.
- **Local-first, zero cloud.** Everything runs on your machine. SQLite + vector store. No accounts, no telemetry.
- **Self-cleaning knowledge.** Outdated facts auto-decay. Loop-contaminated sessions auto-quarantine.
- **Multi-model.** Claude, DeepSeek, OpenAI, Mistral — one pipeline, one memory system.
- **Self-hosted alternative to Anthropic Cloud Routines** — runs locally with full memory, MCP, and file access.
