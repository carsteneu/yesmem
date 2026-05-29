# Config Reference (`config.yaml`)

Complete reference for all settings in `~/.claude/yesmem/config.yaml`.
Missing keys are filled with defaults from `internal/config/config.go:Default()`.

---

## extraction — Extraction Pipeline

Controls LLM-based knowledge extraction from past sessions.

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `model` | string | `sonnet` | Pass 2 — extraction model. Shortnames: `haiku`, `sonnet`, `opus`, or full model ID. |
| `summarize_model` | string | `haiku` | Pass 1 — summarization (compresses session chunks). Haiku is sufficient. |
| `quality_model` | string | `sonnet` | Pass 2 — quality refinement (dedup, relevance, contradictions). Falls back to `narrative_model`. |
| `narrative_model` | string | `opus` | Narrative generation (session handovers, project profiles, persona). |
| `mode` | string | `prefiltered` | `full` = send everything / `prefiltered` = rule-based pre-filter. |
| `chunk_size` | int | `25000` | Sessions larger than N tokens are split into chunks. |
| `auto_extract` | bool | `true` | Start extraction automatically after session end. |
| `max_age_days` | int | `0` | Only extract sessions from the last N days. `0` = all. |
| `max_per_run` | int | `30` | Max sessions per daemon run. `0` = unlimited. |
| `min_session_age_hours` | int | `24` | Skip sessions younger than N hours (waits for replication). |

---

## llm — LLM Backend & Budgets

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `provider` | string | `auto` | `auto` (API if key present, else CLI), `api` (HTTP API), `openai`, `openai_compatible`, `cli` (Claude CLI binary). |
| `claude_binary` | string | `""` | Path to Claude CLI binary (only needed for `provider: cli` if not in PATH). |
| `daily_budget_extract_usd` | float64 | `5.0` | Daily budget for extraction + evolution (USD). `0` = no limit. API providers only. |
| `daily_budget_quality_usd` | float64 | `2.0` | Daily budget for narratives + persona (USD). `0` = no limit. API providers only. |
| `max_budget_per_call_usd` | float64 | `1.0` | Max budget per individual call (CLI: `--max-budget-usd`). `0` = no limit. |

---

## evolution — Knowledge Evolution

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `auto_resolve` | bool | `true` | Automatically detect and resolve contradictions between learnings (older learning marked as superseded). |
| `unfinished_ttl_days` | int | `30` | Open tasks/TODOs stay active for N days, then archived. `0` = active forever. |

---

## briefing — Session Start Briefing

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `detailed_sessions` | int | `3` | Number of recent sessions shown with full details (older appear as one-liners). |
| `other_projects_days` | int | `90` | Time window for "Other Projects" section (days). |
| `max_tokens` | int | `5000` | Max briefing size in tokens (safety limit). |
| `dedup_threshold` | float64 | `0.4` | Similarity threshold for dedup (0.0-1.0). Lower = more aggressive. |
| `max_per_category` | int | `5` | Max learnings per category in the briefing. Rest available via `get_learnings()`. |
| `languages` | []string | `["de","en"]` | ISO 639-1 codes for stop-word filtering during dedup. |
| `remind_open_work` | bool | `true` | Inject instruction to mention open work at session start. |
| `user_profile` | bool | `true` | Include synthesized user profile in briefing. |

---

## proxy — Infinite-Thread Proxy

Sits between Claude Code and the Anthropic/OpenAI API, compressing old messages.

### Core Configuration

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `enabled` | bool | `true` | Start proxy with daemon. |
| `listen` | string | `:9099` | Listen address/port. Claude Code routes via `ANTHROPIC_BASE_URL=http://localhost:9099`. |
| `target` | string | `https://api.anthropic.com` | Upstream API (Anthropic). |
| `openai_target` | string | `https://api.openai.com` | Upstream API for OpenAI-format clients. |

### Compression

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `token_threshold` | int | `250000` | Trigger stubbing when conversation exceeds this token count. |
| `token_minimum_threshold` | int | `100000` | Compress down to this token count. |
| `keep_recent` | int | `10` | Last N messages stay uncompressed (working context). |
| `sawtooth_enabled` | bool | `true` | Sawtooth cache optimization (frozen prefix between stub cycles). |
| `cache_ttl` | string | `ephemeral` | Cache TTL for `cache_control` blocks: `ephemeral` (5 min) or `1h` (2× write cost). |
| `usage_deflation_factor` | float64 | `0.7` | Scale input tokens reported to Claude Code (suppresses "Context low" warning). `0` = off. |
| `token_thresholds` | map | (see below) | Model-specific thresholds. |

