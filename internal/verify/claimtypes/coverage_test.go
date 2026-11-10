package claimtypes

import (
	"strconv"
	"strings"
	"testing"

	"github.com/carsteneu/yesmem/internal/verify/engine"
)

// toyCoverage is the toy language with a coverage command and a go-cover shaped
// artifact: the format is the profile's, the claim parses it without knowing a
// language.
var toyCoverage = engine.Language{
	Name:               "toy",
	Extensions:         []string{".toy"},
	TestFilePatterns:   []string{`_test\.toy$`},
	TestCommand:        "./run.sh",
	CoverageCommand:    "./cov.sh",
	CoverageFormat:     "go-cover",
	CoverageArtifact:   ".cover.out",
	BuildErrorPatterns: []string{`COMPILE ERROR`},
}

// A go-cover profile names a block per line range; a block with a zero count is
// instrumented and did not run, and a covered block covers every line it spans.
func TestParseCoverageGoCover(t *testing.T) {
	profile := strings.Join([]string{
		"mode: set",
		"impl.toy:1.1,1.5 1 1",
		"impl.toy:3.1,5.2 2 0",
		"impl.toy:7.1,7.9 1 4",
		"dir/other.toy:2.1,2.3 1 1",
	}, "\n")

	covered, err := parseCoverage("go-cover", []byte(profile))
	if err != nil {
		t.Fatalf("parseCoverage: %v", err)
	}
	if got := covered["impl.toy"][1]; got != 1 {
		t.Errorf("impl.toy:1 hits = %d, want 1", got)
	}
	for _, line := range []int{3, 4, 5} {
		if hits, instrumented := covered["impl.toy"][line]; !instrumented || hits != 0 {
			t.Errorf("impl.toy:%d = (%d, %v), want (0, instrumented)", line, hits, instrumented)
		}
	}
	if got := covered["impl.toy"][7]; got != 4 {
		t.Errorf("impl.toy:7 hits = %d, want 4", got)
	}
	// Line 2 and line 6 lie in no block: a comment or a blank cannot run, so the
	// artifact says nothing about them.
	for _, line := range []int{2, 6} {
		if _, instrumented := covered["impl.toy"][line]; instrumented {
			t.Errorf("impl.toy:%d = instrumented, want not instrumented", line)
		}
	}
	if got := covered["dir/other.toy"][2]; got != 1 {
		t.Errorf("dir/other.toy:2 hits = %d, want 1", got)
	}
}

// A profile that parses to nothing would silently confirm every line: an artifact
// with no recognised line is an error.
func TestParseCoverageGoCoverRejectsGarbage(t *testing.T) {
	if _, err := parseCoverage("go-cover", []byte("total: (statements) 40.1%\n")); err == nil {
		t.Fatal("a profile without a single line entry parsed as complete")
	}
	if _, err := parseCoverage("go-cover", nil); err == nil {
		t.Fatal("an empty profile parsed as complete")
	}
}

// The tools write their own path form: Go puts the module path in front of the
// file. Without the suffix resolution every changed line would read as uncovered,
// which is a false refutation of an honest report.
func TestCoverageIndexResolvesAModulePrefix(t *testing.T) {
	index := coverageIndex{
		"github.com/example/mod/internal/guard/rules.go": {38: 1},
		"internal/guard/other.go":                        {4: 0},
	}
	if hits, ok := index.hitsFor("internal/guard/rules.go"); !ok || hits[38] != 1 {
		t.Fatalf("module prefix not resolved: (%v, %v)", hits, ok)
	}
	if _, ok := index.hitsFor("internal/guard/missing.go"); ok {
		t.Error("a file the artifact does not carry resolved anyway")
	}
}

// An ambiguous suffix names two files: crediting either would confirm a line
// nobody measured.
func TestCoverageIndexRefusesAnAmbiguousSuffix(t *testing.T) {
	index := coverageIndex{
		"a/internal/guard/rules.go": {38: 1},
		"b/internal/guard/rules.go": {38: 1},
	}
	if _, ok := index.hitsFor("internal/guard/rules.go"); ok {
		t.Error("an ambiguous suffix was resolved")
	}
}

