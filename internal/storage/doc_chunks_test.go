package storage

import (
	"testing"
	"time"

	"github.com/carsteneu/yesmem/internal/models"
)

func TestUpsertDocSource_InsertAndUpdate(t *testing.T) {
	s := mustOpen(t)

	ds := &DocSource{
		Name:    "go-stdlib",
		Version: "1.22",
		Path:    "/docs/go",
		URL:     "https://pkg.go.dev",
		Project: "memory",
	}

	id1, err := s.UpsertDocSource(ds)
	if err != nil {
		t.Fatalf("insert doc_source: %v", err)
	}
	if id1 <= 0 {
		t.Fatal("expected positive ID")
	}

	// Update same name+project — should preserve ID
	ds.Version = "1.23"
	ds.URL = "https://pkg.go.dev/std"
	id2, err := s.UpsertDocSource(ds)
	if err != nil {
		t.Fatalf("update doc_source: %v", err)
	}
	if id2 != id1 {
		t.Errorf("expected same ID %d on update, got %d", id1, id2)
	}

	// Verify updated fields
	got, err := s.GetDocSource("go-stdlib", "memory")
	if err != nil {
		t.Fatalf("get doc_source: %v", err)
	}
	if got.Version != "1.23" {
		t.Errorf("version = %q, want %q", got.Version, "1.23")
	}
	if got.URL != "https://pkg.go.dev/std" {
		t.Errorf("url = %q, want %q", got.URL, "https://pkg.go.dev/std")
	}
	if got.Name != "go-stdlib" {
		t.Errorf("name = %q, want %q", got.Name, "go-stdlib")
	}
}

func TestUpsertDocSource_PreservesTriggerExtensionsOnReIngest(t *testing.T) {
	s := mustOpen(t)

	// Initial ingest with trigger_extensions
	ds := &DocSource{Name: "go-docs", Project: "test", Version: "1.22", TriggerExtensions: ".go,.mod"}
	id1, err := s.UpsertDocSource(ds)
	if err != nil {
		t.Fatal(err)
	}

	got, _ := s.GetDocSource("go-docs", "test")
	if got.TriggerExtensions != ".go,.mod" {
		t.Fatalf("initial: want .go,.mod, got %q", got.TriggerExtensions)
	}

	// Re-ingest WITHOUT trigger_extensions (simulates ingest.Run with empty TriggerExtensions)
	ds2 := &DocSource{Name: "go-docs", Project: "test", Version: "1.23", TriggerExtensions: ""}
	id2, err := s.UpsertDocSource(ds2)
	if err != nil {
		t.Fatal(err)
	}
	if id2 != id1 {
		t.Errorf("expected same ID on update")
	}

	got, _ = s.GetDocSource("go-docs", "test")
	if got.TriggerExtensions != ".go,.mod" {
		t.Fatalf("re-ingest cleared extensions: want .go,.mod, got %q", got.TriggerExtensions)
	}
	if got.Version != "1.23" {
		t.Fatalf("version not updated: want 1.23, got %q", got.Version)
	}

	// Re-ingest WITH new trigger_extensions — should overwrite
	ds3 := &DocSource{Name: "go-docs", Project: "test", Version: "1.24", TriggerExtensions: ".go,.mod,.sum"}
	s.UpsertDocSource(ds3)

	got, _ = s.GetDocSource("go-docs", "test")
	if got.TriggerExtensions != ".go,.mod,.sum" {
		t.Fatalf("explicit extensions not set: want .go,.mod,.sum, got %q", got.TriggerExtensions)
	}

	// Explicitly clear trigger_extensions with sentinel "-"
	ds4 := &DocSource{Name: "go-docs", Project: "test", Version: "1.25", TriggerExtensions: "-"}
	s.UpsertDocSource(ds4)

	got, _ = s.GetDocSource("go-docs", "test")
	if got.TriggerExtensions != "" {
		t.Fatalf("sentinel clear failed: want empty, got %q", got.TriggerExtensions)
	}
	if got.Version != "1.25" {
		t.Fatalf("version not updated after clear: want 1.25, got %q", got.Version)
	}
}

func TestListTriggerExtensions(t *testing.T) {
	s := mustOpen(t)

	// Empty DB → empty result
	exts, err := s.ListTriggerExtensions("")
	if err != nil {
		t.Fatal(err)
	}
	if len(exts) != 0 {
		t.Fatalf("expected empty, got %v", exts)
	}

	// Insert sources with various extensions
	s.UpsertDocSource(&DocSource{Name: "go-docs", Project: "proj-a", TriggerExtensions: ".go,.mod"})
	s.UpsertDocSource(&DocSource{Name: "twig-docs", Project: "proj-a", TriggerExtensions: ".twig,.html.twig"})
	s.UpsertDocSource(&DocSource{Name: "no-ext", Project: "proj-a", TriggerExtensions: ""})
	s.UpsertDocSource(&DocSource{Name: "py-docs", Project: "proj-b", TriggerExtensions: ".py"})

	// All projects
	exts, err = s.ListTriggerExtensions("")
	if err != nil {
		t.Fatal(err)
	}
	extSet := make(map[string]bool)
	for _, e := range exts {
		extSet[e] = true
	}
	for _, want := range []string{".go", ".mod", ".twig", ".html.twig", ".py"} {
		if !extSet[want] {
			t.Errorf("missing %q in %v", want, exts)
		}
	}

	// Project filter
	exts, err = s.ListTriggerExtensions("proj-b")
	if err != nil {
		t.Fatal(err)
	}
	if len(exts) != 1 || exts[0] != ".py" {
		t.Errorf("proj-b filter: want [.py], got %v", exts)
	}

	// Source with no extensions not included
	for _, e := range exts {
		if e == "" {
			t.Error("empty extension in results")
		}
	}
}

