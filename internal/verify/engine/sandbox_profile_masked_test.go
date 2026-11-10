package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The sandbox covers the host's temp and runtime trees with an empty tmpfs on
// purpose: a socket or a copied secret there would land in a verdict, and
// --unshare-net separates IP networking, not AF_UNIX pathnames. A profile read
// from repository content must not put those trees back, and a read-only bind
// after the tmpfs would do exactly that.
func TestSandboxMountRefusesAMaskedHostDirectory(t *testing.T) {
	root := sandboxTestRoot(t)
	runDir := filepath.Join(root, "run")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatalf("mkdir run: %v", err)
	}
	t.Setenv("HOME", filepath.Join(root, "home"))

	for _, path := range []string{"/tmp", "/var/tmp", "/run", "/run/user/1000", "/dev", "/proc", "/sys"} {
		t.Run(path, func(t *testing.T) {
			if _, err := os.Stat(path); err != nil {
				t.Skipf("the host has no %s, nothing to mask", path)
				return
			}
			ctx := &Ctx{Languages: profileFor("fixturetool", []string{path}, nil)}
			mounts, _, _, abort := ctx.SandboxExtras(runDir, "fixturetool")
			if abort == nil {
				t.Fatalf("the mount %q was accepted (%v) although the sandbox masks that tree", path, mounts)
			}
			if abort.Class != ClassDefect {
				t.Fatalf("class = %q (%s), want %q", abort.Class, abort.Reason, ClassDefect)
			}
			if !strings.Contains(abort.Reason, "masks") {
				t.Errorf("reason = %q, want it to name the mask", abort.Reason)
			}
		})
	}
}

// A cache variable is not the place to decide which code the child loads: these
// keys hand an interpreter, a runtime or a helper a program from outside the
// checkout, which is the one thing the sandbox exists to prevent.
func TestSandboxEnvRefusesLoaderAndLookupKeys(t *testing.T) {
	for _, key := range []string{
		"PYTHONPATH", "PYTHONSTARTUP", "PYTHONHOME",
		"NODE_OPTIONS", "NODE_PATH",
		"GIT_SSH_COMMAND", "GIT_EXTERNAL_DIFF", "GIT_DIR", "GIT_CONFIG_COUNT",
		"JDK_JAVA_OPTIONS", "PERLLIB", "SSH_ASKPASS_REQUIRE",
		"BASH_ENV", "ENV",
		"PERL5LIB", "RUBYOPT", "CLASSPATH", "JAVA_TOOL_OPTIONS",
		"LD_PRELOAD", "DYLD_INSERT_LIBRARIES", "SSH_AUTH_SOCK", "GCONV_PATH", "LOCPATH",
	} {
		t.Run(key, func(t *testing.T) {
			ctx := &Ctx{Languages: profileFor("fixturetool", nil, map[string]string{key: "/x"})}
			_, _, _, abort := ctx.SandboxExtras(sandboxTestRoot(t), "fixturetool")
			if abort == nil {
				t.Fatalf("%s was accepted as a sandbox variable", key)
			}
			if abort.Class != ClassDefect {
				t.Fatalf("class = %q (%s), want %q", abort.Class, abort.Reason, ClassDefect)
			}
		})
	}
}

// A toolchain-named cache that happens to live in a masked tree is the host's
// layout, not the project's mistake: the run has to stay an environment gap and
// must not be reported as a defect of the profile.
func TestSandboxMountClassifiesAToolchainCacheInAMaskedTreeAsEnvironment(t *testing.T) {
	root := sandboxTestRoot(t)
	runDir := filepath.Join(root, "run")
	binDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	cache := "/tmp/yesmem-fixture-cache-does-not-need-to-exist"
	tool := filepath.Join(binDir, "fixturetool")
	script := "#!/bin/sh\nif [ \"$1\" = env ] && [ \"$2\" = FIXTURE_CACHE ]; then printf '%s\\n' \"$FIXTURE_CACHE_ROOT\"; exit 0; fi\nexit 1\n"
	if err := os.WriteFile(tool, []byte(script), 0o755); err != nil {
		t.Fatalf("write tool: %v", err)
	}
	if err := os.MkdirAll(cache, 0o755); err != nil {
		t.Fatalf("mkdir cache: %v", err)
	}
	t.Setenv("FIXTURE_CACHE_ROOT", cache)

	ctx := &Ctx{Languages: profileFor(tool, []string{"{toolenv:FIXTURE_CACHE}"}, nil)}
	_, _, _, abort := ctx.SandboxExtras(runDir, tool)
	if abort == nil {
		t.Fatalf("the mount %q was accepted although the sandbox masks it", cache)
	}
	if abort.Class != ClassEnvironment {
		t.Fatalf("class = %q (%s), want %q: the host's cache layout is not the project's defect",
			abort.Class, abort.Reason, ClassEnvironment)
	}
}

// The toolchain's answer is host data, but a cache that points at a credentials
// directory is not a cache: the run stays an environment gap instead of reading
// the operator's keys into a verdict.
func TestSandboxMountRefusesAToolchainCacheInACredentialsDirectory(t *testing.T) {
	root := sandboxTestRoot(t)
	runDir := filepath.Join(root, "run")
	home := filepath.Join(root, "home")
	cache := filepath.Join(home, ".ssh")
	binDir := filepath.Join(root, "bin")
	for _, dir := range []string{runDir, cache, binDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	t.Setenv("HOME", home)
	tool := filepath.Join(binDir, "fixturetool")
	script := "#!/bin/sh\nif [ \"$1\" = env ] && [ \"$2\" = FIXTURE_CACHE ]; then printf '%s\\n' \"$HOME/.ssh\"; exit 0; fi\nexit 1\n"
	if err := os.WriteFile(tool, []byte(script), 0o755); err != nil {
		t.Fatalf("write tool: %v", err)
	}

	ctx := &Ctx{Languages: profileFor(tool, []string{"{toolenv:FIXTURE_CACHE}"}, nil)}
	_, _, _, abort := ctx.SandboxExtras(runDir, tool)
	if abort == nil {
		t.Fatalf("the mount %q was accepted although it is the credentials directory", cache)
	}
	if abort.Class != ClassEnvironment {
		t.Fatalf("class = %q (%s), want %q", abort.Class, abort.Reason, ClassEnvironment)
	}
}

// The toolchain variables a profile does declare still pass: the refusal is a
// list of loader and lookup keys, not a ban on configuration. These three change
// nothing about which code runs, and a narrow prefix rule used to refuse them.
func TestSandboxEnvStillAcceptsAToolchainCacheVariable(t *testing.T) {
	ctx := &Ctx{Languages: profileFor("fixturetool", nil, map[string]string{
		"FIXTURE_CACHE":    "/cache",
		"TMPDIR":           SandboxTmpDir,
		"GOFLAGS":          "-mod=mod",
		"PYTHONUNBUFFERED": "1",
		"NODE_ENV":         "test",
		"GIT_AUTHOR_NAME":  "Fixture",
	})}
	_, _, env, abort := ctx.SandboxExtras(sandboxTestRoot(t), "fixturetool")
	if abort != nil {
		t.Fatalf("a toolchain variable was refused: %s", abort.Reason)
	}
	if len(env) != 6 {
		t.Fatalf("env = %v, want six entries", env)
	}
}
