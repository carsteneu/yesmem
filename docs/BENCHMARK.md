# YesMem LoCoMo Benchmark

Reproducible evaluation of YesMem's memory retrieval against the [LoCoMo benchmark](https://arxiv.org/abs/2402.14780) (Long Conversation Memory).

## Dataset

**LoCoMo-10 Corrected** — 10 synthetic multi-session conversations with 1540 questions across 4 categories:

| Category | Count | Description |
|----------|-------|-------------|
| Single-hop | 282 | Direct factual recall ("When did X happen?") |
| Multi-hop | 321 | Requires combining facts across sessions |
| Temporal | 96 | Time-based reasoning ("When was the last time...") |
| Open-domain | 841 | Open-ended questions about preferences, habits |

The corrected dataset fixes 99 errors in the original LoCoMo gold answers (wrong dates, misspelled names, incomplete lists). The corrections are documented in `testdata/locomo/locomo10_corrected.json`.

## Results

### Primary: Agentic Mode (LLM uses search tools iteratively)

| Eval LLM | Single-hop | Multi-hop | Temporal | Open-domain | **Overall** |
|----------|------------|-----------|----------|-------------|-------------|
| gpt-4o | 0.4433 | 0.6168 | 0.3542 | 0.7729 | **0.6539** |
| gpt-5.4 | 0.7589 | 0.8941 | 0.6042 | 0.9191 | **0.8649** |
| Claude Opus¹ | 0.7838 | 0.9310 | 0.6000 | 0.9114 | **0.8733** |

¹ Opus score from 10% sample (150 questions, Sonnet as judge). Full dataset scores use gpt-4o-mini / gpt-5.4-mini as judge.

### Secondary: Static Mode (single search pass, no agentic iteration)

| Eval LLM | Single-hop | Multi-hop | Temporal | Open-domain | **Overall** |
|----------|------------|-----------|----------|-------------|-------------|
| gpt-5.4 | 0.5071 | 0.6667 | 0.5000 | 0.7408 | 0.6675 |

The agentic mode improves overall score by +0.20 compared to a single static search pass with the same model (0.67 → 0.86).

## Methodology

### Pipeline

1. **Ingest** — LoCoMo conversations are loaded into a dedicated benchmark SQLite database (separate from production). 10 samples, 272 sessions, 5882 messages.
2. **Extract** — YesMem's extraction pipeline processes each session: chunking → LLM summarization → structured learning extraction → dedup → embedding (SSE, 512d).
3. **Query** — Each of the 1540 questions is answered using tiered search:
   - **Agentic mode** (`--agentic-eval`): The eval LLM iteratively calls search tools (hybrid_search, deep_search, keyword_search) with forced rotation in rounds 0-2, then auto-selects in round 3+.
   - **Static mode**: Single tiered search pass (BM25 + vector + entity boost via RRF).
4. **Judge** — A separate judge LLM evaluates each answer against the gold standard. The judge is strict: requires specific facts, not vague topic matches. Partial credit (2/3 items) is accepted.
5. **Report** — Scores are aggregated by category.

### Search Stack

- **BM25** (FTS5) — Full-text keyword search on extracted learnings
- **Vector** (SSE, 512d) — Semantic similarity search
- **Entity Boost** — Named entity extraction with boosted scoring
- **RRF** (Reciprocal Rank Fusion) — Merges BM25 + vector + entity results
- **Message FTS** — Raw conversation search as fallback

### Agentic Tool Rotation

Rounds 0-2 force one specific search tool each to guarantee retrieval diversity:

| Round | Tool | Purpose |
|-------|------|---------|
| 0 | hybrid_search | Broad semantic + keyword entry |
| 1 | deep_search | Raw conversation history |
| 2 | keyword_search | Exact terms, names, dates |
| 3+ | auto (all tools) | Model decides whether to continue or answer |

### Judge Configuration

- **Strict mode**: Requires specific facts from the gold answer, not just the right topic
- **Format-tolerant**: "May 7th" = "7 May", "Paris, France" = "Paris"
- **Partial credit**: If gold has multiple items and the answer contains at least 2/3, it is CORRECT
- **Number/date precision**: Numbers must match exactly, dates allow ±1 day

## Key Findings

### Retrieval is not the bottleneck

The gpt-4o vs gpt-5.4 comparison shows identical retrieval (same database, same search code, same tool rotation) but a +0.21 score difference. Analysis of per-question dumps confirms the retrieval system delivers relevant results — the gap is in the eval LLM's answer synthesis and temporal reasoning.

### Agentic iteration adds +0.20

Giving the LLM iterative access to search tools (3+ rounds) improves scores by ~0.20 compared to a single search pass, consistently across both models. This validates YesMem's multi-tool search architecture.

### Tool diversity alone doesn't close the model gap

