package claimtypes

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/carsteneu/yesmem/internal/verify/engine"
)

// planMarkerRe matches [PLAN: <project>].
var planMarkerRe = regexp.MustCompile(`\[PLAN:([^\]\[]*)\]`)

// MatchPlan parses the plan marker.
//
// The value it carries is the project the plan belongs to, for the reader. The
// thread the plan is looked up by is the caller's knowledge: the report does not
// name it, and one project can hold several plans at once.
func MatchPlan(line string) []engine.Claim {
	var claims []engine.Claim
	for _, match := range planMarkerRe.FindAllStringSubmatch(line, -1) {
		value := trimQuotes(match[1])
		if value == "" {
			continue
		}
		claims = append(claims, engine.Claim{Kind: "plan_items", Plan: value})
	}
	return claims
}

// CheckPlanItems judges a [PLAN: ...] claim against the stored plan.
//
// A DONE report that names a plan with open steps contradicts itself, and the
// plan is the loop's own record, not the report's: an item still pending or in
// progress is a defect. An item without a recognised status counts as open,
// which is how the plan renderer reads it too.
func CheckPlanItems(claim engine.Claim, ctx *engine.Ctx) engine.Verdict {
	if ctx.Plans == nil {
		return engine.Unverifiable(
			"no plan store is configured; the plan cannot be read").
			WithClass(engine.ClassEnvironment)
	}
	threads := ctx.PlanThreadCandidates()
	if len(threads) == 0 {
		return engine.Unverifiable(
			"the caller named no plan thread; the plan cannot be read").
			WithClass(engine.ClassEnvironment)
	}

	// An agent has more than one identity and the plan sits under the one the
	// client reported, so the store is asked for each of them in the caller's
	// order. The first plan found is the agent's plan: every candidate belongs
	// to the same agent, which is what keeps this from picking up a foreign one.
	// A read failure ends the walk: a store that cannot answer about one identity
	// cannot vouch for the others either, and confirming against a plan found
	// below the broken thread would be the unsafe direction.
	var (
		thread string
		items  []engine.PlanItem
		found  bool
	)
	for _, candidate := range threads {
		read, have, err := ctx.Plans.Plan(candidate)
		if err != nil {
			return engine.Unverifiable(fmt.Sprintf(
				"INFRASTRUCTURE, not the report: the plan of %s could not be read (%v)",
				candidate, err)).WithClass(engine.ClassEnvironment)
		}
		if have {
			thread, items, found = candidate, read, true
			break
		}
	}
	if !found {
		if len(ctx.PlanThreads) == 0 {
			// An inline caller named one thread and no agent identity behind it,
			// so the thread is all there is to report.
			return engine.Unverifiable(fmt.Sprintf(
				"thread %s has no plan; the report names plan %q", ctx.PlanThread, claim.Plan)).
				WithClass(engine.ClassEnvironment)
		}
		// The report names a project, not a thread, and the agent wrote no plan
		// under any of its own threads. That is a gap in what the agent did, not
		// a false statement by the report.
		return engine.Unverifiable(fmt.Sprintf(
			"the agent wrote no plan: none of the threads that identify it (%s) carries one; the report names plan %q",
			strings.Join(threads, ", "), claim.Plan)).WithClass(engine.ClassEnvironment)
	}

	var open []string
	for _, item := range items {
		if planItemOpen(item.Status) {
			open = append(open, fmt.Sprintf("%q (%s)", item.Content, planItemStatus(item.Status)))
		}
	}
	if len(open) > 0 {
		return engine.Unverifiable(fmt.Sprintf(
			"the plan still has %d open step(s): %s; close or cancel them with update_plan before reporting DONE",
			len(open), strings.Join(clipList(open, 3), ", "))).
			WithClass(engine.ClassDefect)
	}
	return engine.Confirmed(fmt.Sprintf(
		"all %d plan step(s) of %s are closed", len(items), thread))
}

// planItemOpen reports whether a step counts as unfinished. Only the two closed
// statuses close a step; an unknown one stays open so a typo cannot hide work.
func planItemOpen(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "completed", "cancelled":
		return false
	}
	return true
}

// planItemStatus names an unstored status for the reader.
func planItemStatus(status string) string {
	trimmed := strings.TrimSpace(status)
	if trimmed == "" {
		return "no status"
	}
	return trimmed
}

// clipList bounds a list in a message so one long plan does not produce an
// unreadable line.
func clipList(values []string, max int) []string {
	if len(values) <= max {
		return values
	}
	return append(values[:max:max], fmt.Sprintf("%d more", len(values)-max))
}
