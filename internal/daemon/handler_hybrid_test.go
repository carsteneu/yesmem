package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/carsteneu/yesmem/internal/embedding"
	"github.com/carsteneu/yesmem/internal/models"
	"github.com/carsteneu/yesmem/internal/storage"
)

// mustHandlerWithEmbedding creates a test handler with embedding_vector column
// (missing in initial schema, added only via migration v0.26).
func mustHandlerWithEmbedding(t *testing.T) (*Handler, *storage.Store) {
	t.Helper()
	h, s := mustHandler(t)
	s.DB().Exec(`ALTER TABLE learnings ADD COLUMN embedding_vector BLOB`)
	return h, s
}

func TestHandleHybridSearch(t *testing.T) {
	h, s := mustHandlerWithEmbedding(t)

	// Insert learnings so VectorStore.Add() can UPDATE them
	s.DB().Exec(`INSERT INTO learnings(id, content, category) VALUES (1, 'nginx reverse proxy configuration', 'test')`)
	s.DB().Exec(`INSERT INTO learnings(id, content, category) VALUES (2, 'docker compose networking setup', 'test')`)

	store, err := embedding.NewVectorStore(s.DB(), 384)
	if err != nil {
		t.Fatal(err)
	}

	provider := &testEmbedProvider{dims: 384}
	indexer := embedding.NewIndexer(provider, store)
	h.SetEmbedding(indexer, store, provider)

	ctx := context.Background()
	indexer.Index(ctx, "1", "nginx reverse proxy configuration", nil)
	indexer.Index(ctx, "2", "docker compose networking setup", nil)

	resp := h.Handle(Request{
		Method: "hybrid_search",
		Params: map[string]any{
			"query": "nginx proxy",
			"limit": float64(5),
		},
	})

	if resp.Error != "" {
		t.Fatalf("hybrid_search returned error: %s", resp.Error)
	}
	if len(resp.Result) == 0 {
		t.Fatal("expected non-empty result")
	}
}

func TestHandleHybridSearchWithoutVectorStore(t *testing.T) {
	h, _ := mustHandler(t)
	// No SetEmbedding called — graceful degradation to BM25 only

	resp := h.Handle(Request{
		Method: "hybrid_search",
		Params: map[string]any{
			"query": "test",
			"limit": float64(5),
		},
	})

	if resp.Error != "" {
		t.Fatalf("hybrid_search without vector store should not error: %s", resp.Error)
	}
}

// testEmbedProvider for daemon tests
type testEmbedProvider struct {
	dims int
}

func (m *testEmbedProvider) Embed(_ context.Context, texts []string) ([][]float32, error) {
	vecs := make([][]float32, len(texts))
	for i, text := range texts {
		v := make([]float32, m.dims)
		for j := range v {
			v[j] = float32(len(text))*0.01 + float32(j)*0.001
		}
		var norm float32
		for _, x := range v {
			norm += x * x
		}
		norm = 1.0 / norm
		for j := range v {
			v[j] *= norm
		}
		vecs[i] = v
	}
	return vecs, nil
}

func (m *testEmbedProvider) Dimensions() int { return m.dims }
func (m *testEmbedProvider) Enabled() bool   { return true }
func (m *testEmbedProvider) Close() error    { return nil }

// --- Project-Recency Boost Unit Tests ---

