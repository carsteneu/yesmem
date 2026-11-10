package claimtypes

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/carsteneu/yesmem/internal/verify/engine"
)

// A LINT claim asserts that a lint tool ran clean on the checked-out code.
// The run uses the same engine as the test gate: allowlist, argument escape
// rules, sandbox, output limits and environment classification are shared, not
// copied.
var (
	lintMarkerRe = regexp.MustCompile(`\[LINT:(?P<command>[^\]\[]*?)\]`)

	twigSuccess      = regexp.MustCompile(`All (?P<count>\d+) \w+ files? contain valid syntax`)
	yamlSuccess      = regexp.MustCompile(`All (?P<count>\d+) \w+ files? contain valid syntax`)
	okInFiles        = regexp.MustCompile(`(?m)^OK in (?P<count>\d+) files?$`)
	phpLSuccess      = regexp.MustCompile(`(?m)^No syntax errors detected in (?P<path>.+?)\s*$`)
	containerSuccess = regexp.MustCompile(`The container was linted successfully`)
	composerValid    = regexp.MustCompile(`is valid`)
)

// lintTool is one supported lint tool: how to find it and what its success is.
type lintTool struct {
	name     string
	prefixes [][]string
	// success are the tool's own positive lines; exit 0 alone is not a lint
	// result.
	success []*regexp.Regexp
	// suffixes are the file kinds the tool scans under a target, so a reported
	// count can be bound to the files the checkout really holds.
	suffixes []string
	// bindsPath is true for a tool whose positive line names the checked file.
	bindsPath bool
}

var lintTools = []lintTool{
	{
		name:      "php -l",
		prefixes:  [][]string{{"php", "-l"}},
		success:   []*regexp.Regexp{phpLSuccess},
		bindsPath: true,
	},
	{
		name: "lint:twig",
		prefixes: [][]string{
			{"bin/console", "lint:twig"},
			{"php", "bin/console", "lint:twig"},
		},
		success:  []*regexp.Regexp{twigSuccess, okInFiles},
		suffixes: []string{".twig"},
	},
	{
		name: "lint:yaml",
		prefixes: [][]string{
			{"bin/console", "lint:yaml"},
			{"php", "bin/console", "lint:yaml"},
		},
		success:  []*regexp.Regexp{yamlSuccess, okInFiles},
		suffixes: []string{".yaml", ".yml"},
	},
	{
		name: "lint:container",
		prefixes: [][]string{
			{"bin/console", "lint:container"},
			{"php", "bin/console", "lint:container"},
		},
		success: []*regexp.Regexp{containerSuccess},
	},
	{
		name:     "composer validate",
		prefixes: [][]string{{"composer", "validate"}},
		success:  []*regexp.Regexp{composerValid},
	},
}

// MatchLint parses the [LINT] markers of one line.
func MatchLint(line string) []engine.Claim {
	var claims []engine.Claim
	for _, match := range lintMarkerRe.FindAllStringSubmatch(line, -1) {
		command := strings.TrimSpace(match[1])
		if command == "" {
			continue
		}
		claims = append(claims, engine.Claim{Kind: "lint", Command: command})
	}
	return claims
}

// toolFor is the tool for an argv and the length of its matched prefix.
func toolFor(argv []string) (lintTool, int, bool) {
	for _, tool := range lintTools {
		for _, prefix := range tool.prefixes {
			if len(argv) < len(prefix) {
				continue
			}
			matched := true
			for index, token := range prefix {
				if argv[index] != token {
					matched = false
					break
				}
			}
			if matched {
				return tool, len(prefix), true
			}
		}
	}
	return lintTool{}, 0, false
}

