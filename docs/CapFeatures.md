# Capability Memory — Feature Documentation

Summary of all Capabilities features: design, architecture, implementation status, open issues. Consolidated from spec, plans, git history, code audit, and session learnings. Verified against code state `feat+capability-memory` 2026-04-21.

---

## 1. Problem Statement

Claude Code loses **learned tool knowledge** between sessions: REPL snippets, API wrappers, analysis pipelines, data fetchers. Every new session starts from scratch — even when identical tools were built and tested in prior sessions.

**Goal:** Claude should remember self-built tools, know what exists, and activate them on demand — without token bloat from permanently loaded schemas.

---

## 2. Three-Layer Architecture

```
┌───────────────────────────────────────────────────────────────────┐
│ LAYER A — Catalog in Briefing (session start, once)               │
│   Name + one-liner per capability. No schemas.                    │
│   ~80 tokens/cap → 100 caps ≈ 8k tokens (cached).                │
│   renderCapabilitiesCatalog() in caps_inject.go.                  │
│   Format: <capabilities-available> block as system-reminder.      │
│   Contains bootstrapper: registerTool("activate_cap",...)   │
│                                                                     │
│ LAYER B — Deliberative Activation (per session, on-demand)        │
│   Bootstrapper REPL tool "activate_cap" calls internally    │
│   MCP tool "activate_cap".                                         │
│     1. Loads cap from DB (learnings with category="cap")           │
│     2. Returns generated registerTool() code                       │
│     3. Records cap in session_active_caps                          │
│   ⚠ Naming discrepancy: bootstrapper references                   │
│     mcp__yesmem__activate_cap, MCP registers              │
│     activate_cap. Works only if alias exists.                      │
│                                                                     │
│ LAYER C — Proxy Schema Injection (from turn N+1)                   │
│   Proxy reads active caps for the session via capsCache,           │
│   calls injectCapabilitiesTurn(),                                  │
│   appends their schemas to req["tools"].                           │
│   Tool becomes a native API call — no REPL detour.                 │
└───────────────────────────────────────────────────────────────────┘
```

**Why deliberative instead of embedding-ranked:** Auto-injection via cosine match was evaluated and rejected. The model itself selects more reliably than a heuristic ranker once the directory is visible. No false-positive risk, no vector ingest pipeline required.

---

## 3. Data Model

### Capabilities as Learnings

Capabilities are **not a separate table**, but learnings with `category = "cap"`. Reuses the existing supersede/search/embed infrastructure.

```
learnings (yesmem.db)
├── id, content, category="cap", source, project
├── context → JSON: CapMeta (name, description, schema, handler_repl, handler_bash, tags, version, tested, auto_active)
├── keywords → tags for filtering
└── superseded_by → auto-supersede on matching cap name
```

### CapMeta Struct (`internal/daemon/cap_meta.go`)

```go
type CapMeta struct {
    Name        string   `json:"cap_name"`
    Description string   `json:"cap_description,omitempty"`
    Schema      string   `json:"cap_schema,omitempty"`
    HandlerREPL string   `json:"cap_handler_repl,omitempty"`
    HandlerBash string   `json:"cap_handler_bash,omitempty"`
    Tags        []string `json:"cap_tags,omitempty"`
    Version     int      `json:"cap_version"`
    Tested      bool     `json:"cap_tested"`
    TestDate    string   `json:"cap_test_date,omitempty"`
    AutoActive  bool     `json:"cap_auto_active,omitempty"`
}
```

Functions:

| Function | Signature |
|----------|-----------|
| `ParseCapMeta` | `func ParseCapMeta(s string) (CapMeta, error)` |
| `ToJSON` | `func (m CapMeta) ToJSON() (string, error)` |
| `HasTag` | `func (m CapMeta) HasTag(tag string) bool` |

### Session Activation State

```sql
CREATE TABLE session_active_caps (
    thread_id    TEXT    NOT NULL,
    cap_name     TEXT    NOT NULL,
    activated_at INTEGER NOT NULL,
    last_used_at INTEGER,
    PRIMARY KEY (thread_id, cap_name)
);
```

Stored in `yesmem.db`. Proxy reads which caps are active per thread ID. `auto_active` is **not a column attribute** here, but a field in `CapMeta` (learning context JSON). **Default: true** — new caps are immediately available to all sessions by default.

### Cap Store (generic KV store)

Separate database `capabilities.db`. Each capability gets its own namespace-prefixed tables:

```
capabilities.db
├── cap_store_meta               — registry: which cap owns which tables
├── cap_{name}__{table}          — data tables (e.g. cap_reddit_search__listings)
└── cap_{name}__blobs            — blob chunks for >30KB payloads (planned)
    └── PRIMARY KEY (key, chunk_idx)
```

**Quotas:**
- Max 10 tables per capability
- Max 10,000 rows per table
- Max 64KB per cell
- Blob chunking: 60KB chunks (planned)

**Sandboxing:** Only `CREATE TABLE`, `INSERT`, `UPDATE`, `DELETE`, `SELECT`. No `DROP`, `ALTER`, `ATTACH`, `PRAGMA`. All values via `?` placeholders, names via regex allowlist (`^[a-z][a-z0-9_]{0,63}$`).

---

## 4. Component Map

### Proxy (Injection + Catalog)

**File: `internal/proxy/caps_inject.go`**

