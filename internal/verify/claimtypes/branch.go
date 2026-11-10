package claimtypes

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/carsteneu/yesmem/internal/verify/engine"
)

const (
	origin = "origin"
	// The probe opens its own connection; it only runs after a fetch failed.
	probeTimeout = 30 * time.Second
)

// fetchTimeout bounds the transfer. A full history transfer into an empty
// scratch repository took 32s to 53s on the reference host, so the former 30s
// limit reported a working remote as unreachable. The blob filter keeps the
// usual transfer small; this limit has to fit the filterless retry as well, and
// boundedTimeout cuts it to the run's remaining budget.
//
// A variable, not a constant, so a test can drive the timeout branch without
// waiting.
var fetchTimeout = 120 * time.Second

// blobFilter is what the first attempt asks for: the ancestry question needs
// commits and trees, never a blob. A variable so a test can make git reject the
// filter and drive the filterless retry.
var blobFilter = "blob:none"

// fetchPins are the programs git must never take from a repository config.
// Every command in the scratch repository carries them, so a partial clone
// cannot reach the network through the user's setup either.
func fetchPins() []string {
	return []string{
		"-c", "core.sshCommand=ssh",
		"-c", "credential.helper=",
		"-c", "http.proxy=",
		"-c", "protocol.ext.allow=never",
	}
}

// branchFetchArgs pins the transfer to what the ancestry question needs: the
// private ref only, no tags, and no program named by a repository config. The
// blob filter leaves commits and trees; a blob is never needed to decide
// whether a commit is an ancestor, and the blobs are what made the transfer
// outlast the limit.
func branchFetchArgs(url, branch string, filter bool) []string {
	args := fetchPins()
	args = append(args,
		"fetch",
		"--quiet",
		"--no-tags",
	)
	if filter {
		args = append(args, "--filter="+blobFilter)
	}
	return append(args,
		"--upload-pack=git-upload-pack",
		url,
		"+refs/heads/"+branch+":refs/heads/"+branch,
	)
}

// branchProbeArgs lists the branches the remote advertises, with the same
// pinned programs as the fetch. The listing carries no pattern: an empty answer
// to a pattern cannot be told apart from a remote that advertises nothing, and
// only a populated listing says that a missing ref is really missing.
func branchProbeArgs(url string) []string {
	args := fetchPins()
	return append(args,
		"ls-remote",
		"--heads",
		"--upload-pack=git-upload-pack",
		url,
	)
}

// advertisedBranches returns the branch refs a listing shows.
func advertisedBranches(listing string) []string {
	var branches []string
	for _, line := range strings.Split(listing, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || !strings.HasPrefix(fields[1], "refs/heads/") {
			continue
		}
		branches = append(branches, fields[1])
	}
	return branches
}

// dropFetchRemote removes the remote the transfer was made from. The blob
// filter turns the scratch repository into a partial clone that would fetch a
// missing object on its own, through the user's git config and not through the
// pins. Without the promisor keys every later command stays local: a missing
// object fails instead of being fetched. It reports whether the keys are gone.
//
// The section name is a URL, and git refuses some of them: --remove-section
// fails for every IPv6 literal, so the two keys are unset first and the check
// asks for the keys, not for the section.
func dropFetchRemote(ctx *engine.Ctx, dir, url string) bool {
	for _, key := range []string{"promisor", "partialclonefilter"} {
		ctx.GitIn(dir, "config", "--local", "--unset-all", "remote."+url+"."+key)
	}
	ctx.GitIn(dir, "config", "--local", "--remove-section", "remote."+url)
	left := ctx.GitIn(dir, "config", "--local", "--get-regexp", `^remote\..*\.promisor$`)
	return left.Code != 0 || strings.TrimSpace(left.Stdout) == ""
}

// pinnedGit runs a command in the scratch repository with the transport pins
// applied, so even a leftover promisor remote cannot reach the network with the
// user's setup.
func pinnedGit(ctx *engine.Ctx, dir string, args ...string) engine.GitResult {
	return ctx.GitIn(dir, append(fetchPins(), args...)...)
}

