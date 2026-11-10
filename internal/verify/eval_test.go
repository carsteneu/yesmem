package verify

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/carsteneu/yesmem/internal/verify/engine"
)

// The ported corpus: the reference's 61 cases minus the mathematical claim
// types this port does not implement (halt, searched, cycle, ident, coloring,
// lean). The list is explicit so a case that changes side fails loudly instead
// of being silently skipped.
var unportedCases = map[string]string{
	"g16-halt-confirmed":                 "halt",
	"g17-halt-beyond-verification":       "halt",
	"f16-halt-wrong-step-count":          "halt",
	"f17-halt-wrong-score":               "halt",
	"g29-halt-budget-limit":              "halt",
	"g18-searched-bounded":               "searched",
	"g19-cycle-translated":               "cycle",
	"g20-cycle-unverifiable-certificate": "cycle",
	"f19-cycle-wrong-offset":             "cycle",
	"f20-cycle-halting-machine":          "cycle",
	"g22-ident-affine-progression":       "ident",
	"f21-ident-falsified-coefficient":    "ident",
	"g23-coloring-schur-certificate":     "coloring",
	"f22-coloring-monochromatic-witness": "coloring",
	"f26-lean-sorry":                     "lean",
	"f27-lean-toolchain-path-request":    "lean",
	"g27-lean-proven":                    "lean",
}

type evalCase struct {
	Name               string            `json:"name"`
	Group              string            `json:"group"`
	Note               string            `json:"note"`
	Base               string            `json:"base"`
	Profile            string            `json:"profile"`
	Targets            []string          `json:"targets"`
	ExpectNotConfirmed []string          `json:"expect_not_confirmed"`
	ExpectVerdicts     map[string]string `json:"expect_verdicts"`
	ExpectClasses      map[string]string `json:"expect_classes"`
	ExpectExit         *int              `json:"expect_exit"`
	ExpectClaimCount   *int              `json:"expect_claim_count"`
	Report             string            `json:"report"`
}

type evalSet struct {
	Version int `json:"version"`
	Fixture struct {
		Base string `json:"base"`
		Head string `json:"head"`
	} `json:"fixture"`
	Cases []evalCase `json:"cases"`
}

func loadEvalSet(t *testing.T) evalSet {
	t.Helper()
	path := filepath.Join("testdata", "eval", "pruefset.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("cannot read the eval corpus: %v", err)
	}
	var set evalSet
	if err := json.Unmarshal(raw, &set); err != nil {
		t.Fatalf("cannot parse the eval corpus: %v", err)
	}
	if len(set.Cases) == 0 {
		t.Fatal("the eval corpus is empty")
	}
	return set
}

// The corpus carries hashes of the fixture, so the fixture build has to be
// exact. These anchors come from the reference build; a git whose object
// format, dates or merge behaviour differs fails here instead of producing a
// corpus that tests nothing.
func TestFixtureAnchors(t *testing.T) {
	set := loadEvalSet(t)
	fx := fixtureForTest(t)

	if got := fx.Commits["base"]; got != set.Fixture.Base {
		t.Errorf("base = %s, want %s", got, set.Fixture.Base)
	}
	if got := fx.Commits["fixed"]; got != set.Fixture.Head {
		t.Errorf("fixed = %s, want %s", got, set.Fixture.Head)
	}
	anchors := map[string]string{
		"good":       "10cb019d9d3ee68328c862c7954bac63ed75c481",
		"bad":        "0e1d5fb324f1d087b53ca8f174cb178572d9dc0e",
		"scoped":     "cc4967374ffc98984431a301a69af224a049bf0a",
		"unpushed":   "298fe015c3f3dd0cd35d4e59187f7095dae20614",
		"tool":       "70233b8c48827b251df35d838a30d16f870dd3cb",
		"experiment": "726fbc8f0823f87bc4263b1ba8cd7d57100a138d",
		"merge":      "5d7bdac2630a9510e1a879c685c21c520fec6062",
	}
	for key, want := range anchors {
		if got := fx.Commits[key]; got != want {
			t.Errorf("%s = %s, want %s", key, got, want)
		}
	}
}

