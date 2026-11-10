package claimtypes

import (
	"fmt"
	"strings"

	"github.com/carsteneu/yesmem/internal/verify/engine"
)

// maxWorktreeFiles bounds the file list a refutation names. A tree with a
// thousand changes is one instruction ("commit or clean"), not a transcript, and
// the reason travels into a status field and a relay.
const maxWorktreeFiles = 20

// CheckWorktreeClean judges the run's own precondition: the named repository has
// no uncommitted residue.
//
// A report speaks about commits, and the verifier sees only what the commits
// contain. Uncommitted work in the agent's worktree is invisible to every other
// claim, so a run that judged only the report could accept work the commit does
// not hold at all.
//
// `git status --porcelain` lists tracked modifications and untracked files and
// leaves out what .gitignore excludes: an ignored file is the project's statement
// that it is not part of its history, and the .yesmem directory is the ordinary
// case. The exclusion is read from the repository and from the machine, so a
// checkout can also hide a file from this criterion; that is outside what a status
// read can see, and the commit-bound claims remain the boundary.
//
// A throwaway checkout (a fresh clone, the fixture tree, a scratch worktree
// nobody edits) has an empty status, so the criterion confirms and nothing else
// changes. That is the intended no-op, not an exception.
func CheckWorktreeClean(_ engine.Claim, ctx *engine.Ctx) engine.Verdict {
	if guard := engine.RepoGuard(ctx); guard != nil {
		return *guard
	}
	command := engine.FormatCommand(engine.RepoArgs(ctx, "--no-optional-locks", "status", "--porcelain", "--untracked-files=normal"))
	// A bare repository is a documented target, and it has no working tree: there
	// is no residue to hold, so the criterion is the no-op the decisions name and
	// not an environment gap about the report.
	if bare := ctx.Git("rev-parse", "--is-bare-repository"); bare.Code == 0 &&
		strings.TrimSpace(bare.Stdout) == "true" {
		return engine.Confirmed(fmt.Sprintf("%s is a bare repository: it has no working tree to hold uncommitted files", ctx.Repo)).
			WithCommand(command)
	}
	// --no-optional-locks keeps this read from rewriting the repository's index:
	// the gate asks on every tick, and an index write could collide with the
	// agent's own commit.
	result := ctx.Git("--no-optional-locks", "status", "--porcelain", "--untracked-files=normal")
	if result.Code != 0 {
		// The tree was not readable, so nothing is known about it: an
		// environment gap, not a refutation of the report.
		return engine.Unverifiable(fmt.Sprintf("the working tree of %s cannot be read: %s",
			ctx.Repo, result.Output())).
			WithCommand(command).
			WithClass(engine.ClassEnvironment)
	}

	entries := parseStatusEntries(result.Stdout)
	if len(entries) == 0 {
		return engine.Confirmed(fmt.Sprintf("the working tree of %s holds no uncommitted file", ctx.Repo)).
			WithCommand(command)
	}
	shown := entries
	if len(shown) > maxWorktreeFiles {
		shown = shown[:maxWorktreeFiles]
	}
	reason := fmt.Sprintf("the working tree holds %d uncommitted change(s): %s",
		len(entries), strings.Join(shown, ", "))
	if omitted := len(entries) - len(shown); omitted > 0 {
		reason += fmt.Sprintf(", and %d more", omitted)
	}
	reason += "; a report speaks about commits, and this work is in none of them"
	return engine.Refuted(reason).
		WithCommand(command).
		WithOutput(strings.Join(shown, "\n"))
}

// parseStatusEntries turns porcelain lines into the entries they name. The status
// columns are dropped and the rest is kept as git reports it, including both
// sides of a rename ("old -> new"): the entry is what the reader has to clean up,
// and naming only one side would hide the other.
func parseStatusEntries(stdout string) []string {
	var entries []string
	for _, line := range strings.Split(stdout, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		// "XY <path>": the status columns are fixed width, the path follows.
		entry := line
		if len(line) > 3 {
			entry = line[3:]
		}
		entries = append(entries, strings.TrimSpace(entry))
	}
	return entries
}
