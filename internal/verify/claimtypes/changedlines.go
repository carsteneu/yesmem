package claimtypes

import (
	"fmt"
	"sort"
	"strings"

	"github.com/carsteneu/yesmem/internal/verify/engine"
)

// changedScope is the diff scope of a quality claim: the changed production lines
// grouped by language, plus what the claim could not use.
//
// The grouping exists because a repository may be polyglot: each language brings
// its own suite, and one language's suite says nothing about another's files.
type changedScope struct {
	groups  []changedGroup
	ignored []string // no language profile covers the file
	tests   []string // test files: a mutant there tests the test, not the code
}

type changedGroup struct {
	rules languageRules
	files []changedFile
}

type changedFile struct {
	name  string
	lines []int
}

// lineCount is the number of changed production lines in scope.
func (s changedScope) lineCount() int {
	total := 0
	for _, group := range s.groups {
		for _, file := range group.files {
			total += len(file.lines)
		}
	}
	return total
}

// fileCount is the number of changed production files in scope.
func (s changedScope) fileCount() int {
	total := 0
	for _, group := range s.groups {
		total += len(group.files)
	}
	return total
}

// note names what the scope left out, so a reader sees the boundary of the
// judgement instead of assuming full coverage.
func (s changedScope) note() string {
	var parts []string
	if len(s.ignored) > 0 {
		parts = append(parts, "no language profile covers: "+displayList(s.ignored))
	}
	if len(s.tests) > 0 {
		parts = append(parts, "test files not mutated: "+displayList(s.tests))
	}
	if len(parts) == 0 {
		return ""
	}
	return " (" + strings.Join(parts, "; ") + ")"
}

func displayList(names []string) string {
	displayed := make([]string, 0, len(names))
	for _, name := range names {
		displayed = append(displayed, engine.DisplayName(name))
	}
	return strings.Join(displayed, ", ")
}

// groupFor returns the group of one language, creating it on first use.
func (s *changedScope) groupFor(rules languageRules) *changedGroup {
	for index := range s.groups {
		if s.groups[index].rules.index == rules.index {
			return &s.groups[index]
		}
	}
	s.groups = append(s.groups, changedGroup{rules: rules})
	return &s.groups[len(s.groups)-1]
}

// collectChangedScope groups the changed production lines of base..head by
// language.
//
// A failed diff is a verdict, never an empty scope: an empty scope would let a
// claim about nothing read as a claim about everything.
func collectChangedScope(ctx *engine.Ctx, base, head string) (changedScope, *engine.Verdict) {
	var scope changedScope
	changed, result := ctx.ChangedLines(base, head)
	if changed == nil {
		args := engine.DiffArgs(base, head)
		verdict := engine.Unverifiable(fmt.Sprintf("could not diff %s..%s", short(base), short(head))).
			WithCommand(engine.FormatCommand(engine.RepoArgs(ctx, args...))).
			WithOutput(result.Output())
		return scope, &verdict
	}

	names := make([]string, 0, len(changed))
	for name := range changed {
		names = append(names, name)
	}
	sort.Strings(names)

	rules := compileLanguages(languages(ctx))
	for _, name := range names {
		lines := changed[name]
		if len(lines) == 0 {
			continue
		}
		languageOf, known := rules.forFile(name)
		switch {
		case !known:
			scope.ignored = append(scope.ignored, name)
		case languageOf.isTestFile(name):
			scope.tests = append(scope.tests, name)
		default:
			group := scope.groupFor(languageOf)
			sort.Ints(lines)
			group.files = append(group.files, changedFile{name: name, lines: lines})
		}
	}
	return scope, nil
}

// baseRevision resolves the base a quality claim's diff starts from.
func baseRevision(ctx *engine.Ctx, what string) (string, *engine.Verdict) {
	if ctx.Base == "" {
		verdict := engine.Unverifiable("no base revision given; " + what).
			WithClass(engine.ClassEnvironment)
		return "", &verdict
	}
	base, ok := ctx.RevParse(ctx.Base)
	if !ok {
		verdict := engine.Unverifiable(fmt.Sprintf("base revision does not resolve: %q", ctx.Base)).
			WithClass(engine.ClassEnvironment)
		return "", &verdict
	}
	return base, nil
}

// pinnedCommit resolves the commit a quality claim runs against.
func pinnedCommit(claim engine.Claim, ctx *engine.Ctx, what string) (string, *engine.Verdict) {
	value := strings.TrimSpace(claim.Commit)
	if value == "" {
		verdict := engine.Unverifiable("no commit hash to check out for " + what).
			WithClass(engine.ClassDefect)
		return "", &verdict
	}
	commit, ok := ctx.ResolveCommit(value)
	if !ok {
		verdict := engine.Unverifiable(fmt.Sprintf(
			"commit %q does not resolve to a commit in %s", value, ctx.Repo))
		return "", &verdict
	}
	return commit, nil
}
