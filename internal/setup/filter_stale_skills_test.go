package setup

import (
	"strings"
	"testing"
)

func TestFilterStaleSkillRules_DropsUnavailableSkills(t *testing.T) {
	input := `# Header section stays

## Skill Catalog
rules:
  - id: 25
    skill: available-skill
    priority: MUST
    triggers: ["foo"]
    rule: "available rule"
  - id: 26
    skill: ghost-skill
    priority: MUST
    triggers: ["bar"]
    rule: "ghost rule"

## Project-Specific
1. Some rule.
`
	// Available set has only available-skill; ghost-skill must be dropped
	available := map[string]bool{"available-skill": true}
	got := filterStaleSkillRulesWith(input, available)

	if !strings.Contains(got, "skill: available-skill") {
		t.Error("available skill was dropped")
	}
	if strings.Contains(got, "skill: ghost-skill") {
		t.Error("ghost skill was not filtered out")
	}
	if strings.Contains(got, "ghost rule") {
		t.Error("ghost rule text was not filtered out")
	}
}

func TestFilterStaleSkillRules_PreservesNonCatalogContent(t *testing.T) {
	input := `## Commits & Git
1. Never auto-commit.

## Skill Catalog
rules:
  - id: 25
    skill: ghost-skill
    priority: MUST
    triggers: ["x"]
    rule: "ghost"

## Project-Specific
27. Some project rule.
`
	got := filterStaleSkillRulesWith(input, map[string]bool{})

	if !strings.Contains(got, "Never auto-commit") {
		t.Error("non-catalog content was removed")
	}
	if !strings.Contains(got, "Some project rule") {
		t.Error("project-specific section was removed")
	}
}

func TestFilterStaleSkillRules_EmptyAvailableReturnsInputUnchanged(t *testing.T) {
	// When available set is nil/empty (e.g. loadAgentLoadableSkillNames failed
	// to read the filesystem), do NOT filter — pass through conservatively
	// rather than risk dropping all rules.
	input := `## Skill Catalog
rules:
  - id: 25
    skill: any-skill
    priority: MUST
    triggers: ["x"]
    rule: "y"
`
	got := filterStaleSkillRulesWith(input, nil)
	if got != input {
		t.Errorf("expected unchanged input when available set empty, got drift:\n%s", got)
	}
}

func TestFilterStaleSkillRules_KeepsAllWhenAllAvailable(t *testing.T) {
	input := `## Skill Catalog
rules:
  - id: 25
    skill: skill-a
    priority: MUST
    triggers: ["x"]
    rule: "rule a"
  - id: 26
    skill: skill-b
    priority: SHOULD
    triggers: ["y"]
    rule: "rule b"
`
	got := filterStaleSkillRulesWith(input, map[string]bool{"skill-a": true, "skill-b": true})
	if got != input {
		t.Errorf("expected unchanged input when all skills available, got drift:\n%s", got)
	}
}

func TestFilterStaleSkillRules_RealisticYesmemDocsCase(t *testing.T) {
	// Reproduces the actual bug: yesmem-docs exists in bundled catalog but
	// ~/.claude/skills/yesmem-docs/ has no SKILL.md → agent can't activate →
	// guard suggests it repeatedly → noise.
	input := `## Skill Catalog
rules:
  - id: 28
    skill: yesmem-config
    priority: MUST
    triggers: ["pin this"]
    rule: "config rule"
  - id: 29
    skill: yesmem-docs
    priority: MUST
    triggers: ["docs_search"]
    rule: "docs rule"
  - id: 50
    skill: yesmem-docs
    priority: SHOULD
    triggers: ["grep"]
    rule: "code nav rule"
`
	available := map[string]bool{
		"yesmem-config": true,
		// yesmem-docs NOT available (ghost dir)
	}
	got := filterStaleSkillRulesWith(input, available)

	if !strings.Contains(got, "skill: yesmem-config") {
		t.Error("yesmem-config should survive")
	}
	// yesmem-docs appears twice in input — both must be dropped
	if strings.Contains(got, "skill: yesmem-docs") {
		t.Error("yesmem-docs entries must both be filtered out")
	}
	if strings.Contains(got, "docs rule") || strings.Contains(got, "code nav rule") {
		t.Error("yesmem-docs rule text must be filtered out")
	}
}
