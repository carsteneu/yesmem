# Developer Tools

## 1. Documentation Management

Domain-agnostic knowledge ingestion for arbitrary documentation (code, marketing, legal, finance, PubMed, company knowledge).

### Ingest Pipeline
1. **File discovery:** Walk directories, find `.md`, `.txt`, `.rst`, `.pdf`
2. **Format-aware chunking:** Detects file format and applies the appropriate parser:
   - **Markdown** (`.md`, `.txt`): goldmark AST parser extracts headings
   - **reStructuredText** (`.rst`): Custom heading parser recognizes underline/overline patterns (`===`, `---`, `~~~` etc.). Level determined by order of first adornment character appearance. No external dependency.
   - Both formats: Cuts along heading boundaries, each chunk carries full `heading_path` (e.g. "Messenger Component > Configuration > Retry Strategy"). A section under 200 chars is merged into the **preceding** section (not into its parent heading), a section over 8000 chars is split at blank lines only (`\n\n`)
3. **Rich metadata extraction** per chunk:
   - **Markdown**: `languages` (from fenced code blocks ` ```php `), `code_example`, `has_table`
   - **RST**: `languages` (from `.. code-block:: php`), `deprecated_since`, `version_added`, `version_changed`, `admonition` (warning/tip/note/...), `rst_entities` (`:class:`, `:func:`, `:method:` cross-references), `rst_doc_refs` (`:doc:` links), `code_example`, `has_table`
   - Stored as JSON in `doc_chunks.metadata`, indexed in FTS5
4. **Content hashing:** `source_hash` prevents re-ingesting unchanged files
5. **FTS5 indexing:** Porter stemmer + unicode61 + `tokenchars '_-.'` — technical terms like `text_editor`, `go1.24` indexed as single tokens
6. **SSE embedding:** Each chunk gets a 512d vector (SSE with DyT normalization) for semantic search
7. **Git-aware sync:** Detects `.git` in path → `git pull` before import

### Skill Mode
There is no **producer** for skill sources any more: no CLI or daemon path registers a skill as a doc source (the `is_skill` flag is only ever written as `false`). The **reader** side survives — `handleGetSkillContent` in `internal/daemon/handler_skills.go` still serves a skill body from `doc_sources.full_content`, and the proxy calls it from `skill_hints.go` and `collapse_skills.go`. Existing skill rows therefore keep working; new ones are not created.

### Search Architecture
`docs_search()` runs both lanes for every non-`exact` query and merges them:
- **BM25** (FTS5): term-existence filter removes dead terms, IDF-sort (rarest first), tiered AND-fallback (5→4→3→2 terms)
- **Vector** (SSE 512d): cosine similarity. It runs even when BM25 already has hits; only `exact: true` stays BM25-only
- **RRF merge**: Reciprocal Rank Fusion (`k = 60`) combines both ranked lists when both have results, otherwise returns whichever method produced results. The reported score is the fused RRF value

### Doc versioning
Runs via keywords (`symfony:5`, `symfony:7`) and embedding match — no separate version system. Code context + vector search automatically determine relevant version.

### CLI Commands
```
yesmem add-docs  --name symfony --path /docs/symfony --version 7.2 --project myapp
yesmem add-docs  --name press --path /docs/press --domain marketing
yesmem sync-docs --name symfony          # re-sync single source
yesmem sync-docs --all                   # re-sync everything
yesmem list-docs --project myapp         # show registered sources
yesmem remove-docs --name symfony        # delete source + chunks
```

Two caveats on `add-docs`:
- There is **no** `--trigger-extensions` flag on the CLI. `trigger_extensions` is only settable through the `ingest_docs` RPC/MCP path; use `docs_search(extensions=[...])` to filter by extension instead.
- `--domain` is accepted and passed into the ingest config, but `doc_sources` has no domain column, so the value is not persisted.

---

## 2. Hook System

YesMem hooks into Claude Code at multiple event points.

### Active Hooks

| Hook | Claude Code Event | Purpose |
|------|-------------------|---------|
| `briefing-hook` | SessionStart | Registers the session's PID (`RegisterPID` + PID file) and prints `{}`. It does **not** inject the briefing — the proxy does that as a user/assistant turn pair |
| `hook-check` | PreToolUse | Code-navigation hint (indexed paths) + knowledge-aware gotcha injection + hard block on repeat offenders |
| `hook-guard` | PreToolUse | Evaluates the tool call against `RULES.md` via LLM (see §10c) |
| `hook-failure` | PostToolUseFailure | Learn gotcha from the failure + deep search for solutions (combined) |
| `hook-resolve` | PostToolUse | Auto-resolve open `unfinished` items when a git commit message matches |
| `session-end` | **SessionEnd** (not Stop) | Session tracking for clear/compact recovery, pin cleanup, extraction trigger |
| `idle-tick` | UserPromptSubmit | Dynamic yesmem-usage reminder when idle |

`hook-think` no longer exists: setup actively removes a stale entry (`removeThinkHook`), and the reminder injection it used to do is handled by the proxy.

### Knowledge-Aware PreToolUse Check (`hook-check`)

The core of YesMem's proactive knowledge injection. Fires on every Bash, Edit, and Write tool call and does two things: it suggests the code-navigation tools when a shell command targets an indexed file, and it injects relevant gotchas **before** the action happens.

The recognised navigation commands are `grep`, `egrep`, `fgrep`, `rg`, `sed`, `awk`, `cat`, `head`, `tail`. `find` is **not** in that set, so directory-tree searches get no code-nav hint.

#### How matching works

1. **Keyword extraction** from the tool input:
   - **Bash:** `extractKeywords(command)` — splits command into significant tokens
   - **Edit/Write:** `extractPathKeywords(filePath)` — path components, filename, extension
2. **Scoring** against all active gotcha learnings:
   - **Content matching:** keyword overlap with learning text (score ≥ 2 for Bash, or ≥ 1 with long keyword match ≥6 chars; score ≥ 2 for file ops, or ≥ 1 with filename match). A gotcha auto-learned from a Bash failure never uses the file-op filename shortcut: on Edit/Write it needs score ≥ 2 **and** overlap outside the target path and the file name — the pending file op does not run the command that failed (issue #151)
   - **V2 Entity/Action matching:** direct comparison against structured `learning_entities` and `learning_actions` junction data — more precise than keyword overlap
   - **Long keyword bonus:** single match on keywords ≥ 6 chars counts (avoids noise from short tokens)
3. **Project scoping:** matches split into project-specific (max 3) and global (max 2) buckets. If 0 project matches → global gets 3 slots.

#### Escalation levels

| Condition | Behavior | Exit Code |
|-----------|----------|-----------|
| Gotcha matched, `fail_count < 5` | Warning via `additionalContext` — Claude sees it, can proceed | 0 |
| Gotcha matched, `fail_count ≥ 5`, match score ≥ 4, `source=hook_auto_learned` | **Hard block** — tool call prevented entirely | 2 |
| Edit/Write to protected file (`yesmem-ops.md`) | **Hard block** — must fix source learning instead | 2 |

The score requirement (`blockMinScore = 4`) is easy to miss: a gotcha with high `fail_count` but a weak match to the current command only warns. Imported rows (`origin_tool = import`) are excluded from blocking, because their counters were observed in another installation.

#### Protected files

Auto-generated files are blocked from direct editing:
- `yesmem-ops.md` — generated from learnings, fix via `remember()` + `supersedes` parameter
- Block message guides Claude to the correct workflow

#### save_count heuristic

Measures whether warnings actually prevented errors:
1. When gotchas are shown, state is persisted: `last_gotcha_ids`, `last_gotcha_tool`, `last_gotcha_input_hash`
2. On the **next** matching tool call: if same tool type but **different** input hash → Claude changed approach after seeing warning → `save_count++` for those gotchas
3. State cleared after check (one-shot) — prevents double-counting

#### Gotcha Injection Decay (Historical Usefulness)

`injectionDecay` scales a gotcha by how useful it has been historically, and only after 20 injections: below `decayMinInjections = 20` the factor is 1.0. Above it, the effective uses (`use_count + save_count×2`) divided by `inject_count` form a precision ratio; at or above 0.1 the factor stays 1.0, below it the factor falls linearly to a floor of 0.3. A gotcha that keeps being shown and never helps fades, but never disappears.

#### Feedback loop with `hook-failure`

The PreToolUse check and PostToolUseFailure hook form a closed learning loop:
- **`hook-failure`** auto-learns a gotcha from **any** tool failure, not just Bash (`Bash`, `WebFetch`, `Read`, `Grep`, and a generic fallback for everything else). It writes **directly into `yesmem.db`** (`storage.Open` + `InsertLearning`) with `source=hook_auto_learned` and **no project**, so the gotcha is global. A near-duplicate (TokenSimilarity ≥ 0.5) bumps `match_count`/`fail_count` instead of creating a row
- **`hook-check`** surfaces those gotchas before the same mistake repeats
- After 5 repeated failures → hard block escalation (see the score requirement above)
- After fix confirmed → `resolve_by_text()` or the git-commit auto-resolve removes the block. `hook-resolve` only touches `unfinished` items; it never clears gotchas

### Session Recovery (Clear/Compact)

After `/clear` or `/compact`, Claude Code sends a new session ID. YesMem provides seamless recovery:
- **session-end hook** captures the old session ID via daemon RPC (not direct DB access — avoids SQLite locking issues with concurrent proxy/daemon)
- **briefing-hook** looks up the tracked session and generates a recovery briefing
- Recovery block is injected **post-refine** so it survives the briefing refinement pass
- Deterministic: no guessing, uses the exact session that just ended

### REPL Pattern Detection

YesMem detects repeating tool-use patterns across conversation turns and suggests converting them into reusable capabilities (caps).

**How it works:**
- Each turn's tool sequence is hashed into a compact signature and stored per thread in the **`thread_sequences`** table (`turn_hashes` JSON per thread), not in one row per turn
- Recurring **sub-sequences** are found by comparing hash lists across turns (`GetWorkflowSuggestions`, minimum count 3)
- The proxy asks the daemon for a ready suggestion (`get_repl_pattern_suggestion`, threshold default 3) and, if one exists, posts it as a growth-path hint. The suggestion text points at the matching capability with `cap_search(name=...)` and `cap_collect` — it does not tell the agent to `save_cap()` something new
- `record_repl_pattern` tracks a shape; the daemon also stores which active cap covers it (`matched_cap`, recognised from a `cap_<name>__<table>` reference in the command)
- The RPC response carries both a `pattern` and a `workflow` block. Only `pattern` reaches the rendered hint; the `workflow` variant is returned but never formatted into a suggestion
- Users can dismiss suggestions via `dismiss_repl_pattern(project, shape_hash)`. After 3 dismissals the pattern is permanently suppressed and never suggested again. There is no separate `patternBudget`

---

## 3. OpenCode Plugin

YesMem ships a TypeScript plugin for opencode's plugin API: **`plugins/opencode-yesmem-v2/`**. The v1 plugin (`plugins/opencode-yesmem/`) is no longer in the repository and is not loaded anywhere — where this document used to describe v1 behaviour, the v2 behaviour is what runs.

### Installation

The plugin is embedded in the yesmem binary via `go:embed` (`plugins/embed.go`, `opencode-yesmem-v2/*.ts`). During `yesmem setup`, `InstallOpencodePlugin` installs it to `~/.config/opencode/plugins/opencode-yesmem/` and registers it in `opencode.json`. A Unix socket RPC layer (`rpc.ts`) forwards all tool calls to the yesmem daemon via `daemon.sock`.

### Hooks

The plugin registers more than five hooks. `index.ts` wires them in this order:

| Registration | Components | Purpose |
|---|---|---|
| `tool.execute.before` | `code_nav`, then `rule_guard`, plus `injectYesMemToolContext` | Code-nav block, then the RULES.md guard; the first non-empty message rejects the call |
| `tool.execute.after` | `rule_guard.after`, `failure_learn`, `auto_resolve` | SUGGEST prefix (guard may rewrite the error first), then gotcha learning, then commit auto-resolve |
| `session.hook("context")` ×4 | `guard.capture`, `skill_nudge`, `ledger_nudge`, `idle_reminder` | Capture the user message before any patch, then three nudges that each touch only the terminal user message |
| `shell.hook("create.before")` | `shell` | Inject `YESMEM_SOCKET` / `YESMEM_SOURCE_AGENT` into the shell environment |
| `event.subscribe()` | `session` | Session tracking: register the owning process for every session that starts |

So the components are nine (`code_nav`, `rule_guard`, `failure_learn`, `auto_resolve`, `skill_nudge`, `ledger_nudge`, `idle_reminder`, `shell`, `session`). `idle_reminder` has **no timer of its own**: it is a message-context hook that decides on the turn, not a periodic task.

### Shell Environment

The plugin injects `YESMEM_SOCKET` (pointing to `daemon.sock`) and `YESMEM_SOURCE_AGENT=opencode` into every shell environment. This enables the bash-polyfill layer for cap scripts (see §25) and ensures the daemon can attribute sessions to the correct source agent.

### Session Lifecycle

On `session.created`, the session tracker registers the session with the daemon via `register_pid` RPC — mapping the opencode session ID to the yesmem PID-session infrastructure. This enables:
- Agent-to-agent messaging (send_to, broadcast) for opencode sessions
- Session identity resolution via PID reverse-lookup
- Briefing injection by the proxy (which handles opencode parity requests)

At startup, the plugin primes the code index through the guard (`codeNav.prime()`, forked so activation does not stall) so the CBM code graph is warm before the first user prompt.

### Architecture

```
opencode session
  → opencode-yesmem-v2 plugin (TypeScript/Bun)
    → rpc.ts (Unix socket to daemon.sock)
      → yesmem daemon (Go)
        → RPC handlers (search_code_index, register_pid, store_learning, etc.)
```

The plugin contains no business logic — it intercepts opencode events, normalizes them into RPC calls, and injects the daemon's responses back into opencode's output. All intelligence (search, code graph, learning storage, rule evaluation) lives in the Go daemon — the plugin is a thin integration layer, exactly like the MCP server.

### 10c. rule_guard — Tool-Call Compliance

The `rule_guard` hook is the plugin's most iterated component (~30 commits just for format tuning). It decides whether to block, suggest, or pass a tool call. The judgement is not made in TypeScript: `rule_guard.ts` is an adapter over the daemon RPC `guard_check`, which runs the same core (`internal/guard`) as the Claude Code hook. The plugin picks the event, builds the context the daemon cannot see, calls the RPC and renders the verdict.

#### How it works

1. **The daemon loads the rules** — `guard.LoadRules` reads `RULES.md` from the call's `cwd` (falling back to the installation's copy), and `guard.PromptRules` keeps the lines matching `^\d+\.\s` and appends the `## Decision Policy` and `## Skill Catalog` sections. The plugin sends no rules and caches no rules; a `RULES.md` edited mid-session is read on the next call, and the daemon's 5 s answer cache is keyed on the resulting text.

2. **Evaluation** — On every `tool.execute.before` event the adapter sends tool name, tool input, cwd, session id, the recent conversation and a purpose (`guard`) to `guard_check`. The daemon's core resolves the model (see `docs/features/guard.md`), asks it non-streaming, and returns `BLOCK`, `SUGGEST`, `PASS` or `UNAVAILABLE`. The adapter keeps a 5 s event cache so an identical call is not sent twice.

3. **BLOCK** — The tool call is rejected (`ToolError`) when the tool may be refused (`write`, `edit`, `patch`, `shell`) and the core calls the decision enforceable. The message names the violated rule, collapsed to one line. A BLOCK the adapter may not enforce, or one the core downgraded, becomes an advisory note in the tool result instead.

4. **SUGGEST** — The tool call proceeds, but a deferred directive is injected via `tool.execute.after`. The prefix is `[guard] ⚠️ MANDATORY CHECK — activate <skill> (use Skill tool)`. Used when the agent should have activated a skill before acting (e.g. bash call before memory search).

5. **PASS** — No action; the tool call proceeds normally.

#### Exemptions

`skipTools = {skill, subagent, todowrite}` are never evaluated. Additionally skipped:

- **Non-blockable tools** — `canBlock` is true for `write`, `edit`, `patch` and `shell`. `read`, `grep` and `glob` can receive a SUGGEST but never a hard BLOCK. A call the guard cannot judge (the model endpoint refused the request) is refused for `write`, `edit` and `patch`; a `shell` call and every other non-writing tool run and carry a `RULE GUARD UNAVAILABLE` notice instead. An unreachable daemon is not that case: it is a `PASS` with a reason
- **Documentation targets** — a call whose file path ends in `.md`, `.txt`, `.rst`, `.pdf`; for `grep` the `include` pattern and for `glob` the `pattern` are checked the same way. A `shell` call has no file path, so the three path-based exemptions below and above cannot apply to it
- **Edits to `RULES.md` or `rule_guard.ts`** — exempt so the guard cannot block its own maintenance (file tools only)

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

The final stable configuration (v2 hook names):
- `tool.execute.before` → `ToolError` for BLOCK (hard stop)
- `tool.execute.after` → output prefix for SUGGEST (non-blocking directive)
- `session.hook("context")` → capture the last user message for the guard's context (v2 replaces v1's `experimental.chat.messages.transform` / `chat.params` pair)
- All logging goes to `~/.claude/yesmem/logs/plugin.log` via `dbgLog()` (never `console.*` — that corrupts opencode's TUI)

#### API Key Resolution

The plugin does not resolve a key. `guardmodel.Resolve` in the daemon does, and `docs/features/guard.md` lists the order: `YESMEM_GUARD_URL`/`_KEY`/`_MODEL`/`_NPM`, then the provider search over `~/.cache/opencode/models.json` and `~/.local/share/opencode/auth.json` (config.yaml names the model), then `proxy.provider_targets` with the opencode `gateway` key, then an Anthropic credential. With no resolvable key the decision is `PASS` with the reason `no guard credential`, and the destructive pre-check still runs.

---

## 4. Code Intelligence

YesMem includes a language-agnostic code intelligence system that scans, indexes, and exposes codebase structure through MCP tools and proxy-injected context.

### 18.1 Architecture

The system consists of three layers:

1. **Scanner** — CBM CLI (`codebase-memory-mcp`), an external binary that parses source files using TreeSitter grammars. Extracts functions, types, methods, imports, call edges. Auto-downloaded during `yesmem setup` if not present. Its index lives outside yesmem at `~/.cache/codebase-memory-mcp/<project>.db`
2. **Scan cache** — yesmem's own SQLite tables `project_scan` (the scan JSON plus a git-head and CBM-mtime fingerprint) and `project_scan_files` (per-path membership, so "is this file indexed?" is one indexed lookup). The cache key is the **repository root path**: a linked worktree resolves to its main repository, so one repository has one cache row whichever worktree is scanned
3. **MCP Tools** — Eight tools expose the graph to Claude Code sessions.

### 18.2 Scanning

Scanning runs on daemon startup, on demand through the wiki render path (`yesmem wiki-render`), and on demand from the code-intelligence RPCs (`getCodeGraph` in `handler_code_intelligence.go`, `code_describe.go`). There is no `yesmem scan` subcommand. The scanner:

- Walks the project directory, respecting `.gitignore`
- Reuses the cached scan when the git head and the CBM index mtime are unchanged (per-path membership in `project_scan_files`)
- Extracts symbols (functions, types, methods, constants, variables) with line ranges
- Extracts edges (imports, calls, defines) between symbols
- Stores the result in `project_scan` + `project_scan_files`; the symbol graph itself lives in the CBM database

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

The proxy generates a Code Map summary and injects it as a separate turn pair at session start (a `<system-reminder>` user turn plus the fixed acknowledgement "Understood. I've read the code map."), right after the briefing pair. The Code Map actually renders:

- **Package table** — all packages with file count, description, and gotcha count
- **Entry points** — files with `main()` or standalone execution capability
- **Code health** — test coverage ratio, TODO/FIXME counts

Three further blocks exist in `render.go` but are never called, so they never reach the output: `renderKeyFiles`, `renderChangeCoupling` and `renderActiveZones` (the scan result does not even carry the data they render). Treat "Key Files", "Change coupling" and "Active Zones" as planned, not shipped.

The Code Map is injected as its own turn pair, separate from the briefing, so it doesn't compete for the system prompt cache breakpoint.

### 18.5 Code Navigation Hook

The code-navigation hook prevents agents from reaching for shell tools when the target file is already indexed in the CBM code graph. If the code graph has the file, shell-based navigation (grep/cat/find/sed/rg) is unnecessary — code tools are faster and provide richer context (callers, symbols, co-edits, gotchas).

**Implementation (opencode plugin):**

The `code_nav` hook in the `opencode-yesmem-v2` plugin fires on `tool.execute.before`. It checks whether the target path exists in the code graph and, if so, rejects the call with a tool-specific suggestion:

```
YesMem: BLOCKED <scope>
  → get_file_symbols("<file>", "<project>") for overview, or get_code_snippet(file="<file>", project="<project>", start_line=x, end_line=y)
  Call sites of a function: search_code("<symbol>(", "<project>") returns them with file and line.
  If you need this <tool> anyway: run the same command again, or call dismiss_code_nav(session_id="<session>") to lift the block for this session.
```

A `read` carries `run this read again` instead of `run the same command again`; the escape names both routes because both work (YM-425).

Blocked shell commands: `grep`, `egrep`, `fgrep`, `rg`, `sed`, `awk`, `cat`, `head`, `tail`, `find`. Blocked tool: `read`. Note that this is the opposite of the Claude Code hook, which does not treat `find` as a navigation command.

**Dismissal:**

The suggestion uses a 2-strike auto-allow system with 1h TTL. The first attempt is blocked with a yesmem tool suggestion, the second attempt is auto-allowed.

**Mode (`code_nav_mode`):**

Both layers read `proxy.code_nav_mode`, so the setting has one meaning for Claude Code and opencode:

- `block` (default): the call is rejected, with the two-strike rule and `dismiss_code_nav` as ways past it. This is the only mode in which those two apply.
- `nudge`: the call runs. `tool.execute.before` cannot carry a message, so the hint is recorded per tool call and `execute.after` prefixes the result with `[code_nav] HINT: yesmem has code-navigation tools for indexed file …` — the same advice the Claude Code hook prints to stdout.
- `off`: nothing is checked and nothing is said.

The plugin re-reads `~/.claude/yesmem/config.yaml` at most once a minute, so a changed value lands without restarting opencode.

**Legacy support (Claude Code):**

For Claude Code sessions (which don't run the opencode plugin), the older `hook-check` PreToolUse hook detects shell commands (`cat()`, `grep`) on indexed paths and suggests MCP code tools. Same behavior, different integration layer.

**Impact:**

- The code graph tracks which sessions touched which files (`yesmem_related_to_file`)
- CBM scanner re-parses only when the git working tree changes (cached `CodeGraph`)

### 18.6 Worktree Awareness

Two different keys are in play, and they do not agree:

- **Scan cache (`project_scan` / `project_scan_files`)** is keyed by the **repository root path**. `ScanCacheKey` walks up to the directory holding `.git` and then resolves a linked worktree to its main repository, so every worktree of one repo shares one cache row. A submodule stays separate (its gitdata sits in `.git/modules`, and folding it into the superproject would let one scan overwrite the other's file list)
- **CBM index file** is keyed by the **scan directory**: `cbmProjectName` takes the resolved scan root and replaces `/` with `-`, producing `~/.cache/codebase-memory-mcp/<path-with-dashes>.db`. A worktree therefore gets its own CBM index file

Practical consequence: the cached file list is shared, the CBM symbol index is not. Scanning inside a worktree does not refresh the CBM index the main checkout reads, even though both write the same cache row.

### 18.7 Wiki Export — Rendered Knowledge Base

The daemon periodically renders all YesMem learnings, sessions, files, and code intelligence data into a browsable, pure-Markdown wiki at `~/.claude/yesmem/wiki/<basename>/` (`internal/wikidir` is the single source of truth for that path, and every reader resolves through it — code-map header, `[yesmem-wiki-first]` and docs). Two live projects that share a basename are disambiguated: the owner recorded in `<dir>/.project` keeps the plain basename, every other one gets `<base>-<8 hex of the cleaned path>`. This gives agents (and humans) a static, always-available entry point into the accumulated knowledge of a project — without needing the daemon to be running, and without any MCP tool calls.

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

- **wiki-tick:** The daemon runs a 5-minute ticker (`startWikiTicker`). On each tick, it iterates over all active projects and checks whether the wiki needs re-rendering. Projects whose latest session is older than 24h are "cold" and render at most every 20h.
- **Change detection:** A snapshot file (`.wiki-snapshot.json`) records learning and session counts plus the newest timestamps, the summed content length and the file/session coverage sums. If nothing changed since the last render, the tick skips that project entirely — zero I/O, zero CPU.
- **Write-skip:** Each rendered file is compared byte-for-byte against the existing file (`bytes.Equal`) and only written when it actually differs.
- **Code graph caching:** The codescan result is cached per project via `CachedScanner`. If the git working tree hasn't changed since the last scan, the scanner returns the cached scan — avoiding a full TreeSitter re-parse.

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
yesmem wiki-render --project yesmem

# View the rendered wiki in a browser or file viewer
ls ~/.claude/yesmem/wiki/yesmem/
cat ~/.claude/yesmem/wiki/yesmem/index.md
```

The command is `yesmem wiki-render`. `wiki_export` is **not** a bundled cap in the repository (`caps/bundled-caps/`); a `wiki_export` capability may exist as a locally saved cap, but it is not shipped and not required for scheduled rendering.

---


---

## RULES.md — Policy Engine

A declarative rule file with 30+ rules and a skill catalog. Rules are evaluated against write, edit and patch calls before execution; every other tool can at most receive a suggestion.

**Architecture:**
- `RULES.md` — Rule definitions with activation triggers (tool names, patterns, contexts)
- **Skill Catalog** — 20 entries in the current `RULES.md`, each with `id`, `skill`, `priority`, `triggers` and `rule`. The section is handed to the model as text, not parsed
- **Which calls are evaluated** — `write`, `edit`, `patch` can be blocked; everything else is suggest-only. `skill`, `subagent` and `todowrite` are skipped entirely, and so are documentation-targeted calls (`.md`/`.txt`/`.rst`/`.pdf`). Not every tool call is evaluated.

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
