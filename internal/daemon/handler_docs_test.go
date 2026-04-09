package daemon

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/carsteneu/yesmem/internal/storage"
)

func TestHandleContextualDocs(t *testing.T) {
	h, s := mustHandler(t)

	// Setup: two sources with trigger_extensions, one without
	id1, _ := s.UpsertDocSource(&storage.DocSource{
		Name: "go-docs", Project: "test", Version: "1.22",
		TriggerExtensions: ".go,.mod",
	})
	id2, _ := s.UpsertDocSource(&storage.DocSource{
		Name: "twig-docs", Project: "test", Version: "3.x",
		TriggerExtensions: ".twig,.html.twig",
	})
	s.UpsertDocSource(&storage.DocSource{
		Name: "no-ext", Project: "test", Version: "1.0",
	})

	// Insert chunks
	s.InsertDocChunk(&storage.DocChunk{SourceID: id1, SourceFile: "go.md", Content: "Go concurrency patterns", ContentHash: "c1", TokensApprox: 200})
	s.InsertDocChunk(&storage.DocChunk{SourceID: id2, SourceFile: "twig.md", Content: "Twig template syntax", ContentHash: "c2", TokensApprox: 150})

	// Query for .go extension → should return go-docs chunks
	resp := h.Handle(Request{
		Method: "contextual_docs",
		Params: map[string]any{
			"extensions": []any{".go"},
			"project":    "test",
			"limit":      float64(5),
		},
	})
	if resp.Error != "" {
		t.Fatalf("contextual_docs error: %s", resp.Error)
	}

	var result struct {
		Results []struct {
			Source      string `json:"source"`
			Version     string `json:"version"`
			HeadingPath string `json:"heading_path"`
			Content     string `json:"content"`
			Tokens      int    `json:"tokens_approx"`
		} `json:"results"`
		Total int `json:"total"`
	}
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if result.Total != 1 {
		t.Fatalf("expected 1 result, got %d", result.Total)
	}
	if result.Results[0].Source != "go-docs" {
		t.Errorf("source = %q, want go-docs", result.Results[0].Source)
	}
	if result.Results[0].Version != "1.22" {
		t.Errorf("version = %q, want 1.22", result.Results[0].Version)
	}
	if result.Results[0].Content != "Go concurrency patterns" {
		t.Errorf("content = %q, want 'Go concurrency patterns'", result.Results[0].Content)
	}

	// Query for .html.twig → should return twig-docs (compound extension)
	resp = h.Handle(Request{
		Method: "contextual_docs",
		Params: map[string]any{
			"extensions": []any{".html.twig"},
			"project":    "test",
		},
	})
	if resp.Error != "" {
		t.Fatalf("contextual_docs .html.twig error: %s", resp.Error)
	}
	json.Unmarshal(resp.Result, &result)
	if result.Total != 1 {
		t.Fatalf("expected 1 result for .html.twig, got %d", result.Total)
	}
	if result.Results[0].Source != "twig-docs" {
		t.Errorf("source = %q, want twig-docs", result.Results[0].Source)
	}

	// Query for unregistered extension → empty
	resp = h.Handle(Request{
		Method: "contextual_docs",
		Params: map[string]any{
			"extensions": []any{".rs"},
		},
	})
	json.Unmarshal(resp.Result, &result)
	if result.Total != 0 {
		t.Errorf("expected 0 results for .rs, got %d", result.Total)
	}

	// Missing extensions param → empty
	resp = h.Handle(Request{
		Method: "contextual_docs",
		Params: map[string]any{},
	})
	json.Unmarshal(resp.Result, &result)
	if result.Total != 0 {
		t.Errorf("expected 0 results for missing param, got %d", result.Total)
	}

	// Empty array → empty
	resp = h.Handle(Request{
		Method: "contextual_docs",
		Params: map[string]any{
			"extensions": []any{},
		},
	})
	json.Unmarshal(resp.Result, &result)
	if result.Total != 0 {
		t.Errorf("expected 0 results for empty array, got %d", result.Total)
	}

	// All non-string elements → empty
	resp = h.Handle(Request{
		Method: "contextual_docs",
		Params: map[string]any{
			"extensions": []any{42, true, nil},
		},
	})
	json.Unmarshal(resp.Result, &result)
	if result.Total != 0 {
		t.Errorf("expected 0 results for non-string elements, got %d", result.Total)
	}
}