**Default `token_thresholds`:**
```yaml
token_thresholds:
  opus: 180000
  sonnet: 180000
  haiku: 130000
  gpt-5: 180000
  codex: 180000
```

### Cache Keepalive

Prevents cache expiry during idle periods via periodic pings.

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `cache_keepalive_enabled` | bool | `true` | Send keepalive pings. |
| `cache_keepalive_mode` | string | `5m` | `auto` (detect from response), `5m`, `1h`. |
| `cache_keepalive_pings_5m` | int | `5` | Pings per idle phase when TTL=5min. |
| `cache_keepalive_pings_1h` | int | `1` | Pings per idle phase when TTL=1h. |

### Code Navigation

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `code_nav_mode` | string | `block` | `block` (block Bash, suggest code-nav tools), `nudge` (hint only), `off`. |
| `code_nav_dismiss_count` | int | `5` | Permanently disable after N dismissals. |

### Prompt Injections

Each injection can be toggled individually. Default: all `true` except as noted.

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `prompt_ungate` | bool | `true` | `[deprecated]` Use `claude_prompt.prompt_ungate`. Strip CLAUDE.md subordination disclaimer. |
| `prompt_rewrite` | bool | `false` | `[deprecated]` Use `claude_prompt.prompt_rewrite`. Strip output-throttling + inject quality directives. |
| `prompt_enhance` | bool | `false` | `[deprecated]` Use `claude_prompt.prompt_enhance`. CLAUDE.md authority boost + comment discipline + persona tone. |
| `prompt_tool_prefs` | bool | `true` | `[yesmem-tool-prefs]` Edit/Write preference + error-semantics warning. |
| `prompt_output_discipline` | bool | `true` | `[yesmem-output-discipline]` No preamble + no skill eval + exploratory heuristic. |
| `prompt_coding_discipline` | bool | `true` | `[yesmem-coding-discipline]` Read-before-propose + no brute force + no half-finished. |
| `prompt_beweislast` | bool | `true` | `[yesmem-beweislast]` Fabrication guard + claim vs proof + tool-result honesty. |
| `prompt_scope_discipline` | bool | `true` | `[yesmem-scope-discipline]` Deliver A not A+B+C + adjacent findings separate. |
| `prompt_delegation_contract` | bool | `true` | `[yesmem-delegation-contract]` Self-contained prompts + parallel dispatch. |
| `prompt_clarify_first` | bool | `true` | `[yesmem-clarify-first]` Only clarify when alternative interpretations produce materially different work. |
| `prompt_code_tools_first` | bool | `true` | `[yesmem-code-tools-first]` Prefer MCP code-navigation tools over agent spawns. |
| `prompt_pattern_suggest` | bool | `true` | Record repeated shell-command shapes for cap-suggestion analysis. |
| `effort_floor` | string | `""` | Minimum effort level: `low`, `medium`, `high`, `max`. Empty = off. |
| `skill_eval_inject` | string | `silent` | `true` (verbose eval output), `silent` (internal eval only), `false` (disabled). |

### Profile-Aware Prompt Configuration

Replaces the deprecated flat prompt flags. Each pipeline gets its own prompt profile; empty fields inherit from `shared_prompt`.

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `shared_prompt` | PromptProfile | `{}` | Base prompt profile for ALL pipelines. |
| `claude_prompt` | PromptProfile | `{}` | Claude Code override. Empty = inherit from shared_prompt. |
| `codex_prompt` | PromptProfile | `{}` | Codex override. Empty = inherit from shared_prompt. |
| `opencode_prompt` | PromptProfile | `{}` | OpenCode override. Empty = inherit from shared_prompt. |

### Provider Auto-Discovery

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `auto_configure_providers` | bool | `true` | Read models.json + opencode.json + auth.json at proxy startup to auto-configure provider routing. Set to `false` to disable. |
| `provider_targets` | map[string]string | `{deepseek: "https://api.deepseek.com"}` | Per-provider target URLs for OpenAI-compatible providers. Keys are matched as case-insensitive prefixes against model names. |

