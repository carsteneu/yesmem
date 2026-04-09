package proxy

import (
	"testing"
	"time"
)

func TestCacheTTLDetector_InitialState(t *testing.T) {
	det := NewCacheTTLDetector()
	if det.Is1hSupported() != nil {
		t.Error("initial state should be nil (unknown)")
	}
	if det.EffectiveTTLSeconds() != 300 {
		t.Errorf("unknown state should return 300, got %d", det.EffectiveTTLSeconds())
	}
}

func TestCacheTTLDetector_ImmediateNegative(t *testing.T) {
	det := NewCacheTTLDetector()
	// Substantial write (read=0) with ephemeral_1h=0 → server rejected 1h
	det.RecordResponse(0, 87000, 0)

	sup := det.Is1hSupported()
	if sup == nil || *sup {
		t.Error("substantial write with ephemeral_1h=0 should deny 1h")
	}
}

func TestCacheTTLDetector_PositiveAfterGap_CacheWarm(t *testing.T) {
	det := NewCacheTTLDetector()
	// Simulate >5:01 gap
	det.mu.Lock()
	det.threadRequests["t1"] = time.Now().Add(-6 * time.Minute)
	det.currentThread = "t1"
	det.mu.Unlock()

	// After gap: 97% cache hit → 1h confirmed
	det.RecordResponse(112000, 2000, 0)

	sup := det.Is1hSupported()
	if sup == nil || !*sup {
		t.Error("cache_read >80% after >5:01 gap should confirm 1h")
	}
	if det.EffectiveTTLSeconds() != 3600 {
		t.Errorf("expected 3600, got %d", det.EffectiveTTLSeconds())
	}
}

func TestCacheTTLDetector_NegativeAfterGap_CacheCold(t *testing.T) {
	det := NewCacheTTLDetector()
	// Simulate >5:01 gap
	det.mu.Lock()
	det.threadRequests["t1"] = time.Now().Add(-6 * time.Minute)
	det.currentThread = "t1"
	det.mu.Unlock()

	// After gap: cache cold (0% read), ephemeral_1h=0 → 5min
	det.RecordResponse(0, 87000, 0)

	sup := det.Is1hSupported()
	if sup == nil || *sup {
		t.Error("cold cache after gap with ephemeral_1h=0 should deny 1h")
	}
}

func TestCacheTTLDetector_FirstResponseInconclusive(t *testing.T) {
	det := NewCacheTTLDetector()
	det.RecordRequest("t1")
	// First response: ephemeral_1h > 0 but no gap → inconclusive
	det.RecordResponse(35000, 87000, 87000)

	if det.Is1hSupported() != nil {
		t.Error("first response with no gap should remain unknown")
	}
}

func TestCacheTTLDetector_IgnoresDeltaWrites(t *testing.T) {
	det := NewCacheTTLDetector()
	// read=100k, write=2k (2%) — but >80% hit ratio → no gap so stays unknown
	det.RecordResponse(100000, 2000, 0)
	if det.Is1hSupported() != nil {
		t.Error("no gap: should remain unknown")
	}
}

func TestCacheTTLDetector_ShortGapInconclusive(t *testing.T) {
	det := NewCacheTTLDetector()
	det.mu.Lock()
	det.threadRequests["t1"] = time.Now().Add(-2 * time.Minute)
	det.currentThread = "t1"
	det.mu.Unlock()

	// 2min gap with warm cache — not enough to confirm
	det.RecordResponse(100000, 5000, 5000)
	if det.Is1hSupported() != nil {
		t.Error("<5:01 gap should remain unknown even with warm cache")
	}
}

func TestCacheTTLDetector_IgnoresEmpty(t *testing.T) {
	det := NewCacheTTLDetector()
	det.RecordResponse(0, 0, 0)
	if det.Is1hSupported() != nil {
		t.Error("no tokens should not change state")
	}
}

// Regression: interleaved threads must not corrupt gap measurement.
// Thread A at t=0, Thread B at t=3min, Thread A at t=7min:
// Thread A's gap should be 7min (> 5:01), not 4min (from B's timestamp).
func TestCacheTTLDetector_PerThreadGapMeasurement(t *testing.T) {
	det := NewCacheTTLDetector()

	// Thread A at t=0
	det.RecordRequest("thread-A")
	det.RecordResponse(0, 87000, 87000) // initial write, inconclusive

	// Thread B at t=3min — should NOT affect thread A's gap
	det.mu.Lock()
	det.threadRequests["thread-B"] = time.Now().Add(-3 * time.Minute)
	det.threadRequests["thread-A"] = time.Now().Add(-7 * time.Minute)
	det.mu.Unlock()

	// Thread A at t=7min (7min gap > 5:01)
	det.RecordRequest("thread-A")
	det.RecordResponse(100000, 2000, 0) // >80% read after gap → 1h confirmed

	sup := det.Is1hSupported()
	if sup == nil || !*sup {
		t.Error("thread A had 7min gap with warm cache — should confirm 1h")
	}
}

func TestCacheTTLDetector_PersistAndReload(t *testing.T) {
	dir := t.TempDir()
	det := NewCacheTTLDetectorWithPersist(dir)

	// Simulate 1h detection
	det.mu.Lock()
	det.threadRequests["t1"] = time.Now().Add(-6 * time.Minute)
	det.currentThread = "t1"
	det.mu.Unlock()
	det.RecordResponse(100000, 2000, 0) // >80% read after gap → 1h

	sup := det.Is1hSupported()
	if sup == nil || !*sup {
		t.Fatal("detection should confirm 1h")
	}

	// Load in new detector
	det2 := NewCacheTTLDetectorWithPersist(dir)
	sup2 := det2.Is1hSupported()
	if sup2 == nil || !*sup2 {
		t.Error("reloaded detector should have persisted 1h state")
	}
	if det2.EffectiveTTLSeconds() != 3600 {
		t.Errorf("expected 3600, got %d", det2.EffectiveTTLSeconds())
	}
}
