package claimtypes

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/carsteneu/yesmem/internal/verify/engine"
)

// The mutation claim: a changed production line counts as tested only when an
// edit to it makes the suite fail.
//
// A green suite proves that tests ran, not that they look at the changed line.
// The claim closes that gap by changing the line on purpose and asking the suite
// again: a mutant the suite misses is a line no test depends on, and that is the
// finding.
const (
	// MutationMaxMutants bounds the mutants one claim runs. The suite runs once
	// per mutant, and a claim that never ends proves nothing.
	MutationMaxMutants = 25
	// MutationMaxFiles bounds the changed files one claim may mutate.
	MutationMaxFiles = 5
	// MutationMaxDuration bounds the wall clock of the mutant runs, independent
	// of the run's own budget: this is the most expensive claim the verifier has.
	MutationMaxDuration = 2 * time.Minute
	// MaxMutationFindings is how many discarded mutants a verdict names. The
	// count is exact and all survivors are named: a tolerated survivor has to be
	// justifiable by the reader.
	MaxMutationFindings = 10
	// MutationMaxSourceBytes bounds a source file the claim reads and rewrites. It
	// is far above any hand-written file and far below what would hurt the
	// daemon's heap: the mutants hold the file content.
	MutationMaxSourceBytes = 8 << 20
	// mutationScopeToken is the only scope a [MUTATION] claim may name.
	mutationScopeToken = "changed-lines"
)

var mutationMarkerRe = regexp.MustCompile(`\[MUTATION:(?P<body>[^\[\]]*?)\]`)

// MatchMutation parses the [MUTATION] markers of one line.
//
// The marker asserts that the changed production lines are mutation-sensitive:
// an edit to one of them makes the suite fail. It is a statement about the
// sensitivity of the lines, never about the correctness of what they should do.
//
// The body names the scope and may declare how many survivors the report accepts:
// "[MUTATION: changed-lines survivors=2]". Without the allowance the claim accepts
// none. A survivor the report justifies (log, format, ordering without a behaviour
// contract, a defensive branch with no real path) is not a false confirmation, so
// the count is the report's statement and the verdict names every survivor.
func MatchMutation(line string) []engine.Claim {
	var claims []engine.Claim
	for _, match := range mutationMarkerRe.FindAllStringSubmatch(line, -1) {
		body := strings.Join(strings.Fields(strings.ToLower(match[1])), " ")
		claim := engine.Claim{Kind: "mutation", Token: body}
		fields := strings.Fields(body)
		if len(fields) > 0 && fields[0] == mutationScopeToken {
			if allowance, ok := parseSurvivorAllowance(fields[1:]); ok {
				claim.Token = mutationScopeToken
				claim.Allowance = allowance
				claim.HasAllowance = len(fields) > 1
			}
		}
		claims = append(claims, claim)
	}
	return claims
}

// parseSurvivorAllowance reads the optional "survivors=<n>" field.
//
// A body the verifier cannot bind stays unparsed, and the checker reports it as a
// defect: the marker must not be ignorable by writing a shape nobody checks.
func parseSurvivorAllowance(fields []string) (int, bool) {
	allowance := 0
	for index, field := range fields {
		value, found := strings.CutPrefix(field, "survivors=")
		if !found {
			return 0, false
		}
		if index > 0 {
			// A second allowance is a contradiction, not a sum.
			return 0, false
		}
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 0 {
			return 0, false
		}
		allowance = parsed
	}
	return allowance, true
}

// mutant is one single-line mutation of one changed production line, with the
// whole mutated file content ready to be written.
type mutant struct {
	file     string
	line     int
	operator string
	content  string
	group    int
}