### Custom System Prompt

Replaces the default system prompt with the SYSTEM.md template for supported pipelines.

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `custom_system_prompt.enabled_opencode` | bool | `true` | Inject SYSTEM.md for OpenCode/DeepSeek/OpenAI-compatible pipelines. |
| `custom_system_prompt.enabled_claude_code` | bool | `true` | Inject SYSTEM.md for Claude Code pipeline. Anthropic provides `--system-prompt` as official CLI flag. |
| `custom_system_prompt.enabled_codex` | bool | `true` | Inject SYSTEM.md for Codex pipeline. |
| `custom_system_prompt.template_path` | string | `~/.claude/yesmem/SYSTEM.md` | Path to the system prompt template file. |

### Per-Model Feature Gates

Control which yesmem behavioral features are active per model/provider. Keys under `model_features` are model name prefixes matched case-insensitively (longest wins). Models not listed fall back to `feature_defaults`.

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `model_features.<model>.skill_eval` | bool | `true` | Inject [skill-eval] instruction block. |
| `model_features.<model>.briefing` | bool | `true` | Inject yesmem briefing at session start. |
| `model_features.<model>.rules_reminder` | bool | `true` | Periodic reminder of project rules/guidelines. |
| `model_features.<model>.plan_checkpoint` | bool | `true` | Inject plan checkpoint reminders during long sessions. |
| `model_features.<model>.think_reminder` | bool | `true` | Inject hybrid_search() hint to check memory before assuming. |
| `model_features.<model>.think_reminder_min_chars` | int | `0` | Min user text length to trigger think_reminder. `0` = always. Set to `10` to skip short messages like "hi" or "ok". |
| `model_features.<model>.timestamps` | bool | `true` | Inject [HH:MM:SS] [msg:N] [+Δ] markers for temporal awareness. |
| `feature_defaults.<key>` | — | — | Same keys as above, used as fallback for unlisted models. |

Example: per-model gates for DeepSeek with think_reminder gated at 10+ chars:

```yaml
proxy:
  model_features:
    deepseek:
      think_reminder: true
      think_reminder_min_chars: 10
```

---

## embedding — Semantic Search

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `provider` | string | `local` | `local` (local model, no API call) or `api` (Anthropic/OpenAI embedding API). |
| `local.model` | string | `multilingual-e5-small` | Local embedding model (384d vectors). |
| `local.cache_dir` | string | `~/.claude/yesmem/models` | Model cache path. |
| `vector_db.persist_dir` | string | `~/.claude/yesmem/vectors` | Vector persistence path. |
| `vector_db.collection` | string | `learnings` | Collection name. |
| `search.method` | string | `""` | Search method (empty = hybrid). |
| `search.ivf_threshold` | int | `5000` | Switch to IVF index above N vectors. |
| `search.ivf.k` | int | `0` | IVF cluster count (`0` = auto sqrt). |
| `search.ivf.nprobe` | int | `15` | IVF cluster search breadth. |

---

## api — API Keys

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `api_key` | string | `$ANTHROPIC_API_KEY` | Anthropic API key. Lookup order: 1. env, 2. config, 3. `~/.claude/config.json`. |
| `openai_api_key` | string | `$OPENAI_API_KEY` | OpenAI API key. |
| `openai_base_url` | string | `$OPENAI_BASE_URL` | Custom OpenAI-compatible base URL. |

---

## signals — Cognitive Signals

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `enabled` | bool | `false` | Enable signal reflection. When off, signal tools are not injected. |
| `mode` | string | `reflection` | `reflection` = separate async API call. |
| `model` | string | `sonnet` | Model for reflection calls (haiku is sufficient for classification). |
| `every_n_turns` | int | `1` | Reflection every N end-turn responses. `1` = every turn. |

---