| Symbol | Kind | Description |
|--------|------|-------------|
| `CapInjection` | struct | Fields: `Name`, `Description`, `Schema`, `HandlerBash`, `HandlerREPL` |
| `renderCapabilitiesCatalog` | func | `func(caps []CapInjection) string` — generates `<capabilities-available>` block with bootstrapper code. Replaces the older `renderCapabilitiesBlock`. |
| `renderCapabilitiesBlock` | func | `func(caps []CapInjection) string` — older variant, lists caps as `<capabilities-active>` block. |
| `injectCapabilitiesTurnImpl` | func | `func(req map[string]any, threadID string, queryFn ..., capsCache *CapsCache, logger *log.Logger) bool` — appends active cap schemas to `req["tools"]`. Dedup: native tools win on name collision. |
| `injectCapabilitiesTurn` | method | `func (s *Server) injectCapabilitiesTurn(req map[string]any, threadID string) bool` — server method wrapper around `injectCapabilitiesTurnImpl`. |
| `decodeCapsResponse` | func | `func(raw json.RawMessage) ([]CapInjection, error)` — unmarshals daemon response into `[]CapInjection`. |
| `renderRegisterTool` | func | `func(c CapInjection) string` — generates a single `registerTool()` JS snippet for a cap. |

**File: `internal/proxy/caps_cache.go`**

| Symbol | Kind | Description |
|--------|------|-------------|
| `CapsCache` | struct | Thread-keyed cache. Fields: `mu sync.RWMutex`, `entries map[string][]byte`. |
| `NewCapsCache` | func | `func NewCapsCache() *CapsCache` — constructor. |
| `Get` | method | `func (c *CapsCache) Get(threadID string) ([]byte, bool)` — reads cache entry. |
| `Set` | method | `func (c *CapsCache) Set(threadID string, data []byte)` — writes cache entry (copy). |
| `Invalidate` | method | `func (c *CapsCache) Invalidate(threadID string)` — deletes cache entry. |
| `invalidateThreadCaches` | method | `func (s *Server) invalidateThreadCaches(threadID, project, projectDir string)` — invalidates frozenStubs, capsCache AND briefingCache for a thread. |
| `cachedQueryFn` | func | `func cachedQueryFn(cache *CapsCache, threadID string, upstream func(...) ...) func(...)` — wraps upstream daemon query, caches `get_active_caps` responses. |

**File: `internal/proxy/proxy_briefing.go`**

No capability functions. `renderCapabilitiesCatalog()` is in `caps_inject.go`, not here.

**File: `internal/proxy/proxy.go`**

Orchestration: calls `capsCache` + `injectCapabilitiesTurn()` in the request pipeline. Configures `cachedQueryFn()` as query upstream.

### Daemon (Handlers + Meta)

**File: `internal/daemon/cap_meta.go`**

| Function | Signature |
|----------|-----------|
| `ParseCapMeta` | `func ParseCapMeta(s string) (CapMeta, error)` |
| `ToJSON` | `func (m CapMeta) ToJSON() (string, error)` |
| `HasTag` | `func (m CapMeta) HasTag(tag string) bool` |

**File: `internal/daemon/handler_caps.go`**

| Function | Description |
|----------|-------------|
| `handleGetCaps` | `func (h *Handler) handleGetCaps(params map[string]any) (any, error)` — lists all capabilities (learnings with category="cap"). Filters: `name`, `tag`, `project`. |
| `handleSaveCap` | `func (h *Handler) handleSaveCap(params map[string]any) (any, error)` — saves/updates capability. Auto-supersede on matching name. `auto_active` default: **true**. |
| `handleRegisterCaps` | `func (h *Handler) handleRegisterCaps(params map[string]any) (any, error)` — batch hydration: generates `registerTool()` JS for multiple caps. |
| `handleActivateCap` | `func (h *Handler) handleActivateCap(params map[string]any) (any, error)` — activates a single cap for a thread, returns code. |
| `handleDeactivateCap` | `func (h *Handler) handleDeactivateCap(params map[string]any) (any, error)` — deactivates cap for thread. |
| `handleGetActiveCaps` | `func (h *Handler) handleGetActiveCaps(params map[string]any) (any, error)` — returns active caps for a thread (internal, for proxy query). |

Helper type: `capResult` struct (internal, for JSON serialization of responses).

**File: `internal/daemon/handler_cap_store.go`**

| Function | Description |
|----------|-------------|
| `handleCapStore` | `func (h *Handler) handleCapStore(params map[string]any) (any, error)` — dispatch for cap store actions. |

Internal helpers: `capStoreCreateTable`, `capStoreUpsert`, `capStoreQuery`, `capStoreDelete`, `capStoreListTables`, `parseColumnDefs`, `parseMapParam`.

**File: `internal/daemon/handler.go`**

Dispatch (lines ~250-260):

```go
case "get_caps":        return h.handleGetCaps(...)
case "save_cap":        return h.handleSaveCap(...)
case "register_caps":   return h.handleRegisterCaps(...)
case "activate_cap":    return h.handleActivateCap(...)
case "deactivate_cap":  return h.handleDeactivateCap(...)
case "get_active_caps": return h.handleGetActiveCaps(...)
case "cap_store":       return h.handleCapStore(...)
```

### Storage

**File: `internal/storage/session_caps.go`**

| Function | Signature |
|----------|-----------|
| `ActivateCap` | `func (s *Store) ActivateCap(threadID, name string) error` |
| `DeactivateCap` | `func (s *Store) DeactivateCap(threadID, name string) error` |
| `GetSessionCaps` | `func (s *Store) GetSessionCaps(threadID string) ([]SessionCap, error)` |
| `TouchCap` | `func (s *Store) TouchCap(threadID, name string) error` — updates `last_used_at`. |

Return type: `SessionCap` struct (thread ID + name + timestamps).

**File: `internal/storage/cap_store.go`**

