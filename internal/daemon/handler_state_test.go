package daemon

import (
	"encoding/json"
	"sync"
	"testing"
)

// --- Proxy State ---

func TestHandleGetProxyState_RequiresKey(t *testing.T) {
	h, _ := mustHandler(t)
	resp := h.handleGetProxyState(map[string]any{})
	if resp.Error == "" {
		t.Fatal("expected error for missing key")
	}
}

func TestHandleSetAndGetProxyState(t *testing.T) {
	h, _ := mustHandler(t)

	resp := h.handleSetProxyState(map[string]any{"key": "test_key", "value": "hello"})
	if resp.Error != "" {
		t.Fatalf("set error: %s", resp.Error)
	}

	resp = h.handleGetProxyState(map[string]any{"key": "test_key"})
	if resp.Error != "" {
		t.Fatalf("get error: %s", resp.Error)
	}
	m := resultMap(t, resp)
	if m["value"] != "hello" {
		t.Errorf("expected 'hello', got %q", m["value"])
	}
}

func TestHandleSetProxyState_RequiresKey(t *testing.T) {
	h, _ := mustHandler(t)
	resp := h.handleSetProxyState(map[string]any{"value": "x"})
	if resp.Error == "" {
		t.Fatal("expected error for missing key")
	}
}

// --- Config Overrides ---

func TestHandleSetConfig_GlobalOverride(t *testing.T) {
	h, _ := mustHandler(t)

	resp := h.handleSetConfig(map[string]any{"key": "token_threshold", "value": "300000"})
	if resp.Error != "" {
		t.Fatalf("set config error: %s", resp.Error)
	}
	m := resultMap(t, resp)
	if m["scope"] != "global" {
		t.Errorf("expected global scope, got %q", m["scope"])
	}

	resp = h.handleGetConfig(map[string]any{"key": "token_threshold"})
	if resp.Error != "" {
		t.Fatalf("get config error: %s", resp.Error)
	}
	m = resultMap(t, resp)
	if m["value"] != "300000" {
		t.Errorf("expected '300000', got %q", m["value"])
	}
	if m["scope"] != "global" {
		t.Errorf("expected global scope, got %q", m["scope"])
	}
}

func TestHandleSetConfig_SessionOverride(t *testing.T) {
	h, _ := mustHandler(t)

	// Set global first
	h.handleSetConfig(map[string]any{"key": "token_threshold", "value": "200000"})
	// Set session-specific
	h.handleSetConfig(map[string]any{"key": "token_threshold", "value": "400000", "session_id": "sess-1"})

	// Session-specific should win
	resp := h.handleGetConfig(map[string]any{"key": "token_threshold", "session_id": "sess-1"})
	m := resultMap(t, resp)
	if m["value"] != "400000" {
		t.Errorf("session override should win, got %q", m["value"])
	}
	if m["scope"] != "session:sess-1" {
		t.Errorf("expected session scope, got %q", m["scope"])
	}

	// Without session_id, get global
	resp = h.handleGetConfig(map[string]any{"key": "token_threshold"})
	m = resultMap(t, resp)
	if m["value"] != "200000" {
		t.Errorf("expected global value '200000', got %q", m["value"])
	}
}

func TestHandleSetConfig_RejectsUnknownKey(t *testing.T) {
	h, _ := mustHandler(t)
	resp := h.handleSetConfig(map[string]any{"key": "unknown_key", "value": "x"})
	if resp.Error == "" {
		t.Fatal("expected error for unknown config key")
	}
}

func TestHandleSetConfig_RequiresKeyAndValue(t *testing.T) {
	h, _ := mustHandler(t)
	resp := h.handleSetConfig(map[string]any{"key": "token_threshold"})
	if resp.Error == "" {
		t.Fatal("expected error for missing value")
	}
	resp = h.handleSetConfig(map[string]any{"value": "123"})
	if resp.Error == "" {
		t.Fatal("expected error for missing key")
	}
}

// --- Compacted Stubs ---

func TestHandleStoreAndGetCompactedBlock(t *testing.T) {
	h, _ := mustHandler(t)

	resp := h.handleStoreCompactedBlock(map[string]any{
		"thread_id": "t-1",
		"start_idx": float64(0),
		"end_idx":   float64(50),
		"content":   "archived summary of messages 0-50",
	})
	if resp.Error != "" {
		t.Fatalf("store error: %s", resp.Error)
	}

	resp = h.handleGetCompactedStubs(map[string]any{"thread_id": "t-1"})
	if resp.Error != "" {
		t.Fatalf("get error: %s", resp.Error)
	}
	blocks := resultSlice(t, resp)
	if len(blocks) == 0 {
		t.Fatal("expected at least 1 block")
	}
}