## claudemd — Operative Reference (`yesmem-ops.md`)

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `enabled` | bool | `true` | Auto-generate `yesmem-ops.md` per project. |
| `max_per_category` | map | (see below) | Max learnings per category in the reference. |
| `refresh_interval` | string | `2h` | Regeneration interval (Go duration: `2h`, `30m`, `24h`). |
| `min_sessions` | int | `3` | Minimum sessions before generating a reference. |
| `output_file` | string | `yesmem-ops.md` | Output file (placed in project's `.claude/` directory). |
| `model` | string | `""` | Model (empty = `narrative_model`). |

**Default `max_per_category`:**
```yaml
max_per_category:
  gotcha: 15
  pattern: 10
  decision: 10
  explicit_teaching: 5
  pivot_moment: 5
```

---

## http — HTTP API Server

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `enabled` | bool | `true` | Start HTTP listener. |
| `listen` | string | `127.0.0.1:9377` | Listen address. |
| `auth_token` | string | `""` | Bearer token (empty = auto-generated). |

---

## update — Auto-Update

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `auto_update` | bool | `true` | Automatically check for new versions. Set to `false` on dev machines. |
| `check_interval` | string | `6h` | Check interval (Go duration). |
| `channel` | string | `stable` | Update channel: `stable`. |

---

## agents — Agent Spawning

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `terminal` | string | `""` | Preferred terminal: `ghostty`, `kitty`, `gnome-terminal`, `alacritty`, `wezterm`, `xterm`. Empty = auto-detect. |
| `viewer_terminal` | string | `""` | Terminal for displaying agent sessions. Falls back to `terminal`. |
| `max_runtime` | string | `""` | Max runtime per agent (Go duration: `30m`, `1h`). Empty = 30m default. |
| `max_turns` | int | `0` | Max relay turns per agent. `0` = 30 default. |
| `max_depth` | int | `0` | Max spawn depth (agent → sub-agent). `0` = 3 default. |
| `token_budget` | int | `0` | Max tokens per agent (input+output). `0` = 500000 default. Overridable per spawn. |

---

## forked_agents — Forked-Agent Proxy

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `enabled` | bool | `true` | Enable forked-agent feature. |
| `model` | string | `""` | Forked-agent model (empty = same model as main thread). |
| `token_growth_trigger` | int | `30000` | Token growth that triggers a fork. |
| `max_forks_per_session` | int | `0` | Max forks per session. `0` = unlimited. |
| `max_cost_per_session` | float64 | `0` | Max cost per session (USD). `0` = no limit. |
| `debug` | bool | `false` | Debug logging for forked-agent requests. |

---

## secrets_sanitization — Secret Redaction

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `enabled` | bool | `true` | Enable secret redaction (LLM input/output + persisted bash outputs). |
| `allowed_exceptions` | []string | `[]` | List of strings that bypass redaction (full match, not substring). |

**Important:** `allowed_exceptions` matches the **entire** pattern match, not substrings. Template patterns (`aws_secret_access_key`, `password_in_url`) ignore exceptions entirely — those patterns must be removed from the redactor if suppression is needed.

---

## pricing — Pricing Table

Per-million-token pricing for budget tracking. Configurable without rebuild.

| Key | Type | Default (Input/Output) |
|-----|------|------------------------|
| `haiku` | `{input, output}` | 1.0 / 5.0 |
| `sonnet` | `{input, output}` | 3.0 / 15.0 |
| `opus` | `{input, output}` | 5.0 / 25.0 |
| `gpt-5-mini` | `{input, output}` | 0.25 / 2.0 |
| `gpt-5.2` | `{input, output}` | 1.75 / 14.0 |
| `gpt-5.4` | `{input, output}` | 2.5 / 15.0 |

---

## paths — Paths (optional)

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `db` | string | `~/.claude/yesmem/yesmem.db` | Main database. |
| `bleve_index` | string | `~/.claude/yesmem/bleve-index` | Full-text index. |
| `archive` | string | `~/.claude/yesmem/archive` | Archive path. |
| `claude_projects` | string | `~/.claude/projects` | Claude Code project directory. |

---

## default_sandbox_profile

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `default_sandbox_profile` | string | `""` | Default sandbox profile: `none`, `standard`, `strict`. Empty = `standard`. |

---

> **See also:** `config.example.yaml` in repo root (abridged version with inline comments).

### exclude_projects — Project Exclusion

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `exclude_projects` | []string | `[]` | Projects to exclude from session indexing. Prevents noise directories from being tracked. |

### caps_dir — CAP Storage

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `caps_dir` | string | `~/.claude/caps/` | Directory for CAP.md capability files. Runtime-agnostic storage for reusable tool definitions. |
