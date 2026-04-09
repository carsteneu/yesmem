package models

import (
	"testing"
	"time"
)

func TestComputeScore_CategoryWeight(t *testing.T) {
	now := time.Now()
	gotcha := &Learning{Category: "gotcha", CreatedAt: now, HitCount: 0}
	pref := &Learning{Category: "preference", CreatedAt: now, HitCount: 0}

	if ComputeScore(gotcha) <= ComputeScore(pref) {
		t.Error("gotcha should score higher than preference at same age")
	}
}

func TestComputeScore_RecencyDecay(t *testing.T) {
	recent := &Learning{Category: "pattern", CreatedAt: time.Now(), TurnsAtCreation: 90, CurrentTurnCount: 100}
	old := &Learning{Category: "pattern", CreatedAt: time.Now(), TurnsAtCreation: 0, CurrentTurnCount: 100}

	if ComputeScore(recent) <= ComputeScore(old) {
		t.Error("recent learning (fewer turns since) should score higher than old one")
	}
}

func TestComputeScore_UseBoost(t *testing.T) {
	noUse := &Learning{Category: "pattern", CreatedAt: time.Now(), UseCount: 0}
	manyUse := &Learning{Category: "pattern", CreatedAt: time.Now(), UseCount: 10}

	if ComputeScore(manyUse) <= ComputeScore(noUse) {
		t.Error("learning with use_count should score higher")
	}
}

func TestScoreAndSort(t *testing.T) {
	now := time.Now()
	learnings := []Learning{
		{ID: 1, Category: "preference", CreatedAt: now.AddDate(0, 0, -60), HitCount: 0},
		{ID: 2, Category: "gotcha", CreatedAt: now, HitCount: 5},
		{ID: 3, Category: "pattern", CreatedAt: now.AddDate(0, 0, -30), HitCount: 0},
	}

	ScoreAndSort(learnings)

	if learnings[0].ID != 2 {
		t.Errorf("expected gotcha (id=2) first, got id=%d", learnings[0].ID)
	}
	// All should have scores > 0
	for _, l := range learnings {
		if l.Score <= 0 {
			t.Errorf("learning %d has zero score", l.ID)
		}
	}
}

func TestCategoryWeight_PivotMoment(t *testing.T) {
	now := time.Now()
	pivot := &Learning{Category: "pivot_moment", CreatedAt: now, HitCount: 0}
	score := ComputeScore(pivot)
	// pivot_moment should have weight 1.6 (highest — rare and valuable)
	if score < 1.5 {
		t.Errorf("pivot_moment score too low: %f", score)
	}
}

func TestComputeScore_EmotionalIntensity(t *testing.T) {
	now := time.Now()
	calm := &Learning{Category: "pattern", CreatedAt: now, HitCount: 0, EmotionalIntensity: 0.0}
	intense := &Learning{Category: "pattern", CreatedAt: now, HitCount: 0, EmotionalIntensity: 0.8}

	calmScore := ComputeScore(calm)
	intenseScore := ComputeScore(intense)

	if intenseScore <= calmScore {
		t.Errorf("intense session learning should score higher: intense=%f, calm=%f", intenseScore, calmScore)
	}

	// Emotional boost should be moderate: max +30% at intensity 1.0
	maxIntense := &Learning{Category: "pattern", CreatedAt: now, HitCount: 0, EmotionalIntensity: 1.0}
	maxScore := ComputeScore(maxIntense)
	boost := maxScore / calmScore
	if boost > 1.35 || boost < 1.25 {
		t.Errorf("max emotional boost should be ~1.3x, got %fx", boost)
	}
}

func TestRecencyDecay_StabilizedByUse(t *testing.T) {
	now := time.Now()
	ninetyDaysAgo := now.AddDate(0, 0, -90)
	thirtyDaysAgo := now.AddDate(0, 0, -30)

	// Old learning with no use — should decay normally
	oldNoUse := &Learning{
		Category:  "pattern",
		CreatedAt: ninetyDaysAgo,
		LastHitAt: nil,
	}

	// Old learning but recently accessed with genuine use — decay based on LastHitAt + useBoost
	oldRecentUse := &Learning{
		Category:  "pattern",
		CreatedAt: ninetyDaysAgo,
		UseCount:  5,
		LastHitAt: &thirtyDaysAgo,
	}

	scoreNoUse := ComputeScore(oldNoUse)
	scoreRecentUse := ComputeScore(oldRecentUse)

	// The recently-used learning should score significantly higher
	// (both from useBoost AND from stabilized decay via LastHitAt)
	if scoreRecentUse <= scoreNoUse*2 {
		t.Errorf("recently-used old learning should score much higher: recentUse=%f, noUse=%f", scoreRecentUse, scoreNoUse)
	}
}