| Function | Signature |
|----------|-----------|
| `OpenCapsDB` | `func (s *Store) OpenCapsDB() error` — opens separate `capabilities.db`. |
| `CloseCapsDB` | `func (s *Store) CloseCapsDB()` — closes `capabilities.db`. |
| `CapsReady` | `func (s *Store) CapsReady() bool` — checks whether capabilities.db is open. |
| `ValidateCapName` | `func ValidateCapName(name string) error` — regex validation `^[a-z][a-z0-9_]{0,63}$`. |
| `CapsCreateTable` | `func (s *Store) CapsCreateTable(cap, table string, columns []ColumnDef) error` — creates `cap_{name}__{table}`. |
| `CapsUpsert` | `func (s *Store) CapsUpsert(cap, table string, data map[string]any) error` |
| `CapsQuery` | `func (s *Store) CapsQuery(cap, table, where string, args []any, limit int) ([]map[string]any, error)` |
| `CapsQueryPaged` | `func (s *Store) CapsQueryPaged(cap, table, where string, args []any, limit, offset int) (QueryResult, error)` — with pagination metadata. |
| `CapsDelete` | `func (s *Store) CapsDelete(cap, table, where string, args []any) (int64, error)` — returns affected rows. |
| `CapsListTables` | `func (s *Store) CapsListTables(cap string) ([]TableInfo, error)` |

Helper types: `ColumnDef`, `TableInfo`, `QueryResult`.
Internal helpers: `resolveTableName`, `sanitizeWhere`, `createCapStoreSchema`.

**File: `internal/storage/schema.go`**

Schema migration for `session_active_caps` and `cap_store_meta`. v0.55 migration renamed `session_active_capabilities` → `session_active_caps` and `capability_name` → `cap_name`.

### MCP (Tool Exposure)

**File: `internal/mcp/server.go`**

Registers MCP tools (Claude Code sees them as `mcp__yesmem__<name>`):

| Tool name | Description |
|-----------|-------------|
| `activate_cap` | Activates cap for thread. Returns registerTool() JS. |
| `deactivate_cap` | Removes cap from thread state. |
| `get_caps` | Lists all capabilities (filters: name, tag, project). |
| `save_cap` | Saves/updates capability. Auto-supersede on matching name. |
| `register_caps` | Batch hydration: generates registerTool() JS for multiple caps at once. |
| `cap_store` | Generic KV store. Actions: create_table, upsert, query, delete, list_tables, table_exists. |
| `get_active_caps` | Internal query: active caps for a thread (for proxy query, not user-facing). |

**File: `internal/mcp/proxy.go`**

Forwarding to daemon via Unix socket. No capability-specific code — generic RPC relay.

### Briefing

**File: `internal/briefing/briefing.go`**

| Function | Signature |
|----------|-----------|
| `renderCaps` | `func renderCaps(caps []CapEntry) string` — renders caps in briefing text (older path via learnings). |

---

## 5. MCP Tools (Parameter Details)

| Tool | Parameters | Description |
|------|------------|-------------|
| `activate_cap` | `name` (required), `project?` | Activates cap for session. Returns `registerTool()` JS. `thread_id` is injected automatically. |
| `deactivate_cap` | `name` (required) | Removes cap from session state. Thread ID auto-injected. |
| `get_caps` | `project?`, `name?`, `tag?`, `limit?` | Lists all capabilities (from learnings with category=cap). |
| `save_cap` | `name`, `description`, `handler_repl?`, `handler_bash?`, `schema?`, `tags?`, `tested?`, `auto_active?` (default: **true**), `project?` | Saves/updates capability. Auto-supersede on matching name. `auto_active` is stored in CapMeta JSON. Default true — caps are immediately available to all sessions by default. Set explicitly to `false` to make a cap available only on manual activation. |
| `register_caps` | `names?`, `project?` | Batch hydration: generates `registerTool()` JS for multiple caps at once. |
| `cap_store` | `capability`, `action`, `table?`, `columns?`, `data?`, `where?`, `args?`, `limit?`, `offset?` | Generic KV store. Actions: `create_table`, `upsert`, `query`, `delete`, `list_tables`, `table_exists`. |
| `get_active_caps` | `thread_id` (auto-injected) | Internal proxy query. Returns active caps for a thread. |

---

## 6. Proxy Pipeline Integration

Capability injection fits into the existing proxy pipeline:

```
Incoming request
  → StripReminders
  → CompressContext
  → CalcCollapseCutoff
  → CollapseOldMessages / Stubify
  → ReplaceSystemBlock
     └─ renderCapabilitiesCatalog()    ← catalog in system-reminder (caps_inject.go)
  → StripOldNarratives
  → reexpandStubsFor
  → injectCapabilitiesTurn()           ← active caps appended to req["tools"] (caps_inject.go)
  → UpgradeCacheTTL / EnforceCacheBreakpointLimit
Request to Anthropic API
```

**Catalog injection** (`renderCapabilitiesCatalog` in `caps_inject.go`):
- Generates `<capabilities-available>` block as system-reminder
- Contains bootstrapper: `registerTool("activate_cap", ...)` as REPL tool
- Bootstrapper calls `mcp__yesmem__activate_cap` internally (⚠ see naming discrepancy)
- Lists all available caps with name + description as a table
- Rendered once per session

**Schema injection** (`injectCapabilitiesTurn` in `caps_inject.go`):
- Reads active caps via `cachedQueryFn` (cache + daemon fallback)
- Appends JSON schemas to `req["tools"]` in the API request
- Native tools take precedence on name collision (skipped with warn log)

**Cache** (`CapsCache` in `caps_cache.go`):
- In-memory cache keyed by thread ID
- Invalidated via `invalidateThreadCaches()` (also invalidates frozenStubs + briefingCache)
- `cachedQueryFn()` wraps daemon query: only caches `get_active_caps` responses

---

## 7. Blob Pipe (>30KB Payloads)

For capabilities that need HTTP fetches larger than 30KB (e.g. Reddit posts with many comments).

**Problem:** REPL output is truncated at ~30KB. Temp files require Read tool permission.

