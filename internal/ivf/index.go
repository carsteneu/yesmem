package ivf

import (
	"context"
	"database/sql"
	"encoding/binary"
	"fmt"
	"math"
	"sort"
	"sync"

	"github.com/carsteneu/yesmem/internal/embedding"
)

// IVFIndex is an Inverted File Index for approximate nearest neighbor search.
// Centroids and cluster membership lists live in RAM (~400KB for k=100, dim=384).
// Vectors are loaded lazily from SQLite on each search — no RAM duplication.
// Implements embedding.IVFSearcher interface.
type IVFIndex struct {
	Centroids [][]float32  // k × dim
	Clusters  [][]uint64   // k × variable — Learning-IDs per cluster
	K         int
	Dim       int
	NProbe    int // how many clusters to probe during search (default: 5)

	db *sql.DB
	mu sync.RWMutex
}

// Build creates a new IVF index from all embedded vectors in the learnings table.
// k=0 means auto (sqrt(n)), nProbe=0 means default (5).
func Build(db *sql.DB, dim, k, nProbe int) (*IVFIndex, error) {
	if nProbe <= 0 {
		nProbe = 5
	}

	// Load all vectors from SQLite
	ids, vectors, err := loadAllVectors(db, dim)
	if err != nil {
		return nil, fmt.Errorf("load vectors: %w", err)
	}
	if len(vectors) == 0 {
		return &IVFIndex{K: 0, Dim: dim, NProbe: nProbe, db: db}, nil
	}

	// Auto k: sqrt(n), capped at 100 to keep build time reasonable
	if k <= 0 {
		k = int(math.Sqrt(float64(len(vectors))))
		if k < 1 {
			k = 1
		}
	}
	if k > 100 {
		k = 100
	}
	if k > len(vectors) {
		k = len(vectors)
	}

	// Run k-means (5 iterations sufficient for good clusters)
	centroids, assignments := KMeans(vectors, k, 5)

	// Build cluster membership lists
	clusters := make([][]uint64, k)
	for i := range clusters {
		clusters[i] = make([]uint64, 0)
	}
	for i, clusterIdx := range assignments {
		clusters[clusterIdx] = append(clusters[clusterIdx], ids[i])
	}

	return &IVFIndex{
		Centroids: centroids,
		Clusters:  clusters,
		K:         k,
		Dim:       dim,
		NProbe:    nProbe,
		db:        db,
	}, nil
}

// Search finds the topK most similar vectors using IVF probe.
// 1. Find NProbe nearest centroids
// 2. Collect candidate IDs from those clusters
// 3. Load candidate vectors lazily from SQLite
// 4. Brute-force cosine over candidates, return top-K
func (idx *IVFIndex) Search(ctx context.Context, query []float32, topK int) ([]embedding.SearchResult, error) {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	if idx.K == 0 || len(idx.Centroids) == 0 || topK <= 0 {
		return nil, nil
	}

	// Step 1: Find NProbe nearest centroids
	type centroidDist struct {
		idx int
		sim float32
	}
	dists := make([]centroidDist, len(idx.Centroids))
	for i, c := range idx.Centroids {
		dists[i] = centroidDist{idx: i, sim: cosineSim(query, c)}
	}
	sort.Slice(dists, func(i, j int) bool { return dists[i].sim > dists[j].sim })

	nProbe := idx.NProbe
	if nProbe > len(dists) {
		nProbe = len(dists)
	}

	// Step 2: Collect candidate IDs from top clusters
	var candidateIDs []uint64
	for i := 0; i < nProbe; i++ {
		candidateIDs = append(candidateIDs, idx.Clusters[dists[i].idx]...)
	}
	if len(candidateIDs) == 0 {
		return nil, nil
	}

	// Step 3: Load candidate vectors lazily from SQLite
	candidates, err := loadVectorsByIDs(ctx, idx.db, candidateIDs)
	if err != nil {
		return nil, fmt.Errorf("load candidates: %w", err)
	}

	// Step 4: Brute-force cosine over candidates
	type scored struct {
		id       uint64
		content  string
		project  string
		category string
		sim      float32
	}
	results := make([]scored, 0, len(candidates))
	for _, c := range candidates {
		if len(c.vec) != len(query) {
			continue
		}
		results = append(results, scored{
			id: c.id, content: c.content, project: c.project, category: c.category,
			sim: cosineSim(query, c.vec),
		})
	}

	sort.Slice(results, func(i, j int) bool { return results[i].sim > results[j].sim })
	if topK > len(results) {
		topK = len(results)
	}

	out := make([]embedding.SearchResult, topK)
	for i := 0; i < topK; i++ {
		out[i] = embedding.SearchResult{
			ID:         fmt.Sprintf("%d", results[i].id),
			Content:    results[i].content,
			Similarity: results[i].sim,
			Metadata: map[string]string{
				"category": results[i].category,
				"project":  results[i].project,
			},
		}
	}
	return out, nil
}