func TestHandleGetCompactedStubs_RequiresThreadID(t *testing.T) {
	h, _ := mustHandler(t)
	resp := h.handleGetCompactedStubs(map[string]any{})
	if resp.Error == "" {
		t.Fatal("expected error for missing thread_id")
	}
}

func TestHandleGetCompactedStubs_FallbackToSessionID(t *testing.T) {
	h, _ := mustHandler(t)
	h.handleStoreCompactedBlock(map[string]any{
		"thread_id": "s-fallback",
		"start_idx": float64(0),
		"end_idx":   float64(10),
		"content":   "test block",
	})
	resp := h.handleGetCompactedStubs(map[string]any{"session_id": "s-fallback"})
	if resp.Error != "" {
		t.Fatalf("expected fallback to session_id, got error: %s", resp.Error)
	}
}

func TestHandleGetCompactedStubs_WithRange(t *testing.T) {
	h, _ := mustHandler(t)
	h.handleStoreCompactedBlock(map[string]any{"thread_id": "t-range", "start_idx": float64(0), "end_idx": float64(50), "content": "block 1"})
	h.handleStoreCompactedBlock(map[string]any{"thread_id": "t-range", "start_idx": float64(51), "end_idx": float64(100), "content": "block 2"})

	// Only block in range 0-50
	resp := h.handleGetCompactedStubs(map[string]any{"thread_id": "t-range", "from_idx": float64(0), "to_idx": float64(50)})
	if resp.Error != "" {
		t.Fatalf("range query error: %s", resp.Error)
	}
}

func TestHandleStoreCompactedBlock_RequiresContent(t *testing.T) {
	h, _ := mustHandler(t)
	resp := h.handleStoreCompactedBlock(map[string]any{"thread_id": "t-1"})
	if resp.Error == "" {
		t.Fatal("expected error for missing content")
	}
}

// --- Expand Context ---

func TestHandleExpandContext_RequiresQueryOrRange(t *testing.T) {
	h, _ := mustHandler(t)
	resp := h.handleExpandContext(map[string]any{"thread_id": "t-1"})
	if resp.Error == "" {
		t.Fatal("expected error when neither query nor message_range given")
	}
}

func TestHandleExpandContext_MessageRange(t *testing.T) {
	h, _ := mustHandler(t)
	h.handleStoreCompactedBlock(map[string]any{"thread_id": "t-expand", "start_idx": float64(10), "end_idx": float64(20), "content": "context block"})

	resp := h.handleExpandContext(map[string]any{"thread_id": "t-expand", "message_range": "10-20"})
	if resp.Error != "" {
		t.Fatalf("expand error: %s", resp.Error)
	}
}

func TestHandleExpandContext_InvalidRange(t *testing.T) {
	h, _ := mustHandler(t)
	resp := h.handleExpandContext(map[string]any{"thread_id": "t-1", "message_range": "invalid"})
	if resp.Error == "" {
		t.Fatal("expected error for invalid range format")
	}
}

// --- Pins ---

func TestHandlePin_CreateAndGet(t *testing.T) {
	h, _ := mustHandler(t)

	resp := h.handlePin(map[string]any{"content": "Always use TDD", "scope": "permanent", "project": "yesmem"})
	if resp.Error != "" {
		t.Fatalf("pin error: %s", resp.Error)
	}
	m := resultMap(t, resp)
	if m["scope"] != "permanent" {
		t.Errorf("expected permanent scope, got %q", m["scope"])
	}

	resp = h.handleGetPins(map[string]any{"project": "yesmem"})
	if resp.Error != "" {
		t.Fatalf("get pins error: %s", resp.Error)
	}
}

func TestHandlePin_DefaultsToSession(t *testing.T) {
	h, _ := mustHandler(t)
	resp := h.handlePin(map[string]any{"content": "session pin"})
	if resp.Error != "" {
		t.Fatalf("pin error: %s", resp.Error)
	}
	m := resultMap(t, resp)
	if m["scope"] != "session" {
		t.Errorf("expected default session scope, got %q", m["scope"])
	}
}

func TestHandlePin_RequiresContent(t *testing.T) {
	h, _ := mustHandler(t)
	resp := h.handlePin(map[string]any{})
	if resp.Error == "" {
		t.Fatal("expected error for missing content")
	}
}

