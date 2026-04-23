package hooks

import (
	"math"
	"testing"
)

func TestInjectionDecay_InsufficientData(t *testing.T) {
	tests := []struct {
		name        string
		injectCount int
		useCount    int
		saveCount   int
	}{
		{"zero injections", 0, 0, 0},
		{"one injection", 1, 0, 0},
		{"nineteen injections", 19, 0, 0},
		{"few injections with uses", 5, 3, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := injectionDecay(tt.injectCount, tt.useCount, tt.saveCount)
			if got != 1.0 {
				t.Errorf("injectionDecay(%d, %d, %d) = %f, want 1.0 (insufficient data)", tt.injectCount, tt.useCount, tt.saveCount, got)
			}
		})
	}
}

func TestInjectionDecay_ZeroUsesHighInjections(t *testing.T) {
	tests := []struct {
		name        string
		injectCount int
	}{
		{"20 injections", 20},
		{"100 injections", 100},
		{"1000 injections", 1000},
		{"1232 injections", 1232},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := injectionDecay(tt.injectCount, 0, 0)
			if got != 0.3 {
				t.Errorf("injectionDecay(%d, 0, 0) = %f, want 0.3 (floor for zero precision)", tt.injectCount, got)
			}
		})
	}
}

func TestInjectionDecay_HighPrecision(t *testing.T) {
	tests := []struct {
		name        string
		injectCount int
		useCount    int
		saveCount   int
	}{
		{"10% use ratio", 100, 10, 0},
		{"20% use ratio", 50, 10, 0},
		{"saves boosting to 10%", 100, 2, 4},
		{"50% use ratio", 100, 50, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := injectionDecay(tt.injectCount, tt.useCount, tt.saveCount)
			if got != 1.0 {
				t.Errorf("injectionDecay(%d, %d, %d) = %f, want 1.0 (good precision)", tt.injectCount, tt.useCount, tt.saveCount, got)
			}
		})
	}
}

func TestInjectionDecay_Gradient(t *testing.T) {
	tests := []struct {
		name        string
		injectCount int
		useCount    int
		saveCount   int
		wantApprox  float64
	}{
		{"5% ratio", 100, 5, 0, 0.65},
		{"2% ratio", 100, 2, 0, 0.44},
		{"1% ratio", 100, 1, 0, 0.37},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := injectionDecay(tt.injectCount, tt.useCount, tt.saveCount)
			if math.Abs(got-tt.wantApprox) > 0.01 {
				t.Errorf("injectionDecay(%d, %d, %d) = %f, want ~%f", tt.injectCount, tt.useCount, tt.saveCount, got, tt.wantApprox)
			}
		})
	}
}

func TestInjectionDecay_SaveCountBoost(t *testing.T) {
	decayNoSave := injectionDecay(100, 0, 0)
	decayWithSave := injectionDecay(100, 0, 3)
	if decayWithSave <= decayNoSave {
		t.Errorf("save_count should boost decay: without=%f, with=%f", decayNoSave, decayWithSave)
	}
}

func TestInjectionDecay_NeverBelowFloor(t *testing.T) {
	got := injectionDecay(1_000_000, 0, 0)
	if got < 0.3 {
		t.Errorf("injectionDecay should never go below 0.3, got %f", got)
	}
}

func TestInjectionDecay_EffectiveScoreBlocking(t *testing.T) {
	decay := injectionDecay(1232, 0, 0)
	effectiveScore := 2.0 * decay
	if effectiveScore >= 2.0 {
		t.Errorf("a score=2 gotcha with 1232 injections and 0 uses should NOT pass threshold: effective=%f", effectiveScore)
	}
}

func TestInjectionDecay_FreshGotchaPassesThreshold(t *testing.T) {
	decay := injectionDecay(5, 0, 0)
	effectiveScore := 2.0 * decay
	if effectiveScore < 2.0 {
		t.Errorf("a score=2 fresh gotcha should pass threshold: effective=%f", effectiveScore)
	}
}

func TestInjectionDecay_NegativeInputs(t *testing.T) {
	tests := []struct {
		name        string
		injectCount int
		useCount    int
		saveCount   int
	}{
		{"negative injectCount", -5, 0, 0},
		{"negative useCount", 100, -3, 0},
		{"negative saveCount", 100, 0, -2},
		{"all negative", -1, -1, -1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := injectionDecay(tt.injectCount, tt.useCount, tt.saveCount)
			if got < decayFloor || got > 1.0 {
				t.Errorf("injectionDecay(%d, %d, %d) = %f, should be in [%f, 1.0]",
					tt.injectCount, tt.useCount, tt.saveCount, got, decayFloor)
			}
		})
	}
}

func TestInjectionDecay_SingleKeywordWithDecay(t *testing.T) {
	decay := injectionDecay(500, 0, 0)
	effScore := 1.0 * decay
	if effScore >= 1.0 {
		t.Errorf("score=1 with heavy decay should not pass long-keyword threshold: eff=%f", effScore)
	}
}
