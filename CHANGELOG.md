# Changelog

All notable changes to YesMem are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- Add done-verify state machine (Layer 3)
- Add resolveSpawnModel helper for bare model name resolution
- Idle-self-check state machine
- Dead-agent-detection for yesloop agents
- SKILL.md v3 — automated DONE-guard + DISMISSAL-PATTERN + findings-table
- Deterministic regex DONE-guard + heartbeat integration
- Detection-first setup wizard + fix 4 template bugs in generateConfig
- SKILL.md v2 — artifact-prescription + structured phase blocks
- Staged skill suggestion on user messages
- Config.yaml read-write for set_config/get_config + CLI

### Fixed

- Idle relay v3 — prove each phase + explicit Phase 5 review mandate
- Idle relay — prove first, then mark PROVEN
- Filter opencode free-tier models — exclude paid rebranded
- Show only opencode free-tier + providers with API key
- Support free-tier OpenCode users without auth.json
- HasOpenCodeConfig only requires opencode.json + multi-agent user choice
- Replace MergeDefaults with MigrateConfig, add missing migrations
- Deepseek token threshold 500k→600k, add glm-5.2 500k
- Complete template — all sections, fields, and defaults
- Unique agent IDs never recycled
- Include coding-variant providers in model map
- Deterministic provider dedup + document coding-variant assumption
- Wire resolveSpawnModel into spawnAgentProcess
- EnsureV1 for base_url, dynamic summarize_model, complete_provider
- EN-only relay messages and markers
- Address 3 Important code review findings
- YAML indentation bugs in generateConfig + add E2E tests
- Remove double [skill-eval] prefix in BuildMeta
- Backup config.yaml before MigrateConfig and MergeDefaults writes
- Fix resolveGuardConfig provider selection + skill_nudge dedup
- Make Phase 5 Cold Review mandatory with scratchpad evidence
- Proxy_state dual-write + type coercion for config.yaml writes

### Documentation

- Promote cross-backend USP + re-evaluate yesloopultra
- Add YesLoop vs Claude Code /loop and /goal comparison
- Rename tagline 'for Claude Code' → 'for coding agents'
- Document 6-layer phase-completion guarantee + feature entry
- Update CHANGELOG and add yesloop documentation

## [2.1.21] - 2026-06-19

### Changed

- Add .yesmem/ for agent-local temp files
- Rewrite 'Types of memories' as proper Markdown

### Fixed

