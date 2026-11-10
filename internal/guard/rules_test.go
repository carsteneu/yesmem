package guard

import (
	"os"
	"path/filepath"
	"testing"
)

func writeRules(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestLoadRulesPrefersTheProject(t *testing.T) {
	td := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	writeRules(t, filepath.Join(td, "RULES.md"), "cwd rules")
	writeRules(t, filepath.Join(td, "memory", "yesmem", "RULES.md"), "install rules")

	if rules := LoadRules(filepath.Join(td, ".claude", "yesmem"), td); rules != "cwd rules" {
		t.Fatalf("rules = %q, want the project's catalogue", rules)
	}
}

func TestLoadRulesFallsBackToTheInstallation(t *testing.T) {
	td := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	writeRules(t, filepath.Join(td, "memory", "yesmem", "RULES.md"), "install rules")

	if rules := LoadRules(filepath.Join(td, ".claude", "yesmem"), ""); rules != "install rules" {
		t.Fatalf("rules = %q, want the installation copy", rules)
	}
}

func TestLoadRulesWithoutAnyCatalogue(t *testing.T) {
	td := t.TempDir()
	t.Setenv("HOME", t.TempDir())

	if rules := LoadRules(filepath.Join(td, ".claude", "yesmem"), td); rules != "" {
		t.Fatalf("rules = %q, want none", rules)
	}
}

// The enforceable BLOCK set follows the installation's copy of RULES.md, not the literal
// table in code: an operator who narrows the Decision Policy there must get a narrower set,
// or the file the guard reads is decoration. A project copy must not be able to do this
// (see LoadInstalledRules), which is why the file lives in the data directory.
func TestInstalledRulesGovernTheBlockSet(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), ".claude", "yesmem")
	t.Setenv("HOME", t.TempDir())
	writeRules(t, filepath.Join(dataDir, "RULES.md"), `# Rules

## Decision Policy

- **BLOCK** only on rule 1.

## Rules

1. No LLM signature in commit messages.
`)

	set := blockableRuleNumbers(LoadInstalledRules(dataDir))
	if len(set) != 1 || !set[1] {
		t.Errorf("block set = %v, want only rule 1 from the installed policy", set)
	}
}

// Without an installed policy the literal set holds: a fresh installation must not end up
// with an empty block set because no file exists yet.
func TestBlockSetWithoutAnInstalledPolicyIsTheLiteralSet(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	set := blockableRuleNumbers(LoadInstalledRules(filepath.Join(t.TempDir(), "yesmem")))
	if len(set) != len(policyBlockRules) {
		t.Fatalf("block set = %v, want the literal set %v", set, policyBlockRules)
	}
	for number := range policyBlockRules {
		if !set[number] {
			t.Errorf("rule %d missing from the literal set %v", number, set)
		}
	}
}

// The data directory's copy wins over the checkout copy. Setup writes the installation's
// own file there, and a development checkout next to it must not decide which rules are
// enforced on every project of the machine.
func TestLoadInstalledRulesPrefersTheDataDirCopy(t *testing.T) {
	td := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	dataDir := filepath.Join(td, ".claude", "yesmem")
	writeRules(t, filepath.Join(dataDir, "RULES.md"), "installed rules")
	writeRules(t, filepath.Join(td, "memory", "yesmem", "RULES.md"), "checkout rules")

	if rules := LoadInstalledRules(dataDir); rules != "installed rules" {
		t.Fatalf("rules = %q, want the data dir copy", rules)
	}
}
