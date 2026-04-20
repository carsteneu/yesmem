# Changelog

All notable changes to YesMem are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- Install wizard picks CLI vs API key, default model sonnet
- Follow parent process CWD for worktree routing
- Fault-tolerant CBM indexing for worktrees
- Worktree-aware CodeGraph cache
- Worktree-aware filesystem fallback for code tools
- ExtractSymbol for all Go symbol types + get_file_symbols tool
- CBM binary auto-download + managed CLI location
- CBM index mtime invalidation + Module fallback + get_code_snippet
- Add get_code_snippet tool — full function body from source
- Persistent SQLite cache for project scan results
- Add Active Zones — recently changed packages from git log
- CBM graph enrichment — entry points, test coverage, change coupling, key files, imports
- Code Map injection as separate user/assistant turn
- Proxy user/assistant turn injection for briefing
- Complete briefing in daemon RPC (add RefineBriefing + Open Work)
- Split Code Map into separate codemap-hook
- Auto-index projects in codebase-memory-mcp via CLI
- Phase C Karpathy Compilation — LLM-generated package descriptions + cross-package links
- Learning annotations in code map + get_file_index MCP tool (Phase B completion)
- Integration test — TreeSitterScanner + CodeGraph end-to-end on own repo
- Code Intelligence MCP handlers — search_code_index, search_code, get_code_context, get_dependency_map, graph_traverse with lazy CodeGraph init
- In-memory CodeGraph with traversal, search, and cycle detection
- TreeSitterScanner with AST-based signatures + import extraction for 15 languages
- Knowledge Index Phase B — code scanner with adaptive tier rendering
- Knowledge Index Phase A — Doc Index, Health, Recent Context in briefing
- Auto-generated changelog with sync integration
- Non-interactive default install

### Changed

- Whitelist mode for docs/ in sync-public.sh
- Harden public sync pipeline
- Move Knowledge Index sections (Doc Index, Health, Recent Context) into Code Map turn
- Expand Code Map — all packages get individual rows
- Extract GenerateFullBriefing as single source of truth
- Rewrite Code Map render to Spec Ebene 1 table format
- Replace TreeSitter scanner with codebase-memory-mcp CLI
- Wire TreeSitterScanner into briefing, render imports in code map, expose CodeGraph
- Add gotreesitter v0.13.4 (pure Go tree-sitter, 206 grammars, no CGO)
- Consistent session_flavor JSON key, remove redundant DISTINCT
- Remove pulse content truncation from timeline

### Fixed

- Keepalive ping strips thinking — adaptive conflicts with max_tokens=1
- Add error logging to silent-fail load functions in briefing
- Normalize thinking.type=enabled to adaptive for opus-4-6+/sonnet-4-6
- Merge Module nodes into File query for complete scan
- Packed-refs fallback + unique project key for scan cache
- Code Map injection debugging + dedup marker fix
- Code review — lazy-init briefingText + pass projectDir
- Consistent ## Code Map headers across all tiers
- Suppress empty Code Map for projects with no recognized packages
- Inject Code Map post-refine so it survives LLM compression
- Increase queryDaemon timeout to 30s for generate_briefing
- Pass full CWD path to briefing for Code Map scanner
- Harden TreeSitter scanner against OOM + panics
- Add gopath, .worktrees, testdata to scanner skip list (OOM crash)
- Move Code Descriptions to Phase 3.75 (before heavy Narratives/Clustering)
- Phase 4.75 rate-limit to 1 project per extraction cycle
- Code Intelligence review fixes — real grep, glob matching, memory cleanup ordering
- Skill-eval block scope to user text input only, skip tool_result turns
- Preserve session flavors across extraction runs, fetch all phases for current session

### Reverted

- Remove Go-specific filesystem fallbacks from code tools
- Revert "feat(codescan): worktree-aware filesystem fallback for code tools"

### Documentation

- Add Capability Memory spec and Phase 2 implementation plan
- Add pulse/recap feature to Features.md and README.md

### Testing

- Raise tool definition budget to 21000 chars, count to 60

## [1.1.34] - 2026-04-15

### Added

- Integrate pulse learnings into collapse session timeline
- Capture CC away_summary as pulse learnings

### Fixed

- Truncate pulse content to 150 chars in session timeline
- Set created_at on pulse learnings from JSONL event timestamp
- Strip context_management from fork requests

## [1.1.33] - 2026-04-15

### Added

- Add --per-commit mode to sync-public.sh
- Re-enable eager tool-result stubbing (cache-safe with breakpoint shift)
- Persistent timestamp + msg:N injection on all messages
- Selective cache breakpoint shift for text-only turns

### Fixed

- Remove truncation from archive block learnings and session flavors
- Use session start from DB for collapse learning query + propagate threshold to sawtooth
- Append no-echo instruction to rotating timestamp hints
- Restore TotalPings + cache countdown display lost in overbroad revert
- Restore threadID in usage log lines lost in overbroad revert
- Restore hookEventName, .gitignore and sync excludes lost in overbroad revert
- Keepalive interval display uses exact minutes+seconds
- Add missing hookEventName to hook JSON output

### Reverted

- Remove eager tool-result stubbing (breaks cache anchors)

### Documentation

- Restore cache keepalive cost analysis lost in overbroad revert

## [1.1.32] - 2026-04-13

### Added

- Eager tool-result stubbing in fresh tail
- Cache cost analysis script for proxy log evaluation

### Fixed

- Cache status countdown uses elapsed time, usage log includes threadID
- Sync script excludes cache_cost_analysis.py, .last-sync-hash stays local
- Correct collapsing pipeline description in README
- Keepalive ping strips context_management + statusline uses CacheState

### Documentation

- Add eager tool-result stubbing to Features.md, README, and landing page
- Eager tool-result stubbing implementation plan
- Add community files — issue templates, contributing guide, code of conduct
- README overhaul — badges, comparison table, context screenshot

## [1.1.28] - 2026-04-12

### Added

- Effort_floor proxy setting
- Auto-collect commit messages from private repo
- Extended prompt rewrites — 7 new quality directives
- Make update — check, upgrade, build, test dependencies in one step

### Changed

- Change keepalive defaults to 5m mode with 5 pings
- SEO optimization — meta tags, structured data, semantic HTML
- Landing page content refresh
- Compact skill-eval output format
- Remove bleve dependency, add creack/pty
- Landing page styling and content refresh
- Dependencies + make test scope

### Fixed

- Uninstall properly restores Claude Code working state
- Cache status display considers keepalive pings
- Per-thread cache status files prevent cross-session timestamp bleed
- Accept string type for trigger_extensions in ingest_docs handler
- Include version in asset filename to match GoReleaser naming
- Use short temp dir for unix socket in macOS CI tests
- Use root-anchored excludes for git internal files
- Go version 1.24 → 1.25 in CI workflows + add FSL 1.1 license
- Hook-check no longer blocks all bash commands on stale gotcha
- IVF index always-current — save on shutdown, staleness check, periodic save
- Fork extraction on subscription — extract OAuth token from Bearer header, send as x-api-key
- Skip fork extraction on subscription (no API key for /v1/messages)
- Fork extraction auth on subscription — forward original request headers
- Persist rate limits in OpenAI-parity path (subscription fix)

### Performance

- Reduce daemon RSS ~52% — SSE singleton, weight release, parser buffers

### Documentation

- Add plan for cache-status via daemon-RPC
- Add yesmem.io landing page with GitHub Pages deployment
- Add Windows WSL2 install note to README
- Update README — sponsor section, production date correction

## [1.1.27] - 2026-04-09

### Performance

- Preserve tools in fork requests for cache prefix compatibility

## [1.1.26] - 2026-04-09

### Added

- Persist FrozenStubs and DecayTracker across proxy restarts
- Activate fork_coverage tracking — dead code revived
- Fork reflection propagates message range to learnings
- Batch extraction sets lineage from chunk message range
- Chunker carries message index range (FromMsgIdx/ToMsgIdx)
- Persist and read learning lineage (source_msg_from/to)
- Learning lineage — source message attribution
- Terminal shows collapsing savings — raw vs actual tokens

### Changed

- Translate all German strings to English

### Fixed

- Throttle all background LLM calls when API utilization exceeds 50%
- Update Opus pricing to 4.6 rates ($5/$25 per MTok)
- Collapsing savings display — correct raw source and drift
- Persist raw token estimate in FrozenStubs for collapsing display
- Set raw estimate in frozen-prefix path for collapsing display
- Daemon retry on cold start — 100% cache hit after deploy
- Re-persist frozen stubs when initial persist fails after deploy
- Normalize zero-value lineage to -1 sentinel — prevents false attribution on non-extraction learnings
- Add self-test to security scanner + symlink for superpowers plans
- Security scanner was broken — --dry-run prevented actual scan

### Performance

- Keepalive pings 12→6 + statusline refreshInterval
- Slim down MCP tool descriptions — 24863 to 16836 chars (-32%)
- Default to ephemeral cache TTL (5min) instead of 1h

### Documentation

