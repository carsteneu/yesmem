package daemon

import (
	"encoding/json"
	"testing"
	"time"
)

func TestIdleTick_Increments(t *testing.T) {
	h := &Handler{}
	h.initIdleState()

	resp := h.handleIdleTick(map[string]any{"session_id": "s1"})
	var r1 idleTickResult
	if err := json.Unmarshal(resp.Result, &r1); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if r1.Count != 1 {
		t.Errorf("first tick: expected count=1, got %d", r1.Count)
	}
	if r1.Reminder != "" {
		t.Errorf("first tick: expected empty reminder, got %q", r1.Reminder)
	}

	// Tick up to 29 (total 30 with the next call)
	for i := 0; i < 28; i++ {
		h.handleIdleTick(map[string]any{"session_id": "s1"})
	}
	resp = h.handleIdleTick(map[string]any{"session_id": "s1"})
	var r30 idleTickResult
	if err := json.Unmarshal(resp.Result, &r30); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if r30.Count != 30 {
		t.Errorf("30th tick: expected count=30, got %d", r30.Count)
	}
	if r30.Reminder == "" {
		t.Error("30th tick: expected non-empty reminder")
	}
}

func TestIdleTick_ResetsOnMCPCall(t *testing.T) {
	h := &Handler{}
	h.initIdleState()

	for i := 0; i < 10; i++ {
		h.handleIdleTick(map[string]any{"session_id": "s1"})
	}

	// Simulate global MCP call
	h.idleMu.Lock()
	h.lastMCPCallTime = time.Now()
	h.idleMu.Unlock()

	resp := h.handleIdleTick(map[string]any{"session_id": "s1"})
	var r idleTickResult
	if err := json.Unmarshal(resp.Result, &r); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if r.Count != 1 {
		t.Errorf("after MCP reset: expected count=1, got %d", r.Count)
	}
}

func TestIdleTick_PerSession(t *testing.T) {
	h := &Handler{}
	h.initIdleState()

	for i := 0; i < 5; i++ {
		h.handleIdleTick(map[string]any{"session_id": "s1"})
	}

	resp := h.handleIdleTick(map[string]any{"session_id": "s2"})
	var r idleTickResult
	if err := json.Unmarshal(resp.Result, &r); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if r.Count != 1 {
		t.Errorf("session 2 first tick: expected count=1, got %d", r.Count)
	}
}

func TestIdleTick_Threshold50(t *testing.T) {
	h := &Handler{}
	h.initIdleState()

	for i := 0; i < 49; i++ {
		h.handleIdleTick(map[string]any{"session_id": "s1"})
	}
	resp := h.handleIdleTick(map[string]any{"session_id": "s1"})
	var r idleTickResult
	if err := json.Unmarshal(resp.Result, &r); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if r.Count != 50 {
		t.Errorf("50th tick: expected count=50, got %d", r.Count)
	}
	if r.Reminder == "" {
		t.Error("50th tick: expected non-empty reminder")
	}
	// Should contain the urgent marker
	if len(r.Reminder) < 5 {
		t.Errorf("50th tick: reminder too short: %q", r.Reminder)
	}
}

func TestIdleTick_NoSessionID(t *testing.T) {
	h := &Handler{}
	h.initIdleState()

	resp := h.handleIdleTick(map[string]any{})
	var r idleTickResult
	if err := json.Unmarshal(resp.Result, &r); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if r.Count != 1 {
		t.Errorf("no session_id: expected count=1, got %d", r.Count)
	}
}