func TestRecencyDecay_UserOverrideNeverFullyDecays(t *testing.T) {
	now := time.Now()
	veryOld := now.AddDate(0, -6, 0) // 6 months ago

	userStated := &Learning{
		Category:         "preference",
		CreatedAt:        veryOld,
		HitCount:         0,
		Source:           "user_stated",
		TurnsAtCreation:  0,
		CurrentTurnCount: 500,
	}

	normalOld := &Learning{
		Category:         "preference",
		CreatedAt:        veryOld,
		HitCount:         0,
		Source:           "llm_extracted",
		TurnsAtCreation:  0,
		CurrentTurnCount: 500,
	}

	userScore := ComputeScore(userStated)
	normalScore := ComputeScore(normalOld)

	// user_stated should score higher because of decay floor
	if userScore <= normalScore {
		t.Errorf("user_stated should decay less: user=%f, normal=%f", userScore, normalScore)
	}

	// user_stated should not decay below floor (0.5 * categoryWeight * useBoost * emotionalBoost)
	// For preference (0.8), no use (1.0), no emotion (1.0): minimum = 0.8 * 0.5 = 0.4
	if userScore < 0.39 {
		t.Errorf("user_stated decayed too much: %f (should be >= ~0.4)", userScore)
	}
}

func TestIsValidCategory_PivotMoment(t *testing.T) {
	if !IsValidCategory("pivot_moment") {
		t.Error("pivot_moment should be a valid category")
	}
}

func TestUseBoost(t *testing.T) {
	if useBoost(0, 0) != 1.0 {
		t.Errorf("useBoost(0,0) should be 1.0, got %f", useBoost(0, 0))
	}
	if useBoost(5, 0) <= 1.0 {
		t.Errorf("useBoost(5,0) should be > 1.0, got %f", useBoost(5, 0))
	}
	// save_count counts double
	if useBoost(0, 3) <= useBoost(3, 0) {
		t.Errorf("useBoost(0,3) should be > useBoost(3,0): got %f vs %f", useBoost(0, 3), useBoost(3, 0))
	}
}

func TestNoisePenalty(t *testing.T) {
	if noisePenalty(0) != 1.0 {
		t.Errorf("noisePenalty(0) should be 1.0, got %f", noisePenalty(0))
	}
	if noisePenalty(5) >= 1.0 {
		t.Errorf("noisePenalty(5) should be < 1.0, got %f", noisePenalty(5))
	}
	if noisePenalty(5) <= 0.3 {
		t.Errorf("noisePenalty(5) should be > 0.3 (floor at 0.4), got %f", noisePenalty(5))
	}
}

func TestPrecisionFactor(t *testing.T) {
	if precisionFactor(0, 2) != 1.0 {
		t.Errorf("precisionFactor(0,2) should be 1.0 (not enough data), got %f", precisionFactor(0, 2))
	}
	// inject=3: activation=(3-3)/9=0, result=1.0+0*(0.5-1.0)=1.0 exactly
	if precisionFactor(0, 3) != 1.0 {
		t.Errorf("precisionFactor(0,3) should be 1.0 (activation=0), got %f", precisionFactor(0, 3))
	}
	if precisionFactor(8, 10) <= 1.0 {
		t.Errorf("precisionFactor(8,10) should be > 1.0 (good precision), got %f", precisionFactor(8, 10))
	}
	if precisionFactor(0, 20) >= 1.0 {
		t.Errorf("precisionFactor(0,20) should be < 1.0 (zombie), got %f", precisionFactor(0, 20))
	}
}

func TestPrecisionFactorClamped(t *testing.T) {
	// useCount > injectCount (edge case: signal bus bumps use independently)
	// inject=10: activation=(10-3)/9=0.778, precision=1.0 (clamped), raw=1.5
	// result = 1.0 + 0.778 * 0.5 = 1.389
	result := precisionFactor(20, 10)
	if result > 1.5 {
		t.Errorf("precisionFactor(20,10) should be clamped to max 1.5, got %f", result)
	}
}

