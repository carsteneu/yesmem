## Daemon RPC Methods (130 methods)

All 130 dispatch methods from `internal/daemon/handler.go` `Handle()` switch statement. Methods prefixed with `_` are internal (not exposed as MCP tools).

### Search & Retrieval (12)

| Method | Description |
|--------|-------------|
| `search` | Full-text search across session messages |
| `deep_search` | Deep search with full content and ±3 message context |
| `hybrid_search` | BM25 + vector hybrid search with reciprocal rank fusion |
| `vector_search` | Pure vector similarity search (internal) |
| `docs_search` | Search indexed documentation with BM25 + vector |
| `expand_context` | Expand archived/compacted conversation parts |
| `get_learnings` | Retrieve learnings by category or ID, with version history |
| `get_learnings_since` | Incremental learning fetch since a timestamp |
| `query_facts` | Search learning metadata by entity, action, or keyword |
| `pop_recent_remember` | Pop recently-remembered learnings for proxy injection |
| `get_pulse_learnings_since` | Pulse-fetch learnings for briefing injection |
| `get_session_flavors_since` | Fetch session flavor metadata for session types |

### Memory Management (13)

| Method | Description |
|--------|-------------|
| `remember` | Save a learning for future sessions |
| `resolve` | Resolve an unfinished task by learning ID |
| `resolve_by_text` | Find and resolve unfinished task by text search |
| `quarantine_session` | Quarantine session — exclude its learnings from search |
| `skip_indexing` | Skip indexing for a session |
| `relate_learnings` | Set semantic edge between two learnings (supports/contradicts/depends_on) |
| `flag_contradiction` | Flag a contradiction between learnings for evolution system |
| `get_contradicting_pairs` | Get pairs of learnings flagged as contradictory |
| `invalidate_on_commit` | Mark learnings stale after code commit |
| `track_gap` | Track a knowledge gap (topic needing coverage) |
| `resolve_gap` | Resolve a knowledge gap with a learning reference |
| `get_active_gaps` | Get unresolved knowledge gaps |
| `track_session_end` | Record session end for extraction scheduling |

### Session Management (7)

| Method | Description |
|--------|-------------|
| `get_session` | Load a session by ID with summary/recent/paginated/full modes |
| `list_projects` | List all projects with session counts |
| `project_summary` | Chronological project overview |
| `get_project_profile` | Auto-generated project portrait |
| `get_compacted_stubs` | Load compressed conversation stubs for re-expansion |
| `get_session_start` | Get session start timestamp for recency-based search |
| `whoami` | Get own session ID and agent metadata |

### Plan Management (4)

| Method | Description |
|--------|-------------|
| `set_plan` | Set the active plan (thread-scoped, survives context collapse) |
| `update_plan` | Update active plan (add/remove/complete items) |
| `get_plan` | Get active plan |
| `complete_plan` | Mark plan as completed |

### Documentation (9)

| Method | Description |
|--------|-------------|
| `get_docs_hint` | Get documentation reminder hint for active plan |
| `get_skill_content` | Load full skill content by path |
| `list_doc_sources` | List indexed documentation sources |
| `ingest_docs` | Import documentation (.md/.txt/.rst/.pdf) into knowledge base |
| `remove_docs` | Remove a documentation source and its chunks |
| `contextual_docs` | Auto-inject doc chunks for matching file extensions |
| `list_trigger_extensions` | List file extensions that trigger doc injection |
| `get_rules_block` | Fetch condensed CLAUDE.md rules for proxy re-injection |
| `get_session_flavors_for_session` | Get flavor metadata for a specific session |

### Code Intelligence (8)

| Method | Description |
|--------|-------------|
| `search_code_index` | Search code graph for symbols by name pattern |
| `search_code` | Grep source files enriched with graph context |
| `get_code_context` | Symbol details (signature, file, connected nodes) |
| `get_dependency_map` | Package import graph with cycle detection |
| `graph_traverse` | Trace call paths and dependencies from a node |
| `get_file_index` | List source files with learning/gotcha annotations |
| `get_code_snippet` | Full symbol body from source (func, var, const, type) |
| `get_file_symbols` | List top-level symbols in a file with line numbers |

### Capabilities (10)

| Method | Description |
|--------|-------------|
| `get_caps` | Load saved cap definitions |
| `save_cap` | Save an executable cap (tool definition) |
| `register_caps` | Generate registerTool() JS code for saved caps |
| `activate_cap` | Activate a cap for the current thread |
| `deactivate_cap` | Deactivate a cap for the current thread |
| `execute_cap` | Execute a saved CAP handler sandboxed (ai-jail) |
| `get_active_caps` | Internal: get active caps for a thread (proxy RPC only) |
| `cap_store` | Capability database — create/query/upsert/delete on namespaced tables |
| `cap_proposal_decide` | Accept or reject an auto-correct cap proposal |
| `list_cap_proposals` | List pending cap correction proposals |