func TestApplyTurnBasedDecay_RecentVsOld(t *testing.T) {
	h, s := mustHandler(t)

	now := time.Now().Format("2006-01-02 15:04:05")

	// Learning created at turn 90 (recent) vs turn 0 (old), current turn = 100
	s.DB().Exec(`INSERT INTO learnings(id, content, category, project, created_at, model_used, source, turns_at_creation) VALUES (1, 'old pattern', 'pattern', 'memory', ?, 'test', 'llm_extracted', 0)`, now)
	s.DB().Exec(`INSERT INTO learnings(id, content, category, project, created_at, model_used, source, turns_at_creation) VALUES (2, 'recent pattern', 'pattern', 'memory', ?, 'test', 'llm_extracted', 90)`, now)
	s.DB().Exec(`INSERT OR REPLACE INTO turn_counters(project, turn_count) VALUES ('memory', 100)`)

	results := []embedding.RankedResult{
		{ID: "1", Content: "old pattern", Score: 0.5, Project: "memory"},
		{ID: "2", Content: "recent pattern", Score: 0.5, Project: "memory"},
	}

	decayed := h.applyTurnBasedDecay(results, "memory")

	// Recent (10 turns since) should rank higher than old (100 turns since)
	if decayed[0].ID != "2" {
		t.Errorf("recent learning should rank first, got id=%s", decayed[0].ID)
	}
	if decayed[0].Score >= 0.5 {
		t.Errorf("recent score should be decayed below 0.5, got %.4f", decayed[0].Score)
	}
	if decayed[0].Score <= decayed[1].Score {
		t.Errorf("recent should score higher than old: recent=%.4f old=%.4f", decayed[0].Score, decayed[1].Score)
	}
}

func TestApplyTurnBasedDecay_EmptyResults(t *testing.T) {
	h, _ := mustHandler(t)

	results := []embedding.RankedResult{}
	decayed := h.applyTurnBasedDecay(results, "memory")

	if len(decayed) != 0 {
		t.Errorf("empty input should return empty output, got %d", len(decayed))
	}
}

func TestApplyTurnBasedDecay_NoTurnCounter(t *testing.T) {
	h, s := mustHandler(t)

	now := time.Now().Format("2006-01-02 15:04:05")
	s.DB().Exec(`INSERT INTO learnings(id, content, category, project, created_at, model_used, source, turns_at_creation) VALUES (1, 'pattern', 'pattern', 'memory', ?, 'test', 'llm_extracted', 0)`, now)

	results := []embedding.RankedResult{
		{ID: "1", Content: "pattern", Score: 0.5, Project: "memory"},
	}

	// No turn_counters row → currentTurnCount=0, turnsSince=0 → decay=1.0
	decayed := h.applyTurnBasedDecay(results, "memory")

	if decayed[0].Score != 0.5 {
		t.Errorf("no turn counter should mean no decay: got %.4f, want 0.5", decayed[0].Score)
	}
}

func TestApplyTurnBasedDecay_UserStatedFloor(t *testing.T) {
	h, s := mustHandler(t)

	now := time.Now().Format("2006-01-02 15:04:05")
	s.DB().Exec(`INSERT INTO learnings(id, content, category, project, created_at, model_used, source, turns_at_creation) VALUES (1, 'user rule', 'preference', 'memory', ?, 'test', 'user_stated', 0)`, now)
	s.DB().Exec(`INSERT INTO learnings(id, content, category, project, created_at, model_used, source, turns_at_creation) VALUES (2, 'extracted rule', 'preference', 'memory', ?, 'test', 'llm_extracted', 0)`, now)
	s.DB().Exec(`INSERT OR REPLACE INTO turn_counters(project, turn_count) VALUES ('memory', 10000)`)

	results := []embedding.RankedResult{
		{ID: "1", Content: "user rule", Score: 1.0, Project: "memory"},
		{ID: "2", Content: "extracted rule", Score: 1.0, Project: "memory"},
	}

	decayed := h.applyTurnBasedDecay(results, "memory")

	// user_stated should have floor of 0.5, llm_extracted floor of 0.1
	if decayed[0].ID != "1" {
		t.Errorf("user_stated should rank first due to higher floor, got id=%s", decayed[0].ID)
	}
	if decayed[0].Score < 0.49 {
		t.Errorf("user_stated floor should be >= 0.5, got %.4f", decayed[0].Score)
	}
}

