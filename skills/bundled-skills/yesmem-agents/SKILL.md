---
name: yesmem-agents
description: Use when orchestrating multi-agent work, spawning parallel agents, coordinating swarm tasks, or managing agent communication. Trigger on "/schwarm", parallel work requests, or any inter-agent coordination need.
---

# Agent Orchestration

Spawn, manage, and communicate with parallel agents via Claude Code or opencode.

## Workflow
1. `spawn_agent(project, section)` — create agent for a task section
2. `list_agents(project)` — see all agents and their status
3. `relay_agent(to, content)` — inject message into running agent
4. `stop_agent(to)` — gracefully stop an agent

## spawn_agent Parameters

| Parameter | Purpose | Default |
|-----------|---------|---------|
| `project` | Project name | required |
| `section` | Task section name | required |
| `model` | Model override (sonnet, opus, haiku, deepseek-chat, deepseek-v4-pro) | inherited |
| `max_turns` | Turn limit (0=unlimited) | 0 |
| `token_budget` | Max tokens (0=config default) | 0 |
| `caller_session` | Parent session for callbacks | optional |
| `backend` | "claude", "codex", or "opencode" | "claude" |

**Backend choice:**

| Backend | Binary | Models | Notes |
|---------|--------|--------|-------|
| `claude` | `claude` | sonnet, opus, haiku | Anthropic only. Proxy-integrated prompt cache. Full MCP access. |
| `codex` | `codex` | deepseek-chat, deepseek-v4-pro, GPT models | OpenAI-compatible endpoint. Uses opencode.json provider config. |
| `opencode` | `opencode` | deepseek-chat, deepseek-v4-pro, GPT models | Same as codex but uses `opencode` binary name. |

**Gotchas:**
- `backend: "claude"` + `model: "deepseek-v4-pro"` → **silent failure** (0 turns, no output). The claude binary has no DeepSeek endpoint. Always pair DeepSeek models with `backend: "codex"` or `backend: "opencode"`.
- Resume is only supported for `backend: "claude"`.

## Communication

| Action | Tool |
|--------|------|
| Send to specific agent | `relay_agent(to, content)` |
| Send to specific session | `send_to(target, content)` |
| Broadcast to all sessions | `broadcast(content, project)` |
| Check agent status | `get_agent(to)` |
| Resume stopped agent | `resume_agent(to)` |
| Stop all agents | `stop_all_agents(project)` |

**CRITICAL: relay_agent / send_to content MUST end with `\n`** — without trailing newline the prompt stays in the tmux input line and is never submitted. The agent appears unresponsive despite receiving multiple pushes. Always: `relay_agent(to, "instruction text\n")`.

## Tips
- `to` accepts agent ID or section name
- `msg_type`: "command" (expects reply), "status" (no reply), "ack" (confirmation)
- Agents run in their own terminal — use `list_agents` to monitor
- `scratchpad_write/read` for shared state between agents
