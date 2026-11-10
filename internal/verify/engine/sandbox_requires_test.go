package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// toolProfile declares a suite command and, beside it, the tools the run must
// find and the host trees it may read. That is the shape a project profile has:
// the command ties the run to the entry, the declarations describe its
// toolchain.
func toolProfile(program string, requires, readOnly []string) []Language {
	return []Language{{
		Name:            "fixture",
		TestCommand:     program + " test",
		Requires:        requires,
		SandboxReadOnly: readOnly,
	}}
}

// requireSandbox skips when the host cannot confine a run at all, naming the
// missing prerequisite.
func requireSandbox(t *testing.T, checkout string) string {
	t.Helper()
	bwrap := FindBwrap()
	if bwrap == "" {
		t.Skip("bubblewrap is not available")
	}
	if err := SandboxProbe(bwrap, checkout); err != nil {
		t.Skipf("the host cannot start a sandbox: %v", err)
	}
	return bwrap
}

// sandboxRunDir is a run directory beside the fixture home, never above it: the
// checkout is bound writable, so a run directory that contained the home would
// re-expose exactly the tree the shield covers.
func sandboxRunDir(t *testing.T, root string) string {
	t.Helper()
	runDir := filepath.Join(root, "run")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatalf("mkdir run: %v", err)
	}
	return runDir
}

