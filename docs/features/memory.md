# YesMem — Features

> Long-term memory system for Claude Code.
> Not memory FOR an agent, but memory AS agent.

---

# Memory & Knowledge Engine

## 1. Session Indexing & Full-Text Search

### What it does
Every Claude Code session (JSONL files under `~/.claude/projects/`) is automatically indexed by the daemon's file watcher. Messages are parsed, stored in a separate `messages.db` SQLite database, and indexed via FTS5 full-text search.

### Key properties
- **Zero-cost indexing:** Phase 1 requires no LLM calls — all messages go into SQLite/FTS5 for free
- **Separate messages.db:** Messages live in their own database file, keeping yesmem.db lean for fast learnings/sessions queries. Messages.db grows independently and can be partitioned when >1 GB
- **Subagent awareness:** Sessions from subagents are linked via `parent_session_id` and annotated in search results (detected by `cc_entrypoint=sdk-ts` or model containing "haiku")
- **Session flavors:** Each session gets a `session_flavor` tag for categorization
- **File coverage tracking:** Which files were touched in which sessions (`file_coverage` table)
- **Permanent archive:** Claude Code deletes sessions after 30 days — YesMem archives all JSONL files permanently in `~/.claude/yesmem/archive/`, organized by project

### Content-Aware Pre-Processing

Before any LLM call, messages are aggressively filtered and truncated:

| Content Type | Detection | Limit |
|---|---|---|
| Pasted content (HTML, logs, stdout) | `looksLikePaste()` — HTML tags, repetitive line prefixes | 1000 chars |
| Plans/Architecture (assistant) | `looksLikePlan()` — "Implementation Plan", "### Task" | First 1000 + last 500 chars |
| Natural text (user) | Default — may contain instructions, preferences | 3000 chars |
| Natural text (assistant) | Default — conclusions in first paragraphs | 1500 chars |
| Tool results / bash output | Always truncated | 200 chars |
| Thinking blocks | Removed completely | 0 |

Content-aware truncation reduces Pass 1 input before extraction.

### Search modes

| Tool | Method | Best for |
|------|--------|----------|
| `search()` | FTS5 full-text (messages.db) | Exact keywords, error messages, specific terms |
| `deep_search()` | FTS5 over all message types incl. thinking blocks and command outputs | Debugging context, reasoning chains |
| `hybrid_search()` | BM25 + Vector (Reciprocal Rank Fusion) over learnings | Semantic + keyword combined — default recommendation |
| `docs_search()` | BM25 + Vector hybrid (Reciprocal Rank Fusion) over doc_chunks | Framework docs, API references, semantic cross-language matching |

### Supersede chain resolution
When hybrid_search hits a superseded learning, it automatically follows the supersede chain (max 10 hops) to return the active successor instead. Junk-marked learnings (`superseded_by = -1`) and resolved ones (`-2`) are dropped entirely — they enter the redirect map but have no valid successor.

### FTS5 Query Safety
Search terms containing hyphens are automatically quoted to prevent FTS5 from interpreting them as column operators (e.g. "Multi-Agent" → `"Multi" "Agent"` instead of `column:Agent` SQL error).

### Session Cleanup
Short, trivial sessions (< 6 messages) have their learnings automatically superseded on daemon startup via `SupersedeShortSessionLearnings`. Prevents noise from quick status checks or accidental sessions.

---

## 2. Learning Lifecycle

Learnings are the core knowledge unit — extracted insights from sessions that persist across conversations.

### 2.1 Categories

