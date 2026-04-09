package daemon

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/carsteneu/yesmem/internal/config"
)

func TestEmbeddingCacheModelKey_Nil(t *testing.T) {
	if got := embeddingCacheModelKey(nil); got != "unknown" {
		t.Errorf("expected 'unknown', got %q", got)
	}
}

func TestEmbeddingCacheModelKey_WithProvider(t *testing.T) {
	cfg := &config.Config{}
	cfg.Embedding.Provider = "openai"
	if got := embeddingCacheModelKey(cfg); got != "openai" {
		t.Errorf("expected 'openai', got %q", got)
	}
}

func TestEmbeddingCacheModelKey_EmptyProvider(t *testing.T) {
	cfg := &config.Config{}
	if got := embeddingCacheModelKey(cfg); got != "unknown" {
		t.Errorf("expected 'unknown', got %q", got)
	}
}

func TestFindGitRoot_Valid(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, ".git"), 0755)
	subDir := filepath.Join(dir, "a", "b", "c")
	os.MkdirAll(subDir, 0755)

	root := findGitRoot(subDir)
	if root != dir {
		t.Errorf("expected %q, got %q", dir, root)
	}
}

func TestFindGitRoot_NotFound(t *testing.T) {
	dir := t.TempDir()
	root := findGitRoot(dir)
	if root != "" {
		t.Errorf("expected empty, got %q", root)
	}
}

func TestFindGitRoot_File(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, ".git"), 0755)
	file := filepath.Join(dir, "test.go")
	os.WriteFile(file, []byte("package main"), 0644)

	root := findGitRoot(file)
	if root != dir {
		t.Errorf("expected %q for file input, got %q", dir, root)
	}
}

func TestSocketPath(t *testing.T) {
	got := SocketPath("/tmp/yesmem-test")
	expected := "/tmp/yesmem-test/daemon.sock"
	if got != expected {
		t.Errorf("expected %q, got %q", expected, got)
	}
}