- PID-check detectHungAgents freeze path (#73191)
- Check PID before max_runtime freeze (#73505)
- Remove SYSTEM.md from excludes — it's source code, not private config

## [2.1.20] - 2026-06-19

### Added

- Store per-message model provenance from parser and opencode scanner

### Fixed

- Store created_at as RFC3339 with timezone offset
- Sharpen MCP tool descriptions + add metadata-sparse warning
- Raise default max_runtime from 30min to 48h
- Resolve model_used provenance via proxy_state + YESMEM_MODEL_ID env

## [2.1.19] - 2026-06-18

### Fixed

- Restore legitimate test fixtures to SCAN_ALLOWLIST
- Harden sync-public.sh + remove hardcoded private paths
- Exclude scripts/extract_skills.ts from public sync

## [2.1.18] - 2026-06-18

### Added

- Add propagated-claim honesty + yesloop scratchpad verification
- Add second-order + assumption checklist to Phase 5 REVIEW
- Add --files flag for syncing only specific file paths
- Per-model system prompt templates via model_templates map
- Per-agent opencode session ID tracking for precise resume
- Include codex_session_id in agent response
- Per-agent codex session ID tracking for precise resume
- Per-tool MCP approvals for all 70 yesmem tools
- Unify PTY inject for all backends + resume for codex/opencode
- Auto-generate code fingerprint from entity file paths at remember()
- Two-stage Phase 5 REVIEW — Self-Review + Cold Review via task()
- Add REVIEW→VERIFY feedback loop to pipeline
- Native Go daemon tick for auto-trigger staleness scanning
- Add reddit_fetch bundled skill (v12, HTML scraping docs)
- Reddit_fetch v4 — old.reddit.com HTML scraping statt gebrochener .json API

### Changed

- Remove hardcoded gotcha #66986 reference from SKILL.md

### Fixed

- Rename reddit_fetch → reddit, sync with actual cap name
- Add pre-sync untracked-files guard
- Resolve zai-coding-plan routing through auto-discovery + /v1 path stripping
- Inject metamemory block post-refine so it survives LLM refinement
- Support provider/model format in agent model param
- Neutral spawn boilerplate — no anti-planning hack
- Add explicit anti-planning directive to spawn boilerplate
- Truly minimal SYSTEM_CODEX.md — 5 lines, no directives
- Backup config.yaml before overwriting during setup
- Soften spawn boilerplate + remove memory-first from SYSTEM_CODEX.md
- Two-connection PTY inject to prevent bracketed-paste swallowing \r
- Use SIGTERM for codex, not inject-socket Ctrl+C
- Send Ctrl+C for codex backend (not /exit\r)
- Inject OPENAI_API_KEY from ~/.codex/auth.json for codex spawn
- Remove --full-auto from codex args (not valid in codex 0.140.0) + NVM fallback for codex binary resolution
- MCP tools + SYSTEM.md injection + reddit bundle v5
- Remove misleading cost benchmark from cold review stage
- Review fixes — dynamic project discovery, penalty harmonization, bounded wait
- Reap terminal process to prevent zombie accumulation
- Sync reddit_fetch bundle to v4 (HTML scraping, auto_active)
- Wire prompt_fable through legacy config path

### Documentation

- Update scratchpad read description for scoped reads

### Testing

- Add tests for enhanced staleness decisions and penalty helper

## [2.1.17] - 2026-06-12

### Added

- Agent stream-activity tracking
- Add anthropic provider to opencode.json defaults
- Config option agents.default_backend replaces PATH-based default
- Hybrid_search date pre-filtering for vector and AQ lanes

### Changed

- Add --help/ to .gitignore

### Fixed

- Implement parentSubagentCounts, fix subagent_streams for parents without own stream

### Documentation

- Add hierarchical autonomous orchestration design

### Testing

- Remove --pure flag expectation from opencode stdin args

## [2.1.16] - 2026-06-10

### Changed

- Remove debug logging, clean up

## [2.1.15] - 2026-06-10

### Fixed

- Restore OPENCODE_DISABLE_DEFAULT_PLUGINS=true — needed for recipe

## [2.1.14] - 2026-06-10

### Fixed

- Remove OPENCODE_DISABLE_DEFAULT_PLUGINS — MCP disabled is enough

## [2.1.13] - 2026-06-10

### Fixed

- Proxy routing + remove NO TOOLS prefix that conflicted with SYSTEM.md

## [2.1.12] - 2026-06-10

### Fixed

- Route opencode direct to DeepSeek API, bypass proxy injection

## [2.1.11] - 2026-06-10

### Fixed

- FilterEnv ANTHROPIC_BASE_URL instead of empty override

## [2.1.10] - 2026-06-10

## [2.1.9] - 2026-06-10

### Added

- Dual-provider arch — extraction via API, llm_complete via opencode
- Yesloop — worktree guardrail (⛔ prevent working in main)
- Yesloop — drift detection and convergence gate guardrails

### Changed

- Modernc.org/sqlite v1.48.2 -> v1.52.0 (WAL reader-starvation suspect)
- Gitignore .codex as file (not dir), remove it
- Gitignore dev artifacts (codex, bun, asciinema, PACKAGE.md)
- Update CHANGELOG.md
- Update CHANGELOG.md
- Add untracked yesdocs files

### Fixed

- Inherit full env instead of filterEnv for opencode subprocess
- Explicitly unset ANTHROPIC_BASE_URL in opencode subprocess env
- Stdin close without delay + provider-aware client cache
- Session header for openai_compatible llm_complete calls
- Deep_search date bounds bypass recency path
- Llm_complete via proxy — client-nil guard + LLMConfig.OpenAIBaseURL
- Yesloop — remove xdotool reference (doesn't work)
- Yesloop — add check_messages polling for inter-agent comm
- Simplify PTY injection — uniform 15s sleep before inject
- Increase PTY injection delays for opencode TUI

### Performance

- Deep_search date windows via rowid-bounded FTS scan

## [2.1.8] - 2026-06-09

### Added

- Yesloop — always spawn in git worktree for isolation
- Add yesloop — autonomous task loop skill

### Fixed

- Yesloop — subagent identity, whoami, completion protocol
- Yesloop — PR only by default, --merge flag for auto-merge
- Yesloop — PTY-first startup flow with relay backup
- Yesloop — add self-directed research and PTY kick handling
- Add nil guard for formatter in proxyCallFormat

## [2.1.7] - 2026-06-08

### Added

- Add code style matching and outcome reporting rules to all three SYSTEM.md copies
- Add opencode agent backend with PTY prompt injection

### Fixed

- ResolveProjectDir — check project parameter before _cwd

## [2.1.6] - 2026-05-29

### Added

- Add attribution field, fact category, and flavor ground
- Inject associative context in OpenAI path via TimestampStore freeze-replay
- Add backend field to scheduled jobs
- Auto-discover DeepSeek API key from auth.json + default to openai_compatible
- Prompt for DeepSeek/OpenAI API key when model requires it
- Gate think_reminder and OpenWorkRemind with per-model cooldowns

### Changed

- Sync CHANGELOG and reddit launch title tweaks
- Add mcp-tools-reference.md to public sync allowlist

### Fixed

- Use --window instead of --wait for gnome-terminal
- Opencode TUI spawn — thanks @memyselfandidev (PR #53)
- Load Backend field when restoring scheduled jobs from DB
- Update tests for Chat Completions routing
- Review corrections — error handling and dead param cleanup
- Review corrections — flavor guard and embedding SQL
- Add missing normalizeOpenAIResponsesURL wrapper
- Filter role:system messages from Anthropic messages array
- Remove auditCapExecution — stop logging cap calls as learnings
- Surface association neighbors bidirectionally
- Prevent nil formatter panic in proxyCallFormat
- Prevent nil formatter panic in proxyCallFormat
- Strip role:system from messages array, remove briefing-hook additionalContext
- Remove redundant ALTER TABLE embedding_vector from hybrid origin test
- FormatSignature used 'method' prefix — broke extractNameFromSig and lost all Method nodes from search_code_index
- Add Chat Completions support to OpenAIClient for openai_compatible provider
- Inject 'no tools' system prompt for opencode llm_complete calls
- Use full binary path in opencode MCP command
- Add embedding_vector to CREATE TABLE learnings for fresh installs
- Resolve OpenCode session identity via proxy-persisted active_session_opencode
- Increase yesmem MCP timeout 30s→60s for LLM-heavy caps
- Suppress MCP tools in opencode subprocess via OPENCODE_CONFIG_CONTENT
- Add cap execution timeout + YESMEM_SOCK for REPL runtime
- Add llm() polyfill to REPL runtime + shQuote fix + reddit_search after pagination

### Testing

- Add regression tests for formatSignature, extractNameFromSig, and BuildFromScanResult with Methods

## [2.1.5] - 2026-05-27

### Changed

- Also exclude SYSTEM.md from public repo sync
- Exclude RULES.md from public repo sync
- Regenerate CHANGELOG.md (26 release sections)

### Documentation

- Tighten title, fix dialog spacing and comma splice
- Move AI-agent reading-path block before The Experience, smooth em-dash gaps
- Final README polish — fix grammar, remove filler, close sections
- Replace em-dashes with standard punctuation (colons, periods, commas)
- Restructure README — continuity-first narrative (Codex draft)
- Remove 'Implementation Status/Done' checklist from CapFeatures.md
- Remove deprecated items + open issues from feature docs — verified against code
- Remove temporal annotations — present tense, no dates, no commit hashes
- Fix i18n language default claims (German → English)
- Reddit launch draft — add 'Try it: point your AI agent at the repo' CTA
- AI agent orientation rewritten — direct address, numbered path, competitor anchors
- 'Who builds this' — CCM19 funding story ersetzt Built by/Sponsor
- Temporal-Score-Fußnote — Kategorie-Einordnung (diegetic vs coding)

## [2.1.4] - 2026-05-26

### Changed

- Add rule 32 — block PR merge via CLI/API

### Documentation

- BENCHMARK.md — Community-Fix-Quelle, Sample-Größen-Fußnoten, Seed-Dokumentation

## [2.1.3] - 2026-05-26

### Documentation

- Reorder sections for clearer narrative flow
- Expand How YesMem Differs table from 8 to 16 rows, add Procedural memory differentiator
- Rewrite README from Claude-only to multi-platform (Claude Code, OpenCode, Codex and more)
- Add AI agent orientation hints to README and CLAUDE.md, update CHANGELOG and HN draft

## [2.1.2] - 2026-05-22

### Added

- Pre-DeepSeek pattern match for destructive bash
- SQLite cooldown + downgrade BLOCK for non-authorized tools
- Per-skill cooldown for guard suggestions (10min TTL)
- Self-heal hook-guard matcher on yesmem update
- Extend hook-guard to REPL tool calls
- Add Anthropic API fallback for hook-guard
- Add hook-guard with DeepSeek evaluation, config, and auto-register

### Fixed

- Exclude .codex and PACKAGE.md from public sync
- Emit guard SUGGEST/BLOCK in Claude Code hookSpecificOutput schema
- Fix defer-in-loop leak, silent errors, code fence parsing
- Move BLOCKED_PATTERNS and scan_text before first use
- Remove premature scan_text call before function definition
- Add check_open_pr merged-branch detection

### Reverted

- Revert "fix(sync): remove premature scan_text call before function definition"

### Testing

- Add guard tests for config resolution and API evaluation

## [2.1.1] - 2026-05-22

### Added

- Add opencode as third provider option in setup wizard
- Shared SYSTEM.md template for OpenCode + Claude Code pipelines
- Add SYSTEM.md with memory depth layers to repo
- Hybrid_search nudge on first user message

### Changed

- Update CHANGELOG
- Add auto_configure_providers field (default: true)
- Update .gitignore, timestamp hint wording

### Fixed

- Add tool-context filtering for gotcha injection — closes #29
- Use runtime.GOOS instead of hardcoded linux in TestFillSystemTemplateOld
- Add missing limit parameter to GetActiveLearnings calls
- Restore max_context.png to docs/images/ (lost in yesdocs split)
- Restore missing YesMemRPC import in index.ts
- Per-model token threshold override + DeepSeek 500k default
- Add deepseek pricing entries, raise quality budget to 0
- Per-session gotcha cap — suppress after 3 injections
- Use mcp__yesmem__* wildcard instead of 65 individual tool permissions
- Sync user edits — re-read directive positioning, wording refinements
- Single nudge line, no rotation
- Rotate 3 nudge variants based on system reminder text (DE+EN)
- Use experimental.chat.messages.transform to prepend nudge to user messages
- Use input.sessionID not input.session.id in chat.params hook
- Switch to chat.params hook, message.updated never fired
- ...ir spread overwrote composed message.updated hook
- Fix fork SSE: accumulate delta content from all chunks, not just last
- Fix fork SSE parsing: try plain JSON first, fall back to SSE extraction
- Skip instead of hint for doc targets, DeepSeek ignored the hint
- Fix fork cache: remove stream:false for DeepSeek prefix cache sharing
- Doc-target hint via context note, no more false code-nav skill suggestions
- Grep/glob nicht mehr blocken, nur read per get_file_symbols

### Performance

- Add 5 missing indices + 3 PRAGMA optimizations (cache_size, mmap_size, temp_store)

### Documentation

- Convert README features to table format for clarity
- Fix README — correct counts (70/64/130), 10k plan interval, remove unverifiable 70% claims, 3 scheduler modes
- April 21-May 4 commit audit — add cap category + cap_<name> origin to memory.md
- Add 7 missing features from commit audit — excludeProjects, claim_and_read, staus/uninstall, guard git context, opencode merge/update, caps_dir
- Commit audit May 5-21 — 7 missing features found, 4 removed, 18 documented
- Update Features.md overview counts (70 tools, 64 commands, 130 rpc, 48 tables)
- Rewrite reference docs — mcp (70 tools), cli (64 commands), rpc (130 methods), db (48 tables + 4 FTS5)
- Fix 58 audit discrepancies across all feature docs + config-reference
- Document opencode plugin update issue (provider-resolution not loading)
- Doc audit findings (58 discrepancies) + rule_guard provider resolution plan
- Renumber feature doc sections consistently per file (1..N)
- Split Features.md into target-group docs (7 feature files + 4 reference docs)
- README model-agnostic language — replace DeepSeek with the model in functional descriptions
- Update README — LLM backend, SYSTEM.md, autoconf, opencode plugin, RULES.md
- Git history 120 commits — May 15-21: fork-cache, SYSTEM.md, provider autoconf
- Expand search exceptions — reflexive, self-contained, trivial
- Sync runtime SYSTEM.md to repo — reflexive-search exception, formatting alignment
- Add SYSTEM.md — canonical system prompt template
- Auto-extracted superpowers plan/spec documents

### Testing

- Replace hardcoded  path with generic path for public sync

## [2.1.0] - 2026-05-18

### Added

- Add timestamps:true for deepseek in default config and setup, fix think_test for MANDATORY
- Add exclude_projects to setup config template and migration
- Add excludeProjects filter to OpencodeScanner
- Add ExcludeProjects config to prevent indexing of noise directories
- Set YESMEM_DAEMON_CHILD=1 in runClaude headless path
- 1h TTL for fileAttempts 2-strike counter
- Add version and unix timestamp to fork log lines
- 2-strike rule — first block with yesmem hint, second allows
- Add code-tools-first rule (31) + skill catalog entry (50) with grep/glob/read triggers
- Wire JobDone into scheduler executor callback
- Gate dueJobs with running check to prevent concurrent execution
- Add running map to Scheduler struct
- Filter daemon-internal LLM sessions from scanner backlog
- Inject YESMEM_SOURCE_AGENT=opencode into MCP environment by default
- Add git branch context to prevent false feature-branch suggestions
- Add session.created hook for opencode session identification
- Expand extraction model choices to include DeepSeek + GPT via opencode
- Interactive wizard now asks which model to use for conversations
- Add anthropic provider + model/small_model keys to opencode template
- Show session_id + supersede_reason in formatted output
- Inject briefing as system message with MANDATORY_BRIEFING wrapper
- Install opencode plugin files on update if missing/outdated
- Increase opencode MCP timeout 10s → 30s with auto-upgrade from old default
- Bash SUGGEST-only, MANDATORY CHECK prefix
- RULES.md with guard rules + skill catalog
- Mandatory memory search enforcement in rules 23 + 31
- Authoritative skill injection via chat.message + system.transform
- Make SUGGEST mandatory with directive wording
- TUI toast for guard BLOCK/SUGGEST events
- Visual markers for BLOCK/SUGGEST in output
- Buffer last 5 user messages for guard context
- Add chat.message context to guard evaluation
- Fire guard on all tools, always suggest skills
- Add missing CLAUDE.md rules 25-30
- Add Skills section with activation triggers
- Load rules from DECISIONS.md in project root
- Add rule_guard tool.execute.before hook
- Add skill-check instruction to DeepSeek output discipline
- Shorten think-reminder and skill-eval injects for OpenAI path
- Inject think-reminder, skill-eval, rules via timestamp store
- Add prune=false to opencode compaction settings
- Session-resume support for llm_complete RPC
- Model-aware handleLLMComplete + LLMProvider config
- Llm() polyfill for bash caps + llm-complete CLI + llm_complete RPC
- Bash polyfills (store + llm) + llm_complete RPC
- Unix socket MCP transport (replaces HTTP TCP)
- Configurable caps_dir for runtime-agnostic CAP.md storage
- MCP polyfill layer in bun wrapper for cross-compatible caps
- Inject opencode CAP catalog into OpenAI parity pipeline
- Register execute_cap tool
- Execute_cap handler — sandboxed bun/bash CAP execution
- Embed opencode plugin source in binary, install to ~/.local/share/yesmem/plugins/ during setup
- Merge opencode provider/mcp/compaction settings without overwriting existing config; add opencode uninstall + status
- Preload code graph at session start
- Opencode plugin install — symlink + opencode.json registration + make deploy copy
- Opencode-yesmem hook-plugin — code-nav, failure-learn, auto-resolve, idle-reminder
- [yesmem-code-explore] directive — yesmem tools before shell for code exploration
- Set feature_defaults to all-true — new models get full features by default
- Opencode first-class config, migration, and setup wizard
- Agent-aware footer in rules reminder (CLAUDE.md vs OPENCODE.md)
- Concise think_reminder wording for non-Claude agents
- Full timestamp injection (InjectTimestamps) for opencode parity path
- Associative context diagnostic logging for opencode parity path
- Opencode extraction adaption — schema-injection, prompt-neutralization, extractJSON fix
- GenerateAgentsMd() for opencode project reference
- Multi-agent CLIClient with stdin-pipe for codex/opencode
- Profile-aware wording via Generator.SetSourceAgent()
- Multi-agent session identity via resolveClientSessionID()
- Set SharedPrompt in Default() with agent-neutral injector defaults
- Learnings.source_agent + target_agent provenance columns
- Config-split PromptFlags + EffectivePromptFlags(profile) + review fixes
- Golden test framework + PromptProfile type for multi-agent prompt isolation

### Changed

- Ignore .claude lock files; untrack scheduled_tasks.lock
- Bump index V=5, add ensureIndexed log to code_nav
- Rephrase error — yesmem tools first, retry as fallback
- Remove redundant ts= from [req N ver] format
- Self-identifying format [req N ver ts] for pipeline lines
- Bump index.ts cache key for 1h TTL code_nav reload
- Self-identifying log format [req N v2.0.1-nn ts=U]
- Add .opencode/ to gitignore
- Remove debug eval log, verified convMsgs=6 ctxLen~750-1200
- Consolidate extraction pipeline and storage layer
- Use yesmem daemon RPC for last user message, remove broken hooks
- Address code review — dead code removal, caps.db test, parser fix, benchmark deadline, guard cache
- Remove dead code (guard_tui.ts, synthesizeProjectRules)
- Rename DECISIONS.md → RULES.md
- Remove chat.message/system.transform injection — revert to simple tool.execute.after
- Remove debug logging
- Accumulated changes — sandbox, scheduler, capfile adapter, cmd_worker, json commands
- Remove 124MB dbstats binary from tracking
- Sync remaining working tree changes (learnings search, storage schema, project model, opencode cap exec plan)
- Comment out verbose OPENAI-OUT per-message log lines
- Cap version tracking changes from concurrent session
- Commit leftover changes from concurrent session
- Merge CodeExploration into unified CodeToolsFirst, remove CodeExplore flag
- Regenerate for v2.0.6 release
- Ignore node_modules
- Rename InjectToolPrefs → InjectClaudeToolPrefs

### Fixed

- Exclude research/ directory from public sync
- Add test fixtures with  paths to scan allowlist
- Detect extraction pipeline sessions regardless of message count
- Blacklist dangerous scan paths and fix worktree .git detection
- Balanced-bracket JSON parser + extraction session filter
- Session mapped line also with [req N ver] format
- Restore opencode grep/glob/read section lost in TTL edit
- Restore throw after debug logs
- Compose tool.execute.before/after hooks (spread overwritten)
- Sync dbgLog + extend to grep/glob/read tools, add RULES.md to embed
- Raw-byte body construction + flex-int JSON parsing
- Increase max_tokens 1024→4096, complex tool reasoning overflow
- Byte-prefix cache + min-tokens gate + RecordFailure reset
- Increase max_tokens 256→1024, reasoning consumed budget
- Rules in system msg for prefix-cache, sync dbgLog, json_object format
- Add narrative and gap-review daemon prompts to filter
- Split telegram poll/reply into separate single-responsibility handlers
- Use future-proof model name deepseek-v4-flash instead of deprecated deepseek-chat
- Fix suggestions Map type, add _test.go TDD-compliance note
- Auto-detect OpenCode via OPENCODE=1 env var fallback
- Also skip rule_guard.ts self-evaluation
- Narrow file skip to RULES.md only, evaluate yesmem/worktree files normally
- Remove anthropic provider from template + wizard — proxy-only providers
- Correct reversed api-key condition + add anthropic to uninstall cleanup
- Add convMsgs summary log for guard visibility
- Register_pid persists source_agent, remove premature briefing generation
- RecentConversation context, rulesHash cache, retry, robust parsing
- Always show provider selection, remove forced skip for non-claude models
- Correct get_learnings API params in briefing — task_type → category suffix
- Briefing refinement fallback — dedup validateRefinedOutput + prevent double-arrival
- Fork auth + prompt cleanup + keepalive guards
- Bun.write truncate bug in code_nav + auto_resolve — use read+append like rule_guard
- Use chat.params to capture user message (fires before LLM call)
- Chat.message reads from output.message, not input.message
- Cache ResolveProjectPath once to avoid sub-query deadlock inside row loops
- Remove dead code with latent data races, protect learnings map reads
- Remove orphaned code block blocking plugin load
- Address code review — GetCachedGraph tests, handler reuse, TplMs timing, ReadFile guard
- Use message.updated hook name, remove stale plugin file
- Move session_active_caps to caps.db to avoid SQLITE_BUSY contention
- Normalize fork effort to 'high' to prevent DeepSeek 400 on xhigh
- Increase search deadline 12s→30s, fix telegram-poll 1s→15s
- Allow user-requested commits in Rule 1
- Normalize cache_control TTL to prevent ordering violation on resume
- Append-mode logging + debug tracing for chat.message parts injection
- SUGGEST format includes exact skill name with call-to-action
- LoadRules now includes YAML Skill Catalog section
- Console.* replaced with dbgLog — root cause of overlay
- Replace all console.* with dbgLog file logging
- BLOCK silent inline, toast-only
- Show BLOCK reason in title not body
- Remove JSX pragma from TUI plugin (not needed)
- Add JSX pragma to TUI plugin
- Move TUI plugin to tui.json config
- Defer BLOCK to tool.execute.after instead of throwing
- Throw descriptive BLOCKED reason instead of space
- Lowercase tool names in Sets, add FIRED debug log
- Renumber Session Discipline rules to avoid collision with Skill Catalog
- Revert to throw — output.args mutation ineffective for Write
- Use tool.execute.after for clean BLOCKED output
- Remove appendPrompt, keep space-only throw
- Throw with space, prepend warning via appendPrompt
- Try neutralize Write/Edit via output.args mutation
- Revert to throw with single-line error format
- Neutralize tool instead of throwing on rule violation
- Add toast notification for rule_guard blocks
- Exclude bash, DECISIONS.md edits, internal files from rule_guard
- Prevent MCP recursion deadlock when spawning opencode
- Increase bash executor timeout for llm commands from 120s to 600s
- Isolate opencode subagents in separate DeepSeek cache namespace
- Comment out all DeepSeek/OpenAI proxy injections for cache baseline
- Disable associative/doc context injection for DeepSeek
- Prepend stable injections to all user messages for DeepSeek cache consistency
- Restore all DeepSeek injections via prependToFirstSystem
- Cap daemon GOMAXPROCS=4 to prevent CPU saturation
- DeepSeek cache fragmentation — move stable injections to system[0], remove variable blocks
- Rate-limiter queue time excluded from LLM call timeout
- Rate-limiter, wiki-tick skip, 300s timeout, circuit breaker for timeouts
- Provider routing for fork, keepalive, and message forwarding
- Fork uses quality model, keepalive skips small sessions
- Unwrap RPC envelope in _mcpCall polyfill
- Polyfill cap_blob_put/cap_blob_get via cap_store RPC
- Unify session ID prefix from oa: to opencode:
- Pass cache_control through OpenAI reverse translation for DeepSeek caching
- Get_compacted_stubs and expand_context range mode read from frozen stubs + messages.db full-text lookup
- Case-insensitive command detection (Grep == grep)
- Keep i and role for re-enableable log lines
- Extract only real file paths, convert absolute to relative for RPC
- Also block directory-level grep when dir has indexed files
- Block only when target file is in CBM code graph, not entire project
- Remove debug logging, clean throw-based blocking
- Pass plugin directory to code_nav hook, fix property access
- Try output.block first, throw Error as fallback for grep blocking
- Increase RPC timeout to 20s for slow CBM code scan
- RPC unwrap daemon result wrapper, code_nav retries on index miss
- Remove broken @opencode-ai/sdk Plugin import, use plain function types
- Code_nav checks project index existence once, not per-file symbol lookup
- Detect opencode source agent from path, fix daemon resolveProjectDir for full paths
- ResolveProjectDir accepts absolute paths directly, bypassing project_short lookup
- Parse daemon raw JSON, fix symlink to persistent path
- Correct daemon socket path + plan for setup integration
- Move CodeExplore from shared_prompt to opencode/codex prompt — Claude already has CodeToolsFirst
- Sync migration model_features comments with setup template
- Place model_features under proxy:, add sandbox+secrets to template
- Activate_cap thread_id resolution before first hook fires
- Restore error handling in proxyCallWithThreadID + fall-through prevention
- Wire EffectivePromptFlags into proxy + openai_parity pipelines
- Gate Claude-specific injectors out of OpenAI parity path

### Performance

- Disable DeepSeek thinking mode via thinking:{type:disabled}
- Shorter system prompt to reduce reasoning overhead
- Adaptive deep_search routing — global BM25 for sparse queries
- Decouple FTS5 content fetch + bm25 candidate pool
- Parallelize loadAll queries after learnings phase
- Add phase timing, content-hash write skip, and CodeGraph caching
- Skip pipeline for non-interactive (CLI/extraction) requests

### Reverted

- Remove :sub isolation for opencode requests
- Go back to appendToLastUserMessage pattern
- Remove prependToFirstSystem, restore injectAssociativeContext for all DeepSeek injections

### Documentation

- Implementation plan for internal-session-isolation
- Add Claude Code v2.1.128-140 catchup plan
- Add scheduler running-map implementation plan
- Add AGENTS.md, bash polyfills, code-nav expansion, cap consolidation pattern
- Proxy pipeline plugin migration feasibility
- Root cause analysis for reddit cap cross-compatibility (MCP polyfill needed)
- Execute_cap E2E results and cap architecture notes
- Opencode hook-plugin — code-nav enforcement, failure-learning, auto-resolve, idle-reminder
- Add per-field comments for shared_prompt, model_features gates and viewer_terminal
- Translate CapFeatures.md and caps-vs-skills-rationale.md to English
- Opencode plugin implementation plan (Phase 2)

### Testing

- Add running-gate tests for scheduler dueJobs

## [2.0.6] - 2026-05-05

### Testing

- Align stale assertions with current render text and sandbox auto-install

## [2.0.5] - 2026-05-05

### Fixed

- Cache project path to avoid SQLite single-conn deadlock
- Add all public docs to allowlist, remove stale claude-code-repl exclude

### Documentation

- Add config.yaml and settings.json references, move internal docs to yesdocs

## [2.0.4] - 2026-05-05

### Testing

- Use non-pattern fixture value for generic api-key redaction

## [2.0.3] - 2026-05-05

### Fixed

- Harden security scanner

## [2.0.2] - 2026-05-05

### Fixed

- Add WithStringItems to update_plan array params for codex JSON schema compliance

## [2.0.1] - 2026-05-05

### Fixed

- Resolve job work_dir via project resolver instead of hardcoded dev path

## [2.0.0] - 2026-05-05

### Added

- Inject CLAUDE.md Key Packages descriptions as package page intents
- Link file Package to package page, mention packages.md in codemap
- Add packages.md index + packages/ link in README
- Add package-level aggregation pages (packages/<pkg>.md)
- Name-drop top-5 collapsed packages in codemap footer
- Merge CBM scan files into file index for complete coverage
- Add OpenCode TUI sidebar plugin for yesmem cache status display
- Shrink briefing codemap — activity-sort, top-10, wiki-link
- Per-project worktree support + scan caching
- CBM live-scanner + --scan CLI flag for code graph imports
- Add code-graph integration — package, imports, imported-by per file
- Migrate wiki_export cap to native Go subcommand
- Wiki_export file sessions via learnings join path
- Wiki_export session pages with full learning text, file snippets 140→600 chars
- Enable gojq input(s)/0 via WithInputIter
- Wiki_export v65 → v66 — topic co-occurrence + per-file enrichment
- Interval_seconds without cron + script_name targeting (T7+T8)
- Yesmem json now supports full jq-compat flags (-n, -R, -s, -e, --arg, --argjson) via gojq
- Add yesmem query and json subcommands for read-only DB access
- Refactor yesmem-cap-builder for cap-spec v1.1\n\nReplaces the previous 430-line single-file SKILL.md whose save_cap shape\nwas wrong (handler_repl/handler_bash separate fields) with a 336-line\nquick-start index plus three side-files: recipes.md (six working cap\npatterns including yesmem query/json/cap-blob-put pipes), api-reference.md\n(authoritative shapes for save_cap, get_caps, activate_cap, cap_store\nactions, cap_proposal_decide, plus the yesmem CLI surface), gotchas.md\n(28 entries spanning REPL VM allowlist, sh 30KB wall, sanitize_where,\nschema rules, bundled-cap DB-write-back lifecycle including pre-commit\nversion-sync, jq label/apostrophe quirks, and a 9-item spec-feedback\nsection for the cap-spec repo).\n\nSource distilled from two 4116- and 1068-message sessions via verbatim\nstage-1 extraction; audit trail lives under yesdocs/plans/2026-05-01-\ncap-builder-stage1/.\n\nSide-files are picked up natively by InstallBundledSkills which\niterates every file in the skill directory.
- Reject sandbox=none on scope=project caps
- Per-script sandbox overrides scheduled-job profile
- Add Sandbox field to ScriptMeta with enum validation
- Add per-script sandbox metadata field
- Origin-aware multiplier for trust-weighted scoring
- Tag origin_tool=llm_extracted_session on extracted learnings
- HandleRemember accepts origin param, defaults to user
- Persist and read origin_tool in learnings
- Add OriginTool field to Learning struct
- Add origin_tool column to learnings table
- Add openai, ssh, gpg, ipv4_public, hex_secret pattern kinds
- Wrap LLMClient with SanitizingClient when SecretsSanitization enabled
- SanitizingClient wraps LLMClient with Sanitize-before-after
- Redact bash-job output before persist when SecretsSanitization enabled
- Wire SecretRedactor onto Handler when enabled
- Introduce Sanitizer interface and SecretRedactor with 10 kinds
- FREEZE/RESTORE symmetry + eager-stub memory layer
- Re-enable matched-cap inject via Sawtooth-tail
- T3 decide tool, T4/T5 rate-limits, review fixes, multi-bash filter
- Route substantial diffs to cap_proposed for user approval
- Cap-body diff classifier
- Prompt-flow isolation between claude and codex paths
- Telegram bundle CAP.md staging (cap-spec v1.1, Phase E1)
- ScriptName in scheduler + bundle-cap support (cap-spec v1.1, Phase D)
- Telegram_reply bash cap with claude -p + dynamic timeout for LLM jobs
- Model field on ScheduledJob + --model flag for headless mode
- Bash adapter store() + yesmem store CLI + interval_seconds scheduling
- Persist sandbox profile in storage + MCP schema enum
- Wrap executeHeadless in sandbox via WrapExecArgs
- RunWithProfile + BuildSandboxedCommand + wire into fireJobBash
- Add sandbox profile to config + ScheduledJob + scheduleCreate
- Add SandboxProfile type with none/standard/strict presets
- Headless --resume session tracking + cap actions setup flow
- Heartbeat interval 2s → 1s
- Expose mode, cap_name, auto_correct in schedule tool schema
- Heartbeat-driven bash error processing with auto-correct via Sonnet
- Bash mode validation + fireJobBash with sandbox execution
- ScheduledJobRow.CapName/AutoCorrect + BashJobRun CRUD
- Schema for bash-mode scheduler (cap_name, auto_correct, bash_job_runs)
- Ai-jail sandbox integration with download-on-first-use
- Inject [yesmem-code-tools-first] directive block
- Inject code-tools-first directive via InjectAntDirectives
- Bundled caps deployment in setup + update + hook matchers to .*
- Code-nav hook detects rg/grep/cat in REPL code, not just Bash
- Subagent caps propagation + seen-map dedup fix + conditional adapter JS
- UsesGenericAdapters with word-boundary detection for conditional adapter injection
- Migrate adapter to 3-primitive design (store/web/file)
- Add headless mode via claude -p for lightweight scheduled runs
- Multi-turn workflow sequence detection
- Adapter mapping in activate_cap and save_cap handlers
- Writer converts provider-specific to generic names
- Adapter registry with bidirectional name mapping
- Detect requires from generic adapter calls in script
- Add recurring flag for one-shot jobs that auto-delete after firing
- Add cap scheduler with cron parsing, storage, MCP handlers, and daemon wiring
- REPL pattern noise reduction — deny-list, session budget, threshold 8
- Add simple JS formatter for single-line scripts in CAP.md
- Format SQL with column-per-line indentation in CAP.md
- Export DDL from caps.db, omit derivable schema from CAP.md
- Add CAP.md parser, writer, scanner, and daemon integration
- Capabilities lazy-activation catalog + API-actual threshold
- Add PreToolUse code-nav detection hook
- Narrative-in-briefing, caps-inject fallback, minor fixes
- Trivial-shape filter for REPL pattern detection (Phase 7)
- Activity-gate for REPL-pattern detection (Phase 5)
- REPL-pattern detector stage + config wire-up (Phase 4)
- REPL-pattern handlers + RPC + dismiss MCP tool (Phase 3)
- Repl_pattern_observations table + CRUD (Phase 2)
- Shell-command normalizer + shape-hash (Phase 1)
- Add yesmem-clarify-first directive block — narrow threshold (materially different work only)
- Extend beweislast with mental self-check + delegation-contract with model tier guidance
- Sharpen scope-discipline — bug-surfacing is MANDATORY, not just allowed
- Wire three new directive blocks — beweislast, scope-discipline, delegation-contract
- Translate directive blocks to EN, sharpen output-discipline, add N1/N2/N3 inject functions, switch all inject directives to idempotent UpsertSystemBlockCached, add duplicate-inject regression test
- Wire three directive-injection flags into both pipelines
- Add three directive-injection flags, defaults true
- Add three directive inject functions
- Expose session model via whoami + persist from proxy
- Relax WHERE sanitizer + add pagination (offset/has_more/total)
- Blob-pipe via cap_store for >30KB capability payloads
- Auto_active + MANDATORY inject directive + PTY-submit fix
- Capabilities cache + sawtooth-coupled briefing refresh
- Inject active capabilities as user/assist pair before last user message
- Phase 1 lazy-activation (activate/deactivate)
- Add cap_store — sandboxed capability database with CRUD MCP tool
- Add register_capabilities MCP tool, /build-tool skill, review fixes
- Add briefing capability hints, /build-tool skill, review fixes
- Add Capability Memory handlers, MCP tools, and tests
- Add 'capability' as valid learning category
- Gotcha injection decay + tiered output (top-1 only)
- Config migration for setup and update
- Add skill_eval_inject config toggle
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

- Split index.md into file-tree + learnings.md category index
- Copy loop var in save_cap, clarify legacy-handler merge comment
- Merge GenerateSharedAdapterJS into GenerateAdapterJS with skipStore param, add UsesStoreAdapter
- Remove yesmem-build-tool, refresh yesmem-planning
- Rewrite wiki_export as native runtime:bash (v62)
- Silence sandbox-override log when profiles match
- Enumerate valid sandbox values in error
- Tidy sandbox validation per code review
- Exclude .ai-jail sandbox configs
- Drop dead guard in WriteCapToDisk DDL path
- Retire reddit standalones, stage bundle CAP.md exports + idempotent adapter rename
- Pivot REPL-pattern detection to fork-driven model
- Resync cap_search bundle template
- Replace private paths in test fixtures with generic placeholders
- Remove   .last-sync-hash, require --branch flag in sync-public.sh
- Remove Notes section from parser and writer
- Merge 4 schedule_* tools into single schedule tool with action param
- Update catalog format and auto_active default
- Rename capability→caps across all packages
- BlockText helper with trailing separator for all system blocks
- Sync-public.sh requires --branch flag, auto-generated CHANGELOG
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

- Add file-specific entity matching to gotcha filter — old info gotchas with matching file entities are preserved (per code review)
- Session metadata from learning IDs, exclude non-package dirs
- Remove unreliable LOC from package pages
- Go-only package filter, LOC aggregation, deduped health counts
- Filter non-code paths from file index — vendor, PDFs, erledigt
- Point codemap footer to index.md instead of files/
- Normalize absolute file paths to relative in file index
- Filter foreign worktrees, dot-files, absolute paths from file index
- Move wiki-link to top of codemap block — before package table
- Clarify wiki path encoding in imperative block
- CLI --scan also saves to project_scan cache
- Derive file imports from CALLS edges when IMPORTS is empty
- Save_cap field-merge preserves scripts on metadata-only updates
- Per-cap store() wrapper with capability injection and args stringify
- Align wiki_export source with disk v65
- Version-Guard in WriteCapToDisk — skip overwrite when disk version >= DB version
- Blank first user message content on collapse
- Cap WAL size at 10MB via journal_size_limit pragma
- Expose origin parameter in remember tool schema
- Guard startDaemon under go test to prevent fork bomb
- Emit origin_tool in hybrid_search response so proxy multiplier sees it
- Tighten phone regex to reject ipv4-like dotted strings
- Widen generic_api_key charset to ./+= for base64 tokens
- Broaden bearer_token regex beyond Authorization header
- Wrap SummarizeClient at assignment instead of post-replacement
- Wrap quickstart client+qualityClient for all 6 LLM paths
- Wrap briefingClient with SanitizingClient when enabled
- Redact Command/ErrorMsg, headless output and stderr
- Sanitize SanitizingClient output even on inner error
- Respect since/before in search and deep_search
- Always shift cache breakpoint, including tool_result messages
- Fail closed when sandbox unavailable
- GetCapTableDDL prefix-overlap via cap_store_meta JOIN
- Make adapter rename idempotent via word-boundary check
- Correct ai-jail release asset naming and extract from tarball
- 3-way project filter in GetActiveLearnings (briefing tests)
- Hydrate DatabaseSQL via GetCapTableDDL in WriteCapToDisk
- Spec-compliant CAP.md render and parse
- Parse UNIQUE/PK/NotNull constraints from MCP cap_store create_table params
- API key fallback chain for re-setup
- Code review fixes for bash-mode scheduler
- Inject adapter JS (store/web/file aliases) in proxy caps re-injection
- Set dataDir in test helper to prevent CAP.md artifacts in source tree
- Remove cross-project learnings fallback
- Parse nested pattern envelope in suggestion response
- Use already-constructed meta for WriteCapToDisk instead of re-parsing content string
- Use job-specific section names and pass full ScheduledJob to executor
- Translate all agent prompts to English
- Write task to scratchpad before spawn so agent sees it in briefing
- Pre-spawn stop stale agents, unified 10min idle timeout for all states
- Wrap scheduled prompt with focused task-agent preamble
- Replace max_turns with watchScheduledAgent idle-timeout (10min) + status polling
- Add max_turns=10 to scheduled agent spawn, log errors
- Pass project+work_dir to spawn, relay prompt with confirmation
- Quote description in YAML frontmatter to handle colons and special chars
- Individual MCP permissions + memory-first recall reminder
- Remove OR-clause from frozen-stub invalidation
- Inject pattern-suggestion into last user message (Phase 6 cache fix)
- Resolve thread_id via _caller_pid fallback in capability handlers
- Cap_store upsert preserves created_at (#53149)
- Key briefing cache by project to prevent cross-project leak
- Register_capabilities emits 4 positional args to match REPL signature
- Address pre-merge code review findings for Capability Memory
- Exclude capabilities from evolution pipeline, clean embedding text
- Use projectKey() for Code Map header in worktrees
- Resolve git worktree HEAD and project key correctly
- CLI client robustness for subscription installs
- Replace LFS pointer with real sse_dyt_512d.bin binary (6KB)
- Align prompt_rewrite test inputs with updated CC target strings
- Add pre-modification dump, update rewrite targets for CC ~2.1.117
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

- Merged context redundancy analysis with implementation decisions and provenance table
- Drop sandbox prose section from 1.0-copy
- Opencode proxy and injection integration plan
- Verify and correct opencode-integration implementation plan
- Briefing codemap shrink follow-up to wiki-render
- Swap wiki-export-level1-enrichment for wiki-render-go-rewrite
- Cap consolidation pattern + sandbox field spec note
- Sync against main + add capabilities/sanitize/sandbox sections
- Add opencode source integration + wiki-export L1 enrichment plans
- Add DiD-roadmap, learnings-wiki-export, per-cap-sandbox; refresh sanitize-followups
- Update capability-memory design notes
- Add database schema reference for the four SQLite stores
- Document set_plan trigger conditions in MCP and coding-discipline injection
- Add cap-system hardening roadmap (T1, T3, T8)
- Add cap-builder knowledge audit trail\n\nTwo-stage workflow for distilling cap-building knowledge from past\nsessions into the yesmem-cap-builder skill.\n\nStage 1 (verbatim extraction) under cap-builder-stage1/:\n  session-bb37bd60.md (517 lines, full coverage 0..1067)\n  session-cc0ba29d.md (733 lines, coverage 0..1599)\n  session-cc0ba29d-part2.md (1003 lines, coverage 1600..4115)\n  README.md as index and hand-off\n\nStage 2 (synthesised proposal) under cap-builder-stage1/stage2/:\n  SKILL.md, recipes.md, api-reference.md, gotchas.md\n  Snapshot of the proposal before patches and live take-over.\n\nKept under yesdocs/plans/ rather than discarded so the chain from\nsession quote to skill paragraph stays auditable; future revisions\ncan re-run stage 1 against new sessions and diff against this\nbaseline.
- Note why project-scope guard includes script name directly
- Note B8 skip per audit grep result
- Audit trust-multiplier locations and remember touch-points
- Document SanitizingClient decorator-order contract
- Clarify AllowedExceptions full-match semantics + add config example
- Add Plan B+F implementation plan for source integrity and sanitize followups
- Post-review hardening section for sanitization integration
- Mark Defense-in-Depth status (verified 2026-04-29)
- CC 2.1.119-2.1.123 feature adoption plan
- Add system/cache-cycle.md — vollstaendige Cache-Zyklus-Architektur
- Bash-mode-scheduler audit + auto-correct-hardening plan
- Add plans and analyses from 2026-04-24 (private, excluded from public sync)
- Dead-target-detection + cap-suggestion-v2 plans
- Remove obsolete telegram adapter plan and spec
- Update Features.md and README.md for recent development
- Add JobsFeature.md with full scheduler documentation
- Bash-mode scheduler implementation plan
- Minor updates to CHANGELOG, reddit_fetch CAP, build-tool SKILL
- Update CapFeatures.md — noise reduction, workflow detection, open items audit
- Update CapFeatures.md adapter section to 3-primitives design
- Translate scheduler section to English
- Resolve stale items in CapFeatures.md (blob-pipe, naming, open issues)
- Update CapFeatures.md with adapter layer, resolve stale items
- Add CAP.md file format section to CapFeatures.md
- Add yesmem-directive-blocks plan
- Yesmem-build-tool — patterns from session bb1ded28
- Yesmem-build-tool — 4 fixes from session 63ae4565 RED-test
- Cap_store analysis system — architecture + 8 examples
- Remove stale Bleve reference, update vector store description
- Restructure Differentiators into marketing-quality categories
- Add untracked docs/plans to .gitignore and sync-public blocklist
- Corrected CC system prompt diff analysis (March vs April 2026)
- Add Scheduled Agents and Headless Mode to Features.md and README.md
- Rename yesdocs/analysen/ to yesdocs/analysis/, add CC system prompt diff
- Align build-tool skill with CAPS-md-spec
- Add yesmem-build-tool as bundled skill
- Add Capability Memory spec and Phase 2 implementation plan
- Add pulse/recap feature to Features.md and README.md

### Testing

- Drop TestInstallBundledCaps_IncludesWikiExport
- Verify wiki_export bundled cap installs into ~/.claude/caps/
- Add live cap parser probes for proxy_health and wiki_export
- Origin end-to-end smoke verifying handler+store+multiplier
- Reconstruct bash error handler tests (Task 5)
- Add failing tests for three directive inject functions
- Raise MCP tool budget to 24000 chars / 65 tools
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

### Fixed

- Address pre-merge code review findings for Capability Memory
- Exclude capabilities from evolution pipeline, clean embedding text

## [backup-opencode-proxy-20260518-1104] - 2026-05-18

### Added

- 1h TTL for fileAttempts 2-strike counter
- Add version and unix timestamp to fork log lines
- 2-strike rule — first block with yesmem hint, second allows
- Add code-tools-first rule (31) + skill catalog entry (50) with grep/glob/read triggers
- Wire JobDone into scheduler executor callback
- Gate dueJobs with running check to prevent concurrent execution
- Add running map to Scheduler struct
- Filter daemon-internal LLM sessions from scanner backlog
- Inject YESMEM_SOURCE_AGENT=opencode into MCP environment by default
- Add git branch context to prevent false feature-branch suggestions
- Add session.created hook for opencode session identification
- Expand extraction model choices to include DeepSeek + GPT via opencode
- Interactive wizard now asks which model to use for conversations
- Add anthropic provider + model/small_model keys to opencode template
- Show session_id + supersede_reason in formatted output
- Inject briefing as system message with MANDATORY_BRIEFING wrapper
- Install opencode plugin files on update if missing/outdated
- Increase opencode MCP timeout 10s → 30s with auto-upgrade from old default
- Bash SUGGEST-only, MANDATORY CHECK prefix
- RULES.md with guard rules + skill catalog
- Mandatory memory search enforcement in rules 23 + 31
- Authoritative skill injection via chat.message + system.transform
- Make SUGGEST mandatory with directive wording
- TUI toast for guard BLOCK/SUGGEST events
- Visual markers for BLOCK/SUGGEST in output
- Buffer last 5 user messages for guard context
- Add chat.message context to guard evaluation
- Fire guard on all tools, always suggest skills
- Add missing CLAUDE.md rules 25-30
- Add Skills section with activation triggers
- Load rules from DECISIONS.md in project root
- Add rule_guard tool.execute.before hook
- Add skill-check instruction to DeepSeek output discipline
- Shorten think-reminder and skill-eval injects for OpenAI path
- Inject think-reminder, skill-eval, rules via timestamp store
- Add prune=false to opencode compaction settings
- Session-resume support for llm_complete RPC
- Model-aware handleLLMComplete + LLMProvider config
- Llm() polyfill for bash caps + llm-complete CLI + llm_complete RPC
- Bash polyfills (store + llm) + llm_complete RPC
- Unix socket MCP transport (replaces HTTP TCP)
- Configurable caps_dir for runtime-agnostic CAP.md storage
- MCP polyfill layer in bun wrapper for cross-compatible caps
- Inject opencode CAP catalog into OpenAI parity pipeline
- Register execute_cap tool
- Execute_cap handler — sandboxed bun/bash CAP execution
- Embed opencode plugin source in binary, install to ~/.local/share/yesmem/plugins/ during setup
- Merge opencode provider/mcp/compaction settings without overwriting existing config; add opencode uninstall + status
- Preload code graph at session start
- Opencode plugin install — symlink + opencode.json registration + make deploy copy
- Opencode-yesmem hook-plugin — code-nav, failure-learn, auto-resolve, idle-reminder
- [yesmem-code-explore] directive — yesmem tools before shell for code exploration
- Set feature_defaults to all-true — new models get full features by default
- Opencode first-class config, migration, and setup wizard
- Agent-aware footer in rules reminder (CLAUDE.md vs OPENCODE.md)
- Concise think_reminder wording for non-Claude agents
- Full timestamp injection (InjectTimestamps) for opencode parity path
- Associative context diagnostic logging for opencode parity path
- Opencode extraction adaption — schema-injection, prompt-neutralization, extractJSON fix
- GenerateAgentsMd() for opencode project reference
- Multi-agent CLIClient with stdin-pipe for codex/opencode
- Profile-aware wording via Generator.SetSourceAgent()
- Multi-agent session identity via resolveClientSessionID()
- Set SharedPrompt in Default() with agent-neutral injector defaults
- Learnings.source_agent + target_agent provenance columns
- Config-split PromptFlags + EffectivePromptFlags(profile) + review fixes
- Golden test framework + PromptProfile type for multi-agent prompt isolation

### Changed

- Bump index V=5, add ensureIndexed log to code_nav
- Rephrase error — yesmem tools first, retry as fallback
- Remove redundant ts= from [req N ver] format
- Self-identifying format [req N ver ts] for pipeline lines
- Bump index.ts cache key for 1h TTL code_nav reload
- Self-identifying log format [req N v2.0.1-nn ts=U]
- Add .opencode/ to gitignore
- Remove debug eval log, verified convMsgs=6 ctxLen~750-1200
- Consolidate extraction pipeline and storage layer
- Use yesmem daemon RPC for last user message, remove broken hooks
- Address code review — dead code removal, caps.db test, parser fix, benchmark deadline, guard cache
- Remove dead code (guard_tui.ts, synthesizeProjectRules)
- Rename DECISIONS.md → RULES.md
- Remove chat.message/system.transform injection — revert to simple tool.execute.after
- Remove debug logging
- Accumulated changes — sandbox, scheduler, capfile adapter, cmd_worker, json commands
- Remove 124MB dbstats binary from tracking
- Sync remaining working tree changes (learnings search, storage schema, project model, opencode cap exec plan)
- Comment out verbose OPENAI-OUT per-message log lines
- Cap version tracking changes from concurrent session
- Commit leftover changes from concurrent session
- Merge CodeExploration into unified CodeToolsFirst, remove CodeExplore flag
- Ignore node_modules
- Rename InjectToolPrefs → InjectClaudeToolPrefs

### Fixed

- Balanced-bracket JSON parser + extraction session filter
- Session mapped line also with [req N ver] format
- Restore opencode grep/glob/read section lost in TTL edit
- Restore throw after debug logs
- Compose tool.execute.before/after hooks (spread overwritten)
- Sync dbgLog + extend to grep/glob/read tools, add RULES.md to embed
- Raw-byte body construction + flex-int JSON parsing
- Increase max_tokens 1024→4096, complex tool reasoning overflow
- Byte-prefix cache + min-tokens gate + RecordFailure reset
- Increase max_tokens 256→1024, reasoning consumed budget
- Rules in system msg for prefix-cache, sync dbgLog, json_object format
- Add narrative and gap-review daemon prompts to filter
- Split telegram poll/reply into separate single-responsibility handlers
- Use future-proof model name deepseek-v4-flash instead of deprecated deepseek-chat
- Fix suggestions Map type, add _test.go TDD-compliance note
- Auto-detect OpenCode via OPENCODE=1 env var fallback
- Also skip rule_guard.ts self-evaluation
- Narrow file skip to RULES.md only, evaluate yesmem/worktree files normally
- Remove anthropic provider from template + wizard — proxy-only providers
- Correct reversed api-key condition + add anthropic to uninstall cleanup
- Add convMsgs summary log for guard visibility
- Register_pid persists source_agent, remove premature briefing generation
- RecentConversation context, rulesHash cache, retry, robust parsing
- Always show provider selection, remove forced skip for non-claude models
- Correct get_learnings API params in briefing — task_type → category suffix
- Briefing refinement fallback — dedup validateRefinedOutput + prevent double-arrival
- Fork auth + prompt cleanup + keepalive guards
- Bun.write truncate bug in code_nav + auto_resolve — use read+append like rule_guard
- Use chat.params to capture user message (fires before LLM call)
- Chat.message reads from output.message, not input.message
- Cache ResolveProjectPath once to avoid sub-query deadlock inside row loops
- Remove dead code with latent data races, protect learnings map reads
- Remove orphaned code block blocking plugin load
- Address code review — GetCachedGraph tests, handler reuse, TplMs timing, ReadFile guard
- Use message.updated hook name, remove stale plugin file
- Move session_active_caps to caps.db to avoid SQLITE_BUSY contention
- Normalize fork effort to 'high' to prevent DeepSeek 400 on xhigh
- Increase search deadline 12s→30s, fix telegram-poll 1s→15s
- Allow user-requested commits in Rule 1
- Normalize cache_control TTL to prevent ordering violation on resume
- Append-mode logging + debug tracing for chat.message parts injection
- SUGGEST format includes exact skill name with call-to-action
- LoadRules now includes YAML Skill Catalog section
- Console.* replaced with dbgLog — root cause of overlay
- Replace all console.* with dbgLog file logging
- BLOCK silent inline, toast-only
- Show BLOCK reason in title not body
- Remove JSX pragma from TUI plugin (not needed)
- Add JSX pragma to TUI plugin
- Move TUI plugin to tui.json config
- Defer BLOCK to tool.execute.after instead of throwing
- Throw descriptive BLOCKED reason instead of space
- Lowercase tool names in Sets, add FIRED debug log
- Renumber Session Discipline rules to avoid collision with Skill Catalog
- Revert to throw — output.args mutation ineffective for Write
- Use tool.execute.after for clean BLOCKED output
- Remove appendPrompt, keep space-only throw
- Throw with space, prepend warning via appendPrompt
- Try neutralize Write/Edit via output.args mutation
- Revert to throw with single-line error format
- Neutralize tool instead of throwing on rule violation
- Add toast notification for rule_guard blocks
- Exclude bash, DECISIONS.md edits, internal files from rule_guard
- Prevent MCP recursion deadlock when spawning opencode
- Increase bash executor timeout for llm commands from 120s to 600s
- Isolate opencode subagents in separate DeepSeek cache namespace
- Comment out all DeepSeek/OpenAI proxy injections for cache baseline
- Disable associative/doc context injection for DeepSeek
- Prepend stable injections to all user messages for DeepSeek cache consistency
- Restore all DeepSeek injections via prependToFirstSystem
- Cap daemon GOMAXPROCS=4 to prevent CPU saturation
- DeepSeek cache fragmentation — move stable injections to system[0], remove variable blocks
- Rate-limiter queue time excluded from LLM call timeout
- Rate-limiter, wiki-tick skip, 300s timeout, circuit breaker for timeouts
- Provider routing for fork, keepalive, and message forwarding
- Fork uses quality model, keepalive skips small sessions
- Unwrap RPC envelope in _mcpCall polyfill
- Polyfill cap_blob_put/cap_blob_get via cap_store RPC
- Unify session ID prefix from oa: to opencode:
- Pass cache_control through OpenAI reverse translation for DeepSeek caching
- Get_compacted_stubs and expand_context range mode read from frozen stubs + messages.db full-text lookup
- Case-insensitive command detection (Grep == grep)
- Keep i and role for re-enableable log lines
- Extract only real file paths, convert absolute to relative for RPC
- Also block directory-level grep when dir has indexed files
- Block only when target file is in CBM code graph, not entire project
- Remove debug logging, clean throw-based blocking
- Pass plugin directory to code_nav hook, fix property access
- Try output.block first, throw Error as fallback for grep blocking
- Increase RPC timeout to 20s for slow CBM code scan
- RPC unwrap daemon result wrapper, code_nav retries on index miss
- Remove broken @opencode-ai/sdk Plugin import, use plain function types
- Code_nav checks project index existence once, not per-file symbol lookup
- Detect opencode source agent from path, fix daemon resolveProjectDir for full paths
- ResolveProjectDir accepts absolute paths directly, bypassing project_short lookup
- Parse daemon raw JSON, fix symlink to persistent path
- Correct daemon socket path + plan for setup integration
- Move CodeExplore from shared_prompt to opencode/codex prompt — Claude already has CodeToolsFirst
- Sync migration model_features comments with setup template
- Place model_features under proxy:, add sandbox+secrets to template
- Activate_cap thread_id resolution before first hook fires
- Restore error handling in proxyCallWithThreadID + fall-through prevention
- Wire EffectivePromptFlags into proxy + openai_parity pipelines
- Gate Claude-specific injectors out of OpenAI parity path

### Performance

- Disable DeepSeek thinking mode via thinking:{type:disabled}
- Shorter system prompt to reduce reasoning overhead
- Adaptive deep_search routing — global BM25 for sparse queries
- Decouple FTS5 content fetch + bm25 candidate pool
- Parallelize loadAll queries after learnings phase
- Add phase timing, content-hash write skip, and CodeGraph caching
- Skip pipeline for non-interactive (CLI/extraction) requests

### Reverted

- Remove :sub isolation for opencode requests
- Go back to appendToLastUserMessage pattern
- Remove prependToFirstSystem, restore injectAssociativeContext for all DeepSeek injections

### Documentation

- Add scheduler running-map implementation plan
- Add AGENTS.md, bash polyfills, code-nav expansion, cap consolidation pattern
- Proxy pipeline plugin migration feasibility
- Root cause analysis for reddit cap cross-compatibility (MCP polyfill needed)
- Execute_cap E2E results and cap architecture notes
- Opencode hook-plugin — code-nav enforcement, failure-learning, auto-resolve, idle-reminder
- Add per-field comments for shared_prompt, model_features gates and viewer_terminal
- Opencode plugin implementation plan (Phase 2)

### Testing

- Add running-gate tests for scheduler dueJobs

## [backup-main-20260518-1104] - 2026-05-05

### Added

- Inject CLAUDE.md Key Packages descriptions as package page intents
- Link file Package to package page, mention packages.md in codemap
- Add packages.md index + packages/ link in README
- Add package-level aggregation pages (packages/<pkg>.md)
- Name-drop top-5 collapsed packages in codemap footer
- Merge CBM scan files into file index for complete coverage
- Add OpenCode TUI sidebar plugin for yesmem cache status display
- Shrink briefing codemap — activity-sort, top-10, wiki-link
- Per-project worktree support + scan caching
- CBM live-scanner + --scan CLI flag for code graph imports
- Add code-graph integration — package, imports, imported-by per file
- Migrate wiki_export cap to native Go subcommand
- Wiki_export file sessions via learnings join path
- Wiki_export session pages with full learning text, file snippets 140→600 chars
- Enable gojq input(s)/0 via WithInputIter
- Wiki_export v65 → v66 — topic co-occurrence + per-file enrichment
- Interval_seconds without cron + script_name targeting (T7+T8)
- Yesmem json now supports full jq-compat flags (-n, -R, -s, -e, --arg, --argjson) via gojq
- Add yesmem query and json subcommands for read-only DB access
- Refactor yesmem-cap-builder for cap-spec v1.1\n\nReplaces the previous 430-line single-file SKILL.md whose save_cap shape\nwas wrong (handler_repl/handler_bash separate fields) with a 336-line\nquick-start index plus three side-files: recipes.md (six working cap\npatterns including yesmem query/json/cap-blob-put pipes), api-reference.md\n(authoritative shapes for save_cap, get_caps, activate_cap, cap_store\nactions, cap_proposal_decide, plus the yesmem CLI surface), gotchas.md\n(28 entries spanning REPL VM allowlist, sh 30KB wall, sanitize_where,\nschema rules, bundled-cap DB-write-back lifecycle including pre-commit\nversion-sync, jq label/apostrophe quirks, and a 9-item spec-feedback\nsection for the cap-spec repo).\n\nSource distilled from two 4116- and 1068-message sessions via verbatim\nstage-1 extraction; audit trail lives under yesdocs/plans/2026-05-01-\ncap-builder-stage1/.\n\nSide-files are picked up natively by InstallBundledSkills which\niterates every file in the skill directory.
- Reject sandbox=none on scope=project caps
- Per-script sandbox overrides scheduled-job profile
- Add Sandbox field to ScriptMeta with enum validation
- Add per-script sandbox metadata field
- Origin-aware multiplier for trust-weighted scoring
- Tag origin_tool=llm_extracted_session on extracted learnings
- HandleRemember accepts origin param, defaults to user
- Persist and read origin_tool in learnings
- Add OriginTool field to Learning struct
- Add origin_tool column to learnings table
- Add openai, ssh, gpg, ipv4_public, hex_secret pattern kinds
- Wrap LLMClient with SanitizingClient when SecretsSanitization enabled
- SanitizingClient wraps LLMClient with Sanitize-before-after
- Redact bash-job output before persist when SecretsSanitization enabled
- Wire SecretRedactor onto Handler when enabled
- Introduce Sanitizer interface and SecretRedactor with 10 kinds
- FREEZE/RESTORE symmetry + eager-stub memory layer
- Re-enable matched-cap inject via Sawtooth-tail
- T3 decide tool, T4/T5 rate-limits, review fixes, multi-bash filter
- Route substantial diffs to cap_proposed for user approval
- Cap-body diff classifier
- Prompt-flow isolation between claude and codex paths
- Telegram bundle CAP.md staging (cap-spec v1.1, Phase E1)
- ScriptName in scheduler + bundle-cap support (cap-spec v1.1, Phase D)
- Telegram_reply bash cap with claude -p + dynamic timeout for LLM jobs
- Model field on ScheduledJob + --model flag for headless mode
- Bash adapter store() + yesmem store CLI + interval_seconds scheduling
- Persist sandbox profile in storage + MCP schema enum
- Wrap executeHeadless in sandbox via WrapExecArgs
- RunWithProfile + BuildSandboxedCommand + wire into fireJobBash
- Add sandbox profile to config + ScheduledJob + scheduleCreate
- Add SandboxProfile type with none/standard/strict presets
- Headless --resume session tracking + cap actions setup flow
- Heartbeat interval 2s → 1s
- Expose mode, cap_name, auto_correct in schedule tool schema
- Heartbeat-driven bash error processing with auto-correct via Sonnet
- Bash mode validation + fireJobBash with sandbox execution
- ScheduledJobRow.CapName/AutoCorrect + BashJobRun CRUD
- Schema for bash-mode scheduler (cap_name, auto_correct, bash_job_runs)
- Ai-jail sandbox integration with download-on-first-use
- Inject [yesmem-code-tools-first] directive block
- Inject code-tools-first directive via InjectAntDirectives
- Bundled caps deployment in setup + update + hook matchers to .*
- Code-nav hook detects rg/grep/cat in REPL code, not just Bash
- Subagent caps propagation + seen-map dedup fix + conditional adapter JS
- UsesGenericAdapters with word-boundary detection for conditional adapter injection
- Migrate adapter to 3-primitive design (store/web/file)
- Add headless mode via claude -p for lightweight scheduled runs
- Multi-turn workflow sequence detection
- Adapter mapping in activate_cap and save_cap handlers
- Writer converts provider-specific to generic names
- Adapter registry with bidirectional name mapping
- Detect requires from generic adapter calls in script
- Add recurring flag for one-shot jobs that auto-delete after firing
- Add cap scheduler with cron parsing, storage, MCP handlers, and daemon wiring
- REPL pattern noise reduction — deny-list, session budget, threshold 8
- Add simple JS formatter for single-line scripts in CAP.md
- Format SQL with column-per-line indentation in CAP.md
- Export DDL from caps.db, omit derivable schema from CAP.md
- Add CAP.md parser, writer, scanner, and daemon integration
- Capabilities lazy-activation catalog + API-actual threshold
- Add PreToolUse code-nav detection hook
- Narrative-in-briefing, caps-inject fallback, minor fixes
- Trivial-shape filter for REPL pattern detection (Phase 7)
- Activity-gate for REPL-pattern detection (Phase 5)
- REPL-pattern detector stage + config wire-up (Phase 4)
- REPL-pattern handlers + RPC + dismiss MCP tool (Phase 3)
- Repl_pattern_observations table + CRUD (Phase 2)
- Shell-command normalizer + shape-hash (Phase 1)
- Add yesmem-clarify-first directive block — narrow threshold (materially different work only)
- Extend beweislast with mental self-check + delegation-contract with model tier guidance
- Sharpen scope-discipline — bug-surfacing is MANDATORY, not just allowed
- Wire three new directive blocks — beweislast, scope-discipline, delegation-contract
- Translate directive blocks to EN, sharpen output-discipline, add N1/N2/N3 inject functions, switch all inject directives to idempotent UpsertSystemBlockCached, add duplicate-inject regression test
- Wire three directive-injection flags into both pipelines
- Add three directive-injection flags, defaults true
- Add three directive inject functions
- Expose session model via whoami + persist from proxy
- Relax WHERE sanitizer + add pagination (offset/has_more/total)
- Blob-pipe via cap_store for >30KB capability payloads
- Auto_active + MANDATORY inject directive + PTY-submit fix
- Capabilities cache + sawtooth-coupled briefing refresh
- Inject active capabilities as user/assist pair before last user message
- Phase 1 lazy-activation (activate/deactivate)
- Add cap_store — sandboxed capability database with CRUD MCP tool
- Add register_capabilities MCP tool, /build-tool skill, review fixes
- Add briefing capability hints, /build-tool skill, review fixes
- Add Capability Memory handlers, MCP tools, and tests
- Add 'capability' as valid learning category
- Gotcha injection decay + tiered output (top-1 only)
- Config migration for setup and update
- Add skill_eval_inject config toggle
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
- Integrate pulse learnings into collapse session timeline
- Capture CC away_summary as pulse learnings
- Add --per-commit mode to sync-public.sh

### Changed

- Regenerate for v2.0.6 release
- Split index.md into file-tree + learnings.md category index
- Copy loop var in save_cap, clarify legacy-handler merge comment
- Merge GenerateSharedAdapterJS into GenerateAdapterJS with skipStore param, add UsesStoreAdapter
- Remove yesmem-build-tool, refresh yesmem-planning
- Rewrite wiki_export as native runtime:bash (v62)
- Silence sandbox-override log when profiles match
- Enumerate valid sandbox values in error
- Tidy sandbox validation per code review
- Exclude .ai-jail sandbox configs
- Drop dead guard in WriteCapToDisk DDL path
- Retire reddit standalones, stage bundle CAP.md exports + idempotent adapter rename
- Pivot REPL-pattern detection to fork-driven model
- Resync cap_search bundle template
- Replace private paths in test fixtures with generic placeholders
- Remove   .last-sync-hash, require --branch flag in sync-public.sh
- Remove Notes section from parser and writer
- Merge 4 schedule_* tools into single schedule tool with action param
- Update catalog format and auto_active default
- Rename capability→caps across all packages
- BlockText helper with trailing separator for all system blocks
- Sync-public.sh requires --branch flag, auto-generated CHANGELOG
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

- Cache project path to avoid SQLite single-conn deadlock
- Add all public docs to allowlist, remove stale claude-code-repl exclude
- Harden security scanner
- Add WithStringItems to update_plan array params for codex JSON schema compliance
- Resolve job work_dir via project resolver instead of hardcoded dev path
- Add file-specific entity matching to gotcha filter — old info gotchas with matching file entities are preserved (per code review)
- Session metadata from learning IDs, exclude non-package dirs
- Remove unreliable LOC from package pages
- Go-only package filter, LOC aggregation, deduped health counts
- Filter non-code paths from file index — vendor, PDFs, erledigt
- Point codemap footer to index.md instead of files/
- Normalize absolute file paths to relative in file index
- Filter foreign worktrees, dot-files, absolute paths from file index
- Move wiki-link to top of codemap block — before package table
- Clarify wiki path encoding in imperative block
- CLI --scan also saves to project_scan cache
- Derive file imports from CALLS edges when IMPORTS is empty
- Save_cap field-merge preserves scripts on metadata-only updates
- Per-cap store() wrapper with capability injection and args stringify
- Align wiki_export source with disk v65
- Version-Guard in WriteCapToDisk — skip overwrite when disk version >= DB version
- Blank first user message content on collapse
- Cap WAL size at 10MB via journal_size_limit pragma
- Expose origin parameter in remember tool schema
- Guard startDaemon under go test to prevent fork bomb
- Emit origin_tool in hybrid_search response so proxy multiplier sees it
- Tighten phone regex to reject ipv4-like dotted strings
- Widen generic_api_key charset to ./+= for base64 tokens
- Broaden bearer_token regex beyond Authorization header
- Wrap SummarizeClient at assignment instead of post-replacement
- Wrap quickstart client+qualityClient for all 6 LLM paths
- Wrap briefingClient with SanitizingClient when enabled
- Redact Command/ErrorMsg, headless output and stderr
- Sanitize SanitizingClient output even on inner error
- Respect since/before in search and deep_search
- Always shift cache breakpoint, including tool_result messages
- Fail closed when sandbox unavailable
- GetCapTableDDL prefix-overlap via cap_store_meta JOIN
- Make adapter rename idempotent via word-boundary check
- Correct ai-jail release asset naming and extract from tarball
- 3-way project filter in GetActiveLearnings (briefing tests)
- Hydrate DatabaseSQL via GetCapTableDDL in WriteCapToDisk
- Spec-compliant CAP.md render and parse
- Parse UNIQUE/PK/NotNull constraints from MCP cap_store create_table params
- API key fallback chain for re-setup
- Code review fixes for bash-mode scheduler
- Inject adapter JS (store/web/file aliases) in proxy caps re-injection
- Set dataDir in test helper to prevent CAP.md artifacts in source tree
- Remove cross-project learnings fallback
- Parse nested pattern envelope in suggestion response
- Use already-constructed meta for WriteCapToDisk instead of re-parsing content string
- Use job-specific section names and pass full ScheduledJob to executor
- Translate all agent prompts to English
- Write task to scratchpad before spawn so agent sees it in briefing
- Pre-spawn stop stale agents, unified 10min idle timeout for all states
- Wrap scheduled prompt with focused task-agent preamble
- Replace max_turns with watchScheduledAgent idle-timeout (10min) + status polling
- Add max_turns=10 to scheduled agent spawn, log errors
- Pass project+work_dir to spawn, relay prompt with confirmation
- Quote description in YAML frontmatter to handle colons and special chars
- Individual MCP permissions + memory-first recall reminder
- Remove OR-clause from frozen-stub invalidation
- Inject pattern-suggestion into last user message (Phase 6 cache fix)
- Resolve thread_id via _caller_pid fallback in capability handlers
- Cap_store upsert preserves created_at (#53149)
- Key briefing cache by project to prevent cross-project leak
- Register_capabilities emits 4 positional args to match REPL signature
- Address pre-merge code review findings for Capability Memory
- Exclude capabilities from evolution pipeline, clean embedding text
- Use projectKey() for Code Map header in worktrees
- Resolve git worktree HEAD and project key correctly
- CLI client robustness for subscription installs
- Replace LFS pointer with real sse_dyt_512d.bin binary (6KB)
- Align prompt_rewrite test inputs with updated CC target strings
- Add pre-modification dump, update rewrite targets for CC ~2.1.117
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
- Truncate pulse content to 150 chars in session timeline
- Set created_at on pulse learnings from JSONL event timestamp
- Strip context_management from fork requests

### Reverted

- Remove Go-specific filesystem fallbacks from code tools
- Revert "feat(codescan): worktree-aware filesystem fallback for code tools"

### Documentation

- Translate CapFeatures.md and caps-vs-skills-rationale.md to English
- Add config.yaml and settings.json references, move internal docs to yesdocs
- Merged context redundancy analysis with implementation decisions and provenance table
- Drop sandbox prose section from 1.0-copy
- Opencode proxy and injection integration plan
- Verify and correct opencode-integration implementation plan
- Briefing codemap shrink follow-up to wiki-render
- Swap wiki-export-level1-enrichment for wiki-render-go-rewrite
- Cap consolidation pattern + sandbox field spec note
- Sync against main + add capabilities/sanitize/sandbox sections
- Add opencode source integration + wiki-export L1 enrichment plans
- Add DiD-roadmap, learnings-wiki-export, per-cap-sandbox; refresh sanitize-followups
- Update capability-memory design notes
- Add database schema reference for the four SQLite stores
- Document set_plan trigger conditions in MCP and coding-discipline injection
- Add cap-system hardening roadmap (T1, T3, T8)
- Add cap-builder knowledge audit trail\n\nTwo-stage workflow for distilling cap-building knowledge from past\nsessions into the yesmem-cap-builder skill.\n\nStage 1 (verbatim extraction) under cap-builder-stage1/:\n  session-bb37bd60.md (517 lines, full coverage 0..1067)\n  session-cc0ba29d.md (733 lines, coverage 0..1599)\n  session-cc0ba29d-part2.md (1003 lines, coverage 1600..4115)\n  README.md as index and hand-off\n\nStage 2 (synthesised proposal) under cap-builder-stage1/stage2/:\n  SKILL.md, recipes.md, api-reference.md, gotchas.md\n  Snapshot of the proposal before patches and live take-over.\n\nKept under yesdocs/plans/ rather than discarded so the chain from\nsession quote to skill paragraph stays auditable; future revisions\ncan re-run stage 1 against new sessions and diff against this\nbaseline.
- Note why project-scope guard includes script name directly
- Note B8 skip per audit grep result
- Audit trust-multiplier locations and remember touch-points
- Document SanitizingClient decorator-order contract
- Clarify AllowedExceptions full-match semantics + add config example
- Add Plan B+F implementation plan for source integrity and sanitize followups
- Post-review hardening section for sanitization integration
- Mark Defense-in-Depth status (verified 2026-04-29)
- CC 2.1.119-2.1.123 feature adoption plan
- Add system/cache-cycle.md — vollstaendige Cache-Zyklus-Architektur
- Bash-mode-scheduler audit + auto-correct-hardening plan
- Add plans and analyses from 2026-04-24 (private, excluded from public sync)
- Dead-target-detection + cap-suggestion-v2 plans
- Remove obsolete telegram adapter plan and spec
- Update Features.md and README.md for recent development
- Add JobsFeature.md with full scheduler documentation
- Bash-mode scheduler implementation plan
- Minor updates to CHANGELOG, reddit_fetch CAP, build-tool SKILL
- Update CapFeatures.md — noise reduction, workflow detection, open items audit
- Update CapFeatures.md adapter section to 3-primitives design
- Translate scheduler section to English
- Resolve stale items in CapFeatures.md (blob-pipe, naming, open issues)
- Update CapFeatures.md with adapter layer, resolve stale items
- Add CAP.md file format section to CapFeatures.md
- Add yesmem-directive-blocks plan
- Yesmem-build-tool — patterns from session bb1ded28
- Yesmem-build-tool — 4 fixes from session 63ae4565 RED-test
- Cap_store analysis system — architecture + 8 examples
- Remove stale Bleve reference, update vector store description
- Restructure Differentiators into marketing-quality categories
- Add untracked docs/plans to .gitignore and sync-public blocklist
- Corrected CC system prompt diff analysis (March vs April 2026)
- Add Scheduled Agents and Headless Mode to Features.md and README.md
- Rename yesdocs/analysen/ to yesdocs/analysis/, add CC system prompt diff
- Align build-tool skill with CAPS-md-spec
- Add yesmem-build-tool as bundled skill
- Add Capability Memory spec and Phase 2 implementation plan
- Add pulse/recap feature to Features.md and README.md

### Testing

- Align stale assertions with current render text and sandbox auto-install
- Use non-pattern fixture value for generic api-key redaction
- Drop TestInstallBundledCaps_IncludesWikiExport
- Verify wiki_export bundled cap installs into ~/.claude/caps/
- Add live cap parser probes for proxy_health and wiki_export
- Origin end-to-end smoke verifying handler+store+multiplier
- Reconstruct bash error handler tests (Task 5)
- Add failing tests for three directive inject functions
- Raise MCP tool budget to 24000 chars / 65 tools
- Raise tool definition budget to 21000 chars, count to 60