| Category | What it captures | Source |
|---|---|---|
| `explicit_teaching` | Things the user explicitly asked to remember | Extraction |
| `gotcha` | Errors with root cause and solution | Extraction + hook-failure |
| `decision` | Technical decisions with reasoning | Extraction |
| `pattern` | Workflows and approaches that worked | Extraction |
| `preference` | User preferences and work style | Extraction |
| `unfinished` | Incomplete work items with next steps. Sub-classified via `task_type`: `task` (concrete action), `idea` (consideration), `blocked` (waiting), `stale` (session abort) | Extraction |
| `relationship` | Working relationship context | Extraction |
| `pivot_moment` | Turning points — the exact quote or moment that shifted perspective (weight 1.6, highest) | Extraction |
| `strategic` | Long-term goals and business context | `remember()` MCP only |
| `narrative` | Session summaries and milestone narratives | Daemon-generated (not in `validCategories` — created programmatically) |
| `recurrence_alert` | Recurring pattern warnings from cluster analysis | Phase 4.6 auto-generated (not in `validCategories` — created programmatically) |
| `pulse` | CC `/recap` session summaries captured from `away_summary` JSONL events when user returns after idle | Parser + Indexer (automatic via fsnotify watcher) |

### 2.2 Saving: `remember()`

Manually save knowledge via the MCP tool. Supports structured V2 metadata:

| Field | Purpose |
|-------|---------|
| `text` | The knowledge content (required) |
| `category` | See categories above |
| `project` | Project scope |
| `source` | `user_stated`, `claude_suggested`, `agreed_upon` |
| `supersedes` | ID of learning this replaces |
| `entities` | Files, systems, people affected |
| `actions` | Commands or operations involved |
| `trigger` | When should this knowledge activate? |
| `context` | Why/when is this relevant? |
| `domain` | `code`, `marketing`, `legal`, `finance`, `general` |
| `task_type` | Only for `unfinished`: `task`, `idea`, `blocked`, `stale` |
| `anticipated_queries` | 3-5 concrete search phrases for better vector retrieval |
| `model` | Optional: exact model of the calling agent |

**Pre-Admission Dedup:** Before inserting, `remember()` checks via TokenSimilarity (Jaccard ≥ 0.5) if a similar learning already exists. If yes: bumps `match_count` instead of creating a duplicate. Explicit `supersedes` calls bypass this check.

**Fresh Remember Injection:** ~~DEPRECATED (proxy path)~~ — The proxy-side `pop_recent_remember` injection is disabled. It caused echo-loops: Claude saved a learning via `remember()`, then saw it again next turn as `[yesmem fresh memory]`, reacting to its own output. Since `remember()` MCP returns the learning content as `tool_result`, the content is already in the conversation context — making the proxy injection redundant. The `assemble.go` (OpenClaw/HTTP-API) path still has `pop_recent_remember` for non-MCP environments where `tool_result` feedback doesn't exist.

### 2.3 Source Attribution

Every learning carries a `source` field:

| Source | Meaning |
|---|---|
| `user_stated` | User explicitly said this (via `remember()` or direct instruction) |
| `claude_suggested` | Claude proposed this, user didn't object |
| `agreed_upon` | Both discussed and agreed on this |
| `llm_extracted` | Automatically extracted from session content |
| `hook_auto_learned` | Learned from a Bash failure (auto-escalation eligible) |

This prevents Claude from echoing its own suggestions back as user preferences.

### 2.3a Origin Tool & Trust Multiplier (since 2026-04-30)

Independent from `source` (who authored), every learning records `origin_tool` (how it was captured) and a per-origin trust multiplier applied during scoring (`internal/models/scoring.go` — `OriginMultiplier`):

| Origin | Captured by | Multiplier |
|---|---|---|
| `user` | Direct `remember()` MCP call from a user-driven session | 1.0 |
| `file_read` | Bash hook learned from cat/grep on a file the user opened | 0.9 |
| `bash_command_input` | Bash hook learned from a successful command the user typed | 0.7 |
| `llm_extracted_session` | Background extraction pipeline summarising a session | 0.6 |
| `cap_*` | Generated by a capability handler (e.g. `cap_save_analysis`) | 0.5 |
| `web_external` | Pulled from outside the user's environment (web fetch, etc.) | 0.4 |
| _(unknown / legacy)_ | Default for pre-2026-04-30 records | 0.8 |

