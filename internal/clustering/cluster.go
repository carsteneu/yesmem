package clustering

import "math"

// Document represents a single item with an embedding vector.
type Document struct {
	ID        string
	Content   string
	Embedding []float32
	Metadata  map[string]any
}

// Cluster is a group of semantically similar documents.
type Cluster struct {
	Documents []Document
	Centroid  []float32
}

// CosineSimilarity computes the cosine similarity between two vectors.
// Returns 0.0 for zero vectors or length mismatch.
func CosineSimilarity(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0.0
	}
	var dot, normA, normB float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		normA += float64(a[i]) * float64(a[i])
		normB += float64(b[i]) * float64(b[i])
	}
	if normA == 0 || normB == 0 {
		return 0.0
	}
	return dot / (math.Sqrt(normA) * math.Sqrt(normB))
}

// centroid computes the average embedding across all documents.
func centroid(docs []Document) []float32 {
	if len(docs) == 0 {
		return nil
	}
	dim := len(docs[0].Embedding)
	if dim == 0 {
		return nil
	}
	avg := make([]float32, dim)
	for _, d := range docs {
		for i, v := range d.Embedding {
			avg[i] += v
		}
	}
	n := float32(len(docs))
	for i := range avg {
		avg[i] /= n
	}
	return avg
}

// AgglomerativeClustering groups documents by cosine similarity.
// threshold is the minimum similarity to merge two clusters (0.85 recommended).
// Each document starts as its own cluster; the most similar pair above threshold
// is merged repeatedly until no pair qualifies.
func AgglomerativeClustering(docs []Document, threshold float64) []Cluster {
	if len(docs) == 0 {
		return nil
	}

	// Initialize: each doc is its own cluster.
	clusters := make([]Cluster, len(docs))
	for i, d := range docs {
		clusters[i] = Cluster{
			Documents: []Document{d},
			Centroid:  append([]float32(nil), d.Embedding...),
		}
	}

	for {
		if len(clusters) < 2 {
			break
		}

		// Find the most similar pair.
		bestSim := -1.0
		bestI, bestJ := -1, -1
		for i := 0; i < len(clusters); i++ {
			for j := i + 1; j < len(clusters); j++ {
				sim := CosineSimilarity(clusters[i].Centroid, clusters[j].Centroid)
				if sim > bestSim {
					bestSim = sim
					bestI, bestJ = i, j
				}
			}
		}

		if bestSim < threshold {
			break
		}

		// Merge j into i.
		merged := Cluster{
			Documents: append(clusters[bestI].Documents, clusters[bestJ].Documents...),
		}
		merged.Centroid = centroid(merged.Documents)
		clusters[bestI] = merged

		// Remove j (swap with last, shrink).
		clusters[bestJ] = clusters[len(clusters)-1]
		clusters = clusters[:len(clusters)-1]
	}

	return clusters
}

// FilterByMinSize returns only clusters with at least minSize documents.
func FilterByMinSize(clusters []Cluster, minSize int) []Cluster {
	var out []Cluster
	for _, c := range clusters {
		if len(c.Documents) >= minSize {
			out = append(out, c)
		}
	}
	return out
}