func TestFixationPenalty(t *testing.T) {
	if fixationPenalty(0.0) != 1.0 {
		t.Errorf("0%% ratio should be 1.0, got %f", fixationPenalty(0.0))
	}
	if fixationPenalty(0.03) != 1.0 {
		t.Errorf("3%% ratio should be 1.0, got %f", fixationPenalty(0.03))
	}
	if fixationPenalty(0.10) != 0.95 {
		t.Errorf("10%% ratio should be 0.95, got %f", fixationPenalty(0.10))
	}
	if fixationPenalty(0.20) != 0.85 {
		t.Errorf("20%% ratio should be 0.85, got %f", fixationPenalty(0.20))
	}
	if fixationPenalty(0.40) != 0.7 {
		t.Errorf("40%% ratio should be 0.7, got %f", fixationPenalty(0.40))
	}
}

func TestComputeScore_FixationAffectsScore(t *testing.T) {
	base := Learning{
		Category:   "decision",
		Importance: 5,
		CreatedAt:  time.Now().Add(-24 * time.Hour),
		Stability:  30.0,
	}

	good := base
	good.SessionFixationRatio = 0.0
	goodScore := ComputeScore(&good)

	bad := base
	bad.SessionFixationRatio = 0.35
	badScore := ComputeScore(&bad)

	if badScore >= goodScore {
		t.Errorf("fixated session score (%f) should be < clean session score (%f)", badScore, goodScore)
	}
	ratio := badScore / goodScore
	if ratio < 0.65 || ratio > 0.75 {
		t.Errorf("expected ~0.7 ratio, got %f", ratio)
	}
}

func TestComputeScoreUsesNewCounts(t *testing.T) {
	useful := &Learning{Category: "gotcha", Importance: 3, UseCount: 10, InjectCount: 15, CreatedAt: time.Now()}
	zombie := &Learning{Category: "gotcha", Importance: 3, UseCount: 0, InjectCount: 50, NoiseCount: 5, CreatedAt: time.Now()}
	if ComputeScore(useful) <= ComputeScore(zombie) {
		t.Errorf("useful learning (%.4f) should score higher than zombie (%.4f)", ComputeScore(useful), ComputeScore(zombie))
	}
}

func TestComputeContextualScore_ProjectMatch(t *testing.T) {
	l := &Learning{Category: "gotcha", Importance: 3, Stability: 30.0, Project: "memory", CreatedAt: time.Now()}
	ctx := QueryContext{Project: "memory"}
	ctxOther := QueryContext{Project: "greenWebsite"}

	if ComputeContextualScore(l, ctx) <= ComputeContextualScore(l, ctxOther) {
		t.Error("matching project should score higher than non-matching project")
	}
}

func TestComputeContextualScore_EntityMatch(t *testing.T) {
	l := &Learning{Category: "gotcha", Importance: 3, Stability: 30.0, CreatedAt: time.Now(),
		Entities: []string{"schema", "proxy_server"}}
	ctx := QueryContext{FilePaths: []string{"/home/user/memory/yesmem/internal/storage/schema.go"}}
	ctxNoMatch := QueryContext{FilePaths: []string{"/home/user/memory/yesmem/cmd/main.go"}}

	if ComputeContextualScore(l, ctx) <= ComputeContextualScore(l, ctxNoMatch) {
		t.Error("entity match in file paths should score higher than no match")
	}
}

func TestComputeContextualScore_DomainMatch(t *testing.T) {
	l := &Learning{Category: "gotcha", Importance: 3, Stability: 30.0, Domain: "code", CreatedAt: time.Now()}
	ctx := QueryContext{Domain: "code"}
	ctxOther := QueryContext{Domain: "marketing"}

	if ComputeContextualScore(l, ctx) <= ComputeContextualScore(l, ctxOther) {
		t.Error("matching domain should score higher than non-matching domain")
	}
}

func TestComputeContextualScore_NoContext(t *testing.T) {
	// Use a future CreatedAt so ageDays clamps to 0 and decay = exp(0) = 1.0 exactly.
	// This makes both ComputeScore calls produce bit-identical results regardless of wall-clock drift.
	future := time.Now().Add(24 * time.Hour)
	l := &Learning{Category: "gotcha", Importance: 3, Stability: 30.0, CreatedAt: future}
	base := ComputeScore(l)
	contextual := ComputeContextualScore(l, QueryContext{})
	if base != contextual {
		t.Errorf("empty QueryContext must return exactly ComputeScore: base=%.15f contextual=%.15f", base, contextual)
	}
}

