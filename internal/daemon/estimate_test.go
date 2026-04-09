package daemon

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/carsteneu/yesmem/internal/models"
)

// --- EstimateExtractionCost ---

func TestEstimateExtractionCost_Empty(t *testing.T) {
	est := EstimateExtractionCost(nil, nil, "haiku")
	if est.Sessions != 0 || est.EstCostUSD != 0 {
		t.Errorf("empty sessions should give zero estimate, got %+v", est)
	}
}

func TestEstimateExtractionCost_Haiku(t *testing.T) {
	sessions := make([]models.Session, 10)
	est := EstimateExtractionCost(nil, sessions, "haiku")

	if est.Sessions != 10 {
		t.Errorf("expected 10 sessions, got %d", est.Sessions)
	}
	if est.TotalChunks != 10*AvgChunksPerSession {
		t.Errorf("expected %d chunks, got %d", 10*AvgChunksPerSession, est.TotalChunks)
	}
	if est.EstCostUSD <= 0 {
		t.Error("expected positive cost estimate")
	}
}

func TestEstimateExtractionCost_ModelPricingOrder(t *testing.T) {
	sessions := make([]models.Session, 5)
	haiku := EstimateExtractionCost(nil, sessions, "haiku")
	sonnet := EstimateExtractionCost(nil, sessions, "sonnet")
	opus := EstimateExtractionCost(nil, sessions, "opus")

	if haiku.EstCostUSD >= sonnet.EstCostUSD {
		t.Errorf("haiku ($%.4f) should be cheaper than sonnet ($%.4f)", haiku.EstCostUSD, sonnet.EstCostUSD)
	}
	if sonnet.EstCostUSD >= opus.EstCostUSD {
		t.Errorf("sonnet ($%.4f) should be cheaper than opus ($%.4f)", sonnet.EstCostUSD, opus.EstCostUSD)
	}
}

// --- modelPricing ---

func TestModelPricing(t *testing.T) {
	tests := []struct {
		model     string
		wantInput float64
	}{
		{"haiku", HaikuInputPerM},
		{"sonnet", SonnetInputPerM},
		{"opus", OpusInputPerM},
		{"unknown", HaikuInputPerM}, // defaults to haiku
	}
	for _, tt := range tests {
		input, _ := modelPricing(tt.model)
		if input != tt.wantInput {
			t.Errorf("modelPricing(%q) input=%f, want %f", tt.model, input, tt.wantInput)
		}
	}
}

// --- FormatCostEstimate ---

func TestFormatCostEstimate_Empty(t *testing.T) {
	result := FormatCostEstimate(CostEstimate{}, "api")
	if result != "No sessions need extraction." {
		t.Errorf("unexpected: %q", result)
	}
}

func TestFormatCostEstimate_API(t *testing.T) {
	est := CostEstimate{Sessions: 5, AvgChunks: 4, TotalChunks: 20, EstTokensInput: 164000, EstTokensOutput: 16000, EstCostUSD: 1.23}
	result := FormatCostEstimate(est, "api")
	if len(result) == 0 {
		t.Fatal("expected non-empty output")
	}
}

func TestFormatCostEstimate_CLI(t *testing.T) {
	est := CostEstimate{Sessions: 5, AvgChunks: 4, TotalChunks: 20, EstTokensInput: 164000, EstTokensOutput: 16000}
	result := FormatCostEstimate(est, "cli")
	if len(result) == 0 {
		t.Fatal("expected non-empty output")
	}
}

// --- FilterByMaxAge ---

func TestFilterByMaxAge_NoLimit(t *testing.T) {
	sessions := []models.Session{{ID: "a"}, {ID: "b"}}
	filtered := FilterByMaxAge(sessions, 0)
	if len(filtered) != 2 {
		t.Errorf("expected 2, got %d", len(filtered))
	}
}

func TestFilterByMaxAge_FiltersOld(t *testing.T) {
	now := time.Now()
	sessions := []models.Session{
		{ID: "recent", StartedAt: now.Add(-1 * 24 * time.Hour)},
		{ID: "old", StartedAt: now.Add(-60 * 24 * time.Hour)},
	}
	filtered := FilterByMaxAge(sessions, 7)
	if len(filtered) != 1 {
		t.Errorf("expected 1 recent session, got %d", len(filtered))
	}
	if filtered[0].ID != "recent" {
		t.Errorf("expected 'recent', got %q", filtered[0].ID)
	}
}

func TestFilterByMaxAge_AllRecent(t *testing.T) {
	now := time.Now()
	sessions := []models.Session{
		{ID: "a", StartedAt: now.Add(-1 * time.Hour)},
		{ID: "b", StartedAt: now.Add(-2 * time.Hour)},
	}
	filtered := FilterByMaxAge(sessions, 30)
	if len(filtered) != 2 {
		t.Errorf("expected all 2 sessions, got %d", len(filtered))
	}
}

// --- EstimateMonthlyFromDir ---

func TestEstimateMonthlyFromDir_EmptyDir(t *testing.T) {
	dir := t.TempDir()
	costs, count, err := EstimateMonthlyFromDir(dir)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if count != 0 || costs != nil {
		t.Errorf("expected zero sessions, got %d with %d cost entries", count, len(costs))
	}
}

func TestEstimateMonthlyFromDir_WithSessions(t *testing.T) {
	dir := t.TempDir()
	projDir := filepath.Join(dir, "project1")
	os.MkdirAll(projDir, 0755)

	// Create a recent .jsonl file
	f := filepath.Join(projDir, "session1.jsonl")
	os.WriteFile(f, make([]byte, 50000), 0644)

	// Create an old .jsonl file (should be excluded by 30-day cutoff)
	oldF := filepath.Join(projDir, "session2.jsonl")
	os.WriteFile(oldF, make([]byte, 30000), 0644)
	oldTime := time.Now().Add(-60 * 24 * time.Hour)
	os.Chtimes(oldF, oldTime, oldTime)

	costs, count, err := EstimateMonthlyFromDir(dir)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 recent session, got %d", count)
	}
	if len(costs) != 3 {
		t.Fatalf("expected 3 model costs (haiku/sonnet/opus), got %d", len(costs))
	}
	// Haiku should be cheapest
	if costs[0].CostUSD >= costs[2].CostUSD {
		t.Errorf("haiku ($%.4f) should be cheaper than opus ($%.4f)", costs[0].CostUSD, costs[2].CostUSD)
	}
}

func TestEstimateMonthlyFromDir_NonexistentDir(t *testing.T) {
	_, _, err := EstimateMonthlyFromDir("/nonexistent/path")
	if err == nil {
		t.Fatal("expected error for nonexistent dir")
	}
}
