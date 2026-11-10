package verify

import (
	"fmt"
	"strings"
)

// Policy decides which unverifiable residue a report may leave behind.
//
// The zero value is strict: it tolerates nothing. A policy only relaxes the
// residual class, never a refutation, a defect, an environment gap or an
// exhausted budget: those four answer the report, the machine or the budget,
// not the claim type.
type Policy struct {
	// Tolerate names the claim kinds whose residual unverifiable verdict is
	// acceptable because the verifier cannot prove them at all.
	Tolerate map[string]bool
	// DenyConfirmed names the claim kinds whose confirmation is still not a
	// completion: an honest failure report is a correct claim about a red run,
	// and a gate that accepted it would release an agent whose own report says
	// the suite fails.
	DenyConfirmed []string
}

// YesloopDonePolicy is the DONE gate's policy.
//
// Two kinds are tolerated because the verifier sees the repository, not the
// outside world: a deploy reached a machine it cannot inspect, and a merge
// stated as a status token ("no", "pending-PR") names no branch to derive. A
// merge that names a real branch is still judged, and refuted when the
// repository contradicts it.
//
// One kind is denied even when confirmed: a test run that honestly reports a
// non-zero exit. The claim is true and the work is not done.
func YesloopDonePolicy() Policy {
	return Policy{
		Tolerate:      map[string]bool{"deploy": true, "merge": true},
		DenyConfirmed: []string{"tests_exit"},
	}
}

// Decision is the gate's judgement over a whole report: the answer a caller
// with a report and a policy needs, without re-reading the verdicts.
type Decision struct {
	// Accept is true only when nothing blocked.
	Accept bool
	// Kind names the strongest blocker: refuted, defect, limit, environment,
	// failed, unverifiable or nothing-confirmed. Empty when accepted.
	Kind string
	// Reason is the sentence a caller can report as is.
	Reason string
	// Findings are the verdicts that blocked, in report order. Empty when
	// accepted.
	Findings []Verdict
}

// denied reports whether a confirmed verdict of this kind still blocks.
func (p Policy) denied(kind string) bool {
	for _, deniedKind := range p.DenyConfirmed {
		if deniedKind == kind {
			return true
		}
	}
	return false
}

// maxDecisionReasons bounds how many blocking reasons one decision names. A
// report can leave a dozen claims of one class unproven, and the agent needs
// every distinct reason; the line stays an instruction, not a transcript.
const maxDecisionReasons = 10

// maxDecisionReasonChars bounds one reason inside the summary line.
//
// The findings keep their full reason; the summary is a sentence. A report line
// may carry up to maxLineLength bytes, and ten of those concatenated would turn
// the line into a payload that the gate then writes into a status field, a
// notification and a relay.
const maxDecisionReasonChars = 200

// shortenReason cuts a reason to the summary line's bound, keeping it valid UTF-8.
func shortenReason(reason string) string {
	runes := []rune(reason)
	if len(runes) <= maxDecisionReasonChars {
		return reason
	}
	return string(runes[:maxDecisionReasonChars]) + "..."
}

// blockingReasons lists the distinct reasons of the blocking verdicts in report
// order, capped with the rest counted.
//
// A reason repeated across claims is one instruction: it names what is wrong with
// all of them, not several things. A reason that is only reachable once per claim
// still has to reach the agent, which is why the list is not shortened to the
// first one: naming one reason per round costs a round, and a round costs a run.
// The duplicate test runs on the full text, so two long reasons that only differ
// past the cut are still two reasons.
func blockingReasons(verdicts []Verdict) string {
	reasons := make([]string, 0, len(verdicts))
	seen := make(map[string]bool, len(verdicts))
	for _, verdict := range verdicts {
		reason := strings.TrimSpace(verdict.Reason)
		if reason == "" || seen[reason] {
			continue
		}
		seen[reason] = true
		reasons = append(reasons, shortenReason(reason))
	}
	omitted := 0
	if len(reasons) > maxDecisionReasons {
		omitted = len(reasons) - maxDecisionReasons
		reasons = reasons[:maxDecisionReasons]
	}
	listed := strings.Join(reasons, "; ")
	if omitted > 0 {
		listed += fmt.Sprintf("; and %d more", omitted)
	}
	return listed
}

// Decide judges a report's verdicts under the policy.
//
// The blocking order is the exit code's: a refutation outranks a defect, which
// outranks an exhausted budget, which outranks an environment gap, which
// outranks a plain unverifiable claim. A report that blocked nothing but
// confirmed nothing is not success either.
func (p Policy) Decide(verdicts []Verdict) Decision {
	var refuted, defect, limit, environment, unverifiable, denied []Verdict
	confirmed := 0
	for _, verdict := range verdicts {
		switch verdict.Status {
		case StatusConfirmed:
			// A finding the verifier authored is no evidence that the report
			// proved anything: a clean tree must not turn a report without a
			// claim of its own into an accepted one.
			if !metaKinds[verdict.Kind] {
				confirmed++
			}
			if p.denied(verdict.Kind) {
				denied = append(denied, verdict)
			}
			continue
		case StatusRefuted:
			refuted = append(refuted, verdict)
			continue
		}
		switch verdict.Class {
		case ClassDefect:
			defect = append(defect, verdict)
		case ClassLimit:
			limit = append(limit, verdict)
		case ClassEnvironment:
			environment = append(environment, verdict)
		default:
			if !p.Tolerate[verdict.Kind] {
				unverifiable = append(unverifiable, verdict)
			}
		}
	}

	groups := []struct {
		kind      string
		verdicts  []Verdict
		statement string
	}{
		{"refuted", refuted, "refuted"},
		{"defect", defect, "a defect"},
		// A proven unfinished run outranks a gap in what the machine could do: the
		// report itself says the work is not done, and a retry would only re-derive
		// the same answer.
		{"failed", denied, "confirmed and still not a completion"},
		{"limit", limit, "an exhausted budget"},
		{"environment", environment, "an environment gap"},
		{"unverifiable", unverifiable, "unverifiable"},
	}
	for _, group := range groups {
		if len(group.verdicts) == 0 {
			continue
		}
		reason := fmt.Sprintf("%d claim(s) %s", len(group.verdicts), group.statement)
		if listed := blockingReasons(group.verdicts); listed != "" {
			reason += ": " + listed
		}
		return Decision{
			Kind:     group.kind,
			Reason:   reason,
			Findings: group.verdicts,
		}
	}
	if confirmed == 0 {
		return Decision{
			Kind:   "nothing-confirmed",
			Reason: "no claim was confirmed, so the report proves nothing",
		}
	}
	return Decision{Accept: true}
}
