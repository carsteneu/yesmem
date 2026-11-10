package claimtypes

import (
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"

	"github.com/carsteneu/yesmem/internal/verify/engine"
)

// The coverage claim: every changed production line was executed by the suite.
//
// Mutation answers the stronger question (does a test depend on the line), this
// one answers the cheaper one (did the line run at all). Both are worth having:
// coverage catches the untouched function, mutation catches the assertion that
// never fires.
const (
	// CoverageMaxArtifactBytes bounds the coverage artifact the verifier reads.
	CoverageMaxArtifactBytes = 64 << 20
	// MaxCoverageFindings is how many uncovered lines a verdict names. The count
	// is exact, the list is bounded.
	MaxCoverageFindings = 10
	// MaxCoverageBlockLines bounds the line span of one artifact block. The
	// artifact comes from the checkout, so a block claiming a billion lines would
	// expand into an unbounded map.
	MaxCoverageBlockLines = 1 << 17
	// MaxCoverageLineEntries bounds the instrumented lines one artifact may name.
	MaxCoverageLineEntries = 1 << 21
	// coverageScopeToken is the only scope a [COVERAGE] claim may name.
	coverageScopeToken = "changed-lines"
)

// coverageIndex is the artifact's answer: how often each instrumented line ran.
//
// A line the artifact does not list was not instrumented (a comment, a blank, a
// declaration). That is a third state, not a zero: without it a comment on a
// changed line would read as an uncovered line, and the claim would refute an
// honest report.
type coverageIndex map[string]map[int]int

// hitsFor resolves a repository-relative path against the artifact.
//
// Tools write their own path form: Go's profile carries the module path in front
// of the file. A suffix match on a path component boundary recovers the file, and
// an ambiguous match stays unresolved, because crediting the wrong file would
// confirm a line nobody measured.
func (index coverageIndex) hitsFor(file string) (map[int]int, bool) {
	if hits, ok := index[file]; ok {
		return hits, true
	}
	var found map[int]int
	matches := 0
	for key, hits := range index {
		if strings.HasSuffix(key, "/"+file) {
			matches++
			found = hits
		}
	}
	if matches == 1 {
		return found, true
	}
	return nil, false
}

var coverageMarkerRe = regexp.MustCompile(`\[COVERAGE:(?P<body>[^\[\]]*?)\]`)

// MatchCoverageDelta parses the [COVERAGE] markers of one line.
//
// The marker asserts that no changed production line is left unexecuted. The body
// names the scope and travels to the checker as written, so an unknown scope is a
// defect rather than a silently ignored marker.
func MatchCoverageDelta(line string) []engine.Claim {
	var claims []engine.Claim
	for _, match := range coverageMarkerRe.FindAllStringSubmatch(line, -1) {
		body := strings.Join(strings.Fields(strings.ToLower(match[1])), " ")
		claims = append(claims, engine.Claim{Kind: "coverage_delta", Token: body})
	}
	return claims
}

