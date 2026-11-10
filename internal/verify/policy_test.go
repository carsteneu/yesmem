package verify

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// The DONE gate must not read the exit code: a report can carry exit 0 and
// still be unusable, and it can carry exit 4 (strict) for a claim nobody can
// prove. The decision is the policy over the classes, and every class has to
// land on the side the plan names.
func TestPolicyDecidesOverClasses(t *testing.T) {
	blockers := []struct {
		name    string
		verdict Verdict
		kind    string
	}{
		{name: "a refutation blocks", verdict: Refuted("commit does not exist"), kind: "refuted"},
		{name: "a defect blocks", verdict: Unverifiable("unknown marker").WithClass(ClassDefect), kind: "defect"},
		{name: "an exhausted budget blocks with its own reason", verdict: Unverifiable("budget").WithClass(ClassLimit), kind: "limit"},
		{name: "an environment gap blocks", verdict: Unverifiable("no remote").WithClass(ClassEnvironment), kind: "environment"},
		{name: "a plain unverifiable blocks", verdict: Unverifiable("could not decide"), kind: "unverifiable"},
	}

	for _, blocker := range blockers {
		t.Run(blocker.name, func(t *testing.T) {
			decision := YesloopDonePolicy().Decide([]Verdict{Confirmed("ok"), blocker.verdict, Confirmed("also ok")})

			if decision.Accept {
				t.Errorf("Accept = true, want false for class %q", blocker.verdict.Class)
			}
			if decision.Kind != blocker.kind {
				t.Errorf("Kind = %q, want %q", decision.Kind, blocker.kind)
			}
			if len(decision.Findings) != 1 || decision.Findings[0].Reason != blocker.verdict.Reason {
				t.Fatalf("Findings = %v, want exactly the blocking verdict", decision.Findings)
			}
			if decision.Reason == "" {
				t.Error("Reason = empty, want a sentence the orchestrator can read")
			}
		})
	}
}

// A deploy is structurally unprovable: the verifier sees the repository, not
// the outside world. Tolerating it is the single named exception of the
// yesloop-done policy, and it must not leak to any other kind.
func TestPolicyToleratesOnlyDeploy(t *testing.T) {
	deploy := Unverifiable("the outside world is out of reach")
	deploy.Kind = "deploy"

	t.Run("deploy alone is accepted", func(t *testing.T) {
		decision := YesloopDonePolicy().Decide([]Verdict{Confirmed("commit"), deploy})
		if !decision.Accept {
			t.Errorf("Accept = false, want true (reason %q, kind %q)", decision.Reason, decision.Kind)
		}
	})

	t.Run("a tolerated kind does not carry another kind", func(t *testing.T) {
		other := Unverifiable("could not decide")
		other.Kind = "tests_green"
		decision := YesloopDonePolicy().Decide([]Verdict{Confirmed("commit"), deploy, other})
		if decision.Accept {
			t.Error("Accept = true, want false: only deploy is tolerated")
		}
		if decision.Kind != "unverifiable" {
			t.Errorf("Kind = %q, want %q", decision.Kind, "unverifiable")
		}
	})

	t.Run("the zero policy tolerates nothing", func(t *testing.T) {
		decision := Policy{}.Decide([]Verdict{Confirmed("commit"), deploy})
		if decision.Accept {
			t.Error("Accept = true, want false: an empty policy tolerates no kind")
		}
	})
}

// A report that proves nothing is not success, however clean its verdicts look.
func TestPolicyRejectsAReportThatProvesNothing(t *testing.T) {
	decision := YesloopDonePolicy().Decide(nil)

	if decision.Accept {
		t.Error("Accept = true, want false for an empty verdict set")
	}
	if decision.Kind != "nothing-confirmed" {
		t.Errorf("Kind = %q, want %q", decision.Kind, "nothing-confirmed")
	}
}

// The blocking order is the exit code's: a refutation outranks a defect, so a
// report that is both is reported as refuted.
func TestPolicyReportsTheStrongestBlocker(t *testing.T) {
	decision := YesloopDonePolicy().Decide([]Verdict{
		Unverifiable("defect").WithClass(ClassDefect),
		Refuted("wrong hash"),
	})

	if decision.Kind != "refuted" {
		t.Errorf("Kind = %q, want %q", decision.Kind, "refuted")
	}
}

