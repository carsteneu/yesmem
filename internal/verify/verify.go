package verify

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/carsteneu/yesmem/internal/verify/claimtypes"
	"github.com/carsteneu/yesmem/internal/verify/engine"
)

// Request is everything one verification run needs.
//
// The report is the interface: a caller holds a report text and a repository,
// and gets a judgement. Nothing in the request names a harness.
type Request struct {
	// Repo is the repository the claims are checked against.
	Repo string
	// Base is the revision a diff-scope claim is compared from.
	Base string
	// Report is the report text to judge.
	Report string
	// Profile names the claim classes a report of this kind must contain.
	// Empty means no profile is applied.
	Profile string
	// ArtifactRoot is the directory [ARTIFACT] paths resolve against. Empty
	// falls back to Repo.
	ArtifactRoot string
	// TmpDir holds the throwaway checkouts. It is required: the verifier never
	// picks a directory on its own.
	TmpDir string
	// Budget bounds the whole run, not one command. Zero or negative means
	// DefaultBudget.
	Budget time.Duration
	// Strict reserves exit 0 for reports whose every claim was proven.
	Strict bool
	// Sandbox selects the confinement of command claims. Empty means
	// SandboxRequire.
	Sandbox engine.SandboxMode
	// Allowlist, ComputeAllowlist and LintAllowlist override the defaults.
	Allowlist        []string
	ComputeAllowlist []string
	LintAllowlist    []string
	// Policy decides which residual unverifiable verdict may stay. The zero
	// value tolerates nothing.
	Policy Policy
	// Sessions resolves the ids of an evidence claim. Nil leaves the claim
	// unverifiable with an environment class.
	Sessions engine.SessionStore
	// Evidence pins the evidence slots and the agent types allowed per slot.
	Evidence engine.EvidencePolicy
	// ReportSession is the session the report belongs to: every cited subagent
	// must descend from it.
	ReportSession string
	// Plans reads the plan a plan_items claim names.
	Plans engine.PlanSource
	// PlanThread is the thread a plan_items claim is checked against.
	PlanThread string
	// PlanThreads lists the threads that may hold the same agent's plan, in the
	// order they are consulted; PlanThread is appended when the list does not
	// already name it. See engine.Ctx.
	PlanThreads []string
}

// Report is the outcome of one run.
type Report struct {
	Verdicts []Verdict
	Summary  map[Status]int
	Classes  map[Class]int
	ExitCode int
	// Decision is the verdict under Request.Policy. It is the DONE gate's
	// criterion; ExitCode is not, because strict mode and a tolerated class
	// disagree on reports that are perfectly usable.
	Decision Decision
}

// The profile of a yesloop DONE report: it states a commit, a branch and a
// test run. A required class the report does not yield is a defect: the report
// is incomplete, which is the report's fault, not the environment's.
const ProfileYesloopDone = "yesloop-done"

// reportProfile is a report profile: the claim groups a report of this kind must
// contain, and the language data its quality claims work with. The language data
// is configuration, not knowledge: a claim type never branches on a language name,
// it looks a file's extension up here.
type reportProfile struct {
	requirements [][]string
	languages    []engine.Language
}

// profiles maps a profile name to its requirements. One requirement is a group
// of acceptable claim kinds: the report satisfies it with any one of them. The
// test run has two forms, because an honest failure report still states a test
// run.
var profiles = map[string]reportProfile{
	ProfileYesloopDone: {
		requirements: [][]string{
			{"commit_exists"},
			{"branch_pushed"},
			{"tests_green", "tests_exit"},
		},
		languages: claimtypes.DefaultLanguages(),
	},
}

// languagesFor is the language data a profile supplies. A report without a profile
// still gets the defaults: a quality marker is a claim about the code, not about
// the profile, and refusing it outright would only turn it into a defect.
func languagesFor(profile string) []engine.Language {
	if named, ok := profiles[profile]; ok && len(named.languages) > 0 {
		return named.languages
	}
	return claimtypes.DefaultLanguages()
}

// commandsOutsideAllowlist lists the profile's entries the allowlist does not
// already admit. Only those are a right the file has to earn: an entry that is
// already allowed changes nothing and needs no provenance.
func commandsOutsideAllowlist(commands claimtypes.ProfileCommands, allowlist []string) []string {
	var outside []string
	for _, family := range [][]string{commands.Tests, commands.Coverage} {
		for _, command := range family {
			if !engine.IsAllowed(command, allowlist) {
				outside = append(outside, command)
			}
		}
	}
	return outside
}

// sameCommands compares two command lists in order: the disk file and the commit
// only have to agree, not to be sorted.
func sameCommands(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for index := range a {
		if a[index] != b[index] {
			return false
		}
	}
	return true
}

// quoteList names commands in a reason, quoted so an argument stays readable.
func quoteList(commands []string) string {
	if len(commands) == 0 {
		return "none"
	}
	quoted := make([]string, 0, len(commands))
	for _, command := range commands {
		quoted = append(quoted, fmt.Sprintf("%q", command))
	}
	return strings.Join(quoted, ", ")
}