// CheckMutation re-derives a [MUTATION] claim.
//
// Every changed production line is mutated with a generic operator, the suite
// runs against the mutation, and the run decides the class:
//
//	green suite      -> survivor, the finding
//	red suite        -> killed, the line is covered
//	red + build error-> discarded, it proves nothing and is never counted as a kill
func CheckMutation(claim engine.Claim, ctx *engine.Ctx) engine.Verdict {
	if guard := engine.RepoGuard(ctx); guard != nil {
		return *guard
	}
	if claim.Token != mutationScopeToken {
		return engine.Unverifiable(fmt.Sprintf(
			"unknown mutation scope %q; this verifier can only judge %q",
			claim.Token, mutationScopeToken)).
			WithClass(engine.ClassDefect)
	}
	allowance := 0
	if claim.HasAllowance {
		allowance = claim.Allowance
	}
	commit, verdict := pinnedCommit(claim, ctx, "the mutation run")
	if verdict != nil {
		return *verdict
	}
	base, verdict := baseRevision(ctx, "the mutation scope cannot be compared")
	if verdict != nil {
		return *verdict
	}
	scope, verdict := collectChangedScope(ctx, base, commit)
	if verdict != nil {
		return *verdict
	}
	if len(scope.groups) == 0 {
		return engine.Unverifiable(
			"no changed production line in scope; there is nothing to mutate" + scope.note()).
			WithClass(engine.ClassDefect)
	}
	if scope.fileCount() > MutationMaxFiles {
		return engine.Unverifiable(fmt.Sprintf(
			"the diff touches %d production files; the built-in budget covers %d (built-in limit)",
			scope.fileCount(), MutationMaxFiles)).
			WithClass(engine.ClassLimit)
	}

	suites, verdict := prepareSuites(scope, ctx)
	if verdict != nil {
		return *verdict
	}

	checkout, failure := ctx.NewCheckout("mutation", commit)
	if failure != nil {
		return *failure
	}
	defer checkout.Close()

	// The candidates are read before the first suite run: a diff that offers no
	// mutant must not cost a full test run.
	mutants, originals, created, err := mutantCandidates(checkout.Dir, scope)
	if err != nil {
		return engine.Unverifiable("cannot read a changed file from the checkout: " + err.Error()).
			WithClass(engine.ClassEnvironment)
	}
	if created == 0 {
		// No mutant can be built from the changed content: the lines hold no
		// code. The claim asserts a sensitivity that cannot exist, so the fault
		// is the report's form and not the machine's.
		return engine.Unverifiable(fmt.Sprintf(
			"the %d changed production line(s) offer no mutant; "+
				"the claim is not checkable for this diff%s",
			scope.lineCount(), scope.note())).
			WithClass(engine.ClassDefect)
	}
	if allowance >= len(mutants) {
		// The allowance has to leave at least one mutant to observe: otherwise
		// the claim tolerates every survivor it could produce and asserts
		// nothing, while still reading as a confirmation.
		return engine.Unverifiable(fmt.Sprintf(
			"the claim tolerates %d survivors of the %d mutants it would run; "+
				"that tolerates every survivor and asserts nothing", allowance, len(mutants))).
			WithClass(engine.ClassDefect)
	}

	for _, suite := range suites {
		if escape := engine.SymlinkEscape(suite.argv, checkout.Dir, suite.strict); escape != "" {
			return engine.Unverifiable(fmt.Sprintf(
				"the suite argument resolves outside the checkout: %q", escape)).
				WithCommand(suite.command).
				WithClass(engine.ClassDefect)
		}
	}

	now := claimClock(ctx)
	deadline := now().Add(MutationMaxDuration)
	// The claim's wall clock is a ceiling per run as well: one hanging suite
	// must not consume the budget of twenty-five mutants.
	runBudget := func() time.Duration {
		remaining := deadline.Sub(now())
		if remaining > engine.DefaultTestTimeout {
			return engine.DefaultTestTimeout
		}
		return remaining
	}

	// The suite has to be green on the unmutated tree first. A suite that is
	// already red fails on every mutant, which would read as a full kill and
	// confirm a claim nobody proved.
	for _, suite := range suites {
		outcome, problem := ctx.RunInDirWithin(checkout.Dir, suite.command, suite.argv, runBudget())
		if problem != nil {
			return *problem
		}
		if outcome.Code != 0 {
			return engine.Unverifiable(fmt.Sprintf(
				"the suite %q does not pass on the unmutated checkout (exit %d); "+
					"a red baseline would make every mutant read as killed",
				suite.command, outcome.Code)).
				WithCommand(outcome.CommandDesc).
				WithOutput(outcome.Output).
				WithSandboxed(outcome.Sandboxed).
				WithClass(engine.ClassEnvironment)
		}
	}

	commandText := suiteText(suites)
	killed, survived, discarded, ran := 0, 0, 0, 0
	sandboxed := false
	var findings []string
	var rejected []string
	var limits []string
	if created > len(mutants) {
		limits = append(limits, fmt.Sprintf(
			"the diff yields %d mutants; the built-in budget covers %d",
			created, MutationMaxMutants))
	}

	for _, candidate := range mutants {
		if now().After(deadline) || ctx.BudgetSpent() {
			limits = append(limits, fmt.Sprintf(
				"the wall clock budget of %s was exhausted after %d of %d mutants",
				MutationMaxDuration, ran, created))
			break
		}
		original, verdict := originalOf(checkout.Dir, candidate.file, originals)
		if verdict != nil {
			return *verdict
		}
		path := filepath.Join(checkout.Dir, candidate.file)
		if guard := writeRegular(path, []byte(candidate.content), original.mode); guard != nil {
			return guard.WithCommand(commandText)
		}
		outcome, problem := ctx.RunInDirWithin(checkout.Dir,
			suites[candidate.group].command, suites[candidate.group].argv, runBudget())
		ran++
		if problem == nil {
			sandboxed = outcome.Sandboxed
		}
		// The tree is restored before the outcome is read: a checkout left
		// mutated would poison every later mutant.
		if guard := writeRegular(path, original.content, original.mode); guard != nil {
			return guard.WithCommand(commandText)
		}
		if problem != nil {
			if problem.Class == engine.ClassLimit {
				limits = append(limits, fmt.Sprintf(
					"the mutation run stopped after %d of %d mutants: %s",
					ran, created, problem.Reason))
				break
			}
			return *problem
		}
		switch {
		case outcome.Code == 0:
			survived++
			// Not capped: there are at most MutationMaxMutants survivors, and a
			// tolerated survivor still has to be nameable by the reader.
			findings = append(findings, fmt.Sprintf("%s:%d %s",
				engine.DisplayName(candidate.file), candidate.line, candidate.operator))
		case scope.groups[candidate.group].rules.showsBuildFailure(outcome.Head + "\n" + outcome.RawOutput):
			// A mutant that does not build says nothing about the tests, and
			// counting it as killed would confirm a claim nobody proved. The
			// head is searched as well: a compiler prints first, a long test
			// summary pushes the diagnostics out of the tail.
			discarded++
			if len(rejected) < MaxMutationFindings {
				rejected = append(rejected, fmt.Sprintf("%s:%d %s",
					engine.DisplayName(candidate.file), candidate.line, candidate.operator))
			}
		default:
			killed++
		}
	}

	output := fmt.Sprintf("mutants_created=%d mutants_run=%d killed=%d survived=%d discarded=%d%s",
		created, ran, killed, survived, discarded, scope.note())
	if len(rejected) > 0 {
		// A high discard rate is a fact about the operator set, so the reader
		// needs to see which mutants failed to build.
		output += "\ndiscarded as build errors: " + strings.Join(rejected, ", ")
	}
	survivors := ""
	if len(findings) > 0 {
		// A tolerated survivor is still a finding a reader has to see and
		// justify: the count alone would hide it.
		survivors = "\nsurvivors: " + strings.Join(findings, ", ")
	}
	finish := func(verdict engine.Verdict) engine.Verdict {
		if ran > 0 {
			verdict = verdict.WithSandboxed(sandboxed)
		}
		return verdict
	}
	if survived > allowance {
		return finish(engine.Refuted(fmt.Sprintf(
			"%d of %d mutants survived, the claim tolerates %d: a changed line the suite does not "+
				"depend on (created %d, run %d; discarded as build errors: %d). "+
				"This measures %s",
			survived, ran, allowance, created, ran, discarded, mutationCaveat)).
			WithCommand(commandText).
			WithOutput(output + survivors))
	}
	if len(limits) > 0 {
		return finish(engine.Unverifiable("the mutation budget bounds the claim: " +
			strings.Join(limits, "; ") + "; the claim is not proven").
			WithCommand(commandText).
			WithOutput(output + survivors).
			WithClass(engine.ClassLimit))
	}
	if killed == 0 && survived == 0 {
		// Every mutant failed to build: the runs measured nothing, and the
		// declared tolerance must not turn that silence into a confirmation.
		return finish(engine.Unverifiable(fmt.Sprintf(
			"no mutant was killed or survived: all %d runs failed to build; "+
				"the claim is not proven", ran)).
			WithCommand(commandText).
			WithOutput(output + survivors).
			WithClass(engine.ClassEnvironment))
	}
	return finish(engine.Confirmed(fmt.Sprintf(
		"%d of %d mutants of the changed lines were killed by %s; %d survived within the "+
			"declared allowance of %d and %d were discarded as build errors. "+
			"This measures %s",
		killed, ran, commandText, survived, allowance, discarded, mutationCaveat)).
		WithCommand(commandText).
		WithOutput(output + survivors))
}