// CheckLint re-derives a [LINT] claim.
func CheckLint(claim engine.Claim, ctx *engine.Ctx) engine.Verdict {
	if guard := engine.RepoGuard(ctx); guard != nil {
		return *guard
	}
	commandText := claim.Command
	value := strings.TrimSpace(claim.Commit)
	if value == "" {
		return engine.Unverifiable("no commit hash to check out for the lint run").
			WithCommand(commandText).
			WithClass(engine.ClassDefect)
	}
	commit, ok := ctx.ResolveCommit(value)
	if !ok {
		return engine.Unverifiable(fmt.Sprintf(
			"commit %q does not resolve to a commit in %s", value, ctx.Repo)).
			WithCommand(commandText)
	}
	argv, strict, problem := engine.PrepareCommand(commandText, lintAllowlist(ctx))
	if problem != nil {
		return *problem
	}
	tool, prefixLen, ok := toolFor(argv)
	if !ok {
		// Unreachable for an allowlisted command: a safety net for a future
		// allowlist entry without an adapter.
		return engine.Unverifiable(fmt.Sprintf("no lint adapter for this command: %q", commandText)).
			WithCommand(commandText).
			WithClass(engine.ClassEnvironment)
	}
	targets := make([]string, 0, len(argv))
	for _, arg := range argv[prefixLen:] {
		if !strings.HasPrefix(arg, "-") {
			targets = append(targets, arg)
		}
	}
	if tool.bindsPath && len(targets) == 0 {
		// "php -l" lints stdin when no file is named: the run would check
		// nothing from the checkout, so it can never confirm the claim.
		return engine.Unverifiable(fmt.Sprintf(
			"%q needs a file target: without one the run checks no file from the checkout", tool.name)).
			WithCommand(commandText)
	}
	// The starter of the tool: a path token inside the matched prefix
	// (bin/console), whether it stands alone or behind the php launcher.
	launcher := ""
	for _, token := range argv[:prefixLen] {
		if strings.Contains(token, "/") {
			launcher = token
			break
		}
	}
	var expectedCount int

	preRun := func(dir, commandDesc string) *engine.Verdict {
		// The same containment the test gate enforces: an argument that
		// resolves outside the checkout would run or scan something the commit
		// does not contain.
		if escape := engine.SymlinkEscape(argv, dir, strict); escape != "" {
			abort := engine.Unverifiable(
				fmt.Sprintf("command argument resolves outside the checkout: %q", escape)).
				WithCommand(commandDesc).
				WithClass(engine.ClassDefect)
			return &abort
		}
		// A repository-provided launcher must come from the checkout and be
		// executable; the tool's own absence is an environment gap.
		if launcher != "" {
			path := filepath.Join(dir, launcher)
			info, err := os.Stat(path)
			if err != nil || info.IsDir() || info.Mode().Perm()&0o111 == 0 {
				abort := engine.Unverifiable(
					fmt.Sprintf("the checkout's %q is missing or not executable", launcher)).
					WithCommand(commandDesc).
					WithClass(engine.ClassEnvironment)
				return &abort
			}
		}
		for _, target := range targets {
			if _, err := os.Stat(filepath.Join(dir, target)); err != nil {
				abort := engine.Unverifiable(
					fmt.Sprintf("the lint target %q is not in the checked-out commit", target)).
					WithCommand(commandDesc).
					WithClass(engine.ClassEnvironment)
				return &abort
			}
		}
		if len(tool.suffixes) > 0 && len(targets) > 0 {
			count := 0
			for _, target := range targets {
				found, ok := countSuffixes(dir, target, tool.suffixes)
				if !ok {
					abort := engine.Unverifiable(
						fmt.Sprintf("the lint target %q cannot be scanned in the checkout", target)).
						WithCommand(commandDesc).
						WithClass(engine.ClassEnvironment)
					return &abort
				}
				count += found
			}
			if count == 0 {
				// The real linters report success for an empty target; a run
				// that checks no file proves nothing, exactly like "0 passing"
				// in the test gate.
				abort := engine.Unverifiable(fmt.Sprintf(
					"the lint target %q holds no files for this tool; a run that checks nothing confirms nothing",
					targets[0])).
					WithCommand(commandDesc).
					WithClass(engine.ClassUnverifiable)
				return &abort
			}
			expectedCount = count
		}
		return nil
	}

	outcome, problem := ctx.RunInCheckoutHook("lint", commandText, commit, argv, preRun)
	if problem != nil {
		return *problem
	}
	return lintVerdict(claim, tool, targets, expectedCount, outcome, argv)
}

