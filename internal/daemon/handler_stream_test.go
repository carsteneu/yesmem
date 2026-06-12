package daemon

import (
	"testing"
	"time"

	"github.com/carsteneu/yesmem/internal/storage"
)

// resetStreamState clears the global stream state maps. Use in tests that
// depend on clean in-memory state.
func resetStreamState() {
	streamStatesMu.Lock()
	streamStates = make(map[string]*StreamState)
	streamStatesMu.Unlock()

	sessionToThreadMu.Lock()
	sessionToThread = make(map[string]string)
	sessionToThreadMu.Unlock()

	parentSubagentCountsMu.Lock()
	parentSubagentCounts = make(map[string]int)
	parentSubagentCountsMu.Unlock()
}

// --- Track Stream State ---

func TestHandleTrackStreamState_RequiresThreadID(t *testing.T) {
	h, _ := mustHandler(t)
	resp := h.handleTrackStreamState(map[string]any{})
	if resp.Error == "" {
		t.Fatal("expected error for missing thread_id")
	}
}

func TestHandleTrackStreamState_StartStop(t *testing.T) {
	resetStreamState()
	h, _ := mustHandler(t)
	threadID := "test-thread-ss1"

	// Start stream
	resp := h.handleTrackStreamState(map[string]any{
		"thread_id":     threadID,
		"stream_active": true,
	})
	if resp.Error != "" {
		t.Fatalf("unexpected error on start: %s", resp.Error)
	}

	// Verify stream state is active
	streamStatesMu.RLock()
	state, ok := streamStates[threadID]
	streamStatesMu.RUnlock()
	if !ok {
		t.Fatal("stream state not found after start")
	}
	state.mu.RLock()
	if !state.Active {
		t.Error("stream should be active after start")
	}
	if state.StartedAt.IsZero() {
		t.Error("StartedAt should be set")
	}
	state.mu.RUnlock()

	// Stop stream
	resp = h.handleTrackStreamState(map[string]any{
		"thread_id":     threadID,
		"stream_active": false,
		"bytes_so_far":  12345.0,
	})
	if resp.Error != "" {
		t.Fatalf("unexpected error on stop: %s", resp.Error)
	}

	state.mu.RLock()
	if state.Active {
		t.Error("stream should be inactive after stop")
	}
	if state.BytesSoFar != 12345 {
		t.Errorf("bytes = %d, want 12345", state.BytesSoFar)
	}
	state.mu.RUnlock()
}

func TestHandleTrackStreamState_BytesAccumulate(t *testing.T) {
	resetStreamState()
	h, _ := mustHandler(t)
	threadID := "test-thread-bytes"

	// Start with bytes
	h.handleTrackStreamState(map[string]any{
		"thread_id":     threadID,
		"stream_active": true,
		"bytes_so_far":  5000.0,
	})
	// Update bytes mid-stream
	h.handleTrackStreamState(map[string]any{
		"thread_id":     threadID,
		"stream_active": true,
		"bytes_so_far":  15000.0,
	})

	streamStatesMu.RLock()
	state := streamStates[threadID]
	streamStatesMu.RUnlock()

	state.mu.RLock()
	if state.BytesSoFar != 15000 {
		t.Errorf("bytes = %d, want 15000", state.BytesSoFar)
	}
	state.mu.RUnlock()
}

func TestHandleTrackStreamState_SubagentFlag(t *testing.T) {
	resetStreamState()
	h, _ := mustHandler(t)
	threadID := "test-thread-sub"

	h.handleTrackStreamState(map[string]any{
		"thread_id":     threadID,
		"stream_active": true,
		"is_subagent":   true,
	})

	streamStatesMu.RLock()
	state := streamStates[threadID]
	streamStatesMu.RUnlock()

	state.mu.RLock()
	if !state.IsSubagent {
		t.Error("IsSubagent should be true")
	}
	state.mu.RUnlock()
}

// --- Get Stream Fields ---

func TestGetStreamFields_NoMapping(t *testing.T) {
	h, _ := mustHandler(t)
	fields := h.getStreamFields("nonexistent-session")
	if fields["stream_active"] != false {
		t.Error("should default to false")
	}
	if fields["stream_bytes"].(int64) != 0 {
		t.Error("should default to 0")
	}
}

func TestGetStreamFields_WithMapping(t *testing.T) {
	resetStreamState()
	h, _ := mustHandler(t)
	sessionID := "agent-session-1"
	threadID := "test-thread-mapped"

	// Create mapping
	RegisterSessionThread(sessionID, threadID)

	// Create stream state
	h.handleTrackStreamState(map[string]any{
		"thread_id":     threadID,
		"stream_active": true,
		"bytes_so_far":  5000.0,
	})

	// Give time for goroutines
	time.Sleep(10 * time.Millisecond)

	fields := h.getStreamFields(sessionID)
	if fields["stream_active"] != true {
		t.Error("should be active")
	}
	if fields["stream_bytes"].(int64) != 5000 {
		t.Errorf("bytes = %d, want 5000", fields["stream_bytes"])
	}
	if fields["stream_started"].(string) == "" {
		t.Error("stream_started should be non-empty")
	}
}

// --- Enrich Agent ---