// mutationCaveat is what a mutation verdict does and does not say. Without it a
// confirmation reads like a statement about requirements, which it never is.
const mutationCaveat = "the sensitivity of the changed lines to changes, not the correctness " +
	"of what they are supposed to do"

// claimClock is the clock a claim measures its own budget with: the context's
// injectable one where it has one, so a budget test does not have to sleep.
func claimClock(ctx *engine.Ctx) func() time.Time {
	if ctx.Now != nil {
		return ctx.Now
	}
	return time.Now
}

// preparedSuite is one language group's suite, validated once.
type preparedSuite struct {
	command string
	argv    []string
	strict  bool
}

func prepareSuites(scope changedScope, ctx *engine.Ctx) ([]preparedSuite, *engine.Verdict) {
	suites := make([]preparedSuite, 0, len(scope.groups))
	for _, group := range scope.groups {
		command := group.rules.language.TestCommand
		if command == "" {
			verdict := engine.Unverifiable(fmt.Sprintf(
				"the language profile for %q names no test command; the mutants cannot be tested",
				group.rules.language.Name)).
				WithClass(engine.ClassEnvironment)
			return nil, &verdict
		}
		if len(group.rules.testFile) == 0 {
			// A language without a test-file pattern treats every file as a
			// production line, test files included. A mutant inside a test would
			// then count as a kill, which is the one error a quality claim must
			// never make.
			verdict := engine.Unverifiable(fmt.Sprintf(
				"the language profile for %q names no test-file pattern; "+
					"a mutant inside a test file would be counted as killed",
				group.rules.language.Name)).
				WithCommand(command).
				WithClass(engine.ClassDefect)
			return nil, &verdict
		}
		if len(group.rules.buildErrors) == 0 {
			// Without this discrimination a build failure and a failing test
			// look the same, and counting the first as a kill would confirm a
			// claim nobody proved.
			verdict := engine.Unverifiable(fmt.Sprintf(
				"the language profile for %q cannot recognise a build failure; "+
					"a mutant that does not build would be counted as killed",
				group.rules.language.Name)).
				WithCommand(command).
				WithClass(engine.ClassEnvironment)
			return nil, &verdict
		}
		argv, strict, problem := engine.PrepareCommand(command, ctx.Allowlist)
		if problem != nil {
			return nil, problem
		}
		suites = append(suites, preparedSuite{command: command, argv: argv, strict: strict})
	}
	return suites, nil
}

