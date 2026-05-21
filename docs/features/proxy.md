# Proxy Engine

## 1. Infinite Thread (Proxy)

The proxy sits between Claude Code and the Anthropic API, enabling unlimited conversation length.

### Connection Flow

```
Claude Code → ANTHROPIC_BASE_URL=http://localhost:9099 → yesmem proxy → api.anthropic.com
```

Thread identification uses the `X-Claude-Code-Session-Id` header sent by Claude Code. Each unique session ID gets isolated proxy state (stubs, collapse cutoff, sawtooth cache, keepalive timers).

Proxy state is additionally segregated per **client type** (`claude` vs `codex`, since 4f8aaa0): the stub-cycle pipeline, REPL-pattern fork-detection, and system-prompt rewrites run on separate code paths so a Codex session cannot leak Claude-specific injections (or vice versa). REPL-pattern detection itself is fork-driven (8301690) — pattern matches are surfaced as cap-suggestion injections via the Sawtooth-tail (4c353f1) rather than scanning every assistant message inline.

### Stub Cycle Pipeline

When estimated tokens exceed the threshold, the proxy runs:

```
StripReminders → stripSkillHints → CompressContext → CalcCollapseCutoff → CollapseOldMessages
→ ReplaceSystemBlock (Narrative) → StripOldNarratives → reexpandStubs
→ UpgradeCacheTTL → EnforceCacheBreakpointLimit
```

The proxy also rewrites the system prompt early in the pipeline (see §12e):
```
StripCLAUDEMDDisclaimer → StripOutputEfficiency → StripToneBrevity
→ InjectAntDirectives → InjectCLAUDEMDAuthority → InjectPersonaTone
```

**InjectAntDirectives** inserts behavioral directives into an `[ant]` block in the system prompt. Currently carries the `code-tools-first` directive: instructs Claude to use MCP code tools (`get_file_index`, `search_code_index`, `get_file_symbols`, `get_code_snippet`, `get_code_context`) for code navigation before spawning agents or using raw grep/find. The directive is injected as plain text inside the system block — not as a tool hint or user message.

### Context Compression (CompressContext)

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

### Eager Tool-Result Stubbing

Tool results are stubified aggressively on every request — not only when the sawtooth threshold is reached. This keeps token counts low proactively rather than waiting for a full stub cycle to trigger.

Combined with **selective cache breakpoint shifting**: for text-only assistant turns (no tool calls), the proxy shifts the cache breakpoint position forward to keep the most recently cached content warm. Tool-use turns keep the standard breakpoint position. This preserves cache hit rate when eager stubbing compresses content that would otherwise anchor the breakpoint.

History: this feature was reverted once due to cache-invalidation side effects, then re-enabled using the cache-safe selective-shift approach.

### Collapse (Budget-Based)

When token estimates exceed the threshold:
1. `CalcCollapseCutoff()` walks backwards from end, accumulating tokens until `tokenFloor` is reached
2. Everything before the cutoff is collapsed into a single archive block
3. **Orphan safety:** If cutoff lands on `tool_result`, shifts forward to avoid orphaned pairs
4. Archive block contains: message count, `get_compacted_stubs()` hint, tool usage summary, files touched, and structured sections:
   - **Sessions** — extracted session flavors (summaries) grouped by date with timestamps
   - **Recaps** — CC `/recap` events integrated chronologically into the session timeline, prefixed with `[recap]`. These are Claude Code's own session summaries generated when the user returns after idle, captured as pulse learnings from JSONL events.
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
- **`cache_keepalive_mode`:** Config option (`auto`, `ephemeral`, `1h`). Default `auto` uses detection. `ephemeral_1h` is the detected state when 1-hour caching is confirmed. Note: Go code default is `"5m"` (config.go:444); the setup template writes `"auto"` (setup.go:838).
- **Impact on keepalive:** Dynamic interval adjusts based on detected TTL — 4.5min pings for ephemeral (effectiveInterval = 300*0.9 = 270s), 54min for 1h. Default `cache_keepalive_pings_5m: 6` (optimized from 12 — diminishing returns above 6 pings per cycle). Note: Go code default is `5` (config.go:445); the setup template writes `6`.

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
    opus: 500000                      # 1M context models get higher threshold (code defaults: opus=180k, sonnet=180k, haiku=130k — config.go:452-455)
    sonnet: 250000
    haiku: 150000
    gpt-5.2: 250000
  keep_recent: 10
  sawtooth_enabled: true
  cache_ttl: "ephemeral"              # default: 5min TTL (cheaper than 1h; auto-detected overrides this)
  usage_deflation_factor: 0.7
  skill_eval_inject: "silent"          # inject skill evaluation logic into responses (string field; default "silent"; set "" to suppress)
  effort_floor: ""                     # minimum effort value for API requests (string field; proxy raises effort if request is below floor)
