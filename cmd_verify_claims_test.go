package main

import (
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A report is the input; without one the command would verify nothing and read as a
// pass. A silent default here is the difference between "checked" and "looked at".
func TestVerifyClaimsRequiresAReportSource(t *testing.T) {
	flags, err := parseVerifyClaimsFlags([]string{"--repo", "/repo"}, io.Discard)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	_, err = flags.params(strings.NewReader(""))
	if err == nil {
		t.Fatal("params accepted a call without a report source, want an error")
	}
	if !strings.Contains(err.Error(), "--report") || !strings.Contains(err.Error(), "--section") {
		t.Errorf("error %q does not name both ways to pass a report", err)
	}
}

// The report file is the human form of the operation: the text goes to the daemon as
// it stands, and no unset flag becomes an empty parameter that changes the judgement.
func TestVerifyClaimsReadsTheReportFile(t *testing.T) {
	dir := t.TempDir()
	reportPath := filepath.Join(dir, "report.md")
	const report = "[COMMIT: abc]\n[BRANCH: main]\n"
	if err := os.WriteFile(reportPath, []byte(report), 0o600); err != nil {
		t.Fatalf("write report: %v", err)
	}

	flags, err := parseVerifyClaimsFlags([]string{"--repo", "/repo", "--report", reportPath, "--strict"}, io.Discard)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	params, err := flags.params(strings.NewReader(""))
	if err != nil {
		t.Fatalf("params: %v", err)
	}

	if got := params["report"]; got != report {
		t.Errorf("report = %q, want the file's content", got)
	}
	if got := params["repo"]; got != "/repo" {
		t.Errorf("repo = %q, want /repo", got)
	}
	if got, ok := params["strict"]; !ok || got != true {
		t.Errorf("strict = %v (present %v), want true", got, ok)
	}
	for _, absent := range []string{"section", "project", "base", "profile", "policy", "budget"} {
		if _, ok := params[absent]; ok {
			t.Errorf("params carry %q although the flag was not set", absent)
		}
	}
}

// A scratchpad section is the other source: the daemon reads it, so the CLI passes the
// names and no text.
func TestVerifyClaimsSendsTheScratchpadSection(t *testing.T) {
	flags, err := parseVerifyClaimsFlags([]string{
		"--repo", "/repo", "--project", "/proj", "--section", "yesloop-x-verify", "--budget", "2m",
	}, io.Discard)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	params, err := flags.params(strings.NewReader(""))
	if err != nil {
		t.Fatalf("params: %v", err)
	}

	if params["section"] != "yesloop-x-verify" || params["project"] != "/proj" {
		t.Errorf("params = %v, want the section and its project", params)
	}
	if _, ok := params["report"]; ok {
		t.Error("params carry a report text although the section was named")
	}
	if params["budget"] != "2m" {
		t.Errorf("budget = %v, want 2m", params["budget"])
	}
}

// The exit code is the verdict, not the process's mood: a script that pipes the report
// in has to be able to trust it.
func TestVerifyClaimsExitCodeFollowsTheVerdict(t *testing.T) {
	cases := []struct {
		name   string
		answer verifyClaimsAnswer
		want   int
	}{
		{name: "accepted", answer: verifyClaimsAnswer{Status: "accepted"}, want: 0},
		{name: "blocked", answer: verifyClaimsAnswer{Status: "blocked", Kind: "refuted"}, want: 1},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if got := verifyClaimsExitCode(test.answer); got != test.want {
				t.Errorf("exit code = %d, want %d", got, test.want)
			}
		})
	}
}

// A blocked answer has to be actionable: the finding, its class and the command that
// reproduces it. "check your report" is not something a human can follow.
func TestVerifyClaimsTextNamesTheFindingAndTheCommand(t *testing.T) {
	answer := verifyClaimsAnswer{
		Status: "blocked",
		Kind:   "refuted",
		Reason: "1 claim(s) refuted: commit 0000 does not exist",
		Findings: []verifyClaimsCLIFinding{{
			Kind:    "commit_exists",
			Status:  "REFUTED",
			Class:   "none",
			Reason:  "commit 0000 does not exist",
			Command: "git -C /repo rev-parse --verify --quiet 0000^{commit}",
		}},
	}

	text := verifyClaimsText(answer)
	for _, want := range []string{"blocked (refuted)", "commit 0000 does not exist", "commit_exists", "REFUTED", "git -C /repo rev-parse"} {
		if !strings.Contains(text, want) {
			t.Errorf("text %q does not name %q", text, want)
		}
	}
}