func suiteText(suites []preparedSuite) string {
	seen := make(map[string]bool, len(suites))
	var commands []string
	for _, suite := range suites {
		if seen[suite.command] {
			continue
		}
		seen[suite.command] = true
		commands = append(commands, suite.command)
	}
	return strings.Join(commands, ", ")
}

// originalFile is the content a mutant is written over and restored from.
type originalFile struct {
	content []byte
	mode    os.FileMode
}

// readRegularFile reads a file the claim reads and rewrites.
//
// The handle pattern is the artifact checker's: a committed symlink must not turn
// into a device read, and a FIFO must not block the daemon that holds the single
// verification slot.
func readRegularFile(path string) ([]byte, error) {
	handle, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = handle.Close() }()
	info, err := handle.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("not a regular file")
	}
	content, err := io.ReadAll(io.LimitReader(handle, MutationMaxSourceBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(content)) > MutationMaxSourceBytes {
		return nil, fmt.Errorf("larger than %d bytes", MutationMaxSourceBytes)
	}
	return content, nil
}

// writeRegular writes a file only while the path is still a regular file.
//
// The suite runs between two writes and it is repository-owned: it must not be
// able to turn the mutated path into a symlink the verifier would write through.
func writeRegular(path string, content []byte, mode os.FileMode) *engine.Verdict {
	info, err := os.Lstat(path)
	if err != nil {
		verdict := engine.Unverifiable("cannot stat the mutated file: " + err.Error()).
			WithClass(engine.ClassEnvironment)
		return &verdict
	}
	if !info.Mode().IsRegular() {
		verdict := engine.Unverifiable(fmt.Sprintf(
			"the path %q is no longer a regular file; the verifier will not write through it",
			engine.DisplayName(filepath.Base(path)))).
			WithClass(engine.ClassEnvironment)
		return &verdict
	}
	if err := os.WriteFile(path, content, mode); err != nil {
		verdict := engine.Unverifiable("cannot write the file: " + err.Error()).
			WithClass(engine.ClassEnvironment)
		return &verdict
	}
	return nil
}

