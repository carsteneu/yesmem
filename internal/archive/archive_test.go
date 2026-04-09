package archive

import (
	"os"
	"path/filepath"
	"testing"
)

func TestArchiveFile(t *testing.T) {
	// Create temp dirs
	srcDir := t.TempDir()
	archiveDir := t.TempDir()

	// Create a fake JSONL
	srcFile := filepath.Join(srcDir, "test-session.jsonl")
	content := []byte(`{"type":"user","message":"hello"}` + "\n")
	if err := os.WriteFile(srcFile, content, 0644); err != nil {
		t.Fatalf("write src: %v", err)
	}

	a := New(archiveDir)

	// Archive it under project "ccm19"
	err := a.ArchiveFile(srcFile, "ccm19")
	if err != nil {
		t.Fatalf("archive: %v", err)
	}

	// Verify archived file exists
	archived := filepath.Join(archiveDir, "ccm19", "test-session.jsonl")
	data, err := os.ReadFile(archived)
	if err != nil {
		t.Fatalf("read archived: %v", err)
	}
	if string(data) != string(content) {
		t.Error("archived content doesn't match original")
	}
}

func TestArchiveFile_Dedup(t *testing.T) {
	srcDir := t.TempDir()
	archiveDir := t.TempDir()

	srcFile := filepath.Join(srcDir, "session.jsonl")
	os.WriteFile(srcFile, []byte("v1"), 0644)

	a := New(archiveDir)
	a.ArchiveFile(srcFile, "proj")

	// Archive same file again with different content AND size — should overwrite
	os.WriteFile(srcFile, []byte("version-two-longer"), 0644)
	a.ArchiveFile(srcFile, "proj")

	archived := filepath.Join(archiveDir, "proj", "session.jsonl")
	data, _ := os.ReadFile(archived)
	if string(data) != "version-two-longer" {
		t.Errorf("expected 'version-two-longer', got %q", string(data))
	}
}

func TestArchiveFile_SrcNotFound(t *testing.T) {
	a := New(t.TempDir())
	err := a.ArchiveFile("/nonexistent.jsonl", "proj")
	if err == nil {
		t.Error("expected error for nonexistent source")
	}
}