func TestComputeContextualScore_AllBoosts(t *testing.T) {
	l := &Learning{Category: "gotcha", Importance: 3, Stability: 30.0, Project: "memory", Domain: "code",
		Entities: []string{"schema.go"}, CreatedAt: time.Now()}
	ctx := QueryContext{Project: "memory", Domain: "code",
		FilePaths: []string{"/path/to/schema.go"}}

	// All 3 boosts: 1.5 * 1.4 * 1.2 = 2.52x (project boost is 1.5x for fresh learning)
	base := ComputeScore(l)
	contextual := ComputeContextualScore(l, ctx)
	ratio := contextual / base
	const wantRatio = 1.5 * 1.4 * 1.2 // 2.52
	const delta = 0.01
	if ratio < wantRatio-delta || ratio > wantRatio+delta {
		t.Errorf("all-boosts ratio should be ~%.3f, got %.4f", wantRatio, ratio)
	}
}

func TestPrecisionFactorGradualRamp(t *testing.T) {
	// Below 3 injections: neutral
	if precisionFactor(0, 2) != 1.0 {
		t.Error("expected 1.0 for <3 injections")
	}
	// At 3 injections, 0 use: activation=0, result=1.0
	pf3 := precisionFactor(0, 3)
	if pf3 < 0.95 || pf3 > 1.0 {
		t.Errorf("expected ~1.0 at inject=3, got %f", pf3)
	}
	// At 12 injections, 0 use: fully penalized (0.5)
	pf12 := precisionFactor(0, 12)
	if pf12 < 0.49 || pf12 > 0.51 {
		t.Errorf("expected ~0.5 at inject=12 use=0, got %f", pf12)
	}
	// At 12 injections, 12 use: fully boosted (1.5)
	pf12full := precisionFactor(12, 12)
	if pf12full < 1.49 || pf12full > 1.51 {
		t.Errorf("expected ~1.5 at inject=12 use=12, got %f", pf12full)
	}
	// Gradual: inject=6 should be between inject=3 and inject=12
	pf6 := precisionFactor(0, 6)
	if pf6 >= pf3 || pf6 <= pf12 {
		t.Errorf("expected pf6 between pf3 and pf12: pf3=%f pf6=%f pf12=%f", pf3, pf6, pf12)
	}
}

func TestExplorationBonus(t *testing.T) {
	if explorationBonus(0) != 1.3 {
		t.Error("expected 1.3 for 0 injections")
	}
	if explorationBonus(2) != 1.3 {
		t.Error("expected 1.3 for 2 injections")
	}
	if explorationBonus(3) != 1.0 {
		t.Error("expected 1.0 for 3 injections")
	}
	if explorationBonus(100) != 1.0 {
		t.Error("expected 1.0 for 100 injections")
	}
}

func TestEntityMatchMinLength(t *testing.T) {
	// Use future CreatedAt to pin decay=1.0, avoiding float drift between two ComputeScore calls
	future := time.Now().Add(24 * time.Hour)

	// Short entity "go" should NOT match
	l := &Learning{Category: "gotcha", Importance: 3, Stability: 30.0, CreatedAt: future,
		Entities: []string{"go"}}
	ctx := QueryContext{FilePaths: []string{"/path/to/learnings.go"}}
	base := ComputeScore(l)
	contextual := ComputeContextualScore(l, ctx)
	if contextual != base {
		t.Errorf("short entity 'go' should not boost: base=%f contextual=%f", base, contextual)
	}

	// Long entity "schema" SHOULD match
	l2 := &Learning{Category: "gotcha", Importance: 3, Stability: 30.0, CreatedAt: future,
		Entities: []string{"schema"}}
	ctx2 := QueryContext{FilePaths: []string{"/path/to/schema.go"}}
	base2 := ComputeScore(l2)
	contextual2 := ComputeContextualScore(l2, ctx2)
	if contextual2 <= base2 {
		t.Errorf("long entity 'schema' should boost: base=%f contextual=%f", base2, contextual2)
	}
}

// --- Project-Recency Boost Tests ---

func TestProjectRecencyBoost_Fresh(t *testing.T) {
	l := &Learning{Category: "gotcha", Importance: 3, Stability: 30.0,
		Project: "memory", CreatedAt: time.Now().Add(-24 * time.Hour)}
	base := ComputeScore(l)
	contextual := ComputeContextualScore(l, QueryContext{Project: "memory"})
	ratio := contextual / base
	if ratio < 1.49 || ratio > 1.51 {
		t.Errorf("fresh same-project (<48h) should get ~1.5x boost, got %.3fx", ratio)
	}
}

func TestProjectRecencyBoost_MidTurns(t *testing.T) {
	l := &Learning{Category: "gotcha", Importance: 3, Stability: 30.0,
		Project: "memory", CreatedAt: time.Now(), TurnsAtCreation: 70, CurrentTurnCount: 100}
	base := ComputeScore(l)
	contextual := ComputeContextualScore(l, QueryContext{Project: "memory"})
	ratio := contextual / base
	if ratio < 1.29 || ratio > 1.31 {
		t.Errorf("mid-turns same-project (30 turns) should get ~1.3x boost, got %.3fx", ratio)
	}
}

