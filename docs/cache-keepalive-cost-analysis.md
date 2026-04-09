# Cache Keepalive Cost Analysis - one random day in April

**Model:** Claude Opus 4.6
**Basis:** 8 concurrent sessions, random day (April, 2026), 1.198 requests total

## Anthropic Prompt Cache Pricing (Opus 4.6)

| Operation | $/MTok | Relative |
|---|---|---|
| Base Input (uncached) | $5.00 | 1.0× |
| 5min Cache Write | $6.25 | 1.25× |
| 1h Cache Write | $10.00 | 2.0× |
| Cache Read (Hit) | $0.50 | 0.1× |
| Output | $25.00 | — |

## Cache Behavior

Every API request produces:
- **Cache Read**: Tokens from previous turns that are still cached (prefix)
- **Cache Write**: New tokens added in this turn (suffix)
- **Cache Miss**: Full rewrite if the cache has expired (TTL exceeded)

A "Cache Miss" converts what would be a $0.50/MTok Read into a $6.25/MTok (5min) or $10.00/MTok (1h) Write on the entire prefix.

### TTL Behavior

- **5min (ephemeral)**: Cache expires 5 minutes after last request. Each cache hit resets the timer.
- **1h**: Cache expires 1 hour after last request. Costs 2× on writes but survives longer pauses.
- **Keepalive**: Proxy sends no-op API requests during idle periods to keep the cache warm. Each ping is a pure Read (~$0.07 at 140k prefix).

## Empirical Data — one day in 2026

### Per-Session Breakdown

| Session | Reqs | Reads | Writes | Gaps 5-60m | Gaps >60m |
|---|---|---|---|---|---|
| 059b6ebe | 360 | 44.8M | 4.5M | 16 | 1 |
| 1e749247 | 342 | 41.3M | 2.0M | 13 | 1 |
| bd529383 | 159 | 20.2M | 3.2M | 5 | 2 |
| f0f4f394 | 110 | 10.4M | 0.4M | 1 | 0 |
| unknown | 107 | 5.3M | 0.7M | 4 | 2 |
| 37f74a0b | 97 | 10.7M | 0.4M | 2 | 2 |
| 6e7f0a7e | 27 | 2.7M | 0.5M | 1 | 1 |
| d9377e1f | 7 | 0.3M | 0.1M | 1 | 0 |
| **Total** | **1.209** | **135.7M** | **11.8M** | **43** | **9** |

### Write Size Distribution

| Write Size | Events | Tokens | Interpretation |
|---|---|---|---|
| 0-5k | 357 | 695k | Incremental — new message appended |
| 5-20k | 91 | 929k | Incremental — larger tool results |
| 20-50k | 36 | 1.220k | Grey zone — compaction or large injection |
| 50-100k | 49 | 3.883k | Full rewrite — cache invalidated |
| >100k | 43 | 5.849k | Full rewrite — entire prefix rewritten |

92 events with >50k writes = actual cache invalidations (compaction/sawtooth).
448 events with <20k writes = normal incremental growth (TTL-independent).

### Idle Gap Distribution

| Gap Duration | Write Events | Write Tokens |
|---|---|---|
| 0-10s | 331 | 6.467k |
| 10-30s | 161 | 4.065k |
| 30-60s | 40 | 1.127k |
| 1-5min | 40 | 833k |
| 5-60min | 3 | 4k |
| >60min | 1 | 80k |

99.3% of writes occur within 5 minutes of the previous request — these are normal turn writes, not cache misses.

## Strategy Comparison

### Four-Way Comparison (all sessions, full day)

| Rank | Strategy | Cost | vs Best |
|---|---|---|---|
| **1** | **5min + 6 Keepalive** | **$150.39** | Baseline |
| 2 | 5min + 12 Keepalive | $151.44 | +$1.05 (+0.7%) |
| 3 | 5min without Keepalive | $167.90 | +$17.51 (+11.6%) |
| 4 | 1h + Keepalive | $184.56 | +$34.17 (+22.7%) |
| 5 | 1h without Keepalive | $189.09 | +$38.70 (+25.7%) |

### Per-Session Detail (Top 3)

**Session 059b6ebe** (360 reqs, 16 gaps 5-60min, 1 gap >60min):

| Strategy | Cost |
|---|---|
| 5min + 6 KA | $150.39 |
| 1h + KA | $67.12 |
| 1h no KA | $68.76 |
| 5min no KA | $62.17 |

**Session 1e749247** (342 reqs, 13 gaps 5-60min, 1 gap >60min):

| Strategy | Cost |
|---|---|
| 5min + 6 KA | $37.47 |
| 1h + KA | $40.28 |
| 1h no KA | $41.60 |
| 5min no KA | $42.41 |

## Keepalive Ping Optimization

Break-even per ping: $0.07 (Read cost) vs $0.875 (avoided Write cost) = **12.5 pings per gap**.

At 4:30min interval, 6 pings bridge gaps up to ~27 minutes.

| Max Pings | Bridges up to | Cost | vs 1h+KA |
|---|---|---|---|
| 0 | — | $167.90 | -$16.66 |
| 3 | ~14min | $150.94 | -$33.62 |
| **6** | **~27min** | **$150.39** | **-$34.17** |
| 9 | ~40min | $151.44 | -$33.12 |
| 12 | ~54min | $151.44 | -$33.12 |
| 18 | ~81min | $155.15 | -$29.41 |
| unlimited | ∞ | $166.47 | -$18.09 |

6 pings is the empirical optimum because most idle gaps fall in the 5-27 minute range (coffee, reading code, thinking). Beyond that, the few longer gaps don't justify the ping cost.

## Key Insights

1. **Write price dominates**: 5min saves $6.25→$10.00 per MTok on *every* write. With 11.8M write tokens/day, that's ~$44/day.

2. **TTL rarely matters**: Only 0.7% of writes are caused by pauses >5min. 99.3% are incremental (new turns) or compaction-triggered — both happen regardless of TTL.

3. **1h TTL is a trap for active coders**: The 2× write surcharge applies to *all* writes, not just the few that benefit from the longer TTL window.

4. **Keepalive pays for itself**: At $0.07/ping vs $0.875/avoided rewrite, even a single prevented cache miss covers 12 pings.

5. **Diminishing returns above 6 pings**: Gaps >27min are rare enough that the cumulative ping cost exceeds the savings from preventing those few rewrites.

## Recommended Configuration

```yaml
proxy:
  cache_ttl: "ephemeral"           # 5min — cheaper writes
  cache_keepalive_enabled: true
  cache_keepalive_mode: "5m"
  cache_keepalive_pings_5m: 6      # bridges gaps up to ~27min
```

## When to Reconsider

- **Meeting-heavy days** (many 15-60min gaps): More pings might help — but even then, 6 covers most meetings.
- **1h TTL becomes free**: If Anthropic equalizes write pricing, 1h becomes strictly better.
- **Usage pattern changes**: If sessions shift to fewer, longer gaps (>30min), increasing pings to 9 may help.