func originalOf(dir, name string, cache map[string]originalFile) (originalFile, *engine.Verdict) {
	if cached, ok := cache[name]; ok {
		return cached, nil
	}
	path := filepath.Join(dir, name)
	// Lstat, not Stat: a committed symlink would be followed, and the mutant
	// would be written into whatever it points at, outside the throwaway
	// checkout.
	info, err := os.Lstat(path)
	if err != nil || info.IsDir() {
		verdict := engine.Unverifiable(fmt.Sprintf(
			"the changed file is not in the checkout: %s", engine.DisplayName(name))).
			WithClass(engine.ClassEnvironment)
		return originalFile{}, &verdict
	}
	if !info.Mode().IsRegular() {
		verdict := engine.Unverifiable(fmt.Sprintf(
			"the changed file %s is no longer a regular file (symlink, device or socket); "+
				"the verifier will not rewrite it", engine.DisplayName(name))).
			WithClass(engine.ClassEnvironment)
		return originalFile{}, &verdict
	}
	if info.Size() > MutationMaxSourceBytes {
		verdict := engine.Unverifiable(fmt.Sprintf(
			"%s is %d bytes, beyond the limit of %d bytes (built-in limit)",
			engine.DisplayName(name), info.Size(), MutationMaxSourceBytes)).
			WithClass(engine.ClassLimit)
		return originalFile{}, &verdict
	}
	content, err := readRegularFile(path)
	if err != nil {
		verdict := engine.Unverifiable(fmt.Sprintf(
			"cannot read %s from the checkout: %v", engine.DisplayName(name), err)).
			WithClass(engine.ClassEnvironment)
		return originalFile{}, &verdict
	}
	original := originalFile{content: content, mode: info.Mode().Perm()}
	cache[name] = original
	return original, nil
}

