# Cognition & Persona

## 1. Emotional Memory

Sessions aren't all equal — a frustrating debugging marathon that ends with a breakthrough is more memorable than a routine config change.

- **Emotional Intensity** (0.0–1.0) — rated per session during extraction
- **Scoring boost** — learnings from intense sessions score up to 30% higher (`emotionalBoost = 1.0 + intensity × 0.3`)
- All learnings from a session inherit its intensity score
- **Pivot Moments** — turning points extracted as `pivot_moment` category (highest weight 1.6). Format: direct quote + what changed. Max 1-2 per session. Never faded from briefing regardless of age.

---

## 2. Persona Engine

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
7. **Automatic Trait Dedup** — embedding-based cosine similarity (threshold 0.75) finds semantically duplicate traits within the same dimension and supersedes the weaker one. Runs in bootstrap-persona (Phase 2.5) and Phase 6 in the daemon.

### Manual override
`set_persona` MCP tool allows user overrides with highest priority.

### User Profile Synthesis

Auto-generated ~500 token profile of the user — background, expertise, working style, values, how they collaborate with Claude. Synthesized from `preference`, `explicit_teaching`, `pattern`, and `relationship` learnings plus expertise traits.

- **Function:** `synthesizeUserProfile()` in `internal/daemon/persona.go`
- **Schedule:** Runs after `synthesizePersonaDirective()` in the extraction flow. 72h time guard prevents excessive re-synthesis (persona.go:331: `72*time.Hour`). Hash check over input data skips when nothing changed.
- **Storage:** Reuses `persona_directives` table with `user_id = 'user_profile'` — no separate schema
- **LLM:** Quality model (Sonnet/Opus), ~500 tokens output, German, third person
- **Injection:** In briefing after Awakening Narrative, before Persona Directive — every new instance immediately knows who the user is

---

## 3. Pinned Learnings (Bookmarks)

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

## 4. Skill Evaluation

The proxy detects when skill evaluation is relevant and injects a `[skill-eval]` block into the conversation:

- **`isUserInputTurn()` guard** — skill-eval only injected on real user input messages, not on continuation requests (tool results)
- **`buildSkillEvalBlock()`** — assembles available skills from Claude Code settings into a compact evaluation prompt
- **Collapse-protected** — old skill-eval blocks stripped before collapse via `stripSkillHints()`, always re-injected fresh

---

## 5. Cognitive Signals & Reflection

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

## 6. Learning Clustering

Periodically clusters semantically similar learnings using agglomerative clustering:
- Reads embeddings from VectorStore — zero CPU overhead (no on-the-fly embedding)
- Cosine similarity with 0.85 threshold, minimum 2 documents per cluster
- Results feed into narrative generation, pattern synthesis, sleep consolidation, and recurrence detection
- Cluster labels generated via quality model (default: Sonnet, pre-computed during daemon run, not on-the-fly)
- **Cluster Score Propagation:** 3 real columns (inject_count, use_count, noise_count); match/save/fail collapse into inject_count. Propagates to cluster scores via `IncrementClusterScore()`. Enables cluster-level affinity: `1.0 + useRate - noiseRate*0.5`.

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

