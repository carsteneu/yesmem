package daemon

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/carsteneu/yesmem/internal/verify"
)

// claimsRepo builds a deterministic repository for the operation's tests: one
// commit, a passing unittest, and a bare remote that carries main. Nothing is
// taken from the host: identity, dates and the object format are pinned, so a
// host gitconfig cannot flip the hash the report states.
//
// It also returns a commit that exists locally but was never pushed: the shape a
// branch claim has to refute.
func claimsRepo(t *testing.T) (repo, head, unpushed string) {
	t.Helper()
	root := t.TempDir()
	repo = filepath.Join(root, "repo")
	remote := filepath.Join(root, "remote.git")
	home := filepath.Join(root, "home")
	for _, dir := range []string{repo, home} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	env := append(os.Environ(),
		"HOME="+home,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_DEFAULT_HASH=sha1",
		"GIT_AUTHOR_NAME=Claims",
		"GIT_AUTHOR_EMAIL=claims@example.com",
		"GIT_COMMITTER_NAME=Claims",
		"GIT_COMMITTER_EMAIL=claims@example.com",
		"GIT_AUTHOR_DATE=2026-09-01T12:00:00+0000",
		"GIT_COMMITTER_DATE=2026-09-01T12:00:00+0000",
	)
	run := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s: %v: %s", strings.Join(args, " "), err, out)
		}
		return strings.TrimSpace(string(out))
	}
	if err := os.WriteFile(filepath.Join(repo, "test_ok.py"),
		[]byte("import unittest\n\n\nclass TestOk(unittest.TestCase):\n    def test_one(self):\n        self.assertEqual(1, 1)\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(root, "git", "init", "-q", "--bare", remote)
	run(repo, "git", "-c", "init.defaultBranch=main", "init", "-q")
	run(repo, "git", "add", "-A")
	run(repo, "git", "commit", "-q", "-m", "base")
	run(repo, "git", "remote", "add", "origin", remote)
	run(repo, "git", "push", "-q", "origin", "main")
	head = run(repo, "git", "rev-parse", "HEAD")

	run(repo, "git", "commit", "-q", "--allow-empty", "-m", "unpushed")
	unpushed = run(repo, "git", "rev-parse", "HEAD")
	return repo, head, unpushed
}

// verifyClaimsCall runs the operation the way the gate runs it: the profile, the
// strict mode and the class policy are the caller's, not the operation's
// defaults.
func verifyClaimsCall(t *testing.T, h *Handler, params map[string]any) map[string]any {
	t.Helper()
	params["profile"] = "yesloop-done"
	params["strict"] = true
	params["policy"] = "yesloop-done"
	resp := h.Handle(Request{Method: "verify_claims", Params: params})
	if resp.Error != "" {
		t.Fatalf("verify_claims: %s", resp.Error)
	}
	var out map[string]any
	if err := json.Unmarshal(resp.Result, &out); err != nil {
		t.Fatalf("decode result: %v (%s)", err, resp.Result)
	}
	return out
}

// The operation is the one place a report is judged, and its answer has to
// separate the three outcomes the gate acts on: a faked report is blocked, an
// honest one accepted, and a structurally unprovable deploy tolerated instead
// of freezing the agent for it.
func TestVerifyClaimsOperation(t *testing.T) {
	h, _ := mustHandler(t)
	repo, head, _ := claimsRepo(t)

	faked := fmt.Sprintf("[COMMIT: %s]\n[BRANCH: main]\nTests run: python3 -m unittest test_ok.py -> exit 0\n",
		strings.Repeat("0", 40))
	honest := fmt.Sprintf("[COMMIT: %s]\n[BRANCH: main]\nTests run: python3 -m unittest test_ok.py -> exit 0\n", head)
	withDeploy := honest + "[DEPLOY: no]\n"

	cases := []struct {
		name     string
		report   string
		want     string
		wantKind string
	}{
		{name: "an honest report is accepted", report: honest, want: "accepted"},
		{name: "a faked commit hash is blocked", report: faked, want: "blocked", wantKind: "refuted"},
		{name: "a tolerated deploy does not block", report: withDeploy, want: "accepted"},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			out := verifyClaimsCall(t, h, map[string]any{"report": test.report, "repo": repo})

			if got := out["status"]; got != test.want {
				t.Fatalf("status = %v (%v: %v), want %s", got, out["kind"], out["reason"], test.want)
			}
			if test.wantKind != "" && out["kind"] != test.wantKind {
				t.Errorf("kind = %v, want %s", out["kind"], test.wantKind)
			}
			if findings, ok := out["findings"].([]any); test.wantKind != "" && (!ok || len(findings) == 0) {
				t.Errorf("findings = %v, want the blocking verdicts", out["findings"])
			}
		})
	}
}

