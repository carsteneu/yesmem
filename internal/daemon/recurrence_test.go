package daemon

import (
	"testing"

	"github.com/carsteneu/yesmem/internal/models"
)

func TestIsRecurrenceCandidate_True(t *testing.T) {
	c := models.LearningCluster{LearningCount: 5, AvgRecencyDays: 7}
	if !isRecurrenceCandidate(c) {
		t.Error("expected true for 5 learnings, 7 avg days")
	}
}

func TestIsRecurrenceCandidate_TooFew(t *testing.T) {
	c := models.LearningCluster{LearningCount: 2, AvgRecencyDays: 5}
	if isRecurrenceCandidate(c) {
		t.Error("expected false for only 2 learnings")
	}
}

func TestIsRecurrenceCandidate_TooOld(t *testing.T) {
	c := models.LearningCluster{LearningCount: 5, AvgRecencyDays: 30}
	if isRecurrenceCandidate(c) {
		t.Error("expected false for avg 30 days old")
	}
}

func TestContainsLabel(t *testing.T) {
	alerts := map[string]bool{
		`⚠ "proxy caching": some alert text`: true,
	}
	if !containsLabel(alerts, "proxy caching") {
		t.Error("expected to find label in alert set")
	}
	if containsLabel(alerts, "unknown label") {
		t.Error("should not find nonexistent label")
	}
}

func TestTemplateAlert(t *testing.T) {
	cluster := models.LearningCluster{Label: "test pattern", AvgRecencyDays: 5}
	learnings := make([]models.Learning, 4)
	result := templateAlert(cluster, learnings, 3)
	if result == "" {
		t.Fatal("expected non-empty alert")
	}
}

func TestTemplateAlert_MinDays(t *testing.T) {
	cluster := models.LearningCluster{Label: "fresh", AvgRecencyDays: 0.5}
	result := templateAlert(cluster, make([]models.Learning, 3), 2)
	if result == "" {
		t.Fatal("expected non-empty alert even with sub-day recency")
	}
}

func TestInterpretCluster_NilClient(t *testing.T) {
	cluster := models.LearningCluster{Label: "test", AvgRecencyDays: 3}
	learnings := make([]models.Learning, 3)
	result := interpretCluster(nil, cluster, learnings, 2)
	if result == "" {
		t.Fatal("nil client should produce template alert")
	}
}
