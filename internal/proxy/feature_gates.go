package proxy

import (
	"strings"

	"github.com/carsteneu/yesmem/internal/config"
)

// isFeatureEnabled checks whether a behavioral feature is active for a given model.
// Resolution order:
//  1. Longest matching model prefix in ModelFeatures (case-insensitive)
//  2. FeatureDefaults (if set)
//  3. false (zero value)
func isFeatureEnabled(cfg *Config, model, feature string) bool {
	if cfg == nil {
		return false
	}

	gate := resolveFeatureGate(cfg, model)
	if gate == nil {
		return false
	}
	return gateForFeature(gate, feature)
}

// resolveFeatureGate finds the best-matching FeatureGates for a model.
// Returns FeatureDefaults if no model-specific config matches.
func resolveFeatureGate(cfg *Config, model string) *config.FeatureGates {
	if cfg.ModelFeatures != nil {
		modelLower := strings.ToLower(model)
		var bestMatch *config.FeatureGates
		bestLen := 0
		for key, gates := range cfg.ModelFeatures {
			if gates == nil {
				continue
			}
			keyLower := strings.ToLower(key)
			if strings.HasPrefix(modelLower, keyLower) || strings.HasPrefix(modelLower, keyLower+"-") {
				if len(keyLower) > bestLen {
					bestMatch = gates
					bestLen = len(keyLower)
				}
			}
		}
		if bestMatch != nil {
			return bestMatch
		}
	}
	return cfg.FeatureDefaults
}

// gateForFeature extracts a boolean from FeatureGates by feature name.
func gateForFeature(g *config.FeatureGates, feature string) bool {
	if g == nil {
		return false
	}
	switch feature {
	case "skill_eval":
		return g.SkillEval
	case "briefing":
		return g.Briefing
	case "rules_reminder":
		return g.RulesReminder
	case "plan_checkpoint":
		return g.PlanCheckpoint
	case "think_reminder":
		return g.ThinkReminder
	case "timestamps":
		return g.Timestamps
	default:
		return false
	}
}
