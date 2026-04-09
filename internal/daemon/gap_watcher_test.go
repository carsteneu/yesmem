package daemon

import (
	"testing"
)

func TestEnrichGapWithContext_Empty(t *testing.T) {
	_, s := mustHandler(t)
	result := enrichGapWithContext(s, "unknown topic", "")
	if result != "" {
		t.Errorf("expected empty for no results, got %q", result)
	}
}

func TestNewWatcher(t *testing.T) {
	dir := t.TempDir()
	called := false
	w := NewWatcher(dir, func(path string) { called = true }, func(path string) {})
	if w == nil {
		t.Fatal("expected non-nil watcher")
	}
	if w.dir != dir {
		t.Errorf("expected dir %q, got %q", dir, w.dir)
	}
	_ = called
}