// A class can carry several blocking claims, and the agent needs every reason:
// naming only the first sends it back to the report once per tick, each round
// with one line of what is wrong.
func TestPolicyNamesEveryBlockingReasonOfAClass(t *testing.T) {
	decision := YesloopDonePolicy().Decide([]Verdict{
		Refuted("commit 1111111 does not exist"),
		Refuted("commit 2222222 does not exist"),
	})

	if decision.Kind != "refuted" {
		t.Fatalf("Kind = %q, want %q", decision.Kind, "refuted")
	}
	if !strings.Contains(decision.Reason, "2 claim(s) refuted") {
		t.Errorf("Reason = %q, want the count of blocking claims", decision.Reason)
	}
	first := strings.Index(decision.Reason, "commit 1111111")
	second := strings.Index(decision.Reason, "commit 2222222")
	if first < 0 || second < 0 {
		t.Fatalf("Reason = %q, want both blocking reasons named", decision.Reason)
	}
	if first > second {
		t.Errorf("Reason = %q, want the report order (1111111 before 2222222)", decision.Reason)
	}
}

// The same reason twice is one instruction, not two. The count still states how
// many claims blocked, the list states what is wrong with them.
func TestPolicyDeduplicatesRepeatedReasons(t *testing.T) {
	decision := YesloopDonePolicy().Decide([]Verdict{
		Refuted("no upstream to derive a base from"),
		Refuted("EVIDENCE slot security is empty"),
		Refuted("no upstream to derive a base from"),
	})

	if got := strings.Count(decision.Reason, "no upstream to derive a base from"); got != 1 {
		t.Errorf("Reason = %q names the repeated reason %d times, want 1", decision.Reason, got)
	}
	if !strings.Contains(decision.Reason, "EVIDENCE slot security is empty") {
		t.Errorf("Reason = %q drops the reason stated between the duplicates", decision.Reason)
	}
	if !strings.Contains(decision.Reason, "3 claim(s) refuted") {
		t.Errorf("Reason = %q, want the count of blocking claims", decision.Reason)
	}
}

// A report can block a dozen claims of one class. The message lists ten and
// states how many it left out, so the line stays readable without hiding that
// there is more.
func TestPolicyCapsTheReasonsItLists(t *testing.T) {
	var verdicts []Verdict
	for index := 0; index < 12; index++ {
		verdicts = append(verdicts, Refuted(fmt.Sprintf("claim %02d is wrong", index)))
	}

	decision := YesloopDonePolicy().Decide(verdicts)

	for index := 0; index < 10; index++ {
		if want := fmt.Sprintf("claim %02d is wrong", index); !strings.Contains(decision.Reason, want) {
			t.Errorf("Reason = %q does not name %q", decision.Reason, want)
		}
	}
	for _, absent := range []string{"claim 10 is wrong", "claim 11 is wrong"} {
		if strings.Contains(decision.Reason, absent) {
			t.Errorf("Reason = %q names %q beyond the cap of 10", decision.Reason, absent)
		}
	}
	if !strings.Contains(decision.Reason, "and 2 more") {
		t.Errorf("Reason = %q, want the two omitted reasons counted", decision.Reason)
	}
	if !strings.Contains(decision.Reason, "12 claim(s) refuted") {
		t.Errorf("Reason = %q, want the full count of blocking claims", decision.Reason)
	}
}

// The cap counts distinct reasons: the duplicates are removed first, so the "and
// N more" figure never counts a repetition.
func TestPolicyCapsDistinctReasonsOnly(t *testing.T) {
	verdicts := []Verdict{Refuted("claim 00 is wrong")}
	for index := 1; index < 11; index++ {
		verdicts = append(verdicts, Refuted(fmt.Sprintf("claim %02d is wrong", index)))
	}
	// Twelve verdicts, eleven distinct reasons: one falls past the cap.
	verdicts = append(verdicts, Refuted("claim 00 is wrong"))

	decision := YesloopDonePolicy().Decide(verdicts)

	if !strings.Contains(decision.Reason, "12 claim(s) refuted") {
		t.Errorf("Reason = %q, want the full count of blocking claims", decision.Reason)
	}
	if !strings.Contains(decision.Reason, "and 1 more") {
		t.Errorf("Reason = %q, want exactly the one distinct reason left out", decision.Reason)
	}
	if strings.Contains(decision.Reason, "and 2 more") {
		t.Errorf("Reason = %q counts the duplicate among the omitted reasons", decision.Reason)
	}
}

