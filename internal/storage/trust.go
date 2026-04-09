package storage

import (
	"math"
	"time"

	"github.com/carsteneu/yesmem/internal/models"
)

// TrustLevel classifies how resistant a learning is to being superseded.
type TrustLevel int

const (
	TrustLow    TrustLevel = iota // < 1.0 — supersede immediately
	TrustMedium                   // 1.0-3.0 — supersede with warning
	TrustHigh                     // >= 3.0 — only as proposal (pending_confirmation)
)

// ClassifyTrust maps a trust score to a TrustLevel.
func ClassifyTrust(score float64) TrustLevel {
	if score >= 3.0 {
		return TrustHigh
	}
	if score >= 1.0 {
		return TrustMedium
	}
	return TrustLow
}

// TrustScore computes the trust level of a learning.
// Components: recall (hit frequency + recency), source weight, importance.
func TrustScore(l *models.Learning) float64 {
	// Recall: base term + log(1 + hits), scaled by recency of last hit.
	// Base 0.5 ensures source/importance still matter for never-retrieved learnings.
	recall := 0.5 + math.Log1p(float64(l.UseCount+l.SaveCount*2))
	if l.LastHitAt != nil {
		daysSinceHit := time.Since(*l.LastHitAt).Hours() / 24
		if daysSinceHit < 7 {
			recall *= 1.5 // recently accessed = more trusted
		}
	}

	// Source multiplier
	sourceMul := 1.0
	switch l.Source {
	case "user_stated":
		sourceMul = 2.0
	case "agreed_upon":
		sourceMul = 1.8
	case "claude_suggested":
		sourceMul = 1.0
	case "llm_extracted":
		sourceMul = 0.8
	}

	// Importance (1-5, default 3)
	imp := float64(l.Importance)
	if imp == 0 {
		imp = 3.0
	}

	return recall * sourceMul * (imp / 3.0)
}
