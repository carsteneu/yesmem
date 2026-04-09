package embedding

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestVectorStoreAddAndSearch(t *testing.T) {
	db := testDB(t)
	store, err := NewVectorStore(db, 384)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}

	ctx := context.Background()

	// Insert learnings rows first (Add updates existing rows)
	db.Exec(`INSERT INTO learnings(id, content, category) VALUES (1, 'nginx reverse proxy', 'test')`)
	db.Exec(`INSERT INTO learnings(id, content, category) VALUES (2, 'web server load balancing', 'test')`)
	db.Exec(`INSERT INTO learnings(id, content, category) VALUES (3, 'chocolate cake recipe', 'test')`)

	docs := []VectorDoc{
		{ID: "1", Embedding: makeVec(384, 0.1)},
		{ID: "2", Embedding: makeVec(384, 0.11)},
		{ID: "3", Embedding: makeVec(384, 0.9)},
	}
	for _, doc := range docs {
		if err := store.Add(ctx, doc); err != nil {
			t.Fatalf("failed to add doc %s: %v", doc.ID, err)
		}
	}

	if store.Count() != 3 {
		t.Fatalf("expected 3 docs, got %d", store.Count())
	}

	results, err := store.Search(ctx, makeVec(384, 0.1), 2)
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if results[0].ID != "1" {
		t.Errorf("expected 1 as top result, got %s", results[0].ID)
	}
	if results[0].Similarity < 0.99 {
		t.Errorf("expected high similarity for exact match, got %f", results[0].Similarity)
	}
}

func TestVectorStoreDelete(t *testing.T) {
	db := testDB(t)
	store, err := NewVectorStore(db, 384)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}

	ctx := context.Background()
	db.Exec(`INSERT INTO learnings(id, content, category) VALUES (1, 'test', 'test')`)
	store.Add(ctx, VectorDoc{ID: "1", Embedding: makeVec(384, 0.5)})

	if store.Count() != 1 {
		t.Fatalf("expected 1 doc, got %d", store.Count())
	}

	if err := store.Delete(ctx, "1"); err != nil {
		t.Fatalf("delete failed: %v", err)
	}
	if store.Count() != 0 {
		t.Fatalf("expected 0 docs after delete, got %d", store.Count())
	}
}

func TestVectorStorePersistence(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	db.Exec(`INSERT INTO learnings(id, content, category) VALUES (1, 'persisted', 'test')`)

	store1, err := NewVectorStore(db, 384)
	if err != nil {
		t.Fatal(err)
	}
	store1.Add(ctx, VectorDoc{ID: "1", Embedding: makeVec(384, 0.5)})

	// Same DB — data persists in learnings table
	store2, err := NewVectorStore(db, 384)
	if err != nil {
		t.Fatal(err)
	}
	if store2.Count() != 1 {
		t.Fatalf("expected 1 doc after reopen, got %d", store2.Count())
	}
}

func TestVectorStoreAddBatch(t *testing.T) {
	db := testDB(t)
	store, err := NewVectorStore(db, 384)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	db.Exec(`INSERT INTO learnings(id, content, category) VALUES (1, 'one', 'test')`)
	db.Exec(`INSERT INTO learnings(id, content, category) VALUES (2, 'two', 'test')`)
	db.Exec(`INSERT INTO learnings(id, content, category) VALUES (3, 'three', 'test')`)

	docs := []VectorDoc{
		{ID: "1", Embedding: makeVec(384, 0.1)},
		{ID: "2", Embedding: makeVec(384, 0.2)},
		{ID: "3", Embedding: makeVec(384, 0.3)},
	}
	if err := store.AddBatch(ctx, docs); err != nil {
		t.Fatal(err)
	}
	if store.Count() != 3 {
		t.Fatalf("expected 3 docs, got %d", store.Count())
	}
}

func TestVectorStoreHas(t *testing.T) {
	db := testDB(t)
	store, err := NewVectorStore(db, 384)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	db.Exec(`INSERT INTO learnings(id, content, category) VALUES (42, 'test', 'test')`)
	store.Add(ctx, VectorDoc{ID: "42", Embedding: makeVec(384, 0.5)})

	if !store.Has(ctx, "42") {
		t.Error("expected Has(42) = true")
	}
	if store.Has(ctx, "999") {
		t.Error("expected Has(999) = false")
	}
}

func TestVectorStoreGetEmbedding(t *testing.T) {
	db := testDB(t)
	store, err := NewVectorStore(db, 384)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	vec := makeVec(384, 0.42)
	db.Exec(`INSERT INTO learnings(id, content, category) VALUES (7, 'test', 'test')`)
	store.Add(ctx, VectorDoc{ID: "7", Embedding: vec})

	got := store.GetEmbedding(ctx, "7")
	if got == nil {
		t.Fatal("expected embedding, got nil")
	}
	if len(got) != 384 {
		t.Fatalf("expected 384 dims, got %d", len(got))
	}
	if got[0] != vec[0] {
		t.Errorf("embedding mismatch: got[0]=%f, want %f", got[0], vec[0])
	}
	if store.GetEmbedding(ctx, "999") != nil {
		t.Error("expected nil for non-existent ID")
	}
}

