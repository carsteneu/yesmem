package embedding

import (
	"context"
	"testing"
)

func TestMigrateEmbeddings(t *testing.T) {
	provider := &mockProvider{dims: 384}
	db := testDB(t)
	db.Exec(`INSERT INTO learnings(id, content, category) VALUES (1, 'nginx config', 'test')`)
	db.Exec(`INSERT INTO learnings(id, content, category) VALUES (2, 'docker networking', 'test')`)
	db.Exec(`INSERT INTO learnings(id, content, category) VALUES (3, 'go testing', 'test')`)

	store, err := NewVectorStore(db, 384)
	if err != nil {
		t.Fatal(err)
	}

	items := []IndexItem{
		{ID: "1", Content: "nginx config"},
		{ID: "2", Content: "docker networking"},
		{ID: "3", Content: "go testing"},
	}

	stats, err := MigrateEmbeddings(context.Background(), provider, store, items, 2, false, 0)
	if err != nil {
		t.Fatal(err)
	}

	if stats.Total != 3 {
		t.Fatalf("expected total 3, got %d", stats.Total)
	}
	if stats.Embedded != 3 {
		t.Fatalf("expected embedded 3, got %d", stats.Embedded)
	}
	if stats.Skipped != 0 {
		t.Fatalf("expected skipped 0, got %d", stats.Skipped)
	}
	if store.Count() != 3 {
		t.Fatalf("expected 3 docs in store, got %d", store.Count())
	}
}

func TestMigrateEmbeddingsSkipsExisting(t *testing.T) {
	provider := &mockProvider{dims: 384}
	db := testDB(t)
	db.Exec(`INSERT INTO learnings(id, content, category) VALUES (1, 'nginx config', 'test')`)
	db.Exec(`INSERT INTO learnings(id, content, category) VALUES (2, 'existing', 'test')`)
	db.Exec(`INSERT INTO learnings(id, content, category) VALUES (3, 'go testing', 'test')`)

	store, err := NewVectorStore(db, 384)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	store.Add(ctx, VectorDoc{ID: "2", Embedding: makeVec(384, 0.5)})

	items := []IndexItem{
		{ID: "1", Content: "nginx config"},
		{ID: "2", Content: "docker networking"},
		{ID: "3", Content: "go testing"},
	}

	stats, err := MigrateEmbeddings(ctx, provider, store, items, 10, false, 0)
	if err != nil {
		t.Fatal(err)
	}

	if stats.Embedded != 2 {
		t.Fatalf("expected 2 new embeddings, got %d", stats.Embedded)
	}
	if stats.Skipped != 1 {
		t.Fatalf("expected 1 skipped, got %d", stats.Skipped)
	}
}

func TestMigrateEmbeddingsForce(t *testing.T) {
	provider := &mockProvider{dims: 384}
	db := testDB(t)
	db.Exec(`INSERT INTO learnings(id, content, category) VALUES (1, 'old', 'test')`)

	store, err := NewVectorStore(db, 384)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	store.Add(ctx, VectorDoc{ID: "1", Embedding: makeVec(384, 0.5)})

	items := []IndexItem{
		{ID: "1", Content: "updated content"},
	}

	stats, err := MigrateEmbeddings(ctx, provider, store, items, 10, true, 0)
	if err != nil {
		t.Fatal(err)
	}

	if stats.Embedded != 1 {
		t.Fatalf("force should re-embed, got embedded=%d", stats.Embedded)
	}
	if stats.Skipped != 0 {
		t.Fatalf("force should skip 0, got %d", stats.Skipped)
	}
}

func TestMigrateEmbeddingsDisabledProvider(t *testing.T) {
	provider := NewNoneProvider()
	db := testDB(t)
	store, err := NewVectorStore(db, 384)
	if err != nil {
		t.Fatal(err)
	}

	items := []IndexItem{{ID: "1", Content: "test"}}
	_, err = MigrateEmbeddings(context.Background(), provider, store, items, 10, false, 0)
	if err == nil {
		t.Fatal("expected error with disabled provider")
	}
}