func lintVerdict(claim engine.Claim, tool lintTool, targets []string, expectedCount int, outcome *engine.RunOutcome, argv []string) engine.Verdict {
	note := outcome.Note
	if outcome.Code == 0 {
		var matched []string
		for _, pattern := range tool.success {
			if found := pattern.FindStringSubmatch(outcome.RawOutput); found != nil {
				matched = found
				break
			}
		}
		if matched == nil {
			// Exit 0 without the tool's own success line is not a lint result:
			// an aborted or hijacked run must not read as clean.
			return engine.Unverifiable(fmt.Sprintf(
				"the command exited 0 but its output carries no success line of %q; "+
					"exit 0 alone is not a lint result", tool.name) + note).
				WithCommand(outcome.CommandDesc).
				WithOutput(outcome.Output).
				WithSandboxed(outcome.Sandboxed)
		}
		if expectedCount > 0 {
			if reported, ok := namedCount(tool.success, outcome.RawOutput); ok && reported != expectedCount {
				return engine.Unverifiable(fmt.Sprintf(
					"the success line reports %d files, but the checkout holds %d matching the target; "+
						"the run was redirected", reported, expectedCount) + note).
					WithCommand(outcome.CommandDesc).
					WithOutput(outcome.Output).
					WithSandboxed(outcome.Sandboxed).
					WithClass(engine.ClassDefect)
			}
		}
		if tool.bindsPath && len(targets) > 0 {
			reportedPath := namedPath(tool.success, outcome.RawOutput)
			if filepath.Clean(reportedPath) != filepath.Clean(targets[0]) {
				return engine.Unverifiable(fmt.Sprintf(
					"the success line names %q, but the checked target is %q; "+
						"the run checked something else", reportedPath, targets[0]) + note).
					WithCommand(outcome.CommandDesc).
					WithOutput(outcome.Output).
					WithSandboxed(outcome.Sandboxed).
					WithClass(engine.ClassDefect)
			}
		}
		evidence := matched[0]
		evidence = engine.TruncateUTF8(evidence, 160)
		return engine.Confirmed(fmt.Sprintf("%q exited 0; evidence: %s%s",
			claim.Command, strings.TrimSpace(evidence), note)).
			WithCommand(outcome.CommandDesc).
			WithOutput(outcome.Output).
			WithSandboxed(outcome.Sandboxed)
	}
	if outcome.Code < 0 {
		return engine.Unverifiable(fmt.Sprintf(
			"the lint run was killed by signal %d; its outcome cannot be verified%s", -outcome.Code, note)).
			WithCommand(outcome.CommandDesc).
			WithOutput(outcome.Output).
			WithSandboxed(outcome.Sandboxed)
	}
	if missingDependency(argv, outcome.RawOutput) {
		return engine.Unverifiable(
			"the tool's dependencies are missing in the checkout (no vendor/, no composer install); " +
				"nothing was linted" + note).
			WithCommand(outcome.CommandDesc).
			WithOutput(outcome.Output).
			WithSandboxed(outcome.Sandboxed).
			WithClass(engine.ClassEnvironment)
	}
	first := ""
	for _, line := range strings.Split(outcome.Output, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			first = trimmed
			break
		}
	}
	first = engine.TruncateUTF8(first, 160)
	return engine.Refuted(fmt.Sprintf("the lint run exited %d: %s%s",
		outcome.Code, first, note)).
		WithCommand(outcome.CommandDesc).
		WithOutput(outcome.Output).
		WithSandboxed(outcome.Sandboxed)
}

// namedCount is the "count" group of the first success line that carries one.
func namedCount(patterns []*regexp.Regexp, output string) (int, bool) {
	for _, pattern := range patterns {
		found := pattern.FindStringSubmatch(output)
		if found == nil {
			continue
		}
		for index, name := range pattern.SubexpNames() {
			if name == "count" && index < len(found) {
				count := 0
				if _, err := fmt.Sscanf(found[index], "%d", &count); err == nil {
					return count, true
				}
			}
		}
	}
	return 0, false
}

// namedPath is the "path" group of the first success line that carries one.
func namedPath(patterns []*regexp.Regexp, output string) string {
	for _, pattern := range patterns {
		found := pattern.FindStringSubmatch(output)
		if found == nil {
			continue
		}
		for index, name := range pattern.SubexpNames() {
			if name == "path" && index < len(found) {
				return strings.TrimSpace(found[index])
			}
		}
	}
	return ""
}

// countSuffixes counts the files with the tool's suffixes under target.
func countSuffixes(checkout, target string, suffixes []string) (int, bool) {
	path := filepath.Join(checkout, target)
	info, err := os.Stat(path)
	if err != nil {
		return 0, false
	}
	if !info.IsDir() {
		for _, suffix := range suffixes {
			if strings.HasSuffix(path, suffix) {
				return 1, true
			}
		}
		return 0, true
	}
	count := 0
	err = filepath.Walk(path, func(_ string, walked os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if walked.IsDir() {
			return nil
		}
		for _, suffix := range suffixes {
			if strings.HasSuffix(walked.Name(), suffix) {
				count++
				return nil
			}
		}
		return nil
	})
	if err != nil {
		return 0, false
	}
	return count, true
}

// lintAllowlist is the lint allowlist.
//
// The list is separate from the test allowlist on purpose: a [LINT] claim can
// never smuggle a test runner, and vice versa. A caller that wants a further
// lint command extends this list, not the test list.
func lintAllowlist(ctx *engine.Ctx) []string {
	if len(ctx.LintAllowlist) > 0 {
		return ctx.LintAllowlist
	}
	return DefaultLintAllowlist
}
