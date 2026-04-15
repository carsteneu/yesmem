package proxy

import (
	"testing"
	"time"
)

func TestFrozenStubs_StoreAndGet(t *testing.T) {
	f := NewFrozenStubs()

	msgs := []any{
		map[string]any{"role": "user", "content": "hello"},
		map[string]any{"role": "assistant", "content": "hi"},
		map[string]any{"role": "user", "content": "how are you"},
	}

	// Stub messages 0-1, cutoff=2, boundary=msgs[1]
	stubbed := []any{
		map[string]any{"role": "user", "content": "[stub: hello]"},
		map[string]any{"role": "assistant", "content": "[stub: hi]"},
	}
	f.Store("thread1", stubbed, 2, msgs[1], 5000, 0)

	// Get with valid current messages (3 msgs, cutoff=2, boundary matches)
	result := f.Get("thread1", msgs)
	if result == nil {
		t.Fatal("expected frozen stubs, got nil")
	}
	if result.Cutoff != 2 {
		t.Errorf("cutoff: expected 2, got %d", result.Cutoff)
	}
	if result.Tokens != 5000 {
		t.Errorf("tokens: expected 5000, got %d", result.Tokens)
	}
	if len(result.Messages) != 2 {
		t.Errorf("messages: expected 2, got %d", len(result.Messages))
	}
}

func TestFrozenStubs_DeepCopy(t *testing.T) {
	f := NewFrozenStubs()

	original := []any{
		map[string]any{"role": "user", "content": "hello"},
	}
	boundary := map[string]any{"role": "user", "content": "boundary"}
	f.Store("t1", original, 1, boundary, 100, 0)

	// Mutate original — frozen should be unaffected
	original[0].(map[string]any)["content"] = "MUTATED"

	currentMsgs := []any{boundary, map[string]any{"role": "user", "content": "new"}}
	result := f.Get("t1", currentMsgs)
	if result == nil {
		t.Fatal("expected frozen stubs")
	}
	content := result.Messages[0].(map[string]any)["content"]
	if content == "MUTATED" {
		t.Error("deep copy failed: frozen stubs were mutated")
	}
	if content != "hello" {
		t.Errorf("expected 'hello', got %v", content)
	}
}

func TestFrozenStubs_BoundaryChangeStillWorks(t *testing.T) {
	f := NewFrozenStubs()

	boundary := map[string]any{"role": "assistant", "content": "original"}
	stubbed := []any{map[string]any{"role": "user", "content": "[stub]"}}
	f.Store("t1", stubbed, 1, boundary, 100, 0)

	// Different boundary message — should STILL return stubs (boundary hash removed)
	differentBoundary := []any{
		map[string]any{"role": "assistant", "content": "CHANGED"},
		map[string]any{"role": "user", "content": "new"},
	}
	result := f.Get("t1", differentBoundary)
	if result == nil {
		t.Error("expected stubs even with changed boundary (validation removed)")
	}
	if !f.Has("t1") {
		t.Error("thread should still be valid")
	}
}

func TestFrozenStubs_TooFewMessages(t *testing.T) {
	f := NewFrozenStubs()

	boundary := map[string]any{"role": "assistant", "content": "ok"}
	stubbed := []any{map[string]any{"role": "user", "content": "[stub]"}}
	f.Store("t1", stubbed, 3, boundary, 100, 0)

	// Only 2 messages but cutoff=3 → not enough
	result := f.Get("t1", []any{
		map[string]any{"role": "user", "content": "a"},
		map[string]any{"role": "assistant", "content": "b"},
	})
	if result != nil {
		t.Error("expected nil when message count < cutoff")
	}
}

func TestFrozenStubs_Invalidate(t *testing.T) {
	f := NewFrozenStubs()

	stubbed := []any{map[string]any{"role": "user", "content": "[stub]"}}
	f.Store("t1", stubbed, 1, map[string]any{}, 100, 0)

	if !f.Has("t1") {
		t.Fatal("expected Has=true after Store")
	}

	f.Invalidate("t1")

	if f.Has("t1") {
		t.Error("expected Has=false after Invalidate")
	}
}

