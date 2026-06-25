## Database Schema

**48 unique CREATE TABLE names + 4 FTS5 virtual tables + dynamic cap_store tables.** (The `tables` slice in `createSchema()` references 42 constant-defined tables; migrations add 3 more; separate storage files add 3 more.)

### yesmem.db — Core Database

42 tables from the main `createSchema()` slice, plus 3 from migrations, plus 2 from other files that auto-create.

#### Core Memory

| Table | Purpose | Key Columns |
|-------|---------|-------------|
| `sessions` | Chat sessions with project, timestamps, lineage | `id`, `project`, `project_short`, `started_at`, `parent_session_id`, `agent_type`, `source_agent`, `fixation_ratio` |
| `learnings` | 55-column knowledge store — the heart of YesMem | `id`, `category`, `content`, `project`, `source`, `canonical_project`, `superseded_by`, `stability`, `impact_score`, `content_hash` |
| `learnings_fts` | FTS5 virtual table for BM25 full-text search over learnings | Porter stemmer + unicode61 tokenizer |

#### Learning Metadata (V2 junction tables)

| Table | Purpose | Key Columns |
|-------|---------|-------------|
| `learning_entities` | Files, systems, people affected (junction) | `learning_id`, `value`, `type` |
| `learning_actions` | Commands, operations involved (junction) | `learning_id`, `value` |
| `learning_keywords` | Semantic keywords (junction) | `learning_id`, `value` |
| `learning_anticipated_queries` | Concrete search phrases for better vector retrieval (junction) | `learning_id`, `value` |
| `anticipated_queries_fts` | FTS5 virtual table over anticipated queries | Porter + unicode61 tokenizer |

#### Context-Aware Retrieval

| Table | Purpose | Key Columns |
|-------|---------|-------------|
| `query_log` | Persisted query vectors + injected learning IDs from every hybrid_search call | `id`, `project`, `query_text`, `query_vector`, `cluster_id`, `injected_learning_ids` |
| `query_clusters` | Semantically similar queries grouped by agglomerative clustering (cosine 0.80) | `id`, `project`, `centroid_vector`, `label`, `query_count` |
| `learning_cluster_scores` | Per-learning per-cluster performance: inject_count, use_count, noise_count | `learning_id`, `cluster_id`, `inject_count`, `use_count`, `noise_count` |
| `embedding_cache` | Cached embeddings to avoid re-computation | `query_hash`, `query_text`, `vector`, `model` |

#### Documentation

| Table | Purpose | Key Columns |
|-------|---------|-------------|
| `doc_sources` | Registered doc sources (name, version, path, skill/rules flags) | `id`, `name`, `version`, `project`, `doc_type`, `is_skill`, `is_rules`, `trigger_extensions`, `full_content` |
| `doc_chunks` | Document chunks (heading_path, content, tokens_approx, metadata) | `id`, `source_id`, `source_file`, `heading_path`, `content`, `tokens_approx`, `embedding_vector` |
| `doc_chunks_fts` | FTS5 for doc search | Porter + unicode61 + tokenchars `_-'.` |

#### Knowledge Structure

| Table | Purpose | Key Columns |
|-------|---------|-------------|
| `associations` | Graph edges between knowledge entities with typed relations | `source_type`, `source_id`, `target_type`, `target_id`, `relation_type`, `weight` |
| `learning_clusters` | Grouped learnings from agglomerative clustering | `id`, `project`, `label`, `learning_count`, `learning_ids` |
| `contradictions` | Identified conflicts between learnings | `id`, `learning_ids`, `description`, `project`, `resolved` |
| `knowledge_gaps` | Topics needing coverage with hit_count and resolution tracking | `id`, `topic`, `project`, `hit_count`, `resolved_at`, `resolved_by`, `review_verdict` |
| `strategic_context` | Long-term context with supersede support | `id`, `scope`, `context`, `source`, `superseded_by`, `active` |

#### Persona & Profiles

| Table | Purpose | Key Columns |
|-------|---------|-------------|
| `persona_traits` | Individual traits per dimension with confidence and source | `id`, `user_id`, `dimension`, `trait_key`, `trait_value`, `confidence`, `superseded` |
| `persona_directives` | Synthesized directive text from traits | `id`, `user_id`, `directive`, `traits_hash`, `model_used` |
| `project_profiles` | Auto-generated project summaries | `project`, `profile_text`, `session_count`, `generated_at` |

#### Proxy & State

| Table | Purpose | Key Columns |
|-------|---------|-------------|
| `compacted_blocks` | Compressed conversation stubs for context re-expansion | `id`, `thread_id`, `start_idx`, `end_idx`, `content` |
| `proxy_state` | Proxy runtime key-value state (legacy, migrated to runtime.db) | `key`, `value`, `updated_at` |
| `plans` | Active plan storage keyed by thread_id | `thread_id`, `content`, `status`, `scope`, `project` |
| `turn_counters` | Per-project turn counters for decay scoring | `project`, `turn_count`, `updated_at` |

#### Agent Communication