func TestHandlePin_RejectsInvalidScope(t *testing.T) {
	h, _ := mustHandler(t)
	resp := h.handlePin(map[string]any{"content": "x", "scope": "invalid"})
	if resp.Error == "" {
		t.Fatal("expected error for invalid scope")
	}
}

func TestHandleUnpin(t *testing.T) {
	h, _ := mustHandler(t)

	resp := h.handlePin(map[string]any{"content": "to unpin", "scope": "session"})
	m := resultMap(t, resp)
	pinID := m["id"].(float64)

	resp = h.handleUnpin(map[string]any{"id": pinID, "scope": "session"})
	if resp.Error != "" {
		t.Fatalf("unpin error: %s", resp.Error)
	}
}

func TestHandleUnpin_RequiresID(t *testing.T) {
	h, _ := mustHandler(t)
	resp := h.handleUnpin(map[string]any{"scope": "session"})
	if resp.Error == "" {
		t.Fatal("expected error for missing id")
	}
}

// --- Gaps ---

func TestHandleTrackAndGetGaps(t *testing.T) {
	h, _ := mustHandler(t)

	resp := h.handleTrackGap(map[string]any{"topic": "proxy caching", "project": "yesmem"})
	if resp.Error != "" {
		t.Fatalf("track gap error: %s", resp.Error)
	}

	resp = h.handleGetActiveGaps(map[string]any{"project": "yesmem"})
	if resp.Error != "" {
		t.Fatalf("get gaps error: %s", resp.Error)
	}
}

func TestHandleTrackGap_RequiresTopic(t *testing.T) {
	h, _ := mustHandler(t)
	resp := h.handleTrackGap(map[string]any{})
	if resp.Error == "" {
		t.Fatal("expected error for missing topic")
	}
}

func TestHandleResolveGap(t *testing.T) {
	h, _ := mustHandler(t)
	h.handleTrackGap(map[string]any{"topic": "to-resolve", "project": "test"})

	resp := h.handleResolveGap(map[string]any{"topic": "to-resolve", "project": "test"})
	if resp.Error != "" {
		t.Fatalf("resolve gap error: %s", resp.Error)
	}
}

func TestHandleResolveGap_RequiresTopic(t *testing.T) {
	h, _ := mustHandler(t)
	resp := h.handleResolveGap(map[string]any{})
	if resp.Error == "" {
		t.Fatal("expected error for missing topic")
	}
}

// --- PopRecentRemember ---

func TestHandlePopRecentRemember_Empty(t *testing.T) {
	h, _ := mustHandler(t)
	resp := h.handlePopRecentRemember()
	if resp.Error != "" {
		t.Fatalf("error: %s", resp.Error)
	}
}

func TestHandlePopRecentRemember_ReturnsAndClears(t *testing.T) {
	h, _ := mustHandler(t)

	h.recentRememberMu.Lock()
	h.recentRemembered = []recentLearning{{ID: 1, Text: "test item"}}
	h.recentRememberMu.Unlock()

	resp := h.handlePopRecentRemember()
	if resp.Error != "" {
		t.Fatalf("error: %s", resp.Error)
	}

	// Second call should be empty
	resp = h.handlePopRecentRemember()
	m := resultMap(t, resp)
	items := m["items"].([]any)
	if len(items) != 0 {
		t.Errorf("expected empty after pop, got %d items", len(items))
	}
}

// --- Index Status ---

func TestHandleIndexStatus_NoProgress(t *testing.T) {
	h, _ := mustHandler(t)
	resp := h.handleIndexStatus()
	if resp.Error != "" {
		t.Fatalf("error: %s", resp.Error)
	}
	m := resultMap(t, resp)
	if m["running"] != false {
		t.Error("expected running=false when no IndexProgress set")
	}
}

func TestHandleIndexStatus_WithProgress(t *testing.T) {
	h, _ := mustHandler(t)
	h.IndexProgress = func() (total, done, skipped int, running bool) {
		return 100, 42, 3, true
	}
	resp := h.handleIndexStatus()
	m := resultMap(t, resp)
	if m["total"] != float64(100) {
		t.Errorf("expected total=100, got %v", m["total"])
	}
	if m["done"] != float64(42) {
		t.Errorf("expected done=42, got %v", m["done"])
	}
	if m["running"] != true {
		t.Error("expected running=true")
	}
}

// --- Coverage & Project Profile ---

