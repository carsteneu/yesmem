package clustering

import (
	"math"
	"testing"
)

func TestCosineSimilarity(t *testing.T) {
	t.Run("identical vectors", func(t *testing.T) {
		v := []float32{1, 2, 3}
		sim := CosineSimilarity(v, v)
		if math.Abs(sim-1.0) > 1e-6 {
			t.Errorf("expected 1.0, got %f", sim)
		}
	})

	t.Run("orthogonal vectors", func(t *testing.T) {
		a := []float32{1, 0, 0}
		b := []float32{0, 1, 0}
		sim := CosineSimilarity(a, b)
		if math.Abs(sim) > 1e-6 {
			t.Errorf("expected 0.0, got %f", sim)
		}
	})

	t.Run("zero vector", func(t *testing.T) {
		a := []float32{0, 0, 0}
		b := []float32{1, 2, 3}
		sim := CosineSimilarity(a, b)
		if sim != 0.0 {
			t.Errorf("expected 0.0 for zero vector, got %f", sim)
		}
	})

	t.Run("length mismatch", func(t *testing.T) {
		a := []float32{1, 2}
		b := []float32{1, 2, 3}
		sim := CosineSimilarity(a, b)
		if sim != 0.0 {
			t.Errorf("expected 0.0 for length mismatch, got %f", sim)
		}
	})
}

func TestAgglomerativeClustering(t *testing.T) {
	// Two similar docs (close embeddings) and one different.
	docs := []Document{
		{ID: "a", Embedding: []float32{1, 0, 0}},
		{ID: "b", Embedding: []float32{0.95, 0.05, 0}},
		{ID: "c", Embedding: []float32{0, 0, 1}},
	}

	clusters := AgglomerativeClustering(docs, 0.85)

	if len(clusters) != 2 {
		t.Fatalf("expected 2 clusters, got %d", len(clusters))
	}

	// Find which cluster has 2 docs.
	var big, small *Cluster
	for i := range clusters {
		if len(clusters[i].Documents) == 2 {
			big = &clusters[i]
		} else if len(clusters[i].Documents) == 1 {
			small = &clusters[i]
		}
	}
	if big == nil || small == nil {
		t.Fatalf("expected one cluster of size 2 and one of size 1, got sizes %d and %d",
			len(clusters[0].Documents), len(clusters[1].Documents))
	}

	// The singleton should be doc "c".
	if small.Documents[0].ID != "c" {
		t.Errorf("expected singleton to be doc 'c', got '%s'", small.Documents[0].ID)
	}
}

func TestClusteringMinSize(t *testing.T) {
	docs := []Document{
		{ID: "a", Embedding: []float32{1, 0, 0}},
		{ID: "b", Embedding: []float32{0.95, 0.05, 0}},
		{ID: "c", Embedding: []float32{0, 0, 1}},
	}

	clusters := AgglomerativeClustering(docs, 0.85)
	filtered := FilterByMinSize(clusters, 2)

	if len(filtered) != 1 {
		t.Fatalf("expected 1 cluster after filtering, got %d", len(filtered))
	}
	if len(filtered[0].Documents) != 2 {
		t.Errorf("expected cluster of size 2, got %d", len(filtered[0].Documents))
	}
}

func TestEmptyInput(t *testing.T) {
	clusters := AgglomerativeClustering(nil, 0.85)
	if len(clusters) != 0 {
		t.Errorf("expected 0 clusters for empty input, got %d", len(clusters))
	}
}

func TestSingleDoc(t *testing.T) {
	docs := []Document{
		{ID: "only", Embedding: []float32{1, 2, 3}},
	}
	clusters := AgglomerativeClustering(docs, 0.85)
	if len(clusters) != 1 {
		t.Fatalf("expected 1 cluster, got %d", len(clusters))
	}
	if len(clusters[0].Documents) != 1 {
		t.Errorf("expected cluster of size 1, got %d", len(clusters[0].Documents))
	}
	if clusters[0].Documents[0].ID != "only" {
		t.Errorf("expected doc ID 'only', got '%s'", clusters[0].Documents[0].ID)
	}
}
