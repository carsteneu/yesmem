package claimtypes

import (
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/carsteneu/yesmem/internal/verify/engine"
)

// branchFixture builds an inspected repository whose origin is a bare
// repository on the local filesystem. A local remote keeps the fetch hermetic:
// the claim runs the real git, but nothing leaves the machine.
func branchFixture(t *testing.T, remoteBranch string) (repo, commit, remote string) {
	t.Helper()
	repo, commit = makeClaimRepo(t, "branchpushed", map[string]string{"README.md": "probe\n"})
	remote = repo + "-remote.git"
	fixtureGit(t, repo, "init", "--quiet", "--bare", remote)
	fixtureGit(t, remote, "fetch", "--quiet", repo, "refs/heads/main:refs/heads/"+remoteBranch)
	fixtureGit(t, repo, "remote", "add", "origin", remote)
	return repo, commit, remote
}

// emptyRemoteFixture builds an inspected repository whose origin is a bare
// repository that carries no branch at all.
func emptyRemoteFixture(t *testing.T) (repo, commit, remote string) {
	t.Helper()
	repo, commit = makeClaimRepo(t, "branchempty", map[string]string{"README.md": "probe\n"})
	remote = repo + "-remote.git"
	fixtureGit(t, repo, "init", "--quiet", "--bare", remote)
	fixtureGit(t, repo, "remote", "add", "origin", remote)
	return repo, commit, remote
}

func fixtureGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = []string{
		"HOME=" + dir,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=T", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=T", "GIT_COMMITTER_EMAIL=t@example.com",
		"PATH=" + os.Getenv("PATH"),
	}
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
}

func containsArg(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}

func hasArgPrefix(args []string, prefix string) bool {
	for _, arg := range args {
		if strings.HasPrefix(arg, prefix) {
			return true
		}
	}
	return false
}

// A full history transfer of the real repository took more than the old 30s
// limit. Blobs are not needed for an ancestry question, so the fetch must ask
// the server for commits only.
func TestBranchFetchArgsRequestsBlobFilter(t *testing.T) {
	args := branchFetchArgs("https://example.invalid/repo.git", "main", true)
	if !containsArg(args, "--filter=blob:none") {
		t.Fatalf("expected a blob-less transfer, got args %v", args)
	}
}

// The retry that runs when the filtered transfer failed must still be a fetch
// of the same private ref; it only loses the filter.
func TestBranchFetchArgsRetriesWithoutFilter(t *testing.T) {
	args := branchFetchArgs("https://example.invalid/repo.git", "main", false)
	if hasArgPrefix(args, "--filter=") {
		t.Fatalf("the filterless retry must not request a filter, got args %v", args)
	}
	for _, want := range []string{"--no-tags", "--upload-pack=git-upload-pack", "+refs/heads/main:refs/heads/main"} {
		if !containsArg(args, want) {
			t.Fatalf("fetch args missing %q, got %v", want, args)
		}
	}
}

// The defect this test catches: a probe narrowed to the one ref cannot tell a
// missing branch from a remote that advertises nothing, so its empty answer
// would be read as proof that the branch does not exist.
func TestBranchProbeArgsListTheAdvertisedHeads(t *testing.T) {
	args := branchProbeArgs("https://example.invalid/repo.git")
	if !containsArg(args, "ls-remote") {
		t.Fatalf("expected a remote listing, got args %v", args)
	}
	if !containsArg(args, "--heads") {
		t.Fatalf("expected the branch heads only, got args %v", args)
	}
	if hasArgPrefix(args, "refs/heads/") {
		t.Fatalf("the listing must not be narrowed to one ref, got args %v", args)
	}
}