func TestEnrichAgentWithStreamFields(t *testing.T) {
	resetStreamState()
	h, _ := mustHandler(t)
	sessionID := "agent-session-enrich"
	threadID := "test-thread-enrich"

	RegisterSessionThread(sessionID, threadID)
	h.handleTrackStreamState(map[string]any{
		"thread_id":     threadID,
		"stream_active": true,
		"bytes_so_far":  9999.0,
	})

	time.Sleep(10 * time.Millisecond)

	agent := map[string]any{
		"id":         "agent-1",
		"session_id": sessionID,
		"status":     "running",
	}
	result := h.enrichAgentWithStreamFields(agent)

	if result["stream_active"] != true {
		t.Error("stream_active should be true")
	}
	if result["stream_bytes"].(int64) != 9999 {
		t.Errorf("bytes = %d, want 9999", result["stream_bytes"])
	}
	if _, ok := result["subagent_streams"]; !ok {
		t.Error("subagent_streams should be present")
	}
	// Original fields preserved
	if result["id"] != "agent-1" {
		t.Error("original id should be preserved")
	}
}

// --- Subagent Counting ---

func TestSubagentStreamCounting(t *testing.T) {
	resetStreamState()
	h, _ := mustHandler(t)
	parentThread := "parent-thread"
	subThread := "sub-thread"

	// Start parent stream
	h.handleTrackStreamState(map[string]any{
		"thread_id":     parentThread,
		"stream_active": true,
		"is_subagent":   false,
	})

	// Start subagent stream
	h.handleTrackStreamState(map[string]any{
		"thread_id":     subThread,
		"stream_active": true,
		"is_subagent":   true,
	})

	time.Sleep(10 * time.Millisecond)

	// Verify subagent_streams count
	streamStatesMu.RLock()
	subCount := 0
	for _, s := range streamStates {
		s.mu.RLock()
		if s.Active && s.IsSubagent {
			subCount++
		}
		s.mu.RUnlock()
	}
	streamStatesMu.RUnlock()

	if subCount != 1 {
		t.Errorf("subagent count = %d, want 1", subCount)
	}

	// Stop subagent
	h.handleTrackStreamState(map[string]any{
		"thread_id":     subThread,
		"stream_active": false,
		"is_subagent":   true,
	})

	subCount = 0
	streamStatesMu.RLock()
	for _, s := range streamStates {
		s.mu.RLock()
		if s.Active && s.IsSubagent {
			subCount++
		}
		s.mu.RUnlock()
	}
	streamStatesMu.RUnlock()

	if subCount != 0 {
		t.Errorf("subagent count should be 0 after stop, got %d", subCount)
	}
}

// --- Cleanup ---

func TestCleanupStaleStreams(t *testing.T) {
	resetStreamState()
	h, _ := mustHandler(t)
	activeThread := "active-thread"
	staleThread := "stale-thread"

	// Start both streams
	h.handleTrackStreamState(map[string]any{
		"thread_id":     activeThread,
		"stream_active": true,
	})
	h.handleTrackStreamState(map[string]any{
		"thread_id":     staleThread,
		"stream_active": true,
	})

	// Stop both
	h.handleTrackStreamState(map[string]any{
		"thread_id":     activeThread,
		"stream_active": false,
	})
	h.handleTrackStreamState(map[string]any{
		"thread_id":     staleThread,
		"stream_active": false,
	})

	// Manipulate stale thread's timestamp to be in the past
	streamStatesMu.Lock()
	if s, ok := streamStates[staleThread]; ok {
		s.mu.Lock()
		s.StartedAt = time.Now().Add(-2 * time.Hour)
		s.mu.Unlock()
	}
	streamStatesMu.Unlock()

	// Cleanup with 1 hour timeout
	h.cleanupStaleStreams(1 * time.Hour)

	streamStatesMu.RLock()
	_, activeExists := streamStates[activeThread]
	_, staleExists := streamStates[staleThread]
	streamStatesMu.RUnlock()

	if !activeExists {
		t.Error("active thread should still exist (within timeout)")
	}
	if staleExists {
		t.Error("stale thread should have been cleaned up")
	}
}

// --- Per-Parent Subagent Counting (DB-backed) ---

func TestParentSubagentStreamCounting(t *testing.T) {
	resetStreamState()
	h, s := mustHandler(t)
	parentSession := "parent-session-1"
	subThread := "subagent-thread-1"

	// Create parent agent in DB
	s.AgentCreate(storage.Agent{
		ID:        "agent-parent",
		Project:   "test",
		Section:   "main",
		SessionID: parentSession,
		Status:    "running",
	})

	// Create subagent in DB with CallerSession pointing to parent
	s.AgentCreate(storage.Agent{
		ID:            "agent-sub",
		Project:       "test",
		Section:       "sub",
		SessionID:     subThread,
		Status:        "running",
		CallerSession: parentSession,
	})

	// Map parent session to a threadID so getStreamFields resolves
	RegisterSessionThread(parentSession, "parent-thread")

	// Start subagent stream
	resp := h.handleTrackStreamState(map[string]any{
		"thread_id":     subThread,
		"stream_active": true,
		"is_subagent":   true,
	})
	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}

	// Verify parent sees subagent_streams = 1
	fields := h.getStreamFields(parentSession)
	count, _ := fields["subagent_streams"].(int)
	if count != 1 {
		t.Errorf("subagent_streams = %d, want 1 after start", count)
	}

	// Stop subagent stream
	resp = h.handleTrackStreamState(map[string]any{
		"thread_id":     subThread,
		"stream_active": false,
		"is_subagent":   true,
	})
	if resp.Error != "" {
		t.Fatalf("unexpected error on stop: %s", resp.Error)
	}

	// Verify parent sees subagent_streams = 0
	fields = h.getStreamFields(parentSession)
	count, _ = fields["subagent_streams"].(int)
	if count != 0 {
		t.Errorf("subagent_streams = %d, want 0 after stop", count)
	}
}