Multiplier is applied after BM25 + vector fusion in `hybrid_search`. User-stated learnings outrank LLM-extracted ones at parity match. End-to-end smoke covered by `internal/daemon/integration_origin_smoke_test.go`. The `remember()` MCP tool exposes `origin` as an explicit parameter so cap handlers can tag their writes.

### 2.3b Learning Lineage

Every learning tracks which messages it was extracted from:
- `source_msg_from` / `source_msg_to` — message index range in the original session
- Schema v0.53: stored as INTEGER columns, default -1 (sentinel for non-extraction learnings)
- **Chunker propagation:** `ChunkMessages` carries `FromMsgIdx`/`ToMsgIdx` per chunk → batch extraction inherits range → fork reflection propagates to extracted learnings
- **Fork coverage:** `fork_coverage` table records which message ranges were already processed per session, preventing duplicate extraction

### 2.4 Learning V2: Structured Objects

Each learning carries rich metadata for better retrieval:

| Field | Description |
|------|-------------|
| `content` | Core knowledge |
| `context` | Why/when relevant |
| `entities` | Affected files, systems, people (junction table) |
| `actions` | Relevant commands/operations (junction table) |
| `keywords` | Explicit search terms (junction table) |
| `anticipated_queries` | 3-5 concrete search phrases for better vector retrieval (junction table) |
| `trigger_rule` | When should this knowledge activate |
| `domain` | code / marketing / legal / finance / general |
| `importance` | 1-5 scale (auto-assigned from source) |
| `task_type` | Sub-classification for `unfinished`: `task`, `idea`, `blocked`, `stale` (auto-extracted by LLM, manually settable via `remember()`) |
| `emotional_intensity` | 0.0–1.0 (session mood) |
| `session_flavor` | One-liner session character |
| `embedding_text` | Enriched text from all V2 fields + anticipated queries for better vector search |

### 2.5 Extraction Pipeline (Multi-Phase)

When a session ends, the daemon runs a multi-pass LLM pipeline:

**Phase 2 — Extraction** (configurable model, default: Sonnet)
- Chunks session messages (25k tokens each) via `ChunkMessages`
- Content-aware pre-processing reduces input by ~70%
- Extracts structured V2 learnings from chunks
- All categories including `pivot_moment` (max 1-2 per session, only real direction changes)
- V2 metadata: entities, actions, keywords, trigger_rule, domain, context
- **Anticipated queries:** 3-5 concrete search phrases generated per learning for better vector retrieval
- **Deadline parsing:** `trigger_rule: "deadline:YYYY-MM-DD"` auto-detected from user commitments ("bis Freitag", "ich mach das morgen") → sets `expires_at`. Current date injected into extraction prompt for relative→absolute conversion. Also works via manual `remember(trigger="deadline:...")`.

**Phase 2.5 — Embedding** (sync, SSE)
- Embeds new learnings into 512d vector store immediately after extraction

**Phase 3 — Quality Refinement** (configurable model, default: Sonnet)
- Rule-based pre-dedup: `IsSubstanzlos()` filters fragments (<15 chars, JSON, code blocks), `BigramJaccard()` >0.85 triggers near-duplicate supersede — eliminates ~40% of Evolution LLM calls
- LLM-based dedup via TokenSimilarity (Jaccard ≥0.5 pre-filter) + Embedding (≥0.92)
- Confidence rating
- Contradiction detection and resolution

**Phase 3.5 — Auto-Embed Remaining**
- Embeds any learnings not yet embedded from previous phases

**Phase 4 — Narrative Generation** (configurable model, default: Opus)
- Session handover narratives with concrete line numbers, commit hashes, error quotes
- Project profiles
- Persona trait extraction

**Phase 4.5 — Learning Clustering**
- Agglomerative clustering on learning embeddings (cosine 0.85)

**Phase 4.6 — Recurrence Detection**
- Detects recurring patterns in learning clusters

**Phase 5 — Profile Generation**
- Auto-generates project profiles from accumulated session data

**Phase 6 — Persona Signals**
- Updates persona traits from session patterns

### 2.6 Evolution & Supersede

The evolution system manages knowledge lifecycle:

**4 Action Types:**

| Action | Effect |
|--------|--------|
| `supersede` | New replaces old (with trust gate) |
| `update` | Merge content from two learnings — enriches without destroying |
| `confirmation` | Confirms existing → bumps `use_count` and stability |
| `independent` | No conflict |

All 4 action types are available in both inline (per-learning) and bulk (per-category) evolution paths. Previous versions restricted bulk evolution to `supersede`-only, causing knowledge destruction instead of consolidation.

**Rule-Based Pre-Dedup:**
- **Cross-chunk:** BigramJaccard runs over ALL learnings in a category BEFORE chunking into 50-item batches — eliminates duplicates invisible within individual chunks
- **`IsSubstanzlos()`** — filters fragments (<15 chars, JSON, code blocks, <4-word sentences)
- **`BigramJaccard()` > 0.85** — near-duplicate supersede with actual winner ID (not -1 sentinel)
- Eliminates ~40% of Evolution LLM calls

**BM25-Based Conflict Detection:**
`resolveConflicts()` uses BM25 text search on the new learning's content to find semantically related conflicts across the entire history. Falls back to recency-based lookup if BM25 yields < 3 results. Replaces the old LIMIT 30 recency approach that missed old contradictions.

**Chain Resolution:**
- Recursive CTE query resolves entire supersede chain in one SQL call (replaces N+1 per-hop queries)
- Max 10 hops depth
- Cycle detection before setting `superseded_by` — checks if winner already appears in loser's chain, prevents A→B→A loops

**Contradiction-Boost (Pearce & Hall):**
When `remember()` supersedes an existing learning, the correcting learning gets boosted as an "Unexpected Outcome" — the strongest learning signal:
- `importance = max(old_importance, 4)`
- `use_count = 1` (immediately marked as used)
- `ebbinghaus_stability = 45 days` (instead of default 30)
- Superseded learning gets `noise_count += 1`

Corrections outrank the knowledge they replace immediately, not after several sessions.

**Trust-Based Supersede Resistance:**

```
trust_score = (0.5 + log1p(use_count + save_count*2)) × source_multiplier × (importance / 3.0)  <!-- save_count is double-weighted -->
```

| Trust Level | Score | Supersede Behavior |
|---|---|---|
| Low | < 1.0 | Immediate supersede |
| Medium | 1.0 – 3.0 | Supersede + logged warning |
| High | ≥ 3.0 | `pending_confirmation` — old learning stays active until user confirms |

**Temporal Validity:**
- `valid_until` — when a learning stopped being true (set on supersede)
- `created_at` — serves as de facto "valid from" timestamp
- `supersedes` / `superseded_by` — explicit chain linking
- Navigable chains: `GetSupersededChain(id)` walks the full history

**Auto-resolution:** Unfinished tasks auto-archive after configurable TTL (default: 30 days).

### 2.8 Quarantine & Skip-Indexing

Noise control for entire sessions:

- **`quarantine_session(session_id)`** — marks all learnings of a session as quarantined. Quarantined learnings are excluded from vector search and BM25 results (`quarantined_at IS NULL` filter).
- **`unquarantine_session(session_id)`** — restores quarantined learnings
- **`skip_indexing(session_id)`** — prevents extraction pipeline from processing a session. Extractor checks `skip_extraction` before each session.
- Use case: noisy sessions (testing, debugging, accidental data) that would contaminate the knowledge base

### 2.9 Scoring System

#### 6-Count Model

Each learning tracks 6 independent counters:

| Counter | Incremented when | Purpose |
|---------|-----------------|---------|
| `match_count` | BM25 search hit | Search relevance signal |
| `inject_count` | Injected into briefing/context | Visibility (not usefulness) |
| `use_count` | Claude actually uses the learning (via Signal Bus) | Direct value signal |
| `save_count` | Gotcha warning changed user behavior (proxy_state heuristic) | ×2 weight (intentional) |
| `fail_count` | Triggered error | Auto-escalation at ≥5 |
| `noise_count` | Signal reflection: learning was irrelevant | Negative signal |

