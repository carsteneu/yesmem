package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A tool bin directory below the home may be mounted: that is where a toolchain
// installed for one account lives (bun, cargo, nvm). The mount is a real
// subdirectory and is named as such, so the rest of the home stays covered.
func TestSandboxMountsAToolBinDirectoryBelowTheHome(t *testing.T) {
	root := sandboxTestRoot(t)
	home := filepath.Join(root, "home")
	bin := filepath.Join(home, ".fixture", "bin")
	writeTool(t, filepath.Join(bin, "fixture-bun"), "#!/bin/sh\nexit 0\n")

	ctx := &Ctx{Languages: toolProfile("sh", nil, []string{bin})}
	mounts, caches, _, abort := ctx.SandboxExtras(root, "sh")

	if abort != nil {
		t.Fatalf("the tool bin directory was refused: %s", abort.Reason)
	}
	if len(mounts) != 1 || mounts[0] != [2]string{bin, bin} {
		t.Fatalf("mounts = %v, want the tool bin directory", mountPaths(mounts))
	}
	if len(caches) != 0 {
		t.Fatalf("caches = %v, want no writable mount", mountPaths(caches))
	}
}

// The shield is the point of the home tmpfs, so mounting a subtree must leave
// everything around it closed. A credential directory that happens to sit beside
// the mounted bin directory must stay unreadable.
func TestSandboxMountKeepsTheRestOfTheHomeMasked(t *testing.T) {
	root := sandboxTestRoot(t)
	bwrap := requireSandbox(t, root)
	runDir := filepath.Join(root, "run")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatalf("mkdir run: %v", err)
	}
	home := filepath.Join(root, "home")
	bin := filepath.Join(home, ".fixture", "bin")
	writeTool(t, filepath.Join(bin, "fixture-bun"), "#!/bin/sh\nprintf 'fixture-1.2.3\\n'\n")
	secret := filepath.Join(home, ".ssh", "id_rsa")
	writeTool(t, secret, "SECRET-HOST-KEY\n")

	ctx := &Ctx{Languages: toolProfile("sh", nil, []string{bin})}
	probe := "cat " + secret + " 2>/dev/null || echo MASKED"
	outcome, verdict := ctx.runCommand(SandboxRequire, bwrap, runDir, "<checkout>",
		"sh -c "+probe, []string{"sh", "-c", probe}, 0)

	if verdict != nil {
		t.Fatalf("the run was refused: %s", verdict.Reason)
	}
	if strings.Contains(outcome.RawOutput, "SECRET-HOST-KEY") {
		t.Fatalf("output = %q, the credential file below the home was readable", outcome.RawOutput)
	}
	if !strings.Contains(outcome.RawOutput, "MASKED") {
		t.Fatalf("output = %q, want the masked home", outcome.RawOutput)
	}
}

// Only a tool bin directory earns the exception. Any other subtree of the home
// is what the shield exists for, and a profile that names one is a defect.
func TestSandboxMountRefusesAnOrdinaryHomeSubdirectory(t *testing.T) {
	root := sandboxTestRoot(t)
	home := filepath.Join(root, "home")

	cases := []struct {
		name string
		dir  string
	}{
		{name: "a plain directory", dir: filepath.Join(home, "documents")},
		{name: "a cache directory", dir: filepath.Join(home, ".cache")},
		{name: "a credential directory", dir: filepath.Join(home, ".ssh", "keys")},
	}
	for _, entry := range cases {
		t.Run(entry.name, func(t *testing.T) {
			if err := os.MkdirAll(entry.dir, 0o755); err != nil {
				t.Fatalf("mkdir %s: %v", entry.dir, err)
			}
			ctx := &Ctx{Languages: toolProfile("sh", nil, []string{entry.dir})}
			mounts, _, _, abort := ctx.SandboxExtras(root, "sh")
			if abort == nil {
				t.Fatalf("the mount %q was accepted: %v", entry.dir, mountPaths(mounts))
			}
			if abort.Class != ClassDefect {
				t.Fatalf("class = %q (%s), want %q", abort.Class, abort.Reason, ClassDefect)
			}
		})
	}
}

// The mount must be one absolute host path. A composed path is refused for the
// same reason a relative one is: what the guard judges must be what bwrap binds.
func TestSandboxMountRefusesAMultiPathEntry(t *testing.T) {
	root := sandboxTestRoot(t)
	home := filepath.Join(root, "home")
	bin := filepath.Join(home, ".fixture", "bin")
	writeTool(t, filepath.Join(bin, "fixture-bun"), "#!/bin/sh\nexit 0\n")

	for _, entry := range []string{
		bin + ":" + filepath.Join(home, ".ssh"),
		bin + "/../..",
	} {
		t.Run(entry, func(t *testing.T) {
			ctx := &Ctx{Languages: toolProfile("sh", nil, []string{entry})}
			_, _, _, abort := ctx.SandboxExtras(root, "sh")
			if abort == nil {
				t.Fatalf("the mount %q was accepted", entry)
			}
		})
	}
}

// A mounted path that lies inside the verified repository is repository
// content, not a host tree: the run would read the working tree the operator
// edits, not the checkout under judgement.
func TestSandboxMountRefusesAPathInsideTheVerifiedRepo(t *testing.T) {
	root := sandboxTestRoot(t)
	repo := filepath.Join(root, "project")
	inside := filepath.Join(repo, "tools", "bin")
	writeTool(t, filepath.Join(inside, "fixture-bun"), "#!/bin/sh\nexit 0\n")

	ctx := &Ctx{Repo: repo, Languages: toolProfile("sh", nil, []string{inside})}
	_, _, _, abort := ctx.SandboxExtras(root, "sh")
	if abort == nil {
		t.Fatal("a mount inside the verified repository was accepted")
	}
	if abort.Class != ClassDefect {
		t.Fatalf("class = %q (%s), want %q", abort.Class, abort.Reason, ClassDefect)
	}
}
