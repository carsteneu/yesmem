package claimtypes

import (
	"fmt"
	"strings"

	"github.com/carsteneu/yesmem/internal/verify/engine"
)

// CheckCommit re-derives a [COMMIT: <hash>] claim.
//
// The value must have the shape of a hash before git sees it, and it must
// resolve to a commit in the inspected repository: a blob or tree id is a
// refutation, not a confirmation.
func CheckCommit(claim engine.Claim, ctx *engine.Ctx) engine.Verdict {
	if guard := engine.RepoGuard(ctx); guard != nil {
		return *guard
	}
	value := strings.TrimSpace(claim.Commit)
	if !engine.CommitHashShape(value) {
		return engine.Unverifiable(fmt.Sprintf("not a commit hash: %q", value)).
			WithClass(engine.ClassDefect)
	}
	command := engine.FormatCommand(engine.RepoArgs(ctx, "rev-parse", "--verify", "--quiet", value+"^{commit}"))
	resolved, ok := ctx.RevParse(value)
	if ok {
		return engine.Confirmed(fmt.Sprintf("commit %s resolves to %s", value, resolved)).
			WithCommand(command).
			WithOutput(resolved)
	}
	if ctx.Ambiguous(value) {
		// An ambiguous prefix names more than one object: the report is
		// imprecise, but the commit may well exist.
		return engine.Unverifiable(fmt.Sprintf(
			"commit prefix %s is ambiguous in %s; name more characters", value, ctx.Repo)).
			WithCommand(command).
			WithClass(engine.ClassDefect)
	}
	return engine.Refuted(fmt.Sprintf("commit %s does not exist in %s", value, ctx.Repo)).
		WithCommand(command)
}