// metaKinds are the findings the verifier itself authors. They are not claims
// the report stated, so a profile must not be satisfied by them and a confirmed
// one is no evidence that the report proved anything.
var metaKinds = map[string]bool{"profile": true, "unknown_marker": true, "worktree_clean": true}

// MaxClaims bounds the work one report may cause. Every claim can cost a git
// traversal or a command run, so an unbounded claim count would be an unbounded
// wait, whatever the budget says.
const MaxClaims = 256

// MaxReportBytes bounds the report text itself. Parsing happens before the
// first budget check, so the input needs its own ceiling.
const MaxReportBytes = 4 << 20

// MergeBase is the default base revision of a diff-scope claim: the point where
// the inspected branch left its upstream. Empty when git cannot derive one, which
// leaves a Files-in-scope claim unverifiable rather than guessed.
func MergeBase(repo, ref string) string {
	return engine.MergeBase(repo, ref)
}

// Head is the commit the inspected tree stands on. A caller that fingerprints a
// verdict binds it to this, so work that lands later is judged again.
func Head(repo string) string {
	return engine.Head(repo)
}

// Branch is the checked-out branch of the inspected tree. It belongs in a verdict
// key for the same reason Head does: a report about a pushed branch was judged
// against that branch, and a checkout on another one is another question.
func Branch(repo string) string {
	return engine.Branch(repo)
}

// WorktreeState is the digest of the inspected tree's uncommitted residue. A
// verdict was judged against the tree as it stood, so residue that appeared later
// is another state and needs its own judgement.
func WorktreeState(repo string) string {
	return engine.WorktreeState(repo)
}

// DefaultBudget bounds a run whose caller names no budget. A forgotten field
// must not be the unbounded case.
const DefaultBudget = 10 * time.Minute

// ProfilePath is the project profile, relative to the repository root. It is
// re-exported so a caller that reports a missing profile names the same file the
// verifier reads.
const ProfilePath = claimtypes.ProfilePath