**Why 5 counts:** Single `hit_count` conflated visibility with usefulness → zombie learnings that scored high just from being seen.

#### Scoring Formula

```
score = categoryWeight × TurnBasedDecay × useBoost × noisePenalty
        × precisionFactor × explorationBonus × emotionalBoost × importanceBoost
        × fixationPenalty
```

| Component | Formula | Effect |
|-----------|---------|--------|
| **TurnBasedDecay** | `exp(-turns_since / effective_stability)` | Turn-based forgetting curve (project turns, not wall-clock days). `effective_stability = stability × (1 + log2(1 + use_count + save_count×2))` |
| **useBoost** | `1 + log2(1 + use_count + save_count×2)` | Only genuine utility drives scoring |
| **noisePenalty** | `1/(1 + noise_count×0.15)`, floor 0.4 | Penalizes repeatedly irrelevant learnings |
| **precisionFactor** | Ramp from inject 3→12, range 0.5–1.5 | Learnings shown but never used ("zombies") are penalized |
| **explorationBonus** | 1.3× for learnings with <3 injections | New learnings get a chance before competing |
| **emotionalBoost** | `1.0 + intensity × 0.3` (max 30%) | Learnings from intense sessions score higher |
| **importanceBoost** | Based on importance field (1-5) | User-stated learnings score higher than auto-extracted |
| **fixationPenalty** | Ratio-based, see Session Quality Signal below | Learnings from fixated sessions score lower |
| **Decay floor** | 10% universal, 50% for user_stated and user_override | No learning ever drops to zero |

#### Contextual Scoring

`ComputeContextualScore()` adds context-dependent boosts:
- **Project match:** Turn-graduated: 1.5× (<10 turns), 1.3× (<50 turns), 1.1× (older). Uses suffix/basename matching for short project names.
- **Entity match** in filename/dirname: 1.4× (min 4 chars)
- **Domain match:** 1.2×
- Max combined: 2.184×

#### Project-Recency Boost (Additive)

Same-project learnings get an additive time-graduated boost in hybrid_search:
- Created < 48h ago: **+8 points**
- Created < 7 days ago: **+5 points**
- Older: **+3 points**

Additive instead of multiplicative to keep scores within the 0-100 range. Uses suffix/basename matching so short project names work.

#### Injection-Time Dedup

BigramJaccard (threshold 0.70) removes near-duplicate learnings before injection — prevents double-counting.

#### Session Quality Signal (Fixation Detection)

Detects when Claude gets stuck in fixation loops and penalizes learnings from those sessions proportionally:

**3 Detectors** (pure pattern-matching on message history, no LLM call):

| Detector | Threshold | What it catches |
|----------|-----------|-----------------|
| **Consecutive Errors** | ≥ 8 in a row | Claude retries the same failing command |
| **Edit-Build-Error Cycles** | ≥ 6 cycles | Edit → Build → Error → Edit → Build → Error loop |
| **File Retries** | ≥ 10 edits same file | Claude edits the same file over and over |

**Ratio-based scoring** — proportional to session length:
```
fixation_ratio = fixation_affected_messages / total_messages
```

| Fixation Ratio | Penalty | Meaning |
|----------------|---------|---------|
| < 5% | 1.0 | Normal debugging — no penalty |
| 5–15% | 0.95 | Mild fixation — barely noticeable |
| 15–30% | 0.85 | Moderate fixation — mild penalty |
| > 30% | 0.7 | Pathological fixation — significant penalty |

Conservative by design: 6-7 build loops in a 500-message session are normal development, not fixation. Only pathological patterns trigger meaningful penalties.

**Pipeline:**
- Computed at **collapse time** (full message history available before stubbing)
- Persisted as `fixation_ratio` on `sessions` table via daemon RPC
- Enriched onto learnings before scoring in `get_learnings` and briefing

### 2.8 Embedding & Vector Store