// The defect this test catches: a branch that does not exist on the remote is
// the agent's false claim, but a fetch failure used to report every case as an
// environment gap, which sends the agent into a retry instead of a push.
func TestFetchFailureVerdictSeparatesFalseClaimFromEnvironment(t *testing.T) {
	cases := []struct {
		name          string
		probe         engine.GitResult
		budgetClipped bool
		status        engine.Status
		class         engine.Class
	}{
		{
			name:   "unreachable remote is an environment gap",
			probe:  engine.GitResult{Code: 128, Stderr: "fatal: Could not resolve host: example.invalid"},
			status: engine.StatusUnverifiable,
			class:  engine.ClassEnvironment,
		},
		{
			name:   "a branch beside others means the claim is false",
			probe:  engine.GitResult{Code: 0, Stdout: "1111111111111111111111111111111111111111\trefs/heads/main\n"},
			status: engine.StatusRefuted,
			class:  engine.ClassNone,
		},
		{
			name:   "nothing advertised is no proof of absence",
			probe:  engine.GitResult{Code: 0},
			status: engine.StatusUnverifiable,
			class:  engine.ClassEnvironment,
		},
		{
			name:  "the advertised branch is no verdict, it is a retry",
			probe: engine.GitResult{Code: 0, Stdout: "1111111111111111111111111111111111111111\trefs/heads/feature\n"},
		},
		{
			name:   "a tag is not a branch, so no branch is advertised",
			probe:  engine.GitResult{Code: 0, Stdout: "1111111111111111111111111111111111111111\trefs/tags/feature\n"},
			status: engine.StatusUnverifiable,
			class:  engine.ClassEnvironment,
		},
		{
			name:   "a listing cut by the output cap is no advertisement",
			probe:  engine.GitResult{Code: 124, Stderr: "output exceeded the limit"},
			status: engine.StatusUnverifiable,
			class:  engine.ClassLimit,
		},
		{
			name:          "a listing cut by the run budget is this run's limit",
			probe:         engine.GitResult{Code: 124, Stderr: "timed out after 1ms"},
			budgetClipped: true,
			status:        engine.StatusUnverifiable,
			class:         engine.ClassLimit,
		},
		{
			name:   "a listing at the tail limit may have lost the branch",
			probe:  engine.GitResult{Code: 0, Stdout: strings.Repeat("1111111111111111111111111111111111111111\trefs/heads/x\n", engine.TailBytes/52)},
			status: engine.StatusUnverifiable,
			class:  engine.ClassLimit,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			verdict := fetchFailureVerdict("feature", testCase.probe, testCase.budgetClipped)
			if verdict.Status != testCase.status {
				t.Fatalf("expected status %q, got %q (reason: %s)", testCase.status, verdict.Status, verdict.Reason)
			}
			if verdict.Class != testCase.class {
				t.Fatalf("expected class %q, got %q (reason: %s)", testCase.class, verdict.Class, verdict.Reason)
			}
			if testCase.status == engine.StatusRefuted && !strings.Contains(verdict.Reason, "feature") {
				t.Fatalf("a refuted verdict must name the branch, got reason %q", verdict.Reason)
			}
		})
	}
}

// The defect this test catches: every interrupted fetch was filed as the
// remote's fault, so a run budget that ran out and a built-in output cap sent
// the operator looking at the network instead of at the limit.
func TestFetchInterruptedVerdictSeparatesRunLimitsFromTheTransfer(t *testing.T) {
	cases := []struct {
		name          string
		stderr        string
		budgetClipped bool
		class         engine.Class
	}{
		{
			name:   "a transfer that ran out of time is the remote's fault",
			stderr: "timed out after 2m0s",
			class:  engine.ClassEnvironment,
		},
		{
			name:          "a budget that cut the fetch short is this run's limit",
			stderr:        "timed out after 1ms",
			budgetClipped: true,
			class:         engine.ClassLimit,
		},
		{
			name:   "an exceeded output cap is this run's limit",
			stderr: "output exceeded the limit",
			class:  engine.ClassLimit,
		},
		{
			name:   "an empty message still yields a sentence",
			stderr: "",
			class:  engine.ClassEnvironment,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			verdict := interruptedVerdict("could not fetch refs/heads/main", engine.GitResult{Code: 124, Stderr: testCase.stderr}, testCase.budgetClipped)
			if verdict.Status != engine.StatusUnverifiable {
				t.Fatalf("expected UNVERIFIABLE, got %s", verdict.Status)
			}
			if verdict.Class != testCase.class {
				t.Fatalf("expected class %q, got %q (reason: %s)", testCase.class, verdict.Class, verdict.Reason)
			}
		})
	}
}

// The defect this test catches: the blob filter makes the scratch repository a
// partial clone that would fetch a missing object from the remote on its own,
// with the user's git config instead of the verifier's pins. The check has to
// hold for a URL that git cannot name as a section, which is every IPv6
// literal.
func TestDropFetchRemoteRemovesThePromisorRemote(t *testing.T) {
	for _, url := range []string{
		"git@example.invalid:team/repo.git",
		"http://[::1]:8080/repo.git",
	} {
		t.Run(url, func(t *testing.T) {
			repo, _, _ := branchFixture(t, "main")
			ctx := unsandboxedCtx(t, repo)
			dir := repo + "-scratch.git"
			fixtureGit(t, repo, "init", "--quiet", "--bare", dir)
			fixtureGit(t, dir, "config", "--local", "remote."+url+".promisor", "true")
			fixtureGit(t, dir, "config", "--local", "remote."+url+".partialclonefilter", "blob:none")

			if !dropFetchRemote(ctx, dir, url) {
				t.Fatalf("expected the promisor keys of %q to be gone", url)
			}

			remaining := ctx.GitIn(dir, "config", "--local", "--get-regexp", "^remote\\.")
			if remaining.Code == 0 && strings.TrimSpace(remaining.Stdout) != "" {
				t.Fatalf("expected no remote key, got %q", remaining.Stdout)
			}
		})
	}
}

// The defect this test catches: the push is on the remote, but the check said
// "could not fetch" and left the claim unverifiable, so the agent retried
// instead of trusting the branch. The remote branch must be confirmed.
func TestCheckBranchPushedConfirmsBranchOnTheRemote(t *testing.T) {
	repo, commit, _ := branchFixture(t, "main")

	verdict := CheckBranchPushed(engine.Claim{Kind: "branch_pushed", Branch: "main", Commit: commit}, unsandboxedCtx(t, repo))

	if verdict.Status != engine.StatusConfirmed {
		t.Fatalf("expected CONFIRMED, got %s: %s (output: %s)", verdict.Status, verdict.Reason, verdict.Output)
	}
}