**Solution:** CLI subcommands `cap-blob-put` / `cap-blob-get`:

```
Producer → curl | yesmem cap-blob-put --cap NAME --key KEY
                    ↓
              cap_{NAME}__blobs (60KB chunks, auto-created)
                    ↓
Consumer → cap_store({action:"query", table:"blobs", ...})
                    ↓
              rows.map(r => r.data).join('')  → complete payload
```

**Package:** `internal/capblob/` (blob.go, blob_test.go). CLI: `yesmem cap-blob-put --cap NAME --key KEY`. Used by `reddit_fetch`.

---

## 8. REPL Pattern Detection (Repeated Patterns → Cap Suggestion)

Detects repeatedly executed shell commands and suggests building capabilities from them. This feature bridges ad-hoc REPL usage and persistent tool knowledge.

### Concept

```
Session N: sh('curl ... | jq ...')
Session N+1: sh('curl ... | jq ...')    ← same pattern
Session N+2: sh('curl ... | jq ...')    ← pattern count = 3
...
Session N+4: sh('curl ... | jq ...')    ← count ≥ 5 → Suggestion!

Proxy injects hint: "You have used this pattern 5× times.
  Consider building a capability from it with save_cap(...)."
```

### How It Works

1. **Normalization** (`repl_pattern.go`): Commands are normalized to "shape hashes" — variable parts (URLs, IDs, timestamps) are replaced by placeholders, so `curl https://api.example.com/v1/users/123` and `curl https://api.example.com/v1/users/456` produce the same hash.

2. **Recording** (proxy → daemon): Every REPL/bash tool call is reported by the proxy to the daemon (`record_repl_pattern`). The daemon stores shape hash + raw command + project + timestamp in the `repl_patterns` table.

3. **Detection** (`repl_pattern_detect.go`): Proxy checks per request whether a pattern suggestion is due. Triggers when:
   - Shape hash seen ≥ `minCount` times (default: 5) for the project
   - Pattern not yet saved as a cap
   - Pattern not dismissed
   - Pattern not "trivial" (filtered via `isTrivialShape`)

4. **Suggestion**: Proxy injects hint text into the next response, suggesting saving the pattern as a capability.

5. **Dismiss**: User can reject the suggestion. After 3 dismissals the pattern is permanently ignored.

### Trivial Shape Filter

Filters commands too generic to be useful as a capability:

- Pure `cd`, `ls`, `cat`, `echo` commands
- Simple `git status`, `git log`, `git diff` without complex flags
- `grep` without pipeline

### Components

**File: `internal/proxy/repl_pattern.go`**

| Symbol | Kind | Description |
|--------|------|-------------|
| `PatternShape` | struct | Fields: `Hash`, `Normalized`, `Raw`, `Tokens` |
| `NormalizeCommand` | func | `func NormalizeCommand(cmd string) PatternShape` — normalizes command to shape hash. |
| `isTrivialShape` | func | `func isTrivialShape(shape PatternShape) bool` — filters trivial commands. |

**File: `internal/proxy/repl_pattern_detect.go`**

| Symbol | Kind | Description |
|--------|------|-------------|
| `PatternSuggestion` | struct | Fields: `Hash`, `Raw`, `Count`, `Project` |
| `detectReplPattern` | func/method | Checks whether a pattern suggestion should be injected. Queries daemon `get_repl_patterns`. |
| `formatPatternSuggestion` | func | Generates the hint text for the suggestion. |

**File: `internal/daemon/handler_repl_patterns.go`**

| Function | Description |
|----------|-------------|
| `handleRecordReplPattern` | Saves shape hash + raw command + project to DB. |
| `handleGetReplPatterns` | Returns patterns with count ≥ threshold for a project. |
| `handleDismissReplPattern` | Marks pattern as dismissed. Permanent after 3×. |

**File: `internal/storage/repl_patterns.go`**

| Function | Description |
|----------|-------------|
| `RecordReplPattern` | `func (s *Store) RecordReplPattern(hash, normalized, raw, project string) error` |
| `GetReplPatterns` | `func (s *Store) GetReplPatterns(project string, minCount int) ([]ReplPattern, error)` |
| `DismissReplPattern` | `func (s *Store) DismissReplPattern(hash, project string) error` |
| `IsPatternDismissed` | `func (s *Store) IsPatternDismissed(hash, project string) (bool, error)` |

Schema: `repl_patterns` table (hash, normalized, raw, project, count, first_seen, last_seen, dismissed_count).

### MCP Tools

| Tool | Description |
|------|-------------|
| `record_repl_pattern` | Internal, called by proxy. Not user-facing. |
| `get_repl_patterns` | Returns frequently occurring patterns. |
| `dismiss_repl_pattern` | User rejects a suggestion. |

### Noise Reduction (live since 2026-04-20)

| Measure | File | Effect |
|---------|------|--------|
| **Deny-list extended** (+15: git, mkdir, rm, cp, mv, touch, chmod, chown, ln, export, source, exit, clear, history, wc) | `repl_pattern.go` | Trivial shell commands are not counted |
| **Session budget max 3** — `patternBudget map[string]int` on Server struct | `proxy.go` | After 3 suggestions per thread, no more |
| **Threshold 5→8** — pattern must repeat 8× | `handler_repl_patterns.go` | Filters short-lived repetitions |

Verified: 3 suggestions instead of 40+ per REPL-intensive session.

### Response Format (changed 2026-04-22)

`get_repl_pattern_suggestion` returns an envelope format:
```json
{"pattern": {"shape_hash": "...", "count": 8, ...}, "workflow": {"sequence_hash": "...", "count": 3, ...}}
```
Proxy parses via envelope struct with guard against empty `shape_hash`.

---

## 8b. Multi-Turn Workflow Sequence Detection (2026-04-22)

Detects recurring tool-call sequences across turns and suggests cap bundling.

