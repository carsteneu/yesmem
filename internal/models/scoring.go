package models

import (
	"math"
	"sort"
	"strings"
)

// categoryWeight returns the base importance weight for a category.
func categoryWeight(category string) float64 {
	switch category {
	case "pivot_moment":
		return 1.6
	case "gotcha":
		return 1.5
	case "explicit_teaching":
		return 1.4
	case "decision":
		return 1.3
	case "pattern":
		return 1.0
	case "preference":
		return 0.8
	case "relationship":
		return 0.7
	case "fact":
		return 1.1
	case "unfinished":
		return 1.2
	case "strategic":
		return 1.3
	default:
		return 1.0
	}
}

// TurnBasedDecay returns a decay factor based on how many project turns have
// elapsed since the learning was created, not wall-clock time.
// Formula: decay = e^(-turns_since / effective_stability)
// where effective_stability = base_stability * (1 + log2(1 + use_count + save_count*2))
//
// A project paused for 3 months with 0 turns has 0 decay — all learnings
// remain at full relevance.
func TurnBasedDecay(turnsAtCreation, currentTurnCount int64, stability float64, source string, useCount, saveCount int) float64 {
	turnsSince := currentTurnCount - turnsAtCreation
	if turnsSince <= 0 {
		return 1.0
	}

	if stability <= 0 {
		stability = 30.0
	}

	// Effective stability grows with genuine usage (spaced reinforcement)
	effectiveStability := stability * (1.0 + math.Log2(1.0+float64(useCount+saveCount*2)))

	decay := math.Exp(-float64(turnsSince) / effectiveStability)

	// Universal floor: even ancient learnings retain 10% of their score
	if decay < 0.1 {
		decay = 0.1
	}

	// User-stated/override learnings have a higher floor
	if (source == "user_stated" || source == "user_override") && decay < 0.5 {
		decay = 0.5
	}

	return decay
}

// emotionalBoost returns a boost factor based on emotional intensity.
// Max +30% at intensity 1.0: 1.0 + intensity * 0.3
func emotionalBoost(intensity float64) float64 {
	if intensity <= 0 {
		return 1.0
	}
	if intensity > 1.0 {
		intensity = 1.0
	}
	return 1.0 + intensity*0.3
}

// importanceBoost returns a multiplier based on learning importance (1-5, default 3).
// Importance 3 = neutral (1.0x), 5 = 1.33x, 1 = 0.67x.
func importanceBoost(importance int) float64 {
	imp := importance
	if imp <= 0 {
		imp = 3
	}
	return float64(imp) / 3.0
}

// useBoost returns reinforcement from genuine usage.
// save_count weighs 2x because preventing errors is high-value.
func useBoost(useCount, saveCount int) float64 {
	effective := useCount + saveCount*2
	if effective <= 0 {
		return 1.0
	}
	return 1.0 + math.Log2(1.0+float64(effective))
}

// noisePenalty reduces score for learnings frequently flagged as irrelevant.
// Floor at 0.4 — noise alone never kills a learning completely.
func noisePenalty(noiseCount int) float64 {
	if noiseCount <= 0 {
		return 1.0
	}
	penalty := 1.0 / (1.0 + float64(noiseCount)*0.15)
	if penalty < 0.4 {
		return 0.4
	}
	return penalty
}

// precisionFactor rewards learnings that are used when shown.
// Gradually activates between 3-12 injections (no cliff).
// Range: 0.5 (never used) to 1.5 (always used) at full activation.
func precisionFactor(useCount, injectCount int) float64 {
	if injectCount < 3 {
		return 1.0 // too few data points
	}
	// Gradual activation: 0.0 at inject=3, 1.0 at inject=12
	activation := float64(injectCount-3) / 9.0
	if activation > 1.0 {
		activation = 1.0
	}
	precision := float64(useCount) / float64(injectCount)
	if precision > 1.0 {
		precision = 1.0
	}
	raw := 0.5 + precision // 0.5 – 1.5
	return 1.0 + activation*(raw-1.0) // blend from 1.0 toward raw
}

// explorationBonus gives new learnings a chance to prove themselves.
// 30% boost until they've been injected 3 times.
func explorationBonus(injectCount int) float64 {
	if injectCount < 3 {
		return 1.3
	}
	return 1.0
}

