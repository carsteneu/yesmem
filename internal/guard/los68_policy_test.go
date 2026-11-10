package guard

import (
	"os"
	"path/filepath"
	"testing"
)

// TestPolicySetMatchesRulesFile keeps the production parser and the Decision Policy in step: the policy is
// repository content, and a rule moved into or out of its BLOCK bullet must force a change here. RULES.md is
// excluded from the public mirror (scripts/sync-public.sh), so an absent file skips instead of failing.
func TestPolicySetMatchesRulesFile(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "RULES.md"))
	if err != nil {
		t.Skipf("RULES.md not available: %v", err)
	}

	set := blockableRuleNumbers(string(data))

	for _, number := range []int{1, 2, 25, 32} {
		if !set[number] {
			t.Errorf("the Decision Policy blocks on rule %d, the parser did not find it (set=%v)", number, set)
		}
	}
	for number := range set {
		if number != 1 && number != 2 && number != 25 && number != 32 {
			t.Errorf("the parser found rule %d, which the policy does not class as blockable", number)
		}
	}
}

// TestPolicyBulletParseJoinsWrappedLines pins the parser on a reflowed bullet: a rule on the continuation line
// must not be lost, or the enforceable set silently narrows.
func TestPolicyBulletParseJoinsWrappedLines(t *testing.T) {
	wrapped := `## Decision Policy

- **BLOCK** only on objectively detectable violations: LLM signature (rule 1),
  secrets in diff (rule 2),
  destructive bash patterns (rule 25),
  PR merge via CLI or API (rule 32).
- **SUGGEST** on rules where authorization matters: auto-commit (rule 3).

## Commits
`
	set := blockableRuleNumbers(wrapped)

	for _, number := range []int{1, 2, 25, 32} {
		if !set[number] {
			t.Errorf("wrapped bullet lost rule %d (set=%v)", number, set)
		}
	}
	if set[3] {
		t.Errorf("the SUGGEST bullet must not be enforceable (set=%v)", set)
	}
}