// fetchFailureVerdict classifies a failed fetch. The zero Verdict means the
// remote advertises the ref, so the caller repeats the transfer without the
// filter.
//
// git's own message is localized, so the ref is looked up in a listing instead
// of in the fetch text: a branch the advertisement does not carry is a false
// claim, an unanswered or empty advertisement is an environment gap. Only the
// false claim calls for a push, so the two must not share one message.
//
// The listing cannot separate a branch that is absent from one the server hides
// (uploadpack.hideRefs): the advertisement is all there is.
func fetchFailureVerdict(branch string, probe engine.GitResult, budgetClipped bool) engine.Verdict {
	switch {
	case probe.Code == 124:
		return interruptedVerdict("could not list the branches of "+origin, probe, budgetClipped)
	case probe.Code != 0:
		return engine.Unverifiable(fmt.Sprintf("could not list the branches of %s; the remote is unreachable", origin)).
			WithClass(engine.ClassEnvironment)
	}
	// The verifier keeps only the end of a long output. A listing at that limit
	// may have lost the branch, so it must not be read as a complete one.
	if len(probe.Stdout) >= engine.TailBytes {
		return engine.Unverifiable(fmt.Sprintf("the listing of %s is too long to be read completely", origin)).
			WithClass(engine.ClassLimit)
	}
	branches := advertisedBranches(probe.Stdout)
	if len(branches) == 0 {
		return engine.Unverifiable(fmt.Sprintf("%s advertises no branches; cannot tell whether refs/heads/%s exists", origin, branch)).
			WithClass(engine.ClassEnvironment)
	}
	for _, advertised := range branches {
		if advertised == "refs/heads/"+branch {
			return engine.Verdict{}
		}
	}
	return engine.Refuted(fmt.Sprintf("%s advertises %d branches and none is refs/heads/%s", origin, len(branches), branch))
}

// interruptedVerdict classifies git's 124. runGit reports it for a timeout, for
// a run budget that cut the call short and for output that exceeded the cap. A
// transfer limit is the remote's fault; the other two are limits of this run.
func interruptedVerdict(subject string, result engine.GitResult, budgetClipped bool) engine.Verdict {
	message := fetchMessage(result.Stderr)
	if strings.Contains(message, "output exceeded") {
		return engine.Unverifiable(subject+": "+message).
			WithClass(engine.ClassLimit)
	}
	if budgetClipped {
		return engine.Unverifiable("the run budget ended before " + subject).
			WithClass(engine.ClassLimit)
	}
	return engine.Unverifiable(subject+": "+message).
		WithClass(engine.ClassEnvironment)
}

// fetchMessage keeps the remote's own words to one printable line: the reason
// is a sentence the agent reads, not a place for a second message.
func fetchMessage(stderr string) string {
	for _, line := range strings.Split(stderr, "\n") {
		line = strings.TrimSpace(strings.Map(func(r rune) rune {
			if r < 0x20 || r == 0x7f {
				return -1
			}
			return r
		}, line))
		if line != "" {
			return line
		}
	}
	return "the fetch failed"
}

