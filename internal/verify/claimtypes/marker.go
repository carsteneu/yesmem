package claimtypes

import (
	"fmt"

	"github.com/carsteneu/yesmem/internal/verify/engine"
)

// CheckUnknownMarker judges a marker token no registered claim type claims.
//
// The parser emits this claim; nothing about it can be checked against the
// world, so the verdict is the form violation itself. Silently ignoring such a
// marker would let a report smuggle an unknown claim shape past the verifier.
func CheckUnknownMarker(claim engine.Claim, _ *engine.Ctx) engine.Verdict {
	return engine.Unverifiable(fmt.Sprintf(
		"unknown marker token %q: no registered claim type claims it",
		claim.Token,
	)).WithClass(engine.ClassDefect)
}

// CheckDeploy judges a [DEPLOY: ...] claim.
//
// It is structurally unprovable: the verifier can inspect the repository, not
// the outside world a deploy reached. The claim therefore never blocks a
// report by itself; the caller's policy decides whether that is acceptable.
func CheckDeploy(claim engine.Claim, _ *engine.Ctx) engine.Verdict {
	return engine.Unverifiable(fmt.Sprintf(
		"a deploy (%q) cannot be verified from the repository; the outside world is out of reach",
		claim.Deploy,
	))
}

// CheckProfile judges a report that misses a class its profile requires.
//
// The profile logic emits this claim; the missing groups ride in the claim, so
// the verdict names the report's fault rather than the environment's.
func CheckProfile(claim engine.Claim, _ *engine.Ctx) engine.Verdict {
	labels := ""
	for index, requirement := range claim.Missing {
		if index > 0 {
			labels += ", "
		}
		labels += requirement
	}
	required := "claims of classes " + labels
	if len(claim.Missing) == 1 {
		required = "a claim of class " + labels
	}
	return engine.Unverifiable(fmt.Sprintf(
		"profile %q requires %s; the report has none", claim.Profile, required,
	)).WithClass(engine.ClassDefect)
}