// mutantCandidates builds the mutants of a scope and caps them at the budget.
//
// The order is deterministic and broad: every changed line contributes its first
// mutant before any line contributes a second, so a small budget still touches
// every hunk instead of covering one function deeply. Only the mutants inside the
// budget are materialised: a large diff would otherwise hold one copy of its file
// per changed line. The returned count is the uncapped number, because a
// truncated run proves less than the claim asserts.
//
// The originals are read once, before any suite run, and are what the loop
// restores after each mutant: a suite that reformats the tree must not move the
// base the mutants were built from.
func mutantCandidates(dir string, scope changedScope) ([]mutant, map[string]originalFile, int, error) {
	type pendingLine struct {
		file    string
		line    int
		lines   []string
		mutants []lineMutant
		group   int
	}
	var pending []pendingLine
	originals := make(map[string]originalFile, scope.fileCount())
	for groupIndex, group := range scope.groups {
		for _, file := range group.files {
			original, verdict := originalOf(dir, file.name, originals)
			if verdict != nil {
				return nil, nil, 0, fmt.Errorf("%s", verdict.Reason)
			}
			text := strings.Split(string(original.content), "\n")
			prose := blockLiteralState(text)
			for _, number := range file.lines {
				if number < 1 || number > len(text) || prose[number-1] {
					continue
				}
				candidates := lineMutants(text[number-1])
				if len(candidates) == 0 {
					continue
				}
				pending = append(pending, pendingLine{
					file: file.name, line: number, lines: text, mutants: candidates, group: groupIndex,
				})
			}
		}
	}

	width := 0
	for _, line := range pending {
		if len(line.mutants) > width {
			width = len(line.mutants)
		}
	}
	type slot struct{ line, mutant int }
	var order []slot
	for index := 0; index < width; index++ {
		for lineIndex, line := range pending {
			if index < len(line.mutants) {
				order = append(order, slot{line: lineIndex, mutant: index})
			}
		}
	}
	created := len(order)
	if len(order) > MutationMaxMutants {
		order = order[:MutationMaxMutants]
	}

	mutants := make([]mutant, 0, len(order))
	for _, item := range order {
		line := pending[item.line]
		mutation := line.mutants[item.mutant]
		content := append([]string(nil), line.lines...)
		content[line.line-1] = mutation.Line
		mutants = append(mutants, mutant{
			file:     line.file,
			line:     line.line,
			operator: mutation.Operator,
			content:  strings.Join(content, "\n"),
			group:    line.group,
		})
	}
	return mutants, originals, created, nil
}

// blockLiteralState marks the lines that begin inside a block literal.
//
// A text mutator cannot parse a language, but a line inside a raw string, a
// docstring or a heredoc is prose. A mutant there can only survive, and a survivor
// is a finding against the report, so those lines are outside the claim.
//
// The state is tracked by delimiter parity and without a language: a delimiter
// opened an odd number of times leaves the line open. Single-line literals are
// never carried over, because they cannot span lines in the languages a profile
// describes.
func blockLiteralState(lines []string) []bool {
	open := ""
	prose := make([]bool, len(lines))
	for index, line := range lines {
		prose[index] = open != ""
		if open == "" {
			open = openingBlockDelimiter(line)
			continue
		}
		if strings.Count(line, open)%2 == 1 {
			open = ""
		}
	}
	return prose
}

// openingBlockDelimiter returns the delimiter a line opens and does not close.
func openingBlockDelimiter(line string) string {
	for _, delimiter := range []string{"`", `"""`, "'''"} {
		if strings.Count(line, delimiter)%2 == 1 {
			return delimiter
		}
	}
	return ""
}