- Rewrite README for launch — adaptive context window pitch, benefit-oriented features, install script
- Update Features.md with 7 undocumented features from recent commits
- Add cost analyses + learning lineage plan, ignore .codex

## [1.1.0] - 2026-04-08

### Added

- Optimize skill trigger descriptions for better MCP tool activation
- Include LoCoMo benchmark in production binary
- Add _meta maxResultSizeChars to large MCP tool results
- Use X-Claude-Code-Session-Id header in proxy
- Resolve Claude Code auth conflict in setup

### Changed

- Rename docs/ to yesdocs/ for internal docs, new docs/ for public
- Split SSE weights into 3 parts for GitHub compatibility
- Move MultiAgentFeatures.md to docs/, include BENCHMARK.md in public sync

### Fixed

- Neutralize hardcoded paths in tests for public release
- Remove LFS tracking, store SSE weights as regular git objects
- Broaden API key pattern in sync security scanner
- Make yesmem-docs skill description generic
- Correct embedding model references across all active docs
- Permissions.allow serializes as [] not null after uninstall
- Init missing maps in graceful shutdown test setup
- Inject recovery block post-refine so it survives briefing refinement

### Performance

- Force tool rotation in agentic benchmark mode

### Documentation

- Update Features.md with 12 missing features from recent commits
- Add Opus benchmark results and retrieval ceiling finding
- Add LoCoMo benchmark methodology and results
- Add defense-in-depth security plan

## [1.0.3] - 2026-04-07

### Added

- Store API key in settings.json + cache-TTL hint (v1.0.3)

## [1.0.2] - 2026-04-07

### Fixed

- Session-end hook via daemon RPC instead of direct DB access

## [1.0.1] - 2026-04-06

### Added

- Throttle extraction when API utilization exceeds 50%
- Parse rate-limit headers + cache breakdown in forward path
- _track_usage handler accepts cache + rate-limit fields
- TrackTokenUsage with cache_read/cache_write breakdown
- ShouldThrottle + Utilization with fallback chain
- RateLimitInfo struct + ParseRateLimitHeaders

### Changed

- V0.52 — cache breakdown columns in token_usage

### Fixed

- Remove double v-prefix in version output

### Documentation

- Rate-limit tracking implementation plan (8 tasks, TDD)
- Rate-limit tracking design spec (v1.0.1)

## [safety-before-rebase-2026-04-16] - 2026-04-16

### Added

