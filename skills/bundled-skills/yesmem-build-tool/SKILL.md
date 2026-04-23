---
name: yesmem-build-tool
description: Use when a user wants to persist a working script, REPL snippet, or multi-step workflow as a reusable capability available in future sessions. Also use when someone references cap_store, save_cap, auto_active, capblob-pipe, or asks "how do I make this reusable across sessions".
---

# Build Tool — Capability Creation

A capability (cap) is a JS/bash handler plus optional cap_store schema that yesmem persists in the learnings DB and auto-injects into future Claude sessions via the proxy. The pattern turns ad-hoc REPL scripts into session-spanning, queryable tools.

## When to Use

- User has a working command/script they want to reuse across sessions
- User asks: "save this as a tool", "build a capability", "make this reusable", "/build-tool"
- Task solved required a multi-step REPL sequence likely to recur
- Data produced by the task deserves to be queryable later (search, aggregate, meta-analyze)

## When NOT to Build a Cap

- One-off command (e.g. single `curl` for a specific URL today only)
- Trivial bash < 20 chars the user can retype
- Handler has zero reusable value — it's tied to a one-time context
- Data isn't worth persisting (logs already elsewhere, ephemeral state)
- Wrapping existing MCP tools 1:1 with no new logic — call the MCP tool directly

When in doubt: ask the user if they'd reach for this next week. If not, don't build it.

## The 6-Step Workflow

### Step 1: Define

- **name**: snake_case, unique, findable (e.g. `hn_top`, `youtube_transcript`)
- **description**: one sentence, written as discovery bait — Claude reads it to decide "should I call this?" Be specific about triggers, not generic categories:
  - Bad: `Fetches web data`
  - Good: `Fetch the top N HackerNews stories and persist them into cap_hn_top__stories, queryable by score/title/date`

  A generic description makes the cap effectively invisible to future Claude.
- **inputs**: name + type + required?
- **persists data?** if yes → Step 2b; if no → skip to Step 3
- **scope**: start minimal. If the user wants "all metadata" / "everything", resist — pick the 3-5 fields they'll actually query on. Adding fields via supersede later is cheap; un-creeping a cap that dragged you into 20+ columns and parser escape-hell is not. Scope creep is the #1 cause of half-finished caps.

### Step 2a: Handler

