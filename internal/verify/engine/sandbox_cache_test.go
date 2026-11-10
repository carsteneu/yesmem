package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// cacheProfile declares the suite command, the tool the run may need and one
// persistent cache root. GOCACHE names it through the placeholder, which is how
// a profile says "whatever the cache root is" instead of repeating it.
func cacheProfile(program, cache string) []Language {
	return []Language{{
		Name:         "fixture",
		TestCommand:  program + " test",
		SandboxCache: cache,
		SandboxEnv:   map[string]string{"GOCACHE": CacheDirPlaceholder},
	}}
}

// The build cache must outlive the throwaway checkout, otherwise every run
// compiles cold. The root is a writable mount, and the variable points at the
// resolved root the run actually sees.
func TestSandboxCacheIsAWritableMountOutsideTheCheckout(t *testing.T) {
	root := sandboxTestRoot(t)
	runDir := filepath.Join(root, "run")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatalf("mkdir run: %v", err)
	}
	home := filepath.Join(root, "home")
	cache := filepath.Join(home, ".cache", "yesmem", "build")

	ctx := &Ctx{Languages: cacheProfile("sh", cache)}
	_, caches, env, abort := ctx.SandboxExtras(runDir, "sh")

	if abort != nil {
		t.Fatalf("the cache root was refused: %s", abort.Reason)
	}
	if len(caches) != 1 || caches[0] != [2]string{cache, cache} {
		t.Fatalf("caches = %v, want the declared root as the writable mount", mountPaths(caches))
	}
	if !strings.Contains(strings.Join(env, "\n"), "GOCACHE="+cache) {
		t.Fatalf("env = %v, want GOCACHE pointing at the cache root", env)
	}
	if info, err := os.Stat(cache); err != nil || !info.IsDir() {
		t.Fatalf("the declared cache root was not created: %v", err)
	}
}

// A run that writes into the cache leaves its entries behind: that is the whole
// point of the field, and the only way to prove it is a second run that finds
// them.
func TestSandboxCacheKeepsWhatARunWrote(t *testing.T) {
	root := sandboxTestRoot(t)
	bwrap := requireSandbox(t, root)
	runDir := filepath.Join(root, "run")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatalf("mkdir run: %v", err)
	}
	cache := filepath.Join(root, "home", ".cache", "yesmem", "build")

	ctx := &Ctx{Languages: cacheProfile("sh", cache)}
	write := "printf 'entry\\n' > \"$GOCACHE/built\" && cat \"$GOCACHE/built\""
	outcome, verdict := ctx.runCommand(SandboxRequire, bwrap, runDir, "<checkout>",
		"sh -c "+write, []string{"sh", "-c", write}, 0)
	if verdict != nil {
		t.Fatalf("the run was refused: %s", verdict.Reason)
	}
	if outcome.Code != 0 {
		t.Fatalf("exit = %d, want 0: %s", outcome.Code, outcome.RawOutput)
	}

	if _, err := os.Stat(filepath.Join(cache, "built")); err != nil {
		t.Fatalf("the run's cache entry did not survive the run: %v", err)
	}
}

// Without a declaration the build cache stays where it was: inside the throwaway
// checkout. The placeholder must keep resolving there, or every existing profile
// would silently move its cache into the operator's home.
func TestSandboxWithoutACacheKeepsTheCheckoutBuildCache(t *testing.T) {
	runDir := sandboxTestRoot(t)
	ctx := &Ctx{Languages: []Language{{
		Name:        "fixture",
		TestCommand: "fixturetool test",
		SandboxEnv:  map[string]string{"GOCACHE": CacheDirPlaceholder},
	}}}

	mounts, caches, env, abort := ctx.SandboxExtras(runDir, "fixturetool")
	if abort != nil {
		t.Fatalf("extras refused: %s", abort.Reason)
	}
	if len(mounts) != 0 || len(caches) != 0 {
		t.Fatalf("mounts=%v caches=%v, want no mounts without a declaration",
			mountPaths(mounts), mountPaths(caches))
	}
	want := "GOCACHE=" + filepath.Join(runDir, ".yesmem-gocache")
	if !strings.Contains(strings.Join(env, "\n"), want) {
		t.Fatalf("env = %v, want %s", env, want)
	}
}