func TestFrozenStubs_Evict(t *testing.T) {
	f := NewFrozenStubs() // default 30 min TTL

	stubbed := []any{map[string]any{"role": "user", "content": "[stub]"}}
	f.Store("old", stubbed, 1, map[string]any{}, 100, 0)
	f.Store("new", stubbed, 1, map[string]any{}, 100, 0)

	// Backdate the "old" thread past default TTL (30 min)
	f.mu.Lock()
	f.lastAccess["old"] = time.Now().Add(-31 * time.Minute)
	f.mu.Unlock()

	evicted := f.Evict()
	if evicted != 1 {
		t.Errorf("expected 1 evicted, got %d", evicted)
	}
	if f.Has("old") {
		t.Error("old thread should be evicted")
	}
	if !f.Has("new") {
		t.Error("new thread should still exist")
	}
}

func TestFrozenStubs_EvictRespectsCustomTTL(t *testing.T) {
	f := NewFrozenStubsWithTTL(65 * time.Minute) // 1h cache TTL

	stubbed := []any{map[string]any{"role": "user", "content": "[stub]"}}
	f.Store("thread1", stubbed, 1, map[string]any{}, 100, 0)

	// 40 min idle — should NOT be evicted (40 < 65)
	f.mu.Lock()
	f.lastAccess["thread1"] = time.Now().Add(-40 * time.Minute)
	f.mu.Unlock()

	if n := f.Evict(); n != 0 {
		t.Errorf("expected 0 evicted at 40min (TTL=65min), got %d", n)
	}
	if !f.Has("thread1") {
		t.Error("thread should survive at 40min with 65min TTL")
	}

	// 70 min idle — should be evicted (70 > 65)
	f.mu.Lock()
	f.lastAccess["thread1"] = time.Now().Add(-70 * time.Minute)
	f.mu.Unlock()

	if n := f.Evict(); n != 1 {
		t.Errorf("expected 1 evicted at 70min (TTL=65min), got %d", n)
	}
}

func TestSawtoothTTLForCacheTTL(t *testing.T) {
	// 1h cache → TTL must exceed 60 min
	got := sawtoothTTLForCacheTTL("1h")
	if got < 61*time.Minute {
		t.Errorf("1h cache TTL: frozen stubs TTL %v too short (must exceed 60min)", got)
	}
	// ephemeral → default 30 min
	got = sawtoothTTLForCacheTTL("ephemeral")
	if got != 30*time.Minute {
		t.Errorf("ephemeral: expected 30min, got %v", got)
	}
	// empty → default
	got = sawtoothTTLForCacheTTL("")
	if got != 30*time.Minute {
		t.Errorf("empty: expected 30min, got %v", got)
	}
}

func TestFrozenStubs_Touch(t *testing.T) {
	f := NewFrozenStubsWithTTL(10 * time.Minute)
	msgs := []any{map[string]any{"role": "user", "content": "test"}}
	f.Store("t1", msgs, 1, msgs[0], 100, 0)

	// Age the entry past TTL
	f.mu.Lock()
	f.lastAccess["t1"] = time.Now().Add(-12 * time.Minute)
	f.mu.Unlock()

	// Touch refreshes lastAccess
	f.Touch("t1")

	// Should NOT be evicted now
	evicted := f.Evict()
	if evicted > 0 {
		t.Error("touched stubs should not be evicted")
	}
}

func TestFrozenStubs_TouchNonExistent(t *testing.T) {
	f := NewFrozenStubsWithTTL(10 * time.Minute)
	f.Touch("unknown-thread") // must not panic
}

func TestFrozenStubs_UpdateTTL(t *testing.T) {
	f := NewFrozenStubsWithTTL(30 * time.Minute)
	f.UpdateTTL(65 * time.Minute)

	// Store stubs, advance time past 30min but within 65min
	msgs := []any{map[string]any{"role": "user", "content": "test"}}
	f.Store("t1", msgs, 1, msgs[0], 100, 0)
	f.mu.Lock()
	f.lastAccess["t1"] = time.Now().Add(-35 * time.Minute) // 35min ago
	f.mu.Unlock()

	// Should NOT be evicted (within 65min TTL)
	evicted := f.Evict()
	if evicted > 0 {
		t.Error("stubs should survive 35min with 65min TTL")
	}
	if f.Get("t1", msgs) == nil {
		t.Error("stubs should still be present")
	}
}