- **Model:** SSE (Stable Static Embeddings) — eigenes multilinguales Embedding-Modell, kein ONNX
- **Pipeline:** 4 Schritte: WordPiece Tokenize → EmbeddingBag (Lookup + Mean Pool) → Separable DyT Normalization → L2 Normalize
- **Dimensionen:** 512d (nicht 384d)
- **Assets:** `sse_multilingual_512d.bin` (104MB weights) + `sse_dyt_512d.bin` (6KB DyT parameters) + `tokenizer.json` (2.5MB WordPiece vocab)
- **Embedded in binary** via `go:embed` — no download, no external dependencies, no ONNX runtime
- **Multilingual:** 100+ Sprachen (bert-base-multilingual-uncased Vocab, eigenes Static-Embedding-Format)
- **Pure Go:** Keine CGo-Abhängigkeit, keine C-Runtime — `SSEProvider` in `internal/embedding/sse.go`
- **Persistent store:** `~/.claude/yesmem/vectors/`
- **Async embedding:** Background worker queue, non-blocking
- **`BuildEmbeddingText()`** generates enriched text from all V2 fields for better vector search

---

## 3. MEMORY.md Auto-Generation

YesMem maintains a `MEMORY.md` file per project that Claude Code loads automatically via its built-in auto-memory feature. Contains only a **narrative redirect** to YesMem's MCP tools — no learning content is written into MEMORY.md.

### What it generates
- A usage guide pointing Claude to `hybrid_search()`, `search()`, `deep_search()`, `query_facts()`, `expand_context()`, `remember()`, `get_session()`, `get_learnings()`, `related_to_file()`, `set_plan()`, `update_plan()`, `get_plan()`, `complete_plan()`
- Injected below the `# --- YesMem Auto-Generated ---` marker
- User-written content above the marker is preserved

### Why no learning content
Early versions wrote Sackgassen, Patterns, Preferences, and Open Tasks directly into MEMORY.md. This was removed because:
- MEMORY.md is loaded every turn (~200 line limit) — learning content bloated it
- Learnings are better served dynamically via briefing and associative context (scored, decayed, deduplicated)
- Static snapshots in MEMORY.md became stale immediately after new extractions

Triggered by `GenerateAllMemoryMDs()` via CLI commands (`yesmem claudemd`). Note: The `OnMutation` callback in the daemon handler is defined but not wired — runtime auto-generation does not currently happen.

### 5b. AGENTS.md Auto-Generation

For opencode users, yesmem generates `AGENTS.md` — an opencode-specific instruction file (`yesmem claudemd --opencode`). While MEMORY.md is Claude Code's project instruction file with condensed learnings, AGENTS.md is the opencode equivalent with:

- **Tool preference directives** — instructions to use yesmem MCP tools (search, learnings, wiki) before raw grep/cat/find
- **OpenCode-relevant structural info** — project name, session history summary, open tasks, wiki path
- **Same database** — AGENTS.md is generated from the same yesmem.db learnings as MEMORY.md, just formatted for opencode's tool and instruction structure

Generated at `CLAUDE.md` location (since opencode reuses the Claude Code convention), or alongside the project root's existing `CLAUDE.md`.

---


---

## 4. Structured Fact Search (`query_facts`)

Structured search on learning metadata — JOINs on junction tables (`learning_entities`, `learning_actions`, `learning_keywords`) with LIKE matching.

### Parameters

| Parameter | Type | Description |
|-----------|------|-------------|
| `entity` | string | LIKE match on entities (files, systems, people) |
| `action` | string | LIKE match on actions (commands, operations) |
| `keyword` | string | LIKE match on keywords/tags |
| `domain` | string | Exact match: code, marketing, legal, finance, general |
| `project` | string | Project filter |
| `category` | string | Category filter (gotcha, decision, pattern, etc.) |
| `limit` | number | Max results (default 20) |

### Examples
- `query_facts(entity="proxy.go")` → all learnings about proxy.go
- `query_facts(action="git push")` → all learnings about git push
- `query_facts(domain="marketing", keyword="SEO")` → marketing/SEO learnings