// The cache field is the one writable host tree a profile may name, so its rules
// are the strictest: never the home or a parent, never a credential directory,
// never the verified repository, never a masked tree.
func TestSandboxCacheRefusesAnUnsafeRoot(t *testing.T) {
	root := sandboxTestRoot(t)
	home := filepath.Join(root, "home")
	repo := filepath.Join(root, "project")
	for _, dir := range []string{home, repo, filepath.Join(root, "containing", "victim")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}

	cases := []struct {
		name string
		root string
		repo string
	}{
		{name: "the home itself", root: home},
		{name: "a parent of the home", root: root},
		{name: "the filesystem root", root: string(os.PathSeparator)},
		{name: "a top-level directory of the home", root: filepath.Join(home, ".cache")},
		{name: "a credential directory", root: filepath.Join(home, ".ssh")},
		{name: "a relative path", root: "cache"},
		{name: "a masked tree", root: filepath.Join("/tmp", "yesmem-cache-probe")},
		{name: "inside the verified repository", root: filepath.Join(repo, "cache"), repo: repo},
		{name: "the repository itself", root: repo, repo: repo},
		{name: "a parent of the repository", root: root, repo: repo},
		{name: "the verifier's cache root itself", root: filepath.Join(home, ".cache", "yesmem")},
		{name: "a binary directory the host executes from", root: filepath.Join(home, ".local", "bin")},
		{name: "an arbitrary system path", root: "/etc/yesmem-cache"},
	}
	for _, entry := range cases {
		t.Run(entry.name, func(t *testing.T) {
			ctx := &Ctx{Repo: entry.repo, Languages: cacheProfile("sh", entry.root)}
			_, _, _, abort := ctx.SandboxExtras(filepath.Join(root, "run"), "sh")
			if abort == nil {
				t.Fatalf("the cache root %q was accepted", entry.root)
			}
			if abort.Class != ClassDefect {
				t.Fatalf("class = %q (%s), want %q", abort.Class, abort.Reason, ClassDefect)
			}
		})
	}
}

// A symlinked cache root is followed before it is judged: the link is not the
// tree that gets mounted, so a link into the repository must be refused as what
// it points at, not accepted as what it is called.
func TestSandboxCacheRefusesASymlinkOutOfTheAllowedTree(t *testing.T) {
	root := sandboxTestRoot(t)
	repo := filepath.Join(root, "project")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}
	link := filepath.Join(root, "cache-link")
	if err := os.Symlink(repo, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	ctx := &Ctx{Repo: repo, Languages: cacheProfile("sh", link)}
	_, _, _, abort := ctx.SandboxExtras(filepath.Join(root, "run"), "sh")
	if abort == nil {
		t.Fatal("a cache root that links into the verified repository was accepted")
	}
}

// A cache root that cannot be created is the machine's gap, not the profile's
// fault: the run keeps going, and the claim that needed the cache reports an
// environment gap instead of a defect nobody can fix.
func TestSandboxCacheThatCannotBeCreatedIsAnEnvironmentGap(t *testing.T) {
	root := sandboxTestRoot(t)
	base := filepath.Join(root, "home", ".cache", "yesmem")
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatalf("mkdir base: %v", err)
	}
	blocker := filepath.Join(base, "blocker")
	if err := os.WriteFile(blocker, []byte("not a directory\n"), 0o644); err != nil {
		t.Fatalf("write blocker: %v", err)
	}

	ctx := &Ctx{Languages: cacheProfile("sh", filepath.Join(blocker, "cache"))}
	_, _, _, abort := ctx.SandboxExtras(filepath.Join(root, "run"), "sh")
	if abort == nil {
		t.Fatal("a cache root below a regular file was accepted")
	}
	if abort.Class != ClassEnvironment {
		t.Fatalf("class = %q (%s), want %q", abort.Class, abort.Reason, ClassEnvironment)
	}
}

// The cache root cannot name itself: a profile that writes {cache} into its own
// cache field is a loop, and a loop is a defect in the profile.
func TestSandboxCacheCannotNameItself(t *testing.T) {
	root := sandboxTestRoot(t)
	ctx := &Ctx{Languages: cacheProfile("sh", CacheDirPlaceholder)}

	_, _, _, abort := ctx.SandboxExtras(filepath.Join(root, "run"), "sh")
	if abort == nil {
		t.Fatal("a cache root built from its own placeholder was accepted")
	}
	if abort.Class != ClassDefect {
		t.Fatalf("class = %q (%s), want %q", abort.Class, abort.Reason, ClassDefect)
	}
}
