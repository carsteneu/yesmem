# Cache Keepalive Cost Analysis

**Model:** Claude Opus 4.6
**Basis:** 81 sessions, 22,356 requests over 33 days (March 12 – April 13, 2026)
**Source:** YesMem proxy logs (yesmem TIDs only, subagents and test sessions filtered)

## Anthropic Prompt Cache Pricing

### API / Pro (Pay-per-Token)

| Operation | $/MTok | Relative |
|---|---|---|
| Base Input (uncached) | $5.00 | 1.0x |
| 5min Cache Write | $6.25 | 1.25x |
| 1h Cache Write | $10.00 | 2.0x |
| Cache Read (Hit) | $0.50 | 0.1x |
| Output | $25.00 | — |

### Max Subscription (Flat-Rate with Token Budget)

Empirically validated by cnighswonger (1,500 calls, 6 quota windows):

| Operation | Budget Weight | Source |
|---|---|---|
| Cache Read | **0.0x** | Does NOT count toward 5h quota |
| Cache Write/Create | ~2.0x | Counts at ~2x input rate |
| Uncached Input | 1.0x | Full weight |
| Output | ~5.0x | Opus output rate = 5x input |

Source: [anthropics/claude-code#45756](https://github.com/anthropics/claude-code/issues/45756)

### Team / Enterprise / Max

Default 1h cache TTL. Same cache behavior across all subscription tiers — the only difference is the billing model (Max: token budget with `cache_read = 0.0x`; Team/Enterprise: separate infrastructure). As of April 2026, the 1h TTL appears broken for some users (falling back to 5min), confirmed by multiple reports and Jarred Sumner. Boris (Anthropic) states 1h is the intended default.

## Cache Behavior

Every API request produces:
- **Cache Read**: Tokens from previous turns that are still cached (prefix match)
- **Cache Write**: New tokens added in this turn (suffix) or full rewrite on cache miss
- **Cache Miss**: Full rewrite if the cache has expired (TTL exceeded) or prefix changed

A "Cache Miss" converts what would be a $0.50/MTok Read into a $6.25/MTok (5min) or $10.00/MTok (1h) Write on the entire prefix.

### TTL Behavior

- **5min (ephemeral)**: Cache expires 5 minutes after last request. Each cache hit resets the timer.
- **1h**: Cache expires 1 hour after last request. Costs 2x on writes but survives longer pauses.
- **Keepalive**: Proxy sends no-op API requests during idle periods to keep the cache warm. Each ping is a pure Read (~$0.07 at 140k prefix, or FREE at Max subscription).

### Cache-Busting Beyond TTL

Two additional cache invalidation mechanisms exist regardless of TTL (see [#47098](https://github.com/anthropics/claude-code/issues/47098), [#47107](https://github.com/anthropics/claude-code/issues/47107)):

1. **System prompt ordering instability**: Skills, CLAUDE.md, MCP blocks in `messages[0]` reorder non-deterministically between sessions. YesMem's `ReplaceSystemBlock` stabilizes this.
2. **git status changes**: File status updates in the system prompt change the byte prefix, busting cache on every file change.

## Empirical Data

### Gap Distribution (time between consecutive requests per thread)

| Gap Duration | Count | % of Total | Write Tokens |
|---|---|---|---|
| 0-10s | 10,094 | 45.3% | 38.0M |
| 10-30s | 7,419 | 33.3% | 86.9M |
| 30s-1m | 1,782 | 8.0% | 22.7M |
| 1-5m | 2,183 | 9.8% | 27.7M |
| **5-15m** | **452** | **2.0%** | **38.5M** |
| **15-30m** | **113** | **0.5%** | **10.3M** |
| **30-60m** | **71** | **0.3%** | **6.2M** |
| **1-2h** | **72** | **0.3%** | **6.3M** |
| **>2h** | **89** | **0.4%** | **7.7M** |

96.4% of gaps fall within 5 minutes (normal interactive use).
3.6% of gaps exceed 5 minutes — these are potential cache busts.
0.7% exceed 1 hour.

**Potential TTL busts: 797 over 33 days = ~24/day**

### External Validation

u/Medium_Island_2795 independently measured 199 busts/day without keepalive across ~1,140 sessions (Reddit, April 2026). Normalized per session: ~5 busts/session/day vs our ~3 busts/session/day. The difference is explained by workflow patterns; the monthly cost delta ($277.80) matches our keepalive savings ($280/month) within 1%.

## Strategy Comparison: API / Pro

Delta-based simulation: for each request, `prefix = in_tokens - delta` (reusable), `delta = in_tokens - prev_in_tokens` (new content). Cache hit: prefix is Read, delta is Write. Cache miss: everything is Write.

| Rank | Strategy | Input Cost | Busts | Pings | vs Best |
|---|---|---|---|---|---|
| **1** | **5min + 6 Pings (~27m)** | **$3,181** | 216 | 1,189 | **Baseline** |
| 2 | 5min + 9 Pings (~40m) | $3,185 | 190 | 1,399 | +$3 |
| 3 | 5min + 5 Pings (~24m) | $3,186 | 247 | 1,003 | +$5 |
| 4 | 5min + 3 Pings (~14m) | $3,202 | 298 | 773 | +$20 |
| 5 | 5min without Keepalive | $3,489 | 795 | 0 | +$307 |
| 6 | 1h + 6 Pings | $2,222 | 114 | 150 | — |
| 7 | 1h without Keepalive | $2,224 | 161 | 0 | — |

Note: 1h strategies appear cheaper in raw simulation but are NOT directly comparable — the 2x write surcharge ($10 vs $6.25/MTok) applies to ALL writes, not just busts. The surcharge on 273M normal write tokens adds $1,024 over 33 days ($31/day), which overwhelms any bust-prevention savings.

### Why 1h TTL Is a Trap for API/Pro

| Factor | 5min + 6 Pings | 1h + 6 Pings |
|---|---|---|
| Keepalive savings vs no-KA | $307/33d ($9.30/day) | $55/33d ($1.68/day) |
| Write surcharge (all writes) | $0 | +$1,024/33d ($31/day) |
| **Net vs 5min baseline** | **-$307 (savings)** | **+$969 (loss)** |

### Keepalive Ping Economics (API/Pro)

Break-even per ping: $0.07 (Read cost) vs $0.875 (avoided 5min Write cost at 140k prefix) = **12.5 pings per prevented bust**.

At 4:30 min interval, 6 pings bridge gaps up to ~27 minutes.

| Max Pings | Bridges up to | Cost | vs Best |
|---|---|---|---|
| 0 | — | $3,489 | +$307 |
| 3 | ~14min | $3,202 | +$20 |
| 5 | ~24min | $3,186 | +$5 |
| **6** | **~27min** | **$3,181** | **Baseline** |
| 9 | ~40min | $3,185 | +$3 |
| 12 | ~54min | $3,191 | +$9 |

6 pings is the empirical optimum because most idle gaps fall in the 5-27 minute range.

## Strategy Comparison: Max / Team / Enterprise

All subscription tiers share the same 1h cache TTL (when working correctly). The analysis uses Max budget weights (`cache_read = 0.0x`, `cache_create = 2.0x`) — Team/Enterprise billing differs but the cache mechanics and optimal strategy are identical.

| Rank | Strategy | Budget (Input) | Busts | Pings | vs Best |
|---|---|---|---|---|---|
| **1** | **1h + 6 Pings** | **283M** | 114 | 150 | **Baseline** |
| 2 | 1h + 3 Pings | 287M | 135 | 51 | +4M |
| 3 | 1h without Keepalive | 292M | 161 | 0 | +9M |
| 4 | 5min + 6 Pings | 303M | 216 | 1,203 | +20M |
| 5 | 5min + 5 Pings | 311M | 247 | 1,017 | +28M |
| 6 | 5min without Keepalive | **452M** | 799 | 0 | **+169M (+37.5%)** |

### Why 1h + Keepalive Is Optimal for Max / Team / Enterprise

1. **No write surcharge**: 1h writes cost the same budget weight as 5min writes (~2.0x). The factor that makes 1h a trap for API/Pro does not exist.
2. **Keepalive pings are FREE** (Max): Reads = 0.0x quota weight. There is zero cost to keeping the cache alive. For Team/Enterprise, pings are cheap reads.
3. **37.5% budget savings** (Max): 5min without keepalive burns 37.5% more budget than 1h + keepalive. This explains the Max users on Reddit exhausting their 5h quota in 1.5 hours.
4. **Currently broken**: As of April 2026, some users report 5min TTL instead of the intended 1h. YesMem's proxy forces 1h via `UpgradeCacheTTL`, working around this regression.

## Recommended Configuration

### API / Pro (Pay-per-Token)

```yaml
proxy:
  cache_ttl: "ephemeral"           # 5min — cheaper writes (1.25x vs 2.0x)
  cache_keepalive_enabled: true
  cache_keepalive_mode: "5m"
  cache_keepalive_pings_5m: 6      # bridges gaps up to ~27min, saves ~$9/day
```

### Max / Team / Enterprise (Subscription)

```yaml
proxy:
  cache_ttl: "1h"                  # no write surcharge, fewer busts
  cache_keepalive_enabled: true
  cache_keepalive_mode: "1h"
  cache_keepalive_pings_1h: 6      # pings are FREE at Max (reads = 0.0x quota)
```

## Key Insights

1. **Write price dominates for API/Pro**: 5min saves $3.75/MTok on every write vs 1h. With 273M write tokens over 33 days, that's $1,024 — far more than keepalive savings.

2. **Keepalive is critical for API/Pro**: $307 savings over 33 days ($9.30/day). 579 busts prevented at $0.875 each, minus $83 in ping costs.

3. **1h is strictly better for Max**: No write surcharge + reads are free = 37.5% less budget consumption. Keepalive is free bonus.

4. **TTL rarely matters within sessions**: 96.4% of gaps are under 5 minutes. The 3.6% that exceed 5min cause disproportionate cost.

5. **System prompt instability may matter more than TTL**: Non-deterministic block ordering and git-status changes bust cache regardless of TTL. Stabilizing the prefix (which YesMem's proxy does) is a prerequisite for any TTL strategy to work.

6. **Diminishing returns above 6 pings**: Gaps >27min are rare enough that additional pings don't pay for themselves (API/Pro) or are free but prevent very few additional busts (Max).

## When to Reconsider

- **If Anthropic equalizes write pricing**: 1h becomes strictly better for everyone.
- **If cache_read starts counting for Max quota**: Keepalive becomes a cost factor, reduce pings.
- **Meeting-heavy days (many 30-60min gaps)**: Consider 9 pings temporarily.
- **After proxy restart**: All per-thread cache state is lost. First request per thread is always a full write.