func TestListTriggerExtensions_SpacesInExtensions(t *testing.T) {
	s := mustOpen(t)

	s.UpsertDocSource(&DocSource{Name: "spacy", Project: "test", TriggerExtensions: " .go , .mod "})

	exts, err := s.ListTriggerExtensions("")
	if err != nil {
		t.Fatal(err)
	}
	extSet := make(map[string]bool)
	for _, e := range exts {
		extSet[e] = true
	}
	if !extSet[".go"] {
		t.Errorf("expected .go after TrimSpace, got %v", exts)
	}
	if !extSet[".mod"] {
		t.Errorf("expected .mod after TrimSpace, got %v", exts)
	}
}

func TestGetDocSource_NotFound(t *testing.T) {
	s := mustOpen(t)
	_, err := s.GetDocSource("nonexistent", "nope")
	if err == nil {
		t.Fatal("expected error for nonexistent doc_source")
	}
}

func TestListDocSources(t *testing.T) {
	s := mustOpen(t)

	s.UpsertDocSource(&DocSource{Name: "alpha", Project: "proj-a", Version: "1"})
	s.UpsertDocSource(&DocSource{Name: "beta", Project: "proj-a", Version: "2"})
	s.UpsertDocSource(&DocSource{Name: "gamma", Project: "proj-b", Version: "3"})

	// All sources
	all, err := s.ListDocSources("")
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	if len(all) != 3 {
		t.Errorf("expected 3 sources, got %d", len(all))
	}

	// Filter by project
	projA, err := s.ListDocSources("proj-a")
	if err != nil {
		t.Fatalf("list proj-a: %v", err)
	}
	if len(projA) != 2 {
		t.Errorf("expected 2 sources for proj-a, got %d", len(projA))
	}

	projB, err := s.ListDocSources("proj-b")
	if err != nil {
		t.Fatalf("list proj-b: %v", err)
	}
	if len(projB) != 1 {
		t.Errorf("expected 1 source for proj-b, got %d", len(projB))
	}
}

func TestDeleteDocSource(t *testing.T) {
	s := mustOpen(t)

	id, _ := s.UpsertDocSource(&DocSource{Name: "deleteme", Project: "test", Version: "1"})

	// Insert some chunks for this source
	s.InsertDocChunk(&DocChunk{SourceID: id, SourceFile: "f.md", SourceHash: "abc", Content: "chunk content", ContentHash: "h1"})
	s.InsertDocChunk(&DocChunk{SourceID: id, SourceFile: "f.md", SourceHash: "abc", Content: "chunk two", ContentHash: "h2"})

	// Delete source
	if _, err := s.DeleteDocSource("deleteme", "test"); err != nil {
		t.Fatalf("delete: %v", err)
	}

	// Source should be gone
	_, err := s.GetDocSource("deleteme", "test")
	if err == nil {
		t.Error("expected error after delete")
	}

	// Chunks should be gone
	chunks, err := s.GetDocChunksBySource(id)
	if err != nil {
		t.Fatalf("get chunks after delete: %v", err)
	}
	if len(chunks) != 0 {
		t.Errorf("expected 0 chunks after source delete, got %d", len(chunks))
	}
}

func TestInsertDocChunk_AndGetBySource(t *testing.T) {
	s := mustOpen(t)

	srcID, _ := s.UpsertDocSource(&DocSource{Name: "src", Project: "p", Version: "1"})

	chunks := []DocChunk{
		{SourceID: srcID, SourceFile: "a.md", SourceHash: "sha1", HeadingPath: "# Intro", SectionLevel: 1, Content: "Introduction text", ContentHash: "c1", TokensApprox: 10, Metadata: map[string]string{"lang": "en"}},
		{SourceID: srcID, SourceFile: "a.md", SourceHash: "sha1", HeadingPath: "# Intro > ## Details", SectionLevel: 2, Content: "Detail text here", ContentHash: "c2", TokensApprox: 15},
		{SourceID: srcID, SourceFile: "b.md", SourceHash: "sha2", HeadingPath: "# Setup", SectionLevel: 1, Content: "Setup instructions", ContentHash: "c3", TokensApprox: 12, Metadata: map[string]string{"os": "linux", "arch": "amd64"}},
	}

	for i := range chunks {
		id, err := s.InsertDocChunk(&chunks[i])
		if err != nil {
			t.Fatalf("insert chunk %d: %v", i, err)
		}
		if id <= 0 {
			t.Errorf("expected positive ID for chunk %d", i)
		}
	}

	// Retrieve by source
	got, err := s.GetDocChunksBySource(srcID)
	if err != nil {
		t.Fatalf("get by source: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 chunks, got %d", len(got))
	}

	// Verify fields of first chunk
	if got[0].SourceFile != "a.md" {
		t.Errorf("source_file = %q, want %q", got[0].SourceFile, "a.md")
	}
	if got[0].HeadingPath != "# Intro" {
		t.Errorf("heading_path = %q, want %q", got[0].HeadingPath, "# Intro")
	}
	if got[0].SectionLevel != 1 {
		t.Errorf("section_level = %d, want 1", got[0].SectionLevel)
	}
	if got[0].Content != "Introduction text" {
		t.Errorf("content = %q, want %q", got[0].Content, "Introduction text")
	}
	if got[0].TokensApprox != 10 {
		t.Errorf("tokens_approx = %d, want 10", got[0].TokensApprox)
	}
}

