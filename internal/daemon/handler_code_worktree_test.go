package daemon

import (
	"testing"
)

func TestResolveProjectDir_UsesCWDWhenProvided(t *testing.T) {
	h, _ := mustHandler(t)

	params := map[string]any{
		"_cwd": "/tmp/worktree-path",
	}

	got := h.resolveProjectDir("yesmem", params)
	if got != "/tmp/worktree-path" {
		t.Errorf("expected /tmp/worktree-path, got %q", got)
	}
}

func TestResolveProjectDir_PrefersProjectDirOverCWD(t *testing.T) {
	h, _ := mustHandler(t)

	params := map[string]any{
		"_cwd":        "/tmp/cwd-path",
		"project_dir": "/tmp/explicit-path",
	}

	got := h.resolveProjectDir("yesmem", params)
	if got != "/tmp/explicit-path" {
		t.Errorf("expected /tmp/explicit-path, got %q", got)
	}
}

func TestResolveProjectDir_FallsBackToStoreWhenNoParams(t *testing.T) {
	h, _ := mustHandler(t)

	got := h.resolveProjectDir("nonexistent-project")
	if got != "" {
		t.Errorf("expected empty for unregistered project, got %q", got)
	}
}

func TestResolveProjectDir_IgnoresEmptyCWD(t *testing.T) {
	h, _ := mustHandler(t)

	params := map[string]any{
		"_cwd": "",
	}

	got := h.resolveProjectDir("nonexistent-project", params)
	if got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}

func TestResolveProjectDir_IgnoresNonStringCWD(t *testing.T) {
	h, _ := mustHandler(t)

	params := map[string]any{
		"_cwd": 42,
	}

	got := h.resolveProjectDir("nonexistent-project", params)
	if got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}
