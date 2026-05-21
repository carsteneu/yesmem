# Developer Tools

## 1. Documentation Management

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

## 2. Hook System

YesMem hooks into Claude Code at multiple event points.

### Active Hooks

| Hook | Claude Code Event | Purpose |
|------|-------------------|---------|
| `briefing-hook` | SessionStart | Generate and inject briefing |
| `micro-reminder` | UserPromptSubmit | [deprecated, migrated to idle-tick] |
| `hook-check` | PreToolUse | Knowledge-aware gotcha injection + hard block on repeat offenders |
| `hook-failure` | PostToolUseFailure | Learn gotcha from failure + deep search for solutions (combined) |
| `hook-resolve` | PostToolUse | Auto-resolve open tasks when commit messages match |
| `hook-think` | PostToolUse | [removed — reminder injection now handled by proxy (settings.go:258)] |
| `session-end` | Stop | Session cleanup, trigger extraction |
| `idle-tick` | UserPromptSubmit | Dynamic yesmem-usage reminder when idle |

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

#### Gotcha Injection Decay (Precision-Based)

Gotcha injection now uses precision-based decay rather than simple age-based decay. Each gotcha candidate is scored by its precision — relevance to the current tool context. At low precision, only the top-1 gotcha is shown (tiered output). This prevents flooding the context with marginally relevant gotchas while ensuring the most relevant one always appears.

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

### REPL Pattern Detection

YesMem detects repeating tool-use patterns across conversation turns and suggests converting them into reusable capabilities (caps).

**How it works:**
- Each turn's tool sequence is hashed into a compact signature
- Turn hashes are stored per thread in `thread_turn_hashes`
- Subsequence matching finds recurring patterns across turns (e.g., the same Read→Edit→Read cycle)
- When a pattern is detected, the proxy injects a suggestion to convert it into a cap via `save_cap()`
- Users can dismiss suggestions via `dismiss_repl_pattern(project, shape_hash)`. After 3 dismissals the pattern is permanently suppressed and never suggested again.

---

## 3. OpenCode Plugin

YesMem ships a TypeScript/Bun hook plugin (`plugins/opencode-yesmem/`) that integrates directly with opencode's plugin API. Unlike the bash-based hook system for Claude Code (§10), this plugin hooks into opencode's own event system for tool-call interception, session lifecycle, and user message capture.

### Installation

The plugin is embedded in the yesmem binary via `go:embed` (`plugins/embed.go`). During `yesmem setup`, it is installed to `~/.local/share/yesmem/plugins/opencode-yesmem/` and registered in `opencode.json`. A Unix socket RPC layer (`rpc.ts`) forwards all tool calls to the yesmem daemon via `~/.claude/yesmem/daemon.sock`.

### Five Hooks

The plugin registers five hooks into opencode's event system:

| Hook | opencode Event | Purpose |
|------|---------------|---------|
| **code_nav** | `tool.execute.before` | Blocks `grep`, `cat`, `find`, `sed`, `rg` etc. when the target file is indexed in the CBM code graph. Suggests MCP code tools instead. 2-strike auto-allow system with 1h TTL. First attempt blocked with yesmem tool suggestion, second attempt auto-allowed. The dismiss_code_nav function referenced in docs does not exist in current code (code_nav.ts:14). |
| **rule_guard** | `tool.execute.before` + `tool.execute.after` | Evaluates every tool call against `RULES.md` + Skill-Catalog via DeepSeek. BLOCK throws to prevent the tool call; SUGGEST injects a directive via `tool.execute.after`; PASS lets it through. (See §10c below for full detail.) |
| **failure_learn** | `tool.execute.after` | Detects failed Bash commands and forwards them to the daemon for gotcha extraction — the same auto-learning loop as `hook-failure` in the Claude Code hooks. |
| **auto_resolve** | `tool.execute.after` | Watches for git commit messages and auto-resolves matching open tasks via `resolve_by_text()`. |
| **idle_reminder** | Periodic (plugin-internal timer) | After configurable idle periods, injects a YesMem usage reminder (search your memory, check open tasks, update the plan). |

