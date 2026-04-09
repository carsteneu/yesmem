package proxy

import (
	"io"
	"log"
	"testing"
)

// mockHandler implements SignalHandler for testing.
type mockHandler struct {
	name     string
	activate bool
	toolDef  map[string]any
	sysInst  string
	calls    []ToolCallResult
}

func (m *mockHandler) Name() string                         { return m.name }
func (m *mockHandler) ToolDefinition() map[string]any       { return m.toolDef }
func (m *mockHandler) SystemInstruction() string            { return m.sysInst }
func (m *mockHandler) ShouldActivate(_ RequestContext) bool { return m.activate }
func (m *mockHandler) HandleResult(_ RequestContext, c ToolCallResult) {
	m.calls = append(m.calls, c)
}

func testLogger() *log.Logger {
	return log.New(io.Discard, "", 0)
}

func TestSignalBusRegisterAndEvaluate(t *testing.T) {
	bus := NewSignalBus(testLogger())

	always := &mockHandler{name: "_signal_always", activate: true}
	never := &mockHandler{name: "_signal_never", activate: false}

	bus.Register(always)
	bus.Register(never)

	active := bus.Evaluate(RequestContext{})
	if len(active) != 1 {
		t.Fatalf("expected 1 active handler, got %d", len(active))
	}
	if active[0].Name() != "_signal_always" {
		t.Errorf("expected _signal_always, got %s", active[0].Name())
	}
}

func TestSignalBusBuildToolDefs(t *testing.T) {
	bus := NewSignalBus(testLogger())

	h := &mockHandler{
		name:     "_signal_test",
		activate: true,
		toolDef: map[string]any{
			"name":         "_signal_test",
			"description":  "test tool",
			"input_schema": map[string]any{"type": "object"},
		},
	}
	bus.Register(h)

	active := bus.Evaluate(RequestContext{})
	defs := bus.BuildToolDefs(active)

	if len(defs) != 1 {
		t.Fatalf("expected 1 tool def, got %d", len(defs))
	}
	def, ok := defs[0].(map[string]any)
	if !ok {
		t.Fatal("tool def is not map[string]any")
	}
	if def["name"] != "_signal_test" {
		t.Errorf("expected name _signal_test, got %v", def["name"])
	}
}

func TestSignalBusRouteToolCall(t *testing.T) {
	bus := NewSignalBus(testLogger())

	h := &mockHandler{name: "_signal_feedback", activate: true}
	bus.Register(h)

	ctx := RequestContext{ThreadID: "t1", Project: "test"}
	call := ToolCallResult{
		ID:    "tu_123",
		Name:  "_signal_feedback",
		Input: map[string]any{"score": float64(5)},
	}

	found := bus.RouteToolCall(ctx, call)
	if !found {
		t.Fatal("expected handler to be found")
	}
	if len(h.calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(h.calls))
	}
	if h.calls[0].Input["score"] != float64(5) {
		t.Errorf("expected score 5, got %v", h.calls[0].Input["score"])
	}
}

func TestSignalBusRouteNonSignalTool(t *testing.T) {
	bus := NewSignalBus(testLogger())
	h := &mockHandler{name: "_signal_test", activate: true}
	bus.Register(h)

	found := bus.RouteToolCall(RequestContext{}, ToolCallResult{Name: "regular_tool"})
	if found {
		t.Error("should not route non-signal tools")
	}
}

func TestIsSignalTool(t *testing.T) {
	tests := []struct {
		name   string
		expect bool
	}{
		{"_signal_learning_used", true},
		{"_signal_knowledge_gap", true},
		{"_signal_contradiction", true},
		{"regular_tool", false},
		{"signal_no_prefix", false},
		{"", false},
		{"_signal_", true},
	}
	for _, tt := range tests {
		if got := IsSignalTool(tt.name); got != tt.expect {
			t.Errorf("IsSignalTool(%q) = %v, want %v", tt.name, got, tt.expect)
		}
	}
}

// containsStr is defined in stubify_test.go