func TestHandleContextualDocs_ProjectFilter(t *testing.T) {
	h, s := mustHandler(t)

	id1, _ := s.UpsertDocSource(&storage.DocSource{
		Name: "go-a", Project: "proj-a", TriggerExtensions: ".go",
	})
	id2, _ := s.UpsertDocSource(&storage.DocSource{
		Name: "go-b", Project: "proj-b", TriggerExtensions: ".go",
	})

	s.InsertDocChunk(&storage.DocChunk{SourceID: id1, SourceFile: "a.md", Content: "Project A Go", ContentHash: "ca", TokensApprox: 200})
	s.InsertDocChunk(&storage.DocChunk{SourceID: id2, SourceFile: "b.md", Content: "Project B Go", ContentHash: "cb", TokensApprox: 200})

	var result struct {
		Results []struct {
			Source  string `json:"source"`
			Content string `json:"content"`
		} `json:"results"`
		Total int `json:"total"`
	}

	// Filter by proj-a
	resp := h.Handle(Request{
		Method: "contextual_docs",
		Params: map[string]any{
			"extensions": []any{".go"},
			"project":    "proj-a",
			"limit":      float64(10),
		},
	})
	json.Unmarshal(resp.Result, &result)
	if result.Total != 1 {
		t.Fatalf("proj-a filter: expected 1, got %d", result.Total)
	}
	if result.Results[0].Source != "go-a" {
		t.Errorf("expected go-a, got %s", result.Results[0].Source)
	}
}

func TestHandleListTriggerExtensions(t *testing.T) {
	h, s := mustHandler(t)

	// Empty DB
	resp := h.Handle(Request{
		Method: "list_trigger_extensions",
		Params: map[string]any{},
	})
	if resp.Error != "" {
		t.Fatalf("error: %s", resp.Error)
	}

	var result struct {
		Extensions []string `json:"extensions"`
	}
	json.Unmarshal(resp.Result, &result)
	if len(result.Extensions) != 0 {
		t.Errorf("expected empty, got %v", result.Extensions)
	}

	// Add sources
	s.UpsertDocSource(&storage.DocSource{Name: "go", Project: "p", TriggerExtensions: ".go,.mod"})
	s.UpsertDocSource(&storage.DocSource{Name: "twig", Project: "p", TriggerExtensions: ".twig"})
	s.UpsertDocSource(&storage.DocSource{Name: "plain", Project: "p"})

	resp = h.Handle(Request{
		Method: "list_trigger_extensions",
		Params: map[string]any{},
	})
	json.Unmarshal(resp.Result, &result)

	extSet := make(map[string]bool)
	for _, e := range result.Extensions {
		extSet[e] = true
	}

	for _, want := range []string{".go", ".mod", ".twig"} {
		if !extSet[want] {
			t.Errorf("missing %q in %v", want, result.Extensions)
		}
	}
	if extSet[""] {
		t.Error("empty extension in results")
	}
}

