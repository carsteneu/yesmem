package verify

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/carsteneu/yesmem/internal/verify/engine"
)

// The project file has to reach the allowlist gate, not only the language data.
// A suite command no built-in knows is the project's word about its own stack, so
// a report that claims exactly that command must be judged by its output instead
// of being refused with an allowlist gap.
func TestTheProjectsTestCommandReachesTheClaim(t *testing.T) {
	repo, _, head := profileRepo(t, "commandreaches",
		map[string]string{"README": "one\n"},
		map[string]string{
			".yesmem/verify.yaml": "languages:\n  - name: go\n    test_command: sh ./run.sh\n",
			"run.sh":              "#!/bin/sh\necho \"Ran 2 tests in 0.01s\"\nexit 0\n",
		},
	)

	report, err := Verify(context.Background(), Request{
		Repo:    repo,
		Report:  "[COMMIT: " + head + "]\nTests run: sh ./run.sh -> exit 0\n",
		TmpDir:  profileRepoDir(t, "commandreaches-tmp"),
		Sandbox: engine.SandboxOff,
	})
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}

	var verdict *Verdict
	for index := range report.Verdicts {
		if report.Verdicts[index].Kind == "tests_green" {
			verdict = &report.Verdicts[index]
		}
	}
	if verdict == nil {
		t.Fatalf("no tests_green verdict in %+v", report.Verdicts)
	}
	if strings.Contains(verdict.Reason, "allowlist") {
		t.Fatalf("the profile's own command was refused: %s", verdict.Reason)
	}
	if verdict.Status != StatusConfirmed {
		t.Fatalf("status = %s (%s), want the run's own output to decide it",
			verdict.Status, verdict.Reason)
	}
}

// A command the profile names is an execution right, so it has to come from the
// history the report speaks about. An ignored profile is a file beside the
// commits that the party under judgement can write: admitting its command would
// let an agent have a suite confirmed without running the project's own.
func TestAnUncommittedProfileCannotGrantACommand(t *testing.T) {
	repo, _, head := profileRepo(t, "uncommittedprofile",
		map[string]string{"README": "one\n"},
		map[string]string{
			".gitignore": ".yesmem/\n",
			"run.sh":     "#!/bin/sh\necho \"Ran 2 tests in 0.01s\"\nexit 0\n",
		},
	)
	// The usual shape: .yesmem is ignored, the file is written but never committed.
	if err := os.MkdirAll(filepath.Join(repo, ".yesmem"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".yesmem", "verify.yaml"),
		[]byte("languages:\n  - name: go\n    test_command: sh ./run.sh\n"), 0o644); err != nil {
		t.Fatalf("write profile: %v", err)
	}

	report, err := Verify(context.Background(), Request{
		Repo:    repo,
		Report:  "[COMMIT: " + head + "]\nTests run: sh ./run.sh -> exit 0\n",
		TmpDir:  profileRepoDir(t, "uncommittedprofile-tmp"),
		Sandbox: engine.SandboxOff,
	})
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}

	if report.Decision.Accept {
		t.Fatal("a report was accepted on a command only an uncommitted profile names")
	}
	if report.Decision.Kind != "defect" {
		t.Fatalf("kind = %q (%s), want the defect that names the uncommitted profile",
			report.Decision.Kind, report.Decision.Reason)
	}
	if !strings.Contains(report.Decision.Reason, ".yesmem/verify.yaml") {
		t.Errorf("reason = %q, want it to name the profile file", report.Decision.Reason)
	}
	for _, verdict := range report.Verdicts {
		if verdict.Kind == "tests_green" && verdict.Status == StatusConfirmed {
			t.Error("the test claim was confirmed from a command no commit names")
		}
	}
}

// The binding is exact: a report that extends the profile's entry does not run.
// An extra argument changes what the suite does, and only the project's own
// wording is admitted.
func TestTheProjectsCommandIsNotACommandFamily(t *testing.T) {
	repo, _, head := profileRepo(t, "commandfamily",
		map[string]string{"README": "one\n"},
		map[string]string{
			".yesmem/verify.yaml": "languages:\n  - name: go\n    test_command: sh ./run.sh\n",
			"run.sh":              "#!/bin/sh\necho \"Ran 2 tests in 0.01s\"\nexit 0\n",
		},
	)

	report, err := Verify(context.Background(), Request{
		Repo:    repo,
		Report:  "[COMMIT: " + head + "]\nTests run: sh ./run.sh --all -> exit 0\n",
		TmpDir:  profileRepoDir(t, "commandfamily-tmp"),
		Sandbox: engine.SandboxOff,
	})
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}

	for _, verdict := range report.Verdicts {
		if verdict.Kind != "tests_green" {
			continue
		}
		if verdict.Status == StatusConfirmed {
			t.Fatal("a command extending the profile entry was confirmed")
		}
		if !strings.Contains(verdict.Reason, "allowlist") {
			t.Errorf("reason = %q, want the allowlist refusal", verdict.Reason)
		}
		if !strings.Contains(verdict.Reason, "sh ./run.sh") {
			t.Errorf("reason = %q, want it to name the profile's command", verdict.Reason)
		}
		return
	}
	t.Fatalf("no tests_green verdict in %+v", report.Verdicts)
}

