package ingest

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/carsteneu/yesmem/internal/storage"
)

func mustOpenStore(t *testing.T) *storage.Store {
	t.Helper()
	s, err := storage.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestDocIngest_CreatesChunks(t *testing.T) {
	store := mustOpenStore(t)

	dir := t.TempDir()
	docFile := filepath.Join(dir, "docs.md")
	os.WriteFile(docFile, []byte("# Regular Docs\n\nJust documentation."), 0644)

	cfg := Config{
		Project: "test-proj",
		Name:    "regular-docs",
	}

	_, err := Run(cfg, []string{docFile}, store)
	if err != nil {
		t.Fatalf("ingest run: %v", err)
	}

	ds, err := store.GetDocSource("regular-docs", "test-proj")
	if err != nil {
		t.Fatalf("get doc source: %v", err)
	}

	// Non-skill docs should NOT have full_content
	if ds.FullContent != "" {
		t.Errorf("full_content should be empty for non-skill docs, got %q", ds.FullContent)
	}
}

func TestDocIngest_RSTFile(t *testing.T) {
	store := mustOpenStore(t)

	dir := t.TempDir()
	rstContent := "Messenger Component\n====================\n\n" +
		"The Messenger component helps sending messages to a message bus.\n\n" +
		"Configuration\n-------------\n\n" +
		".. versionadded:: 6.1\n\n" +
		"    The messenger:consume command was added in Symfony 6.1.\n\n" +
		"Use :class:`Symfony\\Component\\Messenger\\MessageBus` to dispatch messages.\n\n" +
		".. code-block:: php\n\n" +
		"    $bus->dispatch(new MyMessage());\n\n" +
		".. warning::\n\n" +
		"    Always configure a failure transport.\n\n" +
		"Retry Strategy\n~~~~~~~~~~~~~~\n\n" +
		"See :doc:`/messenger/retry` and :func:`retry` for details.\n\n" +
		".. code-block:: yaml\n\n" +
		"    framework:\n        messenger:\n            failure_transport: failed\n"
	rstFile := filepath.Join(dir, "messenger.rst")
	os.WriteFile(rstFile, []byte(rstContent), 0644)

	cfg := Config{
		Project: "test-proj",
		Name:    "symfony-docs",
	}

	result, err := Run(cfg, []string{rstFile}, store)
	if err != nil {
		t.Fatalf("ingest run: %v", err)
	}

	if result.FilesProcessed != 1 {
		t.Errorf("expected 1 file processed, got %d", result.FilesProcessed)
	}
	if result.ChunksCreated == 0 {
		t.Fatal("expected chunks to be created")
	}

	// Verify chunks have correct structure
	ds, err := store.GetDocSource("symfony-docs", "test-proj")
	if err != nil {
		t.Fatalf("get doc source: %v", err)
	}

	chunks, err := store.GetDocChunksBySource(ds.ID)
	if err != nil {
		t.Fatalf("get chunks: %v", err)
	}

	if len(chunks) == 0 {
		t.Fatal("expected chunks from RST file")
	}

	// Check heading paths exist
	paths := make(map[string]bool)
	for _, c := range chunks {
		paths[c.HeadingPath] = true
	}

	if !paths["Messenger Component > Configuration"] {
		t.Errorf("missing 'Messenger Component > Configuration' path, got: %v", paths)
	}

	// Check metadata on chunks
	var foundLanguages, foundVersionAdded, foundEntities, foundWarning bool
	for _, c := range chunks {
		meta := c.Metadata
		if meta["languages"] != "" {
			foundLanguages = true
		}
		if meta["version_added"] != "" {
			foundVersionAdded = true
		}
		if meta["rst_entities"] != "" {
			foundEntities = true
		}
		if meta["admonition"] != "" {
			foundWarning = true
		}
	}

	if !foundLanguages {
		t.Error("expected at least one chunk with languages metadata")
	}
	if !foundVersionAdded {
		t.Error("expected at least one chunk with version_added metadata")
	}
	if !foundEntities {
		t.Error("expected at least one chunk with rst_entities metadata")
	}
	if !foundWarning {
		t.Error("expected at least one chunk with admonition metadata")
	}
}