- Add cap_store — sandboxed capability database with CRUD MCP tool
- Add register_capabilities MCP tool, /build-tool skill, review fixes
- Add briefing capability hints, /build-tool skill, review fixes
- Add Capability Memory handlers, MCP tools, and tests
- Add 'capability' as valid learning category
- Auto-generated changelog with sync integration
- Non-interactive default install
- Integrate pulse learnings into collapse session timeline
- Capture CC away_summary as pulse learnings
- Add --per-commit mode to sync-public.sh
- Re-enable eager tool-result stubbing (cache-safe with breakpoint shift)
- Persistent timestamp + msg:N injection on all messages
- Selective cache breakpoint shift for text-only turns
- Eager tool-result stubbing in fresh tail
- Cache cost analysis script for proxy log evaluation
- Effort_floor proxy setting
- Auto-collect commit messages from private repo
- Extended prompt rewrites — 7 new quality directives
- Make update — check, upgrade, build, test dependencies in one step
- Persist FrozenStubs and DecayTracker across proxy restarts
- Activate fork_coverage tracking — dead code revived
- Fork reflection propagates message range to learnings
- Batch extraction sets lineage from chunk message range
- Chunker carries message index range (FromMsgIdx/ToMsgIdx)
- Persist and read learning lineage (source_msg_from/to)
- Learning lineage — source message attribution
- Terminal shows collapsing savings — raw vs actual tokens
- Optimize skill trigger descriptions for better MCP tool activation
- Include LoCoMo benchmark in production binary
- Add _meta maxResultSizeChars to large MCP tool results
- Use X-Claude-Code-Session-Id header in proxy
- Resolve Claude Code auth conflict in setup
- Store API key in settings.json + cache-TTL hint (v1.0.3)
- Throttle extraction when API utilization exceeds 50%
- Parse rate-limit headers + cache breakdown in forward path
- _track_usage handler accepts cache + rate-limit fields
- TrackTokenUsage with cache_read/cache_write breakdown
- ShouldThrottle + Utilization with fallback chain
- RateLimitInfo struct + ParseRateLimitHeaders
- Fork reflection — quality filter, importance, emotional_intensity
- CI workflow with macOS matrix, update release pipeline
- GitHub Actions release pipeline via GoReleaser
- Typed graph augmentation — depends_on/supports edges score higher in hybrid search
- Statusline shows keepalive timeline and actual token usage
- Per-thread keepalive — each session stays warm
- TTL detection via cache_read ratio + terminal display
- Log TTL detection result on first determination
- Add cache_keepalive_mode + ephemeral_1h detection
- Add cache keepalive to setup wizard
- Wire CacheKeepalive + TTLDetector into request pipeline
- Add CacheKeepalive — bounded ping timer
- Add CacheTTLDetector — adaptive 1h TTL measurement
- Add cache keepalive config fields
- Persist SawtoothTrigger token state across restarts
- Daemon test suite — coverage 21.7% → 47.8% (+387 tests)
- Wire loop detector into handleMessages pipeline
- Loop warning formatting + state management with cooldown
- Loop detector core — extraction + 3 detection signals
- Add 8 bundled yesmem skills for MCP tool workflows
- Contradiction warning when injected learning contradicts previous
- Get_contradicting_pairs handler for proxy contradiction checks
- Graph augmentation in hybrid_search via association edges
- GetAssociationNeighbors + GetContradictingPairs
- Relate_learnings MCP tool + entity-overlap auto-linking in fork extract
- Contradiction handlers write contradicts edges to association graph
- Typed association edges — v0.51 migration, relation_type field, entity overlap
- Fork token tracking + extraction session-age gate
- Fork reflection integration — impact, contradictions, previous learnings
- Fork reflection handlers — GetForkLearnings, UpdateImpact, ResolveContradiction
- Session-aware fork prompt via i18n + PreviousForkLearnings
- Extended fork structs — Contradictions, ImpactScore, Status
- Fork reflection prompt strings with German defaults
- UpdateImpactScore, GetForkLearnings, ForkCoverage methods
- Schema migration for impact_score + fork_coverage
- Fork_extract_learnings + fork_evaluate_learning handlers
- Extract_and_evaluate fork type + config integration
- FireForkedAgents orchestration + proxy integration
- BuildForkRequest + doForkCall — request cloning for forked agents
- ForkState — per-thread token growth + failure tracking
- System prompt rewrite — strip throttling, inject Ant-quality directives
- Add msg_type parameter to send_to MCP tool schema
- Type-aware buildChannelDirective — ack/status suppress replies
- Send_to parses msg_type + drops ACK-on-ACK loops
- Add msg_type to channel messages — command/response/ack/status + IsAckOnAck detection
- Forked Agent Design Spec + Token Cost Calculator
- Docs-hint injection for subagents via proxy passthrough + periodic pipeline
- Thinking-based subagent detection — replace message-count heuristic
- Smart doc injection infrastructure (AND-matching, doc_type, coding context)
- Inject docs-available reminder at 10k plan checkpoints
- Build DocsHint from reference sources at set_plan time
- Add GetReferenceSources for plan docs reminder
- Swarm-Driven Development Skill + Setup-Integration
- ExtractCodingQuery — Query aus Edit/Write/Read tool_use extrahieren
- RST-Support + Rich Metadata Extraction für Doc Ingestion
- CC Cache Bug Mitigations — Sentinel Sanitization + Billing Header Normalization
- Remember() akzeptiert anticipated_queries + bessere trigger Description
- Agent supervision — cascade stop, dead PID detection, orphan grace period, auto-restart
- Contextual doc injection — auto-inject docs based on file extensions
- Plan-nudge injection — remind Claude to set_plan() on plan file reads
- DetectPlanFileRead + shouldNudgePlan — plan file detection
- 5 implementation plans + gitignore .claude/plans/
- Tmux spawn case in BuildSpawnCommand — window-scoped hook + split-window + tiled layout
- SpawnMode + DetectSpawnMode + isTmuxAvailable — tmux-based agent window layout
- Update_agent_status MCP tool — agent-reported phase tracking
- Proxy telemetry interceptor — auto-track turns and tokens per agent session
- Agent telemetry schema — turns_used, input_tokens, output_tokens, phase
- Whoami MCP tool — agent self-discovery of session ID
- Persistent-orchestrator skill — resume-based Implement→Review→Commit pipeline
- Agent resume via true PTY restart — stop→resume→--resume flow
- OpenAI extraction client + tests for threshold, setup, MCP, learnings
- Model-specific token thresholds + configurable pricing + Codex Sawtooth fix
- Codex agent backend — spawn_agent(backend='codex') with full lifecycle
- Codex session parser + setup — JSONL parsing and auto-configuration
- OpenAI parity pipeline — full compression + Sawtooth for Codex/Responses path
- Extract CWD from Codex <cwd> tag in input messages
- Briefing + associative context injection for OpenAI pipeline
- Responses API adapter for Codex CLI
- Telegram-bridge command — auth, polling, agent lifecycle, response capture
- OpenAI upstream forwarding with reverse translation
- Telegram Bot API client — getUpdates + sendMessage with tests
- Route /v1/chat/completions to OpenAI adapter + config
- OpenAI HTTP handler with request/response translation
- Anthropic SSE → OpenAI streaming chunk translator
- OpenAI→Anthropic request translation with tool grouping
- Add OpenAI chat completion types for format adapter
- Telegram adapter implementation plan — 4 tasks, TDD
- Telegram adapter design spec
- Orchestrator status ping every 5min + no-polling guidance in /swarm
- Crash quarantine — isolate learnings + taint scratchpad before retry
- Graceful agent recovery on daemon restart (OTP-style hot reload)
- Crash recovery — auto-retry 3x, stop_all_agents, crash context
- Auto-translate bundled commands during setup
- /schwarm as bundled Claude Code command + setup auto-install
- /schwarm skill — Multi-Agent Orchestrierung Protokoll
- Agent heartbeat, freeze/resume, token tracking + multi-agent orchestration
- Agent orchestrator DB-backed — replace JSON file with SQLite + MCP tools
- PTY bridge — bidirectional agent control via creack/pty
- Relay CLI — route messages between spawned agents via daemon
- Spawn-agents — multi-terminal agent orchestration with stdin pipes
- Scratchpad CLI — yesmem scratchpad write/read/list/delete with stdin pipe support
- Scratchpad MCP tools — write/read/list/delete with formatters
- Scratchpad daemon handlers — write/read/list/delete via RPC
- Orchestrator — terminal detection + spawn command builder (Linux + macOS)
- Scratchpad storage layer — DB schema + CRUD methods with upsert semantics
- Turn-based learning decay — activity-driven scoring replaces wall-clock decay
- Daemon periodic update check with configurable interval
- CLI commands — check-update, update, migrate
- Orchestrator — check, download, migrate, restart
- Binary download with checksum verification and atomic swap
- GitHub Release checker with version comparison
- Add UpdateConfig struct with defaults
- Semver parsing and comparison
- User profile synthesis — auto-generated user persona in briefing
- Build tag isolation — benchmark code excluded from production binary
- Transfer 5 benchmark optimizations to production core
- Anthropic tool-calling + strict judge + ToolCapableClient interface
- Agentic mode + search optimizations — score 0.13 → 0.87
- 3-way RRF with entity-boost search
- Increase anticipated_queries from 3 to 5 per learning
- BM25 optimizations + gold mode + gen-aq + tiered search — score 0.58
- Doc_chunks hybrid search — FTS5 tokenchars + SSE embeddings + vector cache
- Task_type classification for unfinished learnings + get_learnings by ID
- Open work reminder instruction + config + i18n
- Bidirectional memory — proactive user reminders
- Local hybrid search — no daemon dependency
- Expand timestamp hints to 33 variants for 3k+ message sessions
- Rotate timestamp hints to prevent habituation
- Periodic recurrence detection + cluster distillation, remove dead role-persona code
- Redesign Rules Reminder pipeline for quality and universality
- Cost tracking + progress logging for sequential path
- --dry-run cost estimation + --sample-pct for QA subsampling
- Multi-tool tiered search strategy
- Add sync-public.sh for dual-repo workflow
- Rename setup→install, fix uninstall gaps, transparent CLI cost tracking
- LoCoMo benchmark adapter — evaluate YesMem against standard memory benchmarks
- Persist plans in SQLite, survive daemon restarts
- Proxy token tracking + SetStability comment fix
- Hub dampening + query_facts + expand_context
- Plan re-injection with MCP tools + proxy checkpoints
- Auto-refresh watcher for CLAUDE.md changes
- CLAUDE.md condensation + periodic proxy re-injection
- Deep_search hint in hybrid_search MCP output
- Central project name resolution in daemon handler
- Complete project migration across all 15 tables + dry-run mode
- Metamemory briefing block — knowledge self-assessment
- Session quarantine + skip-indexing for noise control
- Recurrence detection — feeling of knowing
- Feedback loop — use/noise signals propagate to cluster scores (Stufe 4)
- LLM cluster distillation (Phase A)
- Query clustering with cluster-affinity scoring (Stufe 2)
- Auto-trigger at daemon startup + settled idle
- Contradiction-boost for correcting learnings
- Lower thresholds + source-boost for user-stated learnings
- Query_log for context-aware retrieval (Stufe 1)
- Anticipated_queries field for context-aware retrieval
- 30-minute cooldown for freshly stored learnings
- BM25 2-term fallback + adaptive associative threshold
- Hook-failure for all tools + error-enriched associative context
- Minimal channel server for future --channels support
- Replace dialog system with channel-based agent messaging
- Channel tags + improved directives + direct injection
- Agent-role overlay for multi-agent persona directives
- EINGEHEND/AUSGEHEND markers for message direction
- Proxy-based dialog injection via metadata.session_id
- Conflict detection, agent_role in search, auto-broadcast
- Terminal detection, Ghostty skip, 1min polling
- Broadcast table, agent_role, dialog lineage
- Formatted MCP responses + _caller_pid in proxyCallFormat
- Xdotool push for real-time session notification
- Add injector.js to repo, auto-install on deploy
- Stdin injection for automatic session notification
- Add broadcast — project-wide messaging to all sessions
- Agent-to-Agent communication with shared memory
- Detect git commits and invalidate stale learnings
- Anti-fixation + project-recency scoring fixes
- Porter stemming for learnings_fts — improves conjugation recall
- Extract newest sessions first for fast initial briefing
- Yesmem consolidate — iterative knowledge dedup with convergence loop
- Embedding-based pre-dedup via cosine similarity (threshold 0.92)
- Pre-admission dedup — skip duplicates and update existing before insert
- Separate messages.db + FTS5, remove Bleve dependency
- Fixation-ratio als Session-Quality-Signal (2e)
- Session-correction-rate als Quality-Signal (2e)
- Skill/doc-learnings rework — whole-file storage, contextual hints, collapse protection
- Add pivot_moment section (Wendepunkte) to archive block
- Session flavors + git commits + structured learnings in archive block
- Event-based timeline replaces useless digests
- Add pinned learnings — refinement-resistant briefing instructions
- Auto-detect Claude Code keys and set provider accordingly
- Redirect search hits to active successors instead of filtering
- Tiered AND search with absolute term caps (5→100, 4→70, 3→40)
- Cosine-based scores replacing rank-based RRF normalization
- Normalize RRF scores to 0-100 scale
- Source attribution, API cost, cross-session recall
- Differentiated gap resolution metrics
- Context-enriched gap review with resolved detection
- LLM-based gap review with daily daemon timer + CLI
- Per-category effectiveness, coverage, time filters, behavioral split
- Source-tracked injection IDs, error feedback loop, auto persona dedup
- SSE provider with DyT normalization + down-rank narrative/unfinished
- Wire HTTP API server into daemon startup
- Refine ingest endpoints — use parser package, fix server_test signature
- Add POST /api/ingest and /api/ingest-history endpoints
- Add POST /api/analyze-turn endpoint
- Export ScanAssistantSignals for httpapi use
- Add POST /api/assemble endpoint
- Add backup and migrate-project CLI commands
- Add OpenClaw JSONL session parser
- Add auth token generation
- Add HTTP server skeleton with middleware
- Add HTTPConfig for OpenClaw HTTP API
- Fixed taxonomy, server-side validation, embedding-based trait dedup
- Implement yesmem stats + benchmark commands
- Add timestamp awareness reminder to all injection points
- Externalize tone rewrites into translatable Strings struct
- Replace ONNX with static multilingual embeddings
- Add IVF index for approximate nearest neighbor search
- Replace chromem-go with brute-force cosine similarity
- Switch SQLite driver from modernc to ncruces/go-sqlite3
- Wire GatedClient into briefing refinement
- Global API health gate + GatedClient wrapper
- Add HasBudget() pre-flight check for LLM phases
- Vector search temporal filtering + FEATURES.md update
- Add HasBudget() pre-flight check for LLM phases
- Persistent extracted_at + narrative_at markers on sessions
- Add since/before temporal filtering to all 5 search APIs
- Persistent extracted_at marker on sessions
- Content hash, stats command, setup improvements, briefing i18n
- Inline reflection — 3 Signale aus Thinking+Text ersetzen Haiku Reflection Call
- Project skill discovery + generalized archive/restore
- Project-level skill discovery + generic archive path
- Think reminder injection via proxy + collapse timestamps
- Sync-docs --discover flag for CLI skill discovery
- Auto-discover and import skills on setup + daily daemon cycle
- Remove-docs now fully cleans up — hard delete, no residue
- --url flag for git clone import + auto-detect SKILL.md
- Git-aware doc sync + daily auto-sync in daemon
- Detect plugin skills and show deinstall hint instead of archiving
- Auto-archive skills on ingest, auto-restore on remove-docs
- Skill-hint learnings for process skills from frontmatter
- Process skill detection in Pass 1 — skip destillation for workflow skills
- Skill directive extraction — behavioral alignment via learnings
- Add remove_docs MCP tool — undo doc imports from within sessions
- Two-pass destillation with cutoff-awareness + purge on remove
- Add ingest_docs MCP tool — import docs from within Claude sessions
- Document ingest pipeline — docs_search, chunking, destillation
- Injection-time content dedup via BigramJaccard, clamp precisionFactor to 0.5-1.5
- Add ComputeContextualScore with project/entity/domain boost, wire into briefing + get_learnings
- Ebbinghaus exponential decay with stability growth on spaced access
- MCP shows u:/i: counts, milestones ranked by use_count, MigrateHitCounts extended
- Save_count heuristic in check.go via proxy_state persistence
- Add increment_match/inject/use/save RPC methods, get_learnings bumps inject_count
- Rewrite ComputeScore with 5-count model (useBoost, noisePenalty, precisionFactor)
- Add 5-count schema (match/inject/use/save) for differentiated learning scoring
- Auto-escalation — block repeated Bash failures via PreToolUse hook
- Disable self-prime signal + raise token thresholds
- Per-session token threshold override via set_config
- Statusline shows collapsing range + remove clock icon
- Statusline shows token usage as current/threshold (e.g. 45k/300k)
- Dynamic token threshold via MCP + statusline display
- Dynamic Claude persona with computed behavioral dimensions + self-reflection
- Fix hook errors for non-Bash tools + periodic learning extraction reminder
- Structured box formatters for remember and resolve MCP output
- Relative timestamps in MCP search and learnings output
- Compact plaintext formatters for all MCP tool outputs
- Go statusline + memory reminder on every prompt and tool use
- Cache status tracking, session recovery, daemon briefing endpoint
- Opus briefing refinement + persona extraction in briefing system
- Message refs in compress stubs + metamemory in compress-only path
- Temporal annotation + metamemory learnings in archive blocks
- GetLearningsSince storage + get_learnings_since RPC handler
- Usage deflation to suppress CC "Context low" warning
- Sawtooth Cache Optimization — frozen stubs for ~74% token cost reduction
- Sawtooth Cache Optimization Design-Spec + Codex OpenAI Proxy Spec
- Hook-think — periodischer Memory-Reminder im UserPromptSubmit
- Proaktive Context-Kompression (Phase 0) vor Stubify
- Quickstart command + setup integration + .mcp.json merge fix
- Setup wizard and config improvements
- Daemon RPC handlers for signal processing
- Associative context improvements — stopwords, score-gap, language auto-detect
- Signal Reflection Call — async Sonnet-based cognitive signals
- Project-boost for associative context
- Knowledge gap tracking in proxy associative context
- 5 memory utilization improvements for better context delivery
- Block direct edits to auto-generated yesmem-ops.md
- Move narrative to system block, strip old narrative messages
- Inject briefing as cached system block
- Add system-block manipulation helpers
- Add `yesmem cost` CLI command
- Persistent budget tracker — spend survives daemon restarts
- Include llm budget defaults in generated config.yaml
- Trust-gate in remember() — low/medium/high supersede resistance
- Trust score calculation for supersede resistance
- Importance + supersede_status columns for trust-based resistance
- Explicit knowledge gap detection and tracking
- Wire two-pass extraction into daemon
- Add TwoPassExtractor + SessionExtractor interface
- Add PreFilterMessages, SummarizeSystemPrompt, and extraction config for two-pass pipeline
- Cross-project dedup for global truths
- Sharper prompt — near-duplicates, stale facts, junk detection
- Batch large groups into chunks of 50
- Dynamic categories instead of hardcoded whitelist
- Navigable supersede chains with volatility tracking
- Remember() supports supersedes parameter for explicit knowledge evolution
- Add valid_until + supersedes columns for temporal validity
- Subagent indexing — discover, parse, and surface Claude Code subagents
- Register hook-assist + replace micro-reminder with idle-tick
- Hook-assist — deep_search on Bash failures (Priming)
- Idle-tick — dynamic yesmem-usage reminder via daemon
- Idle_tick endpoint with per-session counter + global MCP reset
- Add export/import CLI for non-recoverable learnings
- Tokenizer-based estimation + setup enhancements
- Aufwach-Narrative mit Metamemory-Clustering
- Index_status handler + IndexAllWithProgress rename
- Progress tracking via callback + atomic counters
- Compaction pipeline — collapse, compaction blocks, reminder injection
- Cost estimation from real data, API key prompt, Windows support
- Daily budget limits + extraction cap + skip logging
- Decay pinned paths + file path tracking
- Decay stage 3, compaction pipeline, calibrator DB persistence
- Centralized version via buildinfo + Makefile with auto-versioning
- ANSI color logging, project name, and service resilience
- Auto-setup on --all and daemon refresh
- Add --all and --dry-run flags
- Add CLAUDE.md inotify watch (30s debounce) + fix schema migration
- Add 2h claudemd refresh timer and runClaudeMdRefresh
- Add SetupProject helper and CLI subcommand (--project, --dir, --setup)
- Add Generator, prompt builder, setup helper with tests
- Add ClaudeMdConfig, ClaudeMdState CRUD, GetLearningsForClaudeMd
- Setup generates proxy config (enabled: true) + confirmation message
- Proxy auto-start in daemon + config toggle + session ID in logs
- Proxy logs to ~/.claude/yesmem/proxy.log + stderr
- Add `yesmem stop` command to cleanly stop daemon/proxy
- Proxy auto-start on SessionStart + setup integration
- Infinite-thread proxy — API proxy with intelligent context management
- Narrative prompt with emotional arc + self-reflection
- Richer milestones — top 10, intensity labels, 300 char context
- Auto-cleanup short sessions on daemon startup
- Add SupersedeShortSessionLearnings for cleaning up trivial sessions
- Compact narrative display — newest full, older as pulse only
- Compact pulse format in narratives
- Project pulse in narrative generation
- Milestone timeline in briefing — top emotional sessions across time
- Enhanced backfill-flavor with --force, emotional_intensity, 5k token input
- Add synthesize-persona CLI + use Opus for persona synthesis
- Use Opus for narratives + persona synthesis in daemon
- Feed pivot moments into persona synthesis + rewrite prompt for relationship anchors
- Remove DIRECTIVE header + extend BuildSynthesisPrompt with pivots
- Reorder briefing — narratives before persona directive
- Add progress logging to embed-learnings migration
- Embed all-MiniLM-L6-v2 model in binary (go:embed)
- Integrate embedding into daemon lifecycle
- Auto-embed learnings on insert via daemon handler
- Add embed-learnings CLI command + migration logic
- Add embedding config + provider factory
- Add hybrid_search MCP tool + daemon handler
- Add Reciprocal Rank Fusion (RRF) for hybrid search
- Add embedding indexer (connects Provider + VectorStore)
- Add chromem-go vector store wrapper
- Add local embedding provider (hugot + GoMLX simplego)
- Add embedding Provider interface + NoneProvider
- Gap awareness section — 'Da war noch mehr...' in briefing
- Gap awareness data types, template, and i18n string
- GetLearningCounts query for gap awareness briefing section
- Auto-migrate hit_counts after reextract
- Add reextract command for retroactive extraction
- Filter faded learnings from briefing (score < 0.1)
- Decay stabilized by last_hit_at, user_stated floor 0.5
- Track last_hit_at on learnings for decay stabilization
- Emotional_intensity boosts scoring (max +30%)
- Extract session_emotional_intensity (0.0-1.0)
- Add emotional_intensity column to learnings
- Show pivot_moments in briefing as Schlüsselmomente
- Extract pivot_moments from sessions (Wendepunkte)
- Add pivot_moment as new learning category (weight 1.6)
- SessionEnd Hook + deterministische Recovery nach /clear und /compact
- Hook-check fuer Edit|Write erweitert (File-Gotchas)
- FTS5 full-text search for learnings v0.8.18
- Session recovery for /clear and /compact events
- Unfinished task resolution system — resolve, resolve_by_text, hooks, CLI v0.8.17
- Persona engine improvements — expertise extraction, evidence thresholds, cleanup
- --reset flag for bootstrap-persona to start clean
- Bootstrap-persona CLI command for initial persona extraction
- Adaptive Persona Engine — auto-calibrating user profile (Phase 1-5)
- Unfinished TTL — filter old open-work items from briefing
- Immersive narrative prompt + SummarizeMessages + regenerate command
- Relevance scoring for learnings — categoryWeight × recencyDecay × hitBoost
- Hybrid LLM backend — API + CLI fallback + cost transparency
- Add mcp-error.log for diagnosing broken pipe events
- Lazy proxy connection + auto-reconnect for MCP server
- Register MCP in ~/.claude.json for user-scope (all projects)
- Generate narratives for all existing sessions on startup
- Source attribution for learnings
- Session narratives — handover from Claude to Claude
- Multilingual briefing via LLM translation at setup
- Auto-detect system language for stop-word filtering
- Configurable languages for stop-word filtering
- Narrative briefing with dedup and personal tone
- Daemon writes to log file ~/.claude/yesmem/daemon.log
- Daemon start via systemd/launchd when auto-start enabled
- Setup auto-starts daemon in background
- Auto-detect non-permanent binary location in setup
- Uninstall command + clean settings removal
- Interactive setup wizard + robust settings registration
- Auto-generate MEMORY.md per project
- Daemon lifecycle management (single instance + --replace)
- Live progress display for extraction + profiling
- LLM extraction in daemon + improved micro-reminder
- Complete YesMem v0.1.0
- Setup command + status command
- Self-awareness features (profiles, mood, feedback)
- LLM learning extractor + knowledge evolution
- Anthropic API client + session chunker
- YAML configuration with sensible defaults
- Daemon with fsnotify file watcher
- In-memory association graph with BFS traversal
- Briefing generator with staffed output
- MCP server with 9 tools + micro-reminder subcommand
- Indexing pipeline orchestrating full data flow
- Per-session bloom filters for search pre-filtering
- Bleve full-text search with faceted filtering
- JSONL archiver for permanent session backup
- SQLite storage layer (9 files, 1 concern each)
- JSONL parser for Claude Code session files
- Core data models for YesMem
- Initial YesMem project structure

