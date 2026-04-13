# YesMem

[![CI](https://github.com/carsteneu/yesmem/actions/workflows/ci.yml/badge.svg)](https://github.com/carsteneu/yesmem/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/carsteneu/yesmem)](https://github.com/carsteneu/yesmem/releases)
[![Go](https://img.shields.io/badge/Go-1.25-00ADD8?logo=go)](https://go.dev)
[![License: FSL-1.1-ALv2](https://img.shields.io/badge/License-FSL--1.1--ALv2-blue)](LICENSE)
[![LoCoMo](https://img.shields.io/badge/LoCoMo-0.87-brightgreen)](docs/BENCHMARK.md)

**Adaptive context window for Claude Code — every session starts where it matters, not at zero.**

Sessions that never forget. Context that collapses losslessly.
Knowledge that self-corrects. One binary, zero setup friction.


## Install

```bash
# One-line install (Linux/macOS)
curl -fsSL https://raw.githubusercontent.com/carsteneu/yesmem/main/scripts/install.sh | bash

# Run setup (MCP server, hooks, proxy, services — one command)
yesmem setup

# Done. Open a new Claude Code session.
```

Or download the binary from [GitHub Releases](https://github.com/carsteneu/yesmem/releases).

## Why YesMem

Tuesday morning. New session. You type: *"What did we do last Tuesday?"* Claude tells you — the refactoring, the bug in the auth middleware, the decision to switch to connection pooling. You ask: *"What was still open?"* Claude shows you. You ask: *"Why did we stop?"* Claude explains — you hit a dependency issue, decided to wait for the upstream fix. You ask: *"What did you think about that approach?"* Claude gives you its honest assessment from last week's context, not a guess.

That's where you start. Not from zero. From where it matters.

## What You Get

- **Nothing is ever lost:** Claude Code's built-in compaction throws away your conversation. YesMem collapses it losslessly instead. Three hours in, you need something from hour one? Claude pulls it back, word for word.

- **Optimal context window:** set your threshold, resize on the fly. 150K for focused work, 500K for deep research, switch mid-session. The window adapts to you, not the other way around. No performance degradation, no matter how long you work.

- **Better answers out of the box:** you didn't change anything, but Claude suddenly stops cutting corners. YesMem replaces Anthropic's output throttling with quality directives. Same workflow, better results.

- **Zero effort knowledge capture:** you finish debugging, close the session, go home. Next morning, Claude already knows what you fixed and why. Extraction happens in the background after every response. You don't do anything.

- **Knowledge that self-corrects:** last month you decided on REST. This week you switched to gRPC. Claude doesn't suggest REST anymore. Outdated facts get replaced automatically, and your explicit decisions always outrank Claude's guesses.

- **Costs go down over time,** the longer you work, the more context hits the prompt cache. Collapsing cycles exploit Anthropic's cache TTL. Context typically gets cheaper over time, not more expensive.

- **Your rules stick:** you wrote careful instructions in CLAUDE.md. After 20 minutes, Claude ignores them, buried under tool outputs. YesMem re-injects your rules every 40k tokens. Mid-session, not just at start.

- **Pick up where you left off,** not "here are 5 bullet points" but *"last time you were debugging the race condition in the proxy. You found that the mutex wasn't held across the cache write..."* Immersive handovers, not summaries.

- **Right docs at the right time:** you're using an unfamiliar API and Claude guesses wrong. You indexed the docs once, now Claude searches them on demand and gets the actual function signatures instead of guessing.

- **Parallel work:** one agent refactors the auth module, another writes tests, a third updates the docs. They share state, talk to each other, and you watch them work. Heartbeat, crash recovery, cascade shutdown built in.

- **Self-cleaning:** Claude gets stuck in a loop, suggesting the same broken approach three times. YesMem detects it, quarantines the learnings from that session. The knowledge base maintains itself.

### Foundations

- **Find anything:** full-text + semantic search combined (BM25 + 512d vectors, Reciprocal Rank Fusion)
- **Your words matter most,** 4-tier trust hierarchy: `user_stated` > `agreed_upon` > `claude_suggested` > `llm_extracted`
- **Noise fades, signal stays:** Ebbinghaus decay based on conversation turns. Useful knowledge strengthens, irrelevant fades.
- **Smart extraction,** 70% noise filtered before extraction starts. Then: extraction → embedding → quality refinement → clustering.
- **One binary, one command:** no Python, no Node, no Docker, no cloud account. `yesmem setup`, done.
- **Your data stays yours,** everything in `~/.claude/yesmem/`. Nothing leaves your machine.
- **Free:** FSL-1.1-ALv2. Use it for anything except building a competing product. After 2 years, Apache 2.0.


### Windows (via WSL2)

YesMem runs natively on Linux and macOS. On Windows, install Claude Code inside [WSL2](https://learn.microsoft.com/en-us/windows/wsl/install) and use the Linux binary — everything works identically (Unix sockets, daemon, proxy, hooks). Native Windows support is not available yet.

### Build from source

```bash
make install    # Build + install to ~/.local/bin/yesmem
yesmem setup    # Configure MCP server, hooks, proxy, services
```

## Architecture

Single Go binary (~120MB with embedded SSE embedding model). Three cooperating processes plus a hook layer:

| Component | Role | Communication |
|-----------|------|---------------|
| **Daemon** | Background service: indexing, extraction, search, embedding, all RPC | Unix socket + HTTP |
| **MCP Server** | Thin stdio interface for Claude Code — forwards to daemon | stdio / Unix socket |
| **Proxy** | Between Claude Code and Anthropic API — context collapsing, prompt cache, associative injection, system prompt rewrite. **Optional** — YesMem works without it. | HTTP `:9099` |
| **Hooks** | Event-driven Claude Code integration (SessionStart, PreToolUse, PostToolUseFailure, UserPromptSubmit) | CLI subcommands |

All data local. No cloud. No external dependencies. Pure Go — no CGo, no C compiler. One static binary.

**Data:** `~/.claude/yesmem/` — SQLite databases (learnings, messages, runtime state), vector store, logs, everything else.

## Features

50 MCP tools · ~130 daemon RPCs · 53 CLI commands — **[full reference →](Features.md)**

### Find & Remember
- **Find anything across all sessions** — full-text + semantic search combined via Reciprocal Rank Fusion
- **Knowledge self-corrects** — supersede chains with trust-based resistance, cycle detection, contradiction detection
- **Your words outrank Claude's guesses** — `user_stated` > `agreed_upon` > `claude_suggested` > `llm_extracted`
- **Signal stays, noise fades** — Ebbinghaus decay based on conversation turns, not wall-clock time
- **Quality signals** — match, inject, use, save, noise — six independent measures per learning, not a hit counter

### Automatic Learning
- **Smart extraction** — 70% noise filtered, then extraction → embedding → quality refinement → narrative generation → clustering → recurrence detection
- **Zero overhead** — extraction runs async in the background after every response
- **Knowledge self-organizes** — dedup and distillation without user intervention

### Infinite Sessions (Proxy)

The proxy is **optional**. YesMem works fully without it — all MCP tools, briefing, extraction, search, agents, docs, and plans work in MCP-only mode. The proxy adds infinite context, associative injection, and prompt cache optimization on top.

- **Sessions run forever** — intelligent lossless collapsing, decisions and pivot moments protected from decay
- **Better answers** — quality directives replace output throttling, CLAUDE.md authority reinforced
- **Rules that stick** — CLAUDE.md re-injected every 40k tokens (spaced repetition, anti-drift)
- **Costs drop over time** — prompt cache exploitation across collapsing cycles (sawtooth pattern)
- **Right knowledge at the right time** — relevant learnings injected automatically with every user message
- **Docs when you need them** — indexed documentation searchable on demand via `docs_search()`

### Continuity
- **Claude adapts to your style** — 50+ traits across 6 dimensions, evolving from how you work
- **Pick up where you left off** — immersive handovers: "last time you were debugging the race condition in the proxy..."
- **Every session starts ready** — open tasks, project context, your communication style — before you type a character

### Parallel Work
- **Spawn parallel agents** — heartbeat monitoring, crash recovery, cascade shutdown
- **Agents talk to each other** — `send_to()`, `broadcast()`, typed messages across sessions
- **Shared state** — multi-agent scratchpad for coordination
- **Plans that persist** — `set_plan()`, `update_plan()`, checkpoint injection every 20k tokens

### Knowledge That Grows
- **Index your docs** — Markdown, reStructuredText, PDF. Heading-aware chunking with rich metadata.
- **Rules that survive everything** — pinned instructions visible in every turn, every collapsing cycle
- **Knows what it doesn't know** — tracks knowledge gaps, auto-resolves when answers arrive
- **Self-cleaning** — detects fixation loops, quarantines bad learnings automatically

### Tools & CLI
- **50 MCP tools** — search, remember, personas, plans, agents, docs, scratchpad, config
- **~53 CLI commands** — daemon, proxy, setup, extraction, benchmarking, export/import, cost tracking
- **~130 daemon RPC methods** — full programmatic access

## How YesMem Differs

| Capability | Typical memory tools | YesMem |
|---|---|---|
| Knowledge lifecycle | Append-only, manual cleanup | Auto-supersede, decay, contradiction detection |
| Trust model | All sources equal | 4-tier hierarchy (user > agreed > suggested > extracted) |
| Context management | External RAG or full rewrite | Transparent proxy — lossless collapse, prompt cache exploitation |
| Integration | Custom hooks, config files | `yesmem setup` — one command, zero config |
| Data location | Cloud/hybrid | Local only (`~/.claude/yesmem/`) |
| Search | Keyword OR semantic | Hybrid BM25 + 512d vectors, Reciprocal Rank Fusion |
| Architecture | Python/Node service + dependencies | Single Go binary, no CGo, no runtime dependencies |

## Benchmarks

Real numbers from production use (1000+ sessions, `yesmem stats` and `yesmem benchmark`).

### Knowledge system

| Metric | Value |
|--------|-------|
| Sessions indexed | 2,537 (401k messages) |
| Active learnings | 4,067 |
| Superseded (auto-corrected) | 38,057 |
| Embedding model | SSE multilingual 512d (embedded in binary) |
| Embedding coverage | 100% |

### Context collapsing

| Metric | Value |
|--------|-------|
| Collapsing ratio | 87-98% (measured across sessions) |
| Configurable window | 100k - 1M tokens (per-model thresholds) |
| Pipeline | Eager stub → Stubify → progressive decay → collapse to archive block |
| Protected content | Decisions, pivot moments, active debug pairs |
| Eager stubbing | Tool results compressed in-place after processing (~25% savings, zero cache cost) |
| Recovery | Full, all collapsed content retrievable via `deep_search()` |

The proxy collapses in cycles (sawtooth pattern): context grows, hits the threshold, gets stubbed down, grows again. Prompt cache breakpoints are preserved across cycles. The API never sees more than your configured limit. Session keeps running indefinitely.

![Context collapsing in action — 2M tokens of conversation compressed to 15% context usage](docs/images/max_context.png)

### LoCoMo benchmark

Evaluated against the [LoCoMo dataset](https://github.com/snap-research/locomo) (Long Conversation Memory), the de-facto standard for memory system evaluation. 1,540 questions across 10 conversations, 4 categories.

| Eval LLM | Single-hop | Multi-hop | Temporal | Open-domain | **Overall** |
|----------|------------|-----------|----------|-------------|-------------|
| gpt-5.4 | 0.76 | 0.89 | 0.60 | 0.92 | **0.86** |
| Claude Opus | 0.78 | 0.93 | 0.60 | 0.91 | **0.87** |

Agentic mode (+0.20 over static retrieval): the LLM iteratively calls search tools (hybrid, deep, keyword) with forced rotation. Retrieval is not the bottleneck, the same retrieval system scores 0.65 with gpt-4o and 0.86 with gpt-5.4.

See **[docs/BENCHMARK.md](docs/BENCHMARK.md)** for full methodology, reproduction steps, and cost estimates.

### Cost analysis

Real cost data from 24h production use (Opus 4.6, 8 concurrent sessions, 1,200+ requests).

| Scenario | Daily Cost | vs YesMem Proxy |
|----------|-----------|-----------------|
| **YesMem Proxy @200k** | **$112** | Baseline |
| Manual /clear @200k | $112 | $0 (but 7 session restarts, context lost) |
| Manual /clear @300k | $132 | +$20 |
| CC-native compaction | $159 | +$47 |
| No compaction (1M ceiling) | $159 | +$47 |

The proxy saves ~$47/day vs CC-native by keeping average context at ~125k instead of letting it grow to ~261k. Cache reads are 73% of total cost, bounded context cuts them in half.

Prompt cache keepalive (6 pings at 4:30min intervals) bridges idle gaps up to ~27min for $0.07/ping, preventing $0.88 cache rewrites. 5min cache TTL with keepalive beats 1h TTL by 23%.

See **[docs/sawtooth-cost-analysis.md](docs/sawtooth-cost-analysis.md)** and **[docs/cache-keepalive-cost-analysis.md](docs/cache-keepalive-cost-analysis.md)** for full data.

## LLM Backend

| Provider | Status | How |
|----------|--------|-----|
| **Anthropic API** | Production | Direct HTTP (default) |
| **Claude CLI** | Production | Via `claude` binary (Pro/Max/Team — no API key needed) |
| **OpenAI** | Planned | Responses API (GPT-5.x) |
| **OpenAI-compatible** | Planned | Any compatible endpoint |

The proxy supports Anthropic Messages API natively. OpenAI Responses API support (Codex CLI compatibility) is in development.

### API Key vs. Subscription

YesMem works with both **API keys** (pay-per-token) and **subscription plans** (Pro/Max/Team via Claude CLI).

**With API keys:** Full functionality, no restrictions. The proxy modifies requests transparently — Anthropic bills per token regardless of how the request was constructed.

**With subscription plans:** MCP tools, briefing, extraction, search, agents, and all non-proxy features work without issues. The proxy modifies requests between Claude Code and Anthropic's API (context collapsing, cache optimization, associative injection). Anthropic may restrict what third-party tools can do with subscription authentication — if the proxy causes issues, disable it and use MCP-only mode. All core memory features remain fully functional without the proxy.

## License

[FSL-1.1-ALv2](LICENSE) — Functional Source License. Use it for anything except building a competing product. After 2 years per version, it becomes Apache 2.0.

## Built by

Papoo Software & Media GmbH, Bonn, Germany. In production since March 2026. Private development since November 2025, public since April 2026.

## Sponsor

<details>
<summary>Sponsored by CCM19</summary>

[ccm19.de](https://www.ccm19.de/en/) — the Cookie Consent Manager from Germany.

</details>