| Table | Purpose | Key Columns |
|-------|---------|-------------|
| `agent_dialogs` | 1:1 dialog state machine (pending/active/ended) | `id`, `initiator`, `partner`, `topic`, `status` |
| `agent_messages` | Dialog messages with delivery tracking | `id`, `dialog_id`, `sender`, `target`, `content`, `read`, `delivered`, `msg_type` |
| `agent_broadcasts` | Project-wide broadcasts with read_by tracking | `id`, `sender`, `project`, `content`, `read_by` |
| `agents` | Spawned agent lifecycle tracking (PTY bridge) | `id`, `project`, `section`, `pid`, `status`, `relay_count`, `depth`, `token_budget`, `backend` |
| `scratchpad_entries` | Shared whiteboard for multi-agent collaboration | `id`, `project`, `section`, `content`, `owner` |

#### Operations

| Table | Purpose | Key Columns |
|-------|---------|-------------|
| `index_state` | File indexing progress tracking | `jsonl_path`, `file_size`, `file_mtime`, `indexed_at` |
| `session_tracking` | Why a session is being tracked (e.g., after /clear recovery) | `project`, `session_id`, `reason`, `timestamp` |
| `file_coverage` | Files touched per project with operation types | `project`, `file_path`, `directory`, `session_count`, `last_touched` |
| `self_feedback` | Claude's self-corrections and learned patterns | `id`, `session_id`, `feedback_type`, `description`, `pattern` |
| `refined_briefings` | Cached briefings (raw_hash → refined_text) | `project`, `raw_hash`, `refined_text`, `model_used` |
| `daily_spend` | API cost tracking per day/model | `day`, `bucket`, `spent_usd`, `calls` |
| `claudemd_state` | Operative CLAUDE.md reference generation state | `project`, `last_generated`, `learnings_hash`, `output_path` |
| `token_usage` | Per-session cumulative token accounting | `thread_id`, `input_tokens`, `output_tokens`, `cache_read_tokens`, `cache_write_tokens` |
| `pinned_learnings` | Bookmarked instructions (permanent scope) | `id`, `project`, `content`, `source` |
| `session_active_caps` | Thread-scoped capability activation tracking | `thread_id`, `cap_name`, `activated_at`, `last_used_at` |

#### Code Intelligence

| Table | Purpose | Key Columns |
|-------|---------|-------------|
| `code_descriptions` | Package-level code descriptions with anti-patterns | `project`, `package_name`, `description`, `anti_patterns`, `git_head` |
| `project_scan` | Cached codebase scan results with git head and CBM mtime | `project`, `scan_json`, `git_head`, `cbm_mtime` |

#### Pattern Detection

| Table | Purpose | Key Columns |
|-------|---------|-------------|
| `repl_pattern_observations` | Per-project REPL command shape-hashes with counts and suggestion state | `id`, `project`, `shape_hash`, `first_cmd_example`, `matched_cap`, `count`, `dismiss_count` |
| `thread_sequences` | Per-thread turn sequence hashes for workflow pattern detection | `thread_id`, `project`, `turn_hashes`, `updated_at` |
| `code_nav_dismissals` | Per-session code-navigation suggestion dismissal counts | `session_id`, `dismiss_count` |

#### Fork & Scheduling

| Table | Purpose | Key Columns |
|-------|---------|-------------|
| `fork_coverage` | Tracks which message ranges have been processed by forked agents (dedup) | `id`, `session_id`, `from_msg_idx`, `to_msg_idx`, `fork_index` |
| `scheduled_jobs` | Cron + interval job definitions with mode/cap/sandbox config | `id`, `name`, `cron`, `prompt`, `enabled`, `recurring`, `mode`, `cap_name`, `interval_seconds`, `model` |
| `bash_job_runs` | Execution log for bash-mode scheduled jobs | `id`, `job_id`, `job_name`, `cap_name`, `command`, `status`, `exit_code`, `output`, `error_msg`, `processed` |

### messages.db — Message Storage

| Table | Purpose | Key Columns |
|-------|---------|-------------|
| `messages` | Session messages with role, content, tool metadata | `id`, `session_id`, `role`, `message_type`, `content`, `content_blob`, `tool_name`, `file_path`, `sequence`, `timestamp`, `source_agent` |
| `messages_fts` | FTS5 full-text index over message content (incl. thinking blocks from content_blob) | unicode61 tokenizer |

Thinking blocks are stored in `content_blob` by the parser but copied to `content` during insert — making them searchable via FTS5 without separate enrichment queries.

### runtime.db — Runtime State

| Table | Purpose | Key Columns |
|-------|---------|-------------|
| `proxy_state` | Runtime key-value state (session scope — primary location post-migration) | `key`, `value`, `updated_at` |
| `pinned_learnings` | Session-scoped pinned instructions | `id`, `project`, `content`, `source` |

### caps.db — Capability Storage

Opened lazily via `OpenCapsDB()`. Uses DELETE journal mode to prevent stale reads during concurrent daemon operations.

| Table | Purpose | Key Columns |
|-------|---------|-------------|
| `cap_store_meta` | Registry of all dynamic cap tables (cap_name → table_name → full_name) | `id`, `cap_name`, `table_name`, `full_name` |
| `session_active_caps` | Thread-scoped capability activation (when caps.db is available; falls back to yesmem.db) | `thread_id`, `cap_name`, `activated_at`, `last_used_at` |
| `cap_*__*` | Dynamic per-capability tables — created on demand via `cap_store` MCP tool | Variable — defined by cap schema at creation time |

Dynamic cap tables are namespaced as `cap_<capname>__<tablename>` with a 10-table-per-cap and 10,000-row-per-table quota limit.

---