### Changed

- Consistent session_flavor JSON key, remove redundant DISTINCT
- Remove pulse content truncation from timeline
- Change keepalive defaults to 5m mode with 5 pings
- SEO optimization — meta tags, structured data, semantic HTML
- Landing page content refresh
- Compact skill-eval output format
- Remove bleve dependency, add creack/pty
- Landing page styling and content refresh
- Dependencies + make test scope
- Translate all German strings to English
- Rename docs/ to yesdocs/ for internal docs, new docs/ for public
- Split SSE weights into 3 parts for GitHub compatibility
- Move MultiAgentFeatures.md to docs/, include BENCHMARK.md in public sync
- V0.52 — cache breakdown columns in token_usage
- Archive unused test scripts and prototype code
- Simplify sync-public.sh — exclude entire docs/ directory
- Split learnings.go into 7 concern-based files
- Features.md, config, proxy, setup — accumulated minor changes
- Add MsgType to DialogMessage model + schema migration
- Consolidate heartbeat — merge crashRecovery, wire status ping, add hung detection
- Add example_query column to doc_sources for plan docs reminder
- Docs_search Vector-Suche als BM25-Fallback + Source-Filter
- ReExpansion Budget 25%, User Profile Time Guard 72h, skill-Param entfernt
- Actual-based token estimation — lastActual + delta ersetzt Calibrator
- Remove viewerTerminal field + config — viewer vollständig entfernt
- Remove tmux shared viewer — kein Tastatur-Input, zu unzuverlässig
- Re-enable tmux viewer, add TODO for bridge-in-pane refactor
- Disable shared tmux viewer — kein Tastatur-Input möglich
- Swarm skill — clarify ACK protocol, prevent infinite loops
- HandleSpawnAgent — use tmux panes when running inside tmux session
- Remove INSTRUCTION: prefix from system-reminder hints — reduces reflexive trigger behavior
- Persistent-orchestrator skill — telemetry-based health checks
- Agent telemetry plan — schema migration, threadID verify, thread-safety, session cache, token_budget check
- Agent limits x10 — Implementer 1000, Committer 100, Swarm default 300 turns
- Swarm + persistent-orchestrator — whoami() als Step 0 beim Start
- Persistent-orchestrator — hard prohibitions on self-implementation and auto-commit
- Remove remaining local projectMatches/projectMatchesHybrid copies
- Spawn_agent — add model and max_turns parameters
- Cmd_relay — remove relayResume, delegate to daemon
- Setup wizard — model-first selection + auto-install to ~/.local/bin
- Deep_search + get_session return full untruncated content
- Setup wizard + thinking extraction + multi-provider CLI cleanup
- Extraction OpenAI provider + messages.db migration fix + GPT pricing
- Storage + setup — schema extensions, session tracking, status command
- Daemon + indexer — watcher consolidation, WalkDir migration, handler cleanup
- Rename /schwarm → /swarm — MCP command name must be English
- /schwarm skill — DAG-Modus für Execution Dependencies
- /schwarm skill — crash recovery docs, stop_all_agents, auto-retry
- /schwarm skill — Agent A entscheidet Modell-Wahl per Budget-Strategie
- Spawn-agents — add --caller-session flag for send_to callback + spec/plan docs
- Persona trait normalization + cleanup
- Remove skill-ingestion pipeline + move skill-eval to proxy
- Deduplicate timestamp hints into shared internal/hints package
- Switch project-recency boost from multiplicative to additive
- Clean separation — hook does dialogs, proxy does instructions
- Remove dead extractDecisions function
- Remove redundant Decisions section, increase learnings limit
- Dedup events, fix git heredoc parsing, boost learnings
- Disable fresh remember injection, re-enable associative context
- Remove ONNX model assets + hugot dependency from go.mod
- Remove ONNX/hugot local provider
- Add yesmem.db to gitignore
- Migrate hook-think to proxy, fix config path + progress race
- Comprehensive pipeline status + embed throttle
- Learn.go+failure.go bump match_count instead of hit_count
- Associate.go+signal_bus use match/inject/use counts instead of hit_count
- Flatten extraction to single learnings array with category field
- Upgrade GoMLX to v0.27.0 (GQA, SDPA fixes)
- Split god files — main.go, proxy.go, handler.go
- Gitignore + docs cleanup, deprecate ops.md in CLAUDE.md
- Config alignment + calibrator default ratio 1.0
- FINAL-Logzeile farbkodiert nach Kompression
- FINAL-Logzeile zeigt CompressContext-Einsparung
- Associative Context Logging — CAP-Limits sichtbar machen
- Dynamic associative context limits
- Milestone threshold 0.3 → 0.5 + FEATURES.md updates
- Clean up dead hooks + milestone label
- Switch hybrid_search BM25 from Bleve messages to FTS5 learnings
- Clustering reads embeddings from VectorStore instead of on-the-fly ONNX
- Remove dead isNarrativeBriefing code
- Remove briefing + idle-tick hooks — proxy handles these now
- Extend .gitignore with build artifacts
- Add perspective-shift passage to awakening template
- Remove superseded plan docs + update gitignore
- Lower MinSessions default from 10 to 3
- Optimize pipeline quality
- Ignore .worktrees directory
- Unify log paths under ~/.claude/yesmem/logs/
- Bump version to v0.9.2 (V2 Vector Search)
- Hook-resolve outputs visible feedback on git commit
- Narrative generation with configurable model + Opus default
- Improve briefing signal-to-noise ratio
- Dbstats --update-memory for manual MEMORY.md regeneration
- Dbstats --set-profile for manual project profiles + correct memory profile
- Increase MaxTokens to 8192 for bulk evolution responses
- Dbstats --profiles uses LLMClient interface + generated 14 profiles
- Supersede stale learnings + dbstats --supersede command
- Rename module github.com/chiefcll → github.com/carsteneu
- Rename all paths from .claude/memory to .claude/yesmem
- MCP server proxies to daemon via Unix socket