// Cobertura carries a hits counter per line, which is the only thing a
// line-coverage claim needs.
func TestParseCoverageCobertura(t *testing.T) {
	report := `<?xml version="1.0"?>
<coverage line-rate="0.5">
  <packages>
    <package name="app">
      <classes>
        <class name="Impl" filename="src/Impl.php">
          <lines>
            <line number="3" hits="1"/>
            <line number="4" hits="0"/>
          </lines>
        </class>
      </classes>
    </package>
  </packages>
</coverage>`

	covered, err := parseCoverage("cobertura", []byte(report))
	if err != nil {
		t.Fatalf("parseCoverage: %v", err)
	}
	if got := covered["src/Impl.php"][3]; got != 1 {
		t.Errorf("src/Impl.php:3 hits = %d, want 1", got)
	}
	if hits, instrumented := covered["src/Impl.php"][4]; !instrumented || hits != 0 {
		t.Errorf("src/Impl.php:4 = (%d, %v), want (0, instrumented)", hits, instrumented)
	}
	if _, instrumented := covered["src/Impl.php"][5]; instrumented {
		t.Error("src/Impl.php:5 = instrumented, want not instrumented")
	}
}

func TestParseCoverageUnknownFormat(t *testing.T) {
	if _, err := parseCoverage("lcov", []byte("SF:x\nDA:1,1\n")); err == nil {
		t.Fatal("an unknown format parsed as complete")
	}
}

// coverageCtx is a context for the covering toy language.
func coverageCtx(t *testing.T, repo, base string, languages ...engine.Language) *engine.Ctx {
	t.Helper()
	ctx := unsandboxedCtx(t, repo)
	ctx.Base = base
	ctx.Allowlist = []string{"./run.sh", "./cov.sh"}
	if len(languages) == 0 {
		languages = []engine.Language{toyCoverage}
	}
	ctx.Languages = languages
	return ctx
}

func coverageClaim(commit string) engine.Claim {
	return engine.Claim{Kind: "coverage_delta", Token: coverageScopeToken, Commit: commit}
}

// All changed lines covered is the only thing the claim asserts, so it is the
// only thing that confirms it.
func TestCheckCoverageDeltaConfirmsCoveredLines(t *testing.T) {
	repo, base, head := makeMutationRepo(t, "coveragecovered",
		map[string]string{
			"impl.toy": "limit=2\n",
			"cov.sh":   "#!/bin/sh\nprintf 'impl.toy:1.1,1.5 1 1\\n' > .cover.out\n",
		},
		map[string]string{"impl.toy": "limit=1\n"},
	)

	verdict := CheckCoverageDelta(coverageClaim(head), coverageCtx(t, repo, base))

	if verdict.Status != engine.StatusConfirmed {
		t.Fatalf("status = %s (%s)", verdict.Status, verdict.Reason)
	}
	if !strings.Contains(verdict.Output, "changed_lines=1") {
		t.Errorf("output = %q, want the changed line count", verdict.Output)
	}
}

// An uncovered changed line is the finding, with its file and line.
func TestCheckCoverageDeltaRefutesUncoveredLine(t *testing.T) {
	repo, base, head := makeMutationRepo(t, "coverageuncovered",
		map[string]string{
			"impl.toy": "limit=2\n",
			"cov.sh":   "#!/bin/sh\nprintf 'impl.toy:1.1,1.5 1 0\\n' > .cover.out\n",
		},
		map[string]string{"impl.toy": "limit=1\n"},
	)

	verdict := CheckCoverageDelta(coverageClaim(head), coverageCtx(t, repo, base))

	if verdict.Status != engine.StatusRefuted {
		t.Fatalf("status = %s (%s), want refuted", verdict.Status, verdict.Reason)
	}
	if !strings.Contains(verdict.Output, "impl.toy:1") {
		t.Errorf("output = %q, want the uncovered file:line", verdict.Output)
	}
}

// A changed comment line cannot run: the artifact says nothing about it, and
// treating that silence as a gap would refute an honest report.
func TestCheckCoverageDeltaIgnoresUninstrumentedLines(t *testing.T) {
	repo, base, head := makeMutationRepo(t, "coveragecomment",
		map[string]string{
			"impl.toy": "# a comment\nlimit=2\n",
			"cov.sh": "#!/bin/sh\n" +
				"printf 'impl.toy:2.1,2.5 1 1\\n' > .cover.out\n",
		},
		// Line 1 is the comment, line 2 is the instrumented statement.
		map[string]string{"impl.toy": "# a different comment\nlimit=1\n"},
	)

	verdict := CheckCoverageDelta(coverageClaim(head), coverageCtx(t, repo, base))

	if verdict.Status != engine.StatusConfirmed {
		t.Fatalf("status = %s (%s), want confirmed: a comment is no coverage gap",
			verdict.Status, verdict.Reason)
	}
	if !strings.Contains(verdict.Output, "changed_lines=1") ||
		!strings.Contains(verdict.Output, "not_instrumented=1") {
		t.Errorf("output = %q, want one instrumented and one uninstrumented line", verdict.Output)
	}
}