**handler_repl** (runs in Claude's JS VM, richer):
```javascript
async ({limit = 10}) => {
  const raw = await sh('curl -s --max-time 15 "https://hacker-news.firebaseio.com/v0/topstories.json"');
  const ids = JSON.parse(raw).slice(0, limit);
  // ... fetch each, persist, return
}
```

**handler_bash** (portable, callable outside REPL):
```bash
curl -s --max-time 15 "https://hacker-news.firebaseio.com/v0/topstories.json" | head -c 1000
```

Most caps have both. Hard rules:
- Bash single-line (chain with `&&` or `;`). No heredoc.
- Timeouts ≤ 20s on shell and HTTP.
- Outputs > 30KB → capblob-pipe (see below).

**REPL globals available in handlers:** `sh()`, `cat()`, `rg()`, `gl()`, `haiku(prompt, schema?)`, `mcp__yesmem__*()`. `haiku()` routes through the Claude Code process — no API key needed, no env variable, no config. Use it for lightweight classification, extraction, or synthesis inside handlers.

### Step 2b: cap_store Table (if persisting)

```javascript
await mcp__yesmem__cap_store({
  capability: 'hn_top',
  action: 'create_table',
  table: 'stories',
  columns: JSON.stringify([
    {name: 'story_id', type: 'INTEGER'},
    {name: 'title',    type: 'TEXT'},
    {name: 'url',      type: 'TEXT'},
    {name: 'score',    type: 'INTEGER'},
    {name: 'fetched_at', type: 'INTEGER'}
  ])
});
```

**Auto-added columns — NEVER include in your list:**

| Column | Auto-added | Purpose |
|---|---|---|
| `id` | INTEGER PK AUTOINCREMENT | row id |
| `created_at` | DATETIME DEFAULT CURRENT_TIMESTAMP | insertion time |
| `updated_at` | DATETIME DEFAULT CURRENT_TIMESTAMP | update time |

Including any of them → `duplicate column name` error. See `docs/cap-store-analysis.md` for schema details and WHERE-sanitizer rules.

**Upsert semantics — the `id`-trick:**

| `upsert` call shape | Result |
|---|---|
| `data: {url: '...', title: '...'}` (no `id`) | **INSERT** — new row, new auto-`id`, new `created_at` |
| `data: {id: 42, url: '...', title: '...'}` | **UPDATE** row 42 in place |

Time-series capture (same URL, N fetches over time, one row per fetch) works by **omitting `id`** so every call appends — you get a `fetched_at` history for free, no extra schema. Accidentally passing `id` overwrites history silently. Pick intentionally; the API shape makes both choices look identical until you query.

### Step 3: Test

Run the handler with real inputs in the REPL before saving:

```javascript
const r = await handler({limit: 3});
r;                                          // check return shape
await mcp__yesmem__cap_store({capability:'hn_top', action:'query', table:'stories'});  // persisted?
await handler({limit: 3});                  // re-run: idempotent, no duplicates?
```

Verify:
- Output structure matches what Claude will consume downstream
- Persisted rows exist with expected column values
- Re-running yields dedup, not duplicates (upsert via id or a uniqueness check)
- Error paths: network fail, empty result, malformed input
- Size: if output approaches 30KB, switch to capblob-pipe now (not later)

### Step 4: Schema & Metadata

Input schema (JSON Schema). Claude reads this to call the cap correctly:
```json
{
  "type": "object",
  "properties": {
    "limit": {"type": "integer", "description": "Number of top stories, default 10"}
  },
  "additionalProperties": false
}
```

- `additionalProperties: false` on every object level (top + nested) — required by Anthropic tool-use spec, prevents schema drift
- `tags`: comma-separated, reuse existing (`web`, `fetch`, `cap_store`, `analysis`)
- `auto_active: true` → proxy injects into every session automatically. Use ONLY for universally useful caps (see Common Mistakes)

### Step 5: save_cap

```javascript
await mcp__yesmem__save_cap({
  name: 'hn_top',
  description: 'Fetch top N HackerNews stories and persist into cap_hn_top__stories',
  handler_repl: '...',  // full JS source
  handler_bash: '...',
  schema: JSON.stringify({...}),
  tags: 'web,hn,fetch,cap_store',
  tested: true,
  test_date: '2026-04-18',
  auto_active: false,   // start false; enable later if truly universal
  supersedes: 0         // omit or set to prior id when replacing
});
```

Re-saving with same `name` auto-supersedes the old row, bumps version. Use explicit `supersedes: <id>` only when replacing across a rename.

### Step 6: Verify

```javascript
// 1. Cap is in DB
await mcp__yesmem__get_caps({name: 'hn_top'})

// 2. Register in this thread (for non auto_active)
await mcp__yesmem__activate_cap({name: 'hn_top'})
// → returns registerTool(...) snippet; paste into REPL, confirm it loads

// 3. Call it
await hn_top({limit: 5})

// 4. Data is persisted
await mcp__yesmem__cap_store({capability: 'hn_top', action: 'query', table: 'stories'})
```

After superseding a schema, existing threads must re-`activate_cap` — proxy caches per-thread.

## Complete Example: hn_top

End-to-end build of a Hacker News top-stories cap. All 6 steps, ~40 lines.

```javascript
// Step 1+2a+2b+3: build and test in REPL first
const handler = async ({limit = 10}) => {
  const HN = 'https://hacker-news.firebaseio.com/v0';  // inline — closure vars don't survive handler.toString()
  // Step 2b: idempotent table create
  await mcp__yesmem__cap_store({capability:'hn_top', action:'create_table', table:'stories',
    columns: JSON.stringify([
      {name:'story_id',type:'INTEGER'},{name:'title',type:'TEXT'},
      {name:'url',type:'TEXT'},{name:'score',type:'INTEGER'},
      {name:'fetched_at',type:'INTEGER'}
    ])});
  const raw = await sh(`curl -s --max-time 15 "${HN}/topstories.json"`, 20000);
  const ids = JSON.parse(raw).slice(0, limit);
  const now = Math.floor(Date.now()/1000);
  const stories = [];
  for (const id of ids) {
    const s = JSON.parse(await sh(`curl -s --max-time 10 "${HN}/item/${id}.json"`, 12000));
    if (!s?.title) continue;
    const row = {story_id:id, title:s.title, url:s.url||'', score:s.score||0, fetched_at:now};
    await mcp__yesmem__cap_store({capability:'hn_top', action:'upsert', table:'stories',
      data: JSON.stringify(row)});
    stories.push(row);
  }
  return {count: stories.length, stories};
};

// Step 3: test with real data
const test = await handler({limit: 3});  // verify shape, no crashes, stored rows

// Step 4+5: save
await mcp__yesmem__save_cap({
  name: 'hn_top',
  description: 'Fetch top N HackerNews stories and persist them queryable',
  handler_repl: handler.toString(),
  // no handler_bash — nested per-item fetches need loop + parse logic cleaner in JS
  schema: JSON.stringify({
    type:'object',
    properties: {limit:{type:'integer',description:'How many stories (default 10)'}},
    additionalProperties: false
  }),
  tags: 'web,hn,fetch,cap_store',
  tested: true,
  test_date: '2026-04-18',
  auto_active: false
});

// Step 6: verify
await mcp__yesmem__get_caps({name: 'hn_top'});
await mcp__yesmem__activate_cap({name: 'hn_top'});
// paste returned registerTool(...) into REPL
await hn_top({limit: 5});
await mcp__yesmem__cap_store({capability:'hn_top', action:'query', table:'stories',
  where:'score > ?', args:JSON.stringify([100])});
```

Follow-up in later sessions:
```javascript
// Search stored HN history
await cap_search({cap:'hn_top', table:'stories', where:"title LIKE '%LLM%'", all:true});

// Analyze a subset
await cap_collect({cap:'hn_top', table:'stories',
  where:"fetched_at >= ?", args:[weekAgo],
  instruction:'Welche Themen dominieren diese Woche?'});
```

## Complete Example: cap_delete (destructive, TDD)

More involved than hn_top — multi-DB, destructive, built TDD-style in REPL. Registered as `cap_delete` (`auto_active: false`; invoke explicitly).

**Scenario:** Remove a capability completely. Touches `learnings` + 5 FK-cascade child tables + FTS + `session_active_capabilities` in `yesmem.db`, then all `cap_<name>__*` data tables + `cap_store_meta` row in `capabilities.db`.

**TDD loop in REPL** — the handler under test is also its own teardown:

```javascript
const TARGET = 'cap_delete_test_target_' + Date.now();
// Seed: save + activate + one cap_store table + one row
await mcp__yesmem__save_cap({name: TARGET, description: 'TDD target', handler_repl: 'async () => "noop"', schema: JSON.stringify({type:'object',properties:{},additionalProperties:false})});
await mcp__yesmem__activate_cap({name: TARGET});
await mcp__yesmem__cap_store({capability: TARGET, action: 'create_table', table: 'dummy', columns: JSON.stringify([{name:'v',type:'TEXT'}])});
await mcp__yesmem__cap_store({capability: TARGET, action: 'upsert', table: 'dummy', data: JSON.stringify({v:'hi'})});

// Pre-assertion: 4 counts [learnings, session_active, cap_*__* tables, cap_store_meta] all == 1
// Run the handler under test (which deletes everything)
const result = await handler({cap_name: TARGET});
// Post-assertion: same 4 counts all == 0, result.deleted.{learnings, data_tables} populated
```

**Iteration-1 bug (worth knowing):** `cap_store_meta` has two table-name columns — `table_name` is the *logical* name (`"dummy"`), `full_name` is the *physical* one (`"cap_<cap>__dummy"`). Filtering `table_name` against a `cap_<...>__<...>` regex silently yields zero matches → orphan tables after the `DELETE FROM cap_store_meta`. Fix: skip the meta mapping, query `sqlite_master` directly:

```javascript
const tbls = (await sh(`sqlite3 ${cdb} "SELECT name FROM sqlite_master WHERE type='table' AND name LIKE 'cap_${cap_name}__%';"`))
  .trim().split('\n').filter(s => /^cap_[a-zA-Z0-9_]+__[a-zA-Z0-9_]+$/.test(s));
```

The live schema is ground truth; meta-tables can desync.

**Patterns reinforced by this cap:**

| Pattern | Why |
|---|---|
| Whitelist input at the boundary (`/^[a-zA-Z][a-zA-Z0-9_]*$/`) | destructive SQL — escaping alone is too error-prone |
| `BEGIN IMMEDIATE` transactions on both DBs | partial failure leaves harder-to-debug residue than a full rollback |
| Check `knowledge_gaps.resolved_by` FK before deleting | one of the few non-CASCADE FKs in the schema; silent break otherwise |
| Self-destructing test target | no fixture teardown to maintain — the test IS the teardown |
| Query `sqlite_master` for real tables, not a meta-mapping | meta can desync; schema cannot |

Full handler source: `get_caps({name:'cap_delete'})`.

## Dependency Caps

When a handler calls other caps (e.g. `reddit_search`, `reddit_fetch`, `cap_save_analysis`), those caps must be activated in the session before the composite cap runs. Otherwise: `ReferenceError` at runtime.

**Rules:**
- List dependencies in the cap `description`: `"Depends on: reddit_search, reddit_fetch, cap_save_analysis (must be activated first)."`
- The handler itself cannot activate dependencies — `activate_cap` returns `registerTool()` code that must be `eval()`'d at the REPL top-level, not inside a handler closure.
- When activating a composite cap, activate its dependencies first:

```javascript
// Activate dependencies, then the composite cap
for (const dep of ['reddit_search', 'reddit_fetch', 'cap_save_analysis']) {
  const r = await mcp__yesmem__activate_cap({name: dep});
  eval(r.code);
}
const r = await mcp__yesmem__activate_cap({name: 'reddit_research'});
eval(r.code);
```

**In Step 3 (Test):** verify that all dependency caps are activated before running the handler. A common trap is that dependencies happen to be registered from earlier work in the session — the test passes, but a fresh session fails.

## Complete Example: reddit_research (composite, with dependencies + haiku)

Composite cap chaining three dependency caps and using `haiku()` for classification + synthesis.

```javascript
// Dependencies: reddit_search, reddit_fetch, cap_save_analysis — must be activated first
const handler = async ({topic, subreddits, limit = 10, score_min = 2, fetch_top = 5, synthesize = true}) => {
  const subs = subreddits || ['ClaudeAI', 'ChatGPTPro', 'cursor', 'ExperiencedDevs'];
  const queries = [topic, `${topic} frustration problem`, `${topic} wish feature`];
  const seen = new Set();
  const allPosts = [];
  for (const sub of subs) {
    for (const q of queries) {
      try {
        const r = await reddit_search({query: q, subreddit: sub, sort: 'relevance', time: 'month',
          limit: Math.ceil(limit / subs.length)});
        for (const p of (r?.posts || [])) {
          const link = p.permalink ? `https://reddit.com${p.permalink}` : '';
          if (link && !seen.has(link) && (p.score || 0) >= score_min) {
            seen.add(link);
            allPosts.push({url: link, title: p.title, score: p.score || 0, subreddit: p.subreddit});
          }
        }
      } catch(e) {}
    }
  }
  allPosts.sort((a, b) => b.score - a.score);
  const fetched = [];
  for (const p of allPosts.slice(0, fetch_top)) {
    try {
      const detail = await reddit_fetch({url: p.url, max_comments: 20});
      const postData = {title: p.title, url: p.url, score: p.score, subreddit: p.subreddit,
        body: (detail?.post?.body || '').substring(0, 1000),
        top_comments: (detail?.comments || []).filter(c => c.score > 3)
          .sort((a, b) => b.score - a.score).slice(0, 8)
          .map(c => ({author: c.author, score: c.score, body: (c.body || '').substring(0, 400)}))};
      // haiku() classification — no API key needed, routes through CC process auth
      if (synthesize) {
        try {
          postData.classification = await haiku(`Classify this post about "${topic}".\nTitle: ${postData.title}\nBody: ${postData.body.substring(0, 600)}`, {
            type: 'object',
            properties: {
              category: {type: 'string'}, sentiment: {type: 'string'},
              relevance: {type: 'number'}, key_insight: {type: 'string'}
            },
            required: ['category', 'sentiment', 'relevance', 'key_insight'], additionalProperties: false
          });
        } catch(e) { postData.classification = {error: String(e)}; }
      }
      fetched.push(postData);
    } catch(e) { fetched.push({title: p.title, url: p.url, error: String(e)}); }
  }
  // haiku() synthesis across all posts
  let synthesis = null;
  if (synthesize && fetched.length > 0) {
    try {
      const input = fetched.map((p, i) => `[${i+1}] ${p.title} (score:${p.score}) — ${p.classification?.key_insight || '?'}`).join('\n');
      synthesis = await haiku(`Analyze these ${fetched.length} posts about "${topic}".\n\n${input}`, {
        type: 'object',
        properties: {
          top_themes: {type: 'array', items: {type: 'object', properties: {theme:{type:'string'},evidence_count:{type:'integer'},description:{type:'string'}}, required:['theme','evidence_count','description'], additionalProperties:false}},
          pain_points: {type: 'array', items: {type: 'string'}},
          wow_opportunities: {type: 'array', items: {type: 'string'}}
        },
        required: ['top_themes', 'pain_points', 'wow_opportunities'], additionalProperties: false
      });
    } catch(e) { synthesis = {error: String(e)}; }
  }
  const result = {topic, total_candidates: allPosts.length, fetched_count: fetched.length, synthesis, posts: fetched};
  try {
    await cap_save_analysis({cap: 'reddit_research', source_table: 'posts',
      instruction: `Research: ${topic}`, row_count: fetched.length,
      summary: JSON.stringify({synthesis, post_count: fetched.length, candidates: allPosts.length}),
      tags: 'reddit,research,' + topic.replace(/\s+/g, '-').toLowerCase()});
  } catch(e) { result._persist_error = String(e); }
  return result;
};