### Fixed

- Address pre-merge code review findings for Capability Memory
- Exclude capabilities from evolution pipeline, clean embedding text
- Skill-eval block scope to user text input only, skip tool_result turns
- Preserve session flavors across extraction runs, fetch all phases for current session
- Truncate pulse content to 150 chars in session timeline
- Set created_at on pulse learnings from JSONL event timestamp
- Strip context_management from fork requests
- Remove truncation from archive block learnings and session flavors
- Use session start from DB for collapse learning query + propagate threshold to sawtooth
- Append no-echo instruction to rotating timestamp hints
- Restore TotalPings + cache countdown display lost in overbroad revert
- Restore threadID in usage log lines lost in overbroad revert
- Restore hookEventName, .gitignore and sync excludes lost in overbroad revert
- Keepalive interval display uses exact minutes+seconds
- Add missing hookEventName to hook JSON output
- Cache status countdown uses elapsed time, usage log includes threadID
- Sync script excludes cache_cost_analysis.py, .last-sync-hash stays local
- Correct collapsing pipeline description in README
- Keepalive ping strips context_management + statusline uses CacheState
- Uninstall properly restores Claude Code working state
- Cache status display considers keepalive pings
- Per-thread cache status files prevent cross-session timestamp bleed
- Accept string type for trigger_extensions in ingest_docs handler
- Include version in asset filename to match GoReleaser naming
- Use short temp dir for unix socket in macOS CI tests
- Use root-anchored excludes for git internal files
- Go version 1.24 → 1.25 in CI workflows + add FSL 1.1 license
- Hook-check no longer blocks all bash commands on stale gotcha
- IVF index always-current — save on shutdown, staleness check, periodic save
- Fork extraction on subscription — extract OAuth token from Bearer header, send as x-api-key
- Skip fork extraction on subscription (no API key for /v1/messages)
- Fork extraction auth on subscription — forward original request headers
- Persist rate limits in OpenAI-parity path (subscription fix)
- Throttle all background LLM calls when API utilization exceeds 50%
- Update Opus pricing to 4.6 rates ($5/$25 per MTok)
- Collapsing savings display — correct raw source and drift
- Persist raw token estimate in FrozenStubs for collapsing display
- Set raw estimate in frozen-prefix path for collapsing display
- Daemon retry on cold start — 100% cache hit after deploy
- Re-persist frozen stubs when initial persist fails after deploy
- Normalize zero-value lineage to -1 sentinel — prevents false attribution on non-extraction learnings
- Add self-test to security scanner + symlink for superpowers plans
- Security scanner was broken — --dry-run prevented actual scan
- Neutralize hardcoded paths in tests for public release
- Remove LFS tracking, store SSE weights as regular git objects
- Broaden API key pattern in sync security scanner
- Make yesmem-docs skill description generic
- Correct embedding model references across all active docs
- Permissions.allow serializes as [] not null after uninstall
- Init missing maps in graceful shutdown test setup
- Inject recovery block post-refine so it survives briefing refinement
- Session-end hook via daemon RPC instead of direct DB access
- Remove double v-prefix in version output
- Fork agent parity — session flavor, project, metadata, dedup
- Register statusline in setup, migrate, and uninstall
- Per-thread state isolation across all proxy paths
- Statusline shows max pings per thread, not sum across all threads
- Keepalive pings now reset sawtooth pause timer
- Per-thread keepalive timers — active sessions no longer starve quiet ones
- Strengthen trigger descriptions for proactive activation
- Three keepalive improvements
- Retrigger keepalive when TTL detection changes
- Fix trait visibility, enforce taxonomy, sharpen dedup, add context TTL
- Extend frozen stubs TTL to 65min when 1h cache detected
- Skip TTL detection on small delta writes
- Correct TTL detection — gap test + ephemeral_1h combined
- Fix undefined reqNum in TTL detection log
- Suppress keepalive pings during TTL detection phase
- Start keepalive at 5min interval, not optimistic 54min
- Dynamic keepalive interval based on detected TTL
- Fix integration test + add keepalive shutdown cleanup
- Address 4 review findings in cache keepalive
- Sanitize billing sentinel in tool_result content blocks
- Loop detector review fixes — state machine bug, hash determinism, isolation test
- Benchmark precision, cost calculation, fork tracking, recall metric
- Benchmark cost split (YesMem-eigen vs Proxy) + precision threshold
- Restrict cache TTL upgrade to API key auth only
- Code Review — zero-score, Status filter, handler tests, error logging
- Code Review Fixes — Token Delta, InjectedIDs, Budget Enforcement
- WebFetch keywords — drop tool name to prevent false positive gotcha matches
- Review fixes — guard short strings in ACK log, semantic msg_type for heartbeat send_to calls
- Exclude is_rules sources from GetReferenceSources
- FTS5 indexiert jetzt trigger_rule neben content
- Hook-failure für WebFetch/WebSearch PostToolUseFailure
- Gate checkDeadlineTriggers behind skipUnfinished — deadline items no longer bypass agent suppression
- Briefing-hook migration + suppress unfinished todos in agent sessions
- Frozen stub TTL an CacheTTL koppeln — Dead Zone 30-61min schließen
- FinalEstimate nutzt actual-based Total statt Full-Recount
- Token budget check — AgentUpdateTelemetry statt leerer token_usage Tabelle
- Session-ID als thread-ID + proxy infrastructure improvements
- Prevent double viewer window on parallel agent spawn
- Ghostty viewer opens with --fullscreen=true
- Ghostty viewer opens in fullscreen
- Ghostty BuildAttachCommand — -e flag instead of --
- Tmux viewer v2 — sync session create + config-based viewer terminal
- Open viewer terminal when tmux session has no attached client
- Tmux detection via exec.LookPath — works from any terminal
- Code-review follow-up — SpawnMode-first detection + precise args guard
- Remove omitempty from telemetry fields — zero values must appear in JSON
- Remove ACK instruction from channel DIREKTIVE — prevents infinite ACK loops
- AgentNextID — use global MAX instead of project-scoped MAX
- Deadline trigger — timezone mismatch in day comparison
- Skip think-reminder on tool_result turns — add lastUserHasText guard
- EmitReminder silent when no gotcha; REFA: consolidate ProjectMatches
- Hook-resolve — scope task matching to current project
- Daemon extends PATH with ~/.local/bin at startup
- Locomo benchmark build — add missing //go:build benchmark tags
- Two-stage message delivery — heartbeat pings, proxy delivers content
- PTY inject newline escape — prevent message fragmentation in agent-to-agent send_to
- CWD extraction for Codex + briefing threshold + remove debug logging
- Skip non-function tools in Responses API translation
- Echo loop — MarkChannelMessagesRead now sets delivered=1
- Channel messages never marked as read — turn-based counter + prefix match
- Spawn-agents uses prompt-as-argument instead of stdin-pipe — terminal PTYs don't forward stdin
- Gnome-terminal detection via GNOME_TERMINAL_SCREEN env + --wait flag for stdin pipe
- Remove unused strings import from spawn.go
- Scratchpad storage spec compliance — correct struct fields and List behavior
- Channel messages never marked as read — turn-based counter + prefix match
- Remove hardcoded home paths from tracked files
- Extraction guard, config merge, hooks update in migrate
- Remove last time-based remnants from scoring + add scientific foundations to docs
- Wire UserProfile setting through briefing + persona fixes
- Review fixes — bytes.NewReader, LimitReader, exec timeouts, atomic backup
- Address 7 code review findings for benchmark-to-production transfer
- Add project filter to docs_search + update Features.md
- ResolveProjectShort matched worktree paths via LIKE, corrupting project param
- Remove hardcoded paths, IPs, debug code for public release
- Batch full-context queries — 1 call per sample instead of per question
- Cap filtered input to prevent runaway LLM costs
- Sharpen CondenseRules prompt to extract only violatable rules
- Let daemon --replace handle its own cleanup, remove pkill
- Wire anticipated_queries, source-boost, hub-dampening, feedback-loop, cleanup dead dialog
- Wire missing project params in plan, broadcast, and hybrid_search
- Detect zombie processes to prevent daemon startup failure
- Exclude current session from search results to prevent echo loop
- Wire project name into rules re-injection pipeline
- Wire RefineBriefing into hook path, add 91 tests
- Add --concurrency flag + longer 429 backoff
- Concurrent workers, progress logging, hybrid search, dataset format fixes
- Resolve currentProject before mismatch comparison
- Resolve_project via DB lookup instead of path.Base
- Use path.Base(proj) for flavors/learnings queries
- Project-mismatch penalty halves score for wrong-project learnings
- Project boost 0.012 → 2.0 (0-100 scale) in associative context
- Lower thresholds + relax noproj-fallback gate
- Review fixes — affinity baseline, purge safety, N+1, re-sort
- Code review fixes — source-boost field, cooldown DB, stability column, indices
- Drop junk/resolved learnings from BM25 supersede redirect
- Extend fuzzy project matching to skills, scoring, and gap hooks
- FTS5 query escaping + fuzzy project matching + fallback noise filter
- DEINE_SESSION_ID only during active agent-to-agent dialog
- 30s time-window for read marking
- Mark_read before insert to prevent self-clearing
- Action-based read marking instead of time/count
- KEINE NEUEN NACHRICHTEN + clearer EINGEHEND/AUSGEHEND directive
- EINGEHEND marker + proxy-only polling (no MCP echo)
- Remove activeSessionID fallback + stronger DIREKTIVE
- Remove channel/node_modules from tracking, add to gitignore
- Skip xdotool push for shared windows (tab detection)
- Skip dialog-response turns in PreFilterMessages
- PID-based session resolution for MCP tools
- Expose full sender session_id for dialog initiation
- Remove dead signal fields, add briefingText mutex, tool pair validation
- Update NewHandler call to match 2-arg signature
- Project name mismatch in flavor + learnings queries
- Category-scan fallback when BM25 misses conjugation variants
- Embed learnings before evolution so embedding-dedup has vectors
- Review fixes — embedding dedup before chunking, -1 sentinel, DryRun removed
- Single-ID supersede is no-op, not -1 sentinel (B4.3 LLM path)
- 6 fixes for supersede/evolution system
- Fallback to 24h lookback when sessionStart is zero after proxy restart
- Extraction via wrapper script + allLearnings merge
- CLI extraction parsing + fence stripping + findAPIKey returns source
- Stop proxy, remove both services, delete binary
- Embedding provider was 'static' instead of 'sse' in template
- Api_key got data directory path instead of actual key
- Load never returns nil, falls back to Default on any error
- Nil pointer crash when config.yaml missing
- Progress bar arguments were swapped in IndexAllWithProgress
- Progress bar clearLine uses full terminal width
- Resolve ordering cycle preventing proxy start on boot
- Filter superseded learnings from BM25 search results
- Add cleanup to extract.go paths (missed 2 of 4 insert sites)
- Exclude from vector search, limit to 2 per project
- Require AND matching with minimum 3 terms
- Remove RRF normalization, fix BM25-only score blowup
- Hard project filter + higher score threshold
- Session_id tracking for remember() + fail_count cascade fix
- Accumulate injected IDs across turns instead of overwriting
- Apply time filter to API spend summary
- Balanced English prompt with explicit keep/noise patterns
- Inject rate per message, filtered gaps, 3-class category split
- Count all learnings as embeddable after SSE migration
- Catch informal Learning #ID and Learning [ID] references
- Include all categories in auto-embed
- Auto-embed all learnings including superseded
- SQLite→VectorStore sync + status display fix
- Eliminate 10GB chromem-go load from embed-learnings
- Clean subprocess lifecycle + per-batch commit + VectorStore wait
- Clean subprocess lifecycle on daemon shutdown
- Per-batch commit + VectorStore wait + independent spawn
- Persistent change detection via DB fingerprint
- Replace volatile raw-hash with DB fingerprint for change detection
- Split FTS5 query to avoid learnings table lock contention
- Batch SupersedeLearning for reduced write contention
- Replace triggers with background sync to eliminate BM25 contention
- Hybrid_search performance, batch inserts, restart prevention
- Migrate all reads to readerDB with MaxOpenConns(8)
- Gate evolution and briefing refresh
- Recover daemon startup and reconnect
- Deploy via atomic swap and service restart
- Harden cache sync and doc sync updates
- Include subagent sessions in extraction pipeline
- Retain embeddings for superseded/resolved learnings as alias search paths
- Directive dedup only against other directives, --skill implies client init
- Align config defaults, setup wizard and example.yaml
- Add network-online.target dependency to systemd units
- DocIngestSchema additionalProperties + use SummarizeModelID
- Address code review findings — FTS AND semantics, relative paths, global docs
- Trust protection + dedup for learning supersede/evolution system
- Revert embedding model name — multilingual-e5-small is the actual embedded model
- Add [ID:xxx] prefix to briefing learnings so signal bus can track use_count
- Remove stability cap (unbounded growth on use), add universal decay floor 0.1
- Stability grows on use not inject, entity match min-length, precision ramp, exploration bonus
- Add error handling to 5-count RPC handlers, remove dead handleIncrementHits
- Make all V2 schema fields required to stay under 24 optional param limit
- Register set_config/get_config as MCP tools
- Write real thread-ID into archive block instead of placeholder
- Error swallowing, dead code removal, global state races
- Gzip header ordering + identity injection dead code
- SessionStart hook — fix missing matcher + re-enable registration in setup
- Align behavioral dimensions with real DB trait keys + add tests
- Silent return for non-Bash/Edit/Write PreToolUse hooks
- Cache_control TTL ordering — ephemeral without explicit ttl field
- Sawtooth threshold harmonization + overhead measurement
- Stubify force-stubs tool_result auch in Protected Tail
- Daemon lifecycle, extraction and embedding improvements
- Clarify hook-failure output with [YesMem Assist] prefix
- Supersede trust bypass + briefing refresh 20→5
- Filter greedy associative queries + add embedding to setup template
- Update subagent tests for new isSubagent signature and boundary
- Fix associative context injection
- IsSubagent by entrypoint, briefing needsReserialization, MEMORY.md cleanup
- IsSubagent now detects by entrypoint/model instead of message count
- Ensure system-block injections trigger re-serialization
- Add briefing load debug logs for troubleshooting
- Retry briefing load on daemon startup race condition
- Flaky TestHandleResolveByText — extract OnMutation callback
- Importance in retrieval scoring + dynamic cross-project categories
- Code review findings — ResolveBatch valid_until, trust base term, rune-safe truncate
- Address code review issues — resolveModelID, budget wrapping, shadowing
- Remove embeddings on supersede via callback
- Backfill valid_until at daemon startup regardless of embedding config
- One-time cleanup of orphaned embeddings from superseded learnings
- Sync vector store on all supersede paths + add temporal validity
- Unify token estimation in proxy pipeline
- Pressure-based decay bugs and test coverage
- Use configured model for cost estimation, translate wizard strings to EN
- Improve log message accuracy for non-stubbed requests
- Add nil check for LLM client with clear error message
- Use threadID (from request content) instead of empty session header in proxy logs
- Setup also adds ANTHROPIC_BASE_URL to ~/.profile
- Simplify micro-reminder, add hybrid_search hint
- Code quality — race conditions, memory leaks, UTF-8, performance
- Remove narrative consolidation, dedup milestones, compact prompt
- Correct indexed/skipped counter in IndexAll logging
- Milestones chronologically sorted + truncate at sentence boundary
- Narrative learnings inherit flavor + intensity from session peers
- NeedsReindex always returned true due to nanosecond precision loss
- Only inject project pulse for sessions < 48h old
- Embedding init parallel to extraction, not blocking on it
- Remove embeddings when learnings are resolved/superseded
- Address code review findings (C1 + I1-I5)
- 3 workers with busy_timeout, progress logging with ETA
- Address code review findings (6 fixes)
- Remove generic preferences.* traits, fix --limit flag parsing
- Bootstrap-persona with --force, --limit, progress logging
- Resolve project from subdirectories — walk up parent dirs on no match
- Cleanup markdown-wrapped JSON/YAML blobs from learnings
- Profile generation — protect manual profiles, fix freshness check, enrich input
- Narrative cap 500→2000 chars (~500 tokens)
- Cleanup junk learnings + input validation
- Structured outputs, parallel workers, briefing hookSpecificOutput
- Phased extraction pipeline + debounced file watcher
- Only create backup if none exists yet
- Backup ~/.claude.json before modifying
- Wrap non-JSON LLM responses as unfinished learnings instead of discarding
- Skip non-JSON LLM responses before parsing
- Harden extraction prompt against code-completion confusion
- 60s backoff on rate limit within chunk loop
- 10s pause between chunks to avoid rate limits
- Increase API delay to 15s to avoid competing with Claude Code
- Filter open work to current project only
- Exponential backoff on rate limits during extraction
- Add limit (default 30) to get_learnings MCP handler
- Deduplicate get_learnings MCP output
- Aggressive daemon replacement via /proc scan
- 64MB parser buffer + read API key from Claude Code config
- Smart session loading with summary/paging, response truncation
- MCP server uses mcp.NewTool() builder API

