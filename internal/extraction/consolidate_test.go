package extraction

import (
	"fmt"
	"testing"
	"time"

	"github.com/carsteneu/yesmem/internal/models"
)

func TestRunConsolidation_ConvergesOnEmptyStore(t *testing.T) {
	store := mustOpenStore(t)

	result := RunConsolidation(store, nil, nil, ConsolidateConfig{MaxRounds: 3})

	if result.Rounds != 1 {
		t.Errorf("expected 1 round on empty store, got %d", result.Rounds)
	}
	if result.TotalSuperseded != 0 {
		t.Errorf("expected 0 superseded, got %d", result.TotalSuperseded)
	}
}

func TestRunConsolidation_RuleBasedOnly(t *testing.T) {
	store := mustOpenStore(t)

	// Insert near-duplicates that BigramJaccard should catch (Jaccard ~0.91 > 0.85 threshold)
	insertTestLearning(store, "User bevorzugt immer die deutsche Sprache in allen Antworten und Kommentaren", "preference")
	insertTestLearning(store, "User bevorzugt immer die deutsche Sprache in allen Antworten und Kommentaren bitte", "preference")

	result := RunConsolidation(store, nil, nil, ConsolidateConfig{MaxRounds: 3, RuleBasedOnly: true})

	if result.TotalSuperseded < 1 {
		t.Errorf("expected at least 1 superseded from near-duplicate, got %d", result.TotalSuperseded)
	}
}

// Capability learnings are managed by save_capability auto-supersede.
// They must not be touched by the consolidation pipeline.
func TestRunConsolidation_ExcludesCapability(t *testing.T) {
	store := mustOpenStore(t)

	insertTestLearning(store, "reddit_fetch — Fetch Reddit posts from a subreddit", "cap")
	insertTestLearning(store, "reddit_fetch — Fetch Reddit posts from a subreddit daily", "cap")

	result := RunConsolidation(store, nil, nil, ConsolidateConfig{MaxRounds: 3, RuleBasedOnly: true})

	if result.TotalSuperseded != 0 {
		t.Errorf("capability must be excluded from consolidation, got %d superseded", result.TotalSuperseded)
	}
}

func TestRunConsolidation_DoesNotCrossCanonicalProjects(t *testing.T) {
	store := mustOpenStore(t)
	content := "The deployment pipeline always verifies the release artifact before publishing"
	for _, project := range []string{"alpha", "beta"} {
		if _, err := store.InsertLearning(&models.Learning{
			SessionID:        "session-" + project,
			Category:         "pattern",
			Content:          content,
			Project:          "/work/" + project,
			CanonicalProject: project,
			Confidence:       1,
			CreatedAt:        time.Now(),
			ModelUsed:        "test",
		}); err != nil {
			t.Fatalf("insert %s: %v", project, err)
		}
	}

	result := RunConsolidation(store, nil, nil, ConsolidateConfig{MaxRounds: 1, RuleBasedOnly: true})

	if result.TotalSuperseded != 0 {
		t.Fatalf("cross-project learnings must remain independent, got %d superseded", result.TotalSuperseded)
	}
}

func TestRunConsolidation_OnlyChecksScopesTouchedAfterWatermark(t *testing.T) {
	store := mustOpenStore(t)
	insert := func(project, content string) {
		if _, err := store.InsertLearning(&models.Learning{
			SessionID: "session-" + project, Category: "pattern", Content: content,
			Project: "/work/" + project, CanonicalProject: project,
			Confidence: 1, CreatedAt: time.Now(), ModelUsed: "test",
		}); err != nil {
			t.Fatalf("insert %s: %v", project, err)
		}
	}
	insert("alpha", "User prefers concise German answers with concrete evidence and direct code references")
	insert("beta", "The beta deployment uses a separate release pipeline with signed artifacts")
	watermark, err := store.GetMaxLearningID()
	if err != nil {
		t.Fatalf("watermark: %v", err)
	}
	insert("alpha", "User prefers concise German answers with concrete evidence and direct code references always")
	through, _ := store.GetMaxLearningID()

	result := RunConsolidation(store, nil, nil, ConsolidateConfig{
		MaxRounds: 1, RuleBasedOnly: true, AfterID: watermark, ThroughID: through,
	})

	if result.TotalChecked != 1 {
		t.Fatalf("expected only the one delta learning to be checked, got %d", result.TotalChecked)
	}
	if result.TotalSuperseded != 1 || result.BigramComparisons != 1 {
		t.Fatalf("unexpected incremental result: superseded=%d bigram_candidates=%d", result.TotalSuperseded, result.BigramComparisons)
	}
	beta, err := store.GetActiveLearnings("pattern", "beta", "", "", 0)
	if err != nil || len(beta) != 1 {
		t.Fatalf("untouched beta scope changed: len=%d err=%v", len(beta), err)
	}
}

func TestFindBigramDuplicates_UsesSparseExactCandidates(t *testing.T) {
	learnings := make([]models.Learning, 2000)
	for i := range learnings {
		learnings[i] = models.Learning{
			ID:      int64(i + 1),
			Content: fmt.Sprintf("token-%d-alpha token-%d-beta token-%d-gamma token-%d-delta shared ending words", i, i, i, i),
		}
	}

	dupes, comparisons := findBigramDuplicates(learnings, 0.85, 0)

	if len(dupes) != 0 {
		t.Fatalf("expected no duplicates, got %d", len(dupes))
	}
	if comparisons >= len(learnings)*2 {
		t.Fatalf("candidate filter regressed toward an all-pairs scan: %d comparisons for %d learnings", comparisons, len(learnings))
	}
}

func TestFindBigramDuplicates_DoesNotMissThresholdMatch(t *testing.T) {
	learnings := []models.Learning{
		{ID: 1, Content: "User prefers concise German answers with concrete evidence and direct code references"},
		{ID: 2, Content: "User prefers concise German answers with concrete evidence and direct code references always"},
		{ID: 3, Content: "Database backups are retained for seven days in encrypted object storage"},
	}

	dupes, _ := findBigramDuplicates(learnings, 0.85, 0)

	if dupes[1] != 2 {
		t.Fatalf("expected exact candidate filter to preserve duplicate 1 -> 2, got %v", dupes)
	}
}

// runEvolution must exclude capability from category processing.
func TestRunEvolution_ExcludesCapability(t *testing.T) {
	store := mustOpenStore(t)

	insertTestLearning(store, "reddit_fetch — Fetch Reddit posts", "cap")
	insertTestLearning(store, "reddit_fetch — Fetch Reddit posts v2", "cap")

	e := &Extractor{}
	checked, superseded := e.runEvolution(store, nil, nil, 0)

	if checked != 0 || superseded != 0 {
		t.Errorf("expected evolution to skip capability entirely, got checked=%d superseded=%d", checked, superseded)
	}
}