func TestGetDocChunksByFile(t *testing.T) {
	s := mustOpen(t)

	srcID, _ := s.UpsertDocSource(&DocSource{Name: "src", Project: "p", Version: "1"})

	s.InsertDocChunk(&DocChunk{SourceID: srcID, SourceFile: "config.md", SourceHash: "s1", Content: "Config section A", ContentHash: "ca"})
	s.InsertDocChunk(&DocChunk{SourceID: srcID, SourceFile: "config.md", SourceHash: "s1", Content: "Config section B", ContentHash: "cb"})
	s.InsertDocChunk(&DocChunk{SourceID: srcID, SourceFile: "install.md", SourceHash: "s2", Content: "Install guide", ContentHash: "ci"})

	got, err := s.GetDocChunksByFile("config.md")
	if err != nil {
		t.Fatalf("get by file: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("expected 2 chunks for config.md, got %d", len(got))
	}

	got2, err := s.GetDocChunksByFile("install.md")
	if err != nil {
		t.Fatalf("get by file install.md: %v", err)
	}
	if len(got2) != 1 {
		t.Errorf("expected 1 chunk for install.md, got %d", len(got2))
	}

	// Nonexistent file
	got3, err := s.GetDocChunksByFile("nope.md")
	if err != nil {
		t.Fatalf("get by file nope.md: %v", err)
	}
	if len(got3) != 0 {
		t.Errorf("expected 0 chunks for nope.md, got %d", len(got3))
	}
}

func TestDeleteDocChunksBySource(t *testing.T) {
	s := mustOpen(t)

	srcID, _ := s.UpsertDocSource(&DocSource{Name: "src", Project: "p", Version: "1"})
	s.InsertDocChunk(&DocChunk{SourceID: srcID, SourceFile: "a.md", SourceHash: "s1", Content: "chunk A", ContentHash: "ca"})
	s.InsertDocChunk(&DocChunk{SourceID: srcID, SourceFile: "b.md", SourceHash: "s2", Content: "chunk B", ContentHash: "cb"})

	if err := s.DeleteDocChunksBySource(srcID); err != nil {
		t.Fatalf("delete by source: %v", err)
	}

	got, _ := s.GetDocChunksBySource(srcID)
	if len(got) != 0 {
		t.Errorf("expected 0 chunks after delete, got %d", len(got))
	}

	// FTS should also be empty — search should return nothing
	results, err := s.SearchDocChunksFTS("chunk", "", "", "", "", 10, nil)
	if err != nil {
		t.Fatalf("search after delete: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 FTS results after delete, got %d", len(results))
	}
}

func TestDeleteDocChunksByFile(t *testing.T) {
	s := mustOpen(t)

	srcID, _ := s.UpsertDocSource(&DocSource{Name: "src", Project: "p", Version: "1"})
	s.InsertDocChunk(&DocChunk{SourceID: srcID, SourceFile: "keep.md", SourceHash: "s1", Content: "keep this", ContentHash: "ck"})
	s.InsertDocChunk(&DocChunk{SourceID: srcID, SourceFile: "remove.md", SourceHash: "s2", Content: "remove this", ContentHash: "cr"})

	if err := s.DeleteDocChunksByFile("remove.md"); err != nil {
		t.Fatalf("delete by file: %v", err)
	}

	kept, _ := s.GetDocChunksByFile("keep.md")
	if len(kept) != 1 {
		t.Errorf("expected 1 kept chunk, got %d", len(kept))
	}

	removed, _ := s.GetDocChunksByFile("remove.md")
	if len(removed) != 0 {
		t.Errorf("expected 0 removed chunks, got %d", len(removed))
	}
}

func TestUpdateDocSourceStats(t *testing.T) {
	s := mustOpen(t)

	srcID, _ := s.UpsertDocSource(&DocSource{Name: "src", Project: "p", Version: "1"})
	s.InsertDocChunk(&DocChunk{SourceID: srcID, SourceFile: "a.md", SourceHash: "s1", Content: "one", ContentHash: "c1"})
	s.InsertDocChunk(&DocChunk{SourceID: srcID, SourceFile: "b.md", SourceHash: "s2", Content: "two", ContentHash: "c2"})
	s.InsertDocChunk(&DocChunk{SourceID: srcID, SourceFile: "c.md", SourceHash: "s3", Content: "three", ContentHash: "c3"})

	if err := s.UpdateDocSourceStats(srcID); err != nil {
		t.Fatalf("update stats: %v", err)
	}

	got, _ := s.GetDocSource("src", "p")
	if got.ChunkCount != 3 {
		t.Errorf("chunk_count = %d, want 3", got.ChunkCount)
	}
}

func TestSearchDocChunksFTS(t *testing.T) {
	s := mustOpen(t)

	srcID, _ := s.UpsertDocSource(&DocSource{Name: "go-docs", Project: "memory", Version: "1.22"})

	s.InsertDocChunk(&DocChunk{SourceID: srcID, SourceFile: "goroutines.md", SourceHash: "s1", HeadingPath: "# Concurrency > ## Goroutines", SectionLevel: 2, Content: "Goroutines are lightweight threads managed by the Go runtime", ContentHash: "c1", TokensApprox: 20})
	s.InsertDocChunk(&DocChunk{SourceID: srcID, SourceFile: "channels.md", SourceHash: "s2", HeadingPath: "# Concurrency > ## Channels", SectionLevel: 2, Content: "Channels provide a way for goroutines to communicate with each other", ContentHash: "c2", TokensApprox: 25})
	s.InsertDocChunk(&DocChunk{SourceID: srcID, SourceFile: "errors.md", SourceHash: "s3", HeadingPath: "# Error Handling", SectionLevel: 1, Content: "Error handling in Go uses explicit return values instead of exceptions", ContentHash: "c3", TokensApprox: 18})

	// Search for "goroutines" — should match both concurrency chunks
	results, err := s.SearchDocChunksFTS("goroutines", "", "", "", "", 10, nil)
	if err != nil {
		t.Fatalf("search goroutines: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results for goroutines, got %d", len(results))
	}
	// First result should have better score (exact content match)
	if results[0].SourceFile != "goroutines.md" {
		t.Errorf("expected goroutines.md first, got %q", results[0].SourceFile)
	}
	// Verify joined fields
	if results[0].SourceName != "go-docs" {
		t.Errorf("source_name = %q, want %q", results[0].SourceName, "go-docs")
	}
	if results[0].Version != "1.22" {
		t.Errorf("version = %q, want %q", results[0].Version, "1.22")
	}

	// Search for "error" — should match only error chunk
	results, err = s.SearchDocChunksFTS("error", "", "", "", "", 10, nil)
	if err != nil {
		t.Fatalf("search error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result for error, got %d", len(results))
	}
	if results[0].SourceFile != "errors.md" {
		t.Errorf("expected errors.md, got %q", results[0].SourceFile)
	}

	// Search with source filter
	srcID2, _ := s.UpsertDocSource(&DocSource{Name: "other-docs", Project: "memory", Version: "1"})
	s.InsertDocChunk(&DocChunk{SourceID: srcID2, SourceFile: "other.md", SourceHash: "s4", Content: "goroutines in another context", ContentHash: "c4"})

	results, err = s.SearchDocChunksFTS("goroutines", "go-docs", "", "", "", 10, nil)
	if err != nil {
		t.Fatalf("search with source filter: %v", err)
	}
	// Should only return go-docs results, not other-docs
	for _, r := range results {
		if r.SourceName != "go-docs" {
			t.Errorf("expected only go-docs results, got source=%q", r.SourceName)
		}
	}

	// Search with section filter
	results, err = s.SearchDocChunksFTS("goroutines", "", "Channels", "", "", 10, nil)
	if err != nil {
		t.Fatalf("search with section filter: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result with Channels section filter, got %d", len(results))
	}
	if results[0].HeadingPath != "# Concurrency > ## Channels" {
		t.Errorf("heading_path = %q, want channels heading", results[0].HeadingPath)
	}

	// Empty query returns nil
	results, err = s.SearchDocChunksFTS("", "", "", "", "", 10, nil)
	if err != nil {
		t.Fatalf("empty search: %v", err)
	}
	if results != nil {
		t.Errorf("expected nil for empty query, got %d results", len(results))
	}
}

func TestDocChunkMetadataRoundTrip(t *testing.T) {
	s := mustOpen(t)

	srcID, _ := s.UpsertDocSource(&DocSource{Name: "src", Project: "p", Version: "1"})

	meta := map[string]string{
		"language": "go",
		"version":  "1.22",
		"author":   "test-user",
	}
	id, err := s.InsertDocChunk(&DocChunk{
		SourceID:    srcID,
		SourceFile:  "meta.md",
		SourceHash:  "ms",
		HeadingPath: "# Meta Test",
		Content:     "Content with metadata",
		ContentHash: "cm",
		Metadata:    meta,
	})
	if err != nil {
		t.Fatalf("insert with metadata: %v", err)
	}

	chunks, err := s.GetDocChunksBySource(srcID)
	if err != nil {
		t.Fatalf("get chunks: %v", err)
	}
	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(chunks))
	}

	got := chunks[0]
	if got.ID != id {
		t.Errorf("id = %d, want %d", got.ID, id)
	}
	if len(got.Metadata) != 3 {
		t.Fatalf("expected 3 metadata entries, got %d: %v", len(got.Metadata), got.Metadata)
	}
	if got.Metadata["language"] != "go" {
		t.Errorf("metadata[language] = %q, want %q", got.Metadata["language"], "go")
	}
	if got.Metadata["version"] != "1.22" {
		t.Errorf("metadata[version] = %q, want %q", got.Metadata["version"], "1.22")
	}
	if got.Metadata["author"] != "test-user" {
		t.Errorf("metadata[author] = %q, want %q", got.Metadata["author"], "test-user")
	}
}