func TestFrozenStubs_PrefixHashVerification(t *testing.T) {
	f := NewFrozenStubs()

	stubbed := []any{map[string]any{"role": "user", "content": "hello"}}
	boundary := map[string]any{"role": "assistant", "content": "boundary"}
	f.Store("t1", stubbed, 1, boundary, 100, 0)

	// Tamper with the stored messages directly (simulate memory corruption)
	f.mu.Lock()
	f.messages["t1"][0].(map[string]any)["content"] = "TAMPERED"
	f.mu.Unlock()

	currentMsgs := []any{boundary, map[string]any{"role": "user", "content": "new"}}
	result := f.Get("t1", currentMsgs)
	if result != nil {
		t.Error("expected nil on prefix hash mismatch (tampered messages)")
	}
}

func TestDeepCopyMessages(t *testing.T) {
	msgs := []any{
		map[string]any{
			"role": "user",
			"content": []any{
				map[string]any{"type": "text", "text": "hello"},
			},
		},
	}

	copied := deepCopyMessages(msgs)

	// Mutate original
	msgs[0].(map[string]any)["role"] = "MUTATED"

	if copied[0].(map[string]any)["role"] != "user" {
		t.Error("deep copy failed: nested structure was shared")
	}
}

func TestSha256hex(t *testing.T) {
	h1 := sha256hex([]byte("hello"))
	h2 := sha256hex([]byte("hello"))
	h3 := sha256hex([]byte("world"))

	if h1 != h2 {
		t.Error("same input should produce same hash")
	}
	if h1 == h3 {
		t.Error("different input should produce different hash")
	}
	if len(h1) != 64 {
		t.Errorf("expected 64 hex chars, got %d", len(h1))
	}
}

// --- SawtoothTrigger Tests ---

func TestSawtoothTrigger_FirstRequest(t *testing.T) {
	st := NewSawtoothTrigger(61*time.Minute, 180000, 80000)
	// No prior data → no trigger
	reason := st.ShouldTrigger("thread1", 50000)
	if reason != TriggerNone {
		t.Errorf("first request should not trigger, got %s", reason)
	}
}

func TestSawtoothTrigger_TokenThreshold(t *testing.T) {
	st := NewSawtoothTrigger(61*time.Minute, 180000, 80000)
	st.UpdateAfterResponse("t1", 185000, 30) // above 180k

	reason := st.ShouldTrigger("t1", 100000)
	if reason != TriggerTokens {
		t.Errorf("expected TriggerTokens, got %s", reason)
	}
}

func TestSawtoothTrigger_BelowThreshold(t *testing.T) {
	st := NewSawtoothTrigger(61*time.Minute, 180000, 80000)
	st.UpdateAfterResponse("t1", 150000, 25) // below 180k

	reason := st.ShouldTrigger("t1", 100000)
	if reason != TriggerNone {
		t.Errorf("expected TriggerNone, got %s", reason)
	}
}

func TestSawtoothTrigger_PauseDetection(t *testing.T) {
	st := NewSawtoothTrigger(61*time.Minute, 180000, 80000)
	st.UpdateAfterResponse("t1", 120000, 20) // above 100k min

	// Backdate to simulate pause > 55 min
	st.mu.Lock()
	st.lastRequestTime["t1"] = time.Now().Add(-62 * time.Minute)
	st.mu.Unlock()

	reason := st.ShouldTrigger("t1", 100000)
	if reason != TriggerPause {
		t.Errorf("expected TriggerPause, got %s", reason)
	}
}

func TestSawtoothTrigger_PauseButTooFewTokens(t *testing.T) {
	st := NewSawtoothTrigger(61*time.Minute, 180000, 80000)
	st.UpdateAfterResponse("t1", 50000, 10) // below 100k min

	// Backdate to simulate pause
	st.mu.Lock()
	st.lastRequestTime["t1"] = time.Now().Add(-6 * time.Minute)
	st.mu.Unlock()

	reason := st.ShouldTrigger("t1", 50000)
	if reason != TriggerNone {
		t.Errorf("expected TriggerNone (too few tokens for pause), got %s", reason)
	}
}

func TestSawtoothTrigger_Emergency(t *testing.T) {
	st := NewSawtoothTrigger(61*time.Minute, 180000, 80000)
	// No prior data, but raw estimate is too high → emergency
	reason := st.ShouldTrigger("t1", 195000)
	if reason != TriggerEmergency {
		t.Errorf("expected TriggerEmergency, got %s", reason)
	}
}