// CheckCoverageDelta re-derives a [COVERAGE] claim.
//
// The diff supplies the lines, the profile supplies the command that measures
// them, and the artifact supplies the answer. Every failure to measure is an
// environment gap: a claim that could not be checked must never read as covered.
func CheckCoverageDelta(claim engine.Claim, ctx *engine.Ctx) engine.Verdict {
	if guard := engine.RepoGuard(ctx); guard != nil {
		return *guard
	}
	if claim.Token != coverageScopeToken {
		return engine.Unverifiable(fmt.Sprintf(
			"unknown coverage scope %q; this verifier can only judge %q",
			claim.Token, coverageScopeToken)).
			WithClass(engine.ClassDefect)
	}
	commit, verdict := pinnedCommit(claim, ctx, "the coverage run")
	if verdict != nil {
		return *verdict
	}
	base, verdict := baseRevision(ctx, "the coverage scope cannot be compared")
	if verdict != nil {
		return *verdict
	}
	scope, verdict := collectChangedScope(ctx, base, commit)
	if verdict != nil {
		return *verdict
	}
	if len(scope.groups) == 0 {
		return engine.Unverifiable(
			"no changed production line in scope; there is nothing to measure" + scope.note()).
			WithClass(engine.ClassDefect)
	}

	suites := make([]preparedSuite, 0, len(scope.groups))
	for _, group := range scope.groups {
		language := group.rules.language
		if language.CoverageCommand == "" || language.CoverageFormat == "" || language.CoverageArtifact == "" {
			return engine.Unverifiable(fmt.Sprintf(
				"the language profile for %q supplies no coverage command, format and artifact; "+
					"the changed lines of that language cannot be measured", language.Name)).
				WithClass(engine.ClassEnvironment)
		}
		if _, known := coverageFormats[language.CoverageFormat]; !known {
			// The format is profile data: an unknown one is caught before any
			// run, with the reason a reader needs.
			return engine.Unverifiable(fmt.Sprintf(
				"the language profile for %q names an unknown coverage format %q",
				language.Name, language.CoverageFormat)).
				WithCommand(language.CoverageCommand).
				WithClass(engine.ClassEnvironment)
		}
		argv, strict, problem := engine.PrepareProfileCommand(language.CoverageCommand, ctx.Allowlist, ctx.ProfileCoverageCommands)
		if problem != nil {
			return *problem
		}
		suites = append(suites, preparedSuite{command: language.CoverageCommand, argv: argv, strict: strict})
	}

	checkout, failure := ctx.NewCheckout("coverage", commit)
	if failure != nil {
		return *failure
	}
	defer checkout.Close()

	for _, suite := range suites {
		// The same containment the test gate enforces: an argument that resolves
		// outside the checkout would measure something the commit does not hold.
		if escape := engine.SymlinkEscape(suite.argv, checkout.Dir, suite.strict); escape != "" {
			return engine.Unverifiable(fmt.Sprintf(
				"the coverage command argument resolves outside the checkout: %q", escape)).
				WithCommand(suite.command).
				WithClass(engine.ClassDefect)
		}
	}

	commandText := suiteText(suites)
	changedLines := 0
	uncoveredCount := 0
	notInstrumented := 0
	var uncovered []string
	sandboxed := false
	ran := 0
	for index, group := range scope.groups {
		language := group.rules.language
		// A stale artifact would be read as this run's answer: a second group
		// writing the same path, or a previous mutant run, would decide the
		// verdict. The removal is confined to the checkout like the read.
		root, rootErr := filepath.EvalSymlinks(checkout.Dir)
		if rootErr != nil {
			root = checkout.Dir
		}
		if stale, ok := resolvedUnderRoot(root, language.CoverageArtifact); ok {
			_ = os.Remove(stale)
		}
		outcome, problem := ctx.RunInDir(checkout.Dir, suites[index].command, suites[index].argv)
		ran++
		if problem != nil {
			return *problem
		}
		sandboxed = outcome.Sandboxed
		if outcome.Code != 0 {
			// A red suite leaves a partial profile: the missing lines were not
			// measured, so a refutation would judge the report for the runner's
			// failure.
			return engine.Unverifiable(fmt.Sprintf(
				"the coverage command exited %d; a partial profile of a run that did not "+
					"complete proves nothing about the changed lines", outcome.Code)).
				WithCommand(outcome.CommandDesc).
				WithOutput(outcome.Output).
				WithSandboxed(outcome.Sandboxed).
				WithClass(engine.ClassEnvironment)
		}
		content, err := readCoverageArtifact(checkout.Dir, language.CoverageArtifact)
		if err != nil {
			return engine.Unverifiable("cannot read the coverage artifact: " + err.Error()).
				WithCommand(outcome.CommandDesc).
				WithClass(engine.ClassEnvironment)
		}
		covered, err := parseCoverage(language.CoverageFormat, content)
		if err != nil {
			return engine.Unverifiable("cannot parse the coverage artifact: " + err.Error()).
				WithCommand(outcome.CommandDesc).
				WithClass(engine.ClassEnvironment)
		}
		for _, file := range group.files {
			hits, measured := covered.hitsFor(file.name)
			if !measured {
				// The file is not in the artifact at all. That is not a line
				// level statement but a missing measurement: a claim about the
				// file would be a guess, and guessing is what this claim refuses.
				return engine.Unverifiable(fmt.Sprintf(
					"the coverage artifact does not measure %s; "+
						"the claim cannot be decided for its changed lines",
					engine.DisplayName(file.name))).
					WithCommand(commandText).
					WithClass(engine.ClassEnvironment)
			}
			for _, line := range file.lines {
				count, instrumented := hits[line]
				if !instrumented {
					// A comment or a declaration cannot run: it is outside the
					// claim, not a coverage gap.
					notInstrumented++
					continue
				}
				changedLines++
				if count > 0 {
					continue
				}
				uncoveredCount++
				if len(uncovered) < MaxCoverageFindings {
					uncovered = append(uncovered, fmt.Sprintf("%s:%d",
						engine.DisplayName(file.name), line))
				}
			}
		}
	}
	if changedLines == 0 {
		// Every changed line is uninstrumented, so the claim speaks about lines
		// that cannot run. That is the report's form, not the machine's.
		return engine.Unverifiable(fmt.Sprintf(
			"none of the changed production lines is instrumented by the coverage artifact; "+
				"the claim is not checkable for this diff (%d line(s) not instrumented)",
			notInstrumented)).
			WithClass(engine.ClassDefect)
	}

	output := fmt.Sprintf("changed_lines=%d uncovered=%d not_instrumented=%d command=%s%s",
		changedLines, uncoveredCount, notInstrumented, commandText, scope.note())
	finish := func(verdict engine.Verdict) engine.Verdict {
		if ran > 0 {
			verdict = verdict.WithSandboxed(sandboxed)
		}
		return verdict
	}
	if uncoveredCount > 0 {
		return finish(engine.Refuted(fmt.Sprintf(
			"%d of %d instrumented changed production lines were not executed by %s",
			uncoveredCount, changedLines, commandText)).
			WithCommand(commandText).
			WithOutput(output + "\nuncovered: " + strings.Join(uncovered, ", ")))
	}
	return finish(engine.Confirmed(fmt.Sprintf(
		"all %d instrumented changed production lines were executed by %s",
		changedLines, commandText)).
		WithCommand(commandText).
		WithOutput(output))
}