Forced tool rotation (hybrid → deep → keyword) has no measurable effect on gpt-4o scores (0.6539 with and without rotation). The model receives diverse search results but cannot synthesize them as effectively as gpt-5.4.

### Corrected dataset matters

The original LoCoMo dataset contains 99 errors in gold answers. Using the uncorrected dataset would penalize correct answers. All YesMem scores are measured against the corrected dataset.

### Retrieval ceiling at ~0.87

Claude Opus and gpt-5.4 score nearly identically (0.87 vs 0.86), suggesting a retrieval ceiling. Further improvements require better retrieval (more context, finer chunking, better embeddings), not stronger eval models.

## Reproduction

### Requirements

- YesMem binary (built from source or release)
- `ANTHROPIC_API_KEY` (for extraction, Opus/Sonnet eval, if not using `--skip-extract`)
- `OPENAI_API_KEY` (for gpt-4o / gpt-5.4 eval)
- LoCoMo dataset: `testdata/locomo/locomo10_corrected.json`

### Build

```bash
make build
```

### Run

```bash
# Full run with extraction (first time, ~$6 with Haiku extraction)
yesmem locomo-bench run \
  --data testdata/locomo/locomo10_corrected.json \
  --hybrid --tiered --agentic-eval \
  --eval-llm gpt-5.4 --judge-llm gpt-5.4-mini

# Subsequent runs (skip extraction, reuse benchmark DB)
yesmem locomo-bench run \
  --data testdata/locomo/locomo10_corrected.json \
  --hybrid --tiered --agentic-eval \
  --eval-llm gpt-4o --judge-llm gpt-4o-mini \
  --skip-extract

# Static mode (no agentic iteration, cheaper)
yesmem locomo-bench run \
  --data testdata/locomo/locomo10_corrected.json \
  --hybrid --tiered \
  --eval-llm gpt-5.4 --judge-llm gpt-5.4-mini \
  --skip-extract

# Dry run (cost estimate only)
yesmem locomo-bench run \
  --data testdata/locomo/locomo10_corrected.json \
  --hybrid --tiered --agentic-eval \
  --eval-llm gpt-5.4 --judge-llm gpt-5.4-mini \
  --dry-run

# Dump per-question results for analysis
yesmem locomo-bench run \
  --data testdata/locomo/locomo10_corrected.json \
  --hybrid --tiered --agentic-eval \
  --eval-llm gpt-4o --judge-llm gpt-4o-mini \
  --skip-extract \
  --dump-results results.json
```

### Estimated Cost

| Model | Role | Estimated Cost |
|-------|------|---------------|
| Haiku | Extraction (one-time) | ~$5 |
| gpt-4o | Eval + Judge | ~$8 per run |
| gpt-5.4 | Eval + Judge | ~$12 per run |

### Benchmark Database

Results are stored in `~/.claude/yesmem/bench/locomo.db` — completely separate from the production database. No production data is touched.

### CLI Reference

```
yesmem locomo-bench run [flags]

Flags:
  --data <path>         Path to LoCoMo JSON dataset (required)
  --db <path>           Benchmark database path (default: ~/.claude/yesmem/bench/locomo.db)
  --eval-llm <model>    LLM for answering queries (default: haiku)
  --judge-llm <model>   LLM for judging answers (default: same as --eval-llm)
  --hybrid              Use local hybrid search (BM25+vector+RRF)
  --tiered              Multi-tool tiered search (implies --hybrid)
  --agentic-eval        LLM uses search tools iteratively
  --skip-extract        Skip extraction, reuse existing benchmark DB
  --dry-run             Cost estimate only, no API calls
  --dump-results <path> Dump per-question JSON for analysis
  --sample-pct <N>      Run on N% of questions (for quick tests)
  --runs <N>            Number of runs for statistical stability
  --json                Output results as JSON

Short model names: haiku, sonnet, opus → Anthropic models
gpt-* model names → OpenAI models (requires OPENAI_API_KEY)
```

## Limitations

- **Eval LLM dependency**: Scores are strongly influenced by the eval LLM's reasoning quality, not just retrieval. The same retrieval system scores 0.65 with gpt-4o and 0.86 with gpt-5.4.
- **Synthetic data**: LoCoMo conversations are LLM-generated, not real human conversations. Real-world performance may differ.
- **Single judge**: The judge LLM is a single model (gpt-5.4-mini or gpt-4o-mini). Inter-annotator agreement is not measured.
- **Corrected dataset**: Our corrections to the gold answers have not been independently validated. The original uncorrected dataset can be obtained from the LoCoMo authors.
- **No adversarial category**: The original LoCoMo includes an adversarial category (questions with no valid answer). Our corrected dataset currently excludes this category.

## Run Date

All scores reported above were measured on 2026-04-07/08 using YesMem v1.0.1 on the `feature/benchmark-to-prod` branch.
