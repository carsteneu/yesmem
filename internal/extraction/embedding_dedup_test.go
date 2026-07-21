package extraction

import (
	"testing"

	"github.com/carsteneu/yesmem/internal/embedding"
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

func TestFindEmbeddingDuplicatesPreservesHigherTrustLearning(t *testing.T) {
	vec := []float32{0.1, 0.2, 0.3, 0.4, 0.5}
	learnings := []models.Learning{
		{ID: 1, Source: "user_stated", Importance: 3, Confidence: 0.8, Content: "older trusted learning"},
		{ID: 2, Source: "llm_extracted", Importance: 3, Confidence: 1, Content: "newer extracted learning with more detail"},
	}

	dupes := FindEmbeddingDuplicates(learnings, map[int64][]float32{1: vec, 2: vec}, 0.92)

	if dupes[2] != 1 {
		t.Fatalf("lower-trust newer learning must lose to higher-trust older learning, got %v", dupes)
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

func TestFindEmbeddingDuplicatesSince_OnlyComparesDirtyLearnings(t *testing.T) {
	learnings := make([]models.Learning, 101)
	vectors := make(map[int64][]float32, len(learnings))
	for i := range learnings {
		id := int64(i + 1)
		learnings[i] = models.Learning{ID: id}
		vectors[id] = []float32{float32(i + 1), 1, 0}
	}
	vectors[101] = append([]float32(nil), vectors[1]...)

	dupes, stats := findEmbeddingDuplicatesSince(learnings, vectors, 0.92, 100)

	if dupes[1] != 101 {
		t.Fatalf("expected dirty learning to replace matching old learning, got %v", dupes)
	}
	if stats.Comparisons > 100 {
		t.Fatalf("expected delta-sized comparison set, got %d comparisons", stats.Comparisons)
	}
}

func TestFindEmbeddingDuplicatesSince_StopsExactDistanceChecksEarly(t *testing.T) {
	const dimensions = 128
	learnings := make([]models.Learning, 101)
	vectors := make(map[int64][]float32, len(learnings))
	for i := range learnings {
		id := int64(i + 1)
		learnings[i] = models.Learning{ID: id}
		vectors[id] = make([]float32, dimensions)
		vectors[id][i%100] = 1
	}
	vectors[101][127] = 1

	_, stats := findEmbeddingDuplicatesSince(learnings, vectors, 0.92, 100)

	if stats.Comparisons != 100 {
		t.Fatalf("expected 100 delta comparisons, got %d", stats.Comparisons)
	}
	if stats.Dimensions >= stats.Comparisons*dimensions/4 {
		t.Fatalf("exact distance bound did not prune dimensions: checked %d of %d", stats.Dimensions, stats.Comparisons*dimensions)
	}
}

func TestLoadVectorsForLearnings_LoadsMultipleSQLiteBatches(t *testing.T) {
	store := mustOpenStore(t)
	ids := make([]int64, 501)
	for i := range ids {
		ids[i] = insertTestLearning(store, "substantive vector learning with unique batch token", "pattern")
		if _, err := store.DB().Exec("UPDATE learnings SET embedding_vector = ? WHERE id = ?", embedding.SerializeFloat32([]float32{float32(i), 1}), ids[i]); err != nil {
			t.Fatalf("store vector %d: %v", i, err)
		}
	}

	vectors, err := LoadVectorsForLearnings(store, ids)

	if err != nil {
		t.Fatalf("load vectors: %v", err)
	}
	if len(vectors) != len(ids) {
		t.Fatalf("expected %d vectors across the batch boundary, got %d", len(ids), len(vectors))
	}
}