// Verify judges a report against the world.
func Verify(caller context.Context, request Request) (Report, error) {
	if request.TmpDir == "" {
		return Report{ExitCode: ExitError}, errors.New("verify: Request.TmpDir is empty")
	}
	if _, ok := profiles[request.Profile]; request.Profile != "" && !ok {
		return Report{ExitCode: ExitError}, fmt.Errorf("verify: unknown profile %q", request.Profile)
	}
	if len(request.Report) > MaxReportBytes {
		verdict := Unverifiable(fmt.Sprintf(
			"the report is %d bytes; the verifier accepts at most %d",
			len(request.Report), MaxReportBytes)).WithClass(ClassDefect)
		return newReport([]Verdict{verdict}, request.Strict, request.Policy), nil
	}
	if caller == nil {
		caller = context.Background()
	}

	// The project's own profile is read here, once, before any claim runs: a
	// file that exists and cannot be used blocks the run instead of quietly
	// judging the report against a built-in entry the project did not choose.
	allowlist := allowlistOrDefault(request.Allowlist, engine.DefaultCommandAllowlist)
	languages := languagesFor(request.Profile)
	var commands claimtypes.ProfileCommands
	if request.Repo != "" {
		project, err := claimtypes.ResolveProfile(request.Repo, languages)
		if err != nil {
			verdict := Unverifiable("the project profile cannot be read: " + err.Error()).
				WithClass(ClassDefect)
			return newReport([]Verdict{verdict}, request.Strict, request.Policy), nil
		}
		languages = project.Languages
		commands = project.Commands
	}
	// A command the project's file names is an execution right, so it may only
	// come from the history the report speaks about. The bytes beside the commits
	// can be edited by whoever is under judgement, and git can even be told to
	// hide the edit; the right therefore comes from the committed blob, and the
	// file on disk only has to agree with it.
	//
	// Only the entries the allowlist does not already admit need that provenance:
	// a profile that names `go test` grants nothing, and blocking its run would
	// blame the report for a file that never changed what may run.
	outsideDisk := commandsOutsideAllowlist(commands, allowlist)
	if len(outsideDisk) > 0 {
		committed, readable := claimtypes.CommittedProfile(request.Repo)
		outsideCommitted := commandsOutsideAllowlist(committed, allowlist)
		if !readable {
			verdict := Unverifiable(fmt.Sprintf(
				"%s names the command %q, but the commit carries no profile; a command "+
					"it names may only be admitted from the repository history (commit "+
					"the profile, or take the command out of it)", ProfilePath, outsideDisk[0])).
				WithClass(ClassDefect)
			return newReport([]Verdict{verdict}, request.Strict, request.Policy), nil
		}
		if !sameCommands(outsideDisk, outsideCommitted) {
			verdict := Unverifiable(fmt.Sprintf(
				"%s names the command %q, but the committed profile names %s; the file "+
					"on disk is not the one that was committed", ProfilePath, outsideDisk[0],
				quoteList(outsideCommitted))).
				WithClass(ClassDefect)
			return newReport([]Verdict{verdict}, request.Strict, request.Policy), nil
		}
		// The committed entries are the right; the disk's wording only had to
		// agree with them.
		commands = committed
	}

	ctx := &engine.Ctx{
		Repo:                    request.Repo,
		TmpDir:                  request.TmpDir,
		Base:                    request.Base,
		ArtifactRoot:            request.ArtifactRoot,
		Allowlist:               allowlist,
		ComputeAllowlist:        allowlistOrDefault(request.ComputeAllowlist, claimtypes.DefaultComputeAllowlist),
		LintAllowlist:           allowlistOrDefault(request.LintAllowlist, claimtypes.DefaultLintAllowlist),
		Sandbox:                 request.Sandbox,
		Caller:                  caller,
		Sessions:                request.Sessions,
		Evidence:                request.Evidence,
		ReportSession:           request.ReportSession,
		Plans:                   request.Plans,
		PlanThread:              request.PlanThread,
		PlanThreads:             request.PlanThreads,
		Languages:               languages,
		ProfileTestCommands:     commands.Tests,
		ProfileCoverageCommands: commands.Coverage,
	}
	if ctx.Sandbox == "" {
		ctx.Sandbox = engine.SandboxRequire
	}
	budget := request.Budget
	if budget <= 0 {
		budget = DefaultBudget
	}
	ctx.Deadline = time.Now().Add(budget)

	claims := profileClaims(ParseReport(request.Report), request.Profile)
	if len(claims) > MaxClaims {
		verdict := Unverifiable(fmt.Sprintf(
			"the report carries %d claims; the verifier accepts at most %d",
			len(claims), MaxClaims)).WithClass(ClassDefect)
		return newReport([]Verdict{verdict}, request.Strict, request.Policy), nil
	}
	// The working tree is judged like a claim the report did not state: it is the
	// verifier's own criterion, answered before the report's claims so a residue
	// leads the findings. It comes after the claim bound, because the report is
	// not at fault for a finding the verifier adds.
	if request.Repo != "" {
		claims = append([]Claim{{Kind: "worktree_clean"}}, claims...)
	}

	verdicts := make([]Verdict, 0, len(claims))
	for _, claim := range claims {
		switch {
		case ctx.Cancelled():
			verdicts = append(verdicts, withKind(claim, Unverifiable(
				fmt.Sprintf("the run was cancelled before %q was judged", claim.Kind))))
		case ctx.BudgetSpent():
			verdicts = append(verdicts, withKind(claim, Unverifiable(fmt.Sprintf(
				"the run budget was exhausted before %q was judged (built-in limit)", claim.Kind)).
				WithClass(ClassLimit)))
		default:
			verdicts = append(verdicts, RunClaim(ctx, claim))
		}
	}

	return newReport(verdicts, request.Strict, request.Policy), nil
}

// withKind names a verdict the dispatcher did not produce, so it reaches the
// caller under the claim kind it belongs to.
func withKind(claim Claim, verdict Verdict) Verdict {
	verdict.Kind = claim.Kind
	return verdict
}

func newReport(verdicts []Verdict, strict bool, policy Policy) Report {
	return Report{
		Verdicts: verdicts,
		Summary:  Summarize(verdicts),
		Classes:  ClassSummary(verdicts),
		ExitCode: ExitCode(verdicts, strict),
		Decision: policy.Decide(verdicts),
	}
}

func allowlistOrDefault(override, fallback []string) []string {
	if len(override) > 0 {
		return override
	}
	return fallback
}

// profileClaims adds one defect claim per requirement the report does not
// satisfy.
//
// One claim per report, not per missing class: the defect is the incomplete
// report, and its reason names every class the profile required.
func profileClaims(claims []Claim, profile string) []Claim {
	if profile == "" {
		return claims
	}
	found := make(map[string]bool, len(claims))
	for _, claim := range claims {
		if metaKinds[claim.Kind] {
			// A finding the verifier authored must not satisfy a requirement
			// the report is supposed to meet.
			continue
		}
		found[claim.Kind] = true
	}
	var missing []string
	for _, requirement := range profiles[profile].requirements {
		satisfied := false
		for _, kind := range requirement {
			if found[kind] {
				satisfied = true
				break
			}
		}
		if !satisfied {
			missing = append(missing, joinKinds(requirement))
		}
	}
	if len(missing) == 0 {
		return claims
	}
	return append(claims, Claim{
		Kind:    "profile",
		Profile: profile,
		Missing: missing,
	})
}

func joinKinds(kinds []string) string {
	joined := ""
	for index, kind := range kinds {
		if index > 0 {
			joined += "/"
		}
		joined += kind
	}
	return joined
}
