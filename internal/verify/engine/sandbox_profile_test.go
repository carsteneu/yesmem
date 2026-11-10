package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// profileFor builds a language profile whose single declared command is the
// program under test: that is what ties a run to its caches.
func profileFor(program string, readOnly []string, env map[string]string) []Language {
	return []Language{{
		Name:            "fixture",
		TestCommand:     program + " test",
		SandboxReadOnly: readOnly,
		SandboxEnv:      env,
	}}
}

func mountPaths(mounts [][2]string) []string {
	paths := make([]string, 0, len(mounts))
	for _, mount := range mounts {
		paths = append(paths, mount[0]+"->"+mount[1])
	}
	return paths
}

// The home shield is a tmpfs over the operator's home. A mount that is the home
// itself, or any parent of it, would put the tree back: that is a configuration
// error, never a special case.
func TestSandboxMountRejectsTheHomeAndItsParents(t *testing.T) {
	root := sandboxTestRoot(t)
	home := filepath.Join(root, "home")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatalf("mkdir home: %v", err)
	}
	t.Setenv("HOME", home)

	cases := []struct {
		name  string
		mount string
	}{
		{name: "the home itself", mount: home},
		{name: "the parent of the home", mount: root},
		{name: "the filesystem root", mount: string(os.PathSeparator)},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			runDir := filepath.Join(root, "run")
			if err := os.MkdirAll(runDir, 0o755); err != nil {
				t.Fatalf("mkdir run: %v", err)
			}
			ctx := &Ctx{Languages: profileFor("fixturetool", []string{testCase.mount}, nil)}

			mounts, _, env, abort := ctx.SandboxExtras(runDir, "fixturetool")
			if abort == nil {
				t.Fatalf("mount %q was accepted: mounts=%v env=%v", testCase.mount, mountPaths(mounts), env)
			}
			if abort.Class != ClassDefect {
				t.Errorf("class = %q (%s), want %q", abort.Class, abort.Reason, ClassDefect)
			}
			if len(mounts) != 0 {
				t.Errorf("mounts = %v, want none after a refused mount", mountPaths(mounts))
			}
		})
	}
}

