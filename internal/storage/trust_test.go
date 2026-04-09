package storage

import (
	"testing"
	"time"

	"github.com/carsteneu/yesmem/internal/models"
)

func TestTrustScoreLow(t *testing.T) {
	// claude_suggested, 0 hits, importance 2 → should be low trust
	l := &models.Learning{
		Source:     "claude_suggested",
		HitCount:  0,
		Importance: 2,
	}
	score := TrustScore(l)
	level := ClassifyTrust(score)
	if level != TrustLow {
		t.Errorf("expected TrustLow, got %d (score: %.2f)", level, score)
	}
	// With base 0.5: 0.5 * 1.0 * (2/3) = 0.33
	if score >= 1.0 {
		t.Errorf("expected score < 1.0, got %.2f", score)
	}
}

func TestTrustScoreMedium(t *testing.T) {
	// llm_extracted, 5 use_count, importance 3, no recent hit → medium
	l := &models.Learning{
		Source:     "llm_extracted",
		UseCount:   5,
		Importance: 3,
	}
	score := TrustScore(l)
	level := ClassifyTrust(score)
	if level != TrustMedium {
		t.Errorf("expected TrustMedium, got %d (score: %.2f)", level, score)
	}
}

func TestTrustScoreHigh(t *testing.T) {
	// user_stated, 12 use_count, importance 4, recent hit → high trust
	recent := time.Now().Add(-2 * time.Hour)
	l := &models.Learning{
		Source:     "user_stated",
		UseCount:   12,
		Importance: 4,
		LastHitAt:  &recent,
	}
	score := TrustScore(l)
	level := ClassifyTrust(score)
	if level != TrustHigh {
		t.Errorf("expected TrustHigh, got %d (score: %.2f)", level, score)
	}
	if score < 3.0 {
		t.Errorf("expected score >= 3.0, got %.2f", score)
	}
}

func TestTrustScoreZeroHitsStillUsesSourceAndImportance(t *testing.T) {
	// Key insight: without base term, both would be 0.0
	low := &models.Learning{Source: "claude_suggested", HitCount: 0, Importance: 2}
	high := &models.Learning{Source: "user_stated", HitCount: 0, Importance: 4}

	lowScore := TrustScore(low)
	highScore := TrustScore(high)

	if lowScore >= highScore {
		t.Errorf("user_stated/imp4 (%.2f) should score higher than claude_suggested/imp2 (%.2f)", highScore, lowScore)
	}
	if lowScore == 0.0 {
		t.Error("zero-hit learning should not have trust score 0.0 (base term missing)")
	}
}

func TestTrustScoreRecencyBoost(t *testing.T) {
	old := &models.Learning{Source: "llm_extracted", HitCount: 3, Importance: 3}
	recent := time.Now().Add(-1 * time.Hour)
	fresh := &models.Learning{Source: "llm_extracted", HitCount: 3, Importance: 3, LastHitAt: &recent}

	oldScore := TrustScore(old)
	freshScore := TrustScore(fresh)

	if freshScore <= oldScore {
		t.Errorf("recently hit learning (%.2f) should score higher than no-recent-hit (%.2f)", freshScore, oldScore)
	}
}

func TestClassifyTrustBoundaries(t *testing.T) {
	tests := []struct {
		score float64
		want  TrustLevel
	}{
		{0.0, TrustLow},
		{0.99, TrustLow},
		{1.0, TrustMedium},
		{2.99, TrustMedium},
		{3.0, TrustHigh},
		{10.0, TrustHigh},
	}
	for _, tt := range tests {
		got := ClassifyTrust(tt.score)
		if got != tt.want {
			t.Errorf("ClassifyTrust(%.2f) = %d, want %d", tt.score, got, tt.want)
		}
	}
}

func TestTrustScoreUsesUseCount(t *testing.T) {
	used := &models.Learning{UseCount: 10, SaveCount: 2, Source: "llm_extracted", Importance: 3, CreatedAt: time.Now()}
	shown := &models.Learning{HitCount: 100, Source: "llm_extracted", Importance: 3, CreatedAt: time.Now()}
	if TrustScore(used) <= TrustScore(shown) {
		t.Errorf("used learning (%.2f) should score higher than HitCount-only (%.2f)", TrustScore(used), TrustScore(shown))
	}
}

func TestSupersedeResistanceIntegration(t *testing.T) {
	store, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	now := time.Now()

	// Insert a high-trust learning (user_stated, many hits, high importance)
	highTrustID, err := store.InsertLearning(&models.Learning{
		Category: "preference", Content: "User bevorzugt Deutsch",
		Confidence: 1.0, CreatedAt: now, ModelUsed: "self",
		Source: "user_stated",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Simulate hits to build trust
	store.IncrementHitCounts([]int64{highTrustID})
	store.IncrementHitCounts([]int64{highTrustID})
	store.IncrementHitCounts([]int64{highTrustID})

	// Reload and check trust
	l, err := store.GetLearning(highTrustID)
	if err != nil {
		t.Fatal(err)
	}
	score := TrustScore(l)
	t.Logf("High-trust learning: source=%s, hits=%d, importance=%d, score=%.2f", l.Source, l.HitCount, l.Importance, score)

	// Insert a low-trust learning
	lowTrustID, err := store.InsertLearning(&models.Learning{
		Category: "preference", Content: "Maybe English?",
		Confidence: 1.0, CreatedAt: now, ModelUsed: "self",
		Source: "claude_suggested",
	})
	if err != nil {
		t.Fatal(err)
	}
	lowL, _ := store.GetLearning(lowTrustID)
	lowScore := TrustScore(lowL)
	t.Logf("Low-trust learning: source=%s, hits=%d, importance=%d, score=%.2f", lowL.Source, lowL.HitCount, lowL.Importance, lowScore)

	// Verify: low-trust can be superseded immediately
	lowLevel := ClassifyTrust(lowScore)
	if lowLevel != TrustLow {
		t.Errorf("claude_suggested with 0 hits should be TrustLow, got %d (score: %.2f)", lowLevel, lowScore)
	}

	// Verify: SetSupersedeStatus works
	err = store.SetSupersedeStatus(lowTrustID, "pending_confirmation")
	if err != nil {
		t.Fatal(err)
	}
	updated, _ := store.GetLearning(lowTrustID)
	if updated.SupersedeStatus != "pending_confirmation" {
		t.Errorf("expected supersede_status=pending_confirmation, got %q", updated.SupersedeStatus)
	}
}