### Architecture

- **One table** `thread_sequences` (thread_id PK, project, turn_hashes JSON max 20 FIFO, updated_at)
- **Turn hash** = tool type names from assistant turn → remove consecutive duplicates → join with `→` → SHA256[:16]
- **Workflow matching** = on-demand, no ticker. Extract 3-subsequences in-memory from all thread sequences of the same project, count, suggest when count ≥ 3 across different threads
- **Budget** = shares `patternBudget` (max 3/thread) with single-command pattern suggestions

### Files

| File | Contents |
|------|----------|
| `internal/storage/turn_sequences.go` | Schema, RecordTurnHash (FIFO upsert), GetWorkflowSuggestions |
| `internal/storage/turn_sequences_test.go` | 7 tests (FIFO, Append, Scope, Subsequence, False-Positive) |
| `internal/proxy/turn_sequence.go` | ExtractToolTypes, ComputeTurnHash, computeTurnHashFromMessages |
| `internal/proxy/turn_sequence_test.go` | 7 tests (Dedup, Empty, Length, Extraction) |

### Data Flow

1. Proxy receives request with user message
2. Extracts tool types from the last assistant turn before the user message
3. Computes turn hash (deduplicated, 16-char)
4. Sends async via RPC `record_turn_sequence` to daemon
5. Daemon upserts into `thread_sequences` (FIFO ring buffer, max 20)
6. On `get_repl_pattern_suggestion`, daemon loads all sequences for the project, extracts 3-subsequences, returns count ≥ 3 as `workflow` in the envelope

---

## 9. Fixation Detector (Infinite Loop Detection)

Detects when Claude is stuck in an unproductive loop — e.g. repeatedly retrying the same failing build, or editing the same file over and over.

### Three Fixation Signals

| Signal | Threshold | Description |
|--------|-----------|-------------|
| Consecutive Error Runs | ≥ 8 | Consecutive tool calls that end with an error. |
| Edit-Build-Error Cycles | ≥ 6 | Repeated pattern: edit file → build/test → error → edit same file. |
| Excessive File Retries | ≥ 10 | Same file edited ≥10× within a sequence. |

### Components

**File: `internal/proxy/fixation_detector.go`**

| Symbol | Kind | Description |
|--------|------|-------------|
| `FixationResult` | struct | Fields: `IsFixated bool`, `Ratio float64`, `Signal string`, `Details string` |
| `DetectFixation` | func | `func DetectFixation(messages []Message) FixationResult` — analyzes message history for fixation signals. |
| `countConsecutiveErrors` | func | Counts consecutive error tool calls. |
| `detectEditBuildCycles` | func | Detects Edit → Build → Error cycles. |
| `countFileRetries` | func | Counts edits per file. |

### Proxy Integration

The fixation detector is called in the proxy pipeline. When fixation is detected, a hint is injected prompting Claude to change strategy.

---

## 10. Existing Capabilities (Examples)

Capabilities registered in the system (as of 2026-04-21):

| Capability | Description | Type |
|------------|-------------|------|
| `reddit_fetch` | Fetch Reddit post + comments + links | handler_repl |
| `reddit_search` | Search Reddit, classify + persist results | handler_repl |
| `cap_search` | Generic search over store() primitive tables | handler_repl |
| `cap_collect` | Collect-and-prep over store() primitives for analysis | handler_repl |
| `cap_save_analysis` | Persist analysis results append-only | handler_repl |
| `reddit_research` | Topic research: parallel search, fetch top posts, haiku() classification + synthesis | handler_repl (composite) |
| `cap_delete` | Remove capability completely (learnings DB + cap_store tables) | handler_repl |
| `proxy_health` | Proxy/daemon health from journalctl, count errors, store in cap_store | handler_repl |

---

## 11. Commit History (chronological)

```
2026-04-15  feat(models): add 'capability' category (later migrated to 'cap')
2026-04-15  feat(daemon): CapMeta type (cap_meta.go)
2026-04-15  feat(daemon): handleGetCaps / handleSaveCap
2026-04-15  feat(daemon): handleRegisterCaps (Batch-Hydration)
2026-04-16  feat(briefing): renderCaps() + Tests
2026-04-16  feat(mcp): register get_caps/save_cap/register_caps tools
2026-04-17  feat(storage): session_active_caps table + methods
2026-04-17  feat(daemon): handleActivateCap/handleDeactivateCap handlers
2026-04-17  feat(mcp): activate_cap/deactivate_cap tools
2026-04-18  feat(storage): cap_store — separate capabilities.db + CRUD
2026-04-18  feat(daemon): handleCapStore handler + sandboxing + quotas
2026-04-18  feat(mcp): cap_store MCP tool
2026-04-18  feat(proxy): injectCapabilitiesTurn + CapsCache
2026-04-20  feat(proxy): renderCapabilitiesCatalog + Bootstrapper
2026-04-20  feat(proxy): capabilities lazy-activation catalog + API-actual threshold
2026-04-21  fix(daemon): auto_active default true for save_cap
2026-04-22  feat(capfile): remove Notes from struct/parser/writer
2026-04-22  feat(capfile): DetectRequires scans script for cap_store/blob_put/blob_get
2026-04-22  feat(capfile): adapter registry with bidirectional name mapping
2026-04-22  feat(capfile): writer converts provider-specific to generic names
2026-04-22  feat(daemon): adapter mapping in activate_cap and save_cap handlers
2026-04-22  fix(caps): use already-constructed meta for WriteCapToDisk (6ed3fe5)
2026-04-22  feat(proxy): multi-turn workflow sequence detection (81eb6b6)
2026-04-22  fix(proxy): parse nested pattern envelope in suggestion response (6f7b9da)
```

---

## 12. Implementation Status

### Done