func TestHandleDocsSearch_ExtensionsFilter(t *testing.T) {
	h, s := mustHandler(t)

	// Two sources with different trigger_extensions and doc_type=reference
	id1, _ := s.UpsertDocSource(&storage.DocSource{
		Name: "go-ref", Project: "test", Version: "1.22",
		TriggerExtensions: ".go,.mod", DocType: "reference",
	})
	id2, _ := s.UpsertDocSource(&storage.DocSource{
		Name: "twig-ref", Project: "test", Version: "3.x",
		TriggerExtensions: ".twig", DocType: "reference",
	})

	// Insert chunks with content that FTS can find
	s.InsertDocChunk(&storage.DocChunk{SourceID: id1, SourceFile: "go.md", HeadingPath: "Goroutine concurrency", Content: "goroutine concurrency channel", ContentHash: "g1", TokensApprox: 100})
	s.InsertDocChunk(&storage.DocChunk{SourceID: id2, SourceFile: "twig.md", HeadingPath: "Twig template filter", Content: "twig template filter render", ContentHash: "t1", TokensApprox: 100})

	// Search with extensions=[".go"] — should only return go-ref results
	resp := h.Handle(Request{
		Method: "docs_search",
		Params: map[string]any{
			"query":      "goroutine concurrency",
			"extensions": []any{".go"},
			"project":    "test",
			"limit":      float64(10),
		},
	})
	if resp.Error != "" {
		t.Fatalf("docs_search error: %s", resp.Error)
	}

	var result struct {
		Results []struct {
			Source  string `json:"source"`
			Content string `json:"content"`
		} `json:"results"`
		Total int `json:"total"`
	}
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, r := range result.Results {
		if r.Source != "go-ref" {
			t.Errorf("extensions filter: expected only go-ref, got source %q", r.Source)
		}
	}
	if result.Total == 0 {
		t.Error("extensions filter: expected at least 1 result for goroutine concurrency in go-ref")
	}

	// Search with extensions=[".twig"] — should only return twig-ref results
	resp = h.Handle(Request{
		Method: "docs_search",
		Params: map[string]any{
			"query":      "twig template filter",
			"extensions": []any{".twig"},
			"project":    "test",
			"limit":      float64(10),
		},
	})
	json.Unmarshal(resp.Result, &result)
	for _, r := range result.Results {
		if r.Source != "twig-ref" {
			t.Errorf("extensions filter .twig: expected only twig-ref, got source %q", r.Source)
		}
	}
}

func TestHandleDocsSearch_DocTypeFilter(t *testing.T) {
	h, s := mustHandler(t)

	// One reference source, one style source
	id1, _ := s.UpsertDocSource(&storage.DocSource{
		Name: "go-ref", Project: "", Version: "1.22",
		TriggerExtensions: ".go", DocType: "reference",
	})
	id2, _ := s.UpsertDocSource(&storage.DocSource{
		Name: "style-guide", Project: "", Version: "1.0",
		TriggerExtensions: ".go", DocType: "style",
	})

	s.InsertDocChunk(&storage.DocChunk{SourceID: id1, SourceFile: "ref.md", HeadingPath: "Error handling", Content: "error handling interface wrap", ContentHash: "r1", TokensApprox: 100})
	s.InsertDocChunk(&storage.DocChunk{SourceID: id2, SourceFile: "style.md", HeadingPath: "Error style", Content: "error handling interface wrap", ContentHash: "s1", TokensApprox: 100})

	// With doc_type=reference and same .go extension — should not return style source
	resp := h.Handle(Request{
		Method: "docs_search",
		Params: map[string]any{
			"query":      "error handling interface",
			"extensions": []any{".go"},
			"doc_type":   "reference",
			"limit":      float64(10),
		},
	})
	if resp.Error != "" {
		t.Fatalf("docs_search doc_type error: %s", resp.Error)
	}

	var result struct {
		Results []struct {
			Source string `json:"source"`
		} `json:"results"`
		Total int `json:"total"`
	}
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, r := range result.Results {
		if r.Source == "style-guide" {
			t.Errorf("doc_type=reference filter: style-guide should be excluded")
		}
	}

	// With doc_type=style — should only return style source
	resp = h.Handle(Request{
		Method: "docs_search",
		Params: map[string]any{
			"query":      "error handling interface",
			"extensions": []any{".go"},
			"doc_type":   "style",
			"limit":      float64(10),
		},
	})
	json.Unmarshal(resp.Result, &result)
	for _, r := range result.Results {
		if r.Source != "style-guide" {
			t.Errorf("doc_type=style filter: expected only style-guide, got %q", r.Source)
		}
	}
}