// readCoverageArtifact reads the artifact a coverage command wrote, confined to
// the checkout.
//
// The handle pattern is the artifact checker's: the coverage command runs before
// this and it is repository-owned, so the path it wrote must not become a device
// read (unbounded) or a FIFO (which would block the daemon that holds the single
// verification slot).
func readCoverageArtifact(checkout, artifact string) ([]byte, error) {
	if artifact == "" {
		return nil, fmt.Errorf("the profile names no artifact")
	}
	root, err := filepath.EvalSymlinks(checkout)
	if err != nil {
		root = checkout
	}
	resolved, ok := resolvedUnderRoot(root, artifact)
	if !ok {
		return nil, fmt.Errorf("the artifact path escapes the checkout: %q", artifact)
	}
	handle, err := os.OpenFile(resolved, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = handle.Close() }()
	info, err := handle.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("not a regular file: %q", artifact)
	}
	if info.Size() > CoverageMaxArtifactBytes {
		return nil, fmt.Errorf("the artifact is larger than %d bytes", CoverageMaxArtifactBytes)
	}
	content, err := io.ReadAll(io.LimitReader(handle, CoverageMaxArtifactBytes+1))
	if err != nil {
		return nil, err
	}
	if len(content) > CoverageMaxArtifactBytes {
		return nil, fmt.Errorf("the artifact is larger than %d bytes", CoverageMaxArtifactBytes)
	}
	return content, nil
}

