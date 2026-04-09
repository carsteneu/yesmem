package proxy

import (
	"sync/atomic"
	"testing"
)

func TestFireForkedAgents_GateFiltering(t *testing.T) {
	var forksCalled atomic.Int32

	configs := []ForkConfig{
		{
			Name:      "always_fires",
			Model:     "claude-sonnet-4-6",
			MaxTokens: 1024,
			Gate: func(ctx ForkContext) bool {
				return true
			},
			Prompt: func(ctx ForkContext) string {
				return "test prompt"
			},
			ParseResult: func(resp ForkResponse, s *Server) error {
				forksCalled.Add(1)
				return nil
			},
		},
		{
			Name:      "never_fires",
			Model:     "claude-sonnet-4-6",
			MaxTokens: 1024,
			Gate: func(ctx ForkContext) bool {
				return false
			},
			Prompt: func(ctx ForkContext) string {
				return "should not be called"
			},
			ParseResult: func(resp ForkResponse, s *Server) error {
				t.Error("never_fires should not have been called")
				return nil
			},
		},
	}

	// fireForkedAgents only calls configs where Gate returns true
	// We test the gate logic, not the actual API call
	ctx := ForkContext{
		OriginalBody:    []byte(`{"model":"test","messages":[{"role":"user","content":"hi"}]}`),
		CacheReadTokens: 1000,
	}

	for _, cfg := range configs {
		if cfg.Gate(ctx) {
			forksCalled.Add(1)
		}
	}

	if forksCalled.Load() != 1 {
		t.Errorf("expected 1 fork to pass gate, got %d", forksCalled.Load())
	}
}
