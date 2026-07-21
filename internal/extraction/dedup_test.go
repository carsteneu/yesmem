package extraction

import (
	"testing"
	"time"

	"github.com/carsteneu/yesmem/internal/models"
)

func TestIsSubstanzlos(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"", true},
		{"short", true},
		{"Dies ist ein valides Learning", false},
		{`{"key": "value"}`, true},
		{"```go\nfoo()```", true},
		{"Nur zwei", true},
		{"User bevorzugt Deutsch, locker und direkt", false},
		{"[{\"array\": true}]", true},
	}
	for _, tt := range tests {
		got := IsSubstanzlos(tt.input)
		if got != tt.want {
			t.Errorf("IsSubstanzlos(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

func TestBigramJaccard(t *testing.T) {
	// Identical → 1.0
	if j := BigramJaccard("User bevorzugt Deutsch", "User bevorzugt Deutsch"); j != 1.0 {
		t.Errorf("identical: got %f", j)
	}
	// Near-duplicate — different word order shares some bigrams
	j := BigramJaccard("User bevorzugt Deutsch, locker", "Sprache: Deutsch, User bevorzugt locker")
	if j < 0.1 || j > 0.5 {
		t.Errorf("near-dup: got %f, expected between 0.1 and 0.5", j)
	}
	// Actual near-duplicate (same words, slight variation) → moderate-high
	j = BigramJaccard("User bevorzugt Deutsch locker", "User bevorzugt Deutsch und locker")
	if j < 0.3 {
		t.Errorf("close near-dup: got %f, expected > 0.3", j)
	}
	// Completely different → low
	j = BigramJaccard("Go ist schnell", "Python hat gute Libraries")
	if j > 0.2 {
		t.Errorf("different: got %f, expected < 0.2", j)
	}
	// Both empty
	if j := BigramJaccard("", ""); j != 1.0 {
		t.Errorf("both empty: got %f, want 1.0", j)
	}
	// One empty
	if j := BigramJaccard("some words here", ""); j != 0.0 {
		t.Errorf("one empty: got %f, want 0.0", j)
	}
}

func TestPreferDuplicateWinnerUsesQualityRanking(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name string
		a    models.Learning
		b    models.Learning
	}{
		{
			name: "trust level before all other signals",
			a:    models.Learning{ID: 1, Source: "user_stated", Importance: 3, Confidence: 0.1, Content: "short trusted learning", CreatedAt: now.Add(-time.Hour)},
			b:    models.Learning{ID: 2, Source: "llm_extracted", Importance: 3, Confidence: 1, Content: "much longer automatically extracted learning with more detail", CreatedAt: now},
		},
		{
			name: "explicit source within the same trust level",
			a:    models.Learning{ID: 1, Source: "user_stated", Importance: 1, Confidence: 0.1, Content: "short user learning", CreatedAt: now.Add(-time.Hour)},
			b:    models.Learning{ID: 2, Source: "llm_extracted", Importance: 3, Confidence: 1, Content: "longer automatically extracted learning with more detail", CreatedAt: now},
		},
		{
			name: "confidence after trust and source",
			a:    models.Learning{ID: 1, Source: "llm_extracted", Importance: 3, Confidence: 0.9, Content: "short confident learning", CreatedAt: now.Add(-time.Hour)},
			b:    models.Learning{ID: 2, Source: "llm_extracted", Importance: 3, Confidence: 0.7, Content: "longer but less confident learning with detail", CreatedAt: now},
		},
		{
			name: "more specific content after confidence",
			a:    models.Learning{ID: 1, Source: "llm_extracted", Importance: 3, Confidence: 0.8, Content: "deployments require signed artifacts and checksum verification", CreatedAt: now.Add(-time.Hour)},
			b:    models.Learning{ID: 2, Source: "llm_extracted", Importance: 3, Confidence: 0.8, Content: "deployments require signed artifacts", CreatedAt: now},
		},
		{
			name: "freshness only as final tie breaker",
			a:    models.Learning{ID: 1, Source: "llm_extracted", Importance: 3, Confidence: 0.8, Content: "identical learning content", CreatedAt: now},
			b:    models.Learning{ID: 2, Source: "llm_extracted", Importance: 3, Confidence: 0.8, Content: "identical learning content", CreatedAt: now.Add(-time.Hour)},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !preferDuplicateWinner(tt.a, tt.b) {
				t.Fatalf("expected learning %d to outrank %d", tt.a.ID, tt.b.ID)
			}
			if preferDuplicateWinner(tt.b, tt.a) {
				t.Fatalf("ranking must not prefer learning %d over %d", tt.b.ID, tt.a.ID)
			}
		})
	}
}

func TestFindBigramDuplicatesPreservesHigherTrustLearning(t *testing.T) {
	learnings := []models.Learning{
		{ID: 1, Source: "user_stated", Importance: 3, Confidence: 0.8, Content: "User prefers concise German answers with concrete evidence and direct code references"},
		{ID: 2, Source: "llm_extracted", Importance: 3, Confidence: 1, Content: "User prefers concise German answers with concrete evidence and direct code references always"},
	}

	dupes, _ := findBigramDuplicates(learnings, 0.85, 0)

	if dupes[2] != 1 {
		t.Fatalf("lower-trust newer learning must lose to higher-trust older learning, got %v", dupes)
	}
}
