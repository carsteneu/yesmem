
package locomo

import (
	"math"
	"testing"
)

func TestAggregate(t *testing.T) {
	results := []JudgeResult{
		// Category 1: Single-hop (2 correct, 1 wrong)
		{Question: "q1", Gold: "a1", Generated: "a1", Category: 1, Label: "CORRECT", Score: 1},
		{Question: "q2", Gold: "a2", Generated: "a2", Category: 1, Label: "CORRECT", Score: 1},
		{Question: "q3", Gold: "a3", Generated: "wrong", Category: 1, Label: "WRONG", Score: 0},
		// Category 2: Multi-hop (1 correct, 1 wrong)
		{Question: "q4", Gold: "a4", Generated: "a4", Category: 2, Label: "CORRECT", Score: 1},
		{Question: "q5", Gold: "a5", Generated: "wrong", Category: 2, Label: "WRONG", Score: 0},
		// Category 3: Temporal (1 correct, 0 wrong)
		{Question: "q6", Gold: "a6", Generated: "a6", Category: 3, Label: "CORRECT", Score: 1},
		// Category 4: Open-domain (1 correct, 1 wrong)
		{Question: "q7", Gold: "a7", Generated: "a7", Category: 4, Label: "CORRECT", Score: 1},
		{Question: "q8", Gold: "a8", Generated: "wrong", Category: 4, Label: "WRONG", Score: 0},
		// Extra category 3 item
		{Question: "q9", Gold: "a9", Generated: "wrong", Category: 3, Label: "WRONG", Score: 0},
	}

	report := Aggregate(results)

	if len(report.Categories) != 4 {
		t.Fatalf("expected 4 categories, got %d", len(report.Categories))
	}

	// Category 1: 2/3 = 0.6667
	cat1 := report.Categories[0]
	if cat1.Category != 1 {
		t.Errorf("expected category 1, got %d", cat1.Category)
	}
	if cat1.Count != 3 {
		t.Errorf("cat1: expected count 3, got %d", cat1.Count)
	}
	if cat1.Correct != 2 {
		t.Errorf("cat1: expected correct 2, got %d", cat1.Correct)
	}
	if math.Abs(cat1.Score-2.0/3.0) > 0.001 {
		t.Errorf("cat1: expected score ~0.667, got %f", cat1.Score)
	}

	// Category 2: 1/2 = 0.5
	cat2 := report.Categories[1]
	if cat2.Count != 2 {
		t.Errorf("cat2: expected count 2, got %d", cat2.Count)
	}
	if cat2.Correct != 1 {
		t.Errorf("cat2: expected correct 1, got %d", cat2.Correct)
	}
	if math.Abs(cat2.Score-0.5) > 0.001 {
		t.Errorf("cat2: expected score 0.5, got %f", cat2.Score)
	}

	// Category 3: 1/2 = 0.5
	cat3 := report.Categories[2]
	if cat3.Count != 2 {
		t.Errorf("cat3: expected count 2, got %d", cat3.Count)
	}
	if cat3.Correct != 1 {
		t.Errorf("cat3: expected correct 1, got %d", cat3.Correct)
	}
	if math.Abs(cat3.Score-0.5) > 0.001 {
		t.Errorf("cat3: expected score 0.5, got %f", cat3.Score)
	}

	// Category 4: 1/2 = 0.5
	cat4 := report.Categories[3]
	if cat4.Count != 2 {
		t.Errorf("cat4: expected count 2, got %d", cat4.Count)
	}
	if cat4.Correct != 1 {
		t.Errorf("cat4: expected correct 1, got %d", cat4.Correct)
	}
	if math.Abs(cat4.Score-0.5) > 0.001 {
		t.Errorf("cat4: expected score 0.5, got %f", cat4.Score)
	}

	// Overall: 5/9 = 0.5556
	if report.TotalCount != 9 {
		t.Errorf("expected total count 9, got %d", report.TotalCount)
	}
	if report.TotalCorrect != 5 {
		t.Errorf("expected total correct 5, got %d", report.TotalCorrect)
	}
	if math.Abs(report.OverallMean-5.0/9.0) > 0.001 {
		t.Errorf("expected overall mean ~0.556, got %f", report.OverallMean)
	}
}

func TestMultiRunStats(t *testing.T) {
	scores := []float64{0.72, 0.68, 0.75, 0.64, 0.75}

	stats := ComputeRunStats(scores)

	if stats.Runs != 5 {
		t.Errorf("expected 5 runs, got %d", stats.Runs)
	}

	// Mean = (0.72 + 0.68 + 0.75 + 0.64 + 0.75) / 5 = 3.54 / 5 = 0.708
	if math.Abs(stats.Mean-0.708) > 0.001 {
		t.Errorf("expected mean ~0.708, got %f", stats.Mean)
	}

	if stats.Stddev <= 0 {
		t.Errorf("expected stddev > 0, got %f", stats.Stddev)
	}

	if stats.Min != 0.64 {
		t.Errorf("expected min 0.64, got %f", stats.Min)
	}

	if stats.Max != 0.75 {
		t.Errorf("expected max 0.75, got %f", stats.Max)
	}
}

func TestAggregateExcludesCategory5(t *testing.T) {
	results := []JudgeResult{
		{Question: "q1", Gold: "a1", Generated: "a1", Category: 1, Label: "CORRECT", Score: 1},
		{Question: "q2", Gold: "a2", Generated: "a2", Category: 5, Label: "CORRECT", Score: 1},
		{Question: "q3", Gold: "a3", Generated: "wrong", Category: 5, Label: "WRONG", Score: 0},
	}

	report := Aggregate(results)

	// Only category 1 should be present
	if len(report.Categories) != 1 {
		t.Fatalf("expected 1 category, got %d", len(report.Categories))
	}
	if report.Categories[0].Category != 1 {
		t.Errorf("expected category 1, got %d", report.Categories[0].Category)
	}

	// Totals should only count category 1
	if report.TotalCount != 1 {
		t.Errorf("expected total count 1, got %d", report.TotalCount)
	}
	if report.TotalCorrect != 1 {
		t.Errorf("expected total correct 1, got %d", report.TotalCorrect)
	}
	if math.Abs(report.OverallMean-1.0) > 0.001 {
		t.Errorf("expected overall mean 1.0, got %f", report.OverallMean)
	}
}