// The provenance rule protects the command right, so it applies only where a
// right is granted. A profile that names what the allowlist already admits widens
// nothing, and blocking its run would blame the report for a file that never
// mattered.
func TestAProfileThatGrantsNothingNeedNotBeCommitted(t *testing.T) {
	repo, _, head := profileRepo(t, "narrowprofile",
		map[string]string{"README": "one\n"},
		map[string]string{".gitignore": ".yesmem/\n"},
	)
	// go test is already on the builtin allowlist by prefix: the file grants
	// nothing, and it is not part of any commit.
	if err := os.MkdirAll(filepath.Join(repo, ".yesmem"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".yesmem", "verify.yaml"),
		[]byte("languages:\n  - name: go\n    test_command: go test ./...\n"), 0o644); err != nil {
		t.Fatalf("write profile: %v", err)
	}

	report, err := Verify(context.Background(), Request{
		Repo:    repo,
		Report:  "[COMMIT: " + head + "]\n",
		TmpDir:  profileRepoDir(t, "narrowprofile-tmp"),
		Sandbox: engine.SandboxOff,
	})
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !report.Decision.Accept {
		t.Fatalf("Decision = %s: %s, want accepted: the profile grants no command",
			report.Decision.Kind, report.Decision.Reason)
	}
}

// The bytes that decide are the committed ones. A tracked profile edited in the
// working tree is not the project's word: reading it would let the party under
// judgement change the command after the fact.
func TestAModifiedProfileDoesNotGrantItsNewCommand(t *testing.T) {
	repo, _, head := profileRepo(t, "modifiedprofile",
		map[string]string{"README": "one\n"},
		map[string]string{
			".yesmem/verify.yaml": "languages:\n  - name: go\n    test_command: sh ./run.sh\n",
			"run.sh":              "#!/bin/sh\necho \"Ran 2 tests in 0.01s\"\nexit 0\n",
			"hijack.sh":           "#!/bin/sh\necho \"Ran 9 tests in 0.01s\"\nexit 0\n",
		},
	)
	// The committed profile names run.sh; the working tree names hijack.sh.
	if err := os.WriteFile(filepath.Join(repo, ".yesmem", "verify.yaml"),
		[]byte("languages:\n  - name: go\n    test_command: sh ./hijack.sh\n"), 0o644); err != nil {
		t.Fatalf("rewrite profile: %v", err)
	}

	report, err := Verify(context.Background(), Request{
		Repo:    repo,
		Report:  "[COMMIT: " + head + "]\nTests run: sh ./hijack.sh -> exit 0\n",
		TmpDir:  profileRepoDir(t, "modifiedprofile-tmp"),
		Sandbox: engine.SandboxOff,
	})
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	for _, verdict := range report.Verdicts {
		if verdict.Kind == "tests_green" && verdict.Status == StatusConfirmed {
			t.Fatal("a command only the edited profile names was confirmed")
		}
	}
	if report.Decision.Accept {
		t.Fatal("the run was accepted although the profile on disk is not the committed one")
	}
}

// The right must be bound to the bytes that run. Assume-unchanged makes git
// answer "nothing changed" for a file that did change, so a check that asks git
// instead of reading the commit can be satisfied by a hidden edit.
func TestAHiddenProfileEditDoesNotGrantItsCommand(t *testing.T) {
	repo, _, head := profileRepo(t, "hiddenprofile",
		map[string]string{"README": "one\n"},
		map[string]string{
			".yesmem/verify.yaml": "languages:\n  - name: go\n    test_command: sh ./run.sh\n",
			"run.sh":              "#!/bin/sh\necho \"Ran 2 tests in 0.01s\"\nexit 0\n",
			"hijack.sh":           "#!/bin/sh\necho \"Ran 9 tests in 0.01s\"\nexit 0\n",
		},
	)
	if out, err := exec.Command("git", "-C", repo, "update-index", "--assume-unchanged",
		".yesmem/verify.yaml").CombinedOutput(); err != nil {
		t.Fatalf("assume-unchanged: %v: %s", err, out)
	}
	if err := os.WriteFile(filepath.Join(repo, ".yesmem", "verify.yaml"),
		[]byte("languages:\n  - name: go\n    test_command: sh ./hijack.sh\n"), 0o644); err != nil {
		t.Fatalf("rewrite profile: %v", err)
	}
	if out, err := exec.Command("git", "-C", repo, "status", "--porcelain").CombinedOutput(); err != nil {
		t.Fatalf("status: %v", err)
	} else if len(out) != 0 {
		t.Fatalf("the fixture did not hide the edit: %s", out)
	}

	report, err := Verify(context.Background(), Request{
		Repo:    repo,
		Report:  "[COMMIT: " + head + "]\nTests run: sh ./hijack.sh -> exit 0\n",
		TmpDir:  profileRepoDir(t, "hiddenprofile-tmp"),
		Sandbox: engine.SandboxOff,
	})
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	for _, verdict := range report.Verdicts {
		if verdict.Kind == "tests_green" && verdict.Status == StatusConfirmed {
			t.Fatal("a command the commit never held was confirmed")
		}
	}
	if report.Decision.Kind != "defect" {
		t.Fatalf("kind = %q (%s), want the defect that names the profile",
			report.Decision.Kind, report.Decision.Reason)
	}
	if !strings.Contains(report.Decision.Reason, ".yesmem/verify.yaml") {
		t.Errorf("reason = %q, want it to name the profile file", report.Decision.Reason)
	}
}
