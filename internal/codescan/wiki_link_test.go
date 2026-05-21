package codescan

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRenderWikiLink_FileMissing(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	out := renderWikiLink("yesmem")
	if !strings.Contains(out, "~/.claude/yesmem/wiki/yesmem/") {
		t.Errorf("missing wiki path in: %q", out)
	}
	if !strings.Contains(out, "not yet rendered") {
		t.Errorf("missing 'not yet rendered' fallback in: %q", out)
	}
	if !strings.Contains(out, "BEFORE") {
		t.Errorf("missing BEFORE mandate in: %q", out)
	}
	if !strings.Contains(out, "check its wiki page") {
		t.Errorf("missing 'check its wiki page' in: %q", out)
	}
}

func TestRenderWikiLink_FilePresent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	wikiDir := filepath.Join(home, ".claude", "yesmem", "wiki", "yesmem")
	if err := os.MkdirAll(wikiDir, 0755); err != nil {
		t.Fatal(err)
	}
	healthPath := filepath.Join(wikiDir, "health.md")
	if err := os.WriteFile(healthPath, []byte("test"), 0644); err != nil {
		t.Fatal(err)
	}
	out := renderWikiLink("yesmem")
	if !strings.Contains(out, "last build:") {
		t.Errorf("missing timestamp label in: %q", out)
	}
	// Verify timestamp is recent (today, within the last hour)
	want := time.Now().Format("15:04")
	if !strings.Contains(out, want) {
		alt := time.Now().Add(-time.Minute).Format("15:04")
		if !strings.Contains(out, alt) {
			t.Errorf("missing recent timestamp in: %q (expected %s or %s)", out, want, alt)
		}
	}
	if !strings.Contains(out, "search_code_index") {
		t.Errorf("missing fallback hierarchy mention (MCP code tools) in: %q", out)
	}
	if !strings.Contains(out, "BEFORE") {
		t.Errorf("missing BEFORE mandate in: %q", out)
	}
}
