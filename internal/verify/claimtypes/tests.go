package claimtypes

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/carsteneu/yesmem/internal/verify/engine"
)

// testEvidencePatterns are the positive signals that tests actually ran.
//
// A green claim without any of them is unverifiable: exiting 0 is not proof
// that tests executed, and a zero-count summary ("0 passing") is not either.
var testEvidencePatterns = []*regexp.Regexp{
	regexp.MustCompile(`\bRan [1-9]\d* tests?\b`),
	regexp.MustCompile(`(?i)\b[1-9]\d* (?:passed|failed|errors?)\b`),
	regexp.MustCompile(`(?i)\b[1-9]\d* (?:passing|failing)\b`),
	regexp.MustCompile(`\b(?:tests?|pass)\s+[1-9]\d*\b`),
	regexp.MustCompile(`(?i)\btest result:\s*\w+\s+[1-9]\d* passed`),
	regexp.MustCompile(`(?im)^\s*tests?:\s.*\b[1-9]\d*\b`),
	regexp.MustCompile(`(?m)^(?:ok|FAIL)\s+\S`),
	regexp.MustCompile(`(?m)^--- (?:PASS|FAIL):`),
	regexp.MustCompile(`(?im)^\s*(?:pass|fail)\s+[1-9]\d*$`),
	// PHPUnit: the text summary and the machine-readable TeamCity stream.
	regexp.MustCompile(`\bOK \([1-9]\d* tests?, \d+ assertions?\)`),
	regexp.MustCompile(`(?m)^##teamcity\[testFinished\b`),
}

var (
	// TestFailurePatterns are signals that tests did not all pass. They are
	// NOT evidence that the suite is green: a runner whose exit code a wrapper
	// swallowed can print "3 passed, 1 failed" and still exit 0, and a green
	// claim must never be confirmed from output that reports failures.
	testFailurePatterns = []*regexp.Regexp{
		regexp.MustCompile(`(?i)\b[1-9]\d* (?:failed|failing|errors?)\b`),
		regexp.MustCompile(`(?m)^(?:FAIL|FAILED)\s+\S`),
		regexp.MustCompile(`(?m)^--- FAIL:`),
		regexp.MustCompile(`(?im)^\s*(?:fail|failed|failing)\s+[1-9]\d*$`),
	}
	noTestsPatterns = []*regexp.Regexp{
		regexp.MustCompile(`(?i)\bran 0 tests?\b`),
		regexp.MustCompile(`(?i)\bno test files\b`),
		regexp.MustCompile(`(?i)\bno tests? (?:were |was )?(?:ran|run|found|collected|to run)\b`),
		regexp.MustCompile(`(?im)^\s*running 0 tests\s*$`),
		regexp.MustCompile(`(?im)^\s*0 tests?\s*$`),
	}
	// Legacy suites hide in skips: "3 passed, 2 skipped" must not read like a
	// clean "3 passed". Display only; the counters never feed a verdict.
	skipCounterPattern = regexp.MustCompile(`(?i)\b[1-9]\d* (?:skipped|pending|ignored|incomplete)\b|\bskipped=[1-9]\d*`)
	missingModuleRe    = regexp.MustCompile(`No module named '?([A-Za-z_][\w.]*)'?`)
	writeLimitRe       = regexp.MustCompile(`\[Errno 27\]|File too large`)
	// The startup failures of the PHP-family tools when the checkout has no
	// vendor/ directory: an environment gap, never a test outcome.
	missingDependencyPatterns = []*regexp.Regexp{
		regexp.MustCompile(`(?i)Failed opening required '?[^'\s]*vendor/autoload\.php`),
		regexp.MustCompile(`(?i)vendor/autoload\.php[^\n]*Failed to open stream`),
		regexp.MustCompile(`(?i)Could not open input file: [^\s]*phpunit`),
		regexp.MustCompile(`(?i)Class "PHPUnit\\`),
		regexp.MustCompile(`(?i)please run .{0,40}composer install`),
		regexp.MustCompile(`(?i)try running .{0,40}composer install`),
	}
	knownRunnerModules = []string{"unittest", "pytest", "nose2", "difflib"}
	pythonTokens       = map[string]bool{"python": true, "python2": true, "python3": true, "pytest": true, "py.test": true}
	// A compiled toolchain needs a module cache and, in a fresh checkout, often
	// a network. A checkout without either is an environment gap: the run says
	// nothing about the code and must not read as a failing suite.
	toolchainTokens = map[string]bool{"go": true, "cargo": true, "npm": true, "yarn": true, "bun": true, "pnpm": true, "npx": true}
	// The phrases only count when the report's own command is a toolchain
	// command: child output is repository-controlled and must not be able to
	// turn a refutation into an unverifiable.
	toolchainGapPatterns = []*regexp.Regexp{
		regexp.MustCompile(`(?m)^go: downloading `),
		regexp.MustCompile(`(?i)cannot find main module`),
		regexp.MustCompile(`(?i)missing go\.sum entry`),
		regexp.MustCompile(`(?i)(?:dial tcp|lookup )[^\n]*(?:connection refused|no such host|i/o timeout)`),
		regexp.MustCompile(`(?i)failed to download|could not fetch|unable to fetch`),
	}
)

