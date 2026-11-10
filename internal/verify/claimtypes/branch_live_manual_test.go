package claimtypes

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/carsteneu/yesmem/internal/verify/engine"
)

// Manual measurement, not part of the suite: it needs the real remote over ssh.
// Run with YESMEM_LIVE_BRANCH=1 go test ./internal/verify/claimtypes/ -run TestLiveBranchPushed -v
func liveBranchCtx(t *testing.T) (repo, commit string, ctx *engine.Ctx) {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatalf("cannot locate the repository root: %v", err)
	}
	cmd := exec.Command("git", "-C", root, "rev-parse", "origin/main")
	cmd.Env = []string{"HOME=" + root, "GIT_CONFIG_NOSYSTEM=1", "PATH=" + os.Getenv("PATH")}
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("cannot resolve origin/main in %s: %v", root, err)
	}
	return root, strings.TrimSpace(string(output)), &engine.Ctx{
		Repo:    root,
		TmpDir:  filepath.Join(root, ".yesmem", "tmp", "live"),
		Sandbox: engine.SandboxOff,
	}
}

func TestLiveBranchPushedAgainstOrigin(t *testing.T) {
	if os.Getenv("YESMEM_LIVE_BRANCH") == "" {
		t.Skip("YESMEM_LIVE_BRANCH not set: the measurement needs the real remote over ssh")
	}
	_, commit, ctx := liveBranchCtx(t)
	start := time.Now()
	verdict := CheckBranchPushed(engine.Claim{Kind: "branch_pushed", Branch: "main", Commit: commit}, ctx)
	t.Logf("elapsed=%s status=%s class=%s reason=%s", time.Since(start).Round(time.Millisecond), verdict.Status, verdict.Class, verdict.Reason)
	if verdict.Status != engine.StatusConfirmed {
		t.Fatalf("expected CONFIRMED, got %s: %s (output: %s)", verdict.Status, verdict.Reason, verdict.Output)
	}
}

// Same manual measurement for the other side of the split: the real remote
// answers, and it does not carry that branch.
func TestLiveBranchPushedRefutesMissingRemoteBranch(t *testing.T) {
	if os.Getenv("YESMEM_LIVE_BRANCH") == "" {
		t.Skip("YESMEM_LIVE_BRANCH not set: the measurement needs the real remote over ssh")
	}
	_, commit, ctx := liveBranchCtx(t)
	start := time.Now()
	verdict := CheckBranchPushed(engine.Claim{Kind: "branch_pushed", Branch: "yesloop/no-such-branch-xyz", Commit: commit}, ctx)
	t.Logf("elapsed=%s status=%s class=%s reason=%s", time.Since(start).Round(time.Millisecond), verdict.Status, verdict.Class, verdict.Reason)
	if verdict.Status != engine.StatusRefuted {
		t.Fatalf("expected REFUTED, got %s: %s (output: %s)", verdict.Status, verdict.Reason, verdict.Output)
	}
}