// Add assigns a new vector to the nearest centroid's cluster.
// No rebuild — just appends to the cluster list.
func (idx *IVFIndex) Add(id uint64, vec []float32) {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	if idx.K == 0 || len(idx.Centroids) == 0 {
		return
	}

	// Find nearest centroid
	best := 0
	bestSim := float32(-1)
	for i, c := range idx.Centroids {
		sim := cosineSim(vec, c)
		if sim > bestSim {
			bestSim = sim
			best = i
		}
	}

	idx.Clusters[best] = append(idx.Clusters[best], id)
}

// Remove deletes an ID from its cluster.
func (idx *IVFIndex) Remove(id uint64) {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	for c := range idx.Clusters {
		for i, cid := range idx.Clusters[c] {
			if cid == id {
				idx.Clusters[c] = append(idx.Clusters[c][:i], idx.Clusters[c][i+1:]...)
				return
			}
		}
	}
}

// NeedsRebuild returns true if any cluster has more than 3× the average size.
func (idx *IVFIndex) NeedsRebuild() bool {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	if idx.K == 0 {
		return false
	}

	total := 0
	for _, c := range idx.Clusters {
		total += len(c)
	}
	avg := total / idx.K
	if avg == 0 {
		return false
	}

	for _, c := range idx.Clusters {
		if len(c) > avg*3 {
			return true
		}
	}
	return false
}

// IsStale returns true if the index covers significantly fewer vectors than dbCount.
// Threshold: >2% gap triggers rebuild.
func (idx *IVFIndex) IsStale(dbCount int) bool {
	total := idx.TotalVectors()
	if total >= dbCount {
		return false
	}
	gap := dbCount - total
	return gap*100 > dbCount*2
}

// TotalVectors returns the total number of indexed vectors.
func (idx *IVFIndex) TotalVectors() int {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	total := 0
	for _, c := range idx.Clusters {
		total += len(c)
	}
	return total
}

// SetDB sets the database connection for lazy vector loading.
func (idx *IVFIndex) SetDB(db *sql.DB) {
	idx.db = db
}

// --- Internal helpers ---

type vectorRow struct {
	id       uint64
	content  string
	project  string
	category string
	vec      []float32
}

func loadAllVectors(db *sql.DB, dim int) ([]uint64, [][]float32, error) {
	rows, err := db.Query(
		`SELECT id, embedding_vector FROM learnings WHERE embedding_vector IS NOT NULL AND superseded_by IS NULL`)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()

	var ids []uint64
	var vectors [][]float32
	for rows.Next() {
		var id uint64
		var blob []byte
		if err := rows.Scan(&id, &blob); err != nil {
			return nil, nil, err
		}
		vec := deserializeFloat32(blob)
		if len(vec) == dim {
			ids = append(ids, id)
			vectors = append(vectors, vec)
		}
	}
	return ids, vectors, rows.Err()
}

func loadVectorsByIDs(ctx context.Context, db *sql.DB, ids []uint64) ([]vectorRow, error) {
	if len(ids) == 0 {
		return nil, nil
	}

	// Build IN clause
	query := `SELECT id, content, embedding_vector, COALESCE(project, ''), COALESCE(category, '') FROM learnings WHERE id IN (`
	args := make([]any, len(ids))
	for i, id := range ids {
		if i > 0 {
			query += ","
		}
		query += "?"
		args[i] = id
	}
	query += ") AND embedding_vector IS NOT NULL"

	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []vectorRow
	for rows.Next() {
		var r vectorRow
		var blob []byte
		if err := rows.Scan(&r.id, &r.content, &blob, &r.project, &r.category); err != nil {
			return nil, err
		}
		r.vec = deserializeFloat32(blob)
		results = append(results, r)
	}
	return results, rows.Err()
}

func deserializeFloat32(buf []byte) []float32 {
	if len(buf)%4 != 0 {
		return nil
	}
	v := make([]float32, len(buf)/4)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(buf[i*4:]))
	}
	return v
}
