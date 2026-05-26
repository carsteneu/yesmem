# Changelog

All notable changes to YesMem are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Changed

- Regenerate CHANGELOG.md (26 release sections)

### Documentation

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

- Remove Go-specific filesystem fallbacks from code tools
- Revert "feat(codescan): worktree-aware filesystem fallback for code tools"
- Remove eager tool-result stubbing (breaks cache anchors)
- Revert "feat(scoring): session-correction-rate als Quality-Signal (2e)"
- Revert "feat(storage): switch SQLite driver from modernc to ncruces/go-sqlite3"
- Remove dynamic persona dimensions + self-reflection

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


[Unreleased]: https://github.com/carsteneu/yesmem/compare/v2.1.4...HEAD
[2.1.4]: https://github.com/carsteneu/yesmem/compare/v2.1.3...v2.1.4
[2.1.3]: https://github.com/carsteneu/yesmem/compare/v2.1.2...v2.1.3
[2.1.2]: https://github.com/carsteneu/yesmem/compare/v2.1.1...v2.1.2
[2.1.1]: https://github.com/carsteneu/yesmem/compare/v2.1.0...v2.1.1
[2.1.0]: https://github.com/carsteneu/yesmem/compare/v2.0.6...v2.1.0
[2.0.6]: https://github.com/carsteneu/yesmem/compare/v2.0.5...v2.0.6
[2.0.5]: https://github.com/carsteneu/yesmem/compare/v2.0.4...v2.0.5
[2.0.4]: https://github.com/carsteneu/yesmem/compare/v2.0.3...v2.0.4
[2.0.3]: https://github.com/carsteneu/yesmem/compare/v2.0.2...v2.0.3
[2.0.2]: https://github.com/carsteneu/yesmem/compare/v2.0.1...v2.0.2
[2.0.1]: https://github.com/carsteneu/yesmem/compare/v2.0.0...v2.0.1
[2.0.0]: https://github.com/carsteneu/yesmem/compare/v1.1.34...v2.0.0
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
[safety-before-rebase-2026-04-16]: https://github.com/carsteneu/yesmem/compare/pre-sanitization-2026-04-29...safety-before-rebase-2026-04-16
[pre-sanitization-2026-04-29]: https://github.com/carsteneu/yesmem/compare/backup-opencode-proxy-20260518-1104...pre-sanitization-2026-04-29
[backup-opencode-proxy-20260518-1104]: https://github.com/carsteneu/yesmem/compare/backup-main-20260518-1104...backup-opencode-proxy-20260518-1104
[backup-main-20260518-1104]: https://github.com/carsteneu/yesmem/releases/tag/backup-main-20260518-1104