- [x] `cap` as valid learning category (v0.55 migration from `capability`)
- [x] `CapMeta` struct with `ParseCapMeta`/`ToJSON`/`HasTag` (`cap_meta.go`)
- [x] Daemon handlers: `handleGetCaps`, `handleSaveCap`, `handleRegisterCaps`, `handleActivateCap`, `handleDeactivateCap`, `handleGetActiveCaps`
- [x] Cap Store: separate `capabilities.db`, CRUD, sandboxing, quotas, paging
- [x] MCP tool registration: `get_caps`, `save_cap`, `register_caps`, `activate_cap`, `deactivate_cap`, `cap_store`, `get_active_caps`
- [x] `session_active_caps` table + storage methods (`ActivateCap`, `DeactivateCap`, `GetSessionCaps`, `TouchCap`)
- [x] Cap Store storage: `CapsCreateTable`, `CapsUpsert`, `CapsQuery`, `CapsQueryPaged`, `CapsDelete`, `CapsListTables`, `OpenCapsDB`, `CloseCapsDB`, `CapsReady`, `ValidateCapName`
- [x] Proxy: `injectCapabilitiesTurn()` + `injectCapabilitiesTurnImpl()` — schema injection for active caps
- [x] Proxy: `CapsCache` — in-memory cache with `Get`/`Set`/`Invalidate` + `cachedQueryFn`
- [x] Proxy: `renderCapabilitiesCatalog()` — catalog in system-reminder with bootstrapper
- [x] Proxy: `invalidateThreadCaches()` — coordinated cache invalidation
- [x] Briefing: `renderCaps()` — caps in session briefing
- [x] Existing capabilities: reddit_fetch, reddit_search, cap_search, cap_collect, cap_save_analysis, reddit_research, cap_delete, proxy_health
- [x] REPL Pattern Detection: `NormalizeCommand`, `isTrivialShape`, `detectReplPattern`, `formatPatternSuggestion`
- [x] Storage: `RecordReplPattern`, `GetReplPatterns`, `DismissReplPattern`, `IsPatternDismissed`
- [x] Daemon handlers: `handleRecordReplPattern`, `handleGetReplPatterns`, `handleDismissReplPattern`
- [x] MCP tools: `record_repl_pattern`, `get_repl_patterns`, `dismiss_repl_pattern`
- [x] Fixation Detector: `DetectFixation` with 3 signals (consecutive errors, edit-build cycles, file retries)
- [x] Trivial shape filter for REPL Pattern Detection (last commit `4708c8d`)
- [x] REPL Pattern Noise Reduction: deny-list +15, budget max 3/thread, threshold 5→8
- [x] REPL Pattern Suggestion envelope format: `{"pattern": {...}, "workflow": {...}}`
- [x] Multi-Turn Workflow Sequence Detection: `thread_sequences` table, turn hash computation, subsequence matching (commit `81eb6b6`)
- [x] `auto_active` default changed to `true` (`handler_caps.go`)
- [x] Blob pipe (`internal/capblob/`): CLI `cap-blob-put`, cap_store-based chunk store, used by `reddit_fetch`
- [x] CAP.md: Notes section removed from parser, writer, struct
- [x] CAP.md: `DetectRequires()` — scans script for `store(`/`web(`/`file(` and populates `Requires []string`
- [x] Adapter registry: `DefaultAdapters()`, `ProviderToGeneric()`, `GenericToProvider()`, `GenerateAdapterJS()` (`adapter.go`)
- [x] Writer applies `ProviderToGeneric` before render — CAP.md files store generic names
- [x] Daemon: `save_cap` normalizes handler_repl via `ProviderToGeneric`, `activate_cap`/`register_caps` expand via `GenericToProvider`
- [x] Existing CAP.md files migrated to generic names
- [x] WriteCapToDisk bug fixed: meta object passed directly instead of re-parsing content string (commit `6ed3fe5`)
- [x] ExportAllCaps + SyncCapsFromDisk on daemon start (`daemon.go:193-194`)
- [x] yesmem-build-tool skill: dependency-caps docs, haiku() note, composite example, API rename, bundled template synced

### Open

