package proxy

import (
	"log"
	"strings"
	"sync"
)

// RequestContext provides information about the current request for handler activation decisions.
type RequestContext struct {
	ReqIdx       int
	ThreadID     string
	Project      string
	UserQuery    string
	MessageCount int
	HasLearnings bool
}

// ToolCallResult represents a signal tool call extracted from Claude's response.
type ToolCallResult struct {
	ID    string
	Name  string
	Input map[string]any
}

// SignalHandler defines the interface for cognitive signal handlers.
type SignalHandler interface {
	Name() string
	ToolDefinition() map[string]any
	SystemInstruction() string
	ShouldActivate(ctx RequestContext) bool
	HandleResult(ctx RequestContext, call ToolCallResult)
}

// SignalBus manages signal handlers and routes tool calls to them.
type SignalBus struct {
	mu       sync.RWMutex
	handlers []SignalHandler
	logger   *log.Logger
}

// NewSignalBus creates a new signal bus with the given logger.
func NewSignalBus(logger *log.Logger) *SignalBus {
	return &SignalBus{
		logger: logger,
	}
}

// Register adds a handler to the bus.
func (sb *SignalBus) Register(h SignalHandler) {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	sb.handlers = append(sb.handlers, h)
}

// Evaluate returns handlers that should be active for this request context.
func (sb *SignalBus) Evaluate(ctx RequestContext) []SignalHandler {
	sb.mu.RLock()
	defer sb.mu.RUnlock()

	var active []SignalHandler
	for _, h := range sb.handlers {
		if h.ShouldActivate(ctx) {
			active = append(active, h)
		}
	}
	return active
}

// BuildToolDefs returns tool definition objects for injection into req["tools"].
func (sb *SignalBus) BuildToolDefs(active []SignalHandler) []any {
	defs := make([]any, 0, len(active))
	for _, h := range active {
		defs = append(defs, h.ToolDefinition())
	}
	return defs
}

// RouteToolCall dispatches a signal tool call to the matching handler.
// Returns true if a handler was found.
func (sb *SignalBus) RouteToolCall(ctx RequestContext, call ToolCallResult) bool {
	if !IsSignalTool(call.Name) {
		return false
	}

	sb.mu.RLock()
	defer sb.mu.RUnlock()

	for _, h := range sb.handlers {
		if call.Name == h.Name() {
			h.HandleResult(ctx, call)
			return true
		}
	}

	if sb.logger != nil {
		sb.logger.Printf("[signal-bus] no handler for signal tool %q", call.Name)
	}
	return false
}

// IsSignalTool returns true if the tool name has the signal prefix.
func IsSignalTool(name string) bool {
	return strings.HasPrefix(name, "_signal_")
}
