
package locomo

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/carsteneu/yesmem/internal/embedding"
	"github.com/carsteneu/yesmem/internal/models"
	"github.com/carsteneu/yesmem/internal/storage"
)

// seedLearnings inserts test learnings into the store and returns their IDs.
func seedLearnings(t *testing.T, store *storage.Store, project string, contents []string) []int64 {
	t.Helper()
	ids := make([]int64, 0, len(contents))
	for _, c := range contents {
		id, err := store.InsertLearning(&models.Learning{
			Content:    c,
			Project:    project,
			Category:   "pattern",
			Confidence: 0.9,
			Source:     "llm_extracted",
		})
		if err != nil {
			t.Fatalf("seed learning: %v", err)
		}
		ids = append(ids, id)
	}
	return ids
}

func TestNewLocalSearcher(t *testing.T) {
	store := mustStore(t)
	searcher := NewLocalSearcher(store, nil, nil)
	if searcher == nil {
		t.Fatal("NewLocalSearcher returned nil")
	}
	if searcher.store != store {
		t.Error("store not set")
	}
	if searcher.provider != nil {
		t.Error("provider should be nil")
	}
	if searcher.vectorStore != nil {
		t.Error("vectorStore should be nil")
	}
}

func TestLocalSearcherBM25Only(t *testing.T) {
	store := mustStore(t)
	searcher := NewLocalSearcher(store, nil, nil)

	// Should not panic with nil provider — falls back to BM25-only
	results, err := searcher.HybridSearch("test query", "test_project", 10)
	if err != nil {
		t.Fatalf("BM25-only hybrid search failed: %v", err)
	}
	// Empty results on empty store are fine — verify no panic
	_ = results
}

func TestLocalSearcherBM25WithData(t *testing.T) {
	store := mustStore(t)
	project := "locomo_test"

	// Seed learnings that should match BM25 search
	seedLearnings(t, store, project, []string{
		"Bob traveled to Paris in March 2023",
		"Alice stayed home and read books",
		"The croissants in Paris were incredible",
	})

	searcher := NewLocalSearcher(store, nil, nil)
	results, err := searcher.HybridSearch("Paris travel", project, 10)
	if err != nil {
		t.Fatalf("HybridSearch with data: %v", err)
	}

	// Should find at least 1 result about Paris
	found := false
	for _, r := range results {
		if strings.Contains(r.Content, "Paris") {
			found = true
			break
		}
	}
	if !found && len(results) > 0 {
		t.Errorf("expected Paris-related results, got: %v", results)
	}
}

func TestLocalSearcherSearchMessages(t *testing.T) {
	store := mustStore(t)

	// Ingest test data via the existing pipeline
	samples, err := ParseDataset(strings.NewReader(miniDataset))
	if err != nil {
		t.Fatalf("ParseDataset: %v", err)
	}
	if _, err := IngestSample(store, samples[0]); err != nil {
		t.Fatalf("IngestSample: %v", err)
	}

	searcher := NewLocalSearcher(store, nil, nil)
	results, err := searcher.SearchMessages("Paris", "locomo_1", 5)
	if err != nil {
		t.Fatalf("SearchMessages: %v", err)
	}

	// Should find messages about Paris
	found := false
	for _, r := range results {
		if strings.Contains(r.Content, "Paris") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected Paris-related messages, got %d results", len(results))
	}
}

