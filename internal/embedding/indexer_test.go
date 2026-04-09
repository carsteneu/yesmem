package embedding

import (
	"context"
	"testing"
)

// mockProvider returns fixed-dimension vectors for testing.
type mockProvider struct {
	dims    int
	callCnt int
}

func (m *mockProvider) Embed(_ context.Context, texts []string) ([][]float32, error) {
	m.callCnt++
	vecs := make([][]float32, len(texts))
	for i, text := range texts {
		vecs[i] = makeVec(m.dims, float32(len(text))*0.01)
	}
	return vecs, nil
}

func (m *mockProvider) Dimensions() int { return m.dims }
func (m *mockProvider) Enabled() bool   { return true }
func (m *mockProvider) Close() error    { return nil }

func TestIndexerIndexSingle(t *testing.T) {
	provider := &mockProvider{dims: 384}
	db := testDB(t)
	db.Exec(`INSERT INTO learnings(id, content, category) VALUES (1, 'nginx reverse proxy config', 'test')`)

	store, err := NewVectorStore(db, 384)
	if err != nil {
		t.Fatal(err)
	}

	indexer := NewIndexer(provider, store)
	ctx := context.Background()

	err = indexer.Index(ctx, "1", "nginx reverse proxy config", nil)
	if err != nil {
		t.Fatalf("index failed: %v", err)
	}

	if store.Count() != 1 {
		t.Fatalf("expected 1 doc, got %d", store.Count())
	}
	if provider.callCnt != 1 {
		t.Fatalf("expected 1 embed call, got %d", provider.callCnt)
	}
}

func TestIndexerIndexBatch(t *testing.T) {
	provider := &mockProvider{dims: 384}
	db := testDB(t)
	db.Exec(`INSERT INTO learnings(id, content, category) VALUES (1, 'docker compose networking', 'test')`)
	db.Exec(`INSERT INTO learnings(id, content, category) VALUES (2, 'ansible playbook structure', 'test')`)
	db.Exec(`INSERT INTO learnings(id, content, category) VALUES (3, 'go testing patterns', 'test')`)

	store, err := NewVectorStore(db, 384)
	if err != nil {
		t.Fatal(err)
	}

	indexer := NewIndexer(provider, store)
	ctx := context.Background()

	items := []IndexItem{
		{ID: "1", Content: "docker compose networking"},
		{ID: "2", Content: "ansible playbook structure"},
		{ID: "3", Content: "go testing patterns"},
	}

	err = indexer.IndexBatch(ctx, items)
	if err != nil {
		t.Fatalf("batch index failed: %v", err)
	}

	if store.Count() != 3 {
		t.Fatalf("expected 3 docs, got %d", store.Count())
	}
}

func TestIndexerReindex(t *testing.T) {
	provider := &mockProvider{dims: 384}
	db := testDB(t)
	db.Exec(`INSERT INTO learnings(id, content, category) VALUES (1, 'original content', 'test')`)

	store, err := NewVectorStore(db, 384)
	if err != nil {
		t.Fatal(err)
	}

	indexer := NewIndexer(provider, store)
	ctx := context.Background()

	indexer.Index(ctx, "1", "original content", nil)
	indexer.Index(ctx, "1", "updated content", nil)

	if store.Count() != 1 {
		t.Fatalf("expected 1 doc after reindex, got %d", store.Count())
	}
}

func TestIndexerDisabledProvider(t *testing.T) {
	provider := NewNoneProvider()
	db := testDB(t)
	store, err := NewVectorStore(db, 384)
	if err != nil {
		t.Fatal(err)
	}

	indexer := NewIndexer(provider, store)
	ctx := context.Background()

	err = indexer.Index(ctx, "1", "test content", nil)
	if err != nil {
		t.Fatalf("expected no error with disabled provider, got: %v", err)
	}
	if store.Count() != 0 {
		t.Fatalf("expected 0 docs with disabled provider, got %d", store.Count())
	}
}