// Test: activate dependencies FIRST, then run
for (const dep of ['reddit_search', 'reddit_fetch', 'cap_save_analysis']) {
  const r = await mcp__yesmem__activate_cap({name: dep}); eval(r.code);
}
const test = await handler({topic: 'AI coding memory', subreddits: ['ClaudeAI'], limit: 3, fetch_top: 2});

// Save with dependencies in description
await mcp__yesmem__save_cap({
  name: 'reddit_research',
  description: 'Research a topic across Reddit: search, fetch top posts, classify+synthesize via haiku(). Depends on: reddit_search, reddit_fetch, cap_save_analysis.',
  handler_repl: handler.toString(),
  schema: JSON.stringify({type:'object', properties: {
    topic:{type:'string'}, subreddits:{type:'array',items:{type:'string'}},
    fetch_top:{type:'integer'}, synthesize:{type:'boolean'}
  }, required:['topic'], additionalProperties:false}),
  tags: 'reddit,research,analysis,haiku,composite', tested: true, test_date: '2026-04-21', auto_active: false
});
```

**Patterns reinforced:**

| Pattern | Why |
|---|---|
| Dependencies as globals, activated before handler | `activate_cap` returns `registerTool()` code — can't eval inside handler closure |
| `haiku(prompt, schema)` without API key | Routes through CC process auth — no env/config needed |
| Two-stage haiku: classify each + synthesize all | Per-item structured extraction, then cross-item aggregation |
| Dependencies listed in `description` | Future Claude reads description to activate deps before calling |
| Persist via dependency cap, not own tables | `cap_save_analysis` handles schema — composite stays lightweight |

## Activation Modes

| Mode | Trigger | Use for |
|---|---|---|
| `auto_active: true` | proxy injects into every session | universal caps: search, general fetchers |
| `activate_cap(name)` | per-thread MCP call | task-specific caps |
| Manual `registerTool` | copy-paste JS | ad-hoc testing before save_cap |

`deactivate_cap(name)` reverses activation for the current thread.

## Delegation Pattern (optional, for analysis caps)

If the cap summarizes data, offer two modes:

- **mode='data'** (default): return rows → conversation-Claude summarizes in chat. Zero extra latency/cost, uses whatever model the user runs.
- **mode='haiku'**: cap calls Claude Haiku via curl. For headless/cron; requires `ANTHROPIC_API_KEY` in env or `~/.claude/yesmem/config.yaml`.

Full pattern with API-key fallback: see `cap_collect` source via `get_caps({name:'cap_collect'})`.

## Useful Fetch Patterns

Real-world caps surfaced two patterns worth knowing before reaching for more complex tools.

### HTTP metadata in one curl call

Use `curl -w` to pull status code, final URL after redirects, and content-type in the same invocation that downloads the body — no HEAD request, no header-parsing pipe:

```bash
curl -sL --max-time 15 -A "YesMem/1.0 (+mycap)" \
  -o "$TF" \
  -w '%{http_code}|%{url_effective}|%{content_type}' \
  "$URL"