```

Runtime override per model via MCP: `set_config(key="token_threshold", value="opus=500000")`.

### OpenAI Parity Pipeline (Codex & OpenCode Support)

The proxy handles both Anthropic Messages API requests (Claude Code) and OpenAI Responses API requests (Codex CLI, opencode) through a single binary. For non-Anthropic requests, a parallel pipeline translates between API formats and applies the same compression, injection, and caching logic.

**Request flow:**
```
Codex/Opencode → POST /v1/responses → yesmem proxy
  → translate to Anthropic Messages API format
    → run same compression pipeline (stubbing, collapse, sawtooth)
    → inject briefing, associative context, directives (profile-aware)
    → translate back to OpenAI Responses API format
  → forward to api.deepseek.com (or configured provider)
    → translate response back to Responses API format
  → Codex/Opencode
```

**Translation layer** (`internal/proxy/openai_reverse.go`):
- Incoming Responses API `input[]` blocks → Anthropic `messages[]` (user/assistant/tool roles)
- Outgoing Anthropic streaming SSE events → Responses API `output[]` blocks
- `cache_control` breakpoints are translated through both directions — preserving DeepSeek prompt cache across the round-trip
- ThreadID detection: uses stable `session_id` from request metadata, not content hash. Falls back to SHA256 of user_id when metadata is absent. Falls back to `DeriveThreadID(req)` (a content hash of session ID + working directory + first user text) when no stable `session_id` is available in request metadata.

**Injection pipeline** (`internal/proxy/openai_parity.go`):

The parity pipeline runs the same injection sequence as the main proxy pipeline, but with profile-aware gating (§19):
- `InjectAntDirectives` → discipline blocks (verification, collaboration)
- `InjectOutputDiscipline` — uses `InjectDeepSeekOutputDiscipline` for DeepSeek models (relaxed brevity, retains structural guidance)
- `InjectCodingDiscipline` / `InjectBeweislast` / `InjectScopeDiscipline`
- `InjectClarifyFirst` / `InjectCodeToolsFirst` / `InjectWikiFirst`
- `InjectTimestamps` → wall-clock timestamps and message sequence numbers (same as §12c)
- Claude-specific injectors (`InjectClaudeToolPrefs`, `InjectDelegationContract`) are **excluded** from the parity path via profile gating
- `injectOpencodeCapabilitiesCatalog` → registers active caps as available tools (§25)

**Cache management for DeepSeek:**
- **Stable injection position:** All prompt injections are placed early and consistently — variable blocks at `system[0]`, deterministic blocks appended to the last user message. This prevents cache fragmentation from injection position drift.
- **`cache_control` passthrough:** Anthropic-format `cache_control` breakpoints are translated into the OpenAI format and back — DeepSeek's prompt cache receives the same prefix structure.
- **TTL normalization:** On session resume, all breakpoint TTLs are normalized to a consistent value to prevent cache-ordering constraint violations.
- **Subagent isolation:** opencode subagents get their own cache namespace (threadID suffix) to prevent cache collisions with the parent session.
- **Fork effort=high normalization:** Forked extraction calls force `effort="high"` because DeepSeek returns HTTP 400 on `effort="xhigh"`.

**Session identity:**
- `source_agent` is detected from working directory patterns and request metadata
- `YESMEM_SOURCE_AGENT=opencode` is injected by the plugin into shell environments (§10b)
- Session IDs are prefixed by source: `opencode:...` for opencode, `codex:...` for Codex, bare UUID for Claude

---


## 2. LLM Backend Flexibility

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

### Recent Setup Improvements

- **Non-interactive install**: `yesmem setup` (without `-i`) runs a streamlined default path that auto-detects authentication type (CLI vs API key)
- **API key detection**: The setup wizard now checks `.claude.json` for `primaryApiKey` as a fallback when `$ANTHROPIC_API_KEY` is not set
- **CBM binary auto-download**: During setup, the CBM CLI binary (code scanner) is automatically downloaded from GitHub releases if not present
- **Config migration**: `yesmem migrate` now applies config template updates and adds new proxy fields (like `effort_floor`, `skill_eval_inject`) to existing configs. Also deploys bundled skills and caps (previously required manual `yesmem install` after updates).

### Setup Auth Conflict Resolution

When the user chooses `provider: api`, the setup wizard handles Claude Code's triple auth system:
- **Problem:** Claude Code recognizes three auth methods — `oauthAccount` (subscription login), `primaryApiKey` (Console key in `~/.claude.json`), and `ANTHROPIC_API_KEY` (env). Multiple active methods cause an "Auth conflict" warning.
- **Solution:** `clearClaudeJSONAuth()` removes both `primaryApiKey` and `oauthAccount` from `~/.claude.json`. Only `ANTHROPIC_API_KEY` in `settings.json` env remains as the sole auth method.
- **Key detection:** Setup checks 3 sources for existing keys: `$ANTHROPIC_API_KEY` env → `settings.json` env block → `~/.claude.json` primaryApiKey. If found, offers "Keep this key" instead of requiring re-entry.
- **Pre-install state:** `install-state.json` saves `oauthAccount`, `primaryApiKey`, `envAPIKey`, and `autoCompactEnabled` before any changes.
- **Uninstall restore:** `restorePrimaryApiKeyFromState()` restores both `oauthAccount` and `primaryApiKey` to `~/.claude.json`, returning the user to their pre-YesMem auth state.
- **Cache-TTL hint:** Wizard explains why a platform.claude.com key is needed (1-hour prompt caching vs 5-minute ephemeral).

### Multi-Agent Prompt Isolation (PromptProfile)

YesMem supports multiple LLM agent frontends — Claude Code, opencode, and Codex CLI — each with different system prompt requirements, tool preferences, and behavioral constraints. The `PromptProfile` system ensures each agent type receives only the prompt directives that apply to it, without cross-contamination.

**PromptProfile Type** (`internal/models/prompt_profile.go`):

Three profiles are defined:
| Profile | Agent Frontend | Detection |
|---------|---------------|-----------|
| `claude` | Claude Code | Default; `source_agent=claude` |
| `opencode` | opencode | Detected from path patterns (`opencode` in working directory) |
| `codex` | OpenAI Codex CLI | Detected from path patterns; receives OpenAI parity pipeline |

**PromptFlags** (`internal/config/config.go`):

Each prompt directive is modeled as a boolean flag in a shared `PromptFlags` struct:

```yaml
# config.yaml
shared_prompt:           # Agent-neutral defaults (applied to all profiles)
  prompt_output_discipline: true
  prompt_coding_discipline: true
  prompt_beweislast: true
  prompt_scope_discipline: true

