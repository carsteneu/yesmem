package extraction

import (
	"database/sql"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/carsteneu/yesmem/internal/embedding"
	"github.com/carsteneu/yesmem/internal/models"
)

// FindEmbeddingDuplicates finds near-duplicate learnings using cosine similarity
// on their embedding vectors. Returns map[loserID]winnerID.
// The quality ranking shared with lexical dedup chooses the winner.
func FindEmbeddingDuplicates(learnings []models.Learning, vectors map[int64][]float32, threshold float64) map[int64]int64 {
	dupes, _ := findEmbeddingDuplicatesSince(learnings, vectors, threshold, 0)
	return dupes
}

type embeddingDuplicateStats struct {
	Comparisons int
	Dimensions  int
}

func findEmbeddingDuplicatesSince(learnings []models.Learning, vectors map[int64][]float32, threshold float64, afterID int64) (map[int64]int64, embeddingDuplicateStats) {
	groups := newDuplicateGroups(learnings)
	stats := embeddingDuplicateStats{}
	normalized := make(map[int64][]float64, len(vectors))
	dimensions := 0
	for id, vector := range vectors {
		norm := 0.0
		for _, value := range vector {
			norm += float64(value) * float64(value)
		}
		if norm == 0 {
			continue
		}
		if dimensions == 0 {
			dimensions = len(vector)
		}
		if len(vector) != dimensions {
			continue
		}
		normalizedVector := make([]float64, len(vector))
		scale := 1 / math.Sqrt(norm)
		for i, value := range vector {
			normalizedVector[i] = float64(value) * scale
		}
		normalized[id] = normalizedVector
	}
	order := make([]int, dimensions)
	means := make([]float64, dimensions)
	for _, vector := range normalized {
		for i, value := range vector {
			means[i] += value
		}
	}
	if len(normalized) > 0 {
		for i := range means {
			means[i] /= float64(len(normalized))
		}
	}
	variances := make([]float64, dimensions)
	for _, vector := range normalized {
		for i, value := range vector {
			delta := value - means[i]
			variances[i] += delta * delta
		}
	}
	for i := range order {
		order[i] = i
	}
	sort.Slice(order, func(i, j int) bool { return variances[order[i]] > variances[order[j]] })

	for i := 0; i < len(learnings); i++ {
		vecA, okA := normalized[learnings[i].ID]
		if !okA {
			continue
		}
		for j := i + 1; j < len(learnings); j++ {
			if afterID > 0 && learnings[i].ID <= afterID && learnings[j].ID <= afterID {
				continue
			}
			vecB, okB := normalized[learnings[j].ID]
			if !okB {
				continue
			}
			stats.Comparisons++
			match, checkedDimensions := normalizedCosineAtLeast(vecA, vecB, order, threshold)
			stats.Dimensions += checkedDimensions
			if match {
				groups.add(learnings[i].ID, learnings[j].ID)
			}
		}
	}
	return groups.links(), stats
}

func normalizedCosineAtLeast(a, b []float64, order []int, threshold float64) (bool, int) {
	maxDistanceSquared := 2 - 2*threshold
	distanceSquared := 0.0
	for checked, dimension := range order {
		delta := a[dimension] - b[dimension]
		distanceSquared += delta * delta
		if distanceSquared > maxDistanceSquared+1e-12 {
			return false, checked + 1
		}
	}
	return distanceSquared <= maxDistanceSquared+1e-12, len(order)
}

// LoadVectorsForLearnings loads embedding vectors for a set of learnings from the DB.
// Returns map[learningID]vector. Learnings without embeddings are omitted.
func LoadVectorsForLearnings(store interface{ DB() *sql.DB }, ids []int64) (map[int64][]float32, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	result := make(map[int64][]float32, len(ids))
	const batchSize = 500
	for start := 0; start < len(ids); start += batchSize {
		end := min(start+batchSize, len(ids))
		args := make([]any, end-start)
		placeholders := make([]string, end-start)
		for i, id := range ids[start:end] {
			args[i] = id
			placeholders[i] = "?"
		}
		rows, err := store.DB().Query(fmt.Sprintf(
			`SELECT id, embedding_vector FROM learnings WHERE id IN (%s) AND embedding_vector IS NOT NULL`,
			strings.Join(placeholders, ","),
		), args...)
		if err != nil {
			return nil, fmt.Errorf("load learning vectors: %w", err)
		}
		for rows.Next() {
			var id int64
			var blob []byte
			if err := rows.Scan(&id, &blob); err != nil {
				rows.Close()
				return nil, fmt.Errorf("scan learning vector: %w", err)
			}
			if len(blob) > 0 {
				result[id] = embedding.DeserializeFloat32(blob)
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, fmt.Errorf("iterate learning vectors: %w", err)
		}
		rows.Close()
	}
	return result, nil
}