```

`-w` emits the format string to stdout AFTER the body has been saved to `-o`. Split on `|` → three fields directly. `-L` follows redirects; `url_effective` is the final URL. Cleaner than `curl -I` + second `curl` for body.

### /dev/shm for fetch tempfiles

When you briefly need the response body to parse (`head -c`, regex extractor, jq), use `/dev/shm/` instead of `/tmp/`:

- **RAM-backed tmpfs** — no disk IO, measurably faster
- **No permission prompt** — hooks don't gate `/dev/shm`
- Always `rm -f` in the same handler to avoid mid-session artefacts

```bash
TF=/dev/shm/mycap_$(date +%s%N)_$$.body
curl -sL --max-time 15 -o "$TF" "$URL"
head -c 262144 "$TF"       # parse / extract
rm -f "$TF"
```

Name with `$(date +%s%N)_$$` so parallel handler invocations don't collide.

## Large Payload Pattern (>30KB)

`sh()` truncates stdout silently at 30KB. For bigger outputs, pipe straight into cap_store:

```bash
curl -sL "URL" | yesmem cap-blob-put --cap CAPNAME --key KEY
```

Read back chunk-by-chunk (cap_store responses cap ~200KB). Full template + reference implementation: see `docs/cap-store-analysis.md` "capblob-pipe" section and `reddit_fetch` v6+ handler.

**When NOT to use capblob:**

| Situation | Do instead |
|---|---|
| Transient data (HTML you only parse once, then discard) | Server-side parse in one pipe: `curl ... \| python3 -c '<parser>'` → upsert only the extracted 1-3 KB row. Never persist raw HTML you won't query. |
| Output < 25 KB | Plain `sh()` is simpler. |
| Data you want to `LIKE`-search on | Blobs aren't queryable. Parse to columns first. |

**The trap:** reaching for capblob because a URL *might* yield >30KB body. If you're extracting a few fields and discarding the rest, capblob is overkill — you persist 60KB of HTML you'll never read again AND hit the 25KB query-response cliff on read-back. Parse in the fetch pipeline; persist only the fields.

## Common Mistakes

| Mistake | Symptom | Fix |
|---|---|---|
| `id` / `created_at` / `updated_at` in columns array | `duplicate column name` on create_table; JSON parse fails downstream | Remove them — cap_store adds them automatically |
| bash heredoc or multiline | hook rejects or shell mangles | Single-line, chain with `&&` or `;` |
| Forgot `create_table` at handler start | first call succeeds by accident, later fails on fresh DB | Always call `create_table` first — it's idempotent |
| `tested: true` without running | future Claude trusts the flag; bugs surface at first real use | Actually run the handler with real inputs before save |
| `auto_active: true` for rare/experimental caps | MANDATORY-block gets noisy; token budget wasted per session | Start `false`; promote to `true` only after validated universal value |
| No timeout on `sh` or `curl` | hung handler blocks the REPL | `sh(cmd, 20000)`; `curl --max-time 15` |
| Concatenating user input into WHERE | sanitizer-bypass risk or blocked keyword | Use `?` placeholders + args array |
| Silent error swallowing | user thinks cap worked; data missing | Return `{error: ..., detail: ...}`, don't throw — REPL surfaces it |
| Inline Python/Perl/awk in bash with nested quoting | `r'\''...\''` escape artefacts, unmaintainable, duplicated across tests | Extract parser to a helper file, OR move logic into `handler_repl` as JS — don't embed interpreter source in shell single-quotes |
| Schema change without `activate_cap` call in open threads | old threads see old tool shape | Tell user to re-activate after supersede |
| Handler calls other caps without documenting dependencies | `ReferenceError` in fresh sessions where deps aren't activated | List deps in `description`; activate deps before composite cap |
| Assuming `haiku()` needs an API key | unnecessary config/env setup | `haiku()` routes through CC process auth — works out of the box |

## Known Limits

- **Task-tool subagents bypass proxy** → `auto_active` caps not injected. Use `spawn_agent` (yesmem swarm) for cap-preloaded subagent work.
- **cap_store response caps:** ~25 KB per single query (silent read-back cliff — a 60 KB blob chunk returns empty), ~200 KB aggregate across pages. Paginate with `all:true` / offset loop for larger scans. LIKE-search fast up to ~10k rows/table.
- **Superseding doesn't clear data, threads, or schema cache** — caller must re-activate.

## Quick Mode (3 steps, trivial caps only)

For caps with no storage, no schema beyond a single input:
1. Working one-liner works in REPL
2. `save_cap({name, description, handler_bash, tags, tested: true, auto_active: false})`
3. `get_caps({name})` to confirm

Most real caps need the full 6 steps.

## Related

- `docs/CAPS-md-spec.md` — CAPS.md file format spec: minimal alternative to MCP-based `save_cap`. One file with frontmatter + Purpose + Script + Database sections.
- `docs/cap-store-analysis.md` — cap_search/cap_collect/cap_save_analysis reference + schema + auth
- `docs/cap-store-analysis-examples.md` — 8 concrete walkthroughs (reddit, topic search, meta-analysis)
- `internal/storage/cap_store.go` — storage layer
- `internal/daemon/handler_capabilities.go` — save/activate/deactivate RPCs
- `internal/proxy/capabilities_inject.go` — auto_active injection mechanics
