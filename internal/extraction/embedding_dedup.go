package extraction

import (
	"database/sql"
	"math"

	"github.com/carsteneu/yesmem/internal/embedding"
	"github.com/carsteneu/yesmem/internal/models"
)

// FindEmbeddingDuplicates finds near-duplicate learnings using cosine similarity
// on their embedding vectors. Returns map[loserID]winnerID.
// Higher ID (newer) wins when similarity exceeds threshold.
func FindEmbeddingDuplicates(learnings []models.Learning, vectors map[int64][]float32, threshold float64) map[int64]int64 {
	dupes := make(map[int64]int64)

	for i := 0; i < len(learnings); i++ {
		vecA, okA := vectors[learnings[i].ID]
		if !okA || len(vecA) == 0 {
			continue
		}
		if _, alreadyLoser := dupes[learnings[i].ID]; alreadyLoser {
			continue
		}

		for j := i + 1; j < len(learnings); j++ {
			vecB, okB := vectors[learnings[j].ID]
			if !okB || len(vecB) == 0 {
				continue
			}
			if _, alreadyLoser := dupes[learnings[j].ID]; alreadyLoser {
				continue
			}

			sim := cosineSimFloat32(vecA, vecB)
			if sim >= threshold {
				if learnings[i].ID > learnings[j].ID {
					dupes[learnings[j].ID] = learnings[i].ID
				} else {
					dupes[learnings[i].ID] = learnings[j].ID
				}
			}
		}
	}
	return dupes
}

// cosineSimFloat32 computes cosine similarity between two float32 vectors.
func cosineSimFloat32(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
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
	return dot / denom
}

// LoadVectorsForLearnings loads embedding vectors for a set of learnings from the DB.
// Returns map[learningID]vector. Learnings without embeddings are omitted.
func LoadVectorsForLearnings(store interface{ DB() *sql.DB }, ids []int64) map[int64][]float32 {
	if len(ids) == 0 {
		return nil
	}
	result := make(map[int64][]float32)
	for _, id := range ids {
		var blob []byte
		err := store.DB().QueryRow(
			`SELECT embedding_vector FROM learnings WHERE id = ? AND embedding_vector IS NOT NULL`, id,
		).Scan(&blob)
		if err != nil || len(blob) == 0 {
			continue
		}
		result[id] = embedding.DeserializeFloat32(blob)
	}
	return result
}