func TestDocChunkNilMetadataRoundTrip(t *testing.T) {
	s := mustOpen(t)

	srcID, _ := s.UpsertDocSource(&DocSource{Name: "src", Project: "p", Version: "1"})

	// Insert without metadata (nil map)
	_, err := s.InsertDocChunk(&DocChunk{
		SourceID:    srcID,
		SourceFile:  "no-meta.md",
		SourceHash:  "nm",
		Content:     "No metadata here",
		ContentHash: "cn",
	})
	if err != nil {
		t.Fatalf("insert without metadata: %v", err)
	}

	chunks, _ := s.GetDocChunksBySource(srcID)
	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(chunks))
	}
	// Metadata should be an empty (non-nil) map
	if chunks[0].Metadata == nil {
		t.Error("metadata should be non-nil empty map")
	}
	if len(chunks[0].Metadata) != 0 {
		t.Errorf("expected empty metadata, got %v", chunks[0].Metadata)
	}
}

func TestGetLearningsBySourceFile(t *testing.T) {
	s := mustOpen(t)

	// Insert learnings with different source files
	s.InsertLearning(&models.Learning{
		Category: "gotcha", Content: "gotcha from doc A",
		SourceFile: "docs/api.md", SourceHash: "abc123",
		Confidence: 1.0, CreatedAt: time.Now(), ModelUsed: "test",
	})
	s.InsertLearning(&models.Learning{
		Category: "pattern", Content: "pattern from doc A",
		SourceFile: "docs/api.md", SourceHash: "abc123",
		Confidence: 1.0, CreatedAt: time.Now(), ModelUsed: "test",
	})
	s.InsertLearning(&models.Learning{
		Category: "gotcha", Content: "gotcha from doc B",
		SourceFile: "docs/setup.md", SourceHash: "def456",
		Confidence: 1.0, CreatedAt: time.Now(), ModelUsed: "test",
	})

	got, err := s.GetLearningsBySourceFile("docs/api.md")
	if err != nil {
		t.Fatalf("get by source_file: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("expected 2 learnings for docs/api.md, got %d", len(got))
	}
	for _, l := range got {
		if l.SourceFile != "docs/api.md" {
			t.Errorf("source_file = %q, want %q", l.SourceFile, "docs/api.md")
		}
	}

	// Superseded learnings should be excluded
	if len(got) > 0 {
		s.SupersedeLearning(got[0].ID, -1, "test supersede")
	}
	got2, _ := s.GetLearningsBySourceFile("docs/api.md")
	if len(got2) != 1 {
		t.Errorf("expected 1 learning after supersede, got %d", len(got2))
	}
}

func TestSupersedeBySourceFile(t *testing.T) {
	s := mustOpen(t)

	s.InsertLearning(&models.Learning{
		Category: "gotcha", Content: "old gotcha 1",
		SourceFile: "docs/api.md", SourceHash: "old",
		Confidence: 1.0, CreatedAt: time.Now(), ModelUsed: "test",
	})
	s.InsertLearning(&models.Learning{
		Category: "pattern", Content: "old pattern 2",
		SourceFile: "docs/api.md", SourceHash: "old",
		Confidence: 1.0, CreatedAt: time.Now(), ModelUsed: "test",
	})
	s.InsertLearning(&models.Learning{
		Category: "gotcha", Content: "unrelated gotcha",
		SourceFile: "docs/other.md", SourceHash: "xxx",
		Confidence: 1.0, CreatedAt: time.Now(), ModelUsed: "test",
	})

	ids, err := s.SupersedeBySourceFile("docs/api.md", "doc re-ingested")
	if err != nil {
		t.Fatalf("supersede: %v", err)
	}
	if len(ids) != 2 {
		t.Errorf("expected 2 superseded IDs, got %d", len(ids))
	}

	// Verify superseded learnings are no longer active
	active, _ := s.GetLearningsBySourceFile("docs/api.md")
	if len(active) != 0 {
		t.Errorf("expected 0 active learnings for docs/api.md after supersede, got %d", len(active))
	}

	// Unrelated learning should still be active
	other, _ := s.GetLearningsBySourceFile("docs/other.md")
	if len(other) != 1 {
		t.Errorf("expected 1 active learning for docs/other.md, got %d", len(other))
	}

	// Second supersede should return nil (nothing left)
	ids2, err := s.SupersedeBySourceFile("docs/api.md", "second try")
	if err != nil {
		t.Fatalf("second supersede: %v", err)
	}
	if ids2 != nil {
		t.Errorf("expected nil on second supersede, got %v", ids2)
	}
}

func TestDocSource_FullContent(t *testing.T) {
	s := mustOpen(t)

	skillContent := "---\nname: test-skill\ndescription: A test skill\n---\n\n# Test Skill\n\nFull skill content here with all the details."

	ds := &DocSource{
		Name:        "test-skill",
		Version:     "1.0",
		Project:     "memory",
		IsSkill:     true,
		FullContent: skillContent,
	}

	id, err := s.UpsertDocSource(ds)
	if err != nil {
		t.Fatalf("insert skill with full_content: %v", err)
	}

	got, err := s.GetDocSource("test-skill", "memory")
	if err != nil {
		t.Fatalf("get skill: %v", err)
	}
	if got.FullContent != skillContent {
		t.Errorf("full_content = %q, want %q", got.FullContent, skillContent)
	}
	if !got.IsSkill {
		t.Error("expected is_skill = true")
	}

	// Update full_content via upsert
	ds.FullContent = "updated skill content"
	id2, err := s.UpsertDocSource(ds)
	if err != nil {
		t.Fatalf("update skill: %v", err)
	}
	if id2 != id {
		t.Errorf("expected same ID on update, got %d vs %d", id2, id)
	}

	got2, err := s.GetDocSource("test-skill", "memory")
	if err != nil {
		t.Fatalf("get updated skill: %v", err)
	}
	if got2.FullContent != "updated skill content" {
		t.Errorf("updated full_content = %q, want %q", got2.FullContent, "updated skill content")
	}
}

func TestGetSkillContent(t *testing.T) {
	s := mustOpen(t)

	// Insert a skill with full_content
	s.UpsertDocSource(&DocSource{
		Name: "coding-standards", Project: "memory", IsSkill: true,
		FullContent: "# Coding Standards\n\nAll the standards.",
	})
	// Insert a non-skill doc (no full_content)
	s.UpsertDocSource(&DocSource{
		Name: "go-release-notes", Project: "memory", IsSkill: false,
	})
	// Insert a skill in another project
	s.UpsertDocSource(&DocSource{
		Name: "other-skill", Project: "other", IsSkill: true,
		FullContent: "# Other\n\nOther project skill.",
	})

	// GetSkillContent should return the full text for a known skill
	content, err := s.GetSkillContent("coding-standards", "memory")
	if err != nil {
		t.Fatalf("get skill content: %v", err)
	}
	if content != "# Coding Standards\n\nAll the standards." {
		t.Errorf("content = %q", content)
	}

	// Non-existent skill should error
	_, err = s.GetSkillContent("nonexistent", "memory")
	if err == nil {
		t.Error("expected error for nonexistent skill")
	}
}

func TestListSkillNames(t *testing.T) {
	s := mustOpen(t)

	s.UpsertDocSource(&DocSource{Name: "skill-a", Project: "p", IsSkill: true, FullContent: "A"})
	s.UpsertDocSource(&DocSource{Name: "skill-b", Project: "p", IsSkill: true, FullContent: "B"})
	s.UpsertDocSource(&DocSource{Name: "doc-c", Project: "p", IsSkill: false})
	s.UpsertDocSource(&DocSource{Name: "skill-d", Project: "other", IsSkill: true, FullContent: "D"})

	// List skills for project "p"
	skills, err := s.ListSkillNames("p")
	if err != nil {
		t.Fatalf("list skills: %v", err)
	}
	if len(skills) != 2 {
		t.Fatalf("expected 2 skills for project p, got %d", len(skills))
	}

	// Should contain skill-a and skill-b, not doc-c or skill-d
	names := map[string]bool{}
	for _, sk := range skills {
		names[sk.Name] = true
	}
	if !names["skill-a"] || !names["skill-b"] {
		t.Errorf("expected skill-a and skill-b, got %v", skills)
	}
}

func TestSearchDocChunksFTS_NoResults(t *testing.T) {
	s := mustOpen(t)

	srcID, _ := s.UpsertDocSource(&DocSource{Name: "src", Project: "p", Version: "1"})
	s.InsertDocChunk(&DocChunk{SourceID: srcID, SourceFile: "a.md", SourceHash: "s1", Content: "Golang concurrency patterns", ContentHash: "c1"})

	results, err := s.SearchDocChunksFTS("nonexistent-xyz-query", "", "", "", "", 10, nil)
	if err != nil {
		t.Fatalf("search no results: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results, got %d", len(results))
	}
}

func TestGetDocSourcesByExtensions(t *testing.T) {
	s := mustOpen(t)

	goSrc := &DocSource{Name: "go-stdlib", Project: "test", Version: "1.22", TriggerExtensions: ".go,.mod"}
	_, err := s.UpsertDocSource(goSrc)
	if err != nil {
		t.Fatal(err)
	}
	twigSrc := &DocSource{Name: "twig", Project: "test", Version: "3.x", TriggerExtensions: ".twig,.html.twig"}
	_, err = s.UpsertDocSource(twigSrc)
	if err != nil {
		t.Fatal(err)
	}
	// Source without trigger_extensions
	_, err = s.UpsertDocSource(&DocSource{Name: "general", Project: "test"})
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name      string
		exts      []string
		wantNames []string
	}{
		{"single .go", []string{".go"}, []string{"go-stdlib"}},
		{"single .twig", []string{".twig"}, []string{"twig"}},
		{"compound .html.twig", []string{".html.twig"}, []string{"twig"}},
		{"multi match", []string{".go", ".twig"}, []string{"go-stdlib", "twig"}},
		{"no match", []string{".py"}, nil},
		{"empty input", []string{}, nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sources, err := s.GetDocSourcesByExtensions(tt.exts, "")
			if err != nil {
				t.Fatal(err)
			}
			if len(tt.wantNames) == 0 {
				if len(sources) != 0 {
					t.Errorf("expected empty, got %d", len(sources))
				}
				return
			}
			gotNames := make(map[string]bool)
			for _, src := range sources {
				gotNames[src.Name] = true
			}
			for _, want := range tt.wantNames {
				if !gotNames[want] {
					t.Errorf("missing %q in results", want)
				}
			}
		})
	}
}

