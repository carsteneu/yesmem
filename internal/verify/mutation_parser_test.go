package verify

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/carsteneu/yesmem/internal/verify/engine"
)

// The two quality markers reach their checkers only through the registry and the
// parser. A missing entry would turn the claim into an unknown marker, which reads
// as a defect against the report, so the wiring is worth one test at the seam
// where it is decided.
func TestQualityMarkersParse(t *testing.T) {
	commit := strings.Repeat("a", 40)
	claims := ParseReport("[COMMIT: " + commit + "]\n" +
		"[MUTATION: changed-lines]\n" +
		"[COVERAGE: changed-lines]\n")

	byKind := map[string]int{}
	for _, claim := range claims {
		byKind[claim.Kind]++
		if claim.Kind == "mutation" || claim.Kind == "coverage_delta" {
			if claim.Commit != commit {
				t.Errorf("%s: commit = %q, want the report's commit bound", claim.Kind, claim.Commit)
			}
			if claim.Token != "changed-lines" {
				t.Errorf("%s: scope = %q", claim.Kind, claim.Token)
			}
		}
	}
	if byKind["mutation"] != 1 || byKind["coverage_delta"] != 1 {
		t.Fatalf("kinds = %v, want one mutation and one coverage_delta", byKind)
	}
	if byKind["unknown_marker"] != 0 {
		t.Errorf("unknown markers = %d, want 0: both tokens must be claimed", byKind["unknown_marker"])
	}
}

// A template marker is no claim: the placeholder rule of the parser must cover the
// new tokens too.
func TestQualityMarkerTemplatesAreNoClaims(t *testing.T) {
	claims := ParseReport("[MUTATION: <scope>]\n[COVERAGE: <scope>]\n")
	for _, claim := range claims {
		t.Errorf("template produced a claim: %s %q", claim.Kind, claim.Token)
	}
}

// Every profile has to carry language data: a quality claim without it could only
// report an environment gap.
func TestProfilesCarryLanguages(t *testing.T) {
	for name, profile := range profiles {
		if len(profile.requirements) == 0 {
			t.Errorf("%s: no requirements", name)
		}
		if len(profile.languages) == 0 {
			t.Errorf("%s: no language data", name)
		}
		for _, language := range profile.languages {
			if language.Name == "" || language.TestCommand == "" {
				t.Errorf("%s: incomplete language %+v", name, language)
			}
		}
	}
}

// The claims have to reach the gate's decision, not only their checker: the
// dispatcher, the class policy and the decision are what a DONE gate reads.
func TestQualityClaimReachesTheDecision(t *testing.T) {
	repo, commit := makeRepo(t, "qualitydecision")

	result, err := Verify(context.Background(), Request{
		Repo:    repo,
		Report:  "**send_to payload:** `[COMMIT: " + commit + "]`\n[MUTATION: changed-lines]\n",
		TmpDir:  filepath.Join(fixtureTmpRoot(t), "qualitydecision"),
		Sandbox: engine.SandboxOff,
	})
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if result.Decision.Accept {
		t.Fatal("a quality claim without a base revision was accepted")
	}
	if result.Decision.Kind != "environment" {
		t.Fatalf("decision = %q (%s), want the environment class",
			result.Decision.Kind, result.Decision.Reason)
	}
	if len(result.Decision.Findings) != 1 || result.Decision.Findings[0].Kind != "mutation" {
		t.Fatalf("findings = %+v, want the mutation verdict", result.Decision.Findings)
	}
}