### Shell Environment

The plugin injects `YESMEM_SOCKET` (pointing to `daemon.sock`) and `YESMEM_SOURCE_AGENT=opencode` into every shell environment. This enables the bash-polyfill layer for cap scripts (see §25) and ensures the daemon can attribute sessions to the correct source agent.

### Session Lifecycle

On `session.created`, the plugin registers the session with the daemon via `register_pid` RPC — mapping the opencode session ID to the yesmem PID-session infrastructure. This enables:
- Agent-to-agent messaging (send_to, broadcast) for opencode sessions
- Session identity resolution via PID reverse-lookup
- Briefing injection by the proxy (which handles opencode parity requests)

At startup, the plugin triggers a one-shot code scan (calling `search_code_index` with a dummy pattern) so the CBM code graph is warm before the first user prompt — eliminating cold-start latency for code_nav.

### Architecture

```
opencode session
  → opencode-yesmem plugin (TypeScript/Bun)
    → rpc.ts (Unix socket to daemon.sock)
      → yesmem daemon (Go)
        → RPC handlers (search_code_index, register_pid, store_learning, etc.)
```

The plugin contains no business logic — it intercepts opencode events, normalizes them into RPC calls, and injects the daemon's responses back into opencode's output. All intelligence (search, code graph, learning storage, rule evaluation) lives in the Go daemon — the plugin is a thin integration layer, exactly like the MCP server.

### 10c. rule_guard — DeepSeek-Based Tool-Call Compliance

The `rule_guard` hook is the plugin's largest and most iterated component (~30 commits just for format tuning). It evaluates every tool call against the project's `RULES.md` and YAML Skill-Catalog using DeepSeek, then decides whether to block, suggest, or pass the call.

#### How it works

1. **Rule loading** — At startup, `loadRules()` reads `RULES.md` from the project root. It extracts numbered rules plus the `## Skill Catalog` section (containing skill activation triggers in YAML format). This combined text is the evaluation context.

2. **DeepSeek evaluation** — On every `tool.execute.before` event, the guard sends the tool name + input + rules context to DeepSeek (`deepseek-v4-flash` model, non-streaming). Model is now read from config.yaml extraction.model (since 2026-05-21). The LLM returns a structured decision: `BLOCK`, `SUGGEST`, or `PASS` with an explanation.

3. **BLOCK** — The tool call is prevented via `throw new Error(...)`. Used for rule violations that would cause data loss, commit pollution, or security issues. The error message includes the violated rule number and a directive (e.g. "Fix the source learning instead of editing yesmem-ops.md").

4. **SUGGEST** — The tool call proceeds, but a deferred directive is injected via `tool.execute.after`. The output prefix contains `[rule_guard] MANDATORY CHECK: activate <skill-name> — ...` with a call-to-action. Used when the agent should have activated a skill before acting (e.g. bash call before memory search).

5. **PASS** — No action; the tool call proceeds normally.

#### Exemptions

Certain tools and operations bypass the guard entirely via a `skipTools` Set:

- **`bash` calls** — Pre-filtered as SUGGEST-only (never BLOCKed). The mandatory memory-search rule means bash calls get the search-before-execute reminder, not a hard wall.
- **Edits to `RULES.md` or `rule_guard.ts`** — Exempt to prevent the guard from blocking its own maintenance.

#### Format Iteration

The guard's output format went through ~30 iterations across May 12 before stabilizing. The key constraint: opencode's plugin API behaves differently per hook phase and per tool type.

| Attempt | Problem | Final |
|---------|---------|-------|
| `throw Error` for BLOCK | Generic, no rule context | ✅ Throw with `[rule_guard] BLOCKED: <reason>` |
| `output.block` for BLOCK | Silent, agent didn't learn | ❌ Abandoned |
| `output.args` mutation for Write/Edit | Doesn't work — args are read-only after the before-phase | ❌ Abandoned |
| `tool.execute.after` for SUGGEST | Clean output, doesn't block the tool | ✅ Active |
| `chat.message` append for context | Invalid — OC 1.14.49 doesn't expose text in chat.message | ❌ Abandoned |
| Toast-only for BLOCK | User missed the toast, agent proceeded unaware | ❌ Abandoned |