func TestSearchDocChunksFTS_ANDMatching(t *testing.T) {
	s := mustOpen(t)

	srcID, _ := s.UpsertDocSource(&DocSource{Name: "go-ref", Project: "test", Version: "1.22"})

	// Insert chunks with distinct vocabulary
	s.InsertDocChunk(&DocChunk{SourceID: srcID, SourceFile: "channels.md", SourceHash: "s1", HeadingPath: "# Concurrency > ## Channels", Content: "Channels provide communication between goroutines using send and receive operations", ContentHash: "c1"})
	s.InsertDocChunk(&DocChunk{SourceID: srcID, SourceFile: "goroutines.md", SourceHash: "s2", HeadingPath: "# Concurrency > ## Goroutines", Content: "Goroutines are lightweight threads managed by the Go runtime scheduler", ContentHash: "c2"})
	s.InsertDocChunk(&DocChunk{SourceID: srcID, SourceFile: "errors.md", SourceHash: "s3", HeadingPath: "# Error Handling", Content: "Error handling in Go uses explicit return values instead of exceptions", ContentHash: "c3"})

	// Multi-word AND query — only "channels.md" matches both "channel" and "goroutine" terms
	results, err := s.SearchDocChunksFTS("goroutine channel communication", "", "", "", "", 10, nil)
	if err != nil {
		t.Fatalf("AND search: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected at least 1 result for goroutine channel communication")
	}
	// channels.md should rank highest (mentions goroutines AND channel terms)
	if results[0].SourceFile != "channels.md" {
		t.Errorf("expected channels.md first (matches all terms), got %q", results[0].SourceFile)
	}

	// Single-word query should still work (1-term fallback)
	results2, err := s.SearchDocChunksFTS("scheduler", "", "", "", "", 10, nil)
	if err != nil {
		t.Fatalf("single-term search: %v", err)
	}
	if len(results2) == 0 {
		t.Fatal("expected at least 1 result for scheduler")
	}
	if results2[0].SourceFile != "goroutines.md" {
		t.Errorf("expected goroutines.md for scheduler, got %q", results2[0].SourceFile)
	}
}