### Properties
- At least one filter required (entity, action, keyword, domain, or category)
- Filters on active learnings only (`superseded_by IS NULL`, not expired, not quarantined)
- Results include full junction data (entities, actions, keywords, anticipated_queries) via `LoadJunctionData()`
- Sorted by `importance DESC, created_at DESC`

---

## 5. Context Expansion (`expand_context`)

Active expansion of archived/compacted conversation parts. Bridges stub/collapse blocks with full-text retrieval — Claude can explicitly request archived context instead of relying on automatic `reexpandStubsFor`.

### Two modes

| Mode | Parameter | How it works |
|------|-----------|-------------|
| **Search** | `query="rules injection"` | Delegates to `deep_search` — finds relevant archived messages by text |
| **Range** | `message_range="200-250"` | Loads compacted blocks covering the given message index range |

### Properties
- Uses `proxyCallWithThreadID` — thread context injected automatically
- Search mode returns top 5 results with snippets
- Range mode returns all compacted blocks intersecting the range
- Complements `get_compacted_stubs` (which lists stubs) and `deep_search` (which searches raw messages)

---

## 6. Knowledge Gaps & Self-Feedback

### Knowledge Gaps
- `track_gap(topic, project?)` — record topics Claude couldn't answer
- `resolve_gap(topic, learning_id?)` — mark gap as filled
- `get_active_gaps(project?, limit?)` — show open gaps
- Auto-resolve: when `remember()` content matches a gap topic, it's resolved
- Gaps surface in briefing as "Themen ohne gespeicherte Erfahrung" with hit count and age

### Knowledge Gap Review
- **Daily daemon timer** — `runGapReviewDaemon()` reviews unreviewed gaps automatically
- **CLI:** `yesmem gap-review [--project] [--dry-run] [--limit]`
- **Context-enriched** — each gap is enriched with existing related learnings via BM25 search before LLM review
- **Three verdicts:** `keep` (genuine gap), `resolved` (existing learnings answer it), `noise` (not a real gap — deleted)
- **Noise patterns:** lost context references, meta-reflection, implementation lookups, incomplete response logs
- **Differentiated metrics:** `yesmem stats` and `yesmem benchmark` show gaps as `open | auto-resolved | review-resolved`

### Self-Feedback
- `get_self_feedback(days?)` — corrections, patterns, and feedback from recent sessions
- Captures moments where Claude self-corrected or received correction
- Used for meta-cognitive improvement

---

## 7. Project Intelligence

### Project Profiles
Auto-generated living portraits of each project:
- Character (what the project is)
- Tech stack
- Minefields (known gotchas)
- Clean parts (what works well)
- Testing status
- Deployment details
- Special features

Regenerated as new sessions accumulate.

### Cross-Project Awareness
- `list_projects()` — all projects with session counts and last activity
- `project_summary(project, limit?)` — chronological summary
- `related_to_file(path)` — which sessions touched a specific file
- `get_coverage(project)` — file coverage map
- Briefing includes 90-day cross-project activity

---

## 8. Export & Import

- `yesmem export [path]` — export all learnings + persona to JSON (default: `yesmem-export.json`)
- `yesmem import <file>` — import learnings from JSON export

Note: Manually saved learnings (`remember()` calls) are NOT recoverable from session data — only `llm_extracted` learnings can be regenerated via re-extraction. Export is the only backup for manual learnings.
## 9. Forked Agent Proxy — Background Learning Extraction

The proxy spawns lightweight "forked" API calls after each assistant response to extract learnings, evaluate injected memories, and detect contradictions — without blocking the main conversation.

### Architecture

The forked agent system runs entirely inside the proxy (`internal/proxy/`). After each response completes, `fireForkedAgents()` runs asynchronously in a goroutine. It clones the current conversation context (via `buildForkRequest`) and sends it to a cheaper model (Haiku by default) with a specialized extraction prompt.

```
User message → Anthropic API → Assistant response → [async] Fork fires
                                                          ↓
                                                    Haiku extracts learnings
                                                          ↓
                                                    Daemon stores/evaluates
```

