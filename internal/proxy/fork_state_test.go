package proxy

import "testing"

func TestForkState_TokenGrowthDelta(t *testing.T) {
	fs := NewForkState(20000, 0, 3, 50) // 20k trigger, max 3 failures, 50 max forks
	fs.MarkCacheWarm("thread-1")

	// First call establishes baseline — no fork
	if fs.ShouldFork("thread-1", 10000, true) {
		t.Error("first call should never fork (establishes baseline)")
	}

	// 5k growth (10000 → 15000) — not enough
	if fs.ShouldFork("thread-1", 15000, true) {
		t.Error("should not fork with only 5k growth")
	}

	// 20k more growth (15000 → 35000) — triggers
	if !fs.ShouldFork("thread-1", 35000, true) {
		t.Error("should fork at 20k+ growth since baseline")
	}

	// After fork, counter resets — next call with small growth should not fork
	fs.RecordFork("thread-1")
	if fs.ShouldFork("thread-1", 37000, true) {
		t.Error("should not fork right after reset (only 2k growth)")
	}

	// Another 20k growth after reset (37000 → 58000) — triggers again
	if !fs.ShouldFork("thread-1", 58000, true) {
		t.Error("should fork again after 20k+ growth since last fork")
	}
}

func TestForkState_AbsoluteTokensNotAccumulated(t *testing.T) {
	// Regression test: ShouldFork must use delta, not accumulate absolute values
	fs := NewForkState(20000, 0, 3, 50)
	fs.MarkCacheWarm("t1")

	// Simulate realistic API call pattern: 100k context, growing slowly
	fs.ShouldFork("t1", 100000, true) // baseline
	fs.RecordFork("t1")               // fork triggered, reset

	// Next request: 105k (only 5k growth) — must NOT fork
	if fs.ShouldFork("t1", 105000, true) {
		t.Error("BUG: absolute tokens accumulated instead of delta — 5k growth should not trigger 20k threshold")
	}

	// 125k (20k growth since fork) — should fork
	if !fs.ShouldFork("t1", 125000, true) {
		t.Error("should fork after 20k growth since last fork")
	}
}

func TestForkState_FailureDisable(t *testing.T) {
	fs := NewForkState(20000, 0, 3, 50)

	// 3 consecutive failures -> disabled
	fs.RecordFailure("thread-1")
	fs.RecordFailure("thread-1")
	fs.RecordFailure("thread-1")

	if !fs.IsDisabled("thread-1") {
		t.Error("should be disabled after 3 failures")
	}

	// Other thread unaffected
	if fs.IsDisabled("thread-2") {
		t.Error("other thread should not be disabled")
	}
}

func TestForkState_SuccessResetsFailures(t *testing.T) {
	fs := NewForkState(20000, 0, 3, 50)
	fs.MarkCacheWarm("thread-1")

	fs.RecordFailure("thread-1")
	fs.RecordFailure("thread-1")
	fs.RecordFork("thread-1") // success resets failure count

	if fs.IsDisabled("thread-1") {
		t.Error("success should reset failure count")
	}
}

func TestForkState_NoCacheNoFork(t *testing.T) {
	fs := NewForkState(20000, 0, 3, 50)

	// Even with enough growth, no fork if no cache
	if fs.ShouldFork("thread-1", 30000, false) {
		t.Error("should not fork without cache")
	}
}

func TestForkState_DisabledSkipsAccumulation(t *testing.T) {
	fs := NewForkState(20000, 0, 3, 50)
	fs.MarkCacheWarm("t1")

	// Disable thread
	fs.RecordFailure("t1")
	fs.RecordFailure("t1")
	fs.RecordFailure("t1")

	// Call ShouldFork many times while disabled — should not accumulate
	fs.ShouldFork("t1", 100000, true)
	fs.ShouldFork("t1", 200000, true)
	fs.ShouldFork("t1", 300000, true)

	// Verify internal state didn't balloon (re-enable manually for check)
	fs.mu.Lock()
	ts := fs.threads["t1"]
	if ts.lastTotalTokens > 0 {
		t.Errorf("disabled thread should not track tokens, got lastTotalTokens=%d", ts.lastTotalTokens)
	}
	fs.mu.Unlock()
}

func TestForkState_MaxForksPerSession(t *testing.T) {
	fs := NewForkState(1000, 0, 3, 3) // low trigger, max 3 forks
	fs.MarkCacheWarm("t1")

	// Fork 3 times
	fs.ShouldFork("t1", 5000, true)
	fs.RecordFork("t1")
	fs.ShouldFork("t1", 10000, true)
	fs.RecordFork("t1")
	fs.ShouldFork("t1", 15000, true)
	fs.RecordFork("t1")

	// 4th fork should be blocked by maxForksPerSession
	if fs.ShouldFork("t1", 25000, true) {
		t.Error("should not fork after reaching maxForksPerSession")
	}
}