func TestHandleGetCoverage(t *testing.T) {
	h, _ := mustHandler(t)
	resp := h.handleGetCoverage(map[string]any{"project": "yesmem"})
	if resp.Error != "" {
		t.Fatalf("error: %s", resp.Error)
	}
}

func TestHandleGetProjectProfile_NotFound(t *testing.T) {
	h, _ := mustHandler(t)
	resp := h.handleGetProjectProfile(map[string]any{"project": "nonexistent"})
	if resp.Error != "" {
		t.Fatalf("should not error on missing profile: %s", resp.Error)
	}
}

func TestHandleGetSelfFeedback(t *testing.T) {
	h, _ := mustHandler(t)
	resp := h.handleGetSelfFeedback(map[string]any{"days": float64(7)})
	if resp.Error != "" {
		t.Fatalf("error: %s", resp.Error)
	}
}

// --- Related To File ---

func TestHandleRelatedToFile(t *testing.T) {
	h, _ := mustHandler(t)
	resp := h.handleRelatedToFile(map[string]any{"path": "internal/proxy/proxy.go"})
	if resp.Error != "" {
		t.Fatalf("error: %s", resp.Error)
	}
}

// --- Track Session End ---

func TestHandleTrackSessionEnd_Success(t *testing.T) {
	h, s := mustHandler(t)

	resp := h.handleTrackSessionEnd(map[string]any{
		"project":    "/home/testuser/projects/myapp",
		"session_id": "test-session-abc",
		"reason":     "clear",
	})
	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}

	// Verify it was tracked in DB
	sid, err := s.GetLastEndedSession("/home/testuser/projects/myapp")
	if err != nil {
		t.Fatalf("GetLastEndedSession: %v", err)
	}
	if sid != "test-session-abc" {
		t.Errorf("expected session_id='test-session-abc', got %q", sid)
	}
}

func TestHandleTrackSessionEnd_ClearsPinsOnClear(t *testing.T) {
	h, s := mustHandler(t)

	// Create a session-scoped pin
	s.PinLearning("session", "myapp", "test pin", "test")

	resp := h.handleTrackSessionEnd(map[string]any{
		"project":    "/home/testuser/projects/myapp",
		"session_id": "sess-1",
		"reason":     "clear",
	})
	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}

	// Session pins should be cleared
	pins, _ := s.GetPinnedLearnings("session", "myapp")
	if len(pins) != 0 {
		t.Errorf("expected 0 session pins after clear, got %d", len(pins))
	}
}

func TestHandleTrackSessionEnd_CompactKeepsPins(t *testing.T) {
	h, s := mustHandler(t)

	s.PinLearning("session", "myapp", "test pin", "test")

	resp := h.handleTrackSessionEnd(map[string]any{
		"project":    "/home/testuser/projects/myapp",
		"session_id": "sess-2",
		"reason":     "compact",
	})
	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}

	// Session pins should survive compact
	pins, _ := s.GetPinnedLearnings("session", "myapp")
	if len(pins) != 1 {
		t.Errorf("expected 1 session pin after compact, got %d", len(pins))
	}
}

func TestHandleTrackSessionEnd_RequiresFields(t *testing.T) {
	h, _ := mustHandler(t)

	resp := h.handleTrackSessionEnd(map[string]any{})
	if resp.Error == "" {
		t.Fatal("expected error for missing fields")
	}

	resp = h.handleTrackSessionEnd(map[string]any{"project": "/tmp", "session_id": "x"})
	if resp.Error == "" {
		t.Fatal("expected error for missing reason")
	}
}

func TestHandleTrackSessionEnd_ViaDispatch(t *testing.T) {
	h, _ := mustHandler(t)

	resp := h.Handle(Request{
		Method: "track_session_end",
		Params: map[string]any{
			"project":    "/home/testuser/projects/myapp",
			"session_id": "dispatch-test",
			"reason":     "clear",
		},
	})
	if resp.Error != "" {
		t.Fatalf("dispatch error: %s", resp.Error)
	}
}

// --- Test helpers ---

func resultMap(t *testing.T, resp Response) map[string]any {
	t.Helper()
	b, err := json.Marshal(resp.Result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal result: %v (%s)", err, string(b))
	}
	return m
}

func resultSlice(t *testing.T, resp Response) []any {
	t.Helper()
	b, err := json.Marshal(resp.Result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	var s []any
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatalf("unmarshal result: %v (%s)", err, string(b))
	}
	return s
}

// Ensure recentLearning is accessible for tests.
var _ = recentLearning{}
var _ = sync.Mutex{}