func TestSearchDocChunksFTS_SourceIDsFilter(t *testing.T) {
	s := mustOpen(t)

	src1, _ := s.UpsertDocSource(&DocSource{Name: "go-ref", Project: "test", Version: "1.22"})
	src2, _ := s.UpsertDocSource(&DocSource{Name: "twig-ref", Project: "test", Version: "3.x"})

	s.InsertDocChunk(&DocChunk{SourceID: src1, SourceFile: "go.md", SourceHash: "s1", Content: "goroutine channel scheduler runtime", ContentHash: "c1"})
	s.InsertDocChunk(&DocChunk{SourceID: src2, SourceFile: "twig.md", SourceHash: "s2", Content: "goroutine channel filter template", ContentHash: "c2"})

	// With sourceIDs filter → only src1 results
	results, err := s.SearchDocChunksFTS("goroutine", "", "", "", "", 10, []int64{src1})
	if err != nil {
		t.Fatalf("sourceIDs filter: %v", err)
	}
	for _, r := range results {
		if r.SourceID != src1 {
			t.Errorf("expected only src1 results, got source_id=%d", r.SourceID)
		}
	}

	// Without sourceIDs filter → results from both
	allResults, err := s.SearchDocChunksFTS("goroutine", "", "", "", "", 10, nil)
	if err != nil {
		t.Fatalf("no sourceIDs filter: %v", err)
	}
	if len(allResults) < 2 {
		t.Errorf("expected at least 2 results without filter, got %d", len(allResults))
	}
}