func TestGraphAugmentation(t *testing.T) {
	h, s := mustHandler(t)

	l1 := &models.Learning{Content: "proxy config needs restart after change", Category: "gotcha", Source: "user_stated", CreatedAt: time.Now().Add(-2 * time.Hour)}
	id1, _ := s.InsertLearning(l1)
	s.DB().Exec("UPDATE learnings SET created_at = datetime('now', '-2 hours') WHERE id = ?", id1)

	l2 := &models.Learning{Content: "config reload requires daemon restart too", Category: "gotcha", Source: "user_stated", CreatedAt: time.Now().Add(-2 * time.Hour)}
	id2, _ := s.InsertLearning(l2)
	s.DB().Exec("UPDATE learnings SET created_at = datetime('now', '-2 hours') WHERE id = ?", id2)

	s.InsertTypedAssociation(id1, id2, "depends_on")

	h.EmbedLearning(id1, l1.Content, l1.Category, "")
	h.EmbedLearning(id2, l2.Content, l2.Category, "")

	resp := h.Handle(Request{
		Method: "hybrid_search",
		Params: map[string]any{"query": "proxy config restart", "limit": float64(10)},
	})
	if resp.Error != "" {
		t.Fatalf("hybrid_search error: %s", resp.Error)
	}

	var result struct {
		Results []struct {
			ID     string `json:"id"`
			Source string `json:"source"`
		} `json:"results"`
		MergedCount int `json:"merged_count"`
	}
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("parse result: %v", err)
	}

	foundGraph := false
	for _, r := range result.Results {
		if r.ID == fmt.Sprintf("%d", id2) && strings.HasPrefix(r.Source, "graph") {
			foundGraph = true
		}
	}
	if !foundGraph {
		t.Error("expected depends_on neighbor to appear with graph source")
	}
	t.Logf("merged_count=%d, foundGraph=%v", result.MergedCount, foundGraph)
}

func TestAugmentWithGraphNeighbors_DependsOnHigherScore(t *testing.T) {
	h, s := mustHandler(t)

	l1 := &models.Learning{Content: "base config", Category: "pattern", Source: "user_stated", CreatedAt: time.Now().Add(-1 * time.Hour)}
	id1, _ := s.InsertLearning(l1)

	l2 := &models.Learning{Content: "prerequisite config", Category: "pattern", Source: "user_stated", CreatedAt: time.Now().Add(-1 * time.Hour)}
	id2, _ := s.InsertLearning(l2)

	l3 := &models.Learning{Content: "loosely related info", Category: "pattern", Source: "user_stated", CreatedAt: time.Now().Add(-1 * time.Hour)}
	id3, _ := s.InsertLearning(l3)

	s.InsertTypedAssociation(id1, id2, "depends_on")
	s.InsertTypedAssociation(id1, id3, "relates_to")

	merged := []embedding.RankedResult{
		{ID: fmt.Sprintf("%d", id1), Content: "base config", Score: 50.0, Source: "keyword"},
	}

	result := h.augmentWithGraphNeighbors(merged)

	var dependsOnScore, relatesToScore float64
	var dependsOnSource, relatesToSource string
	for _, r := range result {
		switch r.ID {
		case fmt.Sprintf("%d", id2):
			dependsOnScore = r.Score
			dependsOnSource = r.Source
		case fmt.Sprintf("%d", id3):
			relatesToScore = r.Score
			relatesToSource = r.Source
		}
	}

	if dependsOnScore == 0 {
		t.Fatal("depends_on neighbor not found in results")
	}
	if relatesToScore == 0 {
		t.Fatal("relates_to neighbor not found in results")
	}

	// depends_on should score higher than relates_to (same weight=1.0)
	if dependsOnScore <= relatesToScore {
		t.Errorf("depends_on should score higher: depends_on=%.2f, relates_to=%.2f",
			dependsOnScore, relatesToScore)
	}

	// Source should reflect the relation type
	if dependsOnSource != "graph:depends_on" {
		t.Errorf("expected source 'graph:depends_on', got %q", dependsOnSource)
	}
	if relatesToSource != "graph" {
		t.Errorf("expected source 'graph' for relates_to, got %q", relatesToSource)
	}
}