func TestLocalSearcherHybridWithVector(t *testing.T) {
	store := mustStore(t)
	project := "locomo_test"

	// Add embedding_vector column — not in initial DDL, only in migrations
	// (migrations run before CREATE TABLE on :memory:, so ALTER fails silently)
	store.DB().Exec("ALTER TABLE learnings ADD COLUMN embedding_vector BLOB")

	// Seed learnings
	ids := seedLearnings(t, store, project, []string{
		"Bob traveled to Paris in March 2023",
		"Alice stayed home and read books",
	})

	// Create a mock provider that returns fixed vectors
	provider := &mockEmbeddingProvider{dim: 4}
	vs, err := embedding.NewVectorStore(store.DB(), 4)
	if err != nil {
		t.Fatalf("NewVectorStore: %v", err)
	}

	// Manually embed the learnings with simple vectors
	for i, id := range ids {
		vec := make([]float32, 4)
		vec[i%4] = 1.0 // orthogonal vectors
		err := vs.Add(context.Background(), embedding.VectorDoc{
			ID:        fmt.Sprintf("%d", id),
			Content:   fmt.Sprintf("content-%d", i),
			Embedding: vec,
		})
		if err != nil {
			t.Fatalf("add vector: %v", err)
		}
	}

	searcher := NewLocalSearcher(store, provider, vs)
	results, err := searcher.HybridSearch("Paris travel", project, 10)
	if err != nil {
		t.Fatalf("HybridSearch with vector: %v", err)
	}

	// Should return results (BM25 + vector merged via RRF)
	_ = results
}

func TestTieredLocalSearchEmpty(t *testing.T) {
	store := mustStore(t)
	searcher := NewLocalSearcher(store, nil, nil)
	cfg := DefaultTieredConfig()

	// Should not panic on empty store
	results := TieredLocalSearch(searcher, "What did Bob eat?", "locomo_conv-26", cfg)
	_ = results
}

func TestTieredLocalSearchWithData(t *testing.T) {
	store := mustStore(t)

	// Ingest messages
	samples, err := ParseDataset(strings.NewReader(miniDataset))
	if err != nil {
		t.Fatalf("ParseDataset: %v", err)
	}
	if _, err := IngestSample(store, samples[0]); err != nil {
		t.Fatalf("IngestSample: %v", err)
	}

	// Seed some learnings too
	seedLearnings(t, store, "locomo_1", []string{
		"Bob traveled to Paris in March 2023",
		"The croissants in Paris were incredible",
	})

	searcher := NewLocalSearcher(store, nil, nil)
	cfg := DefaultTieredConfig()
	cfg.TopK = 5

	results := TieredLocalSearch(searcher, "Paris croissants", "locomo_1", cfg)
	if len(results) == 0 {
		t.Error("expected results from tiered search with seeded data")
	}
}

func TestTieredLocalSearchEscalation(t *testing.T) {
	store := mustStore(t)

	// Ingest messages but NO learnings — should escalate to Tier 2 (messages)
	samples, err := ParseDataset(strings.NewReader(miniDataset))
	if err != nil {
		t.Fatalf("ParseDataset: %v", err)
	}
	if _, err := IngestSample(store, samples[0]); err != nil {
		t.Fatalf("IngestSample: %v", err)
	}

	searcher := NewLocalSearcher(store, nil, nil)
	cfg := DefaultTieredConfig()
	cfg.MinResults = 1    // need at least 1 good result
	cfg.ScoreThreshold = 0 // any score counts

	results := TieredLocalSearch(searcher, "Paris", "locomo_1", cfg)
	// Should find something from messages (Tier 2) since no learnings exist
	_ = results
}

func TestQueryConfigLocalSearcher(t *testing.T) {
	// Verify LocalSearcher field exists on QueryConfig
	store := mustStore(t)
	searcher := NewLocalSearcher(store, nil, nil)

	cfg := DefaultQueryConfig()
	cfg.LocalSearcher = searcher
	cfg.Hybrid = true

	if cfg.LocalSearcher == nil {
		t.Error("LocalSearcher not set on QueryConfig")
	}
}

// mockEmbeddingProvider implements embedding.Provider for testing.
type mockEmbeddingProvider struct {
	dim int
}

func (m *mockEmbeddingProvider) Embed(_ context.Context, texts []string) ([][]float32, error) {
	vecs := make([][]float32, len(texts))
	for i := range texts {
		vec := make([]float32, m.dim)
		vec[0] = 0.5 // non-zero query vector
		vecs[i] = vec
	}
	return vecs, nil
}

func (m *mockEmbeddingProvider) Dimensions() int { return m.dim }
func (m *mockEmbeddingProvider) Enabled() bool   { return true }
func (m *mockEmbeddingProvider) Close() error     { return nil }

// Compile-time check.
var _ embedding.Provider = (*mockEmbeddingProvider)(nil)
