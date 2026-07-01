---
name: yesresearch
description: Autonomous research loop — read plan, decompose, dispatch research agents, integrate, deliver wiki. For research tasks instead of coding. Runs as visible TUI agents in git worktree. Use when user says "yesresearch", "research <topic>", "recherchiere <thema>". Spawns cluster agents that use opencode built-in subagents (general, explore) with webfetch + websearch.
user-invocable: true
disable-model-invocation: false
allowed-tools: Bash(git *) Bash(curl *) Bash(pdftotext *) Bash(mkdir *) Bash(ls *) Bash(cat *) Bash(find *) Bash(rg *) Bash(test *) Grep Glob Read Write Edit todowrite Task
---

# Autonomous Research Loop

You are an orchestrator for autonomous research. Decompose a research topic into clusters, spawn parallel research agents, integrate results into a cited wiki.

## CRITICAL: Two-Tier Architecture

You are the **Orchestrator** (this session). You do NOT do research yourself. You spawn **Bereichs-Agents** (cluster agents) that do the actual research work. Each Bereichs-Agent runs autonomously in its own worktree and reports progress via scratchpad.

```
Orchestrator (this session, light)
  │
  ├─ READ master-plan (##  headings = clusters)
  ├─ DECOMPOSE into 5-8 file groups
  ├─ DISPATCH yesmem_spawn_agent × ≤6 (opencode TUI agents)
  ├─ MONITOR list_agents + scratchpad polling
  ├─ INTEGRATE cross-links + INDEX.md + citation audit
  └─ DELIVER commit + DONE
            │
            ▼ yesmem_spawn_agent (TUI, max 12h, ≤6 concurrent)
            │
   Bereichs-Agent (full TUI, autonomous)
     ├─ CONTEXT read master plan + cluster files
     ├─ PLAN set_plan, per-file strategy
     ├─ EXECUTE task(general) fetch+search+write
     │         task(explore) read-only citation review
     │         bash+curl+pdftotext for PDFs
     ├─ VERIFY min 2 sources/file, every claim cited
     ├─ COMMIT write files to worktree
     └─ DELIVER scratchpad DONE + send_to orchestrator
```

**Why two-tier:** Sawtooth would compress the master plan if one agent did everything. Cluster-scope agents keep context small. Plan lives in DB (scratchpad + set_plan), survives collapse.

## CRITICAL: Always spawn in a worktree

When invoked from an interactive session, the user's session only does setup — it does NOT run the orchestrator pipeline inline. Setup creates the worktree, writes the master plan + scratchpad briefing, then **spawns the Orchestrator as a TUI agent** which takes over all 6 phases.

