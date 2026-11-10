package claimtypes

import (
	"fmt"
	"strings"

	"github.com/carsteneu/yesmem/internal/verify/engine"
)

// mergeStatusValues are the report values of a [MERGE] marker that name no
// branch: a status token ("no", "pending-PR"). HEAD is a revision, not a
// branch, and resolving it would compare against origin/HEAD's branch. Any
// such value stays unverifiable rather than becoming a confirmation by
// imagination.
var mergeStatusValues = map[string]bool{
	"": true, "-": true, "no": true, "none": true,
	"pending-pr": true, "blocked-pr": true, "head": true,
}

// mergeTargetBranch is the branch a merge landed on, as (name, commit).
//
// Order: the remote's default branch, a local main or master, the current
// branch. Without a resolvable candidate the target is unknown, and the merge
// claim stays unverifiable: the target is never guessed.
func mergeTargetBranch(ctx *engine.Ctx) (string, string, bool) {
	if symbolic := ctx.Git("symbolic-ref", "--short", "refs/remotes/origin/HEAD"); symbolic.Code == 0 {
		name := strings.TrimSpace(symbolic.Stdout)
		if name != "" {
			if commit, ok := ctx.RevParse(name); ok {
				return name, commit, true
			}
		}
	}
	for _, candidate := range []string{"main", "master"} {
		if commit, ok := ctx.RevParse("refs/heads/" + candidate); ok {
			return candidate, commit, true
		}
	}
	if symbolic := ctx.Git("symbolic-ref", "--short", "HEAD"); symbolic.Code == 0 {
		name := strings.TrimSpace(symbolic.Stdout)
		if name != "" {
			if commit, ok := ctx.RevParse("HEAD"); ok {
				return name, commit, true
			}
		}
	}
	return "", "", false
}