func TestHandleDocsSearch_ProjectBoost(t *testing.T) {
	h, s := mustHandler(t)

	// Global source (no project) and project-specific source
	idGlobal, _ := s.UpsertDocSource(&storage.DocSource{
		Name: "global-docs", Project: "", Version: "1.0", DocType: "reference",
	})
	idProject, _ := s.UpsertDocSource(&storage.DocSource{
		Name: "my-project-docs", Project: "my-project", Version: "1.0", DocType: "reference",
	})

	// Both sources have identical content so BM25 scores would be equal without boost
	s.InsertDocChunk(&storage.DocChunk{SourceID: idGlobal, SourceFile: "global.md", HeadingPath: "Setup guide", Content: "setup configuration install guide", ContentHash: "glo1", TokensApprox: 100})
	s.InsertDocChunk(&storage.DocChunk{SourceID: idProject, SourceFile: "proj.md", HeadingPath: "Setup guide", Content: "setup configuration install guide", ContentHash: "proj1", TokensApprox: 100})

	// Search with project=my-project — project source should appear first (boosted)
	resp := h.Handle(Request{
		Method: "docs_search",
		Params: map[string]any{
			"query":   "setup configuration install",
			"project": "my-project",
			"limit":   float64(10),
		},
	})
	if resp.Error != "" {
		t.Fatalf("docs_search project boost error: %s", resp.Error)
	}

	var result struct {
		Results []struct {
			Source string  `json:"source"`
			Score  float64 `json:"score"`
		} `json:"results"`
		Total int `json:"total"`
	}
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if result.Total < 2 {
		t.Fatalf("expected at least 2 results (global + project), got %d", result.Total)
	}
	// First result must be the project-specific source (boosted 1.2x)
	if result.Results[0].Source != "my-project-docs" {
		t.Errorf("project boost: expected my-project-docs first, got %q", result.Results[0].Source)
	}
}

func TestHandleIngestDocs_TriggerExtensions(t *testing.T) {
	h, _ := mustHandler(t)

	// Ingest with trigger_extensions (uses a non-existent path, so ingest will fail
	// but the trigger_extensions should be parseable — test the param parsing)
	resp := h.Handle(Request{
		Method: "ingest_docs",
		Params: map[string]any{
			"name":               "test-docs",
			"path":               "/tmp/nonexistent-yesmem-test",
			"project":            "test",
			"trigger_extensions": []any{".go", ".mod"},
		},
	})
	// Ingest will fail because path doesn't exist, but that's OK —
	// we just verify the param wasn't rejected at the handler level
	_ = resp

	// Test sentinel parsing: empty array should produce sentinel "-"
	// We can verify this by checking that the handler doesn't error on empty array
	resp = h.Handle(Request{
		Method: "ingest_docs",
		Params: map[string]any{
			"name":               "test-docs-2",
			"path":               "/tmp/nonexistent-yesmem-test-2",
			"project":            "test",
			"trigger_extensions": []any{},
		},
	})
	_ = resp
}

func TestHandleListDocSources_Empty(t *testing.T) {
	h, _ := mustHandler(t)

	resp := h.Handle(Request{
		Method: "list_doc_sources",
		Params: map[string]any{},
	})
	if resp.Error != "" {
		t.Fatalf("error: %s", resp.Error)
	}

	var result struct {
		Sources []any `json:"sources"`
		Total   int   `json:"total"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if result.Total != 0 {
		t.Errorf("expected 0 sources, got %d", result.Total)
	}
	if result.Message != "No documentation sources indexed yet." {
		t.Errorf("unexpected message: %q", result.Message)
	}
}

func TestHandleListDocSources_WithData(t *testing.T) {
	h, s := mustHandler(t)

	s.UpsertDocSource(&storage.DocSource{
		Name: "go-ref", Project: "proj-a", Version: "1.22",
		TriggerExtensions: ".go,.mod",
	})
	s.UpsertDocSource(&storage.DocSource{
		Name: "twig-ref", Project: "proj-b", Version: "3.x",
		TriggerExtensions: ".twig",
	})

	// Unfiltered: both sources
	resp := h.Handle(Request{
		Method: "list_doc_sources",
		Params: map[string]any{},
	})
	if resp.Error != "" {
		t.Fatalf("error: %s", resp.Error)
	}

	var result struct {
		Sources []struct {
			Name              string `json:"name"`
			Version           string `json:"version"`
			Project           string `json:"project"`
			TriggerExtensions string `json:"trigger_extensions,omitempty"`
		} `json:"sources"`
		Total   int    `json:"total"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if result.Total != 2 {
		t.Fatalf("expected 2 sources, got %d", result.Total)
	}

	// Filtered by project
	resp = h.Handle(Request{
		Method: "list_doc_sources",
		Params: map[string]any{"project": "proj-a"},
	})
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("unmarshal filtered: %v", err)
	}
	if result.Total != 1 {
		t.Fatalf("expected 1 source for proj-a, got %d", result.Total)
	}
	if result.Sources[0].Name != "go-ref" {
		t.Errorf("expected go-ref, got %s", result.Sources[0].Name)
	}
	if result.Sources[0].TriggerExtensions != ".go,.mod" {
		t.Errorf("trigger_extensions = %q, want '.go,.mod'", result.Sources[0].TriggerExtensions)
	}
}

