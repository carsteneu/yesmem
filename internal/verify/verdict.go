package verify

import "github.com/carsteneu/yesmem/internal/verify/engine"

// The public judgement model. The types live in engine so that claim types can
// implement checkers without importing this package; the aliases keep the
// public surface at verify.
type (
	Claim   = engine.Claim
	Verdict = engine.Verdict
	Status  = engine.Status
	Class   = engine.Class
)

const (
	StatusConfirmed    = engine.StatusConfirmed
	StatusRefuted      = engine.StatusRefuted
	StatusUnverifiable = engine.StatusUnverifiable

	ClassNone         = engine.ClassNone
	ClassDefect       = engine.ClassDefect
	ClassEnvironment  = engine.ClassEnvironment
	ClassLimit        = engine.ClassLimit
	ClassUnverifiable = engine.ClassUnverifiable
)

// The verdict constructors are re-exported so a caller judges without
// importing the engine.
func Confirmed(reason string) Verdict    { return engine.Confirmed(reason) }
func Refuted(reason string) Verdict      { return engine.Refuted(reason) }
func Unverifiable(reason string) Verdict { return engine.Unverifiable(reason) }

// Exit codes of one verification run.
const (
	// ExitOK: at least one claim was confirmed, none refuted, no defect.
	ExitOK = 0
	// ExitRefuted: at least one claim was refuted.
	ExitRefuted = 1
	// ExitError: the run itself failed (unreadable report, broken caller).
	ExitError = 2
	// ExitNothing: nothing was refuted and nothing proven, so not success.
	ExitNothing = 3
	// ExitStrict: strict mode with an unverifiable claim left.
	ExitStrict = 4
	// ExitDefect: a form violation in the report or in the checked thing.
	ExitDefect = 5
	// ExitLimit: strict mode with an exhausted budget.
	ExitLimit = 6
)

// allClasses lists every class so the summary shape does not depend on the
// report.
var allClasses = []Class{ClassNone, ClassDefect, ClassEnvironment, ClassLimit, ClassUnverifiable}

// Summarize counts the verdicts per status.
func Summarize(verdicts []Verdict) map[Status]int {
	summary := map[Status]int{
		StatusConfirmed:    0,
		StatusRefuted:      0,
		StatusUnverifiable: 0,
	}
	for _, verdict := range verdicts {
		summary[verdict.Status]++
	}
	return summary
}

// ClassSummary counts the unconfirmed verdicts per class, every class present.
func ClassSummary(verdicts []Verdict) map[Class]int {
	counts := make(map[Class]int, len(allClasses))
	for _, class := range allClasses {
		counts[class] = 0
	}
	for _, verdict := range verdicts {
		counts[verdict.Class]++
	}
	return counts
}

// ExitCode is the run's exit code.
//
// The order is the reference's: a refutation outranks a defect, a defect fails
// in both modes, an exhausted budget only counts in strict mode, "nothing
// proven" is never success, and strict mode reserves 0 for a fully proven
// report.
func ExitCode(verdicts []Verdict, strict bool) int {
	var confirmed, unverifiable, refuted, defect, limit bool
	for _, verdict := range verdicts {
		switch verdict.Status {
		case StatusConfirmed:
			// A finding the verifier authored is no proof of the report: the exit
			// code must not call a report proven because the criterion confirmed,
			// or the same run would say "blocked" and "exit 0" at once.
			if !metaKinds[verdict.Kind] {
				confirmed = true
			}
		case StatusRefuted:
			refuted = true
		case StatusUnverifiable:
			unverifiable = true
		}
		switch verdict.Class {
		case ClassDefect:
			defect = true
		case ClassLimit:
			limit = true
		}
	}

	switch {
	case refuted:
		return ExitRefuted
	case defect:
		return ExitDefect
	case strict && limit:
		return ExitLimit
	case !confirmed:
		return ExitNothing
	case strict && unverifiable:
		return ExitStrict
	}
	return ExitOK
}
