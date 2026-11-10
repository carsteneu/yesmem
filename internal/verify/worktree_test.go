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

// uncommittedFile writes a file into the repository without adding it to the
// index: the residue an agent leaves behind when it forgets to commit, and the
// one thing the report cannot show because it only speaks about commits.
func uncommittedFile(t *testing.T, repo, name, content string) {
	t.Helper()
	path := filepath.Join(repo, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

// A report is a statement about commits, and the verifier sees only commits. The
// working tree is what it does not see: residue there is work the report claims
// and the commit does not contain, so the run is refuted and the files are named.
func TestADirtyWorktreeRefutesTheRun(t *testing.T) {
	repo, commit := makeRepo(t, "dirtyworktree")
	uncommittedFile(t, repo, "leftover.go", "package leftover\n")

	report, err := Verify(context.Background(), Request{
		Repo:    repo,
		Report:  "[COMMIT: " + commit + "]\n",
		TmpDir:  filepath.Join(fixtureTmpRoot(t), "dirtyworktree"),
		Sandbox: engine.SandboxOff,
	})
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}

	if report.Decision.Accept {
		t.Fatal("a report was accepted although the working tree holds uncommitted files")
	}
	if report.Decision.Kind != "refuted" {
		t.Fatalf("kind = %q (%s), want refuted", report.Decision.Kind, report.Decision.Reason)
	}
	if !strings.Contains(report.Decision.Reason, "leftover.go") {
		t.Errorf("reason = %q, want it to name the file", report.Decision.Reason)
	}

	var criterion *Verdict
	for index := range report.Verdicts {
		if report.Verdicts[index].Kind == "worktree_clean" {
			criterion = &report.Verdicts[index]
		}
	}
	if criterion == nil {
		t.Fatalf("no worktree_clean verdict in %+v", report.Verdicts)
	}
	if criterion.Status != StatusRefuted {
		t.Errorf("status = %s (%s), want refuted", criterion.Status, criterion.Reason)
	}
	if criterion.Class != "" {
		t.Errorf("class = %q, want none: a refutation carries no class", criterion.Class)
	}
	if !strings.Contains(criterion.Reason, "leftover.go") {
		t.Errorf("reason = %q, want the file list", criterion.Reason)
	}
}

// The clean case is the ordinary one and must stay accepted: a criterion that
// only rejects would block every honest run.
func TestACleanWorktreeIsAccepted(t *testing.T) {
	repo, commit := makeRepo(t, "cleanworktree")

	report, err := Verify(context.Background(), Request{
		Repo:    repo,
		Report:  "[COMMIT: " + commit + "]\n",
		TmpDir:  filepath.Join(fixtureTmpRoot(t), "cleanworktree"),
		Sandbox: engine.SandboxOff,
	})
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !report.Decision.Accept {
		t.Fatalf("Decision = %s: %s", report.Decision.Kind, report.Decision.Reason)
	}

	var criterion *Verdict
	for index := range report.Verdicts {
		if report.Verdicts[index].Kind == "worktree_clean" {
			criterion = &report.Verdicts[index]
		}
	}
	if criterion == nil || criterion.Status != StatusConfirmed {
		t.Fatalf("worktree_clean verdict = %+v, want one confirmation", criterion)
	}
}

// Ignored files are the project's statement that they are not its history, and
// .yesmem is the ordinary case: the profile, the temp space and the artifacts
// live there. They must not read as residue.
func TestIgnoredFilesDoNotDirtyTheWorktree(t *testing.T) {
	repo, commit := makeRepoFiles(t, "ignoredworktree", map[string]string{
		".gitignore": ".yesmem/\n",
		"file.txt":   "x\n",
	})
	uncommittedFile(t, repo, ".yesmem/tmp/probe", "probe\n")

	report, err := Verify(context.Background(), Request{
		Repo:    repo,
		Report:  "[COMMIT: " + commit + "]\n",
		TmpDir:  filepath.Join(fixtureTmpRoot(t), "ignoredworktree"),
		Sandbox: engine.SandboxOff,
	})
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !report.Decision.Accept {
		t.Fatalf("Decision = %s: %s, want accepted for an ignored file",
			report.Decision.Kind, report.Decision.Reason)
	}
}

// The worktree criterion is the verifier's own finding. It must not count as the
// confirmation a report needs: a report that says nothing beyond a tolerated
// deploy still proves nothing, whatever the tree looks like.
func TestTheWorktreeCriterionDoesNotConfirmAReport(t *testing.T) {
	repo, _ := makeRepo(t, "nocontribution")

	report, err := Verify(context.Background(), Request{
		Repo:    repo,
		Report:  "[DEPLOY: no]\n",
		TmpDir:  filepath.Join(fixtureTmpRoot(t), "nocontribution"),
		Sandbox: engine.SandboxOff,
		Policy:  YesloopDonePolicy(),
	})
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if report.Decision.Accept {
		t.Fatal("a report with no claim of its own was accepted on the verifier's criterion")
	}
	if report.Decision.Kind != "nothing-confirmed" {
		t.Errorf("kind = %q (%s), want nothing-confirmed", report.Decision.Kind, report.Decision.Reason)
	}
	// The exit code is the second reader of the same run: it must not call a
	// report proven because the verifier's own criterion confirmed.
	if report.ExitCode != ExitNothing {
		t.Errorf("exit = %d, want %d (nothing confirmed about the report)", report.ExitCode, ExitNothing)
	}
}

// A bare repository is a documented target: a push is judged against a mirror. It
// has no working tree, so there is no residue and the criterion is the documented
// no-op instead of an environment gap.
func TestABareRepositoryHasNoWorktreeToJudge(t *testing.T) {
	repo, commit := makeRepoFiles(t, "bareworktree", map[string]string{"file.txt": "x\n"})
	bare := filepath.Join(t.TempDir(), "mirror.git")
	if out, err := exec.Command("git", "clone", "-q", "--bare", repo, bare).CombinedOutput(); err != nil {
		t.Fatalf("clone --bare: %v: %s", err, out)
	}

	report, err := Verify(context.Background(), Request{
		Repo:    bare,
		Report:  "[COMMIT: " + commit + "]\n",
		TmpDir:  filepath.Join(fixtureTmpRoot(t), "bareworktree"),
		Sandbox: engine.SandboxOff,
	})
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !report.Decision.Accept {
		t.Fatalf("Decision = %s: %s, want accepted for a bare mirror",
			report.Decision.Kind, report.Decision.Reason)
	}
	for _, verdict := range report.Verdicts {
		if verdict.Kind != "worktree_clean" {
			continue
		}
		if verdict.Status != StatusConfirmed {
			t.Fatalf("status = %s (%s), want a confirmation: a bare repo has no working tree",
				verdict.Status, verdict.Reason)
		}
	}
}

// The repository's own config must not be able to hide untracked work from the
// criterion: showUntrackedFiles is a local setting, and the party under
// judgement can write it.
func TestALocalStatusConfigCannotHideResidue(t *testing.T) {
	repo, commit := makeRepo(t, "hiddenresidue")
	if out, err := exec.Command("git", "-C", repo, "config", "status.showUntrackedFiles", "no").CombinedOutput(); err != nil {
		t.Fatalf("config: %v: %s", err, out)
	}
	uncommittedFile(t, repo, "leftover.go", "package leftover\n")

	report, err := Verify(context.Background(), Request{
		Repo:    repo,
		Report:  "[COMMIT: " + commit + "]\n",
		TmpDir:  filepath.Join(fixtureTmpRoot(t), "hiddenresidue"),
		Sandbox: engine.SandboxOff,
	})
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if report.Decision.Accept {
		t.Fatal("the run was accepted although the repository config hid an untracked file")
	}
	if report.Decision.Kind != "refuted" {
		t.Fatalf("kind = %q (%s), want refuted", report.Decision.Kind, report.Decision.Reason)
	}
	if !strings.Contains(report.Decision.Reason, "leftover.go") {
		t.Errorf("reason = %q, want the file list", report.Decision.Reason)
	}
}