func TestDocSource_DocType(t *testing.T) {
	s := mustOpen(t)

	// Default: doc_type should be "reference"
	ds := &DocSource{Name: "go-stdlib", Project: "test", Version: "1.22"}
	id, err := s.UpsertDocSource(ds)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	got, err := s.GetDocSource("go-stdlib", "test")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.DocType != "reference" {
		t.Errorf("default doc_type = %q, want %q", got.DocType, "reference")
	}

	// Insert with explicit doc_type=style
	ds2 := &DocSource{Name: "effective-go", Project: "test", Version: "1.0", DocType: "style"}
	id2, err := s.UpsertDocSource(ds2)
	if err != nil {
		t.Fatalf("insert style: %v", err)
	}
	if id2 <= 0 {
		t.Fatal("expected positive ID")
	}

	got2, err := s.GetDocSource("effective-go", "test")
	if err != nil {
		t.Fatalf("get style: %v", err)
	}
	if got2.DocType != "style" {
		t.Errorf("explicit doc_type = %q, want %q", got2.DocType, "style")
	}

	// Re-ingest WITHOUT doc_type → preserve existing "style"
	ds3 := &DocSource{Name: "effective-go", Project: "test", Version: "1.1", DocType: ""}
	id3, err := s.UpsertDocSource(ds3)
	if err != nil {
		t.Fatalf("re-ingest: %v", err)
	}
	if id3 != id2 {
		t.Errorf("expected same ID on update, got %d vs %d", id3, id2)
	}
	got3, _ := s.GetDocSource("effective-go", "test")
	if got3.DocType != "style" {
		t.Errorf("re-ingest cleared doc_type: want style, got %q", got3.DocType)
	}

	// Re-ingest WITH explicit doc_type=reference → overwrite
	ds4 := &DocSource{Name: "effective-go", Project: "test", Version: "1.2", DocType: "reference"}
	s.UpsertDocSource(ds4)
	got4, _ := s.GetDocSource("effective-go", "test")
	if got4.DocType != "reference" {
		t.Errorf("explicit doc_type update: want reference, got %q", got4.DocType)
	}

	_ = id
}

