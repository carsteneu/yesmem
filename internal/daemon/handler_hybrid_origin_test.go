package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/carsteneu/yesmem/internal/embedding"
	"github.com/carsteneu/yesmem/internal/models"
	"github.com/carsteneu/yesmem/internal/storage"
)

// mustHandlerWithEmbeddingFile mirrors mustHandlerWithEmbedding but backs the
// store with a temp-file SQLite path so writer/reader connections observe the
// same database. Required for cooldown-sensitive tests because :memory: opens a
// fresh DB image per connection — backdated created_at writes are invisible to
// the reader-pool query that filters fresh learnings.
func mustHandlerWithEmbeddingFile(t *testing.T) (*Handler, *storage.Store) {
	t.Helper()
	dir := t.TempDir()
	s, err := storage.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	h := NewHandler(s, nil)
	h.dataDir = dir
	return h, s
}

// TestEnrichRankedResults_EmitsOriginTool guards against the Plan B Critical:
// without origin_tool in the daemon's hybrid_search JSON response, the proxy's
// hybridResult.OriginTool stays empty and OriginMultiplier silently falls back
// to the unknown-default 0.8 for every result, collapsing the spread.
func TestEnrichRankedResults_EmitsOriginTool(t *testing.T) {
	h, s := mustHandlerWithEmbeddingFile(t)

	l := &models.Learning{
		Content:    "nginx reverse proxy configuration from external web",
		Category:   "pattern",
		Source:     "user_stated",
		OriginTool: "web_external",
		CreatedAt:  time.Now().Add(-2 * time.Hour),
	}
	id, err := s.InsertLearning(l)
	if err != nil {
		t.Fatalf("InsertLearning: %v", err)
	}
	if _, err := s.DB().Exec("UPDATE learnings SET created_at = ? WHERE id = ?",
		time.Now().UTC().Add(-2*time.Hour).Format("2006-01-02 15:04:05"), id); err != nil {
		t.Fatalf("backdate created_at: %v", err)
	}

	store, err := embedding.NewVectorStore(s.DB(), 384)
	if err != nil {
		t.Fatal(err)
	}
	provider := &testEmbedProvider{dims: 384}
	indexer := embedding.NewIndexer(provider, store)
	h.SetEmbedding(indexer, store, provider)
	if err := indexer.Index(context.Background(), fmt.Sprintf("%d", id), l.Content, nil); err != nil {
		t.Fatalf("index: %v", err)
	}

	resp := h.Handle(Request{
		Method: "hybrid_search",
		Params: map[string]any{"query": "nginx proxy", "limit": float64(5)},
	})
	if resp.Error != "" {
		t.Fatalf("hybrid_search error: %s", resp.Error)
	}

	var parsed struct {
		Results []struct {
			ID         string `json:"id"`
			OriginTool string `json:"origin_tool"`
		} `json:"results"`
	}
	if err := json.Unmarshal(resp.Result, &parsed); err != nil {
		t.Fatalf("parse result: %v", err)
	}

	var found bool
	var got string
	for _, r := range parsed.Results {
		if r.ID == fmt.Sprintf("%d", id) {
			found = true
			got = r.OriginTool
			break
		}
	}
	if !found {
		t.Fatalf("expected learning %d in results, got %d results", id, len(parsed.Results))
	}
	if got != "web_external" {
		t.Fatalf("origin_tool not roundtripped through enrichRankedResults: got %q, want %q", got, "web_external")
	}
}