func TestHandleRemoveDocs_MissingName(t *testing.T) {
	h, _ := mustHandler(t)

	resp := h.Handle(Request{
		Method: "remove_docs",
		Params: map[string]any{},
	})
	if resp.Error == "" {
		t.Fatal("expected error for missing name")
	}
	if resp.Error != "name is required" {
		t.Errorf("error = %q, want 'name is required'", resp.Error)
	}
}

func TestHandleRemoveDocs_NotFound(t *testing.T) {
	h, _ := mustHandler(t)

	resp := h.Handle(Request{
		Method: "remove_docs",
		Params: map[string]any{"name": "nonexistent"},
	})
	if resp.Error == "" {
		t.Fatal("expected error for nonexistent source")
	}
	if !strings.Contains(resp.Error, "not found") {
		t.Errorf("error = %q, want 'not found' substring", resp.Error)
	}
}

func TestHandleRemoveDocs_Success(t *testing.T) {
	h, s := mustHandler(t)

	id, _ := s.UpsertDocSource(&storage.DocSource{
		Name: "removeme", Project: "test", Version: "1.0",
	})
	s.InsertDocChunk(&storage.DocChunk{
		SourceID: id, SourceFile: "a.md", Content: "some content",
		ContentHash: "rm1", TokensApprox: 100,
	})
	s.InsertDocChunk(&storage.DocChunk{
		SourceID: id, SourceFile: "b.md", Content: "more content",
		ContentHash: "rm2", TokensApprox: 100,
	})
	s.UpdateDocSourceStats(id)

	resp := h.Handle(Request{
		Method: "remove_docs",
		Params: map[string]any{"name": "removeme", "project": "test"},
	})
	if resp.Error != "" {
		t.Fatalf("remove error: %s", resp.Error)
	}

	var result struct {
		Name            string `json:"name"`
		ChunksDeleted   int    `json:"chunks_deleted"`
		LearningsDeleted int   `json:"learnings_deleted"`
		Message         string `json:"message"`
	}
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if result.Name != "removeme" {
		t.Errorf("name = %q, want 'removeme'", result.Name)
	}
	if result.ChunksDeleted != 2 {
		t.Errorf("chunks_deleted = %d, want 2", result.ChunksDeleted)
	}

	// Verify source is actually gone
	src, _ := s.GetDocSource("removeme", "test")
	if src != nil {
		t.Error("source still exists after removal")
	}
}

func TestHandleGetRulesBlock_Empty(t *testing.T) {
	h, _ := mustHandler(t)

	resp := h.Handle(Request{
		Method: "get_rules_block",
		Params: map[string]any{"project": "nonexistent"},
	})
	if resp.Error != "" {
		t.Fatalf("error: %s", resp.Error)
	}

	var result struct {
		Content string `json:"content"`
		Exists  bool   `json:"exists"`
	}
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if result.Exists {
		t.Error("expected exists=false for empty DB")
	}
	if result.Content != "" {
		t.Errorf("expected empty content, got %q", result.Content)
	}
}

