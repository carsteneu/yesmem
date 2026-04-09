package extraction

import (
	"testing"

	"github.com/carsteneu/yesmem/internal/models"
)

func TestFindEmbeddingDuplicates_IdenticalVectors(t *testing.T) {
	vec := []float32{0.1, 0.2, 0.3, 0.4, 0.5}

	learnings := []models.Learning{
		{ID: 1, Content: "first learning"},
		{ID: 2, Content: "second learning (duplicate)"},
		{ID: 3, Content: "third learning (different)"},
	}
	vectors := map[int64][]float32{
		1: vec,
		2: vec,
		3: {0.9, 0.8, 0.7, 0.6, 0.5},
	}

	dupes := FindEmbeddingDuplicates(learnings, vectors, 0.92)

	if len(dupes) != 1 {
		t.Fatalf("expected 1 duplicate pair, got %d", len(dupes))
	}
	if dupes[1] != 2 {
		t.Errorf("expected loser=1 winner=2, got loser=%d winner=%d", 1, dupes[1])
	}
}

func TestFindEmbeddingDuplicates_BelowThreshold(t *testing.T) {
	learnings := []models.Learning{
		{ID: 1, Content: "first"},
		{ID: 2, Content: "second"},
	}
	vectors := map[int64][]float32{
		1: {1.0, 0.0, 0.0},
		2: {0.0, 1.0, 0.0},
	}

	dupes := FindEmbeddingDuplicates(learnings, vectors, 0.92)
	if len(dupes) != 0 {
		t.Errorf("expected 0 duplicates for orthogonal vectors, got %d", len(dupes))
	}
}

func TestFindEmbeddingDuplicates_NoVectors(t *testing.T) {
	learnings := []models.Learning{
		{ID: 1, Content: "first"},
		{ID: 2, Content: "second"},
	}
	vectors := map[int64][]float32{}

	dupes := FindEmbeddingDuplicates(learnings, vectors, 0.92)
	if len(dupes) != 0 {
		t.Errorf("expected 0 duplicates when no vectors, got %d", len(dupes))
	}
}