// TestEvalContract runs every ported case of the reference corpus.
//
// This is the only protection against the port developing its own opinion: the
// Python reference and this implementation must reach the same verdicts,
// including the defect classification. A single false confirmation fails the
// test, and the summary names every mismatch.
func TestEvalContract(t *testing.T) {
	if engine.FindBwrap() == "" {
		t.Skip("bubblewrap is missing: the command claims of the corpus cannot run on this host")
	}
	set := loadEvalSet(t)
	fx := fixtureForTest(t)
	tmpRoot := fixtureTmpRoot(t)

	seen := make(map[string]bool, len(set.Cases))
	for _, testCase := range set.Cases {
		seen[testCase.Name] = true
		if reason, skip := unportedCases[testCase.Name]; skip {
			t.Run(testCase.Name, func(t *testing.T) {
				t.Skipf("claim type %q is not ported", reason)
			})
			continue
		}
		testCase := testCase
		t.Run(testCase.Name, func(t *testing.T) {
			base := testCase.Base
			if base == "" {
				base = fx.Commits["base"]
			}
			report, err := Verify(context.Background(), Request{
				Repo:    fx.Repo,
				Base:    base,
				Report:  testCase.Report,
				Profile: testCase.Profile,
				TmpDir:  filepath.Join(tmpRoot, "check", testCase.Name),
			})
			if err != nil {
				t.Fatalf("Verify: %v", err)
			}

			byKind := make(map[string][]Verdict, len(report.Verdicts))
			for _, verdict := range report.Verdicts {
				byKind[verdict.Kind] = append(byKind[verdict.Kind], verdict)
			}
			claims := ParseReport(testCase.Report)
			if testCase.ExpectClaimCount != nil && len(claims) != *testCase.ExpectClaimCount {
				t.Errorf("claim count = %d, want %d", len(claims), *testCase.ExpectClaimCount)
			}
			if testCase.ExpectExit != nil && report.ExitCode != *testCase.ExpectExit {
				t.Errorf("exit = %d, want %d", report.ExitCode, *testCase.ExpectExit)
			}
			for kind, want := range testCase.ExpectVerdicts {
				verdicts := byKind[kind]
				if len(verdicts) == 0 {
					t.Errorf("%s: no claim found, expected %s", kind, want)
					continue
				}
				for _, verdict := range verdicts {
					if string(verdict.Status) != want {
						t.Errorf("%s = %s (%s), want %s", kind, verdict.Status, verdict.Reason, want)
					}
				}
			}
			for kind, want := range testCase.ExpectClasses {
				verdicts := byKind[kind]
				if len(verdicts) == 0 {
					t.Errorf("%s: no claim found, expected class %s", kind, want)
					continue
				}
				for _, verdict := range verdicts {
					if string(verdict.Class) != want {
						t.Errorf("%s class = %q (%s), want %s", kind, verdict.Class, verdict.Reason, want)
					}
				}
			}
			for _, kind := range testCase.ExpectNotConfirmed {
				if len(byKind[kind]) == 0 {
					t.Errorf("%s: no claim found, must never be CONFIRMED", kind)
					continue
				}
				for _, verdict := range byKind[kind] {
					if verdict.Status == StatusConfirmed {
						t.Errorf("%s: CONFIRMED but must never be: %s", kind, verdict.Reason)
					}
				}
			}
			for _, kind := range testCase.Targets {
				if len(byKind[kind]) == 0 {
					t.Errorf("target %s: no claim found; the case cannot be measured", kind)
				}
			}
			// The corpus's own quality claim, restated per case: a false
			// report must never end confirmed on the kind that carries the
			// falsity.
			if testCase.Group == "false" {
				for _, kind := range testCase.Targets {
					for _, verdict := range byKind[kind] {
						if verdict.Status == StatusConfirmed {
							t.Errorf("FALSE CONFIRMATION: %s confirmed on a false report", kind)
						}
					}
				}
			}
		})
	}

	for name := range unportedCases {
		if !seen[name] {
			t.Errorf("skip list names %q, which the corpus no longer carries", name)
		}
	}
	if len(set.Cases)-len(unportedCases) != 44 {
		t.Errorf("ported cases = %d, want 44", len(set.Cases)-len(unportedCases))
	}
}

// The honest reports of the corpus must stay confirmed: a verifier that only
// rejects is worthless.
func TestEvalGenuineReportsAreConfirmed(t *testing.T) {
	if engine.FindBwrap() == "" {
		t.Skip("bubblewrap is missing: the honest command claims cannot run on this host")
	}
	set := loadEvalSet(t)
	fx := fixtureForTest(t)
	tmpRoot := fixtureTmpRoot(t)

	for _, testCase := range set.Cases {
		if testCase.Group != "genuine" {
			continue
		}
		if _, skip := unportedCases[testCase.Name]; skip {
			continue
		}
		testCase := testCase
		t.Run(testCase.Name, func(t *testing.T) {
			base := testCase.Base
			if base == "" {
				base = fx.Commits["base"]
			}
			report, err := Verify(context.Background(), Request{
				Repo:    fx.Repo,
				Base:    base,
				Report:  testCase.Report,
				Profile: testCase.Profile,
				TmpDir:  filepath.Join(tmpRoot, "honest", testCase.Name),
			})
			if err != nil {
				t.Fatalf("Verify: %v", err)
			}
			if report.ExitCode != ExitOK {
				for _, verdict := range report.Verdicts {
					t.Logf("%s = %s (%s) %s", verdict.Kind, verdict.Status, verdict.Class, verdict.Reason)
				}
				t.Fatalf("honest report %q ended with exit %d, want %d", testCase.Name, report.ExitCode, ExitOK)
			}
		})
	}
}

// The public request requires a temp directory: the verifier never picks one.
// A failed call must not carry a success exit code: a caller that forgets the
// error must not read "DONE".
func TestVerifyRequiresTmpDir(t *testing.T) {
	report, err := Verify(context.Background(), Request{})
	if err == nil {
		t.Fatal("expected an error for an empty TmpDir")
	}
	if report.ExitCode == ExitOK {
		t.Errorf("exit = %d on a failed call, want %d", report.ExitCode, ExitError)
	}
}

func TestVerifyRejectsUnknownProfile(t *testing.T) {
	_, err := Verify(context.Background(), Request{
		Report:  "nothing here\n",
		Profile: "no-such-profile",
		TmpDir:  fixtureTmpRoot(t),
	})
	if err == nil {
		t.Fatal("expected an error for an unknown profile")
	}
}

// The sandbox default is the strict one: a caller that says nothing never gets
// unsandboxed execution.
func TestVerifyDefaultsToRequiredSandbox(t *testing.T) {
	if engine.SandboxRequire != "require" {
		t.Fatalf("SandboxRequire = %q", engine.SandboxRequire)
	}
}