claude_prompt:           # Claude-specific overrides
  prompt_tool_prefs: true      # REPL tool guidance
  prompt_code_tools_first: true
  prompt_wiki_first: true

codex_prompt:            # Codex-specific
  prompt_code_tools_first: true
  prompt_wiki_first: true

model_features:          # Per-model injection gating
  claude-opus-4-6: { prompt_ungate: true, ... }
  claude-haiku-4-5: { prompt_ungate: false, prompt_coding_discipline: false, ... }
```

**EffectivePromptFlags(profile) — Three-Layer Merge:**

The proxy resolves the effective prompt flags for each request by merging three layers (last wins):

1. **Hard defaults** — agent-neutral flags enabled by default in `Default()`
2. **`shared_prompt`** — base layer for all profiles
3. **Profile-specific** — `claude_prompt`, `codex_prompt`, or `opencode_prompt` in config.yaml
4. **Legacy flat fields** (backward compatibility) — deprecated `proxy.prompt_code_tools_first` etc. mapped via `claudeLegacyFlags()`

**Profile-aware injector gating** (`internal/proxy/`):

The proxy calls `getPromptFlags(profile)` to resolve flags per request. In the main Anthropic pipeline (`handleMessages`), flags are resolved for `ProfileClaude`. In the OpenAI parity pipeline (`runOpenAIParityPipeline`), flags are resolved for `ProfilesCodex` or `ProfileOpencode`. This ensures:
- Claude-specific directives (REPL tool preferences, Opus/Sonnet/Haiku guidance) never leak into opencode/Codex prompts
- Codex/openCode sessions receive only agent-neutral injectors
- Each agent type can independently toggle prompt rewrites, discipline blocks, and tool guidance

**Feature defaults** (`feature_defaults`):

New models automatically inherit the full feature set (`all-true`). Only models that need reduced prompting (e.g. Haiku for forked extraction) are explicitly downgraded in `model_features`. This prevents the config from growing with every new model release.

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



---

## 3. Provider Auto-Discovery (since 2026-05-21)

The proxy reads three config sources from the opencode installation at startup and auto-configures routing:

- **models.json** (`~/.cache/opencode/models.json`) — Provider definitions with npm package names, API endpoints, and model lists
- **opencode.json** (`~/.config/opencode/opencode.json`) — User's configured providers with API keys and options
- **auth.json** (`~/.local/share/opencode/auth.json`) — API keys stored by opencode (separate from opencode.json)

**What it does:**
1. Discovers active OpenAI-compatible providers (npm check: `@ai-sdk/openai-compatible`, `@ai-sdk/openai`, `@ai-sdk/mistral`)
2. Resolves credentials via three-tier lookup: opencode.json per-provider `env` vars → models.json env hints → auth.json
3. Verifies discovered credentials against provider endpoints
4. Builds `autoProviderTargets` map for `resolveOpenAITarget()` request routing
5. Patches `opencode.json` with `baseURL: http://localhost:9099/v1` for discovered providers (idempotent — skips if already set)
6. First-party defaults for providers without API endpoints in models.json (openai, anthropic, google, groq, mistral)