func TestAugmentWithGraphNeighbors_SupportsSource(t *testing.T) {
	h, s := mustHandler(t)

	l1 := &models.Learning{Content: "main claim", Category: "decision", Source: "user_stated", CreatedAt: time.Now().Add(-1 * time.Hour)}
	id1, _ := s.InsertLearning(l1)

	l2 := &models.Learning{Content: "supporting evidence", Category: "decision", Source: "user_stated", CreatedAt: time.Now().Add(-1 * time.Hour)}
	id2, _ := s.InsertLearning(l2)

	s.InsertTypedAssociation(id1, id2, "supports")

	merged := []embedding.RankedResult{
		{ID: fmt.Sprintf("%d", id1), Content: "main claim", Score: 40.0, Source: "keyword"},
	}

	result := h.augmentWithGraphNeighbors(merged)

	for _, r := range result {
		if r.ID == fmt.Sprintf("%d", id2) {
			if r.Source != "graph:supports" {
				t.Errorf("expected source 'graph:supports', got %q", r.Source)
			}
			return
		}
	}
	t.Error("supports neighbor not found in results")
}

func TestFilterResultsByTime_Empty(t *testing.T) {
	h, _ := mustHandler(t)
	results := h.filterResultsByTime(nil, "", "")
	if len(results) != 0 {
		t.Errorf("expected empty, got %d", len(results))
	}
}

func TestFilterResultsByTime_WithSince(t *testing.T) {
	h, s := mustHandler(t)

	old := &models.Learning{Content: "old learning", Category: "pattern", Source: "user_stated"}
	idOld, _ := s.InsertLearning(old)
	s.DB().Exec("UPDATE learnings SET created_at = '2026-01-01T00:00:00Z' WHERE id = ?", idOld)

	recent := &models.Learning{Content: "recent learning", Category: "pattern", Source: "user_stated"}
	idRecent, _ := s.InsertLearning(recent)
	s.DB().Exec("UPDATE learnings SET created_at = '2026-03-30T00:00:00Z' WHERE id = ?", idRecent)

	results := []embedding.RankedResult{
		{ID: fmt.Sprintf("%d", idOld), Score: 0.9},
		{ID: fmt.Sprintf("%d", idRecent), Score: 0.8},
	}

	filtered := h.filterResultsByTime(results, "2026-03-01", "")
	if len(filtered) != 1 {
		t.Fatalf("expected 1 result after since filter, got %d", len(filtered))
	}
	if filtered[0].ID != fmt.Sprintf("%d", idRecent) {
		t.Error("expected only the recent learning")
	}
}

func TestFilterResultsByTime_WithBefore(t *testing.T) {
	h, s := mustHandler(t)

	l := &models.Learning{Content: "march learning", Category: "pattern", Source: "user_stated"}
	id, _ := s.InsertLearning(l)
	s.DB().Exec("UPDATE learnings SET created_at = '2026-03-15T00:00:00Z' WHERE id = ?", id)

	results := []embedding.RankedResult{{ID: fmt.Sprintf("%d", id), Score: 0.9}}

	filtered := h.filterResultsByTime(results, "", "2026-02-01")
	if len(filtered) != 0 {
		t.Error("should be filtered out by before='2026-02-01'")
	}
}

func TestResolveSupersededResults(t *testing.T) {
	h, s := mustHandler(t)

	l1 := &models.Learning{Content: "original", Category: "decision", Source: "user_stated"}
	id1, _ := s.InsertLearning(l1)

	l2 := &models.Learning{Content: "replacement", Category: "decision", Source: "user_stated"}
	id2, _ := s.InsertLearning(l2)

	s.SupersedeLearning(id1, id2, "newer approach")

	results := []embedding.RankedResult{
		{ID: fmt.Sprintf("%d", id1), Score: 0.9},
		{ID: fmt.Sprintf("%d", id2), Score: 0.8},
	}

	resolved := h.resolveSupersededResults(results)

	// Superseded result should be replaced by its successor
	for _, r := range resolved {
		if r.ID == fmt.Sprintf("%d", id1) {
			t.Error("superseded learning should not appear in results")
		}
	}
}

func TestEnrichRankedResults_Empty(t *testing.T) {
	h, _ := mustHandler(t)
	enriched := h.enrichRankedResults(nil)
	if len(enriched) != 0 {
		t.Errorf("expected empty, got %d", len(enriched))
	}
}