### Performance

- Reduce daemon RSS ~52% — SSE singleton, weight release, parser buffers
- Preserve tools in fork requests for cache prefix compatibility
- Keepalive pings 12→6 + statusline refreshInterval
- Slim down MCP tool descriptions — 24863 to 16836 chars (-32%)
- Default to ephemeral cache TTL (5min) instead of 1h
- Force tool rotation in agentic benchmark mode
- Batch extraction cycle replaces immediate settled-trigger
- Extraction config defaults + distillation batching
- Briefing + extraction cost optimizations (~$11.50/day savings)
- Increase anticipated_queries from 3 to 5 per learning
- Fix FTS5 query plan + index thinking blocks
- Add missing composite indices for learnings + sessions
- Composite index + LIMIT for deep_search enrichment
- Restrict to active vectors only, IVF 5,291 instead of 39,425
- Cap k-means to k=100, 5 iterations for faster rebuilds
- Increase default nprobe from 5 to 15
- Batch=16, nice -n 19, throttle 500ms
- Add throttle to embed-learnings to keep search responsive
- Signal bus — Haiku statt Sonnet + Instruction-Fixes
- Budget-based compression pipeline
- Aggressive collapse — remove Stubify floor, lower minCollapseMessages
- Strip noise from extraction input + respect two-pass mode in re-extract
- Kompaktes Search-Output für hybrid_search, search, deep_search
- Switch associative context from vector_search to hybrid_search
- Pivots every 10 requests + filter system-reminder noise from decisions
- Content-based token estimation instead of JSON tokenization
- Reduce learning limits — 3/category briefing, 10 total MEMORY.md
- Bulk evolution — 1 LLM call per category instead of 2743 individual calls
- Evolution only checks last 24h learnings, not all 2743
- Socket-first startup + auto-reconnect for MCP resilience