func TestSawtoothTrigger_EmergencyOverridesTokens(t *testing.T) {
	st := NewSawtoothTrigger(61*time.Minute, 180000, 80000)
	st.UpdateAfterResponse("t1", 185000, 30)

	// Both token threshold and emergency would fire — emergency takes precedence
	reason := st.ShouldTrigger("t1", 195000)
	if reason != TriggerEmergency {
		t.Errorf("expected TriggerEmergency (takes precedence), got %s", reason)
	}
}

// Regression: keepalive ping must reset pause timer to prevent
// false pause-triggered stub-cycles while the API cache is warm.
func TestSawtoothTrigger_TouchPreventsParuseTrigger(t *testing.T) {
	st := NewSawtoothTrigger(5*time.Minute, 180000, 80000)
	st.UpdateAfterResponse("t1", 110000, 50)

	// Backdate to simulate long gap (>5min)
	st.mu.Lock()
	st.lastRequestTime["t1"] = time.Now().Add(-6 * time.Minute)
	st.mu.Unlock()

	// Without touch: should trigger pause
	reason := st.ShouldTrigger("t1", 110000)
	if reason != TriggerPause {
		t.Fatalf("precondition: expected TriggerPause without touch, got %s", reason)
	}

	// Backdate again, then touch
	st.mu.Lock()
	st.lastRequestTime["t1"] = time.Now().Add(-6 * time.Minute)
	st.mu.Unlock()
	st.TouchRequestTime("t1")

	// After touch: should NOT trigger pause
	reason = st.ShouldTrigger("t1", 110000)
	if reason != TriggerNone {
		t.Errorf("expected TriggerNone after TouchRequestTime, got %s", reason)
	}
}

func TestSawtoothTrigger_GetLastTokens(t *testing.T) {
	st := NewSawtoothTrigger(61*time.Minute, 180000, 80000)
	if st.GetLastTokens("unknown") != 0 {
		t.Error("unknown thread should return 0")
	}
	st.UpdateAfterResponse("t1", 123456, 15)
	if st.GetLastTokens("t1") != 123456 {
		t.Errorf("expected 123456, got %d", st.GetLastTokens("t1"))
	}
}

func TestSawtoothTrigger_GetLastMessageCount(t *testing.T) {
	st := NewSawtoothTrigger(0, 200000, 80000)

	// No data yet
	if got := st.GetLastMessageCount("t1"); got != 0 {
		t.Fatalf("expected 0, got %d", got)
	}

	// After response with 50 messages, 150k tokens
	st.UpdateAfterResponse("t1", 150000, 50)
	if got := st.GetLastMessageCount("t1"); got != 50 {
		t.Fatalf("expected 50, got %d", got)
	}
	if got := st.GetLastTokens("t1"); got != 150000 {
		t.Fatalf("expected 150000, got %d", got)
	}

	// Different thread is independent
	if got := st.GetLastMessageCount("t2"); got != 0 {
		t.Fatalf("expected 0 for t2, got %d", got)
	}
}

func TestSawtoothTrigger_SetTokenThreshold(t *testing.T) {
	st := NewSawtoothTrigger(61*time.Minute, 200000, 80000)

	// 110k < 200k → no trigger
	reason := st.ShouldTrigger("t1", 110000)
	if reason != TriggerNone {
		t.Fatalf("expected TriggerNone before update, got %s", reason)
	}

	// Lower threshold to 100k (emergency = 110k, so 110k is NOT emergency)
	st.SetTokenThreshold(100000)

	// 110k > 100k → TriggerTokens (not emergency since 110k <= 110k)
	reason = st.ShouldTrigger("t1", 110000)
	if reason != TriggerTokens {
		t.Fatalf("expected TriggerTokens after update, got %s", reason)
	}
}

func TestSawtoothTrigger_SetTokenThreshold_Emergency(t *testing.T) {
	st := NewSawtoothTrigger(61*time.Minute, 200000, 80000)

	// 110k < 210k emergency → no trigger
	reason := st.ShouldTrigger("t1", 110000)
	if reason != TriggerNone {
		t.Fatalf("expected TriggerNone, got %s", reason)
	}

	// Lower threshold to 90k → emergency at 100k
	st.SetTokenThreshold(90000)

	// 110k > 100k emergency → TriggerEmergency
	reason = st.ShouldTrigger("t1", 110000)
	if reason != TriggerEmergency {
		t.Fatalf("expected TriggerEmergency, got %s", reason)
	}
}
