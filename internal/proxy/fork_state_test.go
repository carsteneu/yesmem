package proxy

import "testing"

func TestForkState_TokenGrowthDelta(t *testing.T) {
	fs := NewForkState(20000, 3, 50) // 20k trigger, max 3 failures, 50 max forks

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
	fs := NewForkState(20000, 3, 50)

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
	fs := NewForkState(20000, 3, 50)

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
	fs := NewForkState(20000, 3, 50)

	fs.RecordFailure("thread-1")
	fs.RecordFailure("thread-1")
	fs.RecordFork("thread-1") // success resets failure count

	if fs.IsDisabled("thread-1") {
		t.Error("success should reset failure count")
	}
}

func TestForkState_NoCacheNoFork(t *testing.T) {
	fs := NewForkState(20000, 3, 50)

	// Even with enough growth, no fork if no cache
	if fs.ShouldFork("thread-1", 30000, false) {
		t.Error("should not fork without cache")
	}
}

func TestForkState_DisabledSkipsAccumulation(t *testing.T) {
	fs := NewForkState(20000, 3, 50)

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
	fs := NewForkState(1000, 3, 3) // low trigger, max 3 forks

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