### Counters & Analytics (8)

| Method | Description |
|--------|-------------|
| `increment_hits` | Increment learning hit counter (alias for inject) |
| `increment_noise` | Increment learning noise counter |
| `increment_match` | Increment learning match counter |
| `increment_inject` | Increment learning injection counter |
| `increment_use` | Increment learning use counter |
| `increment_fail` | Increment learning fail counter (for auto-escalation blocking) |
| `increment_save` | Increment learning save counter |
| `increment_turn` | Increment per-project turn counter for decay scoring |

### Agent Communication (13)

| Method | Description |
|--------|-------------|
| `send_to` | Send message to another session |
| `check_channel` | Check for pending agent dialog invitations |
| `mark_channel_read` | Mark agent dialog messages as read |
| `broadcast` | Send message to all sessions in a project |
| `check_broadcasts` | Check for unread broadcasts |
| `spawn_agent` | Spawn agent for project section (PTY bridge) |
| `register_agent` | Register a spawned agent with the daemon |
| `update_agent` | Update agent metadata |
| `relay_agent` | Inject content into a running agent's terminal |
| `stop_agent` | Stop an agent |
| `stop_all_agents` | Stop all agents in a project |
| `resume_agent` | Resume a stopped/frozen agent |
| `list_agents` | List agents with status and PID |
| `get_agent` | Get agent details |
| `update_agent_status` | Update agent's semantic work phase |

### Fork System (6)

| Method | Description |
|--------|-------------|
| `fork_extract_learnings` | Proxy fork — store extracted learnings with source="fork" |
| `fork_set_session_flavor` | Proxy fork — set session flavor metadata |
| `fork_evaluate_learning` | Proxy fork — apply verdict actions + update impact_score |
| `fork_update_impact` | Proxy fork — standalone impact score update |
| `fork_resolve_contradiction` | Proxy fork — increment fail_count on both sides of a contradiction |
| `get_fork_learnings` | Proxy fork — fetch previous fork learnings for a session |

### Scratchpad & Pins (6)

| Method | Description |
|--------|-------------|
| `scratchpad_write` | Write a section to the shared scratchpad |
| `scratchpad_read` | Read scratchpad sections |
| `scratchpad_list` | List scratchpad projects and sections |
| `scratchpad_delete` | Delete a scratchpad section or project |
| `pin` | Pin an instruction visible in every turn (session or permanent) |
| `unpin` | Remove a pin by ID |
| `get_pins` | List active pins |

### Proxy & State (8)

| Method | Description |
|--------|-------------|
| `store_compacted_block` | Proxy — save compressed conversation stubs |
| `get_proxy_state` | Proxy — read runtime key-value state |
| `set_proxy_state` | Proxy — write runtime key-value state |
| `delete_proxy_state_prefix` | Proxy — delete all state entries with a key prefix |
| `get_config` | Read runtime config |
| `set_config` | Set runtime config |
| `update_fixation_ratio` | Proxy — persist session fixation ratio after collapse |
| `track_proxy_usage` | Track proxy API token usage per day |
| `track_fork_usage` | Track fork API token usage per day |

### Persona & Profiles (4)

| Method | Description |
|--------|-------------|
| `set_persona` | Set persona trait |
| `get_persona` | Current persona profile |
| `get_self_feedback` | Recent corrections and confirmations about Claude's work |
| `generate_briefing` | Generate briefing hook for session start |

### Files & Coverage (2)

| Method | Description |
|--------|-------------|
| `related_to_file` | Which sessions touched this file? |
| `get_coverage` | Which files were edited in a project? |

### REPL Patterns (4)

| Method | Description |
|--------|-------------|
| `record_repl_pattern` | Internal: record REPL command shape-hash for cap suggestion |
| `record_turn_sequence` | Internal: record per-turn tool sequence hashes |
| `get_repl_pattern_suggestion` | Internal: get cap suggestion from REPL pattern analysis |
| `dismiss_repl_pattern` | Dismiss a REPL pattern from future suggestions |

### Utilities & Health (8)

| Method | Description |
|--------|-------------|
| `ping` | Health check — returns "pong" |
| `index_status` | Indexing progress (total/done/skipped/running) |
| `idle_tick` | Idle detection tick — tracks MCP usage gaps, triggers reminders |
| `reload_vectors` | Vector store hot-reload |
| `schedule` | Create, update, list, or run scheduled jobs (cron + interval) |
| `llm_complete` | On-the-fly LLM completion via configured provider |
| `resolve_project` | Resolve directory path to canonical project name |
| `dismiss_code_nav` | Dismiss code-navigation suggestion for this session |

### Internal (4)

| Method | Description |
|--------|-------------|
| `_track_usage` | Internal: report token usage per session |
| `_persist_rate_limits` | Internal: persist rate limit state across restarts |
| `register_pid` | Internal: register Claude process PID for stdin injection |
| `register_window` | Internal: register X11 window ID for xdotool push |

---
