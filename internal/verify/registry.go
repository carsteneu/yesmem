package verify

import (
	"fmt"
	"strings"

	"github.com/carsteneu/yesmem/internal/verify/claimtypes"
	"github.com/carsteneu/yesmem/internal/verify/engine"
)

// claimTypes is the registry the parser and the dispatcher both consult.
func claimTypes() []engine.ClaimType {
	return claimtypes.All()
}

// claimTypeFor resolves a claim kind to its registered type.
func claimTypeFor(kind string) (engine.ClaimType, bool) {
	for _, claimType := range claimTypes() {
		if claimType.Kind == kind {
			return claimType, true
		}
	}
	return engine.ClaimType{}, false
}

// NeedsRepo reports whether judging a claim of this kind reads a repository.
func NeedsRepo(kind string) bool {
	claimType, ok := claimTypeFor(kind)
	return ok && claimType.NeedsRepo
}

// RunClaim judges one claim through the registry.
//
// A hostile report must never crash the verifier: embedded NUL bytes are
// rejected up front, a broken checker is contained, and a checker that returns
// no status is reported instead of being read as a confirmation.
func RunClaim(ctx *engine.Ctx, claim Claim) Verdict {
	verdict := judge(ctx, claim)
	// The kind is always the claim's, including on every early exit: a
	// verdict without a kind would vanish from the caller's report.
	verdict.Kind = claim.Kind
	return verdict
}

func judge(ctx *engine.Ctx, claim Claim) Verdict {
	if field, found := nulField(claim); found {
		return Unverifiable("claim field " + field + " contains an embedded NUL byte").
			WithClass(ClassDefect)
	}
	claimType, ok := claimTypeFor(claim.Kind)
	if !ok {
		return Unverifiable(fmt.Sprintf("no checker registered for claim kind %q", claim.Kind))
	}
	if claimType.Check == nil {
		return Unverifiable(fmt.Sprintf("no checker implemented for claim kind %q", claim.Kind))
	}

	verdict, failed := runChecker(claimType.Check, claim, ctx)
	if failed != "" {
		return Unverifiable("checker failed: " + failed)
	}
	if verdict.Status == "" {
		return Unverifiable(fmt.Sprintf("checker for %q returned no status", claim.Kind))
	}
	return verdict
}

// runChecker contains a panicking checker: hostile input is the normal case
// here, not an exception.
func runChecker(check func(Claim, *engine.Ctx) Verdict, claim Claim, ctx *engine.Ctx) (verdict Verdict, failure string) {
	defer func() {
		if recovered := recover(); recovered != nil {
			verdict = Verdict{}
			failure = fmt.Sprint(recovered)
		}
	}()
	return check(claim, ctx), ""
}

// nulField names the claim field that carries an embedded NUL byte.
func nulField(claim Claim) (string, bool) {
	fields := []struct {
		name  string
		value string
	}{
		{"commit", claim.Commit},
		{"branch", claim.Branch},
		{"merge", claim.Merge},
		{"deploy", claim.Deploy},
		{"command", claim.Command},
		{"token", claim.Token},
		{"path", claim.Path},
		{"sha256", claim.SHA256},
		{"profile", claim.Profile},
		{"plan", claim.Plan},
	}
	for _, field := range fields {
		if strings.ContainsRune(field.value, 0) {
			return field.name, true
		}
	}
	for _, file := range claim.Files {
		if strings.ContainsRune(file, 0) {
			return "files", true
		}
	}
	for _, ref := range claim.Evidence {
		if strings.ContainsRune(ref.Key, 0) || strings.ContainsRune(ref.ID, 0) {
			return "evidence", true
		}
	}
	return "", false
}