func TestVectorStorePeriodicSave(t *testing.T) {
	db := testDB(t)
	store, err := NewVectorStore(db, 4)
	if err != nil {
		t.Fatal(err)
	}

	mock := &mockIVFSaver{}
	store.SetIVFIndex(mock)

	path := filepath.Join(t.TempDir(), "periodic.ivf")
	store.SetIVFSavePath(path, 3) // save every 3 adds

	ctx := context.Background()

	// Insert learning rows for Add to work
	for i := 1; i <= 5; i++ {
		db.Exec(`INSERT INTO learnings(id, content, category) VALUES (?, 'test', 'test')`, i)
	}

	// Add 2 vectors — no save yet
	store.Add(ctx, VectorDoc{ID: "1", Embedding: makeVec(4, 0.1)})
	store.Add(ctx, VectorDoc{ID: "2", Embedding: makeVec(4, 0.2)})
	if mock.saveCount != 0 {
		t.Fatalf("expected 0 saves after 2 adds, got %d", mock.saveCount)
	}

	// Add 3rd vector — triggers save
	store.Add(ctx, VectorDoc{ID: "3", Embedding: makeVec(4, 0.3)})
	if mock.saveCount != 1 {
		t.Fatalf("expected 1 save after 3 adds, got %d", mock.saveCount)
	}
	if mock.savePath != path {
		t.Fatalf("expected save to %q, got %q", path, mock.savePath)
	}

	// Counter resets — next 2 adds: no save
	store.Add(ctx, VectorDoc{ID: "4", Embedding: makeVec(4, 0.4)})
	store.Add(ctx, VectorDoc{ID: "5", Embedding: makeVec(4, 0.5)})
	if mock.saveCount != 1 {
		t.Fatalf("expected still 1 save after 5 total adds, got %d", mock.saveCount)
	}
}

func TestVectorStoreSaveIVF(t *testing.T) {
	db := testDB(t)
	store, err := NewVectorStore(db, 4)
	if err != nil {
		t.Fatal(err)
	}

	// No IVF index → SaveIVF should be no-op, no error
	path := filepath.Join(t.TempDir(), "test.ivf")
	if err := store.SaveIVF(path); err != nil {
		t.Fatalf("SaveIVF without index should not error: %v", err)
	}
	if _, err := os.Stat(path); err == nil {
		t.Fatal("SaveIVF without index should not create a file")
	}

	// Set a mock IVF index and save
	mock := &mockIVFSaver{savePath: ""}
	store.SetIVFIndex(mock)

	if err := store.SaveIVF(path); err != nil {
		t.Fatalf("SaveIVF: %v", err)
	}
	if mock.savePath != path {
		t.Fatalf("expected Save(%q), got Save(%q)", path, mock.savePath)
	}
}

// mockIVFSaver implements IVFSearcher + IVFSaver for testing.
type mockIVFSaver struct {
	savePath  string
	saveCount int
}

func (m *mockIVFSaver) Search(_ context.Context, _ []float32, _ int) ([]SearchResult, error) {
	return nil, nil
}
func (m *mockIVFSaver) Add(_ uint64, _ []float32) {}
func (m *mockIVFSaver) Remove(_ uint64)            {}
func (m *mockIVFSaver) Save(path string) error {
	m.savePath = path
	m.saveCount++
	return nil
}

func TestCosineSimilarity(t *testing.T) {
	a := []float32{1, 0, 0}
	b := []float32{1, 0, 0}
	if sim := cosineSimilarity(a, b); sim < 0.999 {
		t.Errorf("identical vectors should have similarity ~1.0, got %f", sim)
	}

	c := []float32{0, 1, 0}
	if sim := cosineSimilarity(a, c); sim > 0.001 {
		t.Errorf("orthogonal vectors should have similarity ~0.0, got %f", sim)
	}
}

// makeVec creates a simple normalized-ish vector for testing.
func makeVec(dims int, seed float32) []float32 {
	v := make([]float32, dims)
	for i := range v {
		v[i] = seed + float32(i)*0.001
	}
	var norm float32
	for _, x := range v {
		norm += x * x
	}
	norm = float32(1.0 / float64(norm))
	for i := range v {
		v[i] *= norm
	}
	return v
}

// testDB creates an in-memory SQLite DB with a minimal learnings table.
func testDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	db.Exec(`CREATE TABLE IF NOT EXISTS learnings (
		id INTEGER PRIMARY KEY,
		content TEXT NOT NULL DEFAULT '',
		category TEXT NOT NULL DEFAULT '',
		project TEXT NOT NULL DEFAULT '',
		embedding_vector BLOB,
		embedding_status TEXT,
		superseded_by INTEGER
	)`)
	return db
}