The final stable configuration:
- `tool.execute.before` → throw for BLOCK (hard stop)
- `tool.execute.after` → output prefix for SUGGEST (non-blocking directive)
- `experimental.chat.messages.transform` → capture last user message for context (the only hook in OC 1.14.49 that delivers raw user text)
- `chat.params` → fallback user-message capture when `messages.transform` is unavailable
- All logging goes to `~/.claude/yesmem/logs/plugin.log` via `dbgLog()` (never `console.*` — that corrupts opencode's TUI)

#### API Key Resolution

The guard resolves its DeepSeek API key from `~/.local/share/opencode/auth.json` (`auth.deepseek.key`). This is the same key opencode uses for DeepSeek models — no separate configuration needed.

---

## 4. Code Intelligence

YesMem includes a language-agnostic code intelligence system that scans, indexes, and exposes codebase structure through MCP tools and proxy-injected context.

### 18.1 Architecture

The system consists of three layers:

1. **Scanner** — CBM CLI (`cbm-cli`), an external binary that parses source files using TreeSitter grammars. Extracts functions, types, methods, imports, call edges. Auto-downloaded during `yesmem setup` if not present.
2. **CodeGraph Store** — SQLite tables (`code_files`, `code_symbols`, `code_edges`) storing the indexed codebase. Incremental: only re-scans files with changed mtimes. Worktree-aware: project key is derived from the git repo root, not the working directory.
3. **MCP Tools** — Eight tools expose the graph to Claude Code sessions.

### 18.2 Scanning

Scanning runs on daemon startup and on-demand via `yesmem scan`. The scanner:

- Walks the project directory, respecting `.gitignore`
- Hashes file mtimes against the SQLite cache for incremental updates
- Extracts symbols (functions, types, methods, constants, variables) with line ranges
- Extracts edges (imports, calls, defines) between symbols
- Stores everything in `code_files`, `code_symbols`, `code_edges` tables

Supported languages depend on the CBM CLI's TreeSitter grammar set (Go, Python, JavaScript/TypeScript, Rust, Java, PHP, and others).

### 18.3 MCP Tools

| Tool | Purpose |
|------|---------|
| `get_file_index(project, dir)` | List files in a directory with learning/gotcha annotations |
| `get_file_symbols(file, project)` | All top-level symbols in a file with line numbers |
| `search_code_index(pattern, project)` | Find symbols by name substring (functions, types, methods) |
| `get_code_snippet(qualified_name, project)` | Full function/type body from source. Also supports range mode (file + start_line + end_line) |
| `get_code_context(qualified_name, project)` | Symbol signature, file, and connected nodes (callers, imports) |
| `search_code(pattern, project)` | Grep enriched with graph context (containing function, callers) |
| `get_dependency_map(package, project)` | Package import graph with cycle detection |
| `graph_traverse(from, project)` | Trace call paths and dependencies from a node (inbound/outbound/both) |

These tools are designed to replace raw shell navigation (`grep`, `find`, `cat`) for code understanding tasks. They use less context and leverage the pre-built graph.

### 18.4 Code Map Injection

The proxy generates a Code Map summary and injects it as a separate conversation turn at session start. The Code Map includes:

- **Package table** — all packages with file count, description, and gotcha count
- **Key files** — important files per package (heuristic: most symbols, most edges)
- **Entry points** — files with `main()` or standalone execution capability
- **Active Zones** — directories with the most file changes in the last 7 days (from git log)
- **Change coupling** — file pairs that frequently change together (from git log co-occurrence)
- **Code health** — test coverage ratio, TODO/FIXME counts

The Code Map is injected as its own assistant turn, separate from the briefing, so it doesn't compete for the system prompt cache breakpoint.

### 18.5 Code Navigation Hook

The code-navigation hook prevents agents from reaching for shell tools when the target file is already indexed in the CBM code graph. If the code graph has the file, shell-based navigation (grep/cat/find/sed/rg) is unnecessary — code tools are faster and provide richer context (callers, symbols, co-edits, gotchas).

**Implementation (opencode plugin):**

The `code_nav` hook in the `opencode-yesmem` plugin (§10b) fires on `tool.execute.before`. It checks whether the target path exists in the code graph via the `search_code_index` RPC. If the file is indexed, it throws with a directive:

```
[code-nav] yesmem has indexed this path. Navigate with MCP tools:
  get_file_symbols(file) for symbol overview, get_code_snippet for targeted ranges,
  graph_traverse for call paths — no grep/cat/find needed.
```

**Dismissal:**

The suggestion uses a 2-strike auto-allow system with 1h TTL. First attempt is blocked with a yesmem tool suggestion; second attempt is auto-allowed. The dismiss_code_nav function referenced in some documentation does not exist in current code (code_nav.ts:14).

**Legacy support (Claude Code):**

For Claude Code sessions (which don't run the opencode plugin), the older `hook-check` PreToolUse hook detects shell commands (`cat()`, `grep`) on indexed paths and suggests MCP code tools. Same behavior, different integration layer.

**Impact:**

- The code graph tracks which sessions touched which files (`yesmem_related_to_file`)
- CBM scanner re-parses only when the git working tree changes (cached `CodeGraph`)

### 18.6 Worktree Awareness

The project key for code indexing is derived from directory path (cbm_scanner.go:303-307). Worktree sharing via git-root resolution is not currently supported. This means:

- Each worktree has its own code index
- Scanning in one worktree does NOT update the index for other worktrees of the same repo
- The project key is derived from the directory path, not the git root

### 18.7 Wiki Export — Rendered Knowledge Base

The daemon periodically renders all YesMem learnings, sessions, files, and code intelligence data into a browsable, pure-Markdown wiki at `~/.claude/yesmem/wiki/<project>/`. This gives agents (and humans) a static, always-available entry point into the accumulated knowledge of a project — without needing the daemon to be running, and without any MCP tool calls.

#### What Gets Rendered

| Page | Content |
|------|---------|
| `index.md` | Full file tree with per-file annotations (gotchas, TODOs, session count, last touched) |
| `packages.md` | Package-level overview: file counts, gotchas, TODOs, package descriptions from CLAUDE.md |
| `learnings.md` | All active learnings grouped by category with metadata (source, use_count, stability, agent_role, session_id) |
| `learnings/<ID>.md` | Individual learning detail pages with full content, entities, actions, keywords, supersede chain history |
| `files/<path>.md` | Per-file deep-dives: top-level symbols with line numbers, imports, co-edited files, related learnings, recent sessions that touched the file |
| `packages/<pkg>.md` | Per-package aggregation: all files in the package, combined symbols, change coupling |
| `topics/<name>.md` | Topic clusters — semantically related learnings grouped by agglomerative embedding clustering |
| `sessions/<id>.md` | Session summaries with narrative handovers, commit references, and extracted learnings |
| `health.md` | Code health overview: test coverage ratios, file counts, package list |
| `README.md` | Project portrait with recent sessions, learnings count, package count |

#### Refresh Cycle

- **wiki-tick:** The daemon runs a 5-minute ticker (`startWikiTicker`). On each tick, it iterates over all active projects and checks whether the wiki needs re-rendering.
- **Change detection:** A snapshot file (`wiki_snapshot.json`) tracks `max(updated_at)` of active learnings and `max(at)` of sessions. If nothing changed since the last render, the tick skips that project entirely — zero I/O, zero CPU.
- **Content-hash write-skip:** Each output file's SHA256 hash is compared against the existing file on disk. Files whose content hasn't changed are skipped — only genuinely updated pages trigger disk writes.
- **Code graph caching:** The codescan result is cached per project via `CachedScanner`. If the git working tree hasn't changed since the last scan, the scanner returns the cached `CodeGraph` — avoiding a full TreeSitter re-parse.

#### Performance

- **Parallel queries:** The `loadAll` phase runs database queries for learnings, sessions, entities, and files concurrently via goroutines + `sync.WaitGroup`.
- **Result:** Wiki rendering went from **36 seconds** (sequential queries, no caching, full writes every tick) to **under 1 second** on the main yesmem project (400+ rendered files) — a ~36× improvement across three commits: parallelization, content-hash skip, and code graph caching.
- **Phase timing:** Each render logs per-phase timing: load, compute, template execution, and write — making regressions immediately visible in the daemon log.

#### Integration with Code Map

The code map (see §18.4) includes a wiki link at the top of every code map injection: the wiki path, the last build timestamp, and a "BEFORE editing any file, check its wiki page" directive. The wiki is the browsable backup of the code graph — the code map is the summary; the wiki is the reference library.

The `[yesmem-wiki-first]` system block (injected by the proxy) reinforces this: agents are instructed to check the wiki for per-file gotchas and learnings before editing any file. Falls back to `search_code_index` / `get_code_snippet` / raw grep when wiki pages don't yet exist for a file.

#### CLI Access

```bash
# Trigger a one-shot wiki render for the current project
yesmem wiki-export --project yesmem

# View the rendered wiki in a browser or file viewer
ls ~/.claude/yesmem/wiki/yesmem/
cat ~/.claude/yesmem/wiki/yesmem/index.md
```

The wiki is also available as a bundled capability (`wiki_export` cap) for scheduled or on-demand rendering outside the 5-minute tick cycle.

---


---

## RULES.md — Policy Engine (since 2026-05-13)

A declarative rule file with 30+ rules and a skill catalog. Rules are evaluated against every tool call before execution.

**Architecture:**
- `RULES.md` — Rule definitions with activation triggers (tool names, patterns, contexts)
- **Skill Catalog** — 26 skills (25 distinct) with MANDATORY CHECK prefixes: memory search before answers, code tools before shell, TDD before implementation
- **Model evaluates write, edit, read, bash, grep, glob tool calls** — skill, task, todowrite are skipped. Documentation-targeted calls (.md/.txt/.rst/.pdf) are also skipped. Not every tool call is evaluated.

**Key rules:**
- **Memory-first:** `hybrid_search()` mandatory before answering questions about past work
- **Code-tools-first:** `search_code_index`, `get_file_symbols`, `get_code_snippet` before shelling out to grep/find
- **Wiki-first:** Check file wiki pages before editing — accumulated gotchas and decisions
- **TDD mandatory:** Write tests before implementation for Go code
- **No auto-commit:** User controls commits exclusively

**Self-correcting:** Rules are not static. When a rule blocks incorrectly, the rule is rewritten based on what actually works. Snapshot tracking via content hashes detects drift.

**Performance:** Thinking mode disabled for guard evaluation (`thinking: {type: disabled}`). Dedicated skipt-list for doc-target calls (`.md`, `.txt`, `.rst`, `.pdf` files) — no LLM evaluation needed.

### OpenCode Plugin Lifecycle

- **Non-destructive setup:** `mergeOpencodeSettings()` preserves existing user configuration during `yesmem setup` — provider, model, and MCP settings are merged without overwriting.
- **Auto-update via migration:** `InstallOpencodePlugin()` runs during `yesmem update` — SHA256 comparison ensures only changed plugin files are written.
- **Git branch awareness:** The rule_guard detects the current git branch via `getGitBranch()` and includes it in the evaluation context — suppressing false feature-branch suggestions (e.g., no need to suggest worktree/branch skills on a feature branch).

### Project Exclusion

- **`exclude_projects` config:** Projects can be excluded from session indexing by adding them to `exclude_projects` in config.yaml. Prevents noise directories from being tracked.