// coverageFormats are the artifact shapes the verifier can read. The profile names
// one, and an unknown name is reported before the first run.
var coverageFormats = map[string]bool{"go-cover": true, "cobertura": true}

// parseCoverage turns a coverage artifact into the runs per instrumented line.
//
// The format is data: a language the verifier does not know still works as long
// as its tool writes one of these shapes.
func parseCoverage(format string, content []byte) (coverageIndex, error) {
	switch format {
	case "go-cover":
		return parseGoCover(content)
	case "cobertura":
		return parseCobertura(content)
	}
	return nil, fmt.Errorf("unknown coverage format %q", format)
}

// goCoverBlock is one line of a Go coverage profile:
// "path/file.go:3.12,5.2 2 1" is a block from line 3 to line 5, hit once.
var goCoverBlock = regexp.MustCompile(`^(.+):(\d+)\.\d+,(\d+)\.\d+ (\d+) (\d+)$`)

func parseGoCover(content []byte) (coverageIndex, error) {
	covered := make(coverageIndex)
	blocks, entries := 0, 0
	for _, raw := range strings.Split(string(content), "\n") {
		line := strings.TrimSpace(strings.TrimSuffix(raw, "\r"))
		if line == "" || strings.HasPrefix(line, "mode:") {
			continue
		}
		match := goCoverBlock.FindStringSubmatch(line)
		if match == nil {
			continue
		}
		blocks++
		start, err := strconv.Atoi(match[2])
		if err != nil {
			continue
		}
		end, err := strconv.Atoi(match[3])
		if err != nil {
			continue
		}
		count, err := strconv.Atoi(match[5])
		if err != nil {
			continue
		}
		if end < start || end-start > MaxCoverageBlockLines {
			return nil, fmt.Errorf("implausible block at line %d: %d..%d", blocks, start, end)
		}
		name := match[1]
		if covered[name] == nil {
			covered[name] = make(map[int]int)
		}
		for number := start; number <= end; number++ {
			entries++
			if entries > MaxCoverageLineEntries {
				return nil, fmt.Errorf("more than %d line entries", MaxCoverageLineEntries)
			}
			// Overlapping blocks: the highest count is what ran.
			if existing, ok := covered[name][number]; !ok || count > existing {
				covered[name][number] = count
			}
		}
	}
	if blocks == 0 {
		// An artifact without a single block would leave every line
		// uninstrumented and read as a clean result by omission: it is an
		// error, not an empty answer.
		return nil, fmt.Errorf("no coverage block found")
	}
	return covered, nil
}

// coberturaReport is the part of the Cobertura schema a line claim needs.
type coberturaReport struct {
	Classes []struct {
		Filename string `xml:"filename,attr"`
		Lines    []struct {
			Number int `xml:"number,attr"`
			Hits   int `xml:"hits,attr"`
		} `xml:"lines>line"`
	} `xml:"packages>package>classes>class"`
}

func parseCobertura(content []byte) (coverageIndex, error) {
	var report coberturaReport
	if err := xml.Unmarshal(content, &report); err != nil {
		return nil, fmt.Errorf("not a Cobertura report: %w", err)
	}
	covered := make(coverageIndex)
	lines := 0
	for _, class := range report.Classes {
		if class.Filename == "" {
			continue
		}
		if covered[class.Filename] == nil {
			covered[class.Filename] = make(map[int]int)
		}
		for _, line := range class.Lines {
			lines++
			if existing, ok := covered[class.Filename][line.Number]; !ok || line.Hits > existing {
				covered[class.Filename][line.Number] = line.Hits
			}
		}
	}
	if lines == 0 {
		return nil, fmt.Errorf("no line entry found")
	}
	if lines > MaxCoverageLineEntries {
		return nil, fmt.Errorf("more than %d line entries", MaxCoverageLineEntries)
	}
	return covered, nil
}
