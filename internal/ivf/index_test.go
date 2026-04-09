package ivf

import (
	"context"
	"database/sql"
	"encoding/binary"
	"math"
	"testing"

	_ "modernc.org/sqlite"
)

func testDBWithVectors(t *testing.T, dim int, vecs map[uint64][]float32) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	db.Exec(`CREATE TABLE learnings (
		id INTEGER PRIMARY KEY,
		content TEXT NOT NULL DEFAULT '',
		category TEXT NOT NULL DEFAULT '',
		project TEXT NOT NULL DEFAULT '',
		embedding_vector BLOB,
		superseded_by INTEGER
	)`)

	for id, vec := range vecs {
		blob := serializeFloat32(vec)
		db.Exec(`INSERT INTO learnings(id, content, category, embedding_vector) VALUES (?, ?, 'test', ?)`,
			id, "content for "+string(rune('A'+id%26)), blob)
	}
	return db
}

func serializeFloat32(v []float32) []byte {
	buf := make([]byte, len(v)*4)
	for i, f := range v {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(f))
	}
	return buf
}

func makeTestVec(dim int, seed float32) []float32 {
	v := make([]float32, dim)
	for i := range v {
		v[i] = seed + float32(i)*0.001
	}
	var norm float64
	for _, x := range v {
		norm += float64(x) * float64(x)
	}
	scale := float32(1.0 / math.Sqrt(norm))
	for i := range v {
		v[i] *= scale
	}
	return v
}

func TestBuildAndSearch(t *testing.T) {
	dim := 16
	vecs := map[uint64][]float32{
		1: makeTestVec(dim, 0.1),
		2: makeTestVec(dim, 0.11),
		3: makeTestVec(dim, 0.12),
		4: makeTestVec(dim, 0.9),
		5: makeTestVec(dim, 0.91),
		6: makeTestVec(dim, 0.92),
	}
	db := testDBWithVectors(t, dim, vecs)

	idx, err := Build(db, dim, 2, 2)
	if err != nil {
		t.Fatal(err)
	}

	if idx.K != 2 {
		t.Fatalf("expected k=2, got %d", idx.K)
	}
	if idx.TotalVectors() != 6 {
		t.Fatalf("expected 6 vectors, got %d", idx.TotalVectors())
	}

	// Search for something near seed=0.1
	results, err := idx.Search(context.Background(), makeTestVec(dim, 0.1), 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) < 1 {
		t.Fatal("expected at least 1 result")
	}
	// Top result should be ID 1 (exact match)
	if results[0].ID != "1" {
		t.Errorf("expected ID 1 as top result, got %s", results[0].ID)
	}
	if results[0].Similarity < 0.99 {
		t.Errorf("expected high similarity, got %f", results[0].Similarity)
	}
}

func TestBuildEmpty(t *testing.T) {
	db := testDBWithVectors(t, 16, nil)
	idx, err := Build(db, 16, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if idx.K != 0 {
		t.Errorf("expected k=0 for empty, got %d", idx.K)
	}

	results, err := idx.Search(context.Background(), makeTestVec(16, 0.5), 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results for empty index, got %d", len(results))
	}
}

func TestAddAndRemove(t *testing.T) {
	dim := 8
	vecs := map[uint64][]float32{
		1: makeTestVec(dim, 0.1),
		2: makeTestVec(dim, 0.9),
	}
	db := testDBWithVectors(t, dim, vecs)

	idx, err := Build(db, dim, 2, 2)
	if err != nil {
		t.Fatal(err)
	}
	if idx.TotalVectors() != 2 {
		t.Fatalf("expected 2 vectors, got %d", idx.TotalVectors())
	}

	// Add a new vector
	newVec := makeTestVec(dim, 0.15)
	db.Exec(`INSERT INTO learnings(id, content, category, embedding_vector) VALUES (3, 'new', 'test', ?)`, serializeFloat32(newVec))
	idx.Add(3, newVec)

	if idx.TotalVectors() != 3 {
		t.Fatalf("expected 3 vectors after Add, got %d", idx.TotalVectors())
	}

	// Remove it
	idx.Remove(3)
	if idx.TotalVectors() != 2 {
		t.Fatalf("expected 2 vectors after Remove, got %d", idx.TotalVectors())
	}
}

func TestNeedsRebuild(t *testing.T) {
	dim := 4
	// Create 3 clusters with 1, 1, 40 items → avg=14, cluster 3 (40) > 14*3=42? No.
	// Use 1, 1, 100 → avg=34, cluster 3 (100) > 34*3=102? No.
	// Integer division: total=102, avg=102/3=34, 100>102=false.
	// Use extreme skew: 1, 1, 200 → total=202, avg=67, 200>201=false...
	// The issue is integer division. Use: 0, 0, 10 → total=10, avg=3, 10>9=true
	idx := &IVFIndex{
		K:         3,
		Dim:       dim,
		NProbe:    2,
		Centroids: [][]float32{{1, 0, 0, 0}, {0, 1, 0, 0}, {0, 0, 1, 0}},
		Clusters:  [][]uint64{{}, {}, make([]uint64, 10)},
	}

	if !idx.NeedsRebuild() {
		t.Error("skewed index should need rebuild")
	}

	// Balanced: 8, 8, 8
	idx.Clusters = [][]uint64{make([]uint64, 8), make([]uint64, 8), make([]uint64, 8)}
	if idx.NeedsRebuild() {
		t.Error("balanced index should not need rebuild")
	}
}

func TestIsStale(t *testing.T) {
	idx := &IVFIndex{
		K:         2,
		Dim:       4,
		NProbe:    1,
		Centroids: [][]float32{{1, 0, 0, 0}, {0, 1, 0, 0}},
		Clusters:  [][]uint64{make([]uint64, 50), make([]uint64, 50)},
	}
	// Index has 100 vectors

	// DB has 100 → 0% gap → not stale
	if idx.IsStale(100) {
		t.Error("0% gap should not be stale")
	}

	// DB has 102 → 2% gap → borderline, not stale
	if idx.IsStale(102) {
		t.Error("2% gap should not be stale")
	}

	// DB has 103 → >2% gap → stale
	if !idx.IsStale(103) {
		t.Error(">2% gap should be stale")
	}

	// DB has 200 → 50% gap → definitely stale
	if !idx.IsStale(200) {
		t.Error("50% gap should be stale")
	}

	// Edge: DB has fewer than index (shouldn't happen, but safe)
	if idx.IsStale(90) {
		t.Error("DB < index should not be stale")
	}
}

func TestAutoK(t *testing.T) {
	dim := 4
	vecs := make(map[uint64][]float32)
	for i := uint64(1); i <= 100; i++ {
		vecs[i] = makeTestVec(dim, float32(i)*0.01)
	}
	db := testDBWithVectors(t, dim, vecs)

	idx, err := Build(db, dim, 0, 5) // k=0 → auto sqrt(100)=10
	if err != nil {
		t.Fatal(err)
	}
	if idx.K != 10 {
		t.Errorf("expected auto k=10 for 100 vectors, got %d", idx.K)
	}
}