// CheckMerge re-derives a [MERGE: <branch>] claim.
//
// The checks are structural and local, with no fetch: the bound commit must
// exist and have exactly two parents, one parent must be the tip of the named
// branch, and the other must lie on the target branch. A contradiction is a
// refutation and names the parents that really exist. A branch whose tip moved
// on after the merge leaves the claim unverifiable: no object records where a
// branch pointed at merge time.
func CheckMerge(claim engine.Claim, ctx *engine.Ctx) engine.Verdict {
	if ctx.Repo == "" {
		return engine.Unverifiable(
			"no repository given; a [MERGE] claim needs a repo to compare the merge parents").
			WithClass(engine.ClassEnvironment)
	}
	if guard := engine.RepoGuard(ctx); guard != nil {
		return *guard
	}
	branch := strings.TrimSpace(claim.Merge)
	if mergeStatusValues[strings.ToLower(branch)] {
		// A status token names no branch: nothing to check, and no defect
		// either.
		return engine.Unverifiable(fmt.Sprintf("the claim names no branch to check: %q", branch))
	}
	if !engine.ValidBranch(branch) {
		return engine.Unverifiable(fmt.Sprintf("not a valid branch name: %q", branch)).
			WithClass(engine.ClassDefect)
	}
	value := strings.TrimSpace(claim.Commit)
	if value == "" {
		return engine.Unverifiable(
			"no commit hash to bind the merge claim; the report needs one hash-shaped [COMMIT] marker").
			WithClass(engine.ClassDefect)
	}
	if !engine.CommitHashShape(value) {
		return engine.Unverifiable(fmt.Sprintf("not a commit hash: %q", value)).
			WithClass(engine.ClassDefect)
	}

	resolution := engine.FormatCommand(
		engine.RepoArgs(ctx, "rev-parse", "--verify", "--quiet", value+"^{commit}"))
	commit, ok := ctx.RevParse(value)
	if !ok {
		if ctx.Ambiguous(value) {
			return engine.Unverifiable(fmt.Sprintf(
				"commit prefix %s is ambiguous in %s; name more characters", value, ctx.Repo)).
				WithCommand(resolution).
				WithClass(engine.ClassDefect)
		}
		return engine.Refuted(fmt.Sprintf("commit %s does not exist in %s", value, ctx.Repo)).
			WithCommand(resolution)
	}

	parentsArgs := []string{"rev-list", "--parents", "-n", "1", commit}
	parentsResult := ctx.Git(parentsArgs...)
	command := engine.FormatCommand(engine.RepoArgs(ctx, parentsArgs...))
	if parentsResult.Code != 0 {
		// The repository could not answer. Classed as the environment's failure so
		// the merge tolerance in the gate's policy, which exists for the status-token
		// form only, cannot swallow it.
		return engine.Unverifiable(fmt.Sprintf("could not read the parents of %s", short(commit))).
			WithCommand(command).
			WithOutput(parentsResult.Output()).
			WithClass(engine.ClassEnvironment)
	}
	fields := strings.Fields(parentsResult.Stdout)
	var parents []string
	if len(fields) > 1 {
		parents = fields[1:]
	}
	actual := "none"
	if len(parents) > 0 {
		shorts := make([]string, 0, len(parents))
		for _, parent := range parents {
			shorts = append(shorts, short(parent))
		}
		actual = strings.Join(shorts, ", ")
	}
	if len(parents) != 2 {
		return engine.Refuted(fmt.Sprintf("not a two-parent merge: %s has %d parent(s) (%s)",
			short(commit), len(parents), actual)).
			WithCommand(command).
			WithOutput(strings.TrimSpace(parentsResult.Stdout))
	}

	tip, ok := ctx.RevParse("refs/heads/" + branch)
	if !ok {
		tip, ok = ctx.RevParse("refs/remotes/" + origin + "/" + branch)
	}
	if !ok {
		return engine.Unverifiable(fmt.Sprintf(
			"branch %q does not resolve in %s (neither locally nor on %s)", branch, ctx.Repo, origin)).
			WithCommand(command).
			WithOutput(strings.TrimSpace(parentsResult.Stdout))
	}
	if !containsString(parents, tip) {
		// Two findings are provable from that state: the commit lies on the
		// named branch itself, or no parent belongs to the branch's history at
		// all. In between (the tip moved on after the merge) the claim may
		// well be true for the state at merge time, and stays unverifiable.
		if onBranch, decided := ctx.IsAncestor(commit, tip); decided && onBranch {
			return engine.Refuted(fmt.Sprintf(
				"%s lies on %q (tip %s); it is not a merge of it, its parents are %s",
				short(commit), branch, short(tip), actual)).
				WithCommand(command).
				WithOutput(strings.TrimSpace(parentsResult.Stdout))
		}
		for _, parent := range parents {
			if inHistory, decided := ctx.IsAncestor(parent, tip); decided && inHistory {
				return engine.Unverifiable(fmt.Sprintf(
					"the tip of %q moved on (%s); parent %s lies in its history, so %s cannot be pinned as its merge",
					branch, short(tip), short(parent), short(commit))).
					WithCommand(command).
					WithOutput(strings.TrimSpace(parentsResult.Stdout))
			}
		}
		return engine.Refuted(fmt.Sprintf(
			"no parent of %s belongs to %q (tip %s); the parents are %s",
			short(commit), branch, short(tip), actual)).
			WithCommand(command).
			WithOutput(strings.TrimSpace(parentsResult.Stdout))
	}

	other := parents[1]
	if parents[0] == tip {
		other = parents[0]
	}
	targetName, targetCommit, ok := mergeTargetBranch(ctx)
	if !ok {
		return engine.Unverifiable(
			"cannot determine the target branch (no origin/HEAD, no local main or master, " +
				"no current branch); the other parent cannot be compared").
			WithCommand(command).
			WithOutput(strings.TrimSpace(parentsResult.Stdout)).
			WithClass(engine.ClassEnvironment)
	}

	ancestor, decided := ctx.IsAncestor(other, targetCommit)
	ancestorArgs := []string{"merge-base", "--is-ancestor", other, targetCommit}
	ancestorCommand := engine.FormatCommand(engine.RepoArgs(ctx, ancestorArgs...))
	if !decided {
		compare := ctx.Git(ancestorArgs...)
		return engine.Unverifiable(fmt.Sprintf("could not compare %s with %s", short(other), targetName)).
			WithCommand(ancestorCommand).
			WithOutput(compare.Output())
	}
	if ancestor {
		return engine.Confirmed(fmt.Sprintf(
			"%s is a merge of %q: parent %s is its tip and parent %s lies on %s",
			short(commit), branch, short(tip), short(other), targetName)).
			WithCommand(ancestorCommand).
			WithOutput(strings.TrimSpace(parentsResult.Stdout))
	}
	return engine.Refuted(fmt.Sprintf(
		"the other parent %s of %s lies on no %s: its tip %s does not contain it; the parents are %s",
		short(other), short(commit), targetName, short(targetCommit), actual)).
		WithCommand(ancestorCommand).
		WithOutput(strings.TrimSpace(parentsResult.Stdout))
}

// short is the twelve-character form verdicts and refusals use.
func short(commit string) string {
	if len(commit) > 12 {
		return commit[:12]
	}
	return commit
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
