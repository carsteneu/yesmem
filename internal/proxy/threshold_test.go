package proxy

import (
	"log"
	"testing"
)

func newTestServerWithThresholds(global int, thresholds map[string]int) *Server {
	return &Server{
		cfg: Config{
			TokenThreshold:  global,
			TokenThresholds: thresholds,
		},
		logger: log.Default(),
	}
}

func TestEffectiveTokenThreshold_GlobalDefault(t *testing.T) {
	s := newTestServerWithThresholds(200000, nil)
	got := s.effectiveTokenThreshold("")
	if got != 200000 {
		t.Errorf("effectiveTokenThreshold('') = %d, want 200000", got)
	}
}

func TestEffectiveTokenThreshold_ExactModelMatch(t *testing.T) {
	s := newTestServerWithThresholds(200000, map[string]int{
		"opus": 180000,
	})
	got := s.effectiveTokenThreshold("claude-opus-4-6")
	if got != 180000 {
		t.Errorf("effectiveTokenThreshold('claude-opus-4-6') = %d, want 180000", got)
	}
}

func TestEffectiveTokenThreshold_SonnetMatch(t *testing.T) {
	s := newTestServerWithThresholds(200000, map[string]int{
		"sonnet": 180000,
	})
	got := s.effectiveTokenThreshold("claude-sonnet-4-6")
	if got != 180000 {
		t.Errorf("effectiveTokenThreshold('claude-sonnet-4-6') = %d, want 180000", got)
	}
}

func TestEffectiveTokenThreshold_HaikuMatch(t *testing.T) {
	s := newTestServerWithThresholds(200000, map[string]int{
		"haiku": 130000,
	})
	got := s.effectiveTokenThreshold("claude-haiku-4-5-20251001")
	if got != 130000 {
		t.Errorf("effectiveTokenThreshold('claude-haiku-4-5-20251001') = %d, want 130000", got)
	}
}

func TestEffectiveTokenThreshold_GPT5Match(t *testing.T) {
	s := newTestServerWithThresholds(200000, map[string]int{
		"gpt-5": 180000,
	})
	got := s.effectiveTokenThreshold("gpt-5.4")
	if got != 180000 {
		t.Errorf("effectiveTokenThreshold('gpt-5.4') = %d, want 180000", got)
	}
}

func TestEffectiveTokenThreshold_CodexMatch(t *testing.T) {
	s := newTestServerWithThresholds(200000, map[string]int{
		"codex": 180000,
	})
	got := s.effectiveTokenThreshold("gpt-5.4-codex")
	if got != 180000 {
		t.Errorf("effectiveTokenThreshold('gpt-5.4-codex') = %d, want 180000", got)
	}
}

func TestEffectiveTokenThreshold_NoModelFallsToGlobal(t *testing.T) {
	s := newTestServerWithThresholds(200000, map[string]int{
		"opus":   180000,
		"sonnet": 180000,
	})
	got := s.effectiveTokenThreshold("")
	if got != 200000 {
		t.Errorf("effectiveTokenThreshold('') = %d, want 200000", got)
	}
}

func TestEffectiveTokenThreshold_UnknownModelFallsToGlobal(t *testing.T) {
	s := newTestServerWithThresholds(200000, map[string]int{
		"opus": 180000,
	})
	got := s.effectiveTokenThreshold("some-unknown-model-v3")
	if got != 200000 {
		t.Errorf("effectiveTokenThreshold('some-unknown-model-v3') = %d, want 200000", got)
	}
}

func TestEffectiveTokenThreshold_OverrideTakesPrecedence(t *testing.T) {
	s := newTestServerWithThresholds(200000, map[string]int{
		"opus": 180000,
	})
	s.SetTokenThresholdOverride("opus", 150000)
	got := s.effectiveTokenThreshold("claude-opus-4-6")
	if got != 150000 {
		t.Errorf("effectiveTokenThreshold with override = %d, want 150000 (override wins)", got)
	}
}

func TestEffectiveTokenThreshold_OverrideResetRestoresModel(t *testing.T) {
	s := newTestServerWithThresholds(200000, map[string]int{
		"opus": 180000,
	})
	s.SetTokenThresholdOverride("opus", 150000)
	s.SetTokenThresholdOverride("opus", 0) // reset
	got := s.effectiveTokenThreshold("claude-opus-4-6")
	if got != 180000 {
		t.Errorf("effectiveTokenThreshold after reset = %d, want 180000 (model-specific)", got)
	}
}

func TestEffectiveTokenThreshold_OverrideOnlyAffectsMatchingModel(t *testing.T) {
	s := newTestServerWithThresholds(200000, map[string]int{
		"opus":   180000,
		"sonnet": 180000,
	})
	s.SetTokenThresholdOverride("opus", 150000)
	// Opus gets override
	got := s.effectiveTokenThreshold("claude-opus-4-6")
	if got != 150000 {
		t.Errorf("opus with override = %d, want 150000", got)
	}
	// Sonnet unaffected
	got = s.effectiveTokenThreshold("claude-sonnet-4-6")
	if got != 180000 {
		t.Errorf("sonnet without override = %d, want 180000", got)
	}
}

func TestEffectiveTokenThreshold_GlobalOverrideFallback(t *testing.T) {
	s := newTestServerWithThresholds(200000, map[string]int{
		"opus": 180000,
	})
	// Global override (empty key) applies to unknown models
	s.SetTokenThresholdOverride("", 120000)
	got := s.effectiveTokenThreshold("some-unknown-model")
	if got != 120000 {
		t.Errorf("global override for unknown model = %d, want 120000", got)
	}
	// Model-specific override still wins over global override
	s.SetTokenThresholdOverride("opus", 160000)
	got = s.effectiveTokenThreshold("claude-opus-4-6")
	if got != 160000 {
		t.Errorf("model override should win over global = %d, want 160000", got)
	}
}

func TestEffectiveTokenThreshold_NilMapFallsToGlobal(t *testing.T) {
	s := newTestServerWithThresholds(200000, nil)
	got := s.effectiveTokenThreshold("claude-opus-4-6")
	if got != 200000 {
		t.Errorf("effectiveTokenThreshold with nil map = %d, want 200000", got)
	}
}