// One report line can be thousands of bytes. The summary is a sentence, so a
// single reason is cut; the finding keeps its full text.
func TestPolicyShortensAnOverlongReason(t *testing.T) {
	long := strings.Repeat("x", 2000)

	decision := YesloopDonePolicy().Decide([]Verdict{
		Refuted(long),
		Refuted("the short one"),
	})

	if len(decision.Reason) > 400 {
		t.Errorf("Reason is %d bytes, want a bounded sentence", len(decision.Reason))
	}
	if !strings.Contains(decision.Reason, "the short one") {
		t.Errorf("Reason = %q drops the short reason for the long one", decision.Reason)
	}
	if len(decision.Findings) != 2 || len(decision.Findings[0].Reason) != 2000 {
		t.Errorf("Findings lost their full reason: the summary alone is bounded")
	}
}

// A verdict without a reason must not leave a dangling separator in the summary.
// The helper is exported, so an empty reason is unusual but not impossible.
func TestPolicyOmitsTheReasonListWhenNoVerdictStatesOne(t *testing.T) {
	decision := YesloopDonePolicy().Decide([]Verdict{Refuted(""), Refuted("")})

	if decision.Reason != "2 claim(s) refuted" {
		t.Errorf("Reason = %q, want the count without a dangling list", decision.Reason)
	}
}

// A merge stated as a status token names no branch, so nothing can be derived
// for it. It must not block: the skill's report template carries the marker, and
// a gate that freezes every report that follows its own template is a trap.
func TestPolicyToleratesAMergeStatusToken(t *testing.T) {
	token := Unverifiable("the claim names no branch to check: \"no\"")
	token.Kind = "merge"

	decision := YesloopDonePolicy().Decide([]Verdict{Confirmed("commit"), token})

	if !decision.Accept {
		t.Errorf("Accept = false (%s: %s), want true for a merge status token", decision.Kind, decision.Reason)
	}
}

// An honest failure report is a true claim about a red run. Confirming it is
// correct, and releasing the agent on it is not: the report itself says the
// work is unfinished.
func TestPolicyBlocksAConfirmedRedTestRun(t *testing.T) {
	red := Confirmed("python3 -m unittest test_fail.py reported exit 1, as claimed")
	red.Kind = "tests_exit"

	decision := YesloopDonePolicy().Decide([]Verdict{Confirmed("commit"), red})

	if decision.Accept {
		t.Error("Accept = true, want false: a confirmed red test run is not a completion")
	}
	if decision.Kind != "failed" {
		t.Errorf("Kind = %q, want %q", decision.Kind, "failed")
	}
	if len(decision.Findings) != 1 || decision.Findings[0].Kind != "tests_exit" {
		t.Errorf("Findings = %v, want the red test run", decision.Findings)
	}
}

// The gate's criterion is the decision, not the exit code. Strict mode turns a
// tolerated deploy into exit 4; a gate that read the exit code would freeze a
// report the policy accepts.
func TestVerifyDecisionIgnoresStrictExitForToleratedDeploy(t *testing.T) {
	fx := fixtureForTest(t)

	report, err := Verify(context.Background(), Request{
		Repo:   fx.Repo,
		Base:   fx.Commits["base"],
		Report: "[COMMIT: " + fx.Commits["base"] + "]\n[BRANCH: main]\n[DEPLOY: no]\n",
		TmpDir: filepath.Join(fixtureTmpRoot(t), "policy", "deploy"),
		Strict: true,
		Policy: YesloopDonePolicy(),
	})
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}

	if got, want := report.ExitCode, ExitStrict; got != want {
		t.Errorf("ExitCode = %d, want %d (the tolerated deploy is still unverifiable)", got, want)
	}
	if !report.Decision.Accept {
		t.Errorf("Decision.Accept = false (%s: %s), want true", report.Decision.Kind, report.Decision.Reason)
	}
}