func TestProjectRecencyBoost_Old(t *testing.T) {
	l := &Learning{Category: "gotcha", Importance: 3, Stability: 30.0,
		Project: "memory", CreatedAt: time.Now(), TurnsAtCreation: 0, CurrentTurnCount: 100}
	base := ComputeScore(l)
	contextual := ComputeContextualScore(l, QueryContext{Project: "memory"})
	ratio := contextual / base
	if ratio < 1.09 || ratio > 1.11 {
		t.Errorf("old same-project (100 turns) should get ~1.1x boost, got %.3fx", ratio)
	}
}

func TestProjectRecencyBoost_NoMatch(t *testing.T) {
	// Use future CreatedAt to pin decay and avoid float drift
	future := time.Now().Add(24 * time.Hour)
	l := &Learning{Category: "gotcha", Importance: 3, Stability: 30.0,
		Project: "other", CreatedAt: future}
	base := ComputeScore(l)
	contextual := ComputeContextualScore(l, QueryContext{Project: "memory"})
	if base != contextual {
		t.Errorf("non-matching project should get no boost: base=%.15f contextual=%.15f", base, contextual)
	}
}

// --- TurnBasedDecay tests ---

func TestTurnBasedDecay_ZeroTurns(t *testing.T) {
	decay := TurnBasedDecay(100, 100, 30.0, "llm_extracted", 0, 0)
	if decay != 1.0 {
		t.Errorf("zero turns_since should return 1.0, got %f", decay)
	}
}

func TestTurnBasedDecay_NegativeTurns(t *testing.T) {
	decay := TurnBasedDecay(200, 100, 30.0, "llm_extracted", 0, 0)
	if decay != 1.0 {
		t.Errorf("negative turns_since should return 1.0, got %f", decay)
	}
}

func TestTurnBasedDecay_StandardDecay(t *testing.T) {
	// turns_since=30, stability=30 → e^(-1) ≈ 0.368
	decay := TurnBasedDecay(0, 30, 30.0, "llm_extracted", 0, 0)
	if decay < 0.35 || decay > 0.40 {
		t.Errorf("expected ~0.368 for one half-life, got %f", decay)
	}
}

func TestTurnBasedDecay_StabilizedByUse(t *testing.T) {
	noUse := TurnBasedDecay(0, 60, 30.0, "llm_extracted", 0, 0)
	withUse := TurnBasedDecay(0, 60, 30.0, "llm_extracted", 5, 2)
	if withUse <= noUse {
		t.Errorf("use_count should slow decay: noUse=%f withUse=%f", noUse, withUse)
	}
}

func TestTurnBasedDecay_SaveCountDouble(t *testing.T) {
	usesOnly := TurnBasedDecay(0, 60, 30.0, "llm_extracted", 3, 0)
	savesOnly := TurnBasedDecay(0, 60, 30.0, "llm_extracted", 0, 3)
	if savesOnly <= usesOnly {
		t.Errorf("save_count=3 (weight 2x) should decay slower than use_count=3: uses=%f saves=%f", usesOnly, savesOnly)
	}
}

func TestTurnBasedDecay_UserStatedFloor(t *testing.T) {
	decay := TurnBasedDecay(0, 10000, 30.0, "user_stated", 0, 0)
	if decay < 0.5 {
		t.Errorf("user_stated floor should be 0.5, got %f", decay)
	}
}

func TestTurnBasedDecay_UserOverrideFloor(t *testing.T) {
	decay := TurnBasedDecay(0, 10000, 30.0, "user_override", 0, 0)
	if decay < 0.5 {
		t.Errorf("user_override floor should be 0.5, got %f", decay)
	}
}

func TestTurnBasedDecay_UniversalFloor(t *testing.T) {
	decay := TurnBasedDecay(0, 10000, 30.0, "llm_extracted", 0, 0)
	if decay < 0.1 {
		t.Errorf("universal floor should be 0.1, got %f", decay)
	}
}

func TestTurnBasedDecay_ZeroStability(t *testing.T) {
	decay := TurnBasedDecay(0, 30, 0, "llm_extracted", 0, 0)
	expected := TurnBasedDecay(0, 30, 30.0, "llm_extracted", 0, 0)
	if decay != expected {
		t.Errorf("zero stability should default to 30.0: got %f, expected %f", decay, expected)
	}
}