// A cache root inside the home is the normal case: the module cache lives
// there, and mounting it read-only is the whole point. But only the toolchain
// may name such a path: a profile that composes one under the home could name
// ~/.ssh, and the shield that covers the rest of the home would be undone.
func TestSandboxMountAcceptsACacheRootInsideTheHome(t *testing.T) {
	root := sandboxTestRoot(t)
	runDir := filepath.Join(root, "run")
	home := filepath.Join(root, "home")
	cache := filepath.Join(home, "go", "pkg", "mod")
	binDir := filepath.Join(root, "bin")
	for _, dir := range []string{runDir, cache, binDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	t.Setenv("HOME", home)
	tool := filepath.Join(binDir, "fixturetool")
	script := "#!/bin/sh\nif [ \"$1\" = env ] && [ \"$2\" = FIXTURE_CACHE ]; then printf '%s\\n' \"$FIXTURE_CACHE_ROOT\"; exit 0; fi\nexit 1\n"
	if err := os.WriteFile(tool, []byte(script), 0o755); err != nil {
		t.Fatalf("write tool: %v", err)
	}
	t.Setenv("FIXTURE_CACHE_ROOT", cache)

	ctx := &Ctx{Languages: profileFor(tool, []string{"{toolenv:FIXTURE_CACHE}"}, nil)}
	mounts, _, _, abort := ctx.SandboxExtras(runDir, tool)
	if abort != nil {
		t.Fatalf("cache root inside the home, named by the toolchain, was refused: %s", abort.Reason)
	}
	if want := [][2]string{{cache, cache}}; len(mounts) != 1 || mounts[0] != want[0] {
		t.Fatalf("mounts = %v, want %v", mountPaths(mounts), []string{cache + "->" + cache})
	}
}

// The same cache as a literal path is refused: the profile then names a path it
// composed, and the only path under the home a profile may name is the one the
// toolchain reported for itself. The placeholder form does not help: appending
// ".." to it composes a path just as well.
func TestSandboxMountRefusesAHomeSubtreeComposedByTheProfile(t *testing.T) {
	root := sandboxTestRoot(t)
	runDir := filepath.Join(root, "run")
	home := filepath.Join(root, "home")
	cache := filepath.Join(home, "go", "pkg", "mod")
	secrets := filepath.Join(home, ".ssh")
	binDir := filepath.Join(root, "bin")
	for _, dir := range []string{runDir, cache, secrets, binDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	t.Setenv("HOME", home)
	tool := filepath.Join(binDir, "fixturetool")
	script := "#!/bin/sh\nif [ \"$1\" = env ] && [ \"$2\" = FIXTURE_CACHE ]; then printf '%s\\n' \"$FIXTURE_CACHE_ROOT\"; exit 0; fi\nexit 1\n"
	if err := os.WriteFile(tool, []byte(script), 0o755); err != nil {
		t.Fatalf("write tool: %v", err)
	}
	t.Setenv("FIXTURE_CACHE_ROOT", cache)

	for name, mount := range map[string]string{
		"literal path":        secrets,
		"placeholder plus ..": "{toolenv:FIXTURE_CACHE}/../../../.ssh",
	} {
		t.Run(name, func(t *testing.T) {
			ctx := &Ctx{Languages: profileFor(tool, []string{mount}, nil)}
			_, _, _, abort := ctx.SandboxExtras(runDir, tool)
			if abort == nil {
				t.Fatalf("the mount %q was accepted although it lies in the home the sandbox covers", mount)
			}
			if abort.Class != ClassDefect {
				t.Fatalf("class = %q (%s), want %q", abort.Class, abort.Reason, ClassDefect)
			}
		})
	}
}

// A mount above a masked root reopens it just as a mount below: --ro-bind /var
// after the tmpfs over /var/tmp puts the host's temp tree back.
func TestSandboxMountRefusesAnAncestorOfAMaskedRoot(t *testing.T) {
	root := sandboxTestRoot(t)
	t.Setenv("HOME", filepath.Join(root, "home"))

	for _, mount := range []string{"/var", "/"} {
		t.Run(mount, func(t *testing.T) {
			ctx := &Ctx{Languages: profileFor("fixturetool", []string{mount}, nil)}
			_, _, _, abort := ctx.SandboxExtras(root, "fixturetool")
			if abort == nil {
				t.Fatalf("the mount %q was accepted although it contains a masked root", mount)
			}
			if abort.Class != ClassDefect {
				t.Fatalf("class = %q (%s), want %q", abort.Class, abort.Reason, ClassDefect)
			}
		})
	}
}

// The profile drives both halves: the read-only mounts and the environment the
// run gets. The checkout placeholder is the run directory, so a writable cache
// disappears with the throwaway tree.
func TestSandboxExtrasComeFromTheProfile(t *testing.T) {
	runDir := sandboxTestRoot(t)
	cache := filepath.Join(runDir, "modcache")
	if err := os.MkdirAll(cache, 0o755); err != nil {
		t.Fatalf("mkdir cache: %v", err)
	}
	ctx := &Ctx{Languages: profileFor("fixturetool",
		[]string{"{rundir}/modcache"},
		map[string]string{
			"BUILD_CACHE": "{rundir}/buildcache",
			"FLAGS":       "-mod=mod",
		})}

	mounts, _, env, abort := ctx.SandboxExtras(runDir, "/usr/bin/fixturetool")
	if abort != nil {
		t.Fatalf("extras refused: %s", abort.Reason)
	}
	if len(mounts) != 1 || mounts[0] != [2]string{cache, cache} {
		t.Fatalf("mounts = %v, want the run directory's modcache", mountPaths(mounts))
	}
	joined := strings.Join(env, "\n")
	if !strings.Contains(joined, "BUILD_CACHE="+filepath.Join(runDir, "buildcache")) {
		t.Errorf("env = %v, want BUILD_CACHE under the run directory", env)
	}
	if !strings.Contains(joined, "FLAGS=-mod=mod") {
		t.Errorf("env = %v, want FLAGS passed through", env)
	}
}

// A cache root the host does not have is not bound; the run then stays an
// environment gap instead of being reported as a defect.
func TestSandboxMountSkipsAPathThatDoesNotExist(t *testing.T) {
	runDir := sandboxTestRoot(t)
	ctx := &Ctx{Languages: profileFor("fixturetool", []string{"{rundir}/missing"}, nil)}

	mounts, _, _, abort := ctx.SandboxExtras(runDir, "fixturetool")
	if abort != nil {
		t.Fatalf("a missing cache root was refused as a defect: %s", abort.Reason)
	}
	if len(mounts) != 0 {
		t.Errorf("mounts = %v, want none for a missing path", mountPaths(mounts))
	}
}

// The tool names its own cache paths. The profile asks the tool instead of
// hardcoding a path, and the value it gets back is the one that is mounted.
func TestSandboxExtrasAskTheToolForItsCachePath(t *testing.T) {
	root := sandboxTestRoot(t)
	runDir := filepath.Join(root, "run")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatalf("mkdir run: %v", err)
	}
	binDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	cache := filepath.Join(root, "reported-cache")
	if err := os.MkdirAll(cache, 0o755); err != nil {
		t.Fatalf("mkdir cache: %v", err)
	}
	tool := filepath.Join(binDir, "fixturetool")
	script := "#!/bin/sh\nif [ \"$1\" = env ] && [ \"$2\" = FIXTURE_CACHE ]; then printf '%s\\n' \"$FIXTURE_CACHE_ROOT\"; exit 0; fi\nexit 1\n"
	if err := os.WriteFile(tool, []byte(script), 0o755); err != nil {
		t.Fatalf("write tool: %v", err)
	}
	t.Setenv("FIXTURE_CACHE_ROOT", cache)

	ctx := &Ctx{Languages: profileFor(tool,
		[]string{"{toolenv:FIXTURE_CACHE}"},
		map[string]string{"FIXTURE_CACHE": "{toolenv:FIXTURE_CACHE}"})}

	mounts, _, env, abort := ctx.SandboxExtras(runDir, tool)
	if abort != nil {
		t.Fatalf("extras refused: %s", abort.Reason)
	}
	if len(mounts) != 1 || mounts[0] != [2]string{cache, cache} {
		t.Fatalf("mounts = %v, want the path the tool reported", mountPaths(mounts))
	}
	if !strings.Contains(strings.Join(env, "\n"), "FIXTURE_CACHE="+cache) {
		t.Errorf("env = %v, want the reported path", env)
	}
}

// A symlinked cache root is mounted under its target. Binding the link path
// would leave the link pointing into the home tmpfs, and a link to the home
// itself would undo the shield.
func TestSandboxMountResolvesASymlinkedCacheRoot(t *testing.T) {
	root := sandboxTestRoot(t)
	realCache := filepath.Join(root, "real-cache")
	if err := os.MkdirAll(realCache, 0o755); err != nil {
		t.Fatalf("mkdir cache: %v", err)
	}
	link := filepath.Join(root, "linked-cache")
	if err := os.Symlink(realCache, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	ctx := &Ctx{Languages: profileFor("fixturetool",
		[]string{link},
		map[string]string{"CACHE": link})}

	mounts, _, env, abort := ctx.SandboxExtras(root, "fixturetool")
	if abort != nil {
		t.Fatalf("extras refused: %s", abort.Reason)
	}
	if len(mounts) != 1 || mounts[0] != [2]string{realCache, realCache} {
		t.Fatalf("mounts = %v, want the resolved target", mountPaths(mounts))
	}
	if !strings.Contains(strings.Join(env, "\n"), "CACHE="+realCache) {
		t.Errorf("env = %v, want the resolved target", env)
	}
}

// A program no profile declares gets nothing: no guessed caches, no guessed
// environment.
func TestSandboxExtrasWithoutAMatchingProfile(t *testing.T) {
	ctx := &Ctx{Languages: profileFor("fixturetool", []string{"{rundir}"}, map[string]string{"X": "1"})}

	mounts, _, env, abort := ctx.SandboxExtras(sandboxTestRoot(t), "make")
	if abort != nil {
		t.Fatalf("extras refused: %s", abort.Reason)
	}
	if len(mounts) != 0 || len(env) != 0 {
		t.Errorf("mounts=%v env=%v, want nothing for an undeclared program", mountPaths(mounts), env)
	}
}

// A refused profile must say which command it refused: the verdict is what the
// operator reads.
func TestSandboxRefusalNamesTheCommand(t *testing.T) {
	root := sandboxTestRoot(t)
	home := filepath.Join(root, "home")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatalf("mkdir home: %v", err)
	}
	t.Setenv("HOME", home)
	ctx := &Ctx{Languages: profileFor("fixturetool", []string{home}, nil)}

	_, verdict := ctx.runCommand(SandboxRequire, "bwrap", root, "<checkout>",
		"fixturetool test", []string{"fixturetool", "test"}, 0)
	if verdict == nil {
		t.Fatal("the run was not refused")
	}
	if !strings.Contains(verdict.Command, "fixturetool test") {
		t.Fatalf("command = %q, want the refused command line", verdict.Command)
	}
	// The class separates a refused profile from a run that merely could not
	// start: both carry the same command line.
	if verdict.Class != ClassDefect {
		t.Fatalf("class = %q (%s), want %q", verdict.Class, verdict.Reason, ClassDefect)
	}
}

// The home may itself be a symlink. The shield covers the link, so a mount that
// names the real tree is the home and must be refused.
func TestSandboxMountRefusesTheHomeBehindASymlink(t *testing.T) {
	root := sandboxTestRoot(t)
	realHome := filepath.Join(root, "real-home")
	if err := os.MkdirAll(realHome, 0o755); err != nil {
		t.Fatalf("mkdir home: %v", err)
	}
	link := filepath.Join(root, "home-link")
	if err := os.Symlink(realHome, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	// The account's home is the link; the mount names the real tree.
	t.Setenv("HOME", link)

	ctx := &Ctx{Languages: profileFor("fixturetool", []string{realHome}, nil)}
	_, _, _, abort := ctx.SandboxExtras(root, "fixturetool")
	if abort == nil {
		t.Fatalf("the mount %q was accepted although it is the home behind %q", realHome, link)
	}
	if abort.Class != ClassDefect {
		t.Fatalf("class = %q (%s), want %q", abort.Class, abort.Reason, ClassDefect)
	}
}

// A mount that is a parent of the run directory would cover the one writable
// tree with a read-only bind.
func TestSandboxMountRefusesAnAncestorOfTheRunDir(t *testing.T) {
	root := sandboxTestRoot(t)
	home := filepath.Join(root, "home")
	runs := filepath.Join(root, "runs")
	runDir := filepath.Join(runs, "checkout")
	for _, dir := range []string{home, runDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	t.Setenv("HOME", home)

	ctx := &Ctx{Languages: profileFor("fixturetool", []string{runs}, nil)}
	_, _, _, abort := ctx.SandboxExtras(runDir, "fixturetool")
	if abort == nil {
		t.Fatalf("the mount %q was accepted although it shadows the run directory", runs)
	}
	if abort.Class != ClassDefect {
		t.Fatalf("class = %q (%s), want %q", abort.Class, abort.Reason, ClassDefect)
	}
}

// The tool that answers for a cache path must be the operator's. A program the
// checkout supplies would mean running repository code on the host,
// unsandboxed, just to learn a path.
func TestSandboxExtrasRefuseAProgramInsideTheRunDir(t *testing.T) {
	runDir := sandboxTestRoot(t)
	binDir := filepath.Join(runDir, "vendor", "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	tool := filepath.Join(binDir, "fixturetool")
	if err := os.WriteFile(tool, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write tool: %v", err)
	}

	ctx := &Ctx{Languages: profileFor(tool, []string{"{toolenv:FIXTURE_CACHE}"}, nil)}
	_, _, _, abort := ctx.SandboxExtras(runDir, tool)
	if abort == nil {
		t.Fatal("the checkout's own program was accepted as the toolchain")
	}
	if abort.Class != ClassDefect {
		t.Fatalf("class = %q (%s), want %q", abort.Class, abort.Reason, ClassDefect)
	}
}

// A profile names the same cache path twice (mount and variable). Asking the
// toolchain once for it is enough.
func TestSandboxAsksTheToolOncePerResolution(t *testing.T) {
	root := sandboxTestRoot(t)
	runDir := filepath.Join(root, "run")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatalf("mkdir run: %v", err)
	}
	cache := filepath.Join(root, "cache")
	if err := os.MkdirAll(cache, 0o755); err != nil {
		t.Fatalf("mkdir cache: %v", err)
	}
	calls := filepath.Join(root, "calls")
	tool := filepath.Join(root, "fixturetool")
	script := "#!/bin/sh\nprintf 'call\\n' >> \"$FIXTURE_CALLS\"\nif [ \"$1\" = env ] && [ \"$2\" = FIXTURE_CACHE ]; then printf '%s\\n' \"$FIXTURE_CACHE_ROOT\"; fi\n"
	if err := os.WriteFile(tool, []byte(script), 0o755); err != nil {
		t.Fatalf("write tool: %v", err)
	}
	t.Setenv("FIXTURE_CACHE_ROOT", cache)
	t.Setenv("FIXTURE_CALLS", calls)

	ctx := &Ctx{Languages: profileFor(tool,
		[]string{"{toolenv:FIXTURE_CACHE}"},
		map[string]string{"FIXTURE_CACHE": "{toolenv:FIXTURE_CACHE}"})}
	if _, _, _, abort := ctx.SandboxExtras(runDir, tool); abort != nil {
		t.Fatalf("extras refused: %s", abort.Reason)
	}

	written, err := os.ReadFile(calls)
	if err != nil {
		t.Fatalf("read calls: %v", err)
	}
	if got := len(strings.Fields(string(written))); got != 1 {
		t.Fatalf("the toolchain was asked %d times, want 1", got)
	}
}

// The profile may replace a minimal key, and must not end up with two entries
// for it: the child would pick.
func TestTestEnvWithReplacesAMinimalKey(t *testing.T) {
	checkout := sandboxTestRoot(t)
	env := TestEnvWith(checkout, []string{"HOME=/elsewhere", "GOCACHE=/cache"})

	var homes []string
	for _, entry := range env {
		if strings.HasPrefix(entry, "HOME=") {
			homes = append(homes, entry)
		}
	}
	if len(homes) != 1 || homes[0] != "HOME=/elsewhere" {
		t.Fatalf("HOME entries = %v, want exactly the profile's /elsewhere", homes)
	}
	if !strings.Contains(strings.Join(env, "\n"), "GOCACHE=/cache") {
		t.Fatalf("env = %v, want the profile's addition", env)
	}
}

// The verdict is the audit trail: a reader must see which host trees the run
// was allowed to read, with the home still rendered as <home>.
func TestSandboxDisplayShowsTheMounts(t *testing.T) {
	home := sandboxTestRoot(t)
	t.Setenv("HOME", home)
	mount := filepath.Join(home, "go", "pkg", "mod")

	display := SandboxDisplayWith("go test ./...", [][2]string{{mount, mount}}, nil)
	if !strings.Contains(display, "<home>/go/pkg/mod") {
		t.Fatalf("display = %q, want the cache mount with the home rendered", display)
	}
	if !strings.Contains(display, "--unshare-net") {
		t.Fatalf("display = %q, want the sandbox prefix", display)
	}
}

// A relative mount is not a host tree: EvalSymlinks leaves it relative and
// bwrap would resolve it against a root that is not the one the guard judged.
func TestSandboxMountRefusesARelativePath(t *testing.T) {
	ctx := &Ctx{Languages: profileFor("fixturetool", []string{".."}, nil)}

	mounts, _, _, abort := ctx.SandboxExtras(sandboxTestRoot(t), "fixturetool")
	if abort == nil {
		t.Fatalf("the relative mount %q was accepted: %v", "..", mountPaths(mounts))
	}
	if abort.Class != ClassDefect {
		t.Fatalf("class = %q (%s), want %q", abort.Class, abort.Reason, ClassDefect)
	}
}

// PATH and HOME are the variables the sandbox's guarantees are written in
// terms of: which binary runs, and which init files the run reads. A profile
// may move TMPDIR, but not those two.
func TestSandboxEnvRefusesAKeyTheSandboxOwns(t *testing.T) {
	keys := []string{
		// The two the run's confinement is written in terms of.
		"PATH", "HOME",
		// The ones the sandbox sets to close a specific door.
		"PYTHONNOUSERSITE", "PYTHONDONTWRITEBYTECODE", "GIT_TERMINAL_PROMPT",
		// The dynamic loader: a path here runs host code in every child.
		"LD_PRELOAD", "ld_library_path", "DYLD_INSERT_LIBRARIES",
	}
	for _, key := range keys {
		t.Run(key, func(t *testing.T) {
			runDir := sandboxTestRoot(t)
			ctx := &Ctx{Languages: profileFor("fixturetool", nil, map[string]string{key: "/elsewhere"})}

			_, _, _, abort := ctx.SandboxExtras(runDir, "fixturetool")
			if abort == nil {
				t.Fatalf("the profile was allowed to set %s", key)
			}
			if abort.Class != ClassDefect {
				t.Fatalf("class = %q (%s), want %q", abort.Class, abort.Reason, ClassDefect)
			}
		})
	}
}

// With a relocated account the display must not print the real home either:
// the mounts are resolved, so the rendering has to be.
func TestSandboxDisplayHidesTheHomeBehindASymlink(t *testing.T) {
	root := sandboxTestRoot(t)
	realHome := filepath.Join(root, "real-home")
	if err := os.MkdirAll(realHome, 0o755); err != nil {
		t.Fatalf("mkdir home: %v", err)
	}
	link := filepath.Join(root, "home-link")
	if err := os.Symlink(realHome, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	t.Setenv("HOME", link)
	mount := filepath.Join(realHome, "go", "pkg", "mod")

	display := SandboxDisplayWith("go test ./...", [][2]string{{mount, mount}}, nil)
	if strings.Contains(display, realHome) {
		t.Fatalf("display = %q, want the account layout rendered as <home>", display)
	}
	if !strings.Contains(display, "<home>/go/pkg/mod") {
		t.Fatalf("display = %q, want the mount rendered under <home>", display)
	}
}

// The probe must prepare the sandbox it is given, mounts included: a mount that
// cannot be bound then fails as "the sandbox does not work" before any command
// starts, instead of surfacing as a test outcome.
//
// Scope: this exercises the probe itself. That the run hands its mounts to the
// probe is covered at the other end, by the recorded sandbox line of a
// confirmed run (TestSandboxConfirmsAGoTestRun).
func TestSandboxProbeBindsTheGivenMounts(t *testing.T) {
	bwrap := FindBwrap()
	if bwrap == "" {
		t.Skip("bubblewrap is not available")
	}
	checkout := sandboxTestRoot(t)
	if err := SandboxProbe(bwrap, checkout); err != nil {
		t.Skipf("the host cannot start a sandbox: %v", err)
	}

	missing := filepath.Join(checkout, "no-such-cache-root")
	if err := sandboxProbeWith(bwrap, checkout, [][2]string{{missing, missing}}, nil, nil); err == nil {
		t.Fatalf("the probe passed although the mount %q cannot be bound", missing)
	}
}

// The cache mount must not come at the price of the network namespace.
func TestSandboxPrefixKeepsTheNetworkNamespacedWithMounts(t *testing.T) {
	prefix := SandboxPrefix("bwrap", "/checkout",
		[][2]string{{"/host/cache", "/host/cache"}}, nil, "")

	if len(prefix) == 0 {
		t.Fatal("empty prefix")
	}
	joined := strings.Join(prefix, " ")
	if !strings.Contains(joined, "--unshare-net") {
		t.Fatalf("prefix = %s, want --unshare-net even with cache mounts", joined)
	}
	cacheBind := -1
	checkoutBind := -1
	for index := 0; index+2 < len(prefix); index++ {
		if prefix[index] == "--ro-bind" && prefix[index+1] == "/host/cache" {
			cacheBind = index
		}
		if prefix[index] == "--bind" && prefix[index+1] == "/checkout" {
			checkoutBind = index
		}
	}
	if cacheBind < 0 {
		t.Fatalf("prefix = %s, want the cache bound read-only", joined)
	}
	if checkoutBind < 0 || cacheBind < checkoutBind {
		t.Fatalf("prefix = %s, want the cache bound after the writable checkout", joined)
	}
}