// CheckTests judges a "Tests run: <command> -> exit <n>" claim.
//
// Exit 0 alone is not a green claim: a confirmed tests_green needs a positive
// test summary in the output. A non-zero exit is an honest failure report and
// is verified against its own exit code, never read as green.
func CheckTests(claim engine.Claim, ctx *engine.Ctx) engine.Verdict {
	if guard := engine.RepoGuard(ctx); guard != nil {
		return *guard
	}
	commandText := claim.Command
	value := strings.TrimSpace(claim.Commit)
	if value == "" {
		return engine.Unverifiable("no commit hash to check out for the test run").
			WithCommand(commandText).
			WithClass(engine.ClassDefect)
	}
	commit, ok := ctx.ResolveCommit(value)
	if !ok {
		return engine.Unverifiable(fmt.Sprintf(
			"commit %q does not resolve to a commit in %s", value, ctx.Repo)).
			WithCommand(commandText)
	}
	argv, strict, problem := engine.PrepareProfileCommand(commandText, ctx.Allowlist, ctx.ProfileTestCommands)
	if problem != nil {
		return *problem
	}

	preRun := func(dir, commandDesc string) *engine.Verdict {
		if shadow := shadowedModule(dir, argv); shadow != "" {
			abort := engine.Unverifiable(fmt.Sprintf(
				"the checkout shadows the %q module; the runner would not be the real one", shadow)).
				WithCommand(commandDesc).
				WithClass(engine.ClassEnvironment)
			return &abort
		}
		if escape := engine.SymlinkEscape(argv, dir, strict); escape != "" {
			abort := engine.Unverifiable(
				fmt.Sprintf("command argument resolves outside the checkout: %q", escape)).
				WithCommand(commandDesc).
				WithClass(engine.ClassDefect)
			return &abort
		}
		return nil
	}

	outcome, problem := ctx.RunInCheckoutHook("checkout", commandText, commit, argv, preRun)
	if problem != nil {
		return *problem
	}
	return testsVerdict(claim, outcome, argv)
}