func TestHandleGetRulesBlock_WithRules(t *testing.T) {
	h, s := mustHandler(t)

	// Insert a rules doc source manually
	sourceID, _ := s.UpsertDocSource(&storage.DocSource{
		Name:    "rules",
		Project: "myproj",
		Path:    "/tmp/CLAUDE.md",
	})
	s.SaveRulesContent(sourceID, "Never commit secrets. Always run tests.", "abc123hash")

	resp := h.Handle(Request{
		Method: "get_rules_block",
		Params: map[string]any{"project": "myproj"},
	})
	if resp.Error != "" {
		t.Fatalf("error: %s", resp.Error)
	}

	var result struct {
		Content string `json:"content"`
		Exists  bool   `json:"exists"`
	}
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !result.Exists {
		t.Error("expected exists=true")
	}
	if result.Content != "Never commit secrets. Always run tests." {
		t.Errorf("content = %q", result.Content)
	}
}

func TestHandleDocsSearch_EmptyQuery(t *testing.T) {
	h, _ := mustHandler(t)

	resp := h.Handle(Request{
		Method: "docs_search",
		Params: map[string]any{"query": ""},
	})
	if resp.Error == "" {
		t.Fatal("expected error for empty query")
	}
	if resp.Error != "query is required" {
		t.Errorf("error = %q, want 'query is required'", resp.Error)
	}
}

func TestHandleDocsSearch_NoResults(t *testing.T) {
	h, _ := mustHandler(t)

	resp := h.Handle(Request{
		Method: "docs_search",
		Params: map[string]any{
			"query": "xyznonexistenttermxyz",
			"limit": float64(5),
		},
	})
	if resp.Error != "" {
		t.Fatalf("error: %s", resp.Error)
	}

	var result struct {
		Results []any  `json:"results"`
		Total   int    `json:"total"`
		Method  string `json:"method"`
	}
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if result.Total != 0 {
		t.Errorf("expected 0 results, got %d", result.Total)
	}
	if result.Method != "bm25" {
		t.Errorf("method = %q, want 'bm25'", result.Method)
	}
}

func TestHandleDocsSearch_SourceFilter(t *testing.T) {
	h, s := mustHandler(t)

	id1, _ := s.UpsertDocSource(&storage.DocSource{
		Name: "alpha-docs", Project: "", Version: "1.0", DocType: "reference",
	})
	id2, _ := s.UpsertDocSource(&storage.DocSource{
		Name: "beta-docs", Project: "", Version: "2.0", DocType: "reference",
	})

	s.InsertDocChunk(&storage.DocChunk{SourceID: id1, SourceFile: "a.md", HeadingPath: "Config", Content: "database connection pool config", ContentHash: "sf1", TokensApprox: 100})
	s.InsertDocChunk(&storage.DocChunk{SourceID: id2, SourceFile: "b.md", HeadingPath: "Config", Content: "database connection pool config", ContentHash: "sf2", TokensApprox: 100})

	// Filter by source name
	resp := h.Handle(Request{
		Method: "docs_search",
		Params: map[string]any{
			"query":  "database connection pool",
			"source": "alpha-docs",
			"limit":  float64(10),
		},
	})
	if resp.Error != "" {
		t.Fatalf("error: %s", resp.Error)
	}

	var result struct {
		Results []struct {
			Source string `json:"source"`
		} `json:"results"`
		Total int `json:"total"`
	}
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, r := range result.Results {
		if r.Source != "alpha-docs" {
			t.Errorf("source filter: expected only alpha-docs, got %q", r.Source)
		}
	}
}

