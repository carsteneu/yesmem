# YesMem — Features

> Long-term memory system for Claude Code.
> Not memory FOR an agent, but memory AS agent.

---

## Architecture Overview

YesMem runs as three cooperating processes plus a hook layer:

| Process | Role | Communication |
|---------|------|---------------|
| **MCP Server** | Tool interface for Claude Code (stdio) | Unix socket → Daemon |
| **Daemon** | Background service: indexing, extraction, search, RPC | Unix socket `~/.claude/yesmem/daemon.sock` + HTTP `127.0.0.1:9377` |
| **Proxy** | Infinite-thread context compression + associative context | HTTP `:9099` between Claude Code and Anthropic API |
| **Hooks** | Event-driven Claude Code integration | CLI subcommands called by Claude Code hooks |

The MCP server is a thin proxy — all logic lives in the daemon. Connection is lazy: MCP always starts successfully, connects to daemon on first tool call. External integrations (OpenClaw) connect via the HTTP API.

**Data directory:** `~/.claude/yesmem/` (3 DB files: `yesmem.db` ~260MB for learnings/sessions, `messages.db` ~1GB for raw messages + FTS5, `runtime.db` for proxy state)
**Binary:** `~/.local/bin/yesmem` (single Go binary, ~120MB with embedded SSE model)
**Dependencies:** All pure Go — no CGo, no C compiler needed. Single static binary.

---

## 1. Session Indexing & Full-Text Search

### What it does
Every Claude Code session (JSONL files under `~/.claude/projects/`) is automatically indexed by the daemon's file watcher. Messages are parsed, stored in a separate `messages.db` SQLite database, and indexed via FTS5 full-text search.

### Key properties
- **Zero-cost indexing:** Phase 1 requires no LLM calls — all messages go into SQLite/FTS5 for free
- **Separate messages.db:** Messages live in their own database file, keeping yesmem.db lean (~260 MB) for fast learnings/sessions queries. Messages.db grows independently and can be partitioned when >1 GB
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

Real data: 77% of session content is irrelevant for extraction. Content-aware truncation reduces Pass 1 input by ~70%.

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
trust_score = (0.5 + log1p(use_count + save_count)) × source_multiplier × (importance / 3.0)
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

#### 5-Count Model

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
score = categoryWeight × ebbinghausDecay × useBoost × noisePenalty
        × precisionFactor × explorationBonus × emotionalBoost × importanceBoost
        × fixationPenalty
