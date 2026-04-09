package daemon

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/carsteneu/yesmem/internal/clustering"
)

func TestFindProjectDir_Exact(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "-home-user-project")
	os.MkdirAll(dir, 0755)

	result := findProjectDir(root, "/home/user/project")
	if result != dir {
		t.Errorf("expected %q, got %q", dir, result)
	}
}

func TestFindProjectDir_NotFound(t *testing.T) {
	root := t.TempDir()
	result := findProjectDir(root, "/nonexistent/path")
	if result != "" {
		t.Errorf("expected empty, got %q", result)
	}
}

func TestFindProjectDir_FallbackScan(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "some-prefix-myproject")
	os.MkdirAll(dir, 0755)

	result := findProjectDir(root, "/some/path/myproject")
	if result != dir {
		t.Errorf("expected %q via scan fallback, got %q", dir, result)
	}
}

func TestLabelFromDocs_Empty(t *testing.T) {
	result := labelFromDocs(nil)
	if result != "" {
		t.Errorf("expected empty, got %q", result)
	}
}

func TestLabelFromDocs_Short(t *testing.T) {
	docs := []clustering.Document{{Content: "short label"}}
	result := labelFromDocs(docs)
	if result != "short label" {
		t.Errorf("expected 'short label', got %q", result)
	}
}

func TestLabelFromDocs_Truncation(t *testing.T) {
	long := make([]rune, 100)
	for i := range long {
		long[i] = 'x'
	}
	docs := []clustering.Document{{Content: string(long)}}
	result := labelFromDocs(docs)
	if len([]rune(result)) != 60 {
		t.Errorf("expected 60 runes, got %d", len([]rune(result)))
	}
}
