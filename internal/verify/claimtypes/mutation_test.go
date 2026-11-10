package claimtypes

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/carsteneu/yesmem/internal/verify/engine"
)

// toyLanguage is a language the verifier has never heard of: a text file and a
// shell script that decides whether it is "tested". The mutation claim must work
// from the profile alone, which is what makes the claim language-agnostic.
var toyLanguage = engine.Language{
	Name:               "toy",
	Extensions:         []string{".toy"},
	TestFilePatterns:   []string{`_test\.toy$`},
	TestCommand:        "./run.sh",
	BuildErrorPatterns: []string{`COMPILE ERROR`},
}

// makeMutationRepo builds a repository with a base and a head commit.
func makeMutationRepo(t *testing.T, name string, base, head map[string]string) (repo, baseCommit, headCommit string) {
	t.Helper()
	parent, err := filepath.Abs(filepath.Join("..", "..", ".yesmem", "tmp", "mutationrepos"))
	if err != nil {
		t.Fatalf("abs: %v", err)
	}
	if err := os.MkdirAll(parent, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	root, err := os.MkdirTemp(parent, name+"-")
	if err != nil {
		t.Fatalf("mkdtemp: %v", err)
	}
	git := func(args ...string) string {
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		cmd.Env = []string{
			"HOME=" + root,
			"GIT_CONFIG_NOSYSTEM=1",
			"GIT_AUTHOR_NAME=T", "GIT_AUTHOR_EMAIL=t@example.com",
			"GIT_COMMITTER_NAME=T", "GIT_COMMITTER_EMAIL=t@example.com",
			"PATH=" + os.Getenv("PATH"),
		}
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
		return strings.TrimSpace(string(output))
	}
	write := func(files map[string]string) {
		for filename, content := range files {
			path := filepath.Join(root, filename)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
			mode := os.FileMode(0o644)
			if strings.HasSuffix(filename, ".sh") {
				mode = 0o755
			}
			if err := os.WriteFile(path, []byte(content), mode); err != nil {
				t.Fatalf("write %s: %v", filename, err)
			}
		}
	}
	git("init", "-q", "-b", "main")
	write(base)
	git("add", "-A")
	git("commit", "-q", "-m", "base")
	baseCommit = git("rev-parse", "HEAD")
	write(head)
	git("add", "-A")
	git("commit", "-q", "-m", "head")
	return root, baseCommit, git("rev-parse", "HEAD")
}

// mutationCtx is a context for the toy language: unsandboxed like every other
// claim test, with the toy suite opened in the allowlist.
func mutationCtx(t *testing.T, repo, base string, languages ...engine.Language) *engine.Ctx {
	t.Helper()
	ctx := unsandboxedCtx(t, repo)
	ctx.Base = base
	ctx.Allowlist = []string{"./run.sh"}
	if len(languages) == 0 {
		languages = []engine.Language{toyLanguage}
	}
	ctx.Languages = languages
	return ctx
}

func mutationClaim(commit string) engine.Claim {
	return engine.Claim{Kind: "mutation", Token: mutationScopeToken, Commit: commit}
}

// A suite that fails on the mutant proves the changed line is tested.
func TestCheckMutationConfirmsWhenTheMutantIsKilled(t *testing.T) {
	repo, base, head := makeMutationRepo(t, "mutationkilled",
		map[string]string{
			"impl.toy":      "limit=2\nkeep=1\n",
			"impl_test.toy": "assertion=2\n",
			"run.sh":        "#!/bin/sh\n. ./impl.toy\n[ \"$limit\" = \"1\" ]\n",
		},
		map[string]string{
			"impl.toy":      "limit=1\nkeep=1\n",
			"impl_test.toy": "assertion=1\n",
		},
	)

	verdict := CheckMutation(mutationClaim(head), mutationCtx(t, repo, base))

	if verdict.Status != engine.StatusConfirmed {
		t.Fatalf("status = %s (%s)", verdict.Status, verdict.Reason)
	}
	if !strings.Contains(verdict.Output, "mutants_created=1") {
		t.Errorf("output = %q, want exactly one mutant: the test file must not be mutated", verdict.Output)
	}
	if !strings.Contains(verdict.Output, "killed=1") {
		t.Errorf("output = %q, want one killed mutant", verdict.Output)
	}
	if verdict.Sandboxed == nil || *verdict.Sandboxed {
		t.Errorf("sandboxed = %v, want the unsandboxed test run", verdict.Sandboxed)
	}
}

// A suite that stays green proves nothing about the line: the survivor is the
// finding, with file and line.
func TestCheckMutationRefutesASurvivingMutant(t *testing.T) {
	repo, base, head := makeMutationRepo(t, "mutationsurvived",
		map[string]string{
			"impl.toy": "limit=2\n",
			"run.sh":   "#!/bin/sh\nexit 0\n",
		},
		map[string]string{"impl.toy": "limit=1\n"},
	)

	verdict := CheckMutation(mutationClaim(head), mutationCtx(t, repo, base))

	if verdict.Status != engine.StatusRefuted {
		t.Fatalf("status = %s (%s), want refuted", verdict.Status, verdict.Reason)
	}
	if !strings.Contains(verdict.Output, "impl.toy:1") {
		t.Errorf("output = %q, want the surviving file:line", verdict.Output)
	}
	if !strings.Contains(verdict.Output, "1 -> 0") {
		t.Errorf("output = %q, want the operator", verdict.Output)
	}
}

// A mutant that does not build is discarded: it is never a killed mutant, so a
// run made only of build failures must not confirm anything. The suite itself is
// green on the clean tree, so the failure comes from the mutant.
func TestCheckMutationDiscardsBuildFailures(t *testing.T) {
	repo, base, head := makeMutationRepo(t, "mutationbuild",
		map[string]string{
			"impl.toy": "limit=2\n",
			"run.sh": "#!/bin/sh\n. ./impl.toy\n" +
				"[ \"$limit\" = \"1\" ] || { echo 'COMPILE ERROR: cannot use 0 as string'; exit 1; }\n",
		},
		map[string]string{"impl.toy": "limit=1\n"},
	)

	verdict := CheckMutation(mutationClaim(head), mutationCtx(t, repo, base))

	if verdict.Status == engine.StatusConfirmed {
		t.Fatalf("a build failure was counted as a kill: %s", verdict.Reason)
	}
	if verdict.Status != engine.StatusUnverifiable {
		t.Fatalf("status = %s (%s), want unverifiable", verdict.Status, verdict.Reason)
	}
	if !strings.Contains(verdict.Output, "discarded=1") || !strings.Contains(verdict.Output, "killed=0") {
		t.Errorf("output = %q, want the discarded class", verdict.Output)
	}
}

// A build error printed before a long test summary falls out of a 64 KiB tail: the
// classifier has to read the head as well, or the build failure would be counted
// as a killed mutant.
func TestCheckMutationFindsABuildErrorOutsideTheTail(t *testing.T) {
	repo, base, head := makeMutationRepo(t, "mutationbuildhead",
		map[string]string{
			"impl.toy": "limit=2\n",
			"run.sh": "#!/bin/sh\n. ./impl.toy\n" +
				"[ \"$limit\" = \"1\" ] && exit 0\n" +
				"echo 'COMPILE ERROR: cannot use 0 as string'\n" +
				"head -c 200000 /dev/zero | tr '\\0' x\n" +
				"exit 1\n",
		},
		map[string]string{"impl.toy": "limit=1\n"},
	)

	verdict := CheckMutation(mutationClaim(head), mutationCtx(t, repo, base))

	if verdict.Status == engine.StatusConfirmed {
		t.Fatalf("a build failure outside the tail was counted as a kill: %s", verdict.Reason)
	}
	if !strings.Contains(verdict.Output, "discarded=1") {
		t.Errorf("output = %q, want the discarded class", verdict.Output)
	}
}

// A suite that is already red on the unmutated tree makes every mutant look
// killed. That is the false confirmation this claim must refuse.
func TestCheckMutationRefusesARedBaseline(t *testing.T) {
	repo, base, head := makeMutationRepo(t, "mutationredbaseline",
		map[string]string{
			"impl.toy": "limit=2\n",
			"run.sh":   "#!/bin/sh\nexit 1\n",
		},
		map[string]string{"impl.toy": "limit=1\n"},
	)

	verdict := CheckMutation(mutationClaim(head), mutationCtx(t, repo, base))

	if verdict.Status == engine.StatusConfirmed {
		t.Fatalf("a red baseline confirmed the claim: %s", verdict.Reason)
	}
	if verdict.Status != engine.StatusUnverifiable || verdict.Class != engine.ClassEnvironment {
		t.Fatalf("status = %s class = %q (%s), want an environment gap",
			verdict.Status, verdict.Class, verdict.Reason)
	}
	if !strings.Contains(verdict.Reason, "unmutated") {
		t.Errorf("reason = %q, want it to name the baseline", verdict.Reason)
	}
}

// A profile that cannot recognise a build failure cannot tell it from a failing
// test, so it must refuse instead of counting failures as kills.
func TestCheckMutationRefusesWithoutBuildErrorPattern(t *testing.T) {
	blind := toyLanguage
	blind.Name = "blind"
	blind.BuildErrorPatterns = nil

	repo, base, head := makeMutationRepo(t, "mutationblind",
		map[string]string{
			"impl.toy": "limit=2\n",
			"run.sh":   "#!/bin/sh\nexit 1\n",
		},
		map[string]string{"impl.toy": "limit=1\n"},
	)

	verdict := CheckMutation(mutationClaim(head), mutationCtx(t, repo, base, blind))

	if verdict.Status != engine.StatusUnverifiable || verdict.Class != engine.ClassEnvironment {
		t.Fatalf("status = %s class = %q (%s), want an environment gap",
			verdict.Status, verdict.Class, verdict.Reason)
	}
	if !strings.Contains(verdict.Reason, "build failure") {
		t.Errorf("reason = %q, want it to name the missing discrimination", verdict.Reason)
	}
}

// The budget is a limit, never a defect of the report: an exhausted run says the
// verifier stopped, not that the work is wrong. The claim's own wall clock is what
// expires here: a report may already have used the run's budget for other claims.
func TestCheckMutationExhaustedBudgetIsALimit(t *testing.T) {
	repo, base, head := makeMutationRepo(t, "mutationbudget",
		map[string]string{
			"impl.toy": "limit=2\n",
			"run.sh":   "#!/bin/sh\n. ./impl.toy\n[ \"$limit\" = \"1\" ]\n",
		},
		map[string]string{"impl.toy": "limit=1\n"},
	)
	ctx := mutationCtx(t, repo, base)
	start := time.Now()
	calls := 0
	ctx.Now = func() time.Time {
		calls++
		if calls == 1 {
			return start
		}
		return start.Add(MutationMaxDuration + time.Second)
	}

	verdict := CheckMutation(mutationClaim(head), ctx)

	if verdict.Class != engine.ClassLimit {
		t.Fatalf("class = %q (%s), want limit", verdict.Class, verdict.Reason)
	}
	if verdict.Status == engine.StatusConfirmed {
		t.Fatal("an exhausted run was confirmed")
	}
	if !strings.Contains(verdict.Reason, "wall clock") {
		t.Errorf("reason = %q, want it to name the exhausted wall clock", verdict.Reason)
	}
}

// More changed files than the budget covers is an exhausted budget, not a proof.
func TestCheckMutationTooManyFilesIsALimit(t *testing.T) {
	base := map[string]string{
		"run.sh": "#!/bin/sh\nexit 1\n",
	}
	head := map[string]string{}
	names := []string{"a", "b", "c", "d", "e", "f"}
	for _, name := range names {
		base[name+".toy"] = "value=2\n"
		head[name+".toy"] = "value=1\n"
	}
	repo, baseCommit, headCommit := makeMutationRepo(t, "mutationfiles", base, head)

	verdict := CheckMutation(mutationClaim(headCommit), mutationCtx(t, repo, baseCommit))

	if verdict.Class != engine.ClassLimit {
		t.Fatalf("class = %q (%s), want limit", verdict.Class, verdict.Reason)
	}
}

// A truncated mutant set is a limit too: the claim speaks about the whole diff,
// and a capped run never saw all of it.
func TestCheckMutationTruncatedMutantsIsALimit(t *testing.T) {
	total := MutationMaxMutants + 5
	base := map[string]string{
		"run.sh": `#!/bin/sh
[ "$(grep -c '^value=1$' impl.toy)" = "` + strconv.Itoa(total) + `" ]
`,
		"impl.toy": "value=2\n",
	}
	head := map[string]string{}
	var lines []string
	for index := 0; index < total; index++ {
		lines = append(lines, "value=1")
	}
	head["impl.toy"] = strings.Join(lines, "\n") + "\n"
	repo, baseCommit, headCommit := makeMutationRepo(t, "mutationtruncated", base, head)

	verdict := CheckMutation(mutationClaim(headCommit), mutationCtx(t, repo, baseCommit))

	if verdict.Status == engine.StatusConfirmed {
		t.Fatalf("a truncated run was confirmed: %s", verdict.Reason)
	}
	if verdict.Class != engine.ClassLimit {
		t.Fatalf("class = %q (%s), want limit", verdict.Class, verdict.Reason)
	}
	if !strings.Contains(verdict.Reason, "built-in budget covers") {
		t.Errorf("reason = %q, want it to name the budget", verdict.Reason)
	}
}

// Without a base there is no diff scope: an environment gap, not a confirmation.
func TestCheckMutationWithoutBaseIsAnEnvironmentGap(t *testing.T) {
	repo, base, head := makeMutationRepo(t, "mutationnobase",
		map[string]string{"impl.toy": "limit=2\n", "run.sh": "#!/bin/sh\nexit 0\n"},
		map[string]string{"impl.toy": "limit=1\n"},
	)
	ctx := mutationCtx(t, repo, base)
	ctx.Base = ""

	verdict := CheckMutation(mutationClaim(head), ctx)

	if verdict.Status != engine.StatusUnverifiable || verdict.Class != engine.ClassEnvironment {
		t.Fatalf("status = %s class = %q (%s), want an environment gap",
			verdict.Status, verdict.Class, verdict.Reason)
	}
}

// A file no profile covers is not a changed production line: the claim cannot be
// bound to it, and an empty scope must never read as success.
func TestCheckMutationWithoutLanguageIsADefect(t *testing.T) {
	repo, base, head := makeMutationRepo(t, "mutationnolanguage",
		map[string]string{"notes.md": "one\n"},
		map[string]string{"notes.md": "two\n"},
	)

	verdict := CheckMutation(mutationClaim(head), mutationCtx(t, repo, base))

	if verdict.Status != engine.StatusUnverifiable || verdict.Class != engine.ClassDefect {
		t.Fatalf("status = %s class = %q (%s), want a defect",
			verdict.Status, verdict.Class, verdict.Reason)
	}
	if !strings.Contains(verdict.Reason, "notes.md") {
		t.Errorf("reason = %q, want it to name the ignored file", verdict.Reason)
	}
}

// Changed lines that hold no code (a comment) offer no mutant: the claim is not
// checkable for that diff, which is the report's form and not the machine's.
func TestCheckMutationWithoutMutantLineIsADefect(t *testing.T) {
	repo, base, head := makeMutationRepo(t, "mutationnoline",
		map[string]string{
			"impl.toy": "# nothing to mutate\nkeep=1\n",
			"run.sh":   "#!/bin/sh\nexit 0\n",
		},
		map[string]string{"impl.toy": "# nothing to mutate, second attempt\nkeep=1\n"},
	)

	verdict := CheckMutation(mutationClaim(head), mutationCtx(t, repo, base))

	if verdict.Status != engine.StatusUnverifiable || verdict.Class != engine.ClassDefect {
		t.Fatalf("status = %s class = %q (%s), want a defect",
			verdict.Status, verdict.Class, verdict.Reason)
	}
	if !strings.Contains(verdict.Reason, "offer no mutant") {
		t.Errorf("reason = %q", verdict.Reason)
	}
}

// A committed symlink is not a source file: following it would let the claim write
// outside the throwaway checkout, into whatever the link points at.
func TestOriginalOfRefusesASymlink(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(root, "outside.txt")
	if err := os.WriteFile(outside, []byte("secret\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "link.toy")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	_, verdict := originalOf(root, "link.toy", map[string]originalFile{})

	if verdict == nil {
		t.Fatal("a symlink was accepted as a source file")
	}
	if verdict.Status != engine.StatusUnverifiable || verdict.Class != engine.ClassEnvironment {
		t.Fatalf("status = %s class = %q (%s), want an environment gap",
			verdict.Status, verdict.Class, verdict.Reason)
	}
}

// The write path re-checks the file type: the suite runs in between and could turn
// the mutated path into a symlink.
func TestWriteRegularRefusesASymlink(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target.txt")
	if err := os.WriteFile(target, []byte("keep\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	link := filepath.Join(root, "link.toy")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	if verdict := writeRegular(link, []byte("mutated\n"), 0o644); verdict == nil {
		t.Fatal("a symlink was written through")
	}
	content, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(content) != "keep\n" {
		t.Fatalf("target = %q, want it untouched", content)
	}
}

// A changed line inside a docstring or raw string is prose. A mutant there can
// only survive, and a survivor is a finding, so such lines stay outside the claim.
func TestCheckMutationSkipsBlockLiterals(t *testing.T) {
	repo, base, head := makeMutationRepo(t, "mutationdocstring",
		map[string]string{
			"impl.toy": "\"\"\"\nprose one\n\"\"\"\nlimit=2\n",
			"run.sh":   "#!/bin/sh\ngrep -q '^limit=1$' impl.toy\n",
		},
		map[string]string{"impl.toy": "\"\"\"\nreturn the caller's value\n\"\"\"\nlimit=1\n"},
	)

	verdict := CheckMutation(mutationClaim(head), mutationCtx(t, repo, base))

	if verdict.Status != engine.StatusConfirmed {
		t.Fatalf("status = %s (%s): a docstring line was mutated",
			verdict.Status, verdict.Reason)
	}
	if !strings.Contains(verdict.Output, "mutants_created=1") {
		t.Errorf("output = %q, want only the code line mutated", verdict.Output)
	}
}

// The block state is a delimiter parity: a delimiter opened an odd number of times
// leaves the line open, and nothing else is carried between lines.
func TestBlockLiteralState(t *testing.T) {
	lines := []string{
		"code := 1",
		"raw := `start",
		"prose <= prose",
		"end`",
		"code2 := 2",
		`doc := """`,
		"prose == prose",
		`"""`,
		"single := 'x'",
	}
	want := []bool{false, false, true, true, false, false, true, true, false}
	got := blockLiteralState(lines)
	if len(got) != len(want) {
		t.Fatalf("state = %v, want %v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Errorf("line %d (%q) = %v, want %v", index+1, lines[index], got[index], want[index])
		}
	}
}

// The profile order is the identity of a language: two entries may share a name,
// and merging them would run one language's suite on another's files.
func TestChangedScopeGroupsByProfileIndex(t *testing.T) {
	first, second := toyLanguage, toyLanguage
	first.Name, second.Name = "toy", "toy"
	second.Extensions = []string{".toy2"}
	second.TestCommand = "./run2.sh"
	rules := compileLanguages([]engine.Language{first, second})

	var scope changedScope
	if scope.groupFor(rules[0]).rules.language.TestCommand != "./run.sh" {
		t.Fatal("the first group lost its suite")
	}
	if scope.groupFor(rules[1]).rules.language.TestCommand != "./run2.sh" {
		t.Fatal("two same-named languages were merged into one group")
	}
	if len(scope.groups) != 2 {
		t.Fatalf("groups = %d, want 2", len(scope.groups))
	}
}

// The marker has to reach the checker through the registry, and an unknown scope
// is a defect rather than a silently ignored marker.
func TestMatchMutationScopes(t *testing.T) {
	claims := MatchMutation("[MUTATION: changed-lines] and [MUTATION: whole-repo]")
	if len(claims) != 2 {
		t.Fatalf("claims = %d, want 2", len(claims))
	}
	if claims[0].Token != "changed-lines" {
		t.Errorf("token = %q", claims[0].Token)
	}
	if claims[0].HasAllowance {
		t.Errorf("allowance declared without a field: %d", claims[0].Allowance)
	}
	if claims[1].Token != "whole-repo" {
		t.Errorf("token = %q", claims[1].Token)
	}
}

// A survivor the report justifies is not a false confirmation, so the claim may
// tolerate a count. The verdict still names every survivor.
func TestMatchMutationAllowance(t *testing.T) {
	cases := []struct {
		marker       string
		wantToken    string
		wantAllowed  int
		wantDeclared bool
	}{
		{marker: "[MUTATION: changed-lines survivors=2]", wantToken: "changed-lines", wantAllowed: 2, wantDeclared: true},
		{marker: "[MUTATION: changed-lines survivors=0]", wantToken: "changed-lines", wantAllowed: 0, wantDeclared: true},
		{marker: "[MUTATION: changed-lines survivors=-1]", wantToken: "changed-lines survivors=-1"},
		{marker: "[MUTATION: changed-lines survivors=many]", wantToken: "changed-lines survivors=many"},
		{marker: "[MUTATION: changed-lines survivors=2 survivors=3]", wantToken: "changed-lines survivors=2 survivors=3"},
	}
	for _, test := range cases {
		claims := MatchMutation(test.marker)
		if len(claims) != 1 {
			t.Fatalf("%s: claims = %d", test.marker, len(claims))
		}
		claim := claims[0]
		if claim.Token != test.wantToken {
			t.Errorf("%s: token = %q, want %q", test.marker, claim.Token, test.wantToken)
		}
		if claim.HasAllowance != test.wantDeclared || claim.Allowance != test.wantAllowed {
			t.Errorf("%s: allowance = %d (declared %v), want %d (declared %v)",
				test.marker, claim.Allowance, claim.HasAllowance, test.wantAllowed, test.wantDeclared)
		}
	}
}

// A declared allowance turns survivors into a tolerated finding, and the reason
// says what the claim really measures.
func TestCheckMutationToleratesDeclaredSurvivors(t *testing.T) {
	repo, base, head := makeMutationRepo(t, "mutationtolerated",
		map[string]string{
			"impl.toy": "limit=2\nkeep=1\n",
			"run.sh":   "#!/bin/sh\n. ./impl.toy\n[ \"$limit\" = \"1\" ]\n",
		},
		// The suite looks at limit only, so the mutant on keep survives.
		map[string]string{"impl.toy": "limit=1\nkeep=0\n"},
	)

	claim := mutationClaim(head)
	claim.HasAllowance = true
	claim.Allowance = 1

	verdict := CheckMutation(claim, mutationCtx(t, repo, base))

	if verdict.Status != engine.StatusConfirmed {
		t.Fatalf("status = %s (%s), want confirmed within the allowance",
			verdict.Status, verdict.Reason)
	}
	if !strings.Contains(verdict.Output, "impl.toy:2") {
		t.Errorf("output = %q, want the tolerated survivor named", verdict.Output)
	}
	if !strings.Contains(verdict.Reason, "sensitivity") {
		t.Errorf("reason = %q, want it to name what the claim measures", verdict.Reason)
	}
}

// An allowance the mutant set can never exceed asserts nothing: it would read as
// a confirmation while tolerating every survivor.
func TestCheckMutationRefusesAVacuousAllowance(t *testing.T) {
	repo, base, head := makeMutationRepo(t, "mutationvacuous",
		map[string]string{
			"impl.toy": "limit=2\n",
			"run.sh":   "#!/bin/sh\n. ./impl.toy\n[ \"$limit\" = \"1\" ]\n",
		},
		map[string]string{"impl.toy": "limit=1\n"},
	)
	claim := mutationClaim(head)
	claim.HasAllowance = true
	claim.Allowance = 1 // exactly the one mutant the diff offers

	verdict := CheckMutation(claim, mutationCtx(t, repo, base))

	if verdict.Status != engine.StatusUnverifiable || verdict.Class != engine.ClassDefect {
		t.Fatalf("status = %s class = %q (%s), want a defect",
			verdict.Status, verdict.Class, verdict.Reason)
	}
}

// An allowance above the mutant budget would tolerate every survivor: a defect,
// not a way to switch the claim off.
func TestCheckMutationAllowanceBeyondTheBudgetIsADefect(t *testing.T) {
	repo, base, head := makeMutationRepo(t, "mutationhugeallowance",
		map[string]string{"impl.toy": "limit=2\n", "run.sh": "#!/bin/sh\nexit 0\n"},
		map[string]string{"impl.toy": "limit=1\n"},
	)
	claim := mutationClaim(head)
	claim.HasAllowance = true
	claim.Allowance = MutationMaxMutants + 1

	verdict := CheckMutation(claim, mutationCtx(t, repo, base))

	if verdict.Status != engine.StatusUnverifiable || verdict.Class != engine.ClassDefect {
		t.Fatalf("status = %s class = %q (%s), want a defect",
			verdict.Status, verdict.Class, verdict.Reason)
	}
}