### Reverted

- Remove eager tool-result stubbing (breaks cache anchors)
- Revert "feat(scoring): session-correction-rate als Quality-Signal (2e)"
- Revert "feat(storage): switch SQLite driver from modernc to ncruces/go-sqlite3"
- Remove dynamic persona dimensions + self-reflection

### Documentation

- Add Capability Memory spec and Phase 2 implementation plan
- Add pulse/recap feature to Features.md and README.md
- Restore cache keepalive cost analysis lost in overbroad revert
- Add eager tool-result stubbing to Features.md, README, and landing page
- Eager tool-result stubbing implementation plan
- Add community files — issue templates, contributing guide, code of conduct
- README overhaul — badges, comparison table, context screenshot
- Add plan for cache-status via daemon-RPC
- Add yesmem.io landing page with GitHub Pages deployment
- Add Windows WSL2 install note to README
- Update README — sponsor section, production date correction
- Rewrite README for launch — adaptive context window pitch, benefit-oriented features, install script
- Update Features.md with 7 undocumented features from recent commits
- Add cost analyses + learning lineage plan, ignore .codex
- Update Features.md with 12 missing features from recent commits
- Add Opus benchmark results and retrieval ceiling finding
- Add LoCoMo benchmark methodology and results
- Add defense-in-depth security plan
- Rate-limit tracking implementation plan (8 tasks, TDD)
- Rate-limit tracking design spec (v1.0.1)
- Clarify proxy is optional, add API key vs subscription guidance
- Add project audit report 2026-04-01
- Add typed association and loop detector plan/spec documents
- Update README with current feature set and differentiators
- Add typed association graph, graph augmentation, contradiction warning to FEATURES.md
- Schwarm plan-execution mode implementation plan
- Add implementation plans, specs + uninstall test
- Heartbeat consolidation implementation plan
- Features.md — Subagent Detection + Docs-Available Hint Injection
- Claude Code Source Tiefenanalyse — Architektur, Features, Competitive Intelligence
- Plan-based docs_search reminder spec and implementation plan
- Features.md — RST-Support und Rich Metadata Extraction dokumentiert
- Features.md vollständiges Audit gegen Codebase
- Add/update implementation plans (doc-injection, plan-auto-activation, cache-fix)
- Agent telemetry implementation plan
- Persistent orchestrator design spec
- Features.md — agent orchestrator, multi-backend, scratchpad, /swarm
- Features.md — OpenAI parity pipeline, configurable pricing, token thresholds, Codex parser
- OpenAI extraction integration plans
- OpenAI parity plans + agent dashboard spec
- Add planning docs — migration, locomo benchmark, auto-update design
- Update Features.md — batch extraction cycle, distillation batching, prefiltered default
- Add auto-update feature to Features.md
- Update Features.md — user profile synthesis, LoCoMo benchmark, briefing order fix
- Update master roadmap — B2, E5a, C1, D4, D6 erledigt, Phase K eingeführt
- Add corrected LoCoMo dataset with fixed gold answers
- Plan for agentic benchmark mode
- Update benchmark results — 0.62 with gpt-5.4 + corrected dataset
- LoCoMo benchmark results — 0.58 matching Zep baseline
- Rewrite README for public release
- Upgrade B2 to Bidirectional Memory, retire E3 Zeigarnik
- Update 7 sections for session fixes and redesigns
- Expand hook-check section with full technical details
- Add query_facts, expand_context, hub dampening, plan persistence, proxy usage tracking
- Add query_facts, expand_context, plan tools to all tool hints
- Add Rules Re-Injection + Plan Re-Injection sections
- Comprehensive Features.md update
- B1 Metamemory marked as done in master roadmap
- B2 Prospective Memory → nice-to-have (60% implicit coverage sufficient)
- Add Recurrence Detection (15c) to Features.md + update roadmap
- Update grand synthesis + master roadmap status
- Remove planned phases from Features.md — only document what's built
- Add Sleep Consolidation section (15b) to Features.md
- Update embedding section — SSE model replaces ONNX
- Update Multi-Agent Communication section with proxy-based dialog
- Add multi-agent communication & memory safety section
- Multi-agent memory safety + n:n scratchpad plan
- Update for messages.db separation + MEMORY.md simplification
- Add fixation detection, skill rework, doc context
- Update with archive block v2, evolution fixes, pins, skills, gap-review
- Add bookmark analogy to pin tool description
- Update FEATURES.md for SSE provider + missing CLI commands
- Add missing commands to help output
- Add HTTP API feature to Features.md
- Add backup and migrate-project to CLI commands table
- Update FEATURES.md for static embeddings + IVF
- Update FEATURES.md with API health gate, token tracking, content sanitization
- Reorganize plans, move completed to erledigt/
- Add NewFeatures.md — functional summary of last 40 commits
- Fix FEATURES.md categories — distinguish extraction vs remember vs daemon sources
- Fix FEATURES.md — embedding model, 10 categories, precisionFactor range
- Update master roadmap with 5-count scoring, Ebbinghaus decay, contextual scoring status
- Update FEATURES.md + Grand Synthesis roadmap with 5-count scoring status
- Update FEATURES.md — per-stage model config, content-aware truncation, pre-dedup, prompt caching
- Add skill integration phase 1 implementation plan
- Usage_deflation_factor im Setup-Template dokumentieren
- Sawtooth nice-to-haves spec + roadmap update
- B0 Recurrence-Detection Plan aktualisiert und gemergt
- B0 Recurrence-Detection Implementierungsplan
- Roadmap aufgeräumt — erledigte Phasen gekürzt
- Roadmap + Completed aktualisiert (Stand 2026-03-11)
- Signal reflection plan + remove obsolete injection-architecture-v2
- Recurrence-Detection plan (Phase B0) + Roadmap update
- Signal Bus implementation plan (Phase A+)
- Roadmap update — Harmonics mapping, bug fixes, A2 complete
- Update FEATURES.md with memory utilization improvements
- Update master roadmap + archive injection-arch-v2 plan
- Update FEATURES.md for Injection Arch v2 + FTS5 hybrid search
- Update master roadmap — add Injection Arch v2, hook-assist to completed, fix bug descriptions
- Comprehensive FEATURES.md update
- Move completed mind plans to erledigt/done/
- Move 3 completed mind plans to done/
- Move 16 completed plans to erledigt/
- Move hook-assist plans to erledigt/, update roadmap
- Move completed plans to erledigt/, update master roadmap
- Add two-pass extraction plans, roadmap update, and design docs
- Add roadmap plans and update grand-synthesis
- Add research docs, plan documents, and hook-assist status update
- Hook-assist implementation plan — 7 tasks with TDD steps
- Hook-assist design — deep search bei Fehlern + idle counter
- Add visualization diagrams to stub-compaction plan
- Plan for stub-compaction — adaptiver Decay + Compacted Blocks
- Phase 3 expanded — hysterese, idempotenz, bypass, localhost, budget manager
- Infinite-thread phase 3 plan — ops, health, usage logging, graceful restart
- Infinite-thread phase 2 plan — stub intelligence, re-expansion, cleanup
- Update FEATURES.md with infinite-thread proxy, hybrid search, session recovery
- Implementation plan for relationship primer
- Design for relationship primer in persona directive
- Add emotional memory, pivot moments, decay, CLI commands to FEATURES.md
- Lücken-Bewusstsein implementation plan — 4 TDD tasks
- Lücken-Bewusstsein design — gap awareness section for briefing
- Mark Phase A + B as done, Phase A.5 as next
- Konkurrenzvergleich aktualisiert + Bleve-Design-Plan archiviert
- Design docs — persona engine, immersive narratives, vision overview
- Session narratives design — erinnern statt lesen
- Detailed configuration reference in README
- Comprehensive README with architecture, tools, config, categories