Setup steps (user's interactive session):
1. **Create worktree:** `git worktree add -b yesresearch/<topic-slug> <repo>/.worktrees/yesresearch-<topic-slug>` (fresh-create) OR `git worktree add -b yesresearch/<topic-slug>-update-N <repo>/.worktrees/yesresearch-<topic-slug>-update-N` (update mode, branching from previous wiki commit)
2. Write master plan to `<worktree>/yesdocs/<topic>/PLAN.md` (or pass existing path)
3. **Detect model & backend:** Read `~/.config/opencode/opencode.json` → extract `model` key (e.g. `deepseek/deepseek-reasoner`). If absent, detect from opencode's default (first entry in models.json). Backend defaults to `"opencode"` — override via scratchpad briefing `backend: claude|codex|opencode` or master plan frontmatter. Both model and backend are passed to ALL spawns (Orchestrator + Bereichs-Agents).
4. Write Orchestrator briefing to scratchpad section `yesresearch-<topic-slug>-orchestrator`: worktree path, master plan path, output target, hard constraints, **resolved model**, **mode (fresh-create or update)**
5. Spawn Orchestrator: `yesmem_spawn_agent(project="<project>", section="yesresearch-<topic-slug>-orchestrator", backend="<resolved-backend>", work_dir="<worktree-path>", model="<resolved-model>")`
6. Wait 15s, then relay kick: `yesmem_relay_agent(to="<orchestrator-id>", content="Read scratchpad section yesresearch-<topic-slug>-orchestrator. Begin Orchestrator Phase 1 READ.")`
7. User's session polls `list_agents` + `scratchpad_read` until Orchestrator reports DONE.

**Only run inline if:** user explicitly says `--inline` or task is < 5 min trivial (single-file research, no cluster decomposition needed).

## Model & Backend Configuration

**Nothing is hardcoded.** Both model and backend are configurable via scratchpad briefing, master plan frontmatter, or auto-detected from the environment. Resolution order (first match wins):

1. **Scratchpad briefing** `model:` / `backend:` fields (highest priority — per-run override)
2. **Master plan frontmatter** `default_model:` / `default_backend:` (per-topic default)
3. **Auto-detection** — model: read from `~/.config/opencode/opencode.json → model` key; backend: defaults to `"opencode"`

**How to detect (user's session, step 3 of setup):**
```bash
# Extract the default model from opencode's config
MODEL=$(python3 -c "import json; print(json.load(open('$HOME/.config/opencode/opencode.json')).get('model',''))")
# If empty, opencode uses models.json first-entry
if [ -z "$MODEL" ]; then
  MODEL=$(python3 -c "import json; d=json.load(open('$HOME/.cache/opencode/models.json')); print(list(d.keys())[0] + '/' + list(d[list(d.keys())[0]]['models'].keys())[0])")
fi
```

**Why no daemon default:** The daemon returns `""` when model is empty — same as all other backends. Opencode picks from its own config. The skill bridges this by detecting and passing the model explicitly.

Example master plan frontmatter (override):
```yaml
---
default_model: zai-coding-plan/glm-5.2
default_max_runtime: 1h
update_age_days: 60
---
```

Example scratchpad briefing override:
```
model: zai/deepseek-chat
```

### Model selection rules (MANDATORY once model is chosen)

**Correct model strings:**
- `zai-coding-plan/glm-5.2` — glm-5.2 via Coding-Plan-Endpoint (`api.z.ai/api/coding/paas/v4`)
- `zai-coding-plan/glm-5.2-flash` — faster/cheaper variant
- `zai/deepseek-v4-pro` — yesloop default (regular z.ai endpoint)
- `zai/deepseek-chat` — DeepSeek standard
- Other providers: `anthropic/claude-sonnet-4-6`, `openai/gpt-5`, etc.

**WRONG — do not use:**
- ❌ `zai/glm-5.2` — routes to regular z.ai endpoint (`api.z.ai/api/paas/v4`) which does NOT carry glm-5.2 
- ❌ `glm-5.2` (bare) — resolveSpawnModel filters coding variants from auto-discovery . Bare name falls through to passthrough-with-warning, leaving routing to opencode's own auto-discovery. Unreliable.
- ❌ `zhipuai/glm-5.2` — same problem on zhipuai side

**Why slash-qualified works:** `resolveSpawnModel()` in `internal/daemon/spawn_model.go:38-42` looks up bare names against a providerMap that excludes coding variants. Slash-qualified strings pass through verbatim (line 35-36), so the full `provider/model` reaches opencode's `--model` flag directly. Opencode's auto-discovery (from `~/.cache/opencode/models.json`) then routes to the correct endpoint URL.

### Other configurable defaults

| Parameter | Default | Override via |
|---|---|---|
| `max_runtime` per Bereichs-Agent | `2h` | master plan `default_max_runtime` or scratchpad |
| `max_concurrent_agents` | `6` (cap: 8) | master plan `max_concurrent_agents` |
| `min_sources_per_file` | `2` | master plan `min_sources_per_file` |
| `update_age_days` (UPDATE MODE) | `90` | master plan `update_age_days` |
| `saturation_limit` (empty searches per file) | `3` | master plan `saturation_limit` |

Master plan frontmatter is the single source of truth for per-topic configuration. Scratchpad briefing overrides for per-run adjustments.

## max_runtime

Default `2h` pro Bereichs-Agent. Override via master plan frontmatter or `yesmem_spawn_agent(max_runtime="<duration>")`. **Absolute cap: `12h`** — no agent runs longer. For multi-day research, the orchestrator dispatches fresh batches.

## Orchestrator Contract (what to prescribe vs. delegate)

**The Orchestrator is itself a spawned agent** — invoked via `/yesresearch <topic>`, it spawns itself as a TUI agent via yesmem_spawn_agent and that agent runs the 6-phase pipeline below. The user's interactive session only creates the worktree + scratchpad + spawns the Orchestrator agent. From there the Orchestrator owns all downstream work.

**Prescribe (orchestrator's job):**
- **Topic + success criteria** — what good output looks like, not what to search
- **Master plan** — `##` cluster headings, file targets as bullet points
- **Output target** — `yesdocs/<topic>/wiki/<cluster>/<file>.md`
- **Hard constraints** — language, min sources per file, conflict policy, no-areas
- **Escalation triggers** — scope ambiguity, source unavailability, conflict on topic framing

**Delegate (Bereichs-Agent's job):**
- **CONTEXT** — reading master plan, understanding cluster scope
- **PLAN** — per-file strategy, source selection, citation approach
- **EXECUTE/VERIFY/COMMIT/DELIVER** — the 6-phase pipeline on the agent's cluster

**Calibration test:** Before writing the scratchpad, ask: "If the agent came back with the files, would I be surprised by the content?" If yes (wrong sources, wrong language, wrong depth), prescribe more. If bored (matches what you'd write), delegate more.

## Temp file discipline

NEVER write to `/tmp/` or `~/.claude/yesmem/tmp/` in autonomous operations. Use `<worktree>/.yesmem/tmp/` instead. `mkdir -p .yesmem/tmp` at session start.

## Concurrency: File Locking & Git Mutex

Multiple Bereichs-Agents share one worktree. Without coordination, two failure classes emerge: git index.lock races and silent Edit-overwrites. Both are solved with simple filesystem locks.

### File lock protocol (per-file mutation safety)

**MANDATORY before any Edit on existing file:**
```bash
LOCKDIR=".yesmem/locks"
mkdir -p "$LOCKDIR"
LOCKFILE="$LOCKDIR/<file-slug>.lock"

# Try to acquire lock (atomic — only one writer wins)
if ! ( set -o noclobber; echo "$AGENT_ID $(date -Is)" > "$LOCKFILE" ) 2>/dev/null; then
  # Lock held by another agent — wait up to 5 min, then fail-fast
  for i in $(seq 1 60); do
    sleep 5
    if ( set -o noclobber; echo "$AGENT_ID $(date -Is)" > "$LOCKFILE" ) 2>/dev/null; then
      break
    fi
    if [ $i -eq 60 ]; then
      echo "LOCK_TIMEOUT: $LOCKFILE held >5min, aborting edit on <file>"
      exit 1
    fi
  done
fi

# CRITICAL: Read file AFTER acquiring lock, not before
Read "<file>"

# ... perform Edit ...

# Release lock
rm -f "$LOCKFILE"
```

This guarantees: **1 file → 1 writer at a time**, even if multiple Bereichs-Agents target the same file (e.g. UPDATE MODE: Agent A does `add_visuals`, Agent B does `refresh_sources` on same file). Second writer waits, then sees A's changes, then builds on them.

**Lock dir cleanup:** Orchestrator in DELIVER removes stale locks (>2h old) from `.yesmem/locks/`.

### Git index mutex (commit serialization)

Git's `index.lock` lives at `.git/worktrees/<name>/index.lock` — shared across ALL agents in the worktree. Concurrent `git commit` → `index.lock existiert bereits` error .

**Commit mutex (MANDATORY for all Bereichs-Agents):**
```bash
GIT_LOCK=".yesmem/locks/git-commit.lock"

acquire_git_lock() {
  for i in $(seq 1 120); do
    if ( set -o noclobber; echo "$AGENT_ID $(date -Is)" > "$GIT_LOCK" ) 2>/dev/null; then
      return 0
    fi
    sleep 5
  done
  echo "GIT_LOCK_TIMEOUT: held >10min, aborting commit"
  return 1
}

# Before commit:
acquire_git_lock || exit 1
git add <files>
git commit -m "..."
EXIT_CODE=$?
rm -f "$GIT_LOCK"
exit $EXIT_CODE
```

This serializes commits. Slow (one at a time) but reliable. Bereichs-Agent retries on lock failure instead of crashing.

### Mutation contract for UPDATE MODE (scoped to action)

| Action | May Read | May Write | May Delete |
|---|---|---|---|
| `add_visuals` | existing prose | new `assets/*`, new `![...](...)` + caption blocks | nothing |
| `add_mermaid` | existing prose | new ` ```mermaid ` block at section | nothing |
| `run_persona_review` | full file | append to frontmatter `persona_review` only | nothing |
| `refresh_sources` | existing citations | append new citations to existing bibliography list | never (broken 404 citations stay as history) |
| `verify_status` | frontmatter | frontmatter `status` field only | nothing |
| `expand_content` | full file | append new sections at end | nothing |

**NEVER use Write on existing files.** Write overwrites; Edit mutates. Lock + Read + Edit is the only safe path. Use Write only for new files (no lock needed — file doesn't exist yet, no collision possible).

### Orchestrator frontmatter-union (INTEGRATE phase)

After all Bereichs-Agents complete, the Orchestrator merges frontmatter across files for cross-links and tags. **Union, not overwrite** (#3 fix):

For each file's frontmatter, read `tags` and `related` arrays. Build a union per file:
- `tags`: merge across UPDATE iterations (existing + new)
- `related`: merge across UPDATE iterations
- `persona_review`: replace (newest state wins, not union — it's a snapshot)
- `status`: monotonically progresses Entwurf → Verifiziert → Aktualisiert (highest wins)

Write the merged frontmatter back via Read+Edit. One pass over all files in INTEGRATE.

## Temp file discipline (original)

NEVER write to `/tmp/` or `~/.claude/yesmem/tmp/` in autonomous operations. Use `<worktree>/.yesmem/tmp/` instead. `mkdir -p .yesmem/tmp` at session start.

## Execution Modes

**tui-agent** (DEFAULT) — Bereichs-Agents spawned via yesmem_spawn_agent → gnome-terminal, visible, non-blocking.
**inline** — User explicitly requested inline. Run in current context.
**scheduled** — User asked for recurring research. Use yesmem_schedule.

## Definition of DONE

DONE is a contract. A Bereichs-Agent is DONE only when ALL six phase sections exist in its scratchpad section AND each carries `**Status:** COMPLETE`.

**Valid Status values per phase:**
- `IN PROGRESS` — actively working
- `COMPLETE` — phase finished, evidence present
- `BLOCKED — <reason>` — cannot proceed, needs orchestrator input

**Phase status update mandatory at every transition:**
```
update_agent_status(phase="Phase N/6 NAME")
update_agent_status(phase="Phase N/6 NAME (file M/K)")
update_agent_status(phase="Phase N/6 NAME (blocked: <why>)")
```

**SCRATCHPAD DISCIPLINE (MANDATORY — prevents briefing clobber):**

`scratchpad_write` is an UPSERT — it REPLACES the entire section. Use the RIGHT tool:

| Tool | When to use | Who calls it |
|---|---|---|
| `scratchpad_write` | Write/replace FULL section content (briefing, DONE report) | Orchestrator only (setup), Bereichs-Agent Phase 5+6 DONE report |
| `scratchpad_append` | Append progress update, phase result, partial status | **Bereichs-Agent, ALL phases** (EXCEPT when writing full Phase blocks) |
| `scratchpad_read` | Read current section content | Any time |

**MANDATORY rule:** The FIRST tool call after spawn MUST be `scratchpad_read`. Never call `scratchpad_write` before `scratchpad_read` — it will overwrite the orchestrator's briefing with your status text (#80063).

**For Bereichs-Agents reporting phase progress:**
- Use `scratchpad_append` for intermediate status updates ("Phase 1 done, starting Phase 2")
- Use `scratchpad_write` ONLY for the final DONE report (all 6 Phase blocks at once)
- Phase blocks are written to `scratchpad` via `scratchpad_write` when ALL phases are complete

**For Orchestrator:**
- `scratchpad_write` for initial briefing + final DONE summary
- `scratchpad_read` for monitoring Bereichs-Agent progress
- Never `scratchpad_append` — you own the section, just write it once

**Orchestrator validates DONE:**
- `list_agents` → check status, turns, activity
- `scratchpad_read(project, section)` → check phase blocks
- `### Phase 1` through `### Phase 6` headers present
- Each block has `**Status:** COMPLETE` (or `BLOCKED — <reason>`)
- Phase 4 has citation audit evidence (`Citations verified: yes`, source count per file)
- Phase 6 has `send_to orchestrator:` and file list

## DONE-Gate Hardening (Section 11)

Bereichs-Agent DONE-claims fail the gate when ANY of these deterministic checks fail. Orchestrator INTEGRATE runs all checks before accepting DONE.

### Per-File Checks (Orchestrator INTEGRATE, after all Bereichs-Agents DONE)

| # | Check | Detection Regex/Pattern | Failure Action |
|---|---|---|---|
| F1 | Frontmatter exists + valid YAML | `^---\n[\s\S]*?\n---\n` at file start | Reject file, dispatch fix |
| F2 | Frontmatter has required fields | `topic`, `cluster`, `status`, `last_updated`, `sources_count`, `language` all present | Dispatch fix |
| F3 | sources_count ≥ min_sources_per_file | YAML parse `sources_count` ≥ config | Mark `partial` |
| F4 | Every factual claim has inline citation | Grep `\[[^\]]+\]\(https?://[^)]+,\s*\d{4}-\d{2}-\d{2}\)` — count per paragraph | Dispatch fix |
| F5 | `persona_review` frontmatter block exists (UPDATE MODE) | `persona_review:\s*\n\s+personas_tested:` regex | Re-dispatch VERIFY |
| F6 | persona_review.gaps_fixed ≥ gaps_found | YAML compare | Mark `partial` |
| F7 | No `Write`-style wholesale replacement in git log (UPDATE MODE) | git show --stat each commit, flag commits with >90% line change on existing files | Reject commit, dispatch fix |
| F8 | images_count > 0 OR documented skip per MANDATORY source type | frontmatter `images_count` or scratchpad skip list | Mark `partial` if missing + undocumented |

### Wiki-Wide Checks (Orchestrator INTEGRATE, before DELIVER)

| # | Check | Detection | Failure Action |
|---|---|---|---|
| W1 | INDEX.md exists | `ls INDEX.md` exit 0 | Dispatch INTEGRATE-Subtask |
| W2 | INDEX.md contains Mermaid block | grep ` ```mermaid ` in INDEX.md | Dispatch INTEGRATE-Subtask |
| W3 | INDEX.md Mermaid ≥1 subgraph | grep `subgraph` in Mermaid block | Dispatch fix |
| W4 | INDEX.md Mermaid ≥3 edges | grep `-` or `=` connections in Mermaid block, count ≥3 | Dispatch fix |
| W5 | BIBLIOGRAPHY.md exists | `ls 99-sources/bibliography.md` exit 0 | Generate from scratch |
| W6 | Bibliography in UPDATE MODE preserves existing entries | git diff old vs new bibliography — old entries must all be present | Re-do read-merge-write |
| W7 | No broken git index.lock files in worktree | `ls .yesmem/locks/*.lock` empty after DELIVER | Manual cleanup |
| W8 | Frontmatter Union applied (UPDATE MODE) | spot-check: tags from prior version preserved in ≥80% of files | Re-do INTEGRATE frontmatter-merge |
| W9 | No concurrent Bereichs-Agents exceeded cap | `list_agents(status=running)` historical max ≤ cap | Log warning |
| W10 | All Bereichs-Agent scratchpad DONE blocks complete | Each has `### Phase 1` through `### Phase 6` + all `**Status:** COMPLETE` | Re-dispatch incomplete Bereichs-Agents |

### Orchestrator Self-Checks (before DELIVER complete)

| # | Check | Detection | Failure Action |
|---|---|---|---|
| O1 | Orchestrator plan persisted | `get_plan()` returns non-empty with mode flag | Restore from scratchpad |
| O2 | All clusters either DONE or explicitly aborted | plan state shows terminal status per cluster | Dispatch missing |
| O3 | No orphan locks >2h old | `find .yesmem/locks -mmin +120` empty | Cleanup |
| O4 | send_to caller_session sent | scratchpad has `send_to orchestrator:` line | Send |

### Rejection Protocol

When a check fails:
1. Orchestrator marks affected file/section as `partial` in INDEX.md
2. Dispatches fix task to Bereichs-Agent: `task(general, "Fix failing check <ID> on <file>: <details>")`
3. Re-runs check after fix
4. Max 3 fix cycles per check — after that, file stays `partial` and user is notified via send_to

### Validation Snippet (Bash, used by Orchestrator)

```bash
# Run all per-file checks for one file
validate_file() {
  local FILE="$1"
  local FAILS=0

  # F1: Frontmatter exists
  head -1 "$FILE" | grep -q '^---$' || { echo "F1 FAIL: $FILE"; FAILS=$((FAILS+1)); }

  # F2: Required fields
  for field in topic cluster status last_updated sources_count language; do
    grep -q "^${field}:" "$FILE" || { echo "F2 FAIL: $FILE missing $field"; FAILS=$((FAILS+1)); }
  done

  # F3: sources_count ≥ 2 (default min)
  SOURCES=$(awk -F': ' '/^sources_count:/ {print $2}' "$FILE")
  [ "${SOURCES:-0}" -ge 2 ] || { echo "F3 FAIL: $FILE sources_count=$SOURCES"; FAILS=$((FAILS+1)); }

  # F4: Every paragraph has at least 1 citation (rough heuristic)
  CITES=$(grep -cE '\[[^]]+\]\(https?://[^)]+,\s*[0-9]{4}-[0-9]{2}-[0-9]{2}\)' "$FILE")
  [ "$CITES" -ge 2 ] || { echo "F4 FAIL: $FILE only $CITES citations"; FAILS=$((FAILS+1)); }

  return $FAILS
}

# Wiki-wide W2-W4
validate_index_mermaid() {
  local INDEX="yesdocs/<topic>/wiki/INDEX.md"
  awk '/```mermaid/,/^```$/' "$INDEX" > /tmp/mermaid-block.txt
  grep -q 'subgraph' /tmp/mermaid-block.txt || echo "W3 FAIL: no subgraph"
  EDGES=$(grep -cE '(-|=.*>)' /tmp/mermaid-block.txt)
  [ "$EDGES" -ge 3 ] || echo "W4 FAIL: only $EDGES edges"
}
```

This mirrors the deterministic DONE-Guard pattern from yesloop  — no LLM judgment involved in validation, pure regex/parseable checks.

## Orchestrator Phase Pipeline

### Phase 1/6: READ
```
update_agent_status(phase="Phase 1/6 READ")
```
- Read master plan (plain MD). If path given, Read it. If `--ad-hoc`, generate plan from topic.
- Parse `##` headings as clusters. Bullet points under each heading are file targets.
- If no master plan provided: decompose topic into 3-6 clusters using `task(explore)` for scoping, then synthesize plan.
- **MODE DETECTION (MANDATORY before Phase 2) — explicit decision tree:**

  ```
  wiki_exists = (yesdocs/<topic>/wiki/ contains ≥1 .md file)
  mode_flag = scratchpad briefing "mode:" field  // "update" | "force-fresh" | unset

  if NOT wiki_exists:
      MODE = FRESH          // no existing wiki, build from scratch
  elif wiki_exists AND mode_flag == "force-fresh":
      MODE = FRESH          // overwrite — user accepts data loss
  elif wiki_exists AND mode_flag == "update":
      MODE = UPDATE         // delta-only extension
  elif wiki_exists AND mode_flag unset:
      // AMBIGUOUS — refuse to guess, ask user
      send_to caller_session: "AMBIGUITY: wiki exists at yesdocs/<topic>/wiki/.
        Use --update to extend or --force-fresh to overwrite. Current state: <file count> files."
      WAIT for user decision (poll check_messages every 5 min)
      On user reply: re-evaluate decision tree with new mode_flag
  else:
      MODE = FRESH          // defensive default (shouldn't reach)
  ```

  **Hard rules:**
  - NEVER silently overwrite an existing wiki without explicit `--force-fresh`
  - NEVER silently run UPDATE without user confirming `--update`
  - Mode persists in `set_plan(mode: ...)` for crash recovery
- Output: `set_plan(...)` with cluster list, mode flag, scratchpad entry confirming plan parsed.

**UPDATE MODE — Delta Scan delegated to task(explore) per cluster:**

Delta-Scan is expensive (7 conditions × N files, including websearches). Orchestrator does NOT do it inline — would bloat context and trigger Sawtooth.

Instead: dispatch one `task(explore)` per cluster with prompt:
```
"Scan cluster '<name>' at yesdocs/<topic>/wiki/<cluster-slug>/. For each .md file:
1. Read frontmatter
2. Check: images_count=0/missing? 3+ entities but no ```mermaid block? persona_review missing or gaps_found>gaps_fixed? last_updated > <update_age_days>d ago? status=Entwurf? sources_count < <min_sources>?
3. Run websearch for '<file-topic>' — any new sources not in existing citations?
Return JSON: [{file, reasons: [list], suggested_actions: [add_visuals|add_mermaid|run_persona_review|refresh_sources|verify_status|expand_content]}]"
```

Parallel task(explore) calls (one per cluster) complete in ~2-5 min. Orchestrator collects results, builds delta-plan.

A file is **update-worthy** if ANY of:
- `images_count: 0` or missing (visual citations missing)
- 3+ entities in content but no Mermaid diagram (grep for ` ```mermaid ` returns 0)
- `persona_review` block missing OR `gaps_found > gaps_fixed`
- `last_updated` older than 90 days (configurable via master plan `update_age_days`)
- `status: Entwurf` (never verified)
- `sources_count < min_sources` (below threshold)
- New websearch returns sources not in existing bibliography

Delta-plan output: list of `{file, reason, action}` tuples.

**Files not update-worthy stay untouched.** This is delta-only — avoid the "rebuild all on every tick" anti-pattern .

### Phase 2/6: DECOMPOSE
```
update_agent_status(phase="Phase 2/6 DECOMPOSE")
```
- **FRESH MODE:** Group files into batches à 5-8 per Bereichs-Agent (cap: 6 concurrent agents).
- **UPDATE MODE:** Group delta-tasks (from Phase 1 delta-scan) into batches à 5-8 per Bereichs-Agent. Same cluster-files typically go to the same Bereichs-Agent to preserve context. A Bereichs-Agent may receive mixed actions: "file X: add visuals", "file Y: refresh sources", "file Z: run persona review".
- Create one scratchpad section per cluster: `yesresearch-<topic-slug>-<cluster-slug>`.
- For each cluster section, write:
  - **Both modes:** cluster name, file list (or delta-task list in UPDATE MODE), source hints, language, min-sources-per-file, special constraints, **mode flag (FRESH or UPDATE)**, resolved model
  - **UPDATE MODE additional:** per-file action, existing frontmatter state, existing content path
- Decide `max_runtime` per agent (default `2h`, longer for source-heavy clusters; UPDATE typically shorter since not all files need work).

### Phase 3/6: DISPATCH
```
update_agent_status(phase="Phase 3/6 DISPATCH")
```
- **Dynamic cap:** Spawn `min(cluster_count, max_concurrent_agents)` agents (default cap: 6, configurable). If cluster_count > cap, form batches.
- **Batch strategy for many clusters:**
  - Batch 1: clusters 1..cap → spawn, monitor, wait for all DONE
  - Batch 2: clusters cap+1..2*cap → spawn after Batch 1 complete
  - Continue until all clusters processed
  - Rationale: avoid >cap concurrent agents, but allow large topic decompositions
- Per batch: `yesmem_spawn_agent` × N. Backend: `<resolved-backend>` (from scratchpad briefing). work_dir: shared worktree.
- **Model:** use resolved model from Phase 2 (scratchpad briefing carries it).
- Wait 15s after each spawn for TUI load.
- **Relay kick** within 30s: `yesmem_relay_agent(to="<agent-id>", content="Read scratchpad section <section>. Mode: FRESH|UPDATE. Begin Phase 1 CONTEXT.")`
- Log agent IDs in orchestrator scratchpad.

### Phase 4/6: MONITOR
```
update_agent_status(phase="Phase 4/6 MONITOR")
```
**PATIENCE IS THE JOB.** Research agents work slower than coding agents — often by an order of magnitude. A single file may take 10-15 minutes: webfetch roundtrips, PDF download+pdftotext, draft writing, citation verification via task(explore), revision. A 5-file cluster typically takes 30-90 minutes; a large cluster 2-4 hours. This is normal, not a stall.

**Monitoring cadence (DO NOT over-poll):**
- `list_agents` every 5 minutes (not every 60s) — checks status + last_activity_at + turns_used + stream_active
- `scratchpad_read` every 15 minutes per Bereichs-Agent — more often just burns context
- Trust the agents to work. Polling faster does not make them faster.
- **Prefer `get_agent` over `list_agents` for single-agent checks** — it returns the full field set including `stream_active` and `subagent_streams`.

**Primary vs secondary indicators (clarification):**
- **Primary:** `update_agent_status` field in `get_agent`/`list_agents` responses — shows current phase (`Phase N/6 NAME`). Live, daemon-tracked. Use this for progress tracking.
- **Secondary:** `scratchpad_read` — for DONE-verification (6 phase blocks COMPLETE) and content review. Poll less frequently.
- Do NOT poll scratchpad for phase tracking — `update_agent_status` is the authoritative source.

**What counts as "working" (not stalled):**
- `stream_active == true` (from `get_agent`) → **definitively working**. Agent is actively streaming a response. Ignore `last_activity_at` when stream is live.
- `subagent_streams > 0` → Agent has dispatched task() subagents (e.g. general/explore for research), actively waiting on their returns. Working.
- turns_used increasing between polls → working
- last_activity_at within 10 min → working
- phase field updating through Phase 1→2→3→4→5→6 → on track
- new commits in worktree → working
- file word counts growing → working
- No activity for 15+ min AND turns flat AND `stream_active == false` AND `subagent_streams == 0` → potential stall, investigate

**stream_active is the primary liveness signal** . It's set true by the proxy at SSE-stream start and false at stream end, via `track_stream_state` RPC . `last_activity_at` alone is unreliable — agents that block on long tool calls may not update it for 10+ min while still working.

**What counts as stalled (recovery needed):**
- `stream_active == false` AND no activity (last_activity_at, turns, commits) for 20+ minutes AND `subagent_streams == 0`
- `frozen: max_runtime exceeded` with PID alive → false positive , extend runtime or relay to continue
- PID dead → crashed, respawn
- `BLOCKED — <reason>` in scratchpad → orchestrator resolves

**Recovery actions:**
  - `frozen: max_runtime exceeded` but PID alive → false positive , extend `max_runtime` via respawn, or relay to continue
  - Crashed (PID dead) → respawn with identical task, note "respawn attempt N"
  - Stalled (no activity >20min, no frozen) → `relay_agent` kick: "Status check — current phase? Any blockers?" Wait 10 min for response. If still flat, respawn.
  - `BLOCKED — <reason>` in scratchpad → orchestrator resolves or aborts that cluster
- Cap: 3 respawns per agent. After that, abort cluster, document gap.

**Anti-pattern — the anxious orchestrator:**
- Do NOT relay "status?" messages more than once per 15 minutes
- Do NOT abort an agent just because turns_used hasn't moved for 5 minutes — webfetch calls block for 10-30s each, multi-file drafts take minutes
- Do NOT spawn redundant backup agents "just in case" — burns tokens, clogs worktree
- Do NOT escalate to user unless ALL agents are stalled AND ≥3 respawns exhausted

### Phase 5/6: INTEGRATE
```
update_agent_status(phase="Phase 5/6 INTEGRATE")
```
Triggers after all Bereichs-Agents report DONE (or aborted).

- **Frontmatter Union (UPDATE MODE first):** For each file, merge `tags` + `related` from existing frontmatter with new contributions (union, not overwrite — see "Orchestrator frontmatter-union" in Concurrency section). Status monotonically progresses Entwurf→Verifiziert→Aktualisiert.
- **Cross-links:** For each generated file, add `[[_querverweis]]` to related files in other clusters. Heuristic: shared entities, citations, or topic overlap. Use the union-merged `related:` frontmatter field as authoritative source.
- **INDEX.md — ALWAYS fully regenerate** (both FRESH and UPDATE mode). Single source of truth = file frontmatter + Mermaid cluster-map derived from `related:` fields. Never partial-patch INDEX.md — drift risk too high.
  - Read all `.md` files in `yesdocs/<topic>/wiki/**`
  - Parse frontmatter from each
  - Generate navigation + status + cluster-map fresh
- **Citation audit:** Sample 10% of files (min 3). For each sampled file, verify:
  - Min 2 sources per file
  - Every factual claim has inline citation
  - Conflicts presented dually
  - PDFs (if any) have working curl-fallback
  - Failures → dispatch fix task to Bereichs-Agent or mark "partial"
- **Bibliography — read-merge-write** (UPDATE MODE):
  - Read existing `99-sources/bibliography.md` (if present)
  - Collect new citations from Bereichs-Agent scratchpad DONE blocks
  - Merge: dedupe by URL, preserve existing categories + add new entries
  - NEVER remove existing entries — broken 404 links stay as historical record (#4 decision)
  - FRESH MODE: just write new bibliography from scratch

### Phase 6/6: DELIVER
```
update_agent_status(phase="Phase 6/6 DELIVER")
```
- `git add yesdocs/<topic>/wiki/` → verify staged file count matches plan
- Commit: `docs(<topic>): research-wiki complete — N files, M citations, INDEX live`
- `send_to <caller_session>: "DONE: <topic> wiki. N/M files. Branch: yesresearch/<topic-slug>. INDEX: yesdocs/<topic>/wiki/INDEX.md"`
- Orchestrator (this session) does NOT push or merge — that's user's call.
- `set_plan` mark complete.

## Bereichs-Agent Contract (spawned agent's pipeline)

When you are spawned as a yesresearch Bereichs-Agent, you receive via scratchpad:
- Cluster name + file list
- Output target directory
- Min sources per file, language, constraints
- Reference to master plan (or full plan inline)

### Phase 1/6: CONTEXT
```
update_agent_status(phase="Phase 1/6 CONTEXT")

### Phase 1: CONTEXT
**Status:** COMPLETE
**Session id:** <opencode_session_id from whoami()>
**Mode:** FRESH | UPDATE
**Cluster:** <name>
**Files in scope:** <list with one-line description each>
**UPDATE-mode actions (if UPDATE):** <per-file action list: add_visuals, add_mermaid, run_persona_review, refresh_sources, verify_status, expand_content>
**Source hints:** <from plan, or "discover via websearch">
**Language:** <DE/EN/etc>
**Constraints:** <list>
**Risks:** <e.g. paywalled sources, language availability>
```

**UPDATE MODE — Read existing content first (MANDATORY):**
Before any Edit on existing files, Read the current file + its frontmatter. Preserve all existing content unless the action explicitly replaces it. Anti-pattern : blind overwrite of existing file → content loss.

Allowed mutations per action:
- `add_visuals`: only add `![...](assets/...)` blocks + caption + `assets/` files. Do NOT touch existing prose.
- `add_mermaid`: only insert ` ```mermaid ` block at appropriate section. Do NOT rewrite surrounding prose.
- `run_persona_review`: read-only + append review findings to frontmatter `persona_review` block. Do NOT auto-rewrite content (orchestrator INTEGRATE decides).
- `refresh_sources`: run websearch for new sources since `last_updated`; append new citations to existing bibliography; do NOT remove existing citations.
- `verify_status`: change frontmatter `status: Entwurf → Verifiziert` after successful VERIFY pass; no content change.
- `expand_content`: append new sections addressing identified gaps; do NOT remove existing sections.

### Phase 2/6: PLAN
```
update_agent_status(phase="Phase 2/6 PLAN")

### Phase 2: PLAN
**Status:** COMPLETE
**Plan stored via set_plan:** yes
**Per-file strategy:** <which sources, which subagent approach>
**Subagent allocation:** <e.g. "all files: task(general) for fetch+write, task(explore) for verify">
**Saturation plan:** 3 empty searches = abort file, document
**Files in scope:** <list>
```

### Phase 3/6: EXECUTE
```
update_agent_status(phase="Phase 3/6 EXECUTE (file M/K)")

### Phase 3: EXECUTE
**Status:** IN PROGRESS
**Files:** N total

#### File 1: <name>
**Status:** COMPLETE
**Sources gathered:** <count>
**Draft written:** yes — <word count> words
**Citations inline:** yes — <count>
**Saturation hits:** 0/3
**Subagent calls:** task(general) ×N, task(explore) ×1 for review
**Commits:** <hash> <msg>

**Total progress:** M/N files
**Uncommitted work:** none | <list>
```

Per-file discipline:
- `task(general, "<prompt>")` for source discovery + fetch + draft. Pass topic, file-target path, language, min sources, saturation limit.
- For PDFs: subagent must use `bash` + `curl -sL <url>` + `pdftotext` — `webfetch` cannot parse PDFs.
- For Cloudflare-403: document in scratchpad, try Wayback Machine (`https://web.archive.org/web/*/<url>`) or Google cache.
- After draft: `task(explore, "<prompt>")` for read-only citation check. explore subagent has webfetch + websearch but no write — perfect for verification.
- **Visual Citations** (see "Visual Content Rules" below): for sources with visual evidence value (charts, screenshots of UI, diagrams, PDFs), capture to `assets/` and link from MD.
- **UPDATE MODE — Preserve existing content:** use Edit (not Write) for existing files. Read file first. Only mutate what the action allows. Never overwrite.
- `todowrite` synced after each file.
- One commit per file (or per cluster if files are small). UPDATE MODE: one commit per action-batch per file.

### Phase 4/6: VERIFY
```
update_agent_status(phase="Phase 4/6 VERIFY")

### Phase 4: VERIFY
**Status:** COMPLETE
**Citations verified:** yes — sampled <N>/<total> files
**Min sources met:** yes — all files have ≥<min> sources
**Claims cited:** yes — every factual claim has inline [Title](URL, accessed YYYY-MM-DD)
**Conflicts dual-position:** yes — <count> conflict points, both sides presented
**PDFs handled:** <count> via curl+pdftotext
**Visual assets captured:** <count> screenshots/PDFs in assets/, linked from MD
**Persona Review:** yes — <persona set used>, gaps found: <count>
**Partial files:** none | <list with reason>
**VERIFY cycles used:** N/3
```

**Two-layer VERIFY (MANDATORY — both layers required for COMPLETE status):**

**⓪ HARD RULE — Phase 4 is ONLY COMPLETE when BOTH layers have produced evidence:**
- Citation-Check-Report in scratchpad (subagent ID + missing-citation list resolved or struck)
- Persona-Review-Report in frontmatter `persona_review` block (personas tested + gaps fixed)
- A Bereichs-Agent claiming Phase 4 COMPLETE with only one layer → Orchestrator rejects DONE (see DONE-Gate Hardening, Section 11)

1. **Citation Check via `task(explore)` (MANDATORY):** read-only review of every factual claim. Missing/weak citations → fix or strike. **Required evidence:** subagent ID + list of claims checked + missing-citation actions taken, in scratchpad.

2. **Persona Review via `task(explore)` (MANDATORY, distinct from Citation Check):** simulate a reader with a specific persona trying to answer a question using only this wiki file. Find concept gaps, not just missing sources. **Required evidence:** `persona_review` block in frontmatter with `personas_tested`, `gaps_found`, `gaps_fixed`. Missing block = Phase 4 INCOMPLETE.

Persona Review prompt pattern:
```
Du bist <persona>. Frage: <question the file should answer>.
Lies <file>.md. Beantworte die Frage aus dem Inhalt — OHNE externes Wissen.
Für jede Information, die du aus dem File beziehen kannst, zitiere die Section.
Für jede fehlende Information (die du für die Antwort bräuchtest, aber nicht im File findest), markiere als GAP.
Return: { answered: true/false, gaps: [list of missing concepts], weak_sections: [list of sections needing expansion] }
```

Suggested persona sets (agent picks 2-3 most relevant per file):
- **Executive**: "C-Level, will Investitions-/Strategie-Entscheidung treffen"
- **Engineer**: "Senior Engineer, will Technologie evaluieren und integrieren"
- **Researcher**: "Akademiker, willtiefe Methodik und Quellen prüfen"
- **End User**: "Practitioner, will Tool im Alltag nutzen"
- **Journalist**: "Redakteur, will Thema für Artikel verstehen"

If Persona Review returns gaps → add content addressing the gap (re-dispatch task(general) for that section) and re-run both Citation Check and Persona Review. **Max 3 VERIFY cycles total (combined for both layers)** — if still gaps after 3 cycles, mark file `partial` with `persona_review.gaps_fixed < gaps_found` in frontmatter and proceed. Don't loop endlessly.

If verify fails on citation layer only → fix and re-verify (also counts against the 3-cycle budget).

### Phase 5/6: COMMIT
```
update_agent_status(phase="Phase 5/6 COMMIT")

### Phase 5: COMMIT
**Status:** COMPLETE
**Files written:** <list with paths>
**Total word count:** <sum>
**Total citations:** <count>
**Cross-links added:** yes — <count>
**Status badges:** Entwurf|Verifiziert|Aktualisiert per file
**Commits:** <hashes>
```

### Phase 6/6: DELIVER
```
update_agent_status(phase="Phase 6/6 DELIVER")

### Phase 6: DELIVER
**Status:** COMPLETE
**Files complete:** N/N
**Citations:** M total
**Partial/failed:** none | <list with reason>
**send_to orchestrator:** yes — <timestamp>
**Scratchpad complete:** yes
**Citations for bibliography aggregation:** <list>
```

- `send_to caller_session: "DONE: cluster <name>. N/M files. Citations: <count>."`
- Do NOT push branch or merge — orchestrator's job.

## Citation Rule (replaces Cold Review)

Research has no code review. Instead, every factual claim must be sourced. This is the primary quality gate.

**Format:** `[Title](URL, accessed YYYY-MM-DD)` inline at the claim.

**Min sources per file:** 2 (default). Override via master plan frontmatter or orchestrator scratchpad entry.

**Every factual claim needs a citation.** If you cannot source a claim:
- Strike it from the draft, OR
- Mark as `*[unkenntlich — keine Quelle gefunden]*` and document in scratchpad

**Conflict policy:** When sources disagree, present BOTH positions neutrally. Do not resolve. Example:
```
Die Frist für die Umsetzung wird unterschiedlich gesehen:
- Nach Ansicht von Kanzlei A beträgt sie 24 Monate [A](url, 2026-06-30).
- Kanzlei B argumentiert für 30 Monate [B](url, 2026-06-30).
```

**Citation check via task(explore):** Before Phase 5 COMMIT, dispatch `task(explore, "Review <file>.md. List every factual claim. For each: does it have an inline citation? Is the source accessible? Report missing or weak citations.")`. explore is read-only — perfect for verification.

## Web Research Rules

### webfetch
- Arbitrary HTTP(S) URLs — no domain restriction
- HTML→Markdown automatic
- 5MB response limit, 30s default timeout
- Chrome User-Agent with Cloudflare retry
- **Cannot parse PDFs** — returns garbled bytes

### websearch
- Real web search via Exa or Parallel Web Search provider
- Use for source discovery: `websearch("<topic> site:cms-lawnow.com OR site:eur-lex.europa.eu")`

### PDFs
Always via bash pipeline:
```bash
curl -sL "<pdf-url>" -o /tmp/file.pdf && pdftotext /tmp/file.pdf - | head -500
```
Document the PDF URL + accessed date as citation.

### Cloudflare 403 / Paywall
- Try Wayback Machine: `curl -sL "https://web.archive.org/web/2/<url>" | head -c 50000`
- Try Google cache: `https://webcache.googleusercontent.com/search?q=cache:<url>`
- If all fail: document in scratchpad, mark file `partial`, proceed

### Saturation
After 3 empty searches (websearch returns no new relevant sources) for a single file, stop searching. Document in scratchpad: `Saturation hit at source N. Proceeding with N-K sources, min-sources met: yes/no.`

## Visual Content Rules

Research wikis without visuals lose readers. Text-only citations are weaker than visual evidence. Each Bereichs-Agent should capture visual assets where they add value.

### When to capture visuals (per file) — MANDATORY for marked rows

| Source type | Capture? | What to capture | Where |
|---|---|---|---|
| Vendor website with architecture diagram | **MANDATORY** | Screenshot or download SVG/PNG | `assets/<file>-<source-slug>.png` |
| PDF report with charts/tables | **MANDATORY** | Render relevant page as image OR extract page as separate PDF | `assets/<file>-<source>-p<N>.png` or `.pdf` |
| UI screenshots (tools, dashboards) | **MANDATORY** | Headless browser screenshot | `assets/<file>-ui-<component>.png` |
| Law firm blog post (text only) | skip | — | — |
| Academic paper | Optional | Abstract page, key figure | `assets/<file>-figure-<N>.png` |
| Market research analyst page | **MANDATORY** | Chart/table screenshot | `assets/<file>-chart-<topic>.png` |

**Compliance rule (enforced by Orchestrator INTEGRATE):**
- Files with MANDATORY-categorized sources but `images_count: 0` in frontmatter → file flagged `partial` in INDEX.md
- Bereichs-Agent MUST document in scratchpad for each missing visual: `{file, source_url, reason_skipped}` (e.g. `screenshot_failed: no browser tool available`)
- Undocumented missing visuals = DONE-Gate failure (see Section 11)
- Acceptable skip reasons: `chromium_missing`, `source_no_visual_element` (after Agent checked), `paywall_blocked`, `wayback_unavailable`

Rule of thumb: **if the source has a visual element (chart, diagram, screenshot, table) that strengthens the claim, MANDATORY capture.** Pure text sources skip — but document the skip.

### How to capture

**PDFs → image:**
```bash
# Download PDF
curl -sL "<url>" -o .yesmem/tmp/source.pdf
# Extract specific page as PNG (requires poppler-utils)
pdftoppm -png -f <page> -l <page> -r 150 .yesmem/tmp/source.pdf assets/<file>-<source>-p<page>
```

**Website → screenshot (Fallback-Kaskade):**

Try each tool in order. First success wins. If all fail: document `screenshot_failed: no tool available`, proceed without visual.

```bash
# Try 1: Chromium (most common on Linux)
if command -v chromium >/dev/null 2>&1; then
  chromium --headless --disable-gpu --no-sandbox \
    --window-size=1280,900 \
    --screenshot=assets/<file>-<source>.png \
    "<url>" 2>/dev/null

# Try 2: Google Chrome
elif command -v google-chrome >/dev/null 2>&1; then
  google-chrome --headless --disable-gpu --no-sandbox \
    --window-size=1280,900 \
    --screenshot=assets/<file>-<source>.png \
    "<url>" 2>/dev/null

# Try 3: Firefox (headless mode)
elif command -v firefox >/dev/null 2>&1; then
  firefox --screenshot --window-size=1280,900 \
    assets/<file>-<source>.png \
    "<url>" 2>/dev/null

# Try 4: wkhtmltoimage (simpler, no JS)
elif command -v wkhtmltoimage >/dev/null 2>&1; then
  wkhtmltoimage --width 1280 --height 900 "<url>" assets/<file>-<source>.png 2>/dev/null

# Try 5: Safari (macOS)
elif command -v safari >/dev/null 2>&1 || [ -d /Applications/Safari.app ]; then
  # Safari has no headless CLI — use screencapture via AppleScript
  osascript -e 'tell application "Safari" to open location "<url>"' 2>/dev/null
  sleep 3
  screencapture -R 0,0,1280,900 assets/<file>-<source>.png 2>/dev/null

# All failed
else
  echo "screenshot_failed: no browser tool available (chromium/google-chrome/firefox/wkhtmltoimage/safari)"
fi
```

Verify file exists and has size >1KB before linking. Empty/placeholder PNGs get rejected.

If chromium not available, document in scratchpad `screenshot_failed: chromium missing`, proceed without visual.

**Direct image download (vendor provides PNG/SVG):**
```bash
curl -sL "<image-url>" -o assets/<file>-<source>.png
```

### Linking visuals from MD

```markdown
Die Marktprognose von Grand View sieht ein Wachstum von 30% p.a. bis 2030 
[GrandView](https://grandview.com/..., accessed 2026-07-01).

![GrandView Marktprognose 2025-2030](assets/marktgroesse-grandview-prognose.png)
*Abbildung 1: GrandView Marktprognose (Quelle: [GrandView](https://..., 2026-07-01))*
```

Captions are mandatory under each image: source attribution + brief context. The image without a caption is not a citation — the caption + link is.

### Wayback-Fallback for visuals

If a source URL is likely to disappear (paywalled blog, fast-moving vendor page), also capture via Wayback Machine:
```bash
curl -sL "https://web.archive.org/web/2/<url>" -o .yesmem/tmp/wayback.html
# Extract image URLs from wayback HTML, download to assets/
```

Document in scratchpad which assets were captured from Wayback vs live source.

### Asset naming convention

`<file-name-slug>-<source-slug>-<descriptor>.<ext>`

Examples:
- `marktgroesse-grandview-forecast-chart.png`
- `architektur-patterns-mem0-diagram.svg`
- `financing-rounds-techcrunch-cohere-funding.png`

All lowercase, kebab-case, no spaces, no special chars. Slug-based, not arbitrary names.

## Mermaid Diagrams

Mermaid is text-based diagramming — the diagram source lives in the .md file as a fenced code block, rendered by the publishing tool (GitHub, MkDocs Material, Docusaurus, Obsidian). Same single-source principle as the rest of the wiki: text in, rendering happens elsewhere.

### When to use Mermaid

| Page type | Diagram type | Why |
|---|---|---|
| `INDEX.md` | `graph TD` or `mindmap` | Cluster-overview visualization, navigational anchor |
| Architecture pages | `flowchart`, `sequenceDiagram`, `graph` | Show component relationships, data flows |
| Timeline pages | `timeline` | Chronological events (regulatory timeline, funding history) |
| Comparison pages | `classDiagram` or `gitGraph` | Entity relationships, branching histories |
| Decision/strategy pages | `flowchart` with decision nodes | Decision trees, workflow visualizations |
| Stakeholder pages | `graph` with subgraphs | Stakeholder map, influence diagram |

Rule: **if the page has 3+ entities or events with relationships, add a Mermaid diagram.** Pure prose without structure → no diagram needed.

### Mermaid syntax in MD

````markdown
## Architektur-Patterns im Vergleich

Die drei dominanten Memory-Architekturen unterscheiden sich in der Zuständigkeit für Memory-Management:

```mermaid
graph LR
  subgraph "Memory-as-a-Service"
    A[Agent] -->|store/recall| B[(Memory SaaS)]
    B --> C[Mem0, Zep, Supermemory]
  end
  subgraph "Stateful-Agent-Runtime"
    D[Agent Runtime] -->|owns| E[(In-Process Memory)]
    E --> F[Letta/MemGPT]
  end
  subgraph "MCP-Server"
    G[Agent] -->|MCP tool calls| H[(Memory MCP Server)]
    H --> I[claude-mem, YesMem]
  end
```
````

### Mermaid source files for complex diagrams

For diagrams with 15+ nodes (cluster overview, multi-stakeholder maps), store the source separately:
- `assets/<file>-<diagram-name>.mmd` — editable source
- Embed via reference: `Die Übersicht steht in [assets/INDEX-cluster-map.mmd](assets/INDEX-cluster-map.mmd), gerendert:` + then a fenced mermaid block with the same content inline (so single-file readers still see it).

### INDEX.md mandatory Mermaid (DONE-Gate-enforced)

Every `INDEX.md` MUST contain a Mermaid cluster-map. Orchestrator INTEGRATE regenerates it from file frontmatter (see Section 11 DONE-Gate for validation).

**Minimum requirements (all MUST be met, else DONE rejected):**
- ≥1 `subgraph` block (clusters as subgraphs)
- ≥3 edges derived from actual `related:` frontmatter fields (not invented)
- ≥1 file-node per cluster (all clusters represented)
- Color/styling per cluster (`classDef` or `style` declarations)

**Validation pattern (Orchestrator checks via grep/regex):**
```bash
# Extract Mermaid block from INDEX.md
MERMAID_BLOCK=$(awk '/```mermaid/,/^```$/' yesdocs/<topic>/wiki/INDEX.md)

# Hard checks
echo "$MERMAID_BLOCK" | grep -q 'subgraph'         || echo "FAIL: no subgraph"
echo "$MERMAID_BLOCK" | grep -qE '[-=]>.*[-=]>'    || echo "FAIL: no edges"
echo "$MERMAID_BLOCK" | grep -cE '^\s+[A-Za-z]'    # node count — must be ≥ cluster_count + file_count
EDGE_COUNT=$(echo "$MERMAID_BLOCK" | grep -cE '(-|=.*>|---)')
[ "$EDGE_COUNT" -ge 3 ]                              || echo "FAIL: <3 edges"
```

Any FAIL = Orchestrator refuses to DELIVER, dispatches fix task to Bereichs-Agent.

This is the navigational anchor. Without it, INDEX.md is just a bullet list.

### Diagram citation

If a diagram is derived from a source (not original work), cite it:
````markdown
*Abbildung 2: Architektur-Übersicht adaptiert von [Mem0 Architecture](https://docs.mem0.com/architecture, 2026-07-01)*
````

Original diagrams (synthesis by the agent from multiple sources) marked as `*Eigene Darstellung basierend auf N Quellen*`.

## Output Target Structure

```
yesdocs/<topic>/wiki/
├── INDEX.md                          # Orchestrator-generated, navigation + status
├── <cluster-1-slug>/
│   ├── file-1.md
│   ├── file-2.md
│   └── assets/                       # Visual citations, screenshots, PDFs, diagrams
│       ├── file-1-source-a.png
│       └── file-2-architecture.mmd   # Mermaid source (optional, for editability)
├── <cluster-2-slug>/
│   └── ...
└── 99-sources/
    └── bibliography.md               # Orchestrator-aggregated from all agents
```

Each file carries **extended YAML frontmatter**:
```yaml
---
# Identity
topic: <topic>
cluster: <cluster-name>
title: <Human-readable title>
language: DE | EN | ...

# Lifecycle
status: Entwurf | Verifiziert | Aktualisiert
last_updated: YYYY-MM-DD
created_at: YYYY-MM-DD

# Provenance
sources_count: N
citations_count: N
images_count: N
diagrams_count: N

# Navigation
related:
  - <cluster>/<other-file>.md
  - <cluster>/<other-file>.md
tags:
  - <tag-1>
  - <tag-2>

# Persona review (filled by Phase 4 VERIFY)
persona_review:
  personas_tested: [executive, engineer, researcher]
  gaps_found: N
  gaps_fixed: N
---
```

Frontmatter is the single source of truth for status, navigation, and searchability. Orchestrator INTEGRATE-Phase uses it to generate INDEX.md cross-links, tag pages, and status badges.

## Failure Recovery

**First, rule out "normal slow":** Before declaring failure, confirm the agent is actually stuck, not just taking a long time. A research agent doing webfetch+PDF+draft+verify on a 5-file cluster may legitimately run 60+ minutes without producing a commit.

| Situation | Action |
|---|---|
| Bereichs-Agent crashed (PID dead) | Orchestrator respawns with identical task, scratchpad section preserved. Max 3 respawns. |
| Socket collision on spawn | `rm -f ~/.claude/yesmem/agent-<id>.sock` before respawn  |
| Stalled agent (`stream_active==false` AND no activity >20min AND `subagent_streams==0`) | `relay_agent` kick: "Status check — current phase? Blockers?" Wait 10 min. If still flat, respawn. |
| `frozen: max_runtime exceeded` but PID alive | False positive . Extend `max_runtime` via respawn, or relay to continue. |
| webfetch 403 / timeout | Subagent documents, tries Wayback/Google cache. If all fail: file marked `partial`, proceed. |
| Master plan ambiguous | Orchestrator resolves via inline decomposition. If unresolvable: `send_to caller_session: "AMBIGUITY: <description>"` and wait. |
| All Bereichs-Agents fail | Orchestrator documents, aborts, escalates: `send_to caller_session: "ABORT: all agents failed. <details>"` |

## Anti-Patterns (DONE-Gate-enforced — see Section 11)

Each violation triggers DONE-Gate failure: Orchestrator rejects DONE-claim, dispatches fix task.

| # | Anti-Pattern | DONE-Gate Check |
|---|---|---|
| 1 | Orchestrator doing research inline instead of delegating | Orchestrator scratchpad must NOT contain `task(general)` for source-fetch |
| 2 | Skipping Citation Check (Phase 4 VERIFY) | Scratchpad must contain `**Citations verified:** yes` + subagent ID |
| 3 | Skipping Persona Review (Phase 4 VERIFY) | Frontmatter must contain `persona_review` block with `personas_tested` |
| 4 | Resolving conflicts unilaterally | Content audit must show both positions for every conflict point |
| 5 | Claims without sources | Every factual claim must have inline citation (grep pattern) |
| 6 | Bereichs-Agent running >12h | max_runtime cap enforced at spawn-time |
| 7 | More than 6 concurrent Bereichs-Agents | `list_agents(status=running)` count check |
| 8 | Push or merge without user approval | `git log origin/<branch>` must be empty in DELIVER |
| 9 | Reporting DONE with missing phase sections | Section 11 validation regex |
| 10 | Using Write on existing files (instead of Edit+Lock) | Worktree git log must NOT show wholesale file replacements >100% line change |
| 11 | Missing visuals on MANDATORY source types | `images_count > 0` OR documented skip per source |
| 12 | Missing Mermaid in INDEX.md | grep ` ```mermaid ` + ≥3 edges in INDEX.md |

**Soft anti-patterns (advisory, not DONE-blocking):**
- Do NOT write design documents unless task is architectural
- Do NOT scope-creep beyond the master plan — see DRIFT CHECK
- Do NOT modify agent config files (.claude/, SYSTEM.md, etc.)
- Do NOT delete worktree before orchestrator confirms commit

## DRIFT CHECK — Before each FILE in EXECUTE

1. Re-read cluster scope from scratchpad
2. Compare current file's content against original file description in master plan
3. Drift indicators:
   - File covers material outside cluster scope → split or move to correct cluster
   - Sources are off-topic → restart source discovery
   - Word count explosion (>3x target) → split file
4. Major drift → `scratchpad_write("⚠️ DRIFT: <description>") + send_to orchestrator: "DRIFT in <file>. <details>"`. Wait.

## BEWEISLAST bei Scratchpad-Claims

Before writing "DONE", "complete", "verified" to scratchpad, confirm the artifact matches the claim.

| Claim | Required independent check |
|---|---|
| "N files written" | `ls yesdocs/<topic>/wiki/<cluster>/` shows N .md files |
| "citation verified" | `task(explore)` review output in scratchpad, with missing-citation list empty |
| "min sources met" | per-file source count in scratchpad ≥ min |
| "PDF parsed" | `pdftotext` output snippet in scratchpad |
| "commit pushed" | `git log origin/<branch> --oneline -1` shows hash (only if push was requested) |
| "file edited" | `Read` the file (not just `git show`) |

Apply to consequential claims only: file counts, citation completeness, commit state. Routine per-file todowrite updates don't need this.

## Orchestrator Crash Recovery

If the Orchestrator agent itself crashes (daemon restart, OOM, manual kill), the run is recoverable because state lives in yesmem, not in volatile context.

**On Orchestrator respawn (via `yesmem_resume_agent`):**
1. `get_plan()` — restore phase + cluster list + mode flag
2. `scratchpad_read(project, "yesresearch-<topic-slug>-orchestrator")` — restore detailed state
3. `list_agents(project, status="running")` — check which Bereichs-Agents are still alive
4. Reconstruct: which phase was I in? Which batches dispatched? Which agents completed?
5. `update_agent_status(phase="Phase N/6 NAME (recovered)")` — re-assert state
6. Continue from the right point:
   - Was in Phase 3 DISPATCH with 2/4 agents spawned? Spawn remaining 2.
   - Was in Phase 4 MONITOR? Resume polling.
   - Was in Phase 5 INTEGRATE? Restart integration from file scan.

**Plan persistence contract (Orchestrator writes this via `set_plan`):**
```
Phase: <current phase>
Mode: FRESH | UPDATE
Clusters: [{name, status: pending|dispatched|done|failed, agent_id, batch: N}]
Batch progress: batch N of M complete
Files expected: <count>
Files complete: <count>
```

Orchestrator updates this after every phase transition + every batch completion. On respawn, plan is the source of truth — context is rebuildable from it.

**User interrupt:** If user wants to abort a running orchestrator, send `relay_agent` with `ABORT: stop after current batch, do not start new dispatches`. Orchestrator honors this at the next batch boundary.

## Communication

- **Orchestrator mode (this skill invoked):** progress updates between phases, DONE at end
- **Bereichs-Agent mode (spawned):** scratchpad_write for state, send_to for completion
- Never ask "soll ich weitermachen?" — continue unless blocked

## Mini Example (filled Phase 3, others stubbed)

```
### Phase 1: CONTEXT
**Status:** COMPLETE
**Session id:** ses_abc123
**Cluster:** Primärrecht
**Files in scope:** eu-directive.md, ucpd.md, uwg.md
**Source hints:** eur-lex.europa.eu, gesetze-im-internet.de
**Language:** DE
**Constraints:** min 2 sources/file, conflict dual
**Risks:** EU-Trilog-Versionen können abweichen

### Phase 2: PLAN
**Status:** COMPLETE
**Plan stored via set_plan:** yes
**Per-file strategy:** eu-directive via EUR-Lex + cms-lawnow, ucpd via EUR-Lex + Noerr, uwg via BMJ + Beck
**Subagent allocation:** task(general) pro file für fetch+write, task(explore) nach Draft
**Saturation plan:** 3 leere websearch-Calls = Stopp
**Files in scope:** 3

### Phase 3: EXECUTE
**Status:** COMPLETE
**Files:** 3 total

#### File 1: eu-directive.md
**Status:** COMPLETE
**Sources gathered:** 4
**Draft written:** yes — 720 words
**Citations inline:** yes — 6
**Saturation hits:** 0/3
**Subagent calls:** task(general) ×2, task(explore) ×1
**Commits:** a1b2c3d feat(wiki): eu-directive.md

#### File 2: ucpd.md
**Status:** COMPLETE
...

#### File 3: uwg.md
**Status:** COMPLETE
...

**Total progress:** 3/3 files
**Uncommitted work:** none

### Phase 4: VERIFY
**Status:** COMPLETE
**Citations verified:** yes — sampled 2/3 files (explore subagent)
**Min sources met:** yes — all ≥2
**Claims cited:** yes — every claim sourced
**Conflicts dual-position:** yes — 1 conflict (Trilog-Frist)
**PDFs handled:** 1 (EUR-Lex PDF via curl+pdftotext)
**Partial files:** none
**VERIFY cycles used:** 1/3

### Phase 5: COMMIT
**Status:** COMPLETE
**Files written:** primärrecht/eu-directive.md, primärrecht/ucpd.md, primärrecht/uwg.md
**Total word count:** 2140
**Total citations:** 17
**Cross-links added:** yes — 5
**Status badges:** Verifiziert ×3
**Commits:** a1b2c3d, e4f5g6h, i7j8k9l

### Phase 6: DELIVER
**Status:** COMPLETE
**Files complete:** 3/3
**Citations:** 17 total
**Partial/failed:** none
**send_to orchestrator:** yes — 2026-06-30T23:50
**Scratchpad complete:** yes
**Citations for bibliography:** [EUR-Lex 2024/825](...), [UCPD](...), [BMJ UWG](...), ...
```

## Decision Gates (Autonomous, Bereichs-Agent)

| Situation | Action |
|---|---|
| Source easy to find | Fetch directly via webfetch |
| 2-3 source candidates, unclear best | Pick most authoritative (official > law firm > media), document rationale |
| Need to expand scope within file | Document, proceed if minor (e.g. related regulation); escalate if major |
| Source unverifiable (404, paywall) | Try Wayback/Google cache; if all fail, mark `*[unkenntlich]*`, proceed |
| 3 consecutive saturation hits | Stop searching for this file, document, proceed with what's gathered |
| Citation check fails | Fix missing citations, re-run explore review, max 3 cycles |
| Irreversible action (delete, overwrite existing wiki) | Pause, `send_to orchestrator: "IRREVERSIBLE: <description>"`, wait |

## Decision Gates (Orchestrator)

| Situation | Action |
|---|---|
| Bereichs-Agent reports BLOCKED | Read scratchpad, resolve or abort that cluster |
| Bereichs-Agent crashed 3× | Abort cluster, document gap in INDEX.md |
| Citation audit finds systematic gaps | Re-dispatch fix task to affected Bereichs-Agent |
| Integration conflicts (cross-links broken) | Orchestrator fixes inline, documents |
| User sends new instructions via `check_messages` | Pause, eval, integrate or escalate |
