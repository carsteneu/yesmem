---
name: yesmem-agents
description: Use when orchestrating multi-agent work, spawning parallel agents, coordinating swarm tasks, or managing agent communication. Trigger on "/schwarm", parallel work requests, or any inter-agent coordination need.
---

# Agent Orchestration

Spawn, manage, and communicate with parallel Claude Code agents.

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
| `model` | sonnet, opus, haiku | inherited |
| `max_turns` | Turn limit (0=unlimited) | 0 |
| `token_budget` | Max tokens (0=config default) | 0 |
| `caller_session` | Parent session for callbacks | optional |
| `backend` | "claude" or "codex" | "claude" |

## Communication

| Action | Tool |
|--------|------|
| Send to specific agent | `relay_agent(to, content)` |
| Send to specific session | `send_to(target, content)` |
| Broadcast to all sessions | `broadcast(content, project)` |
| Check agent status | `get_agent(to)` |
| Resume stopped agent | `resume_agent(to)` |
| Stop all agents | `stop_all_agents(project)` |

## Tips
- `to` accepts agent ID or section name
- `msg_type`: "command" (expects reply), "status" (no reply), "ack" (confirmation)
- Agents run in their own terminal — use `list_agents` to monitor
- `scratchpad_write/read` for shared state between agents
