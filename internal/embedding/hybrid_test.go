package embedding

import (
	"testing"
)

func TestRRFMerge(t *testing.T) {
	// BM25 results: doc1 rank1, doc2 rank2, doc3 rank3
	bm25 := []RankedResult{
		{ID: "doc1", Score: 0.9},
		{ID: "doc2", Score: 0.7},
		{ID: "doc3", Score: 0.5},
	}
	// Vector results: doc2 rank1, doc4 rank2, doc1 rank3
	vector := []RankedResult{
		{ID: "doc2", Score: 0.95},
		{ID: "doc4", Score: 0.80},
		{ID: "doc1", Score: 0.60},
	}

	results := RRFMerge(bm25, vector, 60, 5)

	// doc1 appears in both (rank 1 + rank 3) → highest RRF
	// doc2 appears in both (rank 2 + rank 1) → second highest
	if len(results) < 2 {
		t.Fatalf("expected at least 2 results, got %d", len(results))
	}

	// Both doc1 and doc2 should be in top results (both appear in both lists)
	topIDs := make(map[string]bool)
	for _, r := range results[:2] {
		topIDs[r.ID] = true
	}
	if !topIDs["doc1"] || !topIDs["doc2"] {
		t.Errorf("expected doc1 and doc2 in top 2, got %v", results[:2])
	}

	// doc4 should also appear (vector-only)
	found := false
	for _, r := range results {
		if r.ID == "doc4" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected doc4 in results (vector-only hit)")
	}
}

func TestRRFMergeLimit(t *testing.T) {
	bm25 := []RankedResult{
		{ID: "a", Score: 1.0},
		{ID: "b", Score: 0.9},
		{ID: "c", Score: 0.8},
	}
	vector := []RankedResult{
		{ID: "d", Score: 1.0},
		{ID: "e", Score: 0.9},
	}

	results := RRFMerge(bm25, vector, 60, 3)
	if len(results) != 3 {
		t.Fatalf("expected 3 results (limit), got %d", len(results))
	}
}

func TestRRFMergeEmpty(t *testing.T) {
	results := RRFMerge(nil, nil, 60, 10)
	if len(results) != 0 {
		t.Fatalf("expected 0 results for empty inputs, got %d", len(results))
	}

	// Only BM25
	bm25 := []RankedResult{{ID: "a", Score: 1.0}}
	results = RRFMerge(bm25, nil, 60, 10)
	if len(results) != 1 || results[0].ID != "a" {
		t.Fatalf("expected 1 result from BM25-only, got %v", results)
	}
}

func TestRRFMergeSourceTracking(t *testing.T) {
	bm25 := []RankedResult{{ID: "doc1", Score: 0.9}}
	vector := []RankedResult{{ID: "doc2", Score: 0.8}}

	results := RRFMerge(bm25, vector, 60, 10)

	sourceMap := make(map[string]string)
	for _, r := range results {
		sourceMap[r.ID] = r.Source
	}

	if sourceMap["doc1"] != "keyword" {
		t.Errorf("doc1 should be keyword source, got %q", sourceMap["doc1"])
	}
	if sourceMap["doc2"] != "semantic" {
		t.Errorf("doc2 should be semantic source, got %q", sourceMap["doc2"])
	}

	// Now test overlap
	bm25Both := []RankedResult{{ID: "shared", Score: 0.9}}
	vecBoth := []RankedResult{{ID: "shared", Score: 0.8}}
	results = RRFMerge(bm25Both, vecBoth, 60, 10)
	if results[0].Source != "hybrid" {
		t.Errorf("shared doc should be hybrid source, got %q", results[0].Source)
	}
}
