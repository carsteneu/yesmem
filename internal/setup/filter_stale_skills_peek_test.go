package setup

import (
	"strings"
	"testing"
)

func TestPeekSkillName_NoSkillFieldStopsAtNextEntry(t *testing.T) {
	// Entry 1 has id + rule but NO skill field.
	// Entry 2 has id + skill field.
	// peekSkillName on entry 1's first line must return "" — NOT fall through
	// into entry 2 and return entry 2's skill.
	entryLines := []string{
		"  - id: 30",
		"    priority: MUST",
		"    triggers: [\"x\"]",
		"    rule: \"rule with no skill field\"",
		"  - id: 31",
		"    skill: real-skill",
		"    rule: \"next entry\"",
	}
	got := peekSkillName(entryLines)
	if got != "" {
		t.Errorf("expected \"\" for entry without skill field, got %q (falls through to next entry)", got)
	}
}

func TestPeekSkillName_FindsSkillInSameEntry(t *testing.T) {
	entryLines := []string{
		"  - id: 30",
		"    priority: MUST",
		"    skill: target-skill",
		"    triggers: [\"x\"]",
		"    rule: \"rule\"",
		"  - id: 31",
		"    skill: other-skill",
	}
	got := peekSkillName(entryLines)
	if got != "target-skill" {
		t.Errorf("expected \"target-skill\", got %q", got)
	}
}

func TestFilterStaleSkillRules_DoesNotFallThroughForMissingSkill(t *testing.T) {
	// Regression test: previously peekSkillName fell through to the next
	// entry when an entry lacked a skill field, causing the NEXT entry to be
	// incorrectly filtered out. Verify the next entry survives.
	input := `## Skill Catalog
rules:
  - id: 30
    priority: MUST
    triggers: ["x"]
    rule: "no skill field here"
  - id: 31
    skill: available-skill
    priority: MUST
    triggers: ["y"]
    rule: "should survive"
`
	available := map[string]bool{"available-skill": true}
	got := filterStaleSkillRulesWith(input, available)

	if !strings.Contains(got, "skill: available-skill") {
		t.Error("available skill was dropped because previous entry had no skill field")
	}
}