// A file the artifact does not mention is not measured: the claim reports the gap
// instead of refuting an honest report.
func TestCheckCoverageDeltaUnmeasuredFileIsUnverifiable(t *testing.T) {
	repo, base, head := makeMutationRepo(t, "coveragemeasured",
		map[string]string{
			"impl.toy": "limit=2\n",
			"cov.sh":   "#!/bin/sh\nprintf 'elsewhere.toy:1.1,1.5 1 1\\n' > .cover.out\n",
		},
		map[string]string{"impl.toy": "limit=1\n"},
	)

	verdict := CheckCoverageDelta(coverageClaim(head), coverageCtx(t, repo, base))

	if verdict.Status != engine.StatusUnverifiable || verdict.Class != engine.ClassEnvironment {
		t.Fatalf("status = %s class = %q (%s), want an environment gap",
			verdict.Status, verdict.Class, verdict.Reason)
	}
	if !strings.Contains(verdict.Reason, "does not measure") {
		t.Errorf("reason = %q, want it to name the missing measurement", verdict.Reason)
	}
}

// One measured file must not carry a diff whose other file the artifact never
// saw: the unmeasured file would vanish as "not instrumented" and the claim would
// confirm a file nobody measured.
func TestCheckCoverageDeltaRefusesAPartiallyMeasuredDiff(t *testing.T) {
	repo, base, head := makeMutationRepo(t, "coveragepartial",
		map[string]string{
			"a.toy":  "limit=2\n",
			"b.toy":  "other=2\n",
			"cov.sh": "#!/bin/sh\nprintf 'a.toy:1.1,1.5 1 1\\n' > .cover.out\n",
		},
		map[string]string{"a.toy": "limit=1\n", "b.toy": "other=1\n"},
	)

	verdict := CheckCoverageDelta(coverageClaim(head), coverageCtx(t, repo, base))

	if verdict.Status != engine.StatusUnverifiable || verdict.Class != engine.ClassEnvironment {
		t.Fatalf("status = %s class = %q (%s), want an environment gap",
			verdict.Status, verdict.Class, verdict.Reason)
	}
	if !strings.Contains(verdict.Reason, "b.toy") {
		t.Errorf("reason = %q, want it to name the unmeasured file", verdict.Reason)
	}
}

// A red coverage run leaves a partial profile, which would refute an honest
// report: it is an environment gap, never a refutation.
func TestCheckCoverageDeltaRedRunIsUnverifiable(t *testing.T) {
	repo, base, head := makeMutationRepo(t, "coveragered",
		map[string]string{
			"impl.toy": "limit=2\n",
			"cov.sh":   "#!/bin/sh\nprintf 'impl.toy:1.1,1.5 1 0\\n' > .cover.out\nexit 1\n",
		},
		map[string]string{"impl.toy": "limit=1\n"},
	)

	verdict := CheckCoverageDelta(coverageClaim(head), coverageCtx(t, repo, base))

	if verdict.Status != engine.StatusUnverifiable || verdict.Class != engine.ClassEnvironment {
		t.Fatalf("status = %s class = %q (%s), want an environment gap",
			verdict.Status, verdict.Class, verdict.Reason)
	}
	if !strings.Contains(verdict.Reason, "exited") {
		t.Errorf("reason = %q", verdict.Reason)
	}
}

// A run that writes no artifact proves nothing: an unverifiable claim, never a
// confirmation.
func TestCheckCoverageDeltaMissingArtifactIsUnverifiable(t *testing.T) {
	repo, base, head := makeMutationRepo(t, "coveragemissing",
		map[string]string{
			"impl.toy": "limit=2\n",
			"cov.sh":   "#!/bin/sh\nexit 0\n",
		},
		map[string]string{"impl.toy": "limit=1\n"},
	)

	verdict := CheckCoverageDelta(coverageClaim(head), coverageCtx(t, repo, base))

	if verdict.Status != engine.StatusUnverifiable || verdict.Class != engine.ClassEnvironment {
		t.Fatalf("status = %s class = %q (%s), want an environment gap",
			verdict.Status, verdict.Class, verdict.Reason)
	}
}

// A profile without a coverage command cannot measure anything: the claim says so
// instead of confirming silently.
func TestCheckCoverageDeltaWithoutCoverageCommandIsUnverifiable(t *testing.T) {
	withoutCoverage := toyCoverage
	withoutCoverage.CoverageCommand = ""
	withoutCoverage.CoverageArtifact = ""

	repo, base, head := makeMutationRepo(t, "coveragenocommand",
		map[string]string{"impl.toy": "limit=2\n", "run.sh": "#!/bin/sh\nexit 0\n"},
		map[string]string{"impl.toy": "limit=1\n"},
	)

	verdict := CheckCoverageDelta(coverageClaim(head), coverageCtx(t, repo, base, withoutCoverage))

	if verdict.Status != engine.StatusUnverifiable || verdict.Class != engine.ClassEnvironment {
		t.Fatalf("status = %s class = %q (%s), want an environment gap",
			verdict.Status, verdict.Class, verdict.Reason)
	}
	if !strings.Contains(verdict.Reason, "coverage") {
		t.Errorf("reason = %q, want it to name the missing coverage command", verdict.Reason)
	}
}

