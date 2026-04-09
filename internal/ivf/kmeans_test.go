package ivf

import (
	"math"
	"testing"
)

func TestKMeansBasic(t *testing.T) {
	// 3 clusters, 10 vectors each, fully orthogonal — robust against random init
	dim := 32
	var vectors [][]float32
	for i := 0; i < 10; i++ {
		v := make([]float32, dim)
		v[0] = 1.0
		v[1] = float32(i) * 0.001
		vectors = append(vectors, v)
	}
	for i := 0; i < 10; i++ {
		v := make([]float32, dim)
		v[10] = 1.0
		v[11] = float32(i) * 0.001
		vectors = append(vectors, v)
	}
	for i := 0; i < 10; i++ {
		v := make([]float32, dim)
		v[20] = 1.0
		v[21] = float32(i) * 0.001
		vectors = append(vectors, v)
	}

	centroids, assignments := KMeans(vectors, 3, 50)

	if len(centroids) != 3 {
		t.Fatalf("expected 3 centroids, got %d", len(centroids))
	}
	if len(assignments) != 30 {
		t.Fatalf("expected 30 assignments, got %d", len(assignments))
	}

	// Vectors in same group should have same assignment
	for i := 1; i < 10; i++ {
		if assignments[i] != assignments[0] {
			t.Errorf("cluster A: vector %d has assignment %d, expected %d", i, assignments[i], assignments[0])
		}
	}
	for i := 11; i < 20; i++ {
		if assignments[i] != assignments[10] {
			t.Errorf("cluster B: vector %d has assignment %d, expected %d", i, assignments[i], assignments[10])
		}
	}
	for i := 21; i < 30; i++ {
		if assignments[i] != assignments[20] {
			t.Errorf("cluster C: vector %d has assignment %d, expected %d", i, assignments[i], assignments[20])
		}
	}

	// All 3 groups should be different clusters
	if assignments[0] == assignments[10] || assignments[10] == assignments[20] || assignments[0] == assignments[20] {
		t.Errorf("3 groups should map to 3 different clusters: A=%d B=%d C=%d", assignments[0], assignments[10], assignments[20])
	}
}

func TestKMeansEdgeCases(t *testing.T) {
	// Empty input
	c, a := KMeans(nil, 3, 20)
	if c != nil || a != nil {
		t.Error("empty input should return nil")
	}

	// k > n: should clamp
	vectors := [][]float32{{1, 0}, {0, 1}}
	c, a = KMeans(vectors, 10, 20)
	if len(c) != 2 {
		t.Errorf("k clamped to n=2, got %d centroids", len(c))
	}
	if len(a) != 2 {
		t.Errorf("expected 2 assignments, got %d", len(a))
	}

	// k=1: everything in one cluster
	c, a = KMeans(vectors, 1, 20)
	if len(c) != 1 {
		t.Errorf("k=1 should give 1 centroid, got %d", len(c))
	}
	if a[0] != 0 || a[1] != 0 {
		t.Errorf("k=1: all should be cluster 0, got %v", a)
	}
}

func TestKMeansConverges(t *testing.T) {
	// 100 vectors in 2 clusters (high-dim)
	dim := 384
	vectors := make([][]float32, 100)
	for i := range vectors {
		vectors[i] = make([]float32, dim)
		if i < 50 {
			vectors[i][0] = 1.0
		} else {
			vectors[i][1] = 1.0
		}
		// Add small noise
		for d := 2; d < dim; d++ {
			vectors[i][d] = float32(i) * 0.0001
		}
	}

	centroids, assignments := KMeans(vectors, 2, 20)

	if len(centroids) != 2 {
		t.Fatalf("expected 2 centroids, got %d", len(centroids))
	}

	// First 50 should be one cluster, last 50 another
	for i := 1; i < 50; i++ {
		if assignments[i] != assignments[0] {
			t.Fatalf("vector %d should be same cluster as 0", i)
		}
	}
	for i := 51; i < 100; i++ {
		if assignments[i] != assignments[50] {
			t.Fatalf("vector %d should be same cluster as 50", i)
		}
	}
	if assignments[0] == assignments[50] {
		t.Error("two groups should be different clusters")
	}
}

func TestCosineSim(t *testing.T) {
	a := []float32{1, 0, 0}
	b := []float32{1, 0, 0}
	if sim := cosineSim(a, b); sim < 0.999 {
		t.Errorf("identical: expected ~1.0, got %f", sim)
	}

	c := []float32{0, 1, 0}
	if sim := cosineSim(a, c); math.Abs(float64(sim)) > 0.001 {
		t.Errorf("orthogonal: expected ~0.0, got %f", sim)
	}

	d := []float32{-1, 0, 0}
	if sim := cosineSim(a, d); sim > -0.999 {
		t.Errorf("opposite: expected ~-1.0, got %f", sim)
	}
}

func TestNormalize(t *testing.T) {
	v := []float32{3, 4, 0}
	normalize(v)
	norm := float64(v[0])*float64(v[0]) + float64(v[1])*float64(v[1]) + float64(v[2])*float64(v[2])
	if math.Abs(norm-1.0) > 0.001 {
		t.Errorf("expected unit length, got norm=%f", norm)
	}

	// Zero vector stays zero
	z := []float32{0, 0, 0}
	normalize(z)
	if z[0] != 0 || z[1] != 0 || z[2] != 0 {
		t.Error("zero vector should stay zero")
	}
}
