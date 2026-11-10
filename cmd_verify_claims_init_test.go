package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/carsteneu/yesmem/internal/verify"
)

// cliRepo is a throwaway project root under the worktree's own temp space, with
// HOME pointed inside it: the data directory derives from HOME, and a test must
// not write into the operator's.
func cliRepo(t *testing.T, name string) (repo, home string) {
	t.Helper()
	parent, err := filepath.Abs(filepath.Join(".yesmem", "tmp", "initcli"))
	if err != nil {
		t.Fatalf("abs: %v", err)
	}
	if err := os.MkdirAll(parent, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	root, err := os.MkdirTemp(parent, name+"-")
	if err != nil {
		t.Fatalf("mkdtemp: %v", err)
	}
	home = filepath.Join(root, "home")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatalf("mkdir home: %v", err)
	}
	t.Setenv("HOME", home)
	return root, home
}

func TestVerifyInitIsAFlag(t *testing.T) {
	flags, err := parseVerifyClaimsFlags([]string{"--init", "--repo", "/somewhere"}, io.Discard)
	if err != nil {
		t.Fatalf("parseVerifyClaimsFlags: %v", err)
	}
	if flags == nil || !flags.init {
		t.Fatal("--init was not parsed into the flag set")
	}
}

// Nothing recognised is reported and nothing is written: a project without a
// proven command keeps no profile, and the reader gets the file to copy.
func TestVerifyInitReportsAnUnrecognizedStack(t *testing.T) {
	repo, _ := cliRepo(t, "unrecognized")
	var stdout, stderr bytes.Buffer

	if code := runVerifyInit(repo, &stdout, &stderr); code != 1 {
		t.Fatalf("exit = %d, want 1 (nothing written)\nstdout: %s\nstderr: %s",
			code, stdout.String(), stderr.String())
	}
	for _, want := range []string{"nothing was detected", ".yesmem/verify.yaml", "go.mod", "languages:"} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("stdout %q does not say %q", stdout.String(), want)
		}
	}
	if !strings.Contains(stderr.String(), "nothing written") {
		t.Errorf("stderr = %q, want it to say that nothing was written", stderr.String())
	}
	if _, err := os.Stat(filepath.Join(repo, ".yesmem", "verify.yaml")); !os.IsNotExist(err) {
		t.Fatal("a profile was written for an unrecognised stack")
	}
}

// An existing profile is not replaced, and the exit code says the run did not
// do what it was asked to do: nothing was written.
func TestVerifyInitReportsAnExistingProfile(t *testing.T) {
	repo, _ := cliRepo(t, "existing")
	path := filepath.Join(repo, ".yesmem", "verify.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte("languages:\n  - name: go\n    test_command: go test ./...\n"), 0o644); err != nil {
		t.Fatalf("write profile: %v", err)
	}
	var stdout, stderr bytes.Buffer

	if code := runVerifyInit(repo, &stdout, &stderr); code != 1 {
		t.Fatalf("exit = %d, want 1 (nothing written)\nstdout: %s\nstderr: %s",
			code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "already exists") {
		t.Errorf("stderr = %q, want it to name the existing file", stderr.String())
	}
}

// The init mode writes a profile; it judges no report. A caller that passes
// report flags has misunderstood the mode, and silently ignoring them would
// hand back human text where JSON was asked for.
func TestVerifyInitRefusesTheReportFlags(t *testing.T) {
	plain, err := parseVerifyClaimsFlags([]string{"--init"}, io.Discard)
	if err != nil || plain == nil {
		t.Fatalf("parse: %v", err)
	}
	if err := verifyInitConflict(plain); err != nil {
		t.Fatalf("--init alone was refused: %v", err)
	}
	for _, args := range [][]string{
		{"--init", "--report", "-"},
		{"--init", "--project", "p", "--section", "s"},
		{"--init", "--json"},
		{"--init", "--strict"},
		{"--init", "--budget", "1m"},
		{"--init", "--profile", "yesloop-done"},
		{"--init", "--policy", "yesloop-done"},
	} {
		flags, parseErr := parseVerifyClaimsFlags(args, io.Discard)
		if parseErr != nil || flags == nil {
			t.Fatalf("parse %v: %v", args, parseErr)
		}
		if err := verifyInitConflict(flags); err == nil {
			t.Errorf("%v: accepted, want a usage error", args)
		}
	}
}

// A failed proof has to say what failed, not just that it failed: the run's own
// last lines are the only thing that tells a red suite from a broken sandbox.
func TestVerifyInitReportsAFailedProof(t *testing.T) {
	var stdout, stderr bytes.Buffer
	result := verify.InitResult{
		Detected: []verify.InitDetection{{Name: "go", Manifest: "go.mod"}},
		Proven: &verify.InitProof{
			Command:   "go test ./...",
			Exit:      1,
			Sandboxed: true,
			Output:    "--- FAIL: TestThing (0.00s)\n    thing_test.go:12: boom\nFAIL\n",
		},
		Notes: []string{"the proof run of \"go test ./...\" exited 1; nothing was written."},
	}

	if code := reportVerifyInit(result, &stdout, &stderr); code != 1 {
		t.Fatalf("exit = %d, want 1\nstdout: %s", code, stdout.String())
	}
	for _, want := range []string{"detected: go (go.mod)", "proof failed: go test ./... -> exit 1 (sandboxed)", "--- FAIL: TestThing", "thing_test.go:12: boom"} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("stdout %q does not carry %q", stdout.String(), want)
		}
	}
	if !strings.Contains(stderr.String(), "nothing written") {
		t.Errorf("stderr = %q", stderr.String())
	}
}

// The proof log is bounded from the end: a long suite log must not bury the line
// that says why it failed.
func TestProofTailKeepsTheEndAndIsBounded(t *testing.T) {
	var lines []string
	for index := 1; index <= 30; index++ {
		lines = append(lines, fmt.Sprintf("line %d", index))
	}
	tail := proofTail(strings.Join(lines, "\n"))

	if got := strings.Count(tail, "\n"); got != 20 {
		t.Errorf("tail carries %d lines, want 20", got)
	}
	if strings.Contains(tail, "line 10") {
		t.Error("tail carries a line older than the bound")
	}
	if !strings.Contains(tail, "line 30") || !strings.Contains(tail, "line 11") {
		t.Errorf("tail does not cover the end: %q", tail)
	}
	if proofTail("") != "" || proofTail("\n\n") != "" {
		t.Error("an empty output produced a tail")
	}
}
