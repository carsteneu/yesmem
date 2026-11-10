package claimtypes

import (
	"fmt"
	"sort"
	"strings"

	"github.com/carsteneu/yesmem/internal/verify/engine"
)

// CheckDiffScope re-derives a "Files in scope: a, b" claim.
//
// The claimed file list is compared against the files the pinned commit
// actually changed since the base revision. A report that names the wrong
// scope is a refutation, because the scope is part of what it asserts. Without
// an explicit base, `head^` would compare only the last commit and could
// confirm a branch-wide scope while missing earlier changes, so the claim
// stays unverifiable instead.
func CheckDiffScope(claim engine.Claim, ctx *engine.Ctx) engine.Verdict {
	if guard := engine.RepoGuard(ctx); guard != nil {
		return *guard
	}
	if ctx.Base == "" {
		return engine.Unverifiable("no base revision given; the diff scope cannot be compared").
			WithClass(engine.ClassEnvironment)
	}
	planned := make(map[string]bool, len(claim.Files))
	for _, name := range claim.Files {
		planned[strings.TrimPrefix(name, "./")] = true
	}
	delete(planned, "")
	if len(planned) == 0 {
		return engine.Unverifiable("no planned file list to compare against").
			WithClass(engine.ClassDefect)
	}

	base, ok := ctx.RevParse(ctx.Base)
	if !ok {
		return engine.Unverifiable(fmt.Sprintf("base revision does not resolve: %q", ctx.Base)).
			WithClass(engine.ClassEnvironment)
	}
	headValue := strings.TrimSpace(claim.Commit)
	head, ok := ctx.ResolveCommit(headValue)
	if !ok {
		// A missing commit to bind is the report's defect; a present but
		// unresolvable one is a state the verifier cannot pin.
		reason := fmt.Sprintf("head %q does not resolve to a commit", headValue)
		if headValue == "" {
			return engine.Unverifiable(reason).WithClass(engine.ClassDefect)
		}
		return engine.Unverifiable(reason)
	}

	diffArgs := []string{"-c", "core.quotePath=false", "diff", "--name-only", "-z", base + ".." + head}
	diff := ctx.Git(diffArgs...)
	command := engine.FormatCommand(engine.RepoArgs(ctx, diffArgs...))
	if diff.Code != 0 {
		return engine.Unverifiable(fmt.Sprintf("could not diff %s..%s", short(base), short(head))).
			WithCommand(command).
			WithOutput(diff.Output())
	}

	changed := make(map[string]bool)
	for _, name := range strings.Split(diff.Stdout, "\x00") {
		name = strings.TrimRight(name, "\n")
		if strings.TrimSpace(name) != "" {
			changed[name] = true
		}
	}
	if len(changed) == 0 {
		return engine.Refuted(fmt.Sprintf(
			"no changed files between %s and %s; nothing was changed in scope",
			short(base), short(head))).
			WithCommand(command)
	}

	var extra, missing []string
	for name := range changed {
		if !planned[name] {
			extra = append(extra, name)
		}
	}
	for name := range planned {
		if !changed[name] {
			missing = append(missing, name)
		}
	}
	sort.Strings(extra)
	sort.Strings(missing)
	output := sortedNames(changed)

	if len(extra) > 0 || len(missing) > 0 {
		var details []string
		if len(extra) > 0 {
			details = append(details, "changed but not planned: "+joinNames(extra))
		}
		if len(missing) > 0 {
			details = append(details, "planned but unchanged: "+joinNames(missing))
		}
		return engine.Refuted("diff scope mismatch; " + strings.Join(details, "; ")).
			WithCommand(command).
			WithOutput(output)
	}
	return engine.Confirmed(fmt.Sprintf(
		"the %d changed files match the planned scope exactly", len(changed))).
		WithCommand(command).
		WithOutput(output)
}

func sortedNames(names map[string]bool) string {
	ordered := make([]string, 0, len(names))
	for name := range names {
		ordered = append(ordered, name)
	}
	sort.Strings(ordered)
	return strings.Join(ordered, "\n")
}

func joinNames(names []string) string {
	displayed := make([]string, 0, len(names))
	for _, name := range names {
		displayed = append(displayed, engine.DisplayName(name))
	}
	return strings.Join(displayed, ", ")
}
