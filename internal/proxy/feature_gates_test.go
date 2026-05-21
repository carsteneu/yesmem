package proxy

import (
	"testing"

	"github.com/carsteneu/yesmem/internal/config"
)

func TestIsFeatureEnabled_ExactMatch(t *testing.T) {
	cfg := &Config{
		ModelFeatures: map[string]*config.FeatureGates{
			"deepseek": {SkillEval: true},
		},
	}
	if !isFeatureEnabled(cfg, "deepseek", "skill_eval") {
		t.Error("deepseek.skill_eval should be true")
	}
	if isFeatureEnabled(cfg, "deepseek", "briefing") {
		t.Error("deepseek.briefing should be false (not configured)")
	}
	if isFeatureEnabled(cfg, "gpt-5", "skill_eval") {
		t.Error("gpt-5.skill_eval should be false (not configured)")
	}
}

func TestIsFeatureEnabled_PrefixMatch(t *testing.T) {
	cfg := &Config{
		ModelFeatures: map[string]*config.FeatureGates{
			"deepseek": {SkillEval: true, Briefing: true},
		},
	}
	if !isFeatureEnabled(cfg, "deepseek-v4-pro", "skill_eval") {
		t.Error("deepseek-v4-pro should match deepseek prefix for skill_eval")
	}
	if !isFeatureEnabled(cfg, "deepseek-v4-pro", "briefing") {
		t.Error("deepseek-v4-pro should match deepseek prefix for briefing")
	}
	if isFeatureEnabled(cfg, "anthropic-deepseek", "skill_eval") {
		t.Error("anthropic-deepseek should NOT match deepseek prefix")
	}
}

func TestIsFeatureEnabled_CaseInsensitive(t *testing.T) {
	cfg := &Config{
		ModelFeatures: map[string]*config.FeatureGates{
			"deepseek": {SkillEval: true},
		},
	}
	if !isFeatureEnabled(cfg, "DeepSeek-V4-Pro", "skill_eval") {
		t.Error("case-insensitive match failed")
	}
}

func TestIsFeatureEnabled_FeatureDefaults(t *testing.T) {
	cfg := &Config{
		FeatureDefaults: &config.FeatureGates{SkillEval: true},
	}
	if !isFeatureEnabled(cfg, "unknown-model", "skill_eval") {
		t.Error("should use feature_defaults when model not configured")
	}
	if isFeatureEnabled(cfg, "unknown-model", "briefing") {
		t.Error("feature_defaults.briefing = false (zero value)")
	}
}

func TestIsFeatureEnabled_ModelOverridesDefaults(t *testing.T) {
	cfg := &Config{
		FeatureDefaults: &config.FeatureGates{SkillEval: true},
		ModelFeatures: map[string]*config.FeatureGates{
			"deepseek": {SkillEval: false},
		},
	}
	if isFeatureEnabled(cfg, "deepseek-v4-pro", "skill_eval") {
		t.Error("model override should win over feature_defaults")
	}
}

func TestIsFeatureEnabled_MoreSpecificPrefix(t *testing.T) {
	cfg := &Config{
		ModelFeatures: map[string]*config.FeatureGates{
			"deepseek":       {SkillEval: true},
			"deepseek-v4":    {SkillEval: false},
			"deepseek-v4-pro": {SkillEval: true},
		},
	}
	// Longest prefix match wins
	if !isFeatureEnabled(cfg, "deepseek-v4-pro", "skill_eval") {
		t.Error("longest prefix deepseek-v4-pro should match")
	}
	if isFeatureEnabled(cfg, "deepseek-v4-lite", "skill_eval") {
		t.Error("deepseek-v4-lite should match deepseek-v4 (false)")
	}
	if !isFeatureEnabled(cfg, "deepseek-v3", "skill_eval") {
		t.Error("deepseek-v3 should match deepseek (true)")
	}
}

func TestIsFeatureEnabled_AllGates(t *testing.T) {
	cfg := &Config{
		ModelFeatures: map[string]*config.FeatureGates{
			"deepseek": {
				SkillEval:      true,
				Briefing:       true,
				RulesReminder:  false,
				PlanCheckpoint: true,
				ThinkReminder:  false,
			},
		},
	}
	tests := []struct {
		feature string
		want    bool
	}{
		{"skill_eval", true},
		{"briefing", true},
		{"rules_reminder", false},
		{"plan_checkpoint", true},
		{"think_reminder", false},
	}
	for _, tt := range tests {
		if got := isFeatureEnabled(cfg, "deepseek", tt.feature); got != tt.want {
			t.Errorf("%s = %v, want %v", tt.feature, got, tt.want)
		}
	}
}

func TestIsFeatureEnabled_EmptyConfig(t *testing.T) {
	cfg := &Config{}
	if isFeatureEnabled(cfg, "deepseek-v4-pro", "skill_eval") {
		t.Error("empty config should default to false")
	}
}
