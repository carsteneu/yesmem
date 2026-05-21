package setup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGenerateRULESmd_NoPreviousFile(t *testing.T) {
	home := t.TempDir()
	projectDir := t.TempDir()

	path, err := GenerateRULESmd(home, projectDir)
	if err != nil {
		t.Fatalf("GenerateRULESmd: %v", err)
	}
	if path == "" {
		t.Fatal("expected path, got empty (file should be created)")
	}

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read RULES.md: %v", err)
	}

	checks := []string{
		"rule enforcement system",
		"Never auto-commit",
		"Never commit secrets",
		"No workarounds",
		"## Skill Catalog",
		"skill:",
		"priority: MUST",
		"triggers:",
		"feature branch",
		"make deploy",
		"Memory queries",
		"Conventional Commits",
		"root cause",
		"destructive Bash",
		"API contracts",
	}

	for _, check := range checks {
		if !strings.Contains(string(content), check) {
			t.Errorf("RULES.md missing: %s", check)
		}
	}
}

func TestGenerateRULESmd_ExistingFile(t *testing.T) {
	home := t.TempDir()
	projectDir := t.TempDir()

	existingPath := filepath.Join(projectDir, "RULES.md")
	os.WriteFile(existingPath, []byte("custom rules"), 0644)

	path, err := GenerateRULESmd(home, projectDir)
	if err != nil {
		t.Fatalf("GenerateRULESmd returned error: %v", err)
	}
	if path != "" {
		t.Error("expected empty path when RULES.md already exists")
	}

	content, _ := os.ReadFile(existingPath)
	if string(content) != "custom rules" {
		t.Error("existing RULES.md was overwritten")
	}
}

func TestGenerateRULESmd_WithGoMod(t *testing.T) {
	home := t.TempDir()
	projectDir := t.TempDir()

	os.WriteFile(filepath.Join(projectDir, "go.mod"), []byte("module example"), 0644)

	path, err := GenerateRULESmd(home, projectDir)
	if err != nil {
		t.Fatalf("GenerateRULESmd: %v", err)
	}

	content, _ := os.ReadFile(path)
	if !strings.Contains(string(content), "Go: always TDD") {
		t.Error("missing Go-specific rules")
	}
}

func TestGenerateRULESmd_SkillCatalog(t *testing.T) {
	home := t.TempDir()
	projectDir := t.TempDir()

	path, err := GenerateRULESmd(home, projectDir)
	if err != nil {
		t.Fatalf("GenerateRULESmd: %v", err)
	}

	content, _ := os.ReadFile(path)

	// Each bundled skill should appear in the catalog
	expectedSkills := []string{
		"subagent-driven-development",
		"yesmem-agents",
		"yesmem-cap-builder",
		"yesmem-config",
		"yesmem-docs",
		"yesmem-orientation",
		"yesmem-planning",
		"yesmem-remember",
		"yesmem-search",
		"yesmem-sessions",
	}

	for _, skill := range expectedSkills {
		if !strings.Contains(string(content), "skill: "+skill) {
			t.Errorf("missing skill in catalog: %s", skill)
		}
	}
}

func TestLoadSkillCatalog(t *testing.T) {
	catalog, err := loadSkillCatalog()
	if err != nil {
		t.Fatalf("loadSkillCatalog: %v", err)
	}
	if len(catalog) == 0 {
		t.Fatal("no skills loaded")
	}

	for _, entry := range catalog {
		if entry.ID == 0 {
			t.Error("skill has no ID")
		}
		if entry.Skill == "" {
			t.Error("skill has no name")
		}
		if len(entry.Triggers) == 0 {
			t.Logf("skill %s has no triggers (process skill, not keyword-triggered)", entry.Skill)
		}
		if entry.Priority != "MUST" {
			t.Errorf("skill %s priority is %q, expected MUST", entry.Skill, entry.Priority)
		}
	}
}

func TestDetectProjectType(t *testing.T) {
	dir := t.TempDir()

	types := detectProjectType(dir)
	if len(types) != 0 {
		t.Errorf("empty dir should detect nothing, got %v", types)
	}

	os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x"), 0644)
	types = detectProjectType(dir)
	if len(types) != 1 || types[0] != "go.mod" {
		t.Errorf("expected [go.mod], got %v", types)
	}
}
