package ivf

import (
	"math"
	"math/rand"
)

// KMeans runs Lloyd's algorithm: random init, cosine distance, maxIter iterations.
// Returns centroids (k × dim) and assignments (one cluster index per input vector).
func KMeans(vectors [][]float32, k, maxIter int) (centroids [][]float32, assignments []int) {
	n := len(vectors)
	if n == 0 || k <= 0 {
		return nil, nil
	}
	if k > n {
		k = n
	}
	dim := len(vectors[0])

	// k-means++ initialization: first centroid random, rest proportional to distance
	centroids = make([][]float32, k)
	centroids[0] = make([]float32, dim)
	copy(centroids[0], vectors[rand.Intn(n)])

	for c := 1; c < k; c++ {
		// Compute min distance to nearest existing centroid for each vector
		weights := make([]float64, n)
		var totalWeight float64
		for i, vec := range vectors {
			minDist := float64(2.0) // max cosine distance = 2
			for j := 0; j < c; j++ {
				dist := float64(1.0 - cosineSim(vec, centroids[j]))
				if dist < minDist {
					minDist = dist
				}
			}
			weights[i] = minDist * minDist
			totalWeight += weights[i]
		}
		// Weighted random selection
		r := rand.Float64() * totalWeight
		var cumulative float64
		chosen := 0
		for i, w := range weights {
			cumulative += w
			if cumulative >= r {
				chosen = i
				break
			}
		}
		centroids[c] = make([]float32, dim)
		copy(centroids[c], vectors[chosen])
	}

	assignments = make([]int, n)

	for iter := 0; iter < maxIter; iter++ {
		// Assign each vector to nearest centroid (cosine distance)
		changed := 0
		for i, vec := range vectors {
			best := 0
			bestSim := float32(-1)
			for c := 0; c < k; c++ {
				sim := cosineSim(vec, centroids[c])
				if sim > bestSim {
					bestSim = sim
					best = c
				}
			}
			if assignments[i] != best {
				changed++
			}
			assignments[i] = best
		}

		// Recompute centroids as mean of assigned vectors
		newCentroids := make([][]float32, k)
		counts := make([]int, k)
		for c := 0; c < k; c++ {
			newCentroids[c] = make([]float32, dim)
		}
		for i, vec := range vectors {
			c := assignments[i]
			counts[c]++
			for d := 0; d < dim; d++ {
				newCentroids[c][d] += vec[d]
			}
		}
		for c := 0; c < k; c++ {
			if counts[c] == 0 {
				// Empty cluster — reinitialize with random vector
				r := rand.Intn(n)
				copy(newCentroids[c], vectors[r])
			} else {
				for d := 0; d < dim; d++ {
					newCentroids[c][d] /= float32(counts[c])
				}
			}
			normalize(newCentroids[c])
		}
		centroids = newCentroids

		// Early stop if converged
		if changed == 0 {
			break
		}
	}

	return centroids, assignments
}

// cosineSim computes cosine similarity between two vectors.
func cosineSim(a, b []float32) float32 {
	var dot, normA, normB float64
	for i := range a {
		ai, bi := float64(a[i]), float64(b[i])
		dot += ai * bi
		normA += ai * ai
		normB += bi * bi
	}
	denom := math.Sqrt(normA) * math.Sqrt(normB)
	if denom == 0 {
		return 0
	}
	return float32(dot / denom)
}

// normalize scales a vector to unit length in-place.
func normalize(v []float32) {
	var norm float64
	for _, x := range v {
		norm += float64(x) * float64(x)
	}
	if norm == 0 {
		return
	}
	scale := float32(1.0 / math.Sqrt(norm))
	for i := range v {
		v[i] *= scale
	}
}