### Testing

- Add cache keepalive integration test
- E2E test for forked agent flow — build+gate+prompt+parse
- Add TestCrashRecovery tests — TDD red phase
- End-to-end OpenAI adapter integration test
- Verify OpenAI translation compatibility with compression pipeline
- Integration test for full update flow
- Add tests for query clustering, blob conversion, affinity scoring
- Trust score + supersede resistance — 7 tests
- Temporal validity layer — 6 tests covering all new features
- Decay pinned paths — pin boost, suffix match, unpinned normal
- Add comprehensive tests for generator and storage


[Unreleased]: https://github.com/carsteneu/yesmem/compare/v1.1.34...HEAD
[1.1.34]: https://github.com/carsteneu/yesmem/compare/v1.1.33...v1.1.34
[1.1.33]: https://github.com/carsteneu/yesmem/compare/v1.1.32...v1.1.33
[1.1.32]: https://github.com/carsteneu/yesmem/compare/v1.1.28...v1.1.32
[1.1.28]: https://github.com/carsteneu/yesmem/compare/v1.1.27...v1.1.28
[1.1.27]: https://github.com/carsteneu/yesmem/compare/v1.1.26...v1.1.27
[1.1.26]: https://github.com/carsteneu/yesmem/compare/v1.1.0...v1.1.26
[1.1.0]: https://github.com/carsteneu/yesmem/compare/v1.0.3...v1.1.0
[1.0.3]: https://github.com/carsteneu/yesmem/compare/v1.0.2...v1.0.3
[1.0.2]: https://github.com/carsteneu/yesmem/compare/v1.0.1...v1.0.2
[1.0.1]: https://github.com/carsteneu/yesmem/compare/v1.0.0...v1.0.1
[1.0.0]: https://github.com/carsteneu/yesmem/compare/safety-before-rebase-2026-04-16...v1.0.0
[safety-before-rebase-2026-04-16]: https://github.com/carsteneu/yesmem/releases/tag/safety-before-rebase-2026-04-16