### Components

| File | Responsibility |
|------|---------------|
| `fork_state.go` | Per-thread token growth tracking + failure counting (gate decisions) |
| `forked_agent.go` | ForkConfig registry, buildForkRequest (context cloning), doForkCall, fireForkedAgents orchestration |
| `fork_extract.go` | Extraction prompt (i18n), JSON parser, ParseResult handler, struct definitions |

### Fork Types

Currently one fork type: `extract_and_evaluate`. Designed to be extensible — new fork types register a `ForkConfig` with their own Gate, Prompt, Model, MaxTokens, and ParseResult functions.

### Gating

Forks don't fire on every turn. Gate conditions prevent waste:
- **Token growth trigger:** Only fires when `TokensUsed` exceeds configured threshold (default 20k tokens)
- **Failure tracking:** After 3 consecutive failures, that fork type is disabled for the thread
- **Minimum message count:** Requires enough conversation context to be meaningful
- **Quality filter:** Extracted learnings below a quality threshold are discarded. Importance scoring (0.0–1.0) and emotional_intensity are extracted alongside content to prioritize high-value insights.

### Session-Aware Reflection (v0.48)

The extraction prompt is a structured 3-task reflection in the session language (German defaults, configurable via i18n):

**Task 1 — Update Learnings:**
- Receives all previous fork-extracted learnings for this session (`GetForkLearnings`)
- LLM evaluates: which are confirmed, revised, invalidated, or new?
- Status field: `new | confirmed | revised | invalidated`
- Invalidated learnings are skipped (not stored)

**Task 2 — Evaluate Injected Memories:**
- Only fires when associative/briefing context was injected (`InjectedIDs`)
- For each injected learning: `impact_score` (0.0-1.0) + verdict + action
- Verdicts: `useful | critical_save | outdated | noise | wrong | irrelevant`
- Actions: `boost | save | supersede | noise | flag | skip`
- Impact scores use running average (`UpdateImpactScore`) — builds signal over time

**Task 3 — Contradiction Detection:**
- Only fires when Task 2 fires (needs injected learnings to compare)
- Detects conflicts between injected memories or between memories and conversation decisions
- Both sides get `fail_count++` via `IncrementFailCounts`

### Data Flow

```
Fork fires → Haiku returns JSON → parseExtractionJSON
  ├─ learnings[]  → fork_extract_learnings → stored with source="fork"
  ├─ evaluations[] → fork_evaluate_learning → verdict actions + impact_score update
  └─ contradictions[] → fork_resolve_contradiction → fail_count++ both sides
```

### Impact Scoring

`impact_score` is a running average on the `learnings` table:
```
new_score = (old_score * count + new_value) / (count + 1)
```
Measures whether injected memories were actually used by Claude. 0.0 = ignored, 1.0 = central to an answer. Over multiple sessions, high-impact learnings float up, noise sinks.

### Schema

```sql
-- v0.48: impact scoring
ALTER TABLE learnings ADD COLUMN impact_score REAL DEFAULT 0.0;
ALTER TABLE learnings ADD COLUMN impact_count INTEGER DEFAULT 0;

-- v0.49: fork coverage (dedup preparation)
CREATE TABLE fork_coverage (
    session_id TEXT, from_msg_idx INT, to_msg_idx INT, fork_index INT
);
```

### Configuration

```yaml
forked_agents:
  enabled: true
  model: ""                    # empty = same model as main thread (default). Override: haiku, sonnet, opus
  token_growth_trigger: 20000  # min tokens before first fork
```

### Properties
- **Zero latency impact** — fires async after response, never blocks the user
- **Self-correcting** — invalidated learnings filtered, contradictions flagged, impact tracked
- **Session-aware** — each fork sees previous fork results, building cumulative understanding
- **Cost-efficient** — same model as main thread by default (configurable to cheaper models), gated by token growth, max 3072 output tokens
- **i18n** — prompt strings in `internal/briefing/i18n.go`, German defaults, YAML-overridable

---