func TestForkState_ForkPendingPreventsStacking(t *testing.T) {
	fs := NewForkState(10000, 0, 3, 0)
	fs.MarkCacheWarm("t1")

	if !fs.ShouldFork("t1", 50000, true) {
		t.Fatal("first fork should fire (50k tokens)")
	}
	if fs.ShouldFork("t1", 51000, true) {
		t.Error("ShouldFork should be false while fork is pending (RecordFork not called yet)")
	}
	fs.RecordFork("t1")
	if fs.ShouldFork("t1", 55000, true) {
		t.Error("ShouldFork should be false: only 5k growth since last fork (50k→55k)")
	}
	if !fs.ShouldFork("t1", 65000, true) {
		t.Error("ShouldFork should fire: 15k growth since last fork (50k→65k)")
	}
}

func TestForkState_RecordFailureResetsTokenGrowth(t *testing.T) {
	fs := NewForkState(20000, 0, 3, 50)
	fs.MarkCacheWarm("t")

	// First fork: 25k tokens, enough to trigger
	if !fs.ShouldFork("t", 25000, true) {
		t.Error("first fork should fire at 25k")
	}

	// Fork fails
	fs.RecordFailure("t")

	// After failure, tokensSinceLastFork is reset. Next request with
	// even slightly more tokens should NOT re-fire immediately.
	if fs.ShouldFork("t", 26000, true) {
		t.Error("after RecordFailure, fork should NOT re-fire immediately (tokensSinceLastFork must be reset)")
	}

	// 20k growth from the baseline triggers the next fork
	if !fs.ShouldFork("t", 46000, true) {
		t.Error("fork should fire after 20k growth (26k→46k)")
	}
}

func TestForkState_MinTotalTokens_PreventsSmallSessionForks(t *testing.T) {
	fs := NewForkState(20000, 60000, 3, 50)
	fs.MarkCacheWarm("t")

	// Session at 25k with 20k growth: growth satisfied but total < 60k
	if fs.ShouldFork("t", 25000, true) {
		t.Error("fork should NOT fire: total=25k < min=60k, even with 20k growth")
	}

	// Session at 59k: still below minimum. Growth is NOT accumulated
	// while total < min, so no delta tracking yet.
	if fs.ShouldFork("t", 59000, true) {
		t.Error("fork should NOT fire: total=59k < min=60k")
	}

	// At 61k: total >= min, growth from 0→61k triggers fork
	if !fs.ShouldFork("t", 61000, true) {
		t.Error("fork should fire: total=61k >= min=60k, growth=61k >= 20k")
	}
}

func TestForkState_MinTotalTokens_ZeroAllowsAll(t *testing.T) {
	fs := NewForkState(20000, 0, 3, 50)
	fs.MarkCacheWarm("t")

	// min=0: any session can fork immediately at 20k growth
	if !fs.ShouldFork("t", 25000, true) {
		t.Error("fork should fire: minTotalTokens=0 disables the minimum check")
	}
}

func TestForkState_CacheProvenGate(t *testing.T) {
	// After a deploy, no session has proven cache warmth.
	// The caller (openai_parity.go) must check IsCacheProven before ShouldFork.
	fs := NewForkState(20000, 0, 3, 50)

	// Session with enough growth and hasCache=true, but cache NOT yet proven
	// Caller-side gate: IsCacheProven is false → skip ShouldFork entirely
	if fs.IsCacheProven("t1") {
		t.Error("new session should NOT be cache-proven")
	}
	// This demonstrates the caller pattern: only call ShouldFork if cache is proven
	if fs.IsCacheProven("t1") && fs.ShouldFork("t1", 25000, true) {
		t.Error("should NOT fork: cache not yet proven")
	}

	// Mark cache as proven (happens when a main request returns cacheRatio > 0.9)
	// Cache must be proven twice before a fork is allowed
	fs.MarkCacheWarm("t1")
	fs.MarkCacheWarm("t1")

	// Now the caller sees IsCacheProven=true and ShouldFork returns true with sufficient growth
	if !fs.IsCacheProven("t1") {
		t.Error("t1 should be cache-proven after MarkCacheWarm")
	}
	if !fs.ShouldFork("t1", 25000, true) {
		t.Error("should fork: cache proven + hasCache=true + growth=25k")
	}
}

func TestForkState_CacheProvenResetsOnNewState(t *testing.T) {
	// When ForkState is freshly created (after deploy), no sessions are cache-proven.
	fs := NewForkState(20000, 0, 3, 50)
	if fs.IsCacheProven("t1") {
		t.Error("new ForkState should have no cache-proven sessions")
	}
}

func TestForkState_CacheProvenPerSession(t *testing.T) {
	fs := NewForkState(20000, 0, 3, 50)

	// Prove cache twice for t1
	fs.MarkCacheWarm("t1")
	fs.MarkCacheWarm("t1")
	if !fs.IsCacheProven("t1") {
		t.Error("t1 should be cache-proven after MarkCacheWarm")
	}

	// t2 is NOT cache-proven
	if fs.IsCacheProven("t2") {
		t.Error("t2 should NOT be cache-proven (never marked)")
	}

	// Caller pattern: t1 can fork (cache proven), t2 cannot (not proven)
	if !fs.IsCacheProven("t1") || !fs.ShouldFork("t1", 25000, true) {
		t.Error("t1 should fork: cache proven")
	}
	if fs.IsCacheProven("t2") && fs.ShouldFork("t2", 25000, true) {
		t.Error("t2 should NOT fork: cache not proven")
	}
}