**Result:** 84 models across 3 providers (deepseek, openai, mistral) auto-discovered and routed on first proxy start (runtime metric from models.json — not a code-enforced invariant). Toggle with `auto_configure_providers: false`.

### Prompt Cache Architecture

YesMem exploits Anthropic's byte-prefix prompt cache (5-minute TTL) through several mechanisms:

- **Cache breakpoints:** `cache_control: {type: ephemeral}` set on message boundaries (max 4 per Anthropic budget). Breakpoint positions are carefully chosen — cache stability vs granularity.
- **TTL upgrades:** `UpgradeCacheTTL()` overwrites ALL cache_control blocks to the configured TTL (e.g. `"1h"`). This is a brute-force normalization, not a selective extension. The 1h lifetime only works with console API keys — OAuth/login keys are silently capped at 5 minutes by Anthropic. Requires `cache_ttl: "1h"` in config.
- **Boundary hash sentinel:** The proxy normalizes `cch=` hashes (from Claude Code's billing header) to a fixed value to PREVENT thread ID instability and cache invalidation (normalize_billing.go:21-48, sanitize_sentinel.go:9-18).
- **Keepalive background pings:** Periodic cache-warming requests prevent cache expiration during idle periods.

**Stub/Sawtooth interop:** Compaction stubs (frozen context regions) get their own breakpoints. The boundary between stub and new messages is a natural cache frontier — the stub is cache-stable, new messages are cache-dynamic.

### Fork-Cache Optimization

Background learning extraction (Forked Agents) re-uses the main conversation's byte prefix:

- **Byte-identical prefix construction:** Fork request bodies are built via `bytes.Replace` instead of `json.Marshal`, preventing JSON key reordering that destroys prefix identity
- **30s delay:** DeepSeek needs time to persist disk-based KV cache — fork fires 30s after the main response
- **Cache-proven gate:** Fork only fires after 2 confirmed cache hits after deploy (prevents cold-cache storms)
- **Result:** Fork cache hit rate 91-98% (up from 0-39%) (measured metric, not code-guaranteed)

## 4. Runtime Safety

### Loop Detection

Runtime loop detection runs inside the proxy, no LLM call needed:

- **Identical cycle detection:** Message hashes tracked over 2-4 turn windows; 2+ repetitions triggers warning
- **Edit-test-error pattern:** Same file edited, tested, errored 3+ times → stuck development loop
- **Repeated error:** Same error 3+ times without progress
- **Escalation:** 3 warning levels with cooldown (3 requests after each warning):
  - Level 1: "Step back, same approach failed N times"
  - Level 2: "WARNING: Loop continues — ask user for guidance"
  - Level 3+: "ALERT: persistent loop — stop and ask user"
- **Injection:** `[yesmem-loop-warning]` system-reminder block in next user message