// A caller that names no repository gets an error, not an accepted report: the
// claims cannot be checked against nothing.
func TestVerifyClaimsRequiresARepository(t *testing.T) {
	h, _ := mustHandler(t)
	resp := h.Handle(Request{Method: "verify_claims", Params: map[string]any{"report": "[COMMIT: abc1234]\n"}})
	if resp.Error == "" {
		t.Fatalf("expected an error, got result %s", resp.Result)
	}
}

// The operation judges the working tree of the repository it is given: residue
// there is work no commit contains, so the answer is a refutation that names the
// files. This is the entry point an agent reads, not the verifier's own verdict.
func TestVerifyClaimsRefutesADirtyWorktree(t *testing.T) {
	h, _ := mustHandler(t)
	repo, head, _ := claimsRepo(t)
	if err := os.WriteFile(filepath.Join(repo, "leftover.go"),
		[]byte("package leftover\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	out := verifyClaimsCall(t, h, map[string]any{
		"report": fmt.Sprintf("[COMMIT: %s]\n[BRANCH: main]\n", head),
		"repo":   repo,
	})

	if out["status"] != "blocked" || out["kind"] != "refuted" {
		t.Fatalf("status = %v kind = %v (%v), want blocked/refuted",
			out["status"], out["kind"], out["reason"])
	}
	if reason, _ := out["reason"].(string); !strings.Contains(reason, "leftover.go") {
		t.Errorf("reason = %q, want the file list", reason)
	}
}

// The gate's report is a scratchpad section, not an inline string. Reading it
// through the same read-only path keeps the operation usable from the gate
// without the gate passing text around.
func TestVerifyClaimsReadsAReportSection(t *testing.T) {
	h, s := mustHandler(t)
	repo, head, _ := claimsRepo(t)
	if err := s.ScratchpadWrite(repo, "yesloop-x-verify",
		fmt.Sprintf("[COMMIT: %s]\n[BRANCH: main]\nTests run: python3 -m unittest test_ok.py -> exit 0\n", head), ""); err != nil {
		t.Fatalf("ScratchpadWrite: %v", err)
	}

	out := verifyClaimsCall(t, h, map[string]any{"project": repo, "section": "yesloop-x-verify", "repo": repo})

	if out["status"] != "accepted" {
		t.Fatalf("status = %v (%v: %v), want accepted", out["status"], out["kind"], out["reason"])
	}
}

// The CLI and every script decode this answer, so its wire keys are a contract. A rename
// here would turn an accepted report into a failed run outside the process, where no Go
// compiler can see it.
func TestVerifyClaimsAnswerWireKeys(t *testing.T) {
	result := newVerifyClaimsResult(verify.Report{
		Summary: map[verify.Status]int{verify.StatusRefuted: 1},
		Classes: map[verify.Class]int{},
		Decision: verify.Decision{
			Kind:   "refuted",
			Reason: "1 claim(s) refuted",
			Findings: []verify.Verdict{{
				Kind:    "commit_exists",
				Status:  verify.StatusRefuted,
				Reason:  "does not exist",
				Command: "git rev-parse",
			}},
		},
	}, "inline", "/repo", 0)

	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var answer map[string]any
	if err := json.Unmarshal(encoded, &answer); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, key := range []string{"status", "kind", "reason", "findings", "summary", "classes", "exit_code", "duration_ms", "repo", "report_from"} {
		if _, ok := answer[key]; !ok {
			t.Errorf("answer has no %q key: the CLI decodes it by that name", key)
		}
	}
	findings, ok := answer["findings"].([]any)
	if !ok || len(findings) != 1 {
		t.Fatalf("findings = %v, want one entry", answer["findings"])
	}
	finding, _ := findings[0].(map[string]any)
	for _, key := range []string{"kind", "status", "reason", "command"} {
		if _, ok := finding[key]; !ok {
			t.Errorf("finding has no %q key", key)
		}
	}
}