func testsVerdict(claim engine.Claim, outcome *engine.RunOutcome, argv []string) engine.Verdict {
	note := outcome.Note
	if outcome.Code == claim.ClaimedExit {
		if claim.Kind != "tests_green" {
			return engine.Confirmed(fmt.Sprintf("%q exited %d as claimed%s",
				claim.Command, outcome.Code, note)).
				WithCommand(outcome.CommandDesc).
				WithOutput(outcome.Output).
				WithSandboxed(outcome.Sandboxed)
		}
		// Exiting 0 is not proof that tests ran: require a positive test
		// summary, and treat "no tests" output as unverifiable.
		switch {
		case showsTestFailure(outcome.RawOutput):
			// The output contradicts the green claim. This is the case a
			// wrapper creates by swallowing the runner's exit code, so it must
			// never be read as a confirmation.
			return engine.Unverifiable(
				"the command exited 0 but its output reports failing tests" + note).
				WithCommand(outcome.CommandDesc).
				WithOutput(outcome.Output).
				WithSandboxed(outcome.Sandboxed)
		case showsTestEvidence(outcome.RawOutput):
			return engine.Confirmed(fmt.Sprintf("%q exited %d as claimed; %s%s",
				claim.Command, outcome.Code, evidenceSummary(outcome.RawOutput), note)).
				WithCommand(outcome.CommandDesc).
				WithOutput(outcome.Output).
				WithSandboxed(outcome.Sandboxed)
		case claimsNoTests(outcome.RawOutput):
			return engine.Unverifiable(
				"the command exited 0 but reported that no tests were executed" + note).
				WithCommand(outcome.CommandDesc).
				WithOutput(outcome.Output).
				WithSandboxed(outcome.Sandboxed)
		default:
			return engine.Unverifiable(
				"the command exited 0 but its output shows no evidence that tests ran" + note).
				WithCommand(outcome.CommandDesc).
				WithOutput(outcome.Output).
				WithSandboxed(outcome.Sandboxed)
		}
	}

	if outcome.Code < 0 {
		// A process killed by a signal (OOM, external kill) says nothing about
		// the suite: -1 is not a result the report could have claimed.
		return engine.Unverifiable(fmt.Sprintf(
			"the command was killed by signal %d; its outcome cannot be verified%s", -outcome.Code, note)).
			WithCommand(outcome.CommandDesc).
			WithOutput(outcome.Output).
			WithSandboxed(outcome.Sandboxed)
	}
	if toolchainGap(argv, outcome.RawOutput) {
		return engine.Unverifiable(fmt.Sprintf(
			"the toolchain cannot run in this environment (no module cache, no network); "+
				"the run says nothing about the code%s", note)).
			WithCommand(outcome.CommandDesc).
			WithOutput(outcome.Output).
			WithSandboxed(outcome.Sandboxed).
			WithClass(engine.ClassEnvironment)
	}
	// A missing runner module is an environment gap, not evidence that the
	// tests failed, but only when the report's own command names it. Child
	// output is repository-controlled and must not be able to turn a
	// refutation into an unverifiable by printing the phrase.
	if match := missingModuleRe.FindStringSubmatch(outcome.RawOutput); match != nil {
		missing := strings.Split(match[1], ".")[0]
		if runnerModule := moduleAfterFlag(argv, "-m"); runnerModule != "" && missing == runnerModule {
			return engine.Unverifiable(fmt.Sprintf(
				"the runner module %q is not available in this environment%s", missing, note)).
				WithCommand(outcome.CommandDesc).
				WithOutput(outcome.Output).
				WithSandboxed(outcome.Sandboxed).
				WithClass(engine.ClassEnvironment)
		}
	}
	if missingDependency(argv, outcome.RawOutput) {
		return engine.Unverifiable(
			"the runner's dependencies are missing in the checkout (no vendor/, no composer install); " +
				"the run stopped before any test" + note).
			WithCommand(outcome.CommandDesc).
			WithOutput(outcome.Output).
			WithSandboxed(outcome.Sandboxed).
			WithClass(engine.ClassEnvironment)
	}
	if writeLimitRe.MatchString(outcome.RawOutput) {
		return engine.Unverifiable(fmt.Sprintf(
			"the run hit the per-file write limit of %d bytes (built-in limit); "+
				"its outcome cannot be verified", engine.MaxOutputBytes) + note).
			WithCommand(outcome.CommandDesc).
			WithOutput(outcome.Output).
			WithSandboxed(outcome.Sandboxed).
			WithClass(engine.ClassLimit)
	}
	return engine.Refuted(fmt.Sprintf("claimed exit %d, actually exited %d%s",
		claim.ClaimedExit, outcome.Code, note)).
		WithCommand(outcome.CommandDesc).
		WithOutput(outcome.Output).
		WithSandboxed(outcome.Sandboxed)
}

func showsTestEvidence(output string) bool {
	for _, pattern := range testEvidencePatterns {
		if pattern.MatchString(output) {
			return true
		}
	}
	return false
}

// showsTestFailure reports whether the output names failing tests.
//
// Evidence that tests ran and evidence that they passed are two different
// things: a green claim needs the first and must not survive the second.
func showsTestFailure(output string) bool {
	for _, pattern := range testFailurePatterns {
		if pattern.MatchString(output) {
			return true
		}
	}
	return false
}

func claimsNoTests(output string) bool {
	for _, pattern := range noTestsPatterns {
		if pattern.MatchString(output) {
			return true
		}
	}
	return false
}

