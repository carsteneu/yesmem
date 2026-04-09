package proxy

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestIntegration_SignalBusEndToEnd tests the full cycle:
// register handlers → evaluate → build tools → route calls
func TestIntegration_SignalBusEndToEnd(t *testing.T) {
	logger := log.New(io.Discard, "", 0)
	bus := NewSignalBus(logger)

	var hitCalls, noiseCalls, gapCalls, contradictionCalls, primeCalls int
	mockDaemon := func(method string, params map[string]any) (json.RawMessage, error) {
		switch method {
		case "increment_hits":
			hitCalls++
		case "increment_noise":
			noiseCalls++
		case "track_gap":
			gapCalls++
		case "flag_contradiction":
			contradictionCalls++
		}
		return json.RawMessage(`{"status":"ok"}`), nil
	}

	var cachedThreadID, cachedAnchor string
	primeSetter := func(tid, anchor string) {
		cachedThreadID = tid
		cachedAnchor = anchor
		primeCalls++
	}

	registerSignalHandlers(bus, logger, mockDaemon, primeSetter)

	// Evaluate with learnings context
	ctx := RequestContext{
		ReqIdx:       1,
		ThreadID:     "thread-1",
		Project:      "test-project",
		HasLearnings: true,
	}
	active := bus.Evaluate(ctx)

	// All 3 active handlers (learning_used + contradiction need HasLearnings, gap always; self_prime disabled)
	if len(active) != 3 {
		t.Fatalf("expected 3 active handlers, got %d", len(active))
	}

	// Build tool definitions
	defs := bus.BuildToolDefs(active)
	if len(defs) != 3 {
		t.Fatalf("expected 3 tool defs, got %d", len(defs))
	}

	// Route learning_used call
	bus.RouteToolCall(ctx, ToolCallResult{
		Name:  "_signal_learning_used",
		Input: map[string]any{"used_ids": []any{float64(1), float64(2)}, "noise_ids": []any{float64(3)}},
	})

	// Route knowledge_gap call
	bus.RouteToolCall(ctx, ToolCallResult{
		Name:  "_signal_knowledge_gap",
		Input: map[string]any{"topic": "missing API docs", "severity": "would_help"},
	})

	// Route contradiction call
	bus.RouteToolCall(ctx, ToolCallResult{
		Name:  "_signal_contradiction",
		Input: map[string]any{"description": "learning 5 says X but code shows Y", "learning_ids": []any{float64(5)}},
	})

	// Route self_prime call
	bus.RouteToolCall(ctx, ToolCallResult{
		Name:  "_signal_self_prime",
		Input: map[string]any{"anchor": "Deep in debugging mode, tracking race condition in proxy."},
	})
	if primeCalls != 1 {
		t.Errorf("expected 1 prime call, got %d", primeCalls)
	}
	if cachedThreadID != "thread-1" {
		t.Errorf("expected thread-1, got %s", cachedThreadID)
	}
	if !strings.Contains(cachedAnchor, "debugging mode") {
		t.Errorf("anchor should contain 'debugging mode', got %s", cachedAnchor)
	}

	// Non-signal tool should not be routed
	if bus.RouteToolCall(ctx, ToolCallResult{Name: "Read", Input: map[string]any{}}) {
		t.Error("non-signal tool should not be routed")
	}
}

// TestIntegration_EvaluateWithoutLearnings verifies that learning-dependent handlers
// are inactive when HasLearnings is false.
func TestIntegration_EvaluateWithoutLearnings(t *testing.T) {
	logger := log.New(io.Discard, "", 0)
	bus := NewSignalBus(logger)
	mockDaemon := func(method string, params map[string]any) (json.RawMessage, error) {
		return json.RawMessage(`{}`), nil
	}
	registerSignalHandlers(bus, logger, mockDaemon, func(string, string) {})

	ctx := RequestContext{HasLearnings: false}
	active := bus.Evaluate(ctx)

	// Only knowledge_gap should be active (always-on; self_prime is disabled)
	if len(active) != 1 {
		t.Fatalf("expected 1 active handler without learnings, got %d", len(active))
	}
	names := make(map[string]bool)
	for _, h := range active {
		names[h.Name()] = true
	}
	if !names["_signal_knowledge_gap"] {
		t.Error("knowledge_gap should be active without learnings")
	}
}

// TestIntegration_SelfPrimeAnchorLimit verifies the 500 char limit on anchors.
func TestIntegration_SelfPrimeAnchorLimit(t *testing.T) {
	logger := log.New(io.Discard, "", 0)
	var cached string
	h := &selfPrimeHandler{
		logger:    logger,
		setCached: func(_, anchor string) { cached = anchor },
	}

	longAnchor := strings.Repeat("x", 600)
	h.HandleResult(RequestContext{ThreadID: "t1"}, ToolCallResult{
		Name:  "_signal_self_prime",
		Input: map[string]any{"anchor": longAnchor},
	})

	if len(cached) != 500 {
		t.Errorf("expected anchor truncated to 500, got %d", len(cached))
	}
}

// TestIntegration_ReflectionCallEndToEnd tests the full reflection cycle:
// build request → mock API → parse response → route through signal bus
func TestIntegration_ReflectionCallEndToEnd(t *testing.T) {
	logger := log.New(io.Discard, "", 0)
	bus := NewSignalBus(logger)

	var dispatched []string
	mockDaemon := func(method string, params map[string]any) (json.RawMessage, error) {
		return json.RawMessage(`{"status":"ok"}`), nil
	}
	primeSetter := func(tid, anchor string) {
		dispatched = append(dispatched, "_signal_self_prime")
	}
	registerSignalHandlers(bus, logger, mockDaemon, primeSetter)

	// Override learning_used handler to track dispatches
	// (We test via RouteToolCall which is the actual dispatch path)

	// Mock Anthropic API
	apiResp := `{
		"content": [
			{"type": "tool_use", "id": "tu_1", "name": "_signal_self_prime", "input": {"anchor": "debugging proxy"}},
			{"type": "tool_use", "id": "tu_2", "name": "_signal_learning_used", "input": {"used_ids": [1], "noise_ids": [2]}},
			{"type": "tool_use", "id": "tu_3", "name": "_signal_knowledge_gap", "input": {"topic": "missing docs", "severity": "would_help"}}
		],
		"stop_reason": "tool_use",
		"usage": {"input_tokens": 500, "output_tokens": 100}
	}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify the request has tool_choice: any
		body := make([]byte, r.ContentLength)
		r.Body.Read(body)
		var req map[string]any
		json.Unmarshal(body, &req)
		tc, ok := req["tool_choice"].(map[string]any)
		if !ok || tc["type"] != "any" {
			t.Error("expected tool_choice type=any")
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(apiResp))
	}))
	defer srv.Close()

	// Parse response and route through bus
	calls, _, err := doReflectionCall(srv.URL, "test-key", []byte(`{"model":"test","max_tokens":1024,"messages":[{"role":"user","content":"test"}],"tools":[],"tool_choice":{"type":"any"}}`))
	if err != nil {
		t.Fatal(err)
	}

	if len(calls) != 3 {
		t.Fatalf("expected 3 tool calls, got %d", len(calls))
	}

	// Route through bus
	ctx := RequestContext{ReqIdx: 1, ThreadID: "t-1", Project: "test", HasLearnings: true}
	routedCount := 0
	for _, call := range calls {
		if bus.RouteToolCall(ctx, call) {
			routedCount++
		}
	}

	if routedCount != 3 {
		t.Errorf("expected 3 routed calls, got %d", routedCount)
	}
}