// fixationPenalty penalizes learnings from sessions with high fixation ratios.
// Conservative thresholds — only pathological fixation triggers meaningful penalty.
func fixationPenalty(ratio float64) float64 {
	if ratio < 0.05 {
		return 1.0
	}
	if ratio < 0.15 {
		return 0.95
	}
	if ratio < 0.30 {
		return 0.85
	}
	return 0.7
}

// ComputeScore calculates the relevance score for a learning.
func ComputeScore(l *Learning) float64 {
	return categoryWeight(l.Category) *
		TurnBasedDecay(l.TurnsAtCreation, l.CurrentTurnCount, l.Stability, l.Source, l.UseCount, l.SaveCount) *
		useBoost(l.UseCount, l.SaveCount) *
		noisePenalty(l.NoiseCount) *
		precisionFactor(l.UseCount, l.InjectCount) *
		explorationBonus(l.InjectCount) *
		emotionalBoost(l.EmotionalIntensity) *
		importanceBoost(l.Importance) *
		fixationPenalty(l.SessionFixationRatio)
}

// ScoreAndSort computes scores for all learnings and sorts by score descending.
func ScoreAndSort(learnings []Learning) {
	for i := range learnings {
		learnings[i].Score = ComputeScore(&learnings[i])
	}
	sort.Slice(learnings, func(i, j int) bool {
		return learnings[i].Score > learnings[j].Score
	})
}

// QueryContext provides current session context for relevance-weighted scoring.
type QueryContext struct {
	Project   string   // current project short name
	FilePaths []string // files currently being worked on
	Domain    string   // code, marketing, legal, finance, general
}

// ComputeContextualScore applies context-dependent boosts on top of base score.
// Empty context = base score (backward compatible).
func ComputeContextualScore(l *Learning, ctx QueryContext) float64 {
	base := ComputeScore(l)

	// Project match: turn-graduated boost — fresh same-project knowledge is more valuable
	if ctx.Project != "" && l.Project != "" && ProjectMatches(l.Project, ctx.Project) {
		turnsSince := l.CurrentTurnCount - l.TurnsAtCreation
		if turnsSince < 0 {
			turnsSince = 0
		}
		switch {
		case turnsSince < 10:
			base *= 1.5
		case turnsSince < 50:
			base *= 1.3
		default:
			base *= 1.1
		}
	}

	// Entity match: 1.4x boost if any learning entity appears in current file paths
	// Minimum entity length 4 to avoid false positives ("go", "db", "a")
	if len(ctx.FilePaths) > 0 && len(l.Entities) > 0 {
		matched := false
		for _, entity := range l.Entities {
			if len(entity) < 4 {
				continue // skip short entities — too many false positives
			}
			entityLower := strings.ToLower(entity)
			for _, fp := range ctx.FilePaths {
				fpLower := strings.ToLower(fp)
				// Match against basename or last directory component, not full path
				lastSlash := strings.LastIndex(fpLower, "/")
				fpTail := fpLower
				if lastSlash >= 0 {
					fpTail = fpLower[lastSlash+1:]
					// Also check parent directory name
					parentStart := strings.LastIndex(fpLower[:lastSlash], "/")
					parentDir := fpLower[parentStart+1 : lastSlash]
					if strings.Contains(parentDir, entityLower) {
						matched = true
						break
					}
				}
				if strings.Contains(fpTail, entityLower) {
					matched = true
					break
				}
			}
			if matched {
				break
			}
		}
		if matched {
			base *= 1.4
		}
	}

	// Domain match: 1.2x boost for same domain
	if ctx.Domain != "" && l.Domain != "" && l.Domain == ctx.Domain {
		base *= 1.2
	}

	return base
}

// ContextualScoreAndSort computes contextual scores and sorts by score descending.
func ContextualScoreAndSort(learnings []Learning, ctx QueryContext) {
	for i := range learnings {
		learnings[i].Score = ComputeContextualScore(&learnings[i], ctx)
	}
	sort.Slice(learnings, func(i, j int) bool {
		return learnings[i].Score > learnings[j].Score
	})
}