// evidenceSummary is the positive test evidence in one line, with its counters
// appended. Display only: it makes a green verdict readable, and never feeds a
// verdict.
func evidenceSummary(output string) string {
	line := ""
	for _, candidate := range strings.Split(output, "\n") {
		trimmed := strings.TrimSpace(candidate)
		if trimmed != "" && showsTestEvidence(trimmed) {
			trimmed = engine.TruncateUTF8(trimmed, 160)
			line = trimmed
			break
		}
	}

	var counters []string
	for _, match := range skipCounterPattern.FindAllString(output, -1) {
		duplicate := false
		for _, seen := range counters {
			if strings.EqualFold(seen, match) {
				duplicate = true
				break
			}
		}
		if duplicate {
			continue
		}
		counters = append(counters, match)
		if len(counters) >= 4 {
			break
		}
	}
	var kept []string
	for _, counter := range counters {
		if !strings.Contains(strings.ToLower(line), strings.ToLower(counter)) {
			kept = append(kept, counter)
		}
	}

	var pieces []string
	if line != "" {
		pieces = append(pieces, "evidence: "+line)
	}
	if len(kept) > 0 {
		pieces = append(pieces, "counters: "+strings.Join(kept, ", "))
	}
	return strings.Join(pieces, "; ")
}

// moduleAfterFlag is the module named after the first occurrence of flag.
func moduleAfterFlag(argv []string, flag string) string {
	for index := 0; index+1 < len(argv); index++ {
		if argv[index] == flag {
			return strings.Split(argv[index+1], ".")[0]
		}
	}
	return ""
}

// shadowedModule is a module the checkout would shadow for this command.
//
// "python3 -m unittest" imports from the working directory first, so a
// committed unittest.py would fabricate the runner's output. Wrapper commands
// hide the runner name, so the known runner modules are checked for them too.
//
// Boundary: this port does not carry the Python standard-library module list,
// so a committed difflib.py is only detected for wrapper commands through the
// known list. The runner module itself is always checked.
func shadowedModule(checkout string, argv []string) string {
	if module := moduleAfterFlag(argv, "-m"); module != "" && module != "." && module != ".." {
		if modulePresent(checkout, module) {
			return module
		}
	}
	var names []string
	for _, arg := range argv {
		if pythonTokens[filepath.Base(arg)] {
			names = knownRunnerModules
			break
		}
	}
	if names == nil && engine.IsWrapper(argv) {
		names = knownRunnerModules
	}
	for _, name := range names {
		if modulePresent(checkout, name) {
			return name
		}
	}
	return ""
}

func modulePresent(checkout, module string) bool {
	if info, err := os.Stat(filepath.Join(checkout, module+".py")); err == nil && !info.IsDir() {
		return true
	}
	packageDir := filepath.Join(checkout, module)
	if info, err := os.Stat(packageDir); err != nil || !info.IsDir() {
		return false
	}
	for _, marker := range []string{"__init__.py", "__main__.py"} {
		if info, err := os.Stat(filepath.Join(packageDir, marker)); err == nil && !info.IsDir() {
			return true
		}
	}
	return false
}

// missingDependency is true when a PHP-family runner's dependencies are
// visibly absent.
//
// A checkout without vendor/ is an environment gap, never a defect and never a
// confirmation. The output is repository-controlled, so the phrases only count
// when the command itself is a PHP-family runner and no test evidence exists.
// toolchainGap is true when a compiled toolchain could not do its work.
func toolchainGap(argv []string, output string) bool {
	if len(argv) == 0 {
		return false
	}
	if !toolchainTokens[filepath.Base(argv[0])] {
		return false
	}
	for _, pattern := range toolchainGapPatterns {
		if pattern.MatchString(output) {
			return true
		}
	}
	return false
}

func missingDependency(argv []string, output string) bool {
	if len(argv) == 0 || showsTestEvidence(output) {
		return false
	}
	tool := filepath.Base(argv[0])
	if tool != "composer" && tool != "phpunit" && !strings.HasPrefix(tool, "php") {
		return false
	}
	for _, pattern := range missingDependencyPatterns {
		if pattern.MatchString(output) {
			return true
		}
	}
	return false
}