// An unknown format is a profile defect the verifier reports, not a confirmation.
func TestCheckCoverageDeltaUnknownFormatIsUnverifiable(t *testing.T) {
	unknown := toyCoverage
	unknown.CoverageFormat = "lcov"

	repo, base, head := makeMutationRepo(t, "coverageunknownformat",
		map[string]string{
			"impl.toy": "limit=2\n",
			"cov.sh":   "#!/bin/sh\nprintf 'impl.toy:1.1,1.5 1 1\\n' > .cover.out\n",
		},
		map[string]string{"impl.toy": "limit=1\n"},
	)

	verdict := CheckCoverageDelta(coverageClaim(head), coverageCtx(t, repo, base, unknown))

	if verdict.Status != engine.StatusUnverifiable || verdict.Class != engine.ClassEnvironment {
		t.Fatalf("status = %s class = %q (%s), want an environment gap",
			verdict.Status, verdict.Class, verdict.Reason)
	}
}

// The uncovered count is exact even though the named list is bounded: a capped
// counter would understate the finding.
func TestCheckCoverageDeltaCountsEveryUncoveredLine(t *testing.T) {
	total := MaxCoverageFindings + 5
	base := map[string]string{
		"impl.toy": "value=2\n",
		"cov.sh": `#!/bin/sh
printf 'impl.toy:1.1,` + strconv.Itoa(total) + `.5 1 0\n' > .cover.out
`,
	}
	var lines []string
	for index := 0; index < total; index++ {
		lines = append(lines, "value=1")
	}
	head := map[string]string{"impl.toy": strings.Join(lines, "\n") + "\n"}
	repo, baseCommit, headCommit := makeMutationRepo(t, "coveragecount", base, head)

	verdict := CheckCoverageDelta(coverageClaim(headCommit), coverageCtx(t, repo, baseCommit))

	if verdict.Status != engine.StatusRefuted {
		t.Fatalf("status = %s (%s), want refuted", verdict.Status, verdict.Reason)
	}
	if !strings.Contains(verdict.Reason, strconv.Itoa(total)+" of "+strconv.Itoa(total)) {
		t.Errorf("reason = %q, want the exact count of %d", verdict.Reason, total)
	}
}

// An artifact the commit carries is not this run's answer: the claim removes it
// before the run, so a command that writes nothing proves nothing.
func TestCheckCoverageDeltaIgnoresACommittedArtifact(t *testing.T) {
	repo, base, head := makeMutationRepo(t, "coveragestale",
		map[string]string{
			"impl.toy":   "limit=2\n",
			"cov.sh":     "#!/bin/sh\nexit 0\n",
			".cover.out": "mode: set\nimpl.toy:1.1,1.5 1 1\n",
		},
		map[string]string{"impl.toy": "limit=1\n"},
	)

	verdict := CheckCoverageDelta(coverageClaim(head), coverageCtx(t, repo, base))

	if verdict.Status == engine.StatusConfirmed {
		t.Fatalf("a committed artifact confirmed the run that wrote nothing: %s", verdict.Reason)
	}
}

// An artifact block that claims a billion lines would expand into an unbounded
// map: the artifact is repository-controlled, so the span has a ceiling.
func TestParseCoverageGoCoverRefusesAnImplausibleBlock(t *testing.T) {
	huge := "module/x.go:1.1," + strconv.Itoa(20_000_000) + ".1 1 1\n"
	if _, err := parseCoverage("go-cover", []byte(huge)); err == nil {
		t.Fatal("an implausible block span parsed")
	}
}

// The marker reaches the checker through the registry, and an unknown scope stays
// a claim the checker rejects.
func TestMatchCoverageScopes(t *testing.T) {
	claims := MatchCoverageDelta("[COVERAGE: changed-lines] [COVERAGE: whole-repo]")
	if len(claims) != 2 {
		t.Fatalf("claims = %d, want 2", len(claims))
	}
	if claims[0].Token != coverageScopeToken || claims[1].Token != "whole-repo" {
		t.Errorf("tokens = %q, %q", claims[0].Token, claims[1].Token)
	}
}