// writeTool plants a program in a directory and makes it executable.
func writeTool(t *testing.T, path, script string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// withPath puts dir in front of the PATH the operator's tool lookup uses.
func withPath(t *testing.T, dir string) {
	t.Helper()
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// A tool the profile requires and the host does not have is a gap of the
// machine, not a fault of the report: the run never starts, and the verdict says
// environment instead of blaming a suite that could not run.
func TestSandboxRequiredToolAbsentIsAnEnvironmentGap(t *testing.T) {
	runDir := sandboxRunDir(t, sandboxTestRoot(t))
	bwrap := requireSandbox(t, runDir)
	ctx := &Ctx{Languages: toolProfile("sh", []string{"fixture-absent-tool"}, nil)}

	_, verdict := ctx.runCommand(SandboxRequire, bwrap, runDir, "<checkout>",
		"sh -c true", []string{"sh", "-c", "true"}, 0)

	if verdict == nil {
		t.Fatal("the run started although a required tool is missing")
	}
	if verdict.Status != StatusUnverifiable || verdict.Class != ClassEnvironment {
		t.Fatalf("status = %s class = %q (%s), want an environment gap",
			verdict.Status, verdict.Class, verdict.Reason)
	}
	if !strings.Contains(verdict.Reason, "fixture-absent-tool") {
		t.Fatalf("reason = %q, want the missing tool named", verdict.Reason)
	}
}

// The interesting case is a tool that exists on the host but not in the sandbox:
// it lives below the home, and the home is covered by an empty tmpfs. The
// verdict must name the tool and the path, and it must stay environment: the
// report is not wrong, the confinement is.
func TestSandboxRequiredToolBelowTheHomeIsAnEnvironmentGap(t *testing.T) {
	root := sandboxTestRoot(t)
	runDir := sandboxRunDir(t, root)
	bwrap := requireSandbox(t, runDir)
	home := filepath.Join(root, "home")
	bin := filepath.Join(home, ".fixture", "bin")
	writeTool(t, filepath.Join(bin, "fixture-bun"), "#!/bin/sh\nprintf 'fixture-1.2.3\\n'\n")
	withPath(t, bin)

	ctx := &Ctx{Languages: toolProfile("sh", []string{"fixture-bun"}, nil)}
	_, verdict := ctx.runCommand(SandboxRequire, bwrap, runDir, "<checkout>",
		"sh -c fixture-bun --version", []string{"sh", "-c", "fixture-bun --version"}, 0)

	if verdict == nil {
		t.Fatal("the run started although the required tool is invisible in the sandbox")
	}
	if verdict.Status != StatusUnverifiable || verdict.Class != ClassEnvironment {
		t.Fatalf("status = %s class = %q (%s), want an environment gap",
			verdict.Status, verdict.Class, verdict.Reason)
	}
	for _, want := range []string{"fixture-bun", filepath.Join(bin, "fixture-bun")} {
		if !strings.Contains(verdict.Reason, want) {
			t.Errorf("reason = %q, want %q named", verdict.Reason, want)
		}
	}
}

// With the tool bin directory mounted, the same run finds its tool: the mount
// and the PATH the verifier adds are one decision, because a bound directory the
// child cannot look into is a mount that satisfies nobody.
func TestSandboxRequiredToolInAMountedBinDirectoryIsFound(t *testing.T) {
	root := sandboxTestRoot(t)
	runDir := sandboxRunDir(t, root)
	bwrap := requireSandbox(t, runDir)
	home := filepath.Join(root, "home")
	bin := filepath.Join(home, ".fixture", "bin")
	writeTool(t, filepath.Join(bin, "fixture-bun"), "#!/bin/sh\nprintf 'fixture-1.2.3\\n'\n")
	withPath(t, bin)

	ctx := &Ctx{Languages: toolProfile("sh", []string{"fixture-bun"}, []string{bin})}
	outcome, verdict := ctx.runCommand(SandboxRequire, bwrap, runDir, "<checkout>",
		"sh -c fixture-bun --version", []string{"sh", "-c", "fixture-bun --version"}, 0)

	if verdict != nil {
		t.Fatalf("the run was refused: %s", verdict.Reason)
	}
	if outcome.Code != 0 {
		t.Fatalf("exit = %d, want 0: %s", outcome.Code, outcome.RawOutput)
	}
	if !strings.Contains(outcome.RawOutput, "fixture-1.2.3") {
		t.Fatalf("output = %q, want the tool's own answer", outcome.RawOutput)
	}
	if !outcome.Sandboxed {
		t.Fatal("the run was not sandboxed")
	}
	// The verdict's command line is the audit trail: it has to show the mount
	// the run was given.
	if !strings.Contains(outcome.CommandDesc, "<home>/.fixture/bin") {
		t.Fatalf("command = %q, want the mounted bin directory in the sandbox line", outcome.CommandDesc)
	}
}

// Nothing declared means nothing changes: the same suite in the same sandbox
// runs and fails on its own, exactly as before this profile field existed. A
// check that fired without a declaration would be a silent change of every
// existing verdict.
func TestSandboxWithoutRequiresLeavesTheRunUnchecked(t *testing.T) {
	root := sandboxTestRoot(t)
	runDir := sandboxRunDir(t, root)
	bwrap := requireSandbox(t, runDir)
	home := filepath.Join(root, "home")
	bin := filepath.Join(home, ".fixture", "bin")
	writeTool(t, filepath.Join(bin, "fixture-bun"), "#!/bin/sh\nprintf 'fixture-1.2.3\\n'\n")
	withPath(t, bin)

	ctx := &Ctx{Languages: toolProfile("sh", nil, nil)}
	outcome, verdict := ctx.runCommand(SandboxRequire, bwrap, runDir, "<checkout>",
		"sh -c fixture-bun --version", []string{"sh", "-c", "fixture-bun --version"}, 0)

	if verdict != nil {
		t.Fatalf("an undeclared profile was refused: %s", verdict.Reason)
	}
	if outcome.Code == 0 {
		t.Fatalf("the run found a tool the sandbox masks: %q", outcome.RawOutput)
	}
}

// The PATH the verifier adds must never put the mounted directory ahead of the
// system paths: a binary named go in a tool bin directory would then decide
// which compiler the run uses.
func TestSandboxRequiredToolPathIsAppendedNotPrepended(t *testing.T) {
	root := sandboxTestRoot(t)
	runDir := sandboxRunDir(t, root)
	bwrap := requireSandbox(t, runDir)
	home := filepath.Join(root, "home")
	bin := filepath.Join(home, ".fixture", "bin")
	writeTool(t, filepath.Join(bin, "go"), "#!/bin/sh\nprintf 'shadowed\\n'\n")
	withPath(t, bin)

	ctx := &Ctx{Languages: toolProfile("sh", []string{"go"}, []string{bin})}
	outcome, verdict := ctx.runCommand(SandboxRequire, bwrap, runDir, "<checkout>",
		"sh -c go version", []string{"sh", "-c", "go version"}, 0)

	if verdict != nil {
		t.Fatalf("the run was refused: %s", verdict.Reason)
	}
	if strings.Contains(outcome.RawOutput, "shadowed") {
		t.Fatalf("output = %q, want the system go, not the mounted one", outcome.RawOutput)
	}
}
