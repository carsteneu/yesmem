package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// maskedBinDir is a directory the sandbox covers but the run's PATH keeps: it
// sits under /home, which the sandbox masks as a whole, and beside the fixture
// home, so RunPath does not filter it. That is the shape the real defect has: a
// toolchain prepends its own bin directory (Go does), the directory lives below
// the operator's home, and the exec dies against the mask.
func maskedBinDir(t *testing.T, root string) string {
	t.Helper()
	if !strings.HasPrefix(root, "/home"+string(os.PathSeparator)) {
		// The mask this fixture needs is the /home one: a checkout outside /home
		// keeps its siblings visible, so the case cannot be built here.
		t.Skip("the fixture root is not below /home, so no masked sibling can be created")
	}
	dir := filepath.Join(root, "sibling", "bin")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	return dir
}

// A candidate behind the mask cannot be executed, so the lookup takes the next
// one. Choosing the masked candidate turned the run into a failing suite, which
// refutes an honest report for a boundary of the machine.
func TestSandboxSkipsAProgramTheMaskCovers(t *testing.T) {
	root := sandboxTestRoot(t)
	runDir := sandboxRunDir(t, root)
	bwrap := requireSandbox(t, runDir)
	masked := maskedBinDir(t, root)
	writeTool(t, filepath.Join(masked, "fixturetool"), "#!/bin/sh\nprintf 'masked\\n'\n")
	visible := filepath.Join(runDir, "vendor-bin")
	writeTool(t, filepath.Join(visible, "fixturetool"), "#!/bin/sh\nprintf 'visible\\n'\n")
	t.Setenv("PATH", masked+string(os.PathListSeparator)+visible+
		string(os.PathListSeparator)+os.Getenv("PATH"))

	ctx := &Ctx{Languages: toolProfile("fixturetool", nil, nil)}
	outcome, verdict := ctx.runCommand(SandboxRequire, bwrap, runDir, "<checkout>",
		"fixturetool test", []string{"fixturetool", "test"}, 0)

	if verdict != nil {
		t.Fatalf("the run was refused: %s", verdict.Reason)
	}
	if outcome.Code != 0 {
		t.Fatalf("exit = %d, want 0: %s", outcome.Code, outcome.RawOutput)
	}
	if !strings.Contains(outcome.RawOutput, "visible") {
		t.Fatalf("output = %q, want the candidate the sandbox can execute", outcome.RawOutput)
	}
}

// A program that exists only behind the mask is the machine's gap: the verdict
// names the program and the path, and it stays environment. Never a refutation,
// and never a silent fallback to an unsandboxed run.
func TestSandboxAProgramOnlyBehindTheMaskIsAnEnvironmentGap(t *testing.T) {
	root := sandboxTestRoot(t)
	runDir := sandboxRunDir(t, root)
	bwrap := requireSandbox(t, runDir)
	masked := maskedBinDir(t, root)
	writeTool(t, filepath.Join(masked, "fixturetool"), "#!/bin/sh\nexit 0\n")
	t.Setenv("PATH", masked+string(os.PathListSeparator)+os.Getenv("PATH"))

	ctx := &Ctx{Languages: toolProfile("fixturetool", nil, nil)}
	_, verdict := ctx.runCommand(SandboxRequire, bwrap, runDir, "<checkout>",
		"fixturetool test", []string{"fixturetool", "test"}, 0)

	if verdict == nil {
		t.Fatal("the run started although its program is invisible in the sandbox")
	}
	if verdict.Status != StatusUnverifiable || verdict.Class != ClassEnvironment {
		t.Fatalf("status = %s class = %q (%s), want an environment gap",
			verdict.Status, verdict.Class, verdict.Reason)
	}
	for _, want := range []string{"fixturetool", filepath.Join(masked, "fixturetool")} {
		if !strings.Contains(verdict.Reason, want) {
			t.Errorf("reason = %q, want %q named", verdict.Reason, want)
		}
	}
}

// A program behind the mask is reachable again when the profile mounts its
// directory: the mount is the profile's statement that the run needs this
// toolchain.
func TestSandboxAMaskedProgramIsReachableThroughItsMount(t *testing.T) {
	root := sandboxTestRoot(t)
	runDir := sandboxRunDir(t, root)
	bwrap := requireSandbox(t, runDir)
	masked := maskedBinDir(t, root)
	writeTool(t, filepath.Join(masked, "fixturetool"), "#!/bin/sh\nprintf 'mounted\\n'\n")
	t.Setenv("PATH", masked+string(os.PathListSeparator)+os.Getenv("PATH"))

	ctx := &Ctx{Languages: toolProfile("fixturetool", nil, []string{masked})}
	outcome, verdict := ctx.runCommand(SandboxRequire, bwrap, runDir, "<checkout>",
		"fixturetool test", []string{"fixturetool", "test"}, 0)

	if verdict != nil {
		t.Fatalf("the run was refused: %s", verdict.Reason)
	}
	if outcome.Code != 0 {
		t.Fatalf("exit = %d, want 0: %s", outcome.Code, outcome.RawOutput)
	}
	if !strings.Contains(outcome.RawOutput, "mounted") {
		t.Fatalf("output = %q, want the mounted candidate", outcome.RawOutput)
	}
}