- [ ] **Split-Brain Session ID** (#53443) — proxy thread_id vs. daemon session_id diverge via pidMap. Blocks correct end-to-end flow for subagents. **Most critical open issue.**
- [ ] **Subagent Injection** (#53524) — subagents receive no `<capabilities-active>` blocks despite `auto_active=true` in CapMeta. Likely tied to session ID mapping.
- [ ] **Phase 2 Idempotency** — activate_cap over-matching: detection of "already injected" is too broad.
- [ ] **Eviction/TTL** — caps remain active until session end. Add in v2 if friction becomes visible.
- [ ] **SSE Weights LFS** (#53273) — embedding tests failing due to ASCII instead of binary (pre-existing, not capabilities-specific).
- [x] **cap_store→store rename migration** — all caps already use generic `store()`. `GenericToProvider` correctly converts to `mcp__yesmem__cap_store()` on activation. Verified 2026-04-22.
- [x] **Self-improving cap cycle** — via proxy instruction in `caps_inject.go:71`: "When a capability handler errors: diagnose the root cause, fix the handler, save the corrected version via save_cap (auto-supersedes), then retry." No daemon process required.
- [x] **cap export/import CLI** — export runs at daemon start via `ExportAllCaps`. Import implicit: caps in `~/.claude/caps/` directory are loaded at start via `SyncCapsFromDisk`. No CLI subcommand needed.
- [ ] **Workflow Suggestion Injection** — workflow suggestions are returned in the envelope, but the proxy does not yet format and inject them as system-reminder. Only data collection is active.
- [ ] **Worktree → main merge** — the entire feat+capability-memory branch is not merged to main. All features run only via the deployed binary from the worktree.

---

## 13. Design Decisions

| Decision | Rationale |
|----------|-----------|
| Capabilities as learnings (category="cap"), no separate table | Reuses existing supersede/search/embed infrastructure. No schema migration overhead. |
| Deliberative activation instead of auto-injection | Model selects more reliably than an embedding ranker. No false positives. Lower token cost. |
| Native tools win on name collision | Safety: native MCP tools must never be shadowed by capabilities. |
| Separate capabilities.db for cap store | Isolation from yesmem.db. No schema pollution. Independent locking. |
| Blob pipe instead of temp files | No Read tool permission prompt. No /tmp cleanup. More persistent than temp files. |
| Cap store sandboxing (no DROP/ALTER) | Capabilities should write data, not destroy structure. Defensive architecture. |
| Bootstrapper as registerTool() in catalog | Enables `activate_cap` as REPL tool without a prior MCP round-trip. Self-bootstrapping. |
| v0.55 rename capability→cap | Consistency: shorter, uniform name prefixes. All APIs use `cap_*`. |
| auto_active default true | New caps should be immediately available by default. Opt-out rather than opt-in — reduces friction in the cap-building flow. |

---

## 14. CAP.md — File-Based Cap Definitions

### Concept

Each capability has a `CAP.md` file as a human-readable, editable source of truth. The file describes what the cap does, how it does it (script), and what data it stores (database).

SQLite remains the runtime store — files are synced into the DB at daemon start.

### File Format

```markdown
---
name: reddit_search
description: "Search Reddit by topic"
version: 2
tags: [web, reddit]
runtime: repl
scope: user
tested: true
auto_active: true
---

## Purpose
Prose: what the cap does.

## Script
```javascript
async function handler({ query, limit = 10 }) { ... }
```

## Database
```sql
CREATE TABLE IF NOT EXISTS listings (
  id INTEGER PRIMARY KEY,
  title TEXT NOT NULL
);
```

**Frontmatter:** `name` and `description` are required fields. `runtime` is derived from the script language (`javascript` → repl, `bash` → bash). `schema` is derived from the function signature and not stored in the frontmatter.

**Sections:** Order is fixed: Purpose → Script → Database. Database is optional (section may be empty).

**SQL validation:** Only `CREATE TABLE/INDEX/VIEW/TRIGGER IF NOT EXISTS` allowed. `DROP`, `ALTER`, `INSERT`, `UPDATE`, `DELETE` are rejected.

### Directory Structure

```
~/.claude/caps/
├── deploy/
│   └── CAP.md
├── reddit_search/
│   └── CAP.md
├── reddit_fetch/
│   └── CAP.md
└── cap_collect/
    └── CAP.md
```

User scope: `~/.claude/caps/<name>/CAP.md`
Project scope: `<project>/.claude/caps/<name>/CAP.md` (planned)

### Daemon Integration

Two phases run on every daemon start:

1. **File → DB** (`SyncCapsFromDisk`): All CAP.md files are read, parsed, and upserted into the DB via `save_cap`.
2. **DB → File** (`ExportAllCaps`): All DB caps without a CAP.md file are exported as files. DDL is read from `caps.db` via `sqlite_master`.

On every `save_cap` MCP call: after DB upsert, the CAP.md is automatically written/updated.

### Dev Workflow

1. Create or edit `~/.claude/caps/my_cap/CAP.md`
2. Restart daemon (`make deploy` or `systemctl restart yesmem-daemon`)
3. Change is immediately in the DB and available via MCP tools

### Package Structure

| File | Function |
|------|----------|
| `internal/capfile/parse.go` | Parser: YAML frontmatter + 3 sections (Purpose, Script, Database), schema derivation from JS signature |
| `internal/capfile/write.go` | Writer: canonical CAP.md, SQL formatting, JS formatter, atomic write. Applies `ProviderToGeneric` to script |
| `internal/capfile/scanner.go` | Scanner: directory discovery, `ScanAll()` over user + project dirs |
| `internal/capfile/adapter.go` | Adapter registry: `DefaultAdapters()`, `ProviderToGeneric()`, `GenericToProvider()`, `GenerateAdapterJS()` |
| `internal/daemon/cap_sync.go` | Integration: `CapFileToParams()`, `CapMetaToCapFile()`, `WriteCapToDisk()`, `SyncCapsFromDisk()`, `ExportAllCaps()` |
| `internal/storage/cap_store.go` | `GetCapTableDDL()`: DDL from `sqlite_master` for Database section |
| `docs/CAPS-md-spec.md` | Format specification |

### Adapter Layer (Provider Abstraction)

CAP.md files and the DB store **generic** function names for portability. On activation, these are translated into **provider-specific** implementations.

There are **3 adapter primitives**, each action-based:

**Direct Mapping** (`AdapterConfig.Direct`):

| Generic | Provider (YesMem MCP) | Actions |
|---------|-----------------------|---------|
| `store()` | `mcp__yesmem__cap_store()` | `create_table`, `upsert`, `query`, `delete`, `list_tables`, `blob_put`, `blob_get` |

**Dispatcher Mapping** (`AdapterConfig.Dispatchers`):

| Generic | Action | Provider implementation |
|---------|--------|------------------------|
| `web()` | `fetch` | `sh('curl ...')` |
| `web()` | `search` | `WebSearch()` |
| `file()` | `read` | `cat()` |
| `file()` | `write` | `put()` |
| `file()` | `glob` | `gl()` |

**Roundtrip:**

1. **save_cap** (user/daemon → DB): `ProviderToGeneric()` normalizes handler_repl (direct mappings only: `mcp__yesmem__cap_store(` → `store(`)
2. **Writer** (DB → CAP.md): `ProviderToGeneric()` normalizes script before render
3. **activate_cap / register_caps** (DB → Claude): `GenericToProvider()` expands direct mappings back
4. **GenerateAdapterJS()**: generates direct shims (`globalThis.store = async(a) => mcp__yesmem__cap_store(a)`) + dispatcher shims (`globalThis.web = async({action,...p}) => { const d = {fetch: ..., search: ...}; return d[action](p); }`)

**Design principles:**
- `store()` is 1:1 (string replace, as before)
- `web()` and `file()` are dispatchers — the cap writes `web({action:'fetch', url:'...'})`, the JS shim dispatches at runtime
- Runtime builtins (`sh()`, `haiku()`, `log`, `JSON`) are NOT adapters — they are always available
- Whoever wants CC-specific tools directly (`WebFetch`, `Read`) can write them in — just not portable

**Why:** A cap that uses `store()` works unchanged if the MCP server is renamed. Only `DefaultAdapters()` needs to be updated. `web()`/`file()` dispatchers can point to other backends (e.g. `playwright` instead of `curl`) without changing the cap script.

### Gotchas

- **Quote YAML descriptions**: Descriptions containing `:`, backticks, or special characters must be in `%q` quotes, otherwise YAML parse error.
- **Schema not in frontmatter**: Derived from the JS function signature. Explicit schema only when signature derivation is insufficient.
- **SQL validation has its own allowlist**: `storage.blockedSQLPattern` blocks EVERYTHING including CREATE — the Database section has its own validation (`dangerousSQLPattern` + `safeSQLPattern` in `capfile/parse.go`).
- **formatJS for single-liners only**: Multi-line scripts are not reformatted — the naive formatter destroys destructuring parameters.
- **Startup order**: `SyncCapsFromDisk` (file→DB) first, THEN `ExportAllCaps` (DB→file). Reversed order would overwrite hand-edited files.

---

## 15. Source Documents

| Document | Path |
|----------|------|
| Original spec | `docs/superpowers/specs/2026-04-15-capability-memory-design.md` |
| Phase 1+2 plan | `yesdocs/superpowers/plans/2026-04-15-capability-memory-phase-2.md` |
| Lazy-activation plan | `yesdocs/plans/2026-04-17-capability-lazy-activation.md` |
| Blob-pipe plan | `yesdocs/plans/2026-04-18-cap-blob-pipe.md` |
| Phase 3 cap store plan | `.claude/plans/phase3-cap-store.md` |
| Cap Store Analysis | `docs/cap-store-analysis.md` |
| Cap Store Examples | `docs/cap-store-analysis-examples.md` |

---

## 16. Daemon Scheduler

Cron-based task scheduler built into the daemon. Defines recurring or one-shot jobs that automatically spawn agents.

### Two Execution Modes

| Mode | Mechanism | Visible? | Overhead | Use case |
|------|-----------|----------|----------|----------|
| `agent` | PTY bridge + tmux window | Yes | Full briefing + agent lifecycle | Complex tasks, debugging, coding plans |
| `headless` | `claude -p` as subprocess | No | Minimal — no lifecycle management | Routine automation, cron jobs, data collection |

### MCP Interface

Single `schedule` tool with four actions:

| Action | Parameters | Description |
|--------|-----------|-------------|
| `create` | name, cron, prompt, mode, enabled, recurring | Create job |
| `list` | — | List all jobs |
| `delete` | id | Delete job |
| `run` | id, mode, prompt | Manual trigger |

### Task Delivery

The scheduler writes the task prompt to a job-specific scratchpad section **before** spawning the agent. The agent reads its task from the briefing — no relay timing issues.

```
Section: sched-<job-name>
Content: ## SCHEDULED TASK [<job-name>]
         Job-ID: <id>
         <prompt with focus instructions>
```

### Agent Lifecycle (mode `agent`)

- **Pre-spawn cleanup** — stops existing agent on the same section
- **Idle timeout** — 10 minutes, unified across all agent states (running, frozen, idle)
- **Watchdog goroutine** — polls agent status every 30 seconds

### Headless Mode

Uses `claude -p` (Claude Code non-interactive mode) as a daemon subprocess:
- Full MCP tool access (caps, cap_store, haiku, scratchpad)
- Runs through the proxy (subscription-based, no API key needed)
- Output captured and written to scratchpad
- No tmux window, no PTY bridge, no watchdog needed
- ~2x faster than agent mode with comparable results

### Caps as Automation Primitives

Caps are ideal for scheduled tasks because they are predictable: defined schema, known handler, deterministic behavior. The agent activates the cap and executes it — no improvisation needed.

### Comparison with Anthropic Scheduled Tasks

| | Anthropic Cloud Routines | Desktop Scheduled Tasks | YesMem Scheduler |
|---|---|---|---|
| Runs on | Anthropic cloud | Local (app open) | Local (daemon) |
| Memory | None (fresh each run) | None | Full persistent memory |
| Local files | No (fresh clone) | Yes | Yes |
| MCP servers | Connectors only | Local | Full local MCP |
| Caps/Tools | N/A | N/A | Reusable caps + cap_store |
| Cost | API tokens + $0.08/h | Subscription | Subscription |
| Limits | Pro: 5/day, Max: 15/day | Desktop-bound | Unlimited (self-hosted) |

### Components

| File | Symbols |
|------|---------|
| `internal/daemon/scheduler.go` | `ScheduledJob`, `Scheduler`, `JobExecutor`, `AddJob`, `Tick` |
| `internal/daemon/handler_scheduler.go` | `handleSchedule`, `scheduleCreate/List/Delete/Run`, `executeScheduledPrompt`, `executeAgent`, `executeHeadless`, `watchScheduledAgent` |
| `internal/storage/scheduler.go` | `ScheduledJobRow`, `SaveScheduledJob`, `ListScheduledJobs`, `DeleteScheduledJob`, `UpdateJobLastRun` |