// The operation judges HEAD. A human who names a revision the checkout is not standing
// on must be told that, not handed a judgement about another tree.
func TestVerifyClaimsHeadCheckRefusesAnotherTree(t *testing.T) {
	repo := t.TempDir()
	// Hermetic: no host gitconfig, no signing key, no template directory.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	run := func(args ...string) string {
		cmd := exec.Command("git", append([]string{
			"-C", repo,
			"-c", "user.email=t@example.com", "-c", "user.name=t",
			"-c", "commit.gpgsign=false", "-c", "tag.gpgsign=false",
		}, args...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v (%s)", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run("init", "--quiet")
	run("commit", "--quiet", "--allow-empty", "-m", "one")
	first := run("rev-parse", "HEAD")
	run("commit", "--quiet", "--allow-empty", "-m", "two")

	if err := verifyClaimsHeadCheck(repo, first); err == nil {
		t.Fatal("head check accepted a revision the checkout does not stand on, want an error")
	}
	if err := verifyClaimsHeadCheck(repo, "HEAD"); err != nil {
		t.Errorf("head check refused the current HEAD: %v", err)
	}
	if err := verifyClaimsHeadCheck(repo, ""); err != nil {
		t.Errorf("head check without --head refused the call: %v", err)
	}
}

// The JSON shape the CLI reads is the daemon's: a changed field name must not turn an
// accepted report into a failed script.
func TestVerifyClaimsAnswerDecodesTheDaemonShape(t *testing.T) {
	const raw = `{"status":"blocked","kind":"refuted","reason":"1 claim(s) refuted","findings":[{"kind":"commit_exists","status":"REFUTED","class":"none","reason":"does not exist","command":"git rev-parse"}],"summary":{"CONFIRMED":0},"exit_code":1,"duration_ms":42,"repo":"/repo","report_from":"inline"}`

	var answer verifyClaimsAnswer
	if err := json.Unmarshal([]byte(raw), &answer); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if answer.Status != "blocked" || answer.Kind != "refuted" || answer.DurationMS != 42 || answer.ReportFrom != "inline" {
		t.Errorf("answer = %+v, want the daemon's fields", answer)
	}
	if len(answer.Findings) != 1 || answer.Findings[0].Command != "git rev-parse" {
		t.Errorf("findings = %+v, want the finding with its command", answer.Findings)
	}
}

// A budget the daemon would treat as its default cannot become a shorter client deadline:
// the call would report a timeout while the verifier runs on and holds the single slot.
func TestVerifyClaimsBudgetMustBePositive(t *testing.T) {
	for _, budget := range []string{"0s", "-5m"} {
		flags, err := parseVerifyClaimsFlags([]string{"--repo", "/repo", "--budget", budget}, io.Discard)
		if err != nil {
			t.Fatalf("parse %q: %v", budget, err)
		}
		if _, err := flags.clientDeadline(); err == nil {
			t.Errorf("--budget %q was accepted, want an error", budget)
		}
	}

	flags, err := parseVerifyClaimsFlags([]string{"--repo", "/repo", "--budget", "2m"}, io.Discard)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	deadline, err := flags.clientDeadline()
	if err != nil {
		t.Fatalf("clientDeadline: %v", err)
	}
	if deadline != 3*time.Minute {
		t.Errorf("deadline = %s, want the budget plus a minute", deadline)
	}
}

// An unbounded pipe must not be buffered whole: the report is capped where the verifier
// caps it, and the error names the limit.
func TestVerifyClaimsRefusesAnOversizedReport(t *testing.T) {
	flags, err := parseVerifyClaimsFlags([]string{"--repo", "/repo", "--report", "-"}, io.Discard)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	oversized := strings.NewReader(strings.Repeat("x", maxReportBytes+1))

	if _, err := flags.params(oversized); err == nil {
		t.Fatal("an oversized report was accepted, want an error")
	} else if !strings.Contains(err.Error(), "exceeds") {
		t.Errorf("error %q does not name the limit", err)
	}
}

// The evidence and plan claims are judged against a session and a thread; without the two
// flags a report in the gate's own shape is unverifiable through the human path.
func TestVerifyClaimsPassesTheReportSessionAndPlanThread(t *testing.T) {
	flags, err := parseVerifyClaimsFlags([]string{
		"--repo", "/repo", "--report", "-", "--report-session", "ses_abc", "--plan-thread", "thread-1",
	}, io.Discard)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	params, err := flags.params(strings.NewReader("[COMMIT: abc]\n"))
	if err != nil {
		t.Fatalf("params: %v", err)
	}
	if params["report_session"] != "ses_abc" || params["plan_thread"] != "thread-1" {
		t.Errorf("params = %v, want the report session and the plan thread", params)
	}
}
