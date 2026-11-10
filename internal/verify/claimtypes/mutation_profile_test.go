package claimtypes

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/carsteneu/yesmem/internal/verify/engine"
)

// noSuiteDir is a throwaway directory for a check that never runs a command.
func noSuiteDir(t *testing.T) string {
	t.Helper()
	parent, err := filepath.Abs(filepath.Join("..", "..", ".yesmem", "tmp", "mutation"))
	if err != nil {
		t.Fatalf("abs: %v", err)
	}
	if err := os.MkdirAll(parent, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	root, err := os.MkdirTemp(parent, "nosuite-")
	if err != nil {
		t.Fatalf("mkdtemp: %v", err)
	}
	return root
}

// A language whose profile names no test-file pattern would treat every file as
// a production line, test files included: a mutant inside a test would count as
// a kill. The claim refuses instead of guessing the boundary. The profile file
// makes this reachable by simply omitting the field for a new language.
func TestPrepareSuitesRefusesALanguageWithoutTestFilePatterns(t *testing.T) {
	withoutPatterns := toyCoverage
	withoutPatterns.TestFilePatterns = nil
	rules := compileLanguages([]engine.Language{withoutPatterns})[0]
	scope := changedScope{groups: []changedGroup{{
		rules: rules,
		files: []changedFile{{name: "impl.toy", lines: []int{1}}},
	}}}

	suites, verdict := prepareSuites(scope, unsandboxedCtx(t, noSuiteDir(t)))
	if verdict == nil {
		t.Fatalf("prepareSuites accepted a language without test-file patterns: %+v", suites)
	}
	if verdict.Class != engine.ClassDefect {
		t.Fatalf("class = %q (%s), want %q", verdict.Class, verdict.Reason, engine.ClassDefect)
	}
	if !strings.Contains(verdict.Reason, "test-file pattern") {
		t.Errorf("reason = %q, want it to name the missing pattern", verdict.Reason)
	}
}

// The same refusal through the claim, which is where a report meets it.
func TestCheckMutationRefusesAProfileWithoutTestFilePatterns(t *testing.T) {
	repo, base, head := makeMutationRepo(t, "mutationnopatterns",
		map[string]string{"impl.toy": "limit=2\n", "run.sh": "#!/bin/sh\nexit 0\n"},
		map[string]string{"impl.toy": "limit=1\n"},
	)
	language := toyCoverage
	language.TestFilePatterns = nil

	ctx := unsandboxedCtx(t, repo)
	ctx.Base = base
	ctx.Allowlist = []string{"./run.sh", "./cov.sh"}
	ctx.Languages = []engine.Language{language}

	verdict := CheckMutation(mutationClaim(head), ctx)
	if verdict.Status != engine.StatusUnverifiable || verdict.Class != engine.ClassDefect {
		t.Fatalf("status = %s class = %q (%s), want a defect for a profile without test-file patterns",
			verdict.Status, verdict.Class, verdict.Reason)
	}
}