// CheckBranchPushed re-derives a [BRANCH: <name>] claim.
//
// The branch is fetched into a private ref first: a tag or remote HEAD of the
// same name must not confirm a branch claim, and the private ref keeps
// concurrent runs from judging each other's fetch state. A repository without
// a remote cannot answer the question at all, which is an environment gap, not
// the agent's fault.
func CheckBranchPushed(claim engine.Claim, ctx *engine.Ctx) engine.Verdict {
	if guard := engine.RepoGuard(ctx); guard != nil {
		return *guard
	}
	branch := claim.Branch
	if !engine.ValidBranch(branch) {
		return engine.Unverifiable(fmt.Sprintf("not a valid branch name: %q", branch)).
			WithClass(engine.ClassDefect)
	}
	value := strings.TrimSpace(claim.Commit)
	if value == "" {
		return engine.Unverifiable("report names a branch but no commit hash").
			WithClass(engine.ClassDefect)
	}

	remotesResult := ctx.Git("remote")
	remotes := strings.Fields(remotesResult.Stdout)
	remotesCommand := engine.FormatCommand(engine.RepoArgs(ctx, "remote"))
	if len(remotes) == 0 {
		return engine.Unverifiable("no git remote configured; cannot verify the branch was pushed").
			WithCommand(remotesCommand).
			WithClass(engine.ClassEnvironment)
	}
	hasOrigin := false
	for _, remote := range remotes {
		if remote == origin {
			hasOrigin = true
			break
		}
	}
	if !hasOrigin {
		return engine.Unverifiable(fmt.Sprintf("no %q remote (found: %s)", origin, strings.Join(remotes, ", "))).
			WithCommand(remotesCommand).
			WithOutput(remotesResult.Output()).
			WithClass(engine.ClassEnvironment)
	}

	commit, ok := ctx.ResolveCommit(value)
	if !ok {
		return engine.Unverifiable(fmt.Sprintf("commit %q does not resolve to a commit in %s", value, ctx.Repo))
	}

	// Programs named by the repository's own config must not run during
	// verification. Hooks and fsmonitor are disabled in RepoArgs, upload-pack
	// and ssh in the fetch arguments; gitProxy and askpass have no working
	// override, so the fetch is refused when the repository sets them.
	for _, key := range []string{"core.gitProxy", "core.askpass"} {
		config := ctx.Git("config", "--local", "--get", key)
		if config.Code == 0 && strings.TrimSpace(config.Stdout) != "" {
			return engine.Unverifiable(
				fmt.Sprintf("repo config sets %s; refusing to fetch through a repo-configured program", key)).
				WithCommand(engine.FormatCommand(engine.RepoArgs(ctx, "config", "--local", "--get", key))).
				WithOutput(config.Output()).
				WithClass(engine.ClassEnvironment)
		}
	}
	proxies := ctx.Git("config", "--local", "--get-regexp", `^http\..*\.proxy$`)
	if proxies.Code == 0 && strings.TrimSpace(proxies.Stdout) != "" {
		return engine.Unverifiable("repo config sets a URL-specific http proxy; refusing to fetch through it").
			WithCommand(engine.FormatCommand(engine.RepoArgs(ctx, "config", "--local", "--get-regexp", "http-proxy"))).
			WithOutput(proxies.Output()).
			WithClass(engine.ClassEnvironment)
	}

	remoteURL := ctx.Git("remote", "get-url", origin)
	url := strings.TrimSpace(remoteURL.Stdout)
	if strings.HasPrefix(url, "-") {
		// A URL that git would read as an option must never reach the fetch
		// argument list.
		return engine.Unverifiable(fmt.Sprintf("the %q remote URL is not a URL", origin)).
			WithOutput(remoteURL.Output()).
			WithClass(engine.ClassEnvironment)
	}
	if remoteURL.Code != 0 || url == "" {
		return engine.Unverifiable(fmt.Sprintf("cannot read the %q remote URL; cannot verify the branch was pushed", origin)).
			WithCommand(engine.FormatCommand(engine.RepoArgs(ctx, "remote", "get-url", origin))).
			WithOutput(remoteURL.Output()).
			WithClass(engine.ClassEnvironment)
	}

	// The fetch happens in a throwaway bare repository, never in the inspected
	// one: the verifier must not change the object it judges.
	if err := os.MkdirAll(ctx.TmpDir, 0o755); err != nil {
		return engine.Unverifiable(fmt.Sprintf("cannot create a working directory under %s: %v", ctx.TmpDir, err)).
			WithClass(engine.ClassEnvironment)
	}
	fetchDir, err := os.MkdirTemp(ctx.TmpDir, "fetch-")
	if err != nil {
		return engine.Unverifiable(fmt.Sprintf("cannot create a scratch repository under %s: %v", ctx.TmpDir, err)).
			WithClass(engine.ClassEnvironment)
	}
	defer func() { _ = os.RemoveAll(fetchDir) }()
	if init := ctx.GitIn(fetchDir, "init", "--quiet", "--bare"); init.Code != 0 {
		return engine.Unverifiable("could not create a scratch repository for the fetch").
			WithOutput(init.Output())
	}

	fetchArgs := branchFetchArgs(url, branch, true)
	budget, bounded := ctx.Remaining()
	fetch := ctx.GitInTimeout(fetchDir, fetchTimeout, fetchArgs...)
	if fetch.Code != 0 {
		if fetch.Code == 124 {
			// A fetch that was cut short says nothing about the ref, and a
			// probe would spend the same budget a second time.
			return interruptedVerdict("could not fetch refs/heads/"+branch, fetch, bounded && budget < fetchTimeout).
				WithCommand(engine.FormatCommand(fetchArgs)).
				WithOutput(fetch.Output())
		}
		probeArgs := branchProbeArgs(url)
		probe := ctx.GitInTimeout(fetchDir, probeTimeout, probeArgs...)
		if verdict := fetchFailureVerdict(branch, probe, bounded && ctx.BudgetSpent()); verdict.Status != "" {
			return verdict.
				WithCommand(engine.FormatCommand(probeArgs)).
				WithOutput(engine.LastLines(probe.Output(), 5))
		}
		// The remote advertises the ref, so the transfer is possible: a filtered
		// transfer that breaks is retried in full, which the raised limit covers.
		fetchArgs = branchFetchArgs(url, branch, false)
		retryBudget, retryBounded := ctx.Remaining()
		fetch = ctx.GitInTimeout(fetchDir, fetchTimeout, fetchArgs...)
		if fetch.Code != 0 {
			if fetch.Code == 124 {
				return interruptedVerdict("could not fetch refs/heads/"+branch, fetch, retryBounded && retryBudget < fetchTimeout).
					WithCommand(engine.FormatCommand(fetchArgs)).
					WithOutput(fetch.Output())
			}
			return engine.Unverifiable(fmt.Sprintf("could not fetch refs/heads/%s to confirm the push", branch)).
				WithCommand(engine.FormatCommand(fetchArgs)).
				WithOutput(fetch.Output()).
				WithClass(engine.ClassEnvironment)
		}
	}
	// Every command below must stay local: a partial clone without its remote
	// fails on a missing object instead of fetching it.
	if !dropFetchRemote(ctx, fetchDir, url) {
		return engine.Unverifiable("could not detach the scratch repository from the remote; refusing to run the ancestry check").
			WithClass(engine.ClassEnvironment)
	}
	fetchedRef := "refs/heads/" + branch
	tip, ok := pinnedGit(ctx, fetchDir, "rev-parse", "--verify", "--quiet", fetchedRef).Stdout, false
	tip = strings.TrimSpace(tip)
	if tip != "" {
		if resolved := pinnedGit(ctx, fetchDir, "rev-parse", "--verify", "--quiet", fetchedRef+"^{commit}"); resolved.Code == 0 {
			ok = true
		}
	}
	if !ok {
		return engine.Unverifiable(fmt.Sprintf("%s did not resolve to a commit after fetch", fetchedRef))
	}

	// The commit must exist on that branch at all; only then can ancestry say
	// whether the branch contains it.
	if present := pinnedGit(ctx, fetchDir, "cat-file", "-e", commit+"^{commit}"); present.Code != 0 {
		return engine.Refuted(fmt.Sprintf("%s is not reachable from %s on %s", commit, branch, origin)).
			WithCommand(engine.FormatCommand(append(fetchPins(), "merge-base", "--is-ancestor", commit, fetchedRef)))
	}

	ancestor := pinnedGit(ctx, fetchDir, "merge-base", "--is-ancestor", commit, fetchedRef)
	command := engine.FormatCommand(append(fetchPins(), "merge-base", "--is-ancestor", commit, fetchedRef))
	switch ancestor.Code {
	case 0:
		return engine.Confirmed(fmt.Sprintf("%s is reachable from %s on %s", commit, branch, origin)).
			WithCommand(command).
			WithOutput(tip)
	case 1:
		return engine.Refuted(fmt.Sprintf("%s is not reachable from %s on %s", commit, branch, origin)).
			WithCommand(command)
	default:
		return engine.Unverifiable(fmt.Sprintf("could not compare %s with %s on %s", commit, branch, origin)).
			WithCommand(command).
			WithOutput(ancestor.Output())
	}
}
