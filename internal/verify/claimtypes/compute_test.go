package claimtypes

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/carsteneu/yesmem/internal/verify/engine"
)

func digestOf(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

// The [COMPUTE] parser: the last arrow separates the command from the digest,
// and an arrow-less marker names no claim.
func TestMatchCompute(t *testing.T) {
	claims := MatchCompute("[COMPUTE: python3 -m x a -> b -> " + digestOf("x") + "]")
	if len(claims) != 1 {
		t.Fatalf("claims = %d, want 1", len(claims))
	}
	if claims[0].Command != "python3 -m x a -> b" {
		t.Errorf("command = %q, want the part before the last arrow", claims[0].Command)
	}
	if claims[0].SHA256 != digestOf("x") {
		t.Errorf("digest = %q", claims[0].SHA256)
	}

	if got := MatchCompute("[COMPUTE: no arrow here]"); len(got) != 0 {
		t.Errorf("claims = %d, want none for an arrow-less marker", len(got))
	}
	if got := MatchCompute("[COMPUTE: cmd \u2192 " + digestOf("x") + "]"); len(got) != 1 {
		t.Errorf("claims = %d, want 1 for the unicode arrow", len(got))
	}
}

// A shim in the operator's home must not decide a COMPUTE verdict: the digest
// covers the commit's bytes, not whatever `python3` happens to be first.
func TestCheckComputeIgnoresAShimFromTheHomeDirectory(t *testing.T) {
	homeParent := filepath.Join("..", "..", ".yesmem", "tmp", "computehomes")
	if err := os.MkdirAll(homeParent, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	home, err := os.MkdirTemp(homeParent, "home-")
	if err != nil {
		t.Fatalf("mkdtemp: %v", err)
	}
	stubDir := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(stubDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// The shim prints exactly the bytes the report will claim, so a run that
	// picks it up would confirm.
	shim := "#!/bin/sh\necho SHIM-EXECUTED\nexit 0\n"
	if err := os.WriteFile(filepath.Join(stubDir, "python3"), []byte(shim), 0o755); err != nil {
		t.Fatalf("write shim: %v", err)
	}

	repo, commit := makeClaimRepo(t, "computeshim", map[string]string{
		"genuine.py": "print(\"GENUINE\")\n",
	})
	t.Setenv("HOME", home)
	t.Setenv("PATH", stubDir+string(os.PathListSeparator)+"/usr/local/bin:/usr/bin:/bin")

	verdict := CheckCompute(engine.Claim{
		Kind:    "compute",
		Command: "python3 genuine.py",
		SHA256:  digestOf("SHIM-EXECUTED\n"),
		Commit:  commit,
	}, unsandboxedCtx(t, repo))

	if verdict.Status == engine.StatusConfirmed {
		t.Fatalf("the shim decided the verdict: %s", verdict.Reason)
	}
}

// An artifact digest of a file the claim names that is not in the commit is a
// refutation, not a confirmation.
func TestCheckArtifactRefutesAMissingFile(t *testing.T) {
	repo, _ := makeClaimRepo(t, "artifactmissing", map[string]string{"present.txt": "x\n"})

	verdict := CheckArtifact(engine.Claim{
		Kind:   "artifact",
		Path:   "absent.txt",
		SHA256: digestOf("x\n"),
	}, unsandboxedCtx(t, repo))

	if verdict.Status != engine.StatusRefuted {
		t.Fatalf("status = %s (%s), want refuted", verdict.Status, verdict.Reason)
	}
}
