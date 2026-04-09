package briefing

import (
	"testing"
	"time"

	"github.com/carsteneu/yesmem/internal/models"
)

func TestFadeFilter_RemovesLowScoreLearnings(t *testing.T) {
	now := time.Now()
	learnings := []models.Learning{
		{ID: 1, Category: "pattern", Content: "fresh", CreatedAt: now, Score: 1.5},
		{ID: 2, Category: "pattern", Content: "faded", CreatedAt: now.AddDate(0, -6, 0), Score: 0.05},
		{ID: 3, Category: "gotcha", Content: "important", CreatedAt: now, Score: 2.0},
	}

	filtered := filterFaded(learnings, 0.1)
	if len(filtered) != 2 {
		t.Errorf("expected 2 learnings after fade filter, got %d", len(filtered))
	}
	for _, l := range filtered {
		if l.ID == 2 {
			t.Error("faded learning (ID=2) should have been filtered out")
		}
	}
}

func TestFadeFilter_KeepsUnfinished(t *testing.T) {
	learnings := []models.Learning{
		{ID: 1, Category: "unfinished", Content: "open task", Score: 0.05},
	}
	filtered := filterFaded(learnings, 0.1)
	if len(filtered) != 1 {
		t.Error("unfinished items should never be faded")
	}
}

func TestFadeFilter_KeepsPivotMoments(t *testing.T) {
	learnings := []models.Learning{
		{ID: 1, Category: "pivot_moment", Content: "key moment", Score: 0.05},
	}
	filtered := filterFaded(learnings, 0.1)
	if len(filtered) != 1 {
		t.Error("pivot_moment items should never be faded")
	}
}

func TestFadeFilter_ZeroThresholdKeepsAll(t *testing.T) {
	learnings := []models.Learning{
		{ID: 1, Category: "pattern", Content: "low", Score: 0.01},
	}
	filtered := filterFaded(learnings, 0)
	if len(filtered) != 1 {
		t.Error("zero threshold should keep all learnings")
	}
}
