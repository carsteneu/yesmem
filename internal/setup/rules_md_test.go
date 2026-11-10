package setup

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// The four GenerateRULESmd tests that stood here covered the full standard copy the wizard wrote
// into whatever directory it was started in. Variant B retires that mechanism: a project no
// longer receives a standard catalogue, it states deviations instead. The coverage moved, it was
// not dropped — the master's content is asserted by TestInstalledMasterCarriesTheDecisionPolicy
// AndItsNumbering, the language rules by TestSeedProjectRulesWritesAnUnnumberedOverlay, and the
// refusal to touch an existing project catalogue by
// TestSeedProjectRulesLeavesACompleteProjectFileAlone.

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

	// The order is part of the result: it becomes the order of the bullets in the seeded deviation file.
	os.WriteFile(filepath.Join(dir, "requirements.txt"), []byte("x"), 0644)
	os.WriteFile(filepath.Join(dir, "package.json"), []byte("{}"), 0644)
	types = detectProjectType(dir)
	want := []string{"go.mod", "package.json", "requirements.txt"}
	if strings.Join(types, ",") != strings.Join(want, ",") {
		t.Errorf("types = %v, want %v", types, want)
	}
}

// A fresh installation has no RULES.md the guard could read: the project copy belongs to
// whatever repository the wizard was started in, and on a machine without that checkout it
// is absent. The installation keeps its own copy, and guard.LoadInstalledRules finds it
// first, so the enforceable set survives a repository that ships no rules.
func TestInstallRULESmdWritesTheInstallationCopy(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dataDir := filepath.Join(home, ".claude", "yesmem")

	path, err := installRULESmd(dataDir)
	if err != nil {
		t.Fatalf("installRULESmd: %v", err)
	}
	if want := filepath.Join(dataDir, "RULES.md"); path != want {
		t.Fatalf("path = %q, want %q", path, want)
	}

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read installed RULES.md: %v", err)
	}
	for _, check := range []string{"rule enforcement system", "## Rules", "Never auto-commit"} {
		if !strings.Contains(string(content), check) {
			t.Errorf("installed RULES.md missing %q", check)
		}
	}
	// The installation copy is not tied to a project: language rules of the directory the
	// wizard happened to run in must not travel into every session on this machine.
	if strings.Contains(string(content), "## Project-Specific") {
		t.Error("installed RULES.md carries project-specific rules")
	}
}

// Once written, the installation copy is the operator's: a second setup run must not
// overwrite the narrowed Decision Policy it may have been edited to hold.
func TestInstallRULESmdKeepsAnExistingCopy(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dataDir := filepath.Join(home, ".claude", "yesmem")
	existing := filepath.Join(dataDir, "RULES.md")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(existing, []byte("edited by the operator"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	path, err := installRULESmd(dataDir)
	if err != nil {
		t.Fatalf("installRULESmd: %v", err)
	}
	if path != "" {
		t.Errorf("path = %q, want empty when the file already exists", path)
	}
	content, _ := os.ReadFile(existing)
	if string(content) != "edited by the operator" {
		t.Error("the installation copy was overwritten")
	}
}

// The installed catalogue has to ground the rules the guard may block on. The guard
// offers rule 32 to the model and treats a citation outside the numbered catalogue as an
// invention (internal/guard/policy.go:91-149, downgradeUnknownCitation), so a catalogue
// that stopped at 26 would release the PR-merge BLOCK to SUGGEST on every fresh
// installation while still advertising it. The four numbers are the literal set in
// internal/guard/policy.go:29-34; this test names them on purpose, because the catalogue
// is the file that has to match them.
func TestInstalledCatalogueGroundsTheBlockableRules(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	path, err := installRULESmd(filepath.Join(t.TempDir(), "yesmem"))
	if err != nil {
		t.Fatalf("installRULESmd: %v", err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	// Only the numbered lines count, and only before the Decision Policy, exactly as the
	// guard reads it (catalogueRuleNumbers).
	text := string(content)
	if index := strings.Index(text, "## Decision Policy"); index >= 0 {
		text = text[:index]
	}
	numbered := regexp.MustCompile(`(?m)^\s*(\d+)\.\s+(.*)$`)
	rules := map[int]string{}
	for _, match := range numbered.FindAllStringSubmatch(text, -1) {
		number, convErr := strconv.Atoi(match[1])
		if convErr == nil {
			rules[number] = match[2]
		}
	}

	for _, want := range []int{1, 2, 25, 32} {
		if rules[want] == "" {
			t.Errorf("rule %d missing from the installed catalogue: the guard may block on it, a citation of it would be downgraded", want)
		}
	}
	if !strings.Contains(rules[32], "merge") {
		t.Errorf("rule 32 = %q, want the PR-merge rule the guard advertises", rules[32])
	}
	if !strings.Contains(rules[25], "destructive") {
		t.Errorf("rule 25 = %q, want the destructive-command rule", rules[25])
	}
}