func TestGetDocSourcesByExtensions_DocTypeFilter(t *testing.T) {
	s := mustOpen(t)

	s.UpsertDocSource(&DocSource{Name: "go-ref", Project: "test", TriggerExtensions: ".go", DocType: "reference"})
	s.UpsertDocSource(&DocSource{Name: "go-style", Project: "test", TriggerExtensions: ".go", DocType: "style"})

	// No filter → both returned
	all, err := s.GetDocSourcesByExtensions([]string{".go"}, "")
	if err != nil {
		t.Fatalf("no filter: %v", err)
	}
	if len(all) != 2 {
		t.Errorf("expected 2 without filter, got %d", len(all))
	}

	// Filter doc_type=reference → only go-ref
	refs, err := s.GetDocSourcesByExtensions([]string{".go"}, "reference")
	if err != nil {
		t.Fatalf("reference filter: %v", err)
	}
	if len(refs) != 1 || refs[0].Name != "go-ref" {
		t.Errorf("expected [go-ref], got %v", refs)
	}

	// Filter doc_type=style → only go-style
	styles, err := s.GetDocSourcesByExtensions([]string{".go"}, "style")
	if err != nil {
		t.Fatalf("style filter: %v", err)
	}
	if len(styles) != 1 || styles[0].Name != "go-style" {
		t.Errorf("expected [go-style], got %v", styles)
	}
}

func TestGetDocChunksBySourceIDs(t *testing.T) {
	s := mustOpen(t)

	id1, _ := s.UpsertDocSource(&DocSource{Name: "src1", Project: "p", Version: "1"})
	id2, _ := s.UpsertDocSource(&DocSource{Name: "src2", Project: "p", Version: "2"})

	// Chunks with tokens: 10 (tiny), 50 (small), 250 (sweet-spot center), 500 (large), 150 (sweet-spot)
	s.InsertDocChunk(&DocChunk{SourceID: id1, SourceFile: "a.md", Content: "tiny", ContentHash: "c1", TokensApprox: 10})
	s.InsertDocChunk(&DocChunk{SourceID: id1, SourceFile: "b.md", Content: "small", ContentHash: "c2", TokensApprox: 50})
	s.InsertDocChunk(&DocChunk{SourceID: id1, SourceFile: "c.md", Content: "sweet center", ContentHash: "c3", TokensApprox: 250})
	s.InsertDocChunk(&DocChunk{SourceID: id2, SourceFile: "d.md", Content: "large", ContentHash: "c4", TokensApprox: 500})
	s.InsertDocChunk(&DocChunk{SourceID: id2, SourceFile: "e.md", Content: "sweet edge", ContentHash: "c5", TokensApprox: 150})

	// Sweet-spot chunks (100-400) should come first, ordered by distance to 250
	chunks, err := s.GetDocChunksBySourceIDs([]int64{id1, id2}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 5 {
		t.Fatalf("expected 5, got %d", len(chunks))
	}

	// First two should be sweet-spot: 250 (dist=0) and 150 (dist=100)
	if chunks[0].TokensApprox != 250 {
		t.Errorf("first chunk should be 250 (sweet center), got %d", chunks[0].TokensApprox)
	}
	if chunks[1].TokensApprox != 150 {
		t.Errorf("second chunk should be 150 (sweet edge), got %d", chunks[1].TokensApprox)
	}

	// Non-sweet-spot chunks after, ordered by distance to 250
	if chunks[2].TokensApprox != 500 && chunks[2].TokensApprox != 50 && chunks[2].TokensApprox != 10 {
		t.Errorf("third chunk should be non-sweet-spot, got %d", chunks[2].TokensApprox)
	}

	// Limit
	chunks, err = s.GetDocChunksBySourceIDs([]int64{id1, id2}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 2 {
		t.Fatalf("expected 2 with limit, got %d", len(chunks))
	}
	// Limit=2 should return the 2 sweet-spot chunks
	if chunks[0].TokensApprox != 250 || chunks[1].TokensApprox != 150 {
		t.Errorf("limit=2 should return sweet-spot chunks, got %d and %d", chunks[0].TokensApprox, chunks[1].TokensApprox)
	}

	// Empty input
	chunks, err = s.GetDocChunksBySourceIDs(nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 0 {
		t.Errorf("expected empty for nil input, got %d", len(chunks))
	}
}

func TestGetReferenceSources(t *testing.T) {
	s := mustOpen(t)

	// Insert test doc sources
	s.DB().Exec(`INSERT INTO doc_sources (name, version, doc_type, example_query, project) VALUES ('test-lib', '2.0', 'reference', 'http Handler', '')`)
	s.DB().Exec(`INSERT INTO doc_sources (name, version, doc_type, example_query, project) VALUES ('test-style', '1.0', 'style', '', '')`)
	s.DB().Exec(`INSERT INTO doc_sources (name, version, doc_type, example_query, project) VALUES ('test-noquery', '3.0', 'reference', '', '')`)

	sources, err := s.GetReferenceSources()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(sources) < 2 {
		t.Fatalf("expected at least 2 reference sources, got %d", len(sources))
	}

	// Check that style sources are excluded
	for _, s := range sources {
		if s.Name == "test-style" {
			t.Error("style source should not be returned")
		}
	}

	// Check that reference sources are included with example_query
	found := false
	for _, s := range sources {
		if s.Name == "test-lib" && s.ExampleQuery == "http Handler" {
			found = true
		}
	}
	if !found {
		t.Error("expected test-lib with example_query 'http Handler'")
	}
}