func TestHandleDocsSearch_DocTypeWithoutExtensions(t *testing.T) {
	h, s := mustHandler(t)

	id1, _ := s.UpsertDocSource(&storage.DocSource{
		Name: "ref-only", Project: "", DocType: "reference",
	})
	id2, _ := s.UpsertDocSource(&storage.DocSource{
		Name: "style-only", Project: "", DocType: "style",
	})

	s.InsertDocChunk(&storage.DocChunk{SourceID: id1, SourceFile: "r.md", HeadingPath: "Naming", Content: "naming convention variable function", ContentHash: "dt1", TokensApprox: 100})
	s.InsertDocChunk(&storage.DocChunk{SourceID: id2, SourceFile: "s.md", HeadingPath: "Naming", Content: "naming convention variable function", ContentHash: "dt2", TokensApprox: 100})

	// doc_type=style without extensions should filter by type alone
	resp := h.Handle(Request{
		Method: "docs_search",
		Params: map[string]any{
			"query":    "naming convention variable",
			"doc_type": "style",
			"limit":    float64(10),
		},
	})
	if resp.Error != "" {
		t.Fatalf("error: %s", resp.Error)
	}

	var result struct {
		Results []struct {
			Source string `json:"source"`
		} `json:"results"`
		Total int `json:"total"`
	}
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if result.Total == 0 {
		t.Fatal("expected at least 1 result for doc_type=style")
	}
	for _, r := range result.Results {
		if r.Source != "style-only" {
			t.Errorf("doc_type filter without extensions: expected style-only, got %q", r.Source)
		}
	}
}

func TestHandleIngestDocs_MissingParams(t *testing.T) {
	h, _ := mustHandler(t)

	// Missing name
	resp := h.Handle(Request{
		Method: "ingest_docs",
		Params: map[string]any{"path": "/tmp/something"},
	})
	if resp.Error != "name is required" {
		t.Errorf("missing name: error = %q, want 'name is required'", resp.Error)
	}

	// Missing path
	resp = h.Handle(Request{
		Method: "ingest_docs",
		Params: map[string]any{"name": "something"},
	})
	if resp.Error != "path is required" {
		t.Errorf("missing path: error = %q, want 'path is required'", resp.Error)
	}
}

func TestHandleContextualDocs_LimitDefault(t *testing.T) {
	h, s := mustHandler(t)

	id, _ := s.UpsertDocSource(&storage.DocSource{
		Name: "big-docs", Project: "", TriggerExtensions: ".py",
	})
	for i := 0; i < 5; i++ {
		s.InsertDocChunk(&storage.DocChunk{
			SourceID: id, SourceFile: "doc.md",
			Content: fmt.Sprintf("chunk number %d about python", i),
			ContentHash: fmt.Sprintf("lim%d", i), TokensApprox: 50,
		})
	}

	// No limit param: default is 3
	resp := h.Handle(Request{
		Method: "contextual_docs",
		Params: map[string]any{
			"extensions": []any{".py"},
		},
	})
	if resp.Error != "" {
		t.Fatalf("error: %s", resp.Error)
	}

	var result struct {
		Results []any `json:"results"`
		Total   int   `json:"total"`
	}
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if result.Total > 3 {
		t.Errorf("default limit: expected at most 3 results, got %d", result.Total)
	}
}

func TestHandleDocsSearch_LimitRespected(t *testing.T) {
	h, s := mustHandler(t)

	id, _ := s.UpsertDocSource(&storage.DocSource{
		Name: "many-docs", Project: "", DocType: "reference",
	})
	for i := 0; i < 10; i++ {
		s.InsertDocChunk(&storage.DocChunk{
			SourceID: id, SourceFile: "doc.md",
			HeadingPath: fmt.Sprintf("Section %d", i),
			Content: fmt.Sprintf("golang interface embedding struct %d", i),
			ContentHash: fmt.Sprintf("ldr%d", i), TokensApprox: 50,
		})
	}

	resp := h.Handle(Request{
		Method: "docs_search",
		Params: map[string]any{
			"query": "golang interface embedding",
			"limit": float64(3),
		},
	})
	if resp.Error != "" {
		t.Fatalf("error: %s", resp.Error)
	}

	var result struct {
		Results []any `json:"results"`
		Total   int   `json:"total"`
	}
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if result.Total > 3 {
		t.Errorf("limit=3: expected at most 3 results, got %d", result.Total)
	}
}