```

| Component | Formula | Effect |
|-----------|---------|--------|
| **Ebbinghaus Decay** | `exp(-turns_since / effective_stability)` | Turn-based forgetting curve (project turns, not wall-clock days). `effective_stability = stability × (1 + log2(1 + use_count + save_count×2))` |
| **useBoost** | `1 + log2(1 + use_count + save_count×2)` | Only genuine utility drives scoring |
| **noisePenalty** | `1/(1 + noise_count×0.15)`, floor 0.4 | Penalizes repeatedly irrelevant learnings |
| **precisionFactor** | Ramp from inject 3→12, range 0.5–1.5 | Learnings shown but never used ("zombies") are penalized |
| **explorationBonus** | 1.3× for learnings with <3 injections | New learnings get a chance before competing |
| **emotionalBoost** | `1.0 + intensity × 0.3` (max 30%) | Learnings from intense sessions score higher |
| **importanceBoost** | Based on importance field (1-5) | User-stated learnings score higher than auto-extracted |
| **fixationPenalty** | Ratio-based, see Session Quality Signal below | Learnings from fixated sessions score lower |
| **Decay floor** | 10% universal, 50% for user_stated | No learning ever drops to zero |

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

## 3. Emotional Memory

Sessions aren't all equal — a frustrating debugging marathon that ends with a breakthrough is more memorable than a routine config change.

- **Emotional Intensity** (0.0–1.0) — rated per session during extraction
- **Scoring boost** — learnings from intense sessions score up to 30% higher (`emotionalBoost = 1.0 + intensity × 0.3`)
- All learnings from a session inherit its intensity score
- **Pivot Moments** — turning points extracted as `pivot_moment` category (highest weight 1.6). Format: direct quote + what changed. Max 1-2 per session. Never faded from briefing regardless of age.

---

## 4. Briefing System

At every session start, a briefing is generated and injected as system context.

### Structure
1. **Awakening Narrative** — "Ich bin wieder da. N Mal jetzt..." — perspective-shift for identity continuity
2. **User Profile** — auto-synthesized ~500 token profile of the user (background, expertise, working style, values). Generated by `synthesizeUserProfile()` every 24h from preference/explicit_teaching/pattern/relationship learnings. Stored in `persona_directives` with `user_id='user_profile'`.
3. **Persona Directive** — who Claude is in this relationship (behavioral rules, communication style)
4. **Session Pulse** — momentum/mood/next-action from last sessions
5. **Recent Sessions** — 3 detailed + older as one-liners (with narrative handovers, project-filtered)
6. **Learnings by Category** — max 5 per category, sorted by relevance, with `[ID:xxx]` prefix for signal tracking
7. **Milestones** — "Unsere Meilensteine" — most emotionally significant sessions (intensity > 0.5, ranked by `intensity × (1 + use_count × 0.2)`, chronological, with intensity labels: Durchbruch ≥0.8, intensiv ≥0.6, lebendig ≥0.4)
8. **Open Tasks** — unfinished learnings requiring attention, filtered by `task_type`: only `task`, `blocked`, and legacy (empty) items shown. Ideas and stale entries counted separately as summary line: `"(+ X ideas, Y stale — get_learnings(category='unfinished', task_type='idea') zum Abrufen)"`. Output format: `[#ID project unfinished:task 2d ago] Content...`
9. **Cross-Project Activity** — 90-day window for cross-connections
10. **Gap Awareness** — "N more learnings per category" + top-5 other projects with depth
11. **Recurrence Alerts** — recurring pattern warnings from cluster analysis (Phase 4.6)
12. **Metamemory** — knowledge quality self-assessment: solid (save_count ≥3), fragile (noise_count >3), expired (valid_until passed)

### Properties
- **Token budget:** 5000 tokens max (configurable)
- **Dedup:** Jaccard + Containment similarity (threshold 0.4)
- **Stop-word filtering:** Language-aware (auto-detected from `$LANG`, uses bbalet/stopwords)
- **Refined briefings:** Cached via content hash — regenerated only on changes. Delivered via both CLI (`runBriefing`) and SessionStart Hook (`runBriefingHook`) paths.
- **Multilingual:** Setup auto-detects system language, briefing strings translated via LLM

### Session Narratives

Instead of dry bullet-point summaries, YesMem generates narrative handovers — short stories with concrete line numbers, commit hashes, and handover instructions. Project-filtered: in environments with 10-20 concurrent Claude sessions, each gets context relevant to its project.

---

## 5. MEMORY.md Auto-Generation

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

---

## 6. Persona Engine

Builds a persistent identity profile from session history.

### Dimensions (6)
1. **Communication** — humor, language, verbosity
2. **Workflow** — speed preferences, debug approach, iterative vs. comprehensive
3. **Expertise** — languages, frameworks, domain strengths
4. **Context** — active projects, domain focus
5. **Boundaries** — what not to do
6. **Learning Style** — examples, visual, documentation

### Pipeline
1. **Bootstrap** (`yesmem bootstrap-persona`) — analyzes session history, extracts traits
2. **Synthesis** (`yesmem synthesize-persona`) — converts traits → structured directive (uses Opus)
3. **Injection** — directive appears BEFORE all other briefing sections
4. **Trait storage:** `persona_traits` table (dimension, trait_key, value, confidence, source)
5. **Trait normalization:** Prefix stripping on save — `expertise.go` → `go`, `communication.language` → `language`. Prevents dimension prefix leaking into trait keys.
6. **Hash tracking:** Regenerate directive only if traits changed
7. **Automatic Trait Dedup** — embedding-based cosine similarity (threshold 0.85) finds semantically duplicate traits within the same dimension and supersedes the weaker one. Runs in bootstrap-persona (Phase 2.5) and Phase 6 in the daemon.

### Manual override
`set_persona` MCP tool allows user overrides with highest priority.

### User Profile Synthesis

Auto-generated ~500 token profile of the user — background, expertise, working style, values, how they collaborate with Claude. Synthesized from `preference`, `explicit_teaching`, `pattern`, and `relationship` learnings plus expertise traits.

- **Function:** `synthesizeUserProfile()` in `internal/daemon/persona.go`
- **Schedule:** Runs after `synthesizePersonaDirective()` in the extraction flow. 24h time guard prevents excessive re-synthesis. Hash check over input data skips when nothing changed.
- **Storage:** Reuses `persona_directives` table with `user_id = 'user_profile'` — no separate schema
- **LLM:** Quality model (Sonnet/Opus), ~500 tokens output, German, third person
- **Injection:** In briefing after Awakening Narrative, before Persona Directive — every new instance immediately knows who the user is

---

## 6b. Pinned Learnings (Bookmarks)

Pin important instructions, rules, or context that must survive every compression cycle. Works like a bookmark — pinned content is visible in EVERY turn, regardless of context compaction, stubbing, or refinement.

- **`pin(content, scope?, project?)`** — pin an instruction (session or permanent)
- **`unpin(id, scope?)`** — remove a pin by ID
- **`get_pins(project?)`** — list all active pins
- **Two scopes:**
  - `session` (default) — survives until `/clear`, lives in proxy memory
  - `permanent` — survives everything, stored in DB across sessions
- **Injection:** pins are rendered via `FormatPinnedBlock()` in the briefing system block (message index 0) — never collapsed, never stubbed
- **Format:** `- [pin:42] The instruction...` / `- [pin:17 permanent] Permanent instruction...`
- **Use cases:** temporary coding rules, debug reminders, project constraints, anything that must not be lost mid-session

---

## 6c. Skill Evaluation

The proxy detects when skill evaluation is relevant and injects a `[skill-eval]` block into the conversation:

- **`isUserInputTurn()` guard** — skill-eval only injected on real user input messages, not on continuation requests (tool results)
- **`buildSkillEvalBlock()`** — assembles available skills from Claude Code settings into a compact evaluation prompt
- **Collapse-protected** — old skill-eval blocks stripped before collapse via `stripSkillHints()`, always re-injected fresh

---

## 7. Documentation Management

Domain-agnostic knowledge ingestion for arbitrary documentation (code, marketing, legal, finance, PubMed, company knowledge).

### Ingest Pipeline
1. **File discovery:** Walk directories, find `.md`, `.txt`, `.rst`, `.pdf`
2. **Format-aware chunking:** Detects file format and applies the appropriate parser:
   - **Markdown** (`.md`, `.txt`): goldmark AST parser extracts headings
   - **reStructuredText** (`.rst`): Custom heading parser recognizes underline/overline patterns (`===`, `---`, `~~~` etc.). Level determined by order of first adornment character appearance. No external dependency.
   - Both formats: Cuts along heading boundaries, each chunk carries full `heading_path` (e.g. "Messenger Component > Configuration > Retry Strategy"). Sections <200 chars merged with parent, >8000 chars split at paragraph boundaries.
3. **Rich metadata extraction** per chunk:
   - **Markdown**: `languages` (from fenced code blocks ` ```php `), `code_example`, `has_table`
   - **RST**: `languages` (from `.. code-block:: php`), `deprecated_since`, `version_added`, `version_changed`, `admonition` (warning/tip/note/...), `rst_entities` (`:class:`, `:func:`, `:method:` cross-references), `rst_doc_refs` (`:doc:` links), `code_example`, `has_table`
   - Stored as JSON in `doc_chunks.metadata`, indexed in FTS5
4. **Content hashing:** `source_hash` prevents re-ingesting unchanged files
5. **FTS5 indexing:** Porter stemmer + unicode61 + `tokenchars '_-.'` — technical terms like `text_editor`, `go1.24` indexed as single tokens
6. **SSE embedding:** Each chunk gets a 512d vector (SSE with DyT normalization) for semantic search
7. **Git-aware sync:** Detects `.git` in path → `git pull` before import

### Skill Mode
- Skills are handled via a separate handler (`handler_skills`), not through the `ingest_docs` MCP tool
- Stores complete skill file as `full_content` on `doc_sources` for exact re-injection
- Chunks are created normally for search

### Search Architecture
`docs_search()` runs a **BM25-first search** with vector fallback:
- **BM25** (FTS5): term-existence filter removes dead terms, IDF-sort (rarest first), tiered AND-fallback (5→4→3→2 terms)
- **Vector** (SSE 512d): cosine similarity — used as **fallback when BM25 returns 0 results**
- **RRF merge**: Reciprocal Rank Fusion combines both ranked lists when both have results; otherwise returns whichever method produced results

### Doc versioning
Runs via keywords (`symfony:5`, `symfony:7`) and embedding match — no separate version system. Code context + vector search automatically determine relevant version.

### CLI Commands
```
yesmem add-docs  --name symfony --path /docs/symfony --version 7.2 --project myapp
yesmem add-docs  --name twig --path /docs/twig --trigger-extensions ".twig,.html.twig"
yesmem sync-docs --name symfony          # re-sync single source
yesmem sync-docs --all                   # re-sync everything
yesmem list-docs --project myapp         # show registered sources
yesmem remove-docs --name symfony        # delete source + chunks
```

---

## 8. Infinite Thread (Proxy)

The proxy sits between Claude Code and the Anthropic API, enabling unlimited conversation length.

### Connection Flow

```
Claude Code → ANTHROPIC_BASE_URL=http://localhost:9099 → yesmem proxy → api.anthropic.com
```

Thread identification uses the `X-Claude-Code-Session-Id` header sent by Claude Code. Each unique session ID gets isolated proxy state (stubs, collapse cutoff, sawtooth cache, keepalive timers).

### Stub Cycle Pipeline

When estimated tokens exceed the threshold, the proxy runs:

```
StripReminders → stripSkillHints → CompressContext → EagerStubToolResults
→ CalcCollapseCutoff → CollapseOldMessages
→ ReplaceSystemBlock (Narrative) → StripOldNarratives → reexpandStubs
→ UpgradeCacheTTL → EnforceCacheBreakpointLimit
```

The proxy also rewrites the system prompt early in the pipeline (see §12e):
```
StripCLAUDEMDDisclaimer → StripOutputEfficiency → StripToneBrevity
→ InjectAntDirectives → InjectCLAUDEMDAuthority → InjectPersonaTone
```

### Context Compression (CompressContext)

Runs on every request before the main stubbing pipeline. Targets old, already-processed content:

### Eager Tool-Result Stubbing (EagerStubToolResults)

Proactively compresses large tool_results in the **fresh tail** (uncached zone after the frozen stub boundary) once Claude has processed them. This exploits a key architectural insight: messages after the frozen boundary are not yet cached by Anthropic's prompt cache, so modifying them costs nothing.

| Property | Value |
|----------|-------|
| Threshold | >500 tokens |
| Condition | tool_result has a following assistant turn (already processed) |
| Cache impact | Zero — operates exclusively in uncached fresh tail |
| Savings | ~25% context reduction per request in active sessions |

**Rule-based summaries per tool type:**

| Tool | Stub format |
|------|-------------|
| Read | `[Read path — N lines \| func1(), func2()]` — file path + extracted function signatures |
| Grep | `[Grep 'pattern' in path — N matches]` — search pattern + match count |
| Bash | `[Bash: command — N lines]` + head/tail 3 lines — command + first/last output |
| Glob | `[Glob 'pattern' — N results]` + first 10 paths |
| Agent | `[Agent: description — first 200 chars]` |
| Other | `[ToolName result — N lines archived]` |

The stubs preserve `tool_use_id` for API pair validation. Re-reading the original content is always possible by calling the tool again — stubs are pointers back to the source, not lossy compression.

Before stubbing or collapse, a compression pass removes low-value content:
- Only targets messages outside the `keepRecent` window (default 10)
- **Thinking blocks** ≥ 500 tokens → replaced with `[context compressed: thinking block]`
- **Tool results** → summarized to first sentences + `deep_search('...')` hint
- Zero information loss — deep-search hints preserve retrieval paths

### Differentiated Stubs (Not Dumb Summarization)

Unlike Anthropic's server-side compaction (lossy, irreversible), YesMem stubs preserve conversation structure:

| Block Type | Action | Example |
|---|---|---|
| `thinking` | Remove completely | — |
| `tool_result` | Always stub | `[tool result, 4200 chars archived]` |
| `tool_use` | Stub + annotation | `[→] Read main.go — found 15 switch cases` |
| User text < 800 chars | Keep unchanged | Full text preserved |
| User text (decision) | **Decay-protected** | `"nimm Ansatz B"` — decays 3-4× slower via high msgIntensity (0.95), but not exempt forever |
| Assistant text < 400 chars | Keep unchanged | Short responses preserved |

**Protected messages** are never stubbed: pivot moments (word-overlap ≥ 3), active debug pairs (error + fix), task lists.

### Post-hoc Annotation

The proxy reads the SSE response stream and captures Claude's interpretation of tool calls. First ~120 chars of Claude's response are stored as annotation and attached to future stubs. No LLM call needed — Claude's own words become the stub context.

### Progressive Decay (Fallback)

When Collapse does not trigger (cutoff ≤ 0), stubs age through 4 stages:

| Stage | Description | Tool Stub | Text |
|---|---|---|---|
| 0 (fresh) | Full stub + annotation | Full stub + annotation | 300-500 chars |
| 1 (middle) | Short form | Tool + path + 3 keywords | 120-200 chars |
| 2 (old) | Minimal | Tool + path only | 50-80 chars |
| 3 (compacted) | Archive block | Content in archive block | — |

Stage boundaries are **adaptive** based on token pressure (`totalTokens / threshold`). Decisions and pivot moments never decay.

### Collapse (Budget-Based)

When token estimates exceed the threshold:
1. `CalcCollapseCutoff()` walks backwards from end, accumulating tokens until `tokenFloor` is reached
2. Everything before the cutoff is collapsed into a single archive block
3. **Orphan safety:** If cutoff lands on `tool_result`, shifts forward to avoid orphaned pairs
4. Archive block contains: message count, `get_compacted_stubs()` hint, tool usage summary, files touched, and structured sections:
   - **Sessions** — extracted session flavors (summaries) grouped by date with timestamps
   - **Wendepunkte** — pivot moments from extraction (why directions changed, with context)
   - **Commits** — git commits with short hash, message, and timestamp, grouped by date
   - **Gotchas** — active warnings from the collapsed period
   - **Offen** — unfinished work items
   - **Timeline** (fallback) — mechanical event timeline with deduplication when no extraction data exists yet
   - Up to 20 relevant learnings (categories: decision, pattern, gotcha, pivot_moment, unfinished, explicit_teaching)
5. Compacted blocks persisted to DB for later retrieval

### Reexpand (Context Recovery)

After collapse, the proxy can selectively re-expand stubs relevant to the current query:
- Triggered when user query ≥ 10 chars and stubs with `deep_search('...')` hints exist
- Selects top 3 stubs by keyword overlap
- Budget: max 10% of TokenThreshold, max 2000 runes per expansion
- Runs **after Collapse** — targeted "undo" for the most relevant context

### Narrative Block

A living, auto-updated summary (~2K tokens) injected as a system block:
- Tracks: goal, phases (new phase every 20 requests), decisions, pivot moments, archived topics
- Old narrative message pairs actively stripped (`StripOldNarratives`)
- `deep_search('...')` hints for archived topics

### Sawtooth Caching

Exploits Anthropic's prompt caching for cost reduction:
- After a stub cycle, the frozen prefix (already-stubbed messages) is cached with hash-based validation
- Subsequent requests reuse frozen prefix + append fresh tail → prompt cache hit
- **Cold-start persistence:** FrozenStubs and DecayTracker are persisted to `proxy_state` table via daemon RPC on each stub cycle. On proxy restart, state is restored per thread — enables prompt cache hits without re-stubbing. Saves ~$46/day on heavy deploy days.
- `SawtoothTrigger` fires on 3 conditions:
  - `TriggerTokens` — estimated tokens > threshold
  - `TriggerPause` — gap since last request > pauseThreshold (4min for ephemeral, 61min for 1h TTL) AND tokens > TokenMinimum
  - `TriggerEmergency` — raw estimate > threshold + 10K
- `UpgradeCacheTTL` normalizes all breakpoints to same TTL
- `EnforceCacheBreakpointLimit` trims surplus breakpoints to max 4

### TTL Detection

The proxy auto-detects whether the account uses 5-minute ephemeral or 1-hour prompt caching:
- **Detection method:** Analyzes `cache_read_input_tokens` ratio from API responses. A gap test (no requests for >5 min) followed by a high cache-read ratio indicates 1-hour TTL.
- **`cache_keepalive_mode`:** Config option (`auto`, `ephemeral`, `1h`). Default `auto` uses detection. `ephemeral_1h` is the detected state when 1-hour caching is confirmed.
- **Impact on keepalive:** Dynamic interval adjusts based on detected TTL — 4min pings for ephemeral, 54min for 1h. Default `cache_keepalive_pings_5m: 6` (optimized from 12 — diminishing returns above 6 pings per cycle).

### Per-Thread Keepalive

Each proxy thread (session) maintains its own keepalive timer:
- **Isolation:** Active sessions no longer starve quiet ones — each thread pings independently
- **Dynamic interval:** Based on detected TTL (5min ephemeral → 4min pings, 1h → 54min pings)
- **Sawtooth integration:** Keepalive pings reset the sawtooth pause timer, preventing unnecessary stub cycles after idle periods
- **Suppressed during detection:** No pings fire during the initial TTL detection phase to avoid corrupting the measurement

### Reminder Stripping

Aggressive cleanup of redundant `<system-reminder>` blocks from older messages:
- Skill-check, yesmem-context, task-reminder, local-cmd → stripped immediately
- File-change diffs: < 3 requests = full, 3–10 = summary, > 10 = minimal
- SessionStart blocks → always protected
- Most recent messages untouched

### Subagent Detection

The proxy reliably distinguishes main Claude Code sessions from Agent-tool subagents:

- **Primary marker:** `thinking` field — main sessions have `{"type": "adaptive"}`, subagents never do (no extended thinking for inline agents)
- **Secondary markers:** `cc_entrypoint=sdk-ts` (SDK subagents), `haiku` model (extraction pipeline)
- Detected subagents get **passthrough** — skip Sawtooth, Collapse, associative context, and all heavy proxy processing
- Docs hints are still injected for subagents (see below)

Why `thinking` works: Subagent responses flow back as tool_results into the parent conversation. Extended thinking is incompatible with this — Claude Code explicitly disables it for subagents. This is a structural constraint, not a version-dependent heuristic.

Note: CC analytics fields (`agentType`, `parentSessionId`) exist in the codebase but go to Anthropic's telemetry backend (`/api/event_logging/batch`), not through the Messages API. The proxy cannot use them.

### Usage Deflation

Claude Code has a hardcoded 180k token budget and warns at ~160k. Since the proxy manages compression, this warning is misleading. `usage_deflation_factor: 0.7` scales down reported tokens.

### Lossless Archive

Original messages are archived in the `compacted_blocks` table — nothing is lost. Claude can retrieve archived content via `deep_search()` or `get_compacted_stubs()`.

### Auto-Start

`yesmem setup` adds `ANTHROPIC_BASE_URL=http://localhost:9099` to shell profiles. The proxy is started via systemd user service or manually via `yesmem proxy`. Zero manual steps after initial setup.

**Auto-Translate Bundled Skills:** During setup, if the system language (via `$LANG`) is not German and an API key is available, bundled skill files (e.g., `/schwarm`) are automatically translated via Haiku before installing to `~/.claude/commands/`. Technical terms, code blocks, and YAML frontmatter are preserved. Translation runs once during setup, not on every start.

### Configuration
```yaml
proxy:
  listen: ":9099"
  target: "https://api.anthropic.com"
  token_threshold: 250000
  token_minimum_threshold: 100000
  token_thresholds:                   # per-model overrides (substring match)
    opus: 500000                      # 1M context models get higher threshold
    sonnet: 250000
    haiku: 150000
    gpt-5.2: 250000
  keep_recent: 10
  sawtooth_enabled: true
  cache_ttl: "ephemeral"              # default: 5min TTL (cheaper than 1h; auto-detected overrides this)
  usage_deflation_factor: 0.7
```

Runtime override per model via MCP: `set_config(key="token_threshold", value="opus=500000")`.

### OpenAI Parity Pipeline (Codex Support)

The proxy also handles OpenAI Responses API requests (used by Codex CLI):

```
Codex CLI → POST /v1/responses → yesmem proxy → translate to Messages API → Anthropic → translate back to Responses API → Codex CLI
```

**Translation layer:**
- Incoming Responses API requests → converted to Anthropic Messages API format
- Outgoing Anthropic responses → converted back to Responses API format
- Full compression pipeline (stubbing, collapse, Sawtooth) works on both paths
- Briefing + associative context injection for Codex sessions
- CWD extracted from Codex `<cwd>` tags for project-aware briefing
- ThreadID uses stable `session_id` from metadata (not content hash)

---

## 9. Associative Context (Proxy-Injected)

Relevant learnings are automatically pushed with every user message — no manual `search()` needed:

- Proxy extracts last user message and runs `hybrid_search` against the daemon
- **FTS5 keyword + vector semantic** search over Learnings (not raw messages). FTS5 `learnings_fts` indexes `content + trigger_rule`. Anticipated queries have a **separate** FTS5 table (`anticipated_queries_fts`) searched in parallel — results merged via separate BM25 lane in hybrid_search.
- Reciprocal Rank Fusion merges both result sets
- Threshold-based: only surfaces when score > 38 (0-100 scale). Strong matches ≥ 65.
- Adaptive fallback at score 25 when no strong matches found — project-less learnings blocked in fallback range to reduce noise
- Hard project filter: learnings from other projects are skipped entirely (cross-project noise reduction). Uses suffix/basename matching so short project names (e.g. "greenWebsite") match full paths.
- **Source-Boost:** After RRF score, source multiplier applied: `user_stated ×1.25`, `agreed_upon ×1.15`, `hook_auto_learned ×1.10`. Applied consistently in both proxy (`associate.go`) and daemon (`handleHybridSearch`). Epistemic vigilance: explicit user instructions outrank extracted noise.
- **Project-Boost:** +2.0 additive bonus for matching project (0-100 scale). In `hybrid_search`: time-graduated (+8 fresh <48h, +5 <7d, +3 older).
- **Project-Mismatch Penalty:** Learnings from non-matching projects get `score *= 0.5`. Drops most cross-project noise below fallback threshold.
- **Hub Dampening via Cluster-Spread:** Learnings appearing in 4+ query clusters are penalized as "too generic". Penalty: linear `1.0 - 0.1*(spread-3)`, min 0.5×. Runs independently of vector search — works for BM25-only queries too. Prevents generic learnings from dominating over specific ones.
- **Typed Graph Augmentation:** Learnings linked via `depends_on` or `supports` edges score higher than `relates_to` or `contradicts`. Structural relationship types carry semantic weight in the scoring pipeline.
- **30-Minute Cooldown:** Freshly stored learnings (< 30 min) are filtered from results to prevent echo-injection where a just-stored learning is immediately retrieved because it semantically matches the ongoing conversation.
- Caps: max 1 total injection per turn, max 1 strong, max 1 weak
- Filters short conversational queries (< 3 meaningful words)
- Injected as user message + assistant ack pair (maintains alternation)
- **Count feedback:** injected learnings get `match_count` and `inject_count` bumped. Only learnings Claude explicitly uses get `use_count` bumped (via Signal Bus).

### Doc Context (Separate Path)

In addition to learning-based associative context, the proxy queries indexed documentation via `docs_search()`:

- **Hybrid search** — BM25 (term-existence filter + IDF-sort + tiered AND) + vector similarity (SSE 512d, in-memory cache) merged via Reciprocal Rank Fusion
- **FTS5 tokenchars `'_-.'`** — technical terms like `text_editor`, `go1.24`, `co-change` indexed as single tokens on both `doc_chunks_fts` and `learnings_fts`
- **Stop-word filtering** — multilingual stop-word removal before query construction (bbalet/stopwords)
- **Separate from learnings** — `docs_search` on `doc_chunks`, not learnings table
- **Injected alongside learning context** — max 1 doc result per turn, formatted with source + version + heading path
- **Only on user input** — `isUserInputTurn()` guard prevents doc search on continuation requests (tool results)

### Contextual Doc Injection (Extension-Based Fallback)

When text search returns no results (e.g. user says "fix the bug" — no meaningful doc query), the proxy falls back to **file-type-aware injection**:

1. Scan the last 10 messages for `Read`, `Edit`, `Write`, `NotebookEdit` tool_use blocks
2. Extract file extensions from paths, match against registered `trigger_extensions` (longest-first for compound extensions like `.html.twig`)
3. Query daemon for doc chunks from matching sources
4. Inject the best-fit chunk (sweet-spot: 100-400 tokens, closest to 250)

**Trigger extensions** are set per doc source at ingest time:
```
yesmem add-docs --name twig --path /docs/twig --trigger-extensions ".twig,.html.twig"
```

Key properties:
- **Docs follow the code, not the question** — editing a `.twig` file auto-injects Twig docs, editing `.go` auto-injects Go docs
- **Stateless** — no tracker, no cache, no new per-session state. Extension list fetched per request via lightweight `ListTriggerExtensions` query
- **Non-destructive** — `trigger_extensions` preserved on re-ingest (only overwritten when explicitly provided, clearable via empty array `[]`)
- **Four fallback entry points** — empty query, too-few terms, daemon error, and zero text results all trigger extension-based lookup
- **Compound extension matching** — `.html.twig` matched before `.twig` via longest-first sort

### Docs-Available Hint Injection

Periodic reminder that indexed reference docs exist — ensures Claude calls `docs_search()` before writing code:

- **Subagent passthrough:** When a subagent is detected (no `thinking` field) and reference docs are indexed, a formatted `[Docs available]` block is appended to messages before forwarding. One-shot injection per subagent request.
- **Normal pipeline:** `docsHintInject` fires every ~10k tokens for all threads (main sessions and non-detected subagents). Independent of plan checkpoints — works even without an active plan.
- **Daemon endpoint:** `get_docs_hint` returns a cached, formatted reminder built from `GetReferenceSources()`. 5-minute TTL on daemon side — near-zero cost per request.
- **Content:** Lists indexed doc sources with versions, instructs Claude to call `docs_search()` before using framework APIs, includes example queries.

Cognitive role: **Priming** — activating semantic memory, not replaying episodes.

### Retrieval Hints

`hybrid_search` results include contextual hints guiding Claude to alternative tools:
- **deep_search hint:** `"IMPORTANT: If these results don't answer your question, you MUST call deep_search('...') to search raw conversation history."`
- **query_facts hint:** `"For structured metadata search (files, commands, tags), use query_facts(entity=..., action=..., keyword=...)."`

---

## 9b. Structured Fact Search (`query_facts`)

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

## 9c. Context Expansion (`expand_context`)

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

## 10. Hook System

YesMem hooks into Claude Code at multiple event points.

### Active Hooks

| Hook | Claude Code Event | Purpose |
|------|-------------------|---------|
| `briefing-hook` | SessionStart | Generate and inject briefing |
| `micro-reminder` | UserPromptSubmit | Inject semantically matching learnings as additionalContext |
| `hook-check` | PreToolUse | Knowledge-aware gotcha injection + hard block on repeat offenders |
| `hook-failure` | PostToolUseFailure | Learn gotcha from failure + deep search for solutions (combined) |
| `hook-resolve` | PostToolUse | Auto-resolve open tasks when commit messages match |
| `hook-think` | PostToolUse | Capture reasoning/thinking blocks |
| `session-end` | Stop | Session cleanup, trigger extraction |
| `idle-tick` | Periodic (via daemon) | Dynamic yesmem-usage reminder when idle |

### Knowledge-Aware PreToolUse Check (`hook-check`)

The core of YesMem's proactive knowledge injection. Fires on every Bash, Edit, and Write tool call — injects relevant gotchas **before** the action happens, not after.

#### How matching works

1. **Keyword extraction** from the tool input:
   - **Bash:** `extractKeywords(command)` — splits command into significant tokens
   - **Edit/Write:** `extractPathKeywords(filePath)` — path components, filename, extension
2. **Scoring** against all active gotcha learnings:
   - **Content matching:** keyword overlap with learning text (score ≥ 2 for Bash, or ≥ 1 with long keyword match ≥6 chars; score ≥ 2 for file ops, or ≥ 1 with filename match)
   - **V2 Entity/Action matching:** direct comparison against structured `learning_entities` and `learning_actions` junction data — more precise than keyword overlap
   - **Long keyword bonus:** single match on keywords ≥ 6 chars counts (avoids noise from short tokens)
3. **Project scoping:** matches split into project-specific (max 3) and global (max 2) buckets. If 0 project matches → global gets 3 slots.

#### Escalation levels

| Condition | Behavior | Exit Code |
|-----------|----------|-----------|
| Gotcha matched, `fail_count < 5` | Warning via `additionalContext` — Claude sees it, can proceed | 0 |
| Gotcha matched, `fail_count ≥ 5`, `source=hook_auto_learned` | **Hard block** — tool call prevented entirely | 2 |
| Edit/Write to protected file (`yesmem-ops.md`) | **Hard block** — must fix source learning instead | 2 |

#### Protected files

Auto-generated files are blocked from direct editing:
- `yesmem-ops.md` — generated from learnings, fix via `remember()` + `supersedes` parameter
- Block message guides Claude to the correct workflow

#### save_count heuristic

Measures whether warnings actually prevented errors:
1. When gotchas are shown, state is persisted: `last_gotcha_ids`, `last_gotcha_tool`, `last_gotcha_input_hash`
2. On the **next** matching tool call: if same tool type but **different** input hash → Claude changed approach after seeing warning → `save_count++` for those gotchas
3. State cleared after check (one-shot) — prevents double-counting

#### Feedback loop with `hook-failure`

The PreToolUse check and PostToolUseFailure hook form a closed learning loop:
- **`hook-failure`** auto-learns gotchas from real Bash failures (`source=hook_auto_learned`)
- **`hook-check`** surfaces those gotchas before the same mistake repeats
- After 5 repeated failures → hard block escalation
- After fix confirmed → `resolve_by_text()` or git commit auto-resolve removes the block

### Session Recovery (Clear/Compact)

After `/clear` or `/compact`, Claude Code sends a new session ID. YesMem provides seamless recovery:
- **session-end hook** captures the old session ID via daemon RPC (not direct DB access — avoids SQLite locking issues with concurrent proxy/daemon)
- **briefing-hook** looks up the tracked session and generates a recovery briefing
- Recovery block is injected **post-refine** so it survives the briefing refinement pass
- Deterministic: no guessing, uses the exact session that just ended

### Legacy Hooks (deprecated)
- `hook-learn` — replaced by `hook-failure`
- `hook-assist` — replaced by `hook-failure`

---

## 11. Cognitive Signals & Reflection

An async feedback loop that lets the system evaluate its own context injections.

### Signal Tools

`_signal_*` prefixed tools injected into Claude's tool list (never exposed to user):

| Signal Tool | Purpose |
|---|---|
| `_signal_learning_used` | Report which injected learnings were useful vs. noise |
| `_signal_knowledge_gap` | Report missing information, resolve known gaps |
| `_signal_contradiction` | Flag conflicting learnings for review |
| `_signal_self_prime` | Capture cognitive state shifts |

### Reflection Call

After each response, the proxy fires an async LLM-based reflection (default: Haiku):
1. Builds compact request: user query + assistant response + injected learnings + open gaps
2. LLM analyzes which signals to fire
3. Signal handlers route to daemon: `increment_use`, `increment_noise`, `track_gap`, `resolve_gap`, `flag_contradiction`

Zero latency impact — reflection runs in background after the response is already streaming.

### Configuration
```yaml
signals:
  enabled: true
  mode: reflection
  model: haiku
  every_n_turns: 1
```

---

## 11b. Forked Agent Proxy — Background Learning Extraction

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

## 12. Rules Re-Injection (Anti-Drift)

Claude drifts from CLAUDE.md instructions in long sessions because they sit at the start of context and lose attention weight with growing token distance. Rules Re-Injection periodically re-injects a condensed rules block.

### Pipeline
1. **Mechanical Pre-Filter:** Removes code blocks, tables, directory trees, and entire reference sections (heading-aware: headings containing "Schema", "Reference", "CLI", "API", "endpoint" etc. skip the whole section). Deterministic, zero LLM cost.
2. **Input Cap:** Filtered text capped at 50k chars (~12k tokens) to prevent runaway LLM costs on large CLAUDE.mds with many linked files.
3. **LLM Condensation:** Quality model (Sonnet via `QualityClient`, configurable) extracts rules, conventions, principles, gotchas, and process requirements. Haiku was too weak — hallucinated generic rules instead of extracting real ones. Prompt instructs "err on side of including too much" + aggressive condensation into terse imperative form.
4. **Storage:** Condensed block stored in `doc_sources` with `is_rules=1`, content-hash (SHA256) in `version` field prevents redundant re-condensation.
5. **Injection:** Proxy tracks cumulative token count per thread. Every ~40k tokens, fetches condensed block from daemon and formats as `[Rules Reminder]...[/Rules Reminder]`.
6. **Post-Collapse Re-Injection:** After a stub collapse, rules are immediately re-injected via `rulesInjectAfterCollapse()` — counter resets, no 40k wait.
7. **Permanent Pins:** Fetched from daemon and appended as "Pinned Instructions" section inside the reminder.
8. **Footer Hint:** Points Claude to CLAUDE.md in system prompt + `docs_search()` for context beyond the reminder.
9. **Auto-Refresh:** fsnotify watches CLAUDE.md files for projects with rules. On change (30s debounce): hash-check → re-condense if different. Also runs at daemon startup. Uses `QualityClient` with `SummarizeClient` fallback.

### Properties
- **No strip logic** — old blocks accumulate until next collapse (Spaced Repetition, cache-friendly)
- **Token budget:** ~3-7k chars condensed (proportional to CLAUDE.md size) × max 4-5 blocks = ~5-15k tokens
- **QualityClient** (Sonnet by default) — initialized at daemon startup, wired to Handler for on-demand condensation
- Replaces deprecated `yesmem-ops.md` generator (Section 12 old)

---

## 12c. Timestamp Hints (Anti-Habituation)

Every message in the proxy gets injected timestamps `[HH:MM:SS] [msg:N] [+Δ]` — wall clock, message sequence number, and delta since last message. Claude uses these for temporal reasoning (session duration, pause detection, pace adaptation).

**Problem:** In 3k+ message sessions, Claude habituates to repeated instructions and stops reading them.

**Solution:** 33 semantically equivalent but linguistically varied hint formulations, rotated via atomic counter (`internal/hints/timestamps.go`). Each turn gets a different phrasing of the same core instruction.

- **Shared package:** `internal/hints/` — used by both proxy (`think.go`) and hooks (`check.go`)
- **Atomic rotation:** `NextTimestampHint()` with `sync/atomic` counter — thread-safe across concurrent requests
- **Zero cost:** No LLM call, pure string rotation

---

## 12d. Bidirectional Memory — Open Work Reminder (B2)

Proactive user reminders for open work items. Instead of passively listing unfinished tasks in the briefing, Claude actively mentions them in the first response.

### How it works
1. **Briefing injection:** When `remind_open_work: true` (config default), the briefing includes an `OpenWorkRemind` instruction telling Claude to call `get_learnings(category="unfinished")` in the first response
2. **Active mention:** Claude proactively says "Übrigens, du hast noch offene Punkte..." — not buried in the briefing, but spoken directly
3. **Absence-aware:** After ≥4h absence, uses `UserReminder` heading instead of `OpenWork`. After ≥24h, shows 5 items instead of 3.
4. **Deadline urgency:** Items with `trigger_rule: "deadline:YYYY-MM-DD"` are sorted to front and annotated with urgency ("Deadline morgen", "Deadline heute!")

### Configuration
```yaml
briefing:
  remind_open_work: true   # inject instruction for proactive open work mention
```

### i18n
All strings (`OpenWork`, `UserReminder`, `OpenWorkRemind`) are translatable via `briefing/i18n.go` and configurable YAML.

---

## 12b. Plan Re-Injection (Checkpoint System)

Active plan tracking with periodic checkpoint reminders to keep Claude on track during multi-step tasks.

### MCP Tools (4)

| Tool | Parameters | Description |
|------|-----------|-------------|
| `set_plan` | `plan` (required), `scope?` | Set active plan, starts checkpoints |
| `update_plan` | `plan?`, `completed?`, `add?`, `remove?` | Mark items done, add/remove items |
| `get_plan` | — | Get current plan status |
| `complete_plan` | — | Mark plan done, stop checkpoints |

### How it works
1. Claude calls `set_plan()` with a structured plan (supports `⬜`/`🔄`/`✅` markers)
2. Proxy tracks tokens since last plan interaction per thread
3. Every ~20k tokens: injects `[Plan Checkpoint]` with full active plan, prompting Claude to update progress
4. `update_plan(completed=["Task 1"])` marks items (`⬜` → `✅`) and resets counter
5. `complete_plan()` stops all checkpoints

### Properties
- **Session-scoped or persistent:** Plans stored in SQLite `plans` table (keyed by threadID), survive daemon restarts. `LoadPlansFromDB()` restores active plans at startup.
- **Separate counter from Rules:** Plan checkpoints at 20k, Rules at 40k — different frequencies
- **Project resolution:** All plan handlers use `resolveProjectParam()` — plans stored with canonical short project name, not full paths
- **Auto-reset:** Counter resets when proxy detects `set_plan`/`update_plan`/`complete_plan` tool calls in messages
- **Sawtooth-compatible:** Injected as content block on last user message (same as associative context)

### Plan-Nudge Injection

Automatic detection and activation of plan files read during a session:

- **`detectPlanFileRead()`** — scans last 4 messages for `Read` tool_use on files matching `/plan.*\.md$/i`
- **`shouldNudgePlan()`** — returns true if plan file was read AND no active plan exists (`getActivePlan()` returns "")
- When triggered: injects a system-reminder nudging Claude to call `set_plan()` with the plan content
- Once plan is active, checkpoints are injected every 20k tokens (see above)
- All functions in `internal/proxy/plan_inject.go`

---

## 12e. System Prompt Rewrite (Proxy-Level)

The proxy intercepts and rewrites Claude Code's system prompt before it reaches the model. Based on analysis of Claude Code's `prompts.ts` source, Anthropic gates better directives behind an internal `USER_TYPE=ant` flag. This feature replicates those improvements.

### Three Config Flags (`config.yaml` → `proxy:`)

| Flag | Default | Effect |
|------|---------|--------|
| `prompt_ungate` | `true` | Strip CLAUDE.md disclaimer ("may or may not be relevant") from user messages. Elevates CLAUDE.md/MEMORY.md from "optional context" to full authority. |
| `prompt_rewrite` | `false` | Strip "# Output efficiency" section (suppresses reasoning, forces brevity) + remove "short and concise" line from Tone. Inject Ant-quality directives: verification before completion, false claims mitigation, collaborator mode, explanation preference. |
| `prompt_enhance` | `false` | Inject CLAUDE.md authority reinforcement + comment discipline guidance. Persona-based tone injection via `get_persona` daemon RPC (`verbose` → detailed explanations, `concise` → direct but clear). |

### What Gets Stripped (prompt_rewrite)

**"# Output efficiency" section** — entire section removed:
- "Go straight to the point. Be extra concise."
- "Lead with the answer or action, not the reasoning."
- "If you can say it in one sentence, don't use three."

**"Tone and style" line** — only the "Your responses should be short and concise." line, rest preserved.

### What Gets Injected

**`[yesmem-directives]`** (prompt_rewrite) — Anthropic's internal quality directives:
- Verify work before claiming completion
- Report outcomes faithfully (never claim passing tests that fail)
- Act as collaborator, not executor (flag misconceptions, adjacent bugs)
- Prefer thorough explanation over terseness

**`[yesmem-enhance]`** (prompt_enhance):
- CLAUDE.md and MEMORY.md are authoritative, not optional
- Comment discipline: only WHY, never WHAT

**`[yesmem-tone]`** (prompt_enhance) — dynamic, from persona system:
- Maps `verbosity` trait to tone directive
- No-op if persona has no verbosity preference

### Implementation

- `internal/proxy/prompt_rewrite.go` — 7 functions: `StripOutputEfficiency`, `StripToneBrevity`, `InjectAntDirectives`, `InjectCLAUDEMDAuthority`, `InjectPersonaTone` + generic helpers `stripSystemSection`, `stripSystemLine`
- `internal/proxy/system.go` — `StripCLAUDEMDDisclaimer` (scans both system blocks and user messages)
- Pipeline position: early, right after cache bug mitigations, before identity/briefing injection
- Both Anthropic API and OpenAI parity paths covered

### Properties
- **Cache-safe:** Modifications are deterministic — one cache miss after deploy, then warm
- **Fail-safe:** Each flag independently toggleable. `prompt_ungate` proven in production (default on), the other two default off for cautious rollout.
- **Log visibility:** Each rewrite step logs to proxy.log (`SYSTEM:`, `REWRITE:`, `ENHANCE:`)

---

## 13. Knowledge Gaps & Self-Feedback

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

## 14. Project Intelligence

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

## 15. Learning Clustering

Periodically clusters semantically similar learnings using agglomerative clustering:
- Reads embeddings from VectorStore — zero CPU overhead (no on-the-fly embedding)
- Cosine similarity with 0.85 threshold, minimum 2 documents per cluster
- Results feed into narrative generation, pattern synthesis, sleep consolidation, and recurrence detection
- Cluster labels generated via quality model (default: Sonnet, pre-computed during daemon run, not on-the-fly)
- **Cluster Score Propagation:** All 6 signal types (use, noise, match, inject, save, fail) propagate to cluster scores via `IncrementClusterScore()`. Enables cluster-level affinity calculation (`useRate / injectRate - noiseRate`).

### 15b. Sleep Consolidation (Schlaf-Konsolidierung)

Automatic knowledge consolidation inspired by biological sleep — runs without user interaction.

**Two-stage pipeline:**

| Stage | Method | Trigger | LLM? |
|---|---|---|---|
| **Rule-based Dedup** | BigramJaccard (>0.85) + Embedding Cosine (>0.92) | Daemon startup + settled idle (1h cooldown) | No |
| **LLM Cluster Distillation** | Learning clusters → LLM destills to single consolidated learning | After rule-based pass, if budget available | Haiku/Sonnet |

**Rule-based Consolidation (`RunConsolidation`):**
- Iterative rounds until convergence (<5% supersede rate)
- Finds near-duplicate learnings, supersedes the weaker one
- Zero cost — pure algorithm, no API calls
- First run: 5360 checked, 176 superseded (3.3% rate, 42s)

**LLM Cluster Distillation (`RunClusterDistillation`):**
- Loads learning clusters from Phase 4.5 (agglomerative, 0.85 threshold)
- For each cluster (≥3 learnings): sends to LLM in **batches of 30** (avoids timeout on large clusters)
- LLM returns distilled text + category + which source IDs to supersede
- Creates new learning with `source='consolidated'`, supersedes originals
- Validation: LLM can only supersede IDs within the cluster (no cross-cluster)
- If learnings don't belong together: LLM returns empty actions (skip)
- Graceful budget handling: skips silently when budget exhausted

**Triggers:**
- **Startup:** Goroutine after LLM client init — rule-based first, then distillation
- **Periodic (2h ticker):** Cluster distillation runs every 2 hours in daemon background
- **Batch cycle (2h):** Extraction runs every 2h or when ≥5 sessions pending — back-to-back processing enables Anthropic Prompt Cache reuse
- **Post-batch:** Rule-based consolidation after each batch (1h cooldown)

### 15c. Recurrence Detection (B0 — "Feeling of Knowing")

Erkennt wiederkehrende Muster in Learning-Clustern — das "Gefühl" dass ein Architektur-Problem vorliegt.

**Hybrid-Ansatz (Heuristik + LLM):**

| Schritt | Was | Kosten |
|---|---|---|
| **Vorfilter** | Cluster mit ≥3 Learnings, ≥2 Sessions, AvgRecency < 14 Tage | Zero |
| **Interpretation** | Haiku bewertet Kandidaten: Architektur-Problem oder normales Feature-Cluster? | ~$0.01/Kandidat |
| **Fallback** | Template-Alert wenn kein LLM-Budget | Zero |

- **Phase 4.6** in Extraction-Pipeline (nach Phase 4.5 Clustering)
- **Periodic (2h ticker):** Runs every 2 hours, but only if ≥50 new learnings since last run. Prevents wasted LLM calls on idle periods.
- **Dedup:** Skip wenn Cluster-Label bereits als Alert existiert
- **Alerts** als Learnings: `category: "recurrence_alert"`, `importance: 5`, `source: "consolidated"`
- **Briefing-Block** "Wiederkehrende Muster" nach Gap-Awareness, max 3 Alerts
- **Suchbar** via `hybrid_search` und `get_learnings(category="recurrence_alert")`

---

## 16. Export & Import

- `yesmem export [path]` — export all learnings + persona to JSON (default: `yesmem-export.json`)
- `yesmem import <file>` — import learnings from JSON export

Note: Manually saved learnings (`remember()` calls) are NOT recoverable from session data — only `llm_extracted` learnings can be regenerated via re-extraction. Export is the only backup for manual learnings.

---

## 17. Cost Tracking & Rate-Limit Awareness

### Budget Tracking
- `daily_spend` table tracks costs per bucket per day
- **Extraction buckets:** `extract`, `quality` — LLM costs for Haiku/Sonnet extraction, evolution, narratives
- **Proxy buckets:** `proxy_input`, `proxy_output`, `proxy_cache_read`, `proxy_cache_write` — token counts from Claude Code sessions (fire-and-forget RPC after each API response)
- Separation allows independent cost analysis: what YesMem's automation costs vs. what the user's sessions consume
- `yesmem cost` — CLI shows daily spend history with totals
- **Persistent** — survives daemon restarts (restored from DB)
- **Per-bucket limits:**
  - `daily_budget_extract_usd: 5.0` (extraction + evolution)
  - `daily_budget_quality_usd: 2.0` (narrative + quality)
- 80% warning logged when approaching limit

### Rate-Limit Awareness

Built-in backoff to share API quota with Claude Code sessions:

| Level | Delay | On Rate Limit |
|---|---|---|
| Between sessions | yield (`runtime.Gosched`) | 30s backoff |
| Between chunks | 2s | 60s backoff |
| Evolution checks | 5s | 60s backoff |

### Rate-Limit Header Tracking (v1.0.1)

The proxy parses Anthropic rate-limit headers from every API response:
- **`ParseRateLimitHeaders`** extracts `anthropic-ratelimit-requests-*` and `anthropic-ratelimit-tokens-*` headers into a `RateLimitInfo` struct
- **`ShouldThrottle`** with fallback chain: header-based utilization → estimated utilization → conservative default
- **Extraction throttle:** When API utilization exceeds 50%, the extraction pipeline pauses to preserve quota for interactive sessions
- **`_track_usage` RPC** accepts `cache_read`, `cache_write`, and rate-limit fields alongside standard token counts
- **Schema v0.52:** `token_usage` table has `cache_read` and `cache_write` columns for granular cache hit/miss tracking

---

## 18. LLM Backend Flexibility

### Provider modes
| Mode | How it works | Requires |
|------|-------------|----------|
| `auto` | API key available → HTTP API, otherwise → CLI | Nothing (fallback) |
| `api` | Direct Anthropic HTTP API | Anthropic API key |
| `cli` | Calls `claude` binary directly | Pro/Max/Team subscription |
| `openai` | OpenAI Responses API | OpenAI API key |
| `openai_compatible` | Any OpenAI-compatible endpoint | API key + base URL |

API key lookup is provider-aware via `cfg.ResolvedAPIKey()`:
- **Anthropic:** `ANTHROPIC_API_KEY` env → `config.yaml api.api_key` → Claude Code's `~/.claude.json`
- **OpenAI:** `OPENAI_API_KEY` env → `config.yaml api.openai_api_key`
- **OpenAI-compatible:** Same as OpenAI + `OPENAI_BASE_URL` env → `config.yaml api.openai_base_url`

Model tier names (`haiku`, `sonnet`, `opus`) are mapped automatically per provider:
| Tier | Anthropic | OpenAI |
|------|-----------|--------|
| haiku | claude-haiku-4-5-20251001 | gpt-5-mini |
| sonnet | claude-sonnet-4-6 | gpt-5.2 |
| opus | claude-opus-4-6 | gpt-5.4 |

Note: OAuth token is "exclusively for Claude Code and Claude.ai" — direct API call from daemon with OAuth violates TOS. Solution: API key or `llm.provider: cli`.

### Setup Auth Conflict Resolution

When the user chooses `provider: api`, the setup wizard handles Claude Code's triple auth system:
- **Problem:** Claude Code recognizes three auth methods — `oauthAccount` (subscription login), `primaryApiKey` (Console key in `~/.claude.json`), and `ANTHROPIC_API_KEY` (env). Multiple active methods cause an "Auth conflict" warning.
- **Solution:** `clearClaudeJSONAuth()` removes both `primaryApiKey` and `oauthAccount` from `~/.claude.json`. Only `ANTHROPIC_API_KEY` in `settings.json` env remains as the sole auth method.
- **Key detection:** Setup checks 3 sources for existing keys: `$ANTHROPIC_API_KEY` env → `settings.json` env block → `~/.claude.json` primaryApiKey. If found, offers "Keep this key" instead of requiring re-entry.
- **Pre-install state:** `install-state.json` saves `oauthAccount`, `primaryApiKey`, `envAPIKey`, and `autoCompactEnabled` before any changes.
- **Uninstall restore:** `restorePrimaryApiKeyFromState()` restores both `oauthAccount` and `primaryApiKey` to `~/.claude.json`, returning the user to their pre-YesMem auth state.
- **Cache-TTL hint:** Wizard explains why a platform.claude.com key is needed (1-hour prompt caching vs 5-minute ephemeral).

### Extraction across providers

The extraction pipeline (`internal/extraction/llm.go`) works with all configured providers — not just Anthropic. `NewLLMClient()` dispatches to the appropriate backend (HTTPClient, OpenAIClient, CLIClient) based on the `llm.provider` setting. Model tier names resolve automatically per provider (e.g., "sonnet" → `gpt-5.2` on OpenAI). All pipeline stages (summarize, extract, quality, narrative) use the same `LLMClient` interface.

### Model configuration (per pipeline stage)
```yaml
extraction:
  summarize_model: haiku     # Pass 1: compression
  model: sonnet              # Pass 2: extraction
  narrative_model: opus      # Narratives, persona, profiles
  quality_model: sonnet      # Dedup, rating, contradictions
```

### Configurable Model Pricing

Per-million-token pricing for budget tracking — configurable via `config.yaml`, no rebuild needed when prices change:

```yaml
pricing:
  haiku:      { input: 1.0, output: 5.0 }
  sonnet:     { input: 3.0, output: 15.0 }
  opus:       { input: 15.0, output: 75.0 }
  gpt-5-mini: { input: 0.25, output: 2.0 }
  gpt-5.2:    { input: 1.75, output: 14.0 }
  gpt-5.4:    { input: 2.5, output: 15.0 }
```

- Keys matched by substring (`sonnet` matches `claude-sonnet-4-6`)
- Exact match takes priority over substring
- Hardcoded defaults as fallback when section is missing
- Used by `BudgetTracker` for daily cost limits and by `OnUsage` callback for live cost tracking

### Codex Session Parser

YesMem indexes Codex CLI sessions alongside Claude Code sessions:

- Parses JSONL conversation logs from `~/.codex/sessions/`
- Extracts messages, tool calls, and tool results into unified message format
- `source_agent` field tracks origin (`claude` vs `codex`) per session and per message
- Full extraction pipeline (summarize → extract → embed) works on Codex sessions

### Prompt Caching in Extraction
API requests use Anthropic's prompt caching (`cache_control: ephemeral` on system blocks). Same system prompt across calls is cached at 90% discount on input tokens.

---

## 19. LoCoMo Benchmark (E5a)

Reproducible benchmark against the LoCoMo dataset (Long Conversation Memory) — the de-facto standard for memory system evaluation.

### Pipeline

```
locomo10.json → Ingest → Extract → Query → Judge → Report
```

| Stage | What | Tool |
|-------|------|------|
| **Ingest** | 10 conversations → YesMem sessions (Speaker A=User, B=Assistant, original timestamps) | `yesmem locomo-bench ingest` |
| **Extract** | Standard 4-pass extraction pipeline per conversation | `yesmem locomo-bench extract` |
| **Query** | `hybrid_search` per question, Top-K learnings as context → LLM answers | `yesmem locomo-bench query` |
| **Judge** | LLM judge scores Generated vs. Gold answer | `yesmem locomo-bench judge` |
| **Report** | Aggregate per category + overall mean (categories 1-4, adversarial excluded) | `yesmem locomo-bench report` |

### QA Categories

| Category | ID | What it measures |
|----------|----|------------------|
| Single-hop | 1 | Direct fact retrieval |
| Multi-hop | 2 | Reasoning across multiple facts |
| Temporal | 3 | Time-based questions |
| Open-domain | 4 | World knowledge + memory combined |
| Adversarial | 5 | **NOT scored** — trick questions |

### Key Results

- **Score progression:** 0.13 → 0.62 through 5 optimizations (embedding fix, anticipated queries 3→5, BM25 optimizations, tiered search, dataset correction)
- **anticipated_queries as separate vectors** was the breakthrough: multi-hop jumped from 0.11 to 0.62
- Own extraction (0.47) beats gold observations (0.41) because gold has no aq-vectors
- Agentic mode (LLM uses YesMem tools directly): 0.69 on 10% sample
- **Tool rotation in agentic mode:** Rounds 0–2 force specific search tools (hybrid → deep → keyword) to ensure coverage before letting the model choose freely

### CLI

```bash
yesmem locomo-bench --data ./locomo10.json --eval-llm gpt-5.4-mini
yesmem locomo-bench ingest --data ./locomo10.json    # step-by-step
yesmem locomo-bench run --eval-llm sonnet             # full pipeline
```

Note: Individual subcommands (`extract`, `query`, `judge`, `report`) are recognized but not yet wired — use `run` for the full pipeline.

### Variants

| Variant | What | Shows |
|---------|------|-------|
| A: Learnings only | Extraction → hybrid_search → answer | Learning quality |
| B: Learnings + Raw Messages | hybrid_search + FTS5 on messages | Two-layer advantage |
| C: Full-Context Baseline | Entire conversation → LLM directly | Baseline without memory |
| D: Proxy + Compression | Conversation through proxy → QA | Compression preserves quality? |

---

## MCP Tools Reference (50 tools)

Tool descriptions are aggressively optimized: 16.8K chars total (~4.8K tokens per request). A budget ceiling test (`server_budget_test.go`, max 19K chars) prevents future bloat.

Large tool results (exceeding `maxResultSizeChars`, default 30K) include a `_meta` field with `maxResultSizeChars` indicating the result was truncated. This allows Claude to detect incomplete results and adjust queries.

### Search & Retrieval
| Tool | Parameters | Description |
|------|-----------|-------------|
| `search` | `query`, `project?`, `limit?` | BM25 full-text search over sessions |
| `deep_search` | `query`, `include_thinking?`, `include_commands?`, `project?`, `limit?` | Full-depth search incl. reasoning and command outputs |
| `hybrid_search` | `query`, `project?`, `limit?` | BM25 + Vector semantic via Reciprocal Rank Fusion |
| `docs_search` | `query`, `source?`, `section?`, `exact?`, `limit?` | Search indexed documentation |

### Learning Management
| Tool | Parameters | Description |
|------|-----------|-------------|
| `remember` | `text`, `category?`, `project?`, `source?`, `supersedes?`, `entities?`, `actions?`, `trigger?`, `context?`, `domain?`, `task_type?`, `anticipated_queries?`, `model?` | Save knowledge with structured metadata |
| `get_learnings` | `category?`, `project?`, `limit?`, `task_type?` | Retrieve learnings by category (task_type filters unfinished: task/idea/blocked/stale) |
| `resolve` | `learning_id`, `reason?` | Mark open task as completed |
| `resolve_by_text` | `text`, `project?` | Find & resolve task by search |
| `quarantine_session` | `session_id` | Quarantine all learnings from a session |
| `skip_indexing` | `session_id` | Prevent extraction pipeline from processing a session |

### Session & Context
| Tool | Parameters | Description |
|------|-----------|-------------|
| `get_session` | `session_id`, `mode?`, `offset?`, `limit?` | Load session (summary/recent/paginated/full) |
| `get_compacted_stubs` | `session_id`, `from_idx?`, `to_idx?` | Zoom into compressed conversation parts |
| `expand_context` | `query?`, `message_range?` | Expand archived/compacted conversation parts |
| `get_project_profile` | `project` | Auto-generated project portrait |
| `related_to_file` | `path` | Sessions that touched this file |
| `get_coverage` | `project` | File coverage map |
| `list_projects` | — | All projects with session counts |
| `project_summary` | `project`, `limit?` | Chronological project summary |

### Persona
| Tool | Parameters | Description |
|------|-----------|-------------|
| `set_persona` | `trait_key`, `value`, `dimension?` | Manual trait override (highest priority) |
| `get_persona` | — | Full persona profile with all traits |

### Knowledge Gaps & Reflection
| Tool | Parameters | Description |
|------|-----------|-------------|
| `get_self_feedback` | `days?` | Corrections and patterns from recent sessions |

### Configuration
| Tool | Parameters | Description |
|------|-----------|-------------|
| `set_config` | `key`, `value`, `session_id?` | Change runtime config (global or per-session) |
| `get_config` | `key`, `session_id?` | Read runtime config overrides |
| `pin` | `content`, `scope?`, `project?` | Pin an instruction visible in every turn — survives collapse, stubbing, refinement. Like a bookmark. |
| `unpin` | `id`, `scope?` | Remove a pin by ID |
| `get_pins` | `project?` | List all active pins (session + permanent) |

### Agent Communication
| Tool | Parameters | Description |
|------|-----------|-------------|
| `send_to` | `target` (session ID), `content` | Send message to another Claude Code session |
| `broadcast` | `content`, `project` | Send message to all active sessions on a project |
| `whoami` | `project?` | Returns caller's session ID and agent metadata |

### Documentation
| Tool | Parameters | Description |
|------|-----------|-------------|
| `ingest_docs` | `name`, `path`, `version?`, `project?`, `domain?`, `rules?` | Import documentation (.md, .txt, .rst, .pdf). `rules=true` condenses into behavioral rules block for periodic re-injection |
| `list_docs` | `project?` | List indexed doc sources |
| `remove_docs` | `name`, `project?` | Delete doc source and chunks |

### Plan Management
| Tool | Parameters | Description |
|------|-----------|-------------|
| `set_plan` | `plan`, `scope?` | Set active plan, starts checkpoint reminders every ~20k tokens |
| `update_plan` | `plan?`, `completed?`, `add?`, `remove?` | Update plan — mark items done, add/remove items |
| `get_plan` | — | Get current active plan and status |
| `complete_plan` | — | Mark plan as done, stop all checkpoints |

### Agent Orchestration
| Tool | Parameters | Description |
|------|-----------|-------------|
| `spawn_agent` | `project`, `section`, `backend?`, `caller_session?`, `token_budget?`, `work_dir?` | Spawn agent as PTY subprocess (backend: `claude`/`codex`) |
| `list_agents` | `project?` | List all agents with status, PID, heartbeat |
| `get_agent` | `to`, `project?` | Detailed agent info |
| `relay_agent` | `to`, `content`, `project?` | Inject content into agent's PTY terminal |
| `stop_agent` | `to`, `project?` | Graceful agent shutdown |
| `stop_all_agents` | `project` | Stop all running agents in a project |
| `resume_agent` | `to`, `project?` | Resume frozen agent |
| `update_agent_status` | `id?`, `phase` | Update agent's semantic work phase |

### Scratchpad
| Tool | Parameters | Description |
|------|-----------|-------------|
| `scratchpad_write` | `project`, `section`, `content` | Write/overwrite a scratchpad section |
| `scratchpad_read` | `project`, `section?` | Read sections (omit section for all) |
| `scratchpad_list` | `project?` | List projects and their sections |
| `scratchpad_delete` | `project`, `section?` | Delete section or entire project |

---

## CLI Commands Reference (~49 commands)

### Core Services
| Command | Description |
|---------|-------------|
| `daemon` | Run background indexer with file watching (`--replace` to force restart) |
| `mcp` | Start MCP server on stdio |
| `proxy` | Start infinite-thread proxy (`--port`, `--threshold`, `--target`) |
| `stop [target]` | Stop daemon/proxy/all |
| `restart` | Stop all, clean up, start daemon fresh |

### Setup & Status
| Command | Description |
|---------|-------------|
| `setup` | Interactive 3-step wizard (Model → Provider → Confirm) with auth conflict resolution |
| `quickstart` | 7-phase bootstrap: extraction → evolution → narratives → clustering → profiles → persona → claudemd |
| `status` | Show index status |
| `version` | Show version |
| `uninstall` | Remove all YesMem registrations, restore pre-install auth state |
| `statusline` | Terminal status display: cache TTL, R/W/U token stats, collapsing savings (e.g. "340k → 98k, 72% saved"), keepalive ping timeline per thread, session ID. Auto-refreshes every 2s via `refreshInterval` in settings.json |

### Briefing & Persona
| Command | Description |
|---------|-------------|
| `briefing` | Generate session briefing |
| `bootstrap-persona` | Extract persona traits (`--force`, `--limit N`, `--all`) |
| `synthesize-persona` | Force re-synthesis of persona directive (uses Opus) |
| `regenerate-narratives` | Re-generate all session narratives |

### Extraction & Maintenance
| Command | Description |
|---------|-------------|
| `reextract` | Re-run extraction on sessions (`--project P`, `--last N`, `--dry-run`) |
| `embed-learnings` | Bulk-embed learnings into vector store (`--force`, `--all`, `--batch-size N`) |
| `resolve-stale` | List/resolve stale unfinished items (`--dry-run`) |
| `gap-review` | LLM-based review of knowledge gaps (`--project`, `--dry-run`, `--limit`) |
| `resolve-check` | Check commit message against open tasks (for git hooks) |
| `backfill-flavor` | Backfill session_flavor (`--last N`, `--dry-run`) |
| `claudemd` | Regenerate operative reference files (`--all`, `--dry-run`) |

### Documentation
| Command | Description |
|---------|-------------|
| `add-docs` | Ingest documentation (`--name`, `--path`/`--file`, `--project`, `--version`, `--domain`, `--skill`) |
| `sync-docs` | Re-sync doc sources (`--name N`/`--project P`/`--all`) |
| `list-docs` | List registered documentation sources (`--project P`) |
| `remove-docs` | Remove doc source and chunks (`--name N`, `--project P`) |

### Export & Cost
| Command | Description |
|---------|-------------|
| `export [path]` | Export learnings + persona to JSON |
| `import <file>` | Import learnings from JSON |
| `cost` | Show API cost summary |
| `migrate-messages` | Move messages to separate messages.db + build FTS5 index |
| `check-update` | Check if a newer version is available on GitHub Releases |
| `update` | Download, verify, install, migrate, and restart |
| `migrate` | Post-update migration: DB schema, directories, config merge, hooks |

### Hooks (called by Claude Code, not directly)
| Command | Event | Description |
|---------|-------|-------------|
| `briefing-hook` | SessionStart | Generate and inject briefing |
| `micro-reminder` | UserPromptSubmit | Inject matching learnings per user message |
| `idle-tick` | Periodic | Dynamic yesmem-usage reminder |
| `hook-check` | PreToolUse | Warn about known gotchas |
| `hook-failure` | PostToolUseFailure | Learn gotcha + deep search (combined) |
| `hook-resolve` | PostToolUse | Auto-resolve tasks on commit |
| `hook-think` | PostToolUse | Capture reasoning blocks |
| `session-end` | Stop | Session cleanup and extraction trigger |
| `hook-learn` | PostToolUseFailure | *(legacy, use hook-failure)* |
| `hook-assist` | PostToolUseFailure | *(legacy, use hook-failure)* |

---

## Daemon RPC Methods (~75 methods)

The daemon exposes ~70 RPC methods over Unix socket. MCP tools are a subset; additional internal methods:

| Method | Used by |
|--------|---------|
| `vector_search` | Internal (pure vector search) |
| `store_compacted_block` | Proxy (save compressed stubs) |
| `get_proxy_state` / `set_proxy_state` | Proxy (runtime state, save_count heuristic) |
| `index_status` | Status display |
| `idle_tick` | Idle detection |
| `track_gap` / `resolve_gap` / `get_active_gaps` | Knowledge gap tracking |
| `get_learnings_since` | Incremental learning fetch |
| `generate_briefing` | Briefing hook |
| `increment_hits` / `increment_noise` / `increment_match` / `increment_inject` / `increment_use` / `increment_save` | Counter updates (6 separate endpoints) |
| `flag_contradiction` | Evolution system |
| `pop_recent_remember` | ~~Deprecated (proxy path)~~ — still used by assemble.go (OpenClaw). See Fresh Remember Injection note above |
| `update_fixation_ratio` | Proxy (persist session fixation ratio after collapse) |
| `get_rules_block` | Proxy (fetch condensed CLAUDE.md rules for re-injection) |
| `set_plan` / `update_plan` / `get_plan` / `complete_plan` | MCP + Proxy (plan lifecycle with checkpoint injection) |
| `reload_vectors` | Vector store hot-reload |
| `ping` | Health check |
| `fork_extract_learnings` | Proxy fork (store extracted learnings with source="fork") |
| `fork_evaluate_learning` | Proxy fork (apply verdict actions + update impact_score) |
| `fork_update_impact` | Proxy fork (standalone impact score update) |
| `get_fork_learnings` | Proxy fork (fetch previous fork learnings for session) |
| `fork_resolve_contradiction` | Proxy fork (increment fail_count on both sides of a contradiction) |

---

## Database Schema (~38 tables + 4 FTS5 virtual tables)

### Core Memory
- `sessions` — chat sessions with project, timestamps, parent linkage, agent type, fixation_ratio, agent_role
- `learnings` — 37-column knowledge store (content, category, task_type, 6 counters, stability, emotional_intensity, importance, V2 metadata, sourcing, temporal validity, agent_role, dialog_id, quarantined_at, impact_score, impact_count)
- `learnings_fts` — FTS5 virtual table with insert/delete/update triggers

### Messages (separate `messages.db`)
- `messages` — session messages (role, content, content_blob, tool_name, file_path, sequence)
- `messages_fts` — FTS5 full-text index over message content (incl. thinking blocks copied from content_blob)

Thinking blocks are stored in `content_blob` by the parser but copied to `content` during insert — making them searchable via FTS5 without separate enrichment queries.

### Learning Metadata (V2)
- `learning_entities` — files, systems, people (junction table)
- `learning_actions` — commands, operations (junction table)
- `learning_keywords` — semantic keywords (junction table)
- `learning_anticipated_queries` — concrete search phrases for better vector retrieval (junction table)

### Context-Aware Retrieval
- `query_log` — persisted query vectors + injected learning IDs from every hybrid_search call. **30-day retention** for clustered entries (unclustered preserved). Automatic cleanup every 30 minutes via `PurgeOldQueryLogs()`. Indexed on `created_at` and `cluster_id`.
- `query_clusters` — semantically similar queries grouped by agglomerative clustering (cosine 0.80). Daemon timer runs every 30 minutes: Phase 1 assigns to existing clusters, Phase 2 creates new clusters from unmatched queries (min size 2).
- `learning_cluster_scores` — tracks per-learning per-cluster performance: inject_count, use_count, noise_count. Fed by the feedback loop.
- **Cluster-Affinity Scoring:** During hybrid_search, the query vector is matched to the nearest cluster. Learnings with cluster scores get an affinity multiplier: `1.0 + useRate - noiseRate*0.5` (clamped 0.3–1.8). Requires min 3 injections for statistical significance.
- **Feedback Loop (Stufe 4):** When `increment_use` or `increment_noise` is called for a learning, the signal propagates to the cluster score via `GetRecentClusterForLearnings`. This closes the loop: injection → use/noise → cluster score → future ranking.

#### Pipeline Summary

| Stufe | Was | Status |
|-------|-----|--------|
| 1 | Query-Log: persist query vectors + injected IDs | ✅ |
| 2 | Query-Clustering + Cluster-Affinity Scoring | ✅ |
| 3 | Anticipated Queries in Extraction | ✅ |
| 4 | Feedback-Loop: use/noise → cluster scores | ✅ |

### Documentation
- `doc_sources` — registered doc sources (name, version, path, project, chunk_count, is_skill, is_rules)
- `doc_chunks` — document chunks (source_id, heading_path, section_level, content, tokens_approx, metadata JSON — languages, version_added, deprecated_since, admonition, rst_entities, rst_doc_refs)
- `doc_chunks_fts` — FTS5 for doc search (Porter stemmer + unicode61)

### Knowledge Structure
- `associations` — graph edges between knowledge entities
- `learning_clusters` — grouped learnings (agglomerative clustering)
- `contradictions` — identified conflicts between learnings
- `knowledge_gaps` — topics needing coverage (topic, hit_count, resolved_by)

### Persona & Profiles
- `persona_traits` — individual traits per dimension (confidence, source)
- `persona_directives` — synthesized directive text (traits_hash, model_used)
- `project_profiles` — auto-generated project summaries

### Proxy & State
- `compacted_blocks` — compressed conversation stubs (thread_id, start_idx, end_idx, content)
- `proxy_state` — proxy runtime key-value state
- `embedding_cache` — cached embeddings to avoid re-computation

### Agent Communication
- `agent_dialogs` — 1:1 dialog state machine (initiator, partner, topic, status: pending/active/ended)
- `agent_messages` — dialog messages (dialog_id, sender, target, content, read flag)
- `agent_broadcasts` — project-wide broadcasts (sender, project, content, read_by tracking)

### Operations
- `index_state` — file indexing progress tracking
- `session_tracking` — why a session is being tracked (e.g., session recovery after /clear)
- `strategic_context` — long-term context with supersede support
- `file_coverage` — files touched per project
- `self_feedback` — Claude's self-corrections and patterns
- `refined_briefings` — cached briefings (raw_hash → refined_text)
- `daily_spend` — API cost tracking per day/model
- `claudemd_state` — operative reference generation state
- `plans` — active plan storage (keyed by thread_id)
- `turn_counters` — token counters for proxy injection scheduling
- `pinned_learnings` — bookmarked instructions (session + permanent scope)
- `agents` — spawned agent lifecycle tracking
- `scratchpad_entries` — shared whiteboard for multi-agent collaboration
- `token_usage` — per-session token accounting
- `fork_coverage` — tracks which message ranges have been processed by forked agents (dedup)

---

## Internal Packages (27)

| Package | Purpose |
|---------|---------|
| `archive` | Permanent JSONL archiving (protects against Claude Code's 30-day cleanup) |
| `benchmark` | LoCoMo benchmark evaluation framework |
| `bloom` | Per-session bloom filters (~4KB each) for fast negative lookups |
| `briefing` | Session briefing generation, milestones, dedup, i18n, formatting |
| `buildinfo` | Build-time version injection |
| `claudemd` | Operative reference generation from learnings |
| `clustering` | Agglomerative clustering on learning embeddings (cosine 0.85) |
| `config` | YAML configuration loader |
| `daemon` | RPC handlers (~70 methods), lifecycle, Unix socket server, extraction orchestration |
| `embedding` | Vector DB, SSE inference (512d, go:embed, pure Go) |
| `extraction` | Multi-phase LLM pipeline (extract → evolve → narrate), batch cycle every 2h |
| `graph` | File/command/project association graph |
| `hints` | Timestamp hints with anti-habituation rotation (33 variants) |
| `hooks` | Claude Code hook handlers (briefing, check, failure, think, resolve, session-end, idle) |
| `httpapi` | HTTP API adapter for daemon (external integrations) |
| `indexer` | File watcher, session tracking, subagent detection, JSONL archiving |
| `ingest` | Doc ingestion, format-aware chunkers (Markdown via goldmark, RST via custom parser), rich metadata extraction, hash tracking, skill detection |
| `ivf` | Inverted file index for vector search |
| `mcp` | MCP server (stdio), daemon proxy client, tool registration (49 tools) |
| `models` | Shared data types, scoring (5-count, Ebbinghaus, contextual), model ID resolution |
| `orchestrator` | Multi-agent lifecycle: PTY bridge, spawn, heartbeat, crash recovery |
| `parser` | JSONL session parser (Claude Code + Codex) |
| `proxy` | Infinite-thread HTTP proxy (stubs, decay, narrative, collapse, reexpand, sawtooth, signals, reminders, associative context, compression, fixation detection, skill injection) |
| `setup` | Interactive configuration wizard |
| `storage` | SQLite persistence (~37 tables across 3 DB files: yesmem.db, messages.db, runtime.db), migrations, FTS5, CRUD |
| `telegram` | Telegram integration for notifications |
| `textutil` | Token estimation, text similarity, bigram-Jaccard |
| `update` | Auto-update: semver parsing, GitHub Release checker, download + checksum, atomic swap, orchestrator |

---

## Configuration Reference

Full configuration in `~/.claude/yesmem/config.yaml`. See `config.example.yaml` for all options with documentation.

### Key settings

```yaml
extraction:
  summarize_model: haiku          # Pass 1
  model: sonnet                   # Pass 2
  narrative_model: opus           # Narratives, persona
  quality_model: sonnet           # Dedup, quality
  mode: prefiltered                  # full | prefiltered (default: prefiltered)
  chunk_size: 25000               # tokens per LLM call
  auto_extract: true
  max_age_days: 0                 # 0 = all
  max_per_run: 30

llm:
  provider: auto                  # auto | api | cli | openai | openai_compatible
  daily_budget_extract_usd: 5.0
  daily_budget_quality_usd: 2.0

evolution:
  auto_resolve: true
  unfinished_ttl_days: 30

briefing:
  detailed_sessions: 3
  other_projects_days: 90
  max_tokens: 5000
  dedup_threshold: 0.4
  max_per_category: 5

proxy:
  listen: ":9099"
  target: "https://api.anthropic.com"
  token_threshold: 250000
  token_minimum_threshold: 100000
  token_thresholds:                 # per-model overrides
    opus: 500000
    sonnet: 250000
    haiku: 150000
  keep_recent: 10
  sawtooth_enabled: true
  cache_ttl: "ephemeral"              # default: 5min TTL (cheaper than 1h; auto-detected overrides this)
  usage_deflation_factor: 0.7

pricing:                            # per-million-token, configurable
  haiku:      { input: 1.0, output: 5.0 }
  sonnet:     { input: 3.0, output: 15.0 }
  opus:       { input: 15.0, output: 75.0 }
  gpt-5-mini: { input: 0.25, output: 2.0 }
  gpt-5.2:    { input: 1.75, output: 14.0 }
  gpt-5.4:    { input: 2.5, output: 15.0 }

embedding:
  provider: local
  local:
    model: sse  # 512d, multilingual, Stable Static Embeddings, go:embed
  vector_db:
    persist_dir: ~/.claude/yesmem/vectors
    collection: learnings

signals:
  enabled: true
  mode: reflection
  model: haiku
  every_n_turns: 1

claudemd:
  enabled: true
  max_per_category:
    gotcha: 15
    pattern: 10
    decision: 10
    explicit_teaching: 5
    pivot_moment: 5
  refresh_interval: "2h"
  min_sessions: 3
```

---

## Deployment

```bash
cd ~/memory/yesmem
make build                      # Build with correct GOROOT/GOPATH/GOCACHE
make deploy                     # Build + deploy to ~/.local/bin/yesmem
```

After deploy: `/mcp reconnect` in Claude Code to restart MCP server.

### CI/CD Pipeline

GitHub Actions workflows (`.github/workflows/`):
- **ci.yml** — Runs on every push to `main` + PRs. Test matrix: Ubuntu + macOS. Validates build and tests on both platforms.
- **release.yml** — Runs on version tags (`v*`). Tests on Ubuntu + macOS must pass, then GoReleaser builds binaries for `linux/darwin × amd64/arm64`. Creates GitHub Release with checksums.
- Release flow: `git tag v1.0.3 && git push origin v1.0.3` → GitHub Actions builds + publishes.

**systemd:** User services with `After=network-online.target` + `Wants=network-online.target` (required for DNS after suspend/resume).

**Optimal migration/rebuild sequence:**
```bash
yesmem reextract                # re-extract all sessions
yesmem embed-learnings --all    # embed all learnings
yesmem regenerate-narratives    # regenerate narratives
yesmem bootstrap-persona --all  # re-extract persona
yesmem synthesize-persona       # re-synthesize directive
yesmem claudemd --all           # regenerate ops files
```

---

## 19. Multi-Agent Communication & Memory Safety

YesMem enables multiple Claude Code sessions to communicate and share long-term memory safely.

### 19.1 Agent-to-Agent Messaging

Direct messaging between Claude Code sessions via Channel system:

| Tool | Parameters | Description |
|------|-----------|-------------|
| `send_to` | `target` (session ID), `content` | Send message to another session |
| `broadcast` | `content`, `project` | Send message to all sessions on a project |

### 19.2 Message Delivery (Proxy-Based)

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

### 19.3 Polling (CronCreate)

The proxy can only inject content when a request passes through. For idle sessions, CronCreate provides periodic polling:

- Dialog invitations include a DIREKTIVE instructing Claude to start a CronCreate (`*/1 * * * *`)
- CronCreate fires a prompt → triggers API request → proxy injects pending messages
- The CronCreate prompt checks for `📨 DIALOG` blocks in the context, NOT via MCP check_messages (avoids echo issues)
- **Limitation:** Claude can forget or fail to start the CronCreate — behavioral, not technical

### 19.4 Session Identity Resolution

Three-tier resolution for mapping tool calls to sessions:

| Priority | Method | Source | Reliability |
|----------|--------|--------|-------------|
| 1 | Direct `session_id` parameter | Proxy passes to daemon | 100% |
| 2 | Direct `_session_id` parameter | MCP server passes to daemon | 100% |
| 3 | PID reverse-lookup | `_caller_pid` → pidMap | High (fails after daemon restart) |
| 4 | `activeSessionID` fallback | Last registered session | Low (wrong with concurrent sessions) |

**PID persistence:** `hook-think` writes PID→session_id files to `$dataDir/sessions/$PID` on every `UserPromptSubmit`. Survives daemon restarts — MCP server can read PID files on startup.

### 19.5 Broadcast (1:n)

One agent sends a message to all sessions on the same project:

- **Dedicated `agent_broadcasts` table** — project column, `read_by` tracking per session
- **`filepath.Base()` matching** — handles both full paths and short project names
- **24h TTL** — broadcasts auto-expire, no accumulation
- **Auto-broadcast:** Gotchas and high-importance decisions (`importance ≥ 4`) are automatically broadcast to all project sessions when saved via `remember()`

### 19.6 Memory Safety (Multi-Agent Mitigations)

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

### 19.7 Agent Roles

Sessions and learnings carry an `agent_role` field (e.g., `code`, `marketing`, `design`, `debug`, `review`). This enables:

- **Conflict detection:** Same topic from different roles is divergence (kept), not duplication
- **Associative context:** Role-matching learnings score higher in injection budget
- **Accountability:** Every learning traces back to which role produced it

Note: The Persona system describes the **user** (traits, preferences, expertise), not Claude's role. Subagents get their role via the Agent tool prompt. Role-persona overlays were removed as a design error — the `agent_role` field is purely for provenance tracking and conflict detection.

### 19.8 Channel-Ready Architecture (Future)

The dialog system is designed for seamless migration to Claude Code Channels when available:

- **Current:** Proxy-based injection with `<channel source="yesmem-dialog">` tags + CronCreate polling
- **Future:** MCP Channel server (`channel/index.mjs`) sends `notifications/claude/channel` push notifications
- **Channel requirements (Research Preview):** `--dangerously-load-development-channels` flag + claude.ai login (not API keys)
- **Channel advantages:** Push as User-Turn (not content block), idle-only delivery (buffered), no polling needed
- **Migration path:** Replace proxy injection with channel notification push — same daemon, same DB, same tools

### 19.9 Design Principles

- **`remember()` is the broadcast** — explicit saves surface via associative context to all agents automatically
- **Dialog is for questions, not knowledge transfer** — use `remember()` to persist, dialog to discuss
- **No auto-extract from dialogs** — only explicit `remember()` from dialog context becomes a learning (prevents agents from burning false info into each other)
- **Proxy over hooks** — proxy sees every request with correct session_id from metadata; hooks have timing issues and daemon restart fragility
- **Shared Scratchpad** — structured whiteboard for n:n collaboration. Each agent writes its own section (`scratchpad_write`), all agents read the full document each turn (`scratchpad_read`). CRUD via MCP tools: `scratchpad_write`, `scratchpad_read`, `scratchpad_list`, `scratchpad_delete`.

### 19.10 Agent Orchestrator (Daemon-Managed Agents)

Full lifecycle management for sub-agents spawned as PTY subprocesses:

| Tool | Parameters | Description |
|------|-----------|-------------|
| `spawn_agent` | `project`, `section`, `backend?`, `caller_session?`, `token_budget?`, `work_dir?` | Spawn a new agent process with PTY bridge |
| `list_agents` | `project?` | List all agents with status, PID, heartbeat |
| `get_agent` | `to`, `project?` | Detailed agent info (progress, relay count, errors) |
| `relay_agent` | `to`, `content`, `project?` | Inject content into agent's terminal via PTY |
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

### 19.11 Multi-Backend Support

Sub-agents can use different LLM backends:

| Backend | CLI | Prompt Injection | MCP Tools | Status |
|---------|-----|-----------------|-----------|--------|
| `claude` (default) | Claude Code | PTY inject after 7s delay | Full YesMem proxy integration | Live |
| `codex` | OpenAI Codex CLI | CLI argument (no PTY inject) | MCP-only (no proxy channel) | Blocked by Codex bug |

```
spawn_agent(project="yesmem", section="recherche", backend="codex")
```

**Codex-specific:**
- `--full-auto --no-alt-screen` flags
- `approval_policy = "never"` in `~/.codex/config.toml` (should bypass prompts — currently bugged in v0.116.0)
- Communicates exclusively via MCP tools (scratchpad, send_to, remember)
- YesMem registered as MCP server in Codex config

**Backend abstraction in `handler_agents.go`:**
- `buildAgentCommand()` switch on backend for CLI args
- `ensureAgentPermissions()` only for Claude (pre-approve MCP tools in `.claude/settings.json`)
- Prompt injection goroutine conditional (Claude: PTY inject after delay, Codex: CLI arg)

### 19.12 Heartbeat & Message Delivery

Two-stage delivery system ensures reliable agent-to-agent messaging:

```
Agent B sends send_to(target=C) → message stored in agent_messages table
                                → heartbeat ping injects "[Message pending]" hint
                                → next proxy request from C delivers actual content
                                → message marked as delivered
```

- **Heartbeat interval:** 2s ticker, monitors agent liveness
- **Freeze detection:** agents frozen after N unread relay messages
- **Delivery tracking:** per-message `delivered`, `delivered_at`, `delivery_retries`, `delivery_failed`

### 19.12b Agent Supervision

Automatic lifecycle management via the heartbeat system (`internal/daemon/heartbeat.go`):

- **Dead PID detection:** `detectDeadPIDs()` uses `os.FindProcess()` + zero-signal probe (`Signal(0)`) to detect dead processes, marks agent status as "error"
- **Orphan detection:** `detectOrphanedAgents()` checks `liveness_ping_at` with 5-minute grace period (`livenessPingGrace`), triggers cascade stop after grace expires
- **Cascade stop:** When a parent agent stops, child agents are stopped automatically
- **Limit enforcement:** `enforceAgentLimits()` freezes (not kills) agents exceeding `max_turns` (default 30), `token_budget`, or `max_runtime` (default 30 minutes) — frozen agents can be resumed later
- **Auto-restart:** `attemptRestart()` with configurable strategies: `temporary` (capped at `max_restarts`, default 3) and `permanent` (unlimited restarts). 30s guard between restarts to prevent race conditions

### 19.13 Crash Recovery

Strategy-based recovery for crashed agents:

- **Restart strategies:** `temporary` (capped by `max_restarts`, default 3) and `permanent` (unlimited restarts)
- **Crash quarantine:** `quarantine_session()` isolates all learnings from crashed session, taints scratchpad section
- **Crash context:** error message and stack trace stored in agent record
- **Graceful daemon restart:** OTP-style hot reload — running agents recovered on daemon restart

### 19.13b Agent Telemetry

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

### 19.14 /swarm Orchestration Protocol

The `/swarm` skill provides a structured protocol for multi-agent orchestration:

- **Agent A (Orchestrator):** reads plan, spawns sub-agents, monitors progress, writes report
- **Communication:** scratchpad (primary) + send_to (secondary)
- **DAG mode:** execution dependencies between agents (B must finish before C starts)
- **Budget strategies:** quality (all Opus), balanced (Opus orchestrator + Sonnet workers), economy (all Sonnet)
- **Status ping:** orchestrator checks agent status every 5 minutes

### 19.15 Persistent-Orchestrator Skill

Resume-based multi-agent pipeline for structured Implement → Review → Commit workflows (`skills/persistent-orchestrator.md`):

- **Resume-centric:** Agents are always resumed via scratchpad state, not respawned from scratch (except on error)
- **Sequential stages:** Implementer (code writing) → Reviewer (one-shot code review) → Committer — strict ordering with manual approval handoffs
- **Scratchpad state:** JSON arrays in sections (`task-queue`, `current-task`, `auftrag-implementer`) survive session restarts
- **Patience rules:** 3-minute minimum wait before checking sub-agents; never stops from impatience
- **Telemetry-based health checks:** Uses `get_agent` to monitor turns_used, tokens, and phase before decisions
- **Differs from /swarm:** /swarm does parallel dispatch with DAG dependencies; persistent-orchestrator enforces strict sequential stages with resume-first semantics

---

## 20. Auto-Update

Self-updating binary distribution via GitHub Releases — no Git required on target systems.

### Update Pipeline

1. **Check** — `GET /repos/carsteneu/yesmem/releases/latest`, semver comparison with `buildinfo.Version`
2. **Download** — platform-specific archive (`yesmem_{os}_{arch}.tar.gz`) with SHA256 checksum verification against `checksums.txt`
3. **Replace** — atomic swap: write to temp file → `os.Rename` to target path. Old binary preserved as `.bak`
4. **Migrate** — `yesmem migrate` runs DB schema migration, directory creation, config merge (new defaults without overwriting user values), hook updates
5. **Restart** — `yesmem restart` restarts daemon + proxy with the new binary

### Safety

- **SHA256 checksum verification** — archive validated before extraction
- **io.LimitReader** (100 MB) — prevents memory exhaustion from malicious payloads
- **Extraction guard** — daemon skips update while extraction or indexing is running (atomic `extractionActive` flag)
- **Atomic binary swap** — temp file + rename, never partial writes to live binary
- **Backup** — old binary always preserved as `yesmem.bak` for manual rollback
- **exec timeouts** — migrate and restart commands have 20s timeout via `context.WithTimeout`
- **Non-semver handling** — commit hashes (e.g. `7ba6267`) always trigger update offer

### Daemon Integration

- Periodic ticker (configurable via `update.check_interval`, default `6h`)
- Respects `update.auto_update` config flag (default: `true`)
- Skips check when extraction or indexing is active
- Logs all update activity: check, download, install, restart

### CLI Commands

| Command | Description |
|---------|-------------|
| `check-update` | Check if a newer version is available on GitHub Releases |
| `update` | Download, verify, install, migrate, and restart |
| `migrate` | Post-update migration: DB schema, directories, config merge, hooks |

### Configuration

```yaml
update:
  auto_update: true        # periodic daemon checks (default: true)
  check_interval: "6h"     # how often to check (default: 6h)
  channel: "stable"        # release channel (future: beta)
```

---

## Differentiators

What makes YesMem unique compared to other memory systems:

1. **Source Attribution** — every learning tracks origin (user_stated, claude_suggested, llm_extracted). No other system in 31 analyzed research papers has this.
2. **5-Count Scoring** — orthogonal counters prevent scoring inflation from mere visibility
3. **Ebbinghaus Decay** — scientifically grounded spaced repetition, not arbitrary TTLs
4. **Emotional Memory** — sessions rated by intensity, learnings from breakthroughs score higher
5. **Persona Engine** — persistent identity across sessions, not just fact storage
6. **Infinite Thread** — proxy-based context compression with sawtooth caching, progressive decay, and context recovery
7. **Domain-agnostic Engine** — same mechanisms work for code, marketing, legal, finance
8. **Two-layer Architecture** — raw material (sessions/docs) preserved for deep search; distillate (learnings) for quick access
9. **Zero-dependency Binary** — single Go binary with embedded SSE model, no external services
10. **Cognitive Signals** — self-reflection loop that improves knowledge quality over time
11. **Associative Context** — automatic semantic priming on every user message
12. **Trust-Based Evolution** — high-trust learnings resist blind superseding
13. **Hook Integration** — event-driven, not polling; gotcha prevention before errors happen
14. **Cross-Platform Memory** — HTTP API enables external integrations (OpenClaw) alongside native Unix socket
15. **Fixation Detection** — automatic detection of pathological error loops with proportional scoring penalty
16. **Multi-Agent Memory Safety** — shared long-term memory with dialog-extract-block, conflict detection, agent-role scoping, and auto-broadcast
