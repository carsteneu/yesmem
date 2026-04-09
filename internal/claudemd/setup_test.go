package claudemd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSetupProjectInjectsCLAUDEmd(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte("# existing content\n"), 0644)

	if err := SetupProject(dir, "yesmem-ops.md"); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "CLAUDE.md"))
	if !strings.Contains(string(data), "@.claude/yesmem-ops.md") {
		t.Error("import not injected into CLAUDE.md")
	}
}

func TestSetupProjectIdempotent(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte("@.claude/yesmem-ops.md\n"), 0644)

	SetupProject(dir, "yesmem-ops.md")
	SetupProject(dir, "yesmem-ops.md")

	data, _ := os.ReadFile(filepath.Join(dir, "CLAUDE.md"))
	count := strings.Count(string(data), "@.claude/yesmem-ops.md")
	if count != 1 {
		t.Errorf("import added %d times, want 1", count)
	}
}

func TestSetupProjectCreatesClaudeMd(t *testing.T) {
	dir := t.TempDir()
	// no CLAUDE.md exists yet
	if err := SetupProject(dir, "yesmem-ops.md"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "CLAUDE.md"))
	if err != nil {
		t.Fatal("CLAUDE.md should have been created")
	}
	if !strings.Contains(string(data), "@.claude/yesmem-ops.md") {
		t.Error("import not in newly created CLAUDE.md")
	}
}

func TestSetupProjectGitignore(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte(""), 0644)
	os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("vendor/\n"), 0644)

	SetupProject(dir, "yesmem-ops.md")

	data, _ := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if !strings.Contains(string(data), ".claude/yesmem-ops.md") {
		t.Error(".gitignore not updated")
	}
}

func TestSetupProjectGitignoreIdempotent(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte(""), 0644)
	os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(".claude/yesmem-ops.md\n"), 0644)

	SetupProject(dir, "yesmem-ops.md")
	SetupProject(dir, "yesmem-ops.md")

	data, _ := os.ReadFile(filepath.Join(dir, ".gitignore"))
	count := strings.Count(string(data), ".claude/yesmem-ops.md")
	if count != 1 {
		t.Errorf(".gitignore entry duplicated: %d times", count)
	}
}