// The defect this test catches: a filtered transfer that breaks on a remote
// which does carry the ref left the claim unproven instead of repeating the
// transfer in full, which is the one case the retry exists for.
func TestCheckBranchPushedRetriesWithoutTheBlobFilter(t *testing.T) {
	repo, commit, _ := branchFixture(t, "main")
	previous := blobFilter
	blobFilter = "not-a-filter-spec"
	t.Cleanup(func() { blobFilter = previous })

	verdict := CheckBranchPushed(engine.Claim{Kind: "branch_pushed", Branch: "main", Commit: commit}, unsandboxedCtx(t, repo))

	if verdict.Status != engine.StatusConfirmed {
		t.Fatalf("expected the filterless retry to confirm, got %s: %s (output: %s)", verdict.Status, verdict.Reason, verdict.Output)
	}
}

// The defect this test catches: a branch the remote never received was reported
// as an environment gap, which hides the missing push behind a retry.
func TestCheckBranchPushedRefutesMissingRemoteBranch(t *testing.T) {
	repo, commit, _ := branchFixture(t, "main")

	verdict := CheckBranchPushed(engine.Claim{Kind: "branch_pushed", Branch: "feature/not-pushed", Commit: commit}, unsandboxedCtx(t, repo))

	if verdict.Status != engine.StatusRefuted {
		t.Fatalf("expected REFUTED, got %s: %s (output: %s)", verdict.Status, verdict.Reason, verdict.Output)
	}
	if !strings.Contains(verdict.Reason, "feature/not-pushed") {
		t.Fatalf("a refuted verdict must name the branch, got reason %q", verdict.Reason)
	}
}

// The defect this test catches: a remote that advertises no branch at all says
// nothing about the claimed one, and reading that silence as a false claim
// would tell the agent to push a branch the remote may well carry.
func TestCheckBranchPushedReportsEmptyAdvertisementAsEnvironment(t *testing.T) {
	repo, commit, _ := emptyRemoteFixture(t)

	verdict := CheckBranchPushed(engine.Claim{Kind: "branch_pushed", Branch: "main", Commit: commit}, unsandboxedCtx(t, repo))

	if verdict.Status != engine.StatusUnverifiable {
		t.Fatalf("expected UNVERIFIABLE, got %s: %s (output: %s)", verdict.Status, verdict.Reason, verdict.Output)
	}
	if verdict.Class != engine.ClassEnvironment {
		t.Fatalf("expected class %q, got %q (reason: %s)", engine.ClassEnvironment, verdict.Class, verdict.Reason)
	}
}

// The defect this test catches: a fetch that ran out of time was filed as a
// plain unverifiable, and the check then paid for a second probe and a second
// transfer on a remote that had already shown it could not answer.
func TestCheckBranchPushedReportsFetchTimeoutAsEnvironment(t *testing.T) {
	repo, commit, _ := branchFixture(t, "main")
	previous := fetchTimeout
	fetchTimeout = time.Nanosecond
	t.Cleanup(func() { fetchTimeout = previous })

	verdict := CheckBranchPushed(engine.Claim{Kind: "branch_pushed", Branch: "main", Commit: commit}, unsandboxedCtx(t, repo))

	if verdict.Status != engine.StatusUnverifiable {
		t.Fatalf("expected UNVERIFIABLE, got %s: %s", verdict.Status, verdict.Reason)
	}
	if verdict.Class != engine.ClassEnvironment {
		t.Fatalf("expected class %q, got %q (reason: %s)", engine.ClassEnvironment, verdict.Class, verdict.Reason)
	}
	if !strings.Contains(verdict.Reason, "timed out") {
		t.Fatalf("expected the reason to name the timeout, got %q", verdict.Reason)
	}
	if !strings.Contains(verdict.Command, "fetch") {
		t.Fatalf("expected the fetch as the evidence, got command %q", verdict.Command)
	}
}

// The defect this test catches: a remote that cannot be reached at all was
// filed as a plain unverifiable, which does not tell the agent that the gap is
// the environment and not its own report.
func TestCheckBranchPushedReportsUnreachableRemoteAsEnvironment(t *testing.T) {
	repo, commit, remote := branchFixture(t, "main")
	fixtureGit(t, repo, "remote", "set-url", "origin", remote+"-absent")

	verdict := CheckBranchPushed(engine.Claim{Kind: "branch_pushed", Branch: "main", Commit: commit}, unsandboxedCtx(t, repo))

	if verdict.Status != engine.StatusUnverifiable {
		t.Fatalf("expected UNVERIFIABLE, got %s: %s", verdict.Status, verdict.Reason)
	}
	if verdict.Class != engine.ClassEnvironment {
		t.Fatalf("expected class %q, got %q (reason: %s)", engine.ClassEnvironment, verdict.Class, verdict.Reason)
	}
}
