package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	// RunDirPlaceholder stands for the directory a run executes in. A profile
	// uses it for a cache that must live inside the throwaway checkout: a
	// writable cache elsewhere would outlive the run and carry state from one
	// verdict into the next.
	RunDirPlaceholder = "{rundir}"

	// CacheDirPlaceholder stands for the persistent build cache root: the
	// directory SandboxCache declares, or the checkout's own cache when the
	// profile declares none. A profile names the root once and points GOCACHE at
	// the placeholder instead of repeating the path.
	CacheDirPlaceholder = "{cache}"

	// CacheBasePlaceholder stands for the verifier's own cache root below the
	// operator's home. A declared cache must stay inside it: the one writable
	// tree a repository-controlled profile may open is a directory the verifier
	// owns, not a host path the profile composes.
	CacheBasePlaceholder = "{base}"

	// HomePlaceholder stands for the operator's home. A committed profile is the
	// same file on every machine, so a tool directory below the home is named
	// through it instead of through one operator's layout.
	HomePlaceholder = "{home}"

	toolEnvPrefix = "{toolenv:"

	// ToolEnvTimeout bounds one query against the toolchain. A program that
	// cannot answer what its own cache path is within this budget cannot run a
	// suite either.
	ToolEnvTimeout = 10 * time.Second

	// maxPlaceholderPasses stops a value that expands into further
	// placeholders. A profile is data and may be wrong; a wrong profile must
	// not become an endless loop.
	maxPlaceholderPasses = 8
)

var envNameShape = regexp.MustCompile(`\A[A-Za-z_][A-Za-z0-9_]*\z`)

// ToolEnv is the placeholder for the value the language's own program reports
// for name, asked as `env name`.
//
// A profile must not hardcode a cache path: the same profile serves machines
// with different homes and different toolchain layouts, and a path it guessed
// would be mounted as if it were true. Asking the tool keeps the knowledge at
// the one place that knows it.
func ToolEnv(name string) string {
	return toolEnvPrefix + name + "}"
}

// SandboxExtras resolves the sandbox configuration a language profile declares
// for a run of program.
//
// It returns the read-only mounts, the writable cache roots and the environment
// additions the run gets, or a verdict when the profile cannot be followed
// safely. An empty result means no profile declares this program: the run then
// keeps the plain sandbox and its outcome stays an environment gap, exactly as
// before.
func (c *Ctx) SandboxExtras(dir, program string) (mounts, caches [][2]string, env []string, abort *Verdict) {
	language, tool, ok := c.sandboxTool(program)
	if !ok {
		return nil, nil, nil, nil
	}

	reported := map[string]string{}
	answers := map[string]string{}
	cacheRoot, problem := c.sandboxCache(language, dir, tool, answers)
	if problem != nil {
		return nil, nil, nil, problem
	}
	for _, entry := range language.SandboxReadOnly {
		path, problem := c.expandSandboxValue(entry, dir, tool, answers, cacheRoot)
		if problem != nil {
			return nil, nil, nil, problem
		}
		if problem := refuseMountShape(path); problem != nil {
			return nil, nil, nil, problem
		}
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil {
			// The host has no such cache root. Not binding it keeps the run an
			// environment gap, which is what it is; a substitute path would be
			// a guess, and running outside the sandbox is not an option here.
			continue
		}
		if problem := refuseUnsafeMount(resolved, dir, c.Repo, toolchainNamedValue(entry)); problem != nil {
			return nil, nil, nil, problem
		}
		reported[entry] = resolved
		mounts = append(mounts, [2]string{resolved, resolved})
	}
	if cacheRoot != "" {
		caches = append(caches, [2]string{cacheRoot, cacheRoot})
	}

	keys := make([]string, 0, len(language.SandboxEnv))
	for key := range language.SandboxEnv {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if problem := refuseOwnedEnvKey(key); problem != nil {
			return nil, nil, nil, problem
		}
		value, problem := c.expandSandboxValue(language.SandboxEnv[key], dir, tool, answers, cacheRoot)
		if problem != nil {
			return nil, nil, nil, problem
		}
		// A value that names a mounted cache root points at the resolved
		// target: the run sees the tree only under that path. This is what
		// keeps a symlinked cache root consistent between the mount and the
		// variable that names it.
		if resolved, ok := reported[language.SandboxEnv[key]]; ok {
			value = resolved
		}
		env = append(env, key+"="+value)
	}
	return mounts, caches, env, nil
}

// sandboxCache resolves the persistent build cache root a profile declares.
//
// An empty result means the profile declares none: the run then keeps its cache
// inside the throwaway checkout, exactly as before. A declared root is judged,
// created when missing and resolved here, before any command starts: a cache
// that cannot be bound would otherwise surface as a failing suite, which would
// refute an honest report for a host problem.
func (c *Ctx) sandboxCache(language Language, dir, tool string, answers map[string]string) (string, *Verdict) {
	if strings.TrimSpace(language.SandboxCache) == "" {
		return "", nil
	}
	if strings.Contains(language.SandboxCache, CacheDirPlaceholder) {
		problem := Unverifiable(
			"the language profile builds its cache root from {cache} itself; a root cannot be named after itself").
			WithClass(ClassDefect)
		return "", &problem
	}
	value, problem := c.expandSandboxValue(language.SandboxCache, dir, tool, answers, "")
	if problem != nil {
		return "", problem
	}
	if problem := refuseMountShape(value); problem != nil {
		return "", problem
	}
	base := cacheBase()
	if base == "" {
		problem := Unverifiable(
			"the language profile declares a cache root, and the operator's home is unknown; " +
				"there is no directory the verifier owns to put it in").
			WithClass(ClassEnvironment)
		return "", &problem
	}
	// The one writable tree a repository-controlled profile may open is one the
	// verifier owns. Everything else is refused here, before the path is judged:
	// an arbitrary host path would let the checkout write where the host later
	// executes from (~/.local/bin, a repository's hooks directory).
	candidatePath := filepath.Clean(value)
	if !insideDir(candidatePath, base) || candidatePath == base {
		problem := Unverifiable(fmt.Sprintf(
			"the language profile's cache root %q lies outside the verifier's cache root %q; "+
				"a run may only write where the verifier keeps its own state", value, base)).
			WithClass(ClassDefect)
		return "", &problem
	}
	// The root is created where the judgement places it, not where a symlinked
	// ancestor points: an existing prefix is resolved first, the missing tail is
	// re-attached to that target, and only the result is created.
	candidate, err := resolveExistingPrefix(candidatePath)
	if err != nil {
		problem := Unverifiable(fmt.Sprintf(
			"the language profile's cache root %q cannot be resolved: %v", value, err)).
			WithClass(ClassEnvironment)
		return "", &problem
	}
	if !insideDir(candidate, base) || candidate == base {
		problem := Unverifiable(fmt.Sprintf(
			"the language profile's cache root %q resolves to %q, which is outside the "+
				"verifier's cache root %q", value, candidate, base)).
			WithClass(ClassDefect)
		return "", &problem
	}
	if problem := refuseUnsafeCache(candidate, dir, c.Repo, true); problem != nil {
		return "", problem
	}
	if err := os.MkdirAll(candidate, 0o700); err != nil {
		problem := Unverifiable(fmt.Sprintf(
			"the language profile's cache root %q cannot be created: %v; "+
				"the run keeps its build cache inside the checkout", candidate, err)).
			WithClass(ClassEnvironment)
		return "", &problem
	}
	// An existing root says nothing about being writable. A read-only one would
	// surface as a failing suite, which is the misclassification this whole
	// check exists to prevent.
	if err := probeWritable(candidate); err != nil {
		problem := Unverifiable(fmt.Sprintf(
			"the language profile's cache root %q is not writable: %v; "+
				"the run keeps its build cache inside the checkout", candidate, err)).
			WithClass(ClassEnvironment)
		return "", &problem
	}
	resolved, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		problem := Unverifiable(fmt.Sprintf(
			"the language profile's cache root %q cannot be resolved after creating it: %v",
			candidate, err)).
			WithClass(ClassEnvironment)
		return "", &problem
	}
	if problem := refuseUnsafeCache(resolved, dir, c.Repo, true); problem != nil {
		return "", problem
	}
	return resolved, nil
}

// cacheBase is the directory the verifier owns for persistent run state, or ""
// when there is no home to put it in.
//
// It is the one tree a profile may have mounted writable, and the profile names
// a subdirectory of it rather than a host path: a repository-controlled file must
// not decide where the operator's disk is written.
func cacheBase() string {
	home := operatorHome()
	if home == "" {
		return ""
	}
	return filepath.Join(home, ".cache", "yesmem")
}

// probeWritable proves the run can write into dir, not merely that it exists.
func probeWritable(dir string) error {
	probe, err := os.CreateTemp(dir, ".write-probe-")
	if err != nil {
		return err
	}
	name := probe.Name()
	if err := probe.Close(); err != nil {
		_ = os.Remove(name)
		return err
	}
	return os.Remove(name)
}

// resolveExistingPrefix resolves the deepest existing part of path and attaches
// the missing rest to it.
//
// Creating a directory through a symlink that points elsewhere would write
// outside the tree the judgement just approved, and the root is a writable
// mount: the write is the risk.
func resolveExistingPrefix(path string) (string, error) {
	var tail []string
	current := path
	for {
		resolved, err := filepath.EvalSymlinks(current)
		if err == nil {
			parts := append([]string{resolved}, tail...)
			return filepath.Join(parts...), nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", fmt.Errorf("no existing ancestor for %q", path)
		}
		tail = append([]string{filepath.Base(current)}, tail...)
		current = parent
	}
}

// refuseMountShape rejects a value that is not exactly one absolute host path.
//
// The list separator matters: a value carrying one would be read by bwrap as
// two mounts, and the guard that judged it saw one path.
func refuseMountShape(value string) *Verdict {
	reject := func(reason string) *Verdict {
		problem := Unverifiable(reason).WithClass(ClassDefect)
		return &problem
	}
	if strings.ContainsRune(value, os.PathListSeparator) {
		return reject(fmt.Sprintf(
			"the language profile names %q as a mount; a mount is one path, not a list", value))
	}
	if !filepath.IsAbs(value) {
		return reject(fmt.Sprintf(
			"the language profile mounts %q; a host path must be absolute", value))
	}
	return nil
}

// requireTools checks the programs a profile declares against the sandbox the
// run is about to enter.
//
// The lookup answers for the operator's shell, not for the run: the question is
// whether the tool exists at all and where. A tool behind a masked tree is only
// reachable when the profile mounts its directory, and a tool found nowhere is
// the machine's gap; both stay environment, never a refutation. A masked
// candidate is skipped while a reachable one exists further down the PATH: the
// operator's layout can hold a stale install that the run would never start.
func (c *Ctx) requireTools(dir, program string, mounts [][2]string) *Verdict {
	language, _, ok := c.sandboxTool(program)
	if !ok || len(language.Requires) == 0 {
		return nil
	}
	for _, tool := range language.Requires {
		name := filepath.Base(tool)
		blocked, found := "", false
		for _, entry := range strings.Split(os.Getenv("PATH"), string(os.PathListSeparator)) {
			if entry == "" || !filepath.IsAbs(entry) {
				continue
			}
			candidate, ok := programIn(entry, name)
			if !ok {
				continue
			}
			resolved := candidate
			if target, err := filepath.EvalSymlinks(candidate); err == nil {
				resolved = target
			}
			if maskedInSandbox(resolved, dir) && !mountCovers(filepath.Dir(resolved), mounts) {
				if blocked == "" {
					blocked = resolved
				}
				continue
			}
			found = true
			break
		}
		if found {
			continue
		}
		if blocked != "" {
			problem := Unverifiable(fmt.Sprintf(
				"the language profile requires %q, which resolves to %q; the sandbox masks "+
					"that directory and the profile mounts no directory for it", name, blocked)).
				WithClass(ClassEnvironment)
			return &problem
		}
		problem := Unverifiable(fmt.Sprintf(
			"the language profile requires %q, which is not on the operator's PATH; "+
				"the sandbox cannot provide it", name)).
			WithClass(ClassEnvironment)
		return &problem
	}
	return nil
}

// toolPathEnv appends the mounted tool bin directories to the run's PATH.
//
// A mount the child cannot look into satisfies nobody, and the addition is
// appended: a mounted directory can supply a missing tool, never shadow a system
// one. It is driven by the mounts, not by Requires: a profile may mount a
// toolchain for one particular run without declaring every suite to depend on it.
func toolPathEnv(mounts [][2]string) []string {
	var additions []string
	for _, mount := range mounts {
		if filepath.Base(mount[1]) != "bin" {
			continue
		}
		if !containsString(additions, mount[1]) {
			additions = append(additions, mount[1])
		}
	}
	if len(additions) == 0 {
		return nil
	}
	return []string{"PATH=" + RunPath() + string(os.PathListSeparator) +
		strings.Join(additions, string(os.PathListSeparator))}
}

// maskedInSandbox reports whether the sandbox covers path with an empty tmpfs.
//
// It mirrors SandboxPrefix: the operator's home is masked, and so is /home as a
// whole when the home lives below it, because every other account is reachable
// through that root. The run directory is the exception: it is bound writable
// after the masks, so a path inside it is visible even when it sits below the
// home.
func maskedInSandbox(path, runDir string) bool {
	if runDir != "" && insideDir(path, runDir) {
		return false
	}
	if maskedHostPath(path) {
		return true
	}
	// Both forms are judged: SandboxPrefix masks the home as the environment
	// names it, while the paths reaching this function are resolved. A relocated
	// account (a symlinked home) would otherwise be covered by one side and not
	// by the other.
	for _, home := range []string{operatorHome(), resolvedOperatorHome()} {
		if home == "" {
			continue
		}
		home = filepath.Clean(home)
		if path == home || strings.HasPrefix(path, home+string(os.PathSeparator)) {
			return true
		}
		if strings.HasPrefix(home, "/home"+string(os.PathSeparator)) &&
			(path == "/home" || strings.HasPrefix(path, "/home"+string(os.PathSeparator))) {
			return true
		}
	}
	return false
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// sandboxTool is the language profile that declares program, together with the
// declared program token its cache queries go to.
//
// The profile is configuration: the runner knows no language name, and a
// program no profile declares gets nothing rather than a guess.
func (c *Ctx) sandboxTool(program string) (Language, string, bool) {
	name := filepath.Base(program)
	if name == "" || name == "." || name == string(os.PathSeparator) {
		return Language{}, "", false
	}
	for _, language := range c.Languages {
		for _, declared := range declaredPrograms(language) {
			if filepath.Base(declared) == name {
				return language, declared, true
			}
		}
	}
	return Language{}, "", false
}

// declaredPrograms are the programs a language profile runs, in declaration
// order. They are what ties a run to a profile: the profile that declares the
// command also declares the caches that command needs.
func declaredPrograms(language Language) []string {
	var programs []string
	for _, command := range []string{language.TestCommand, language.CoverageCommand} {
		tokens, err := SplitCommand(command)
		if err != nil || len(tokens) == 0 {
			continue
		}
		programs = append(programs, tokens[0])
	}
	return programs
}

// expandSandboxValue substitutes the placeholders a profile value may carry:
// {rundir}, {cache} and {toolenv:NAME}. Anything else is a defect in the
// profile, not a value to guess.
//
// answers memoises the toolchain queries of one resolution: a profile names the
// module cache twice (as a mount and as the variable that points at it), and
// asking the toolchain once per name is enough.
func (c *Ctx) expandSandboxValue(value, dir, tool string, answers map[string]string, cacheRoot string) (string, *Verdict) {
	cache := cacheRoot
	if cache == "" {
		// No declaration: the build cache stays where it always was, inside the
		// throwaway checkout, and dies with it.
		cache = filepath.Join(dir, ".yesmem-gocache")
	}
	expanded := strings.ReplaceAll(value, RunDirPlaceholder, dir)
	expanded = strings.ReplaceAll(expanded, CacheDirPlaceholder, cache)
	expanded = strings.ReplaceAll(expanded, CacheBasePlaceholder, cacheBase())
	expanded = strings.ReplaceAll(expanded, HomePlaceholder, operatorHome())
	for pass := 0; pass < maxPlaceholderPasses; pass++ {
		start := strings.Index(expanded, toolEnvPrefix)
		if start < 0 {
			return expanded, nil
		}
		rest := expanded[start+len(toolEnvPrefix):]
		end := strings.Index(rest, "}")
		if end < 0 {
			problem := Unverifiable(fmt.Sprintf(
				"the language profile carries the unclosed placeholder %q", expanded[start:])).
				WithClass(ClassDefect)
			return "", &problem
		}
		name := rest[:end]
		if !envNameShape.MatchString(name) {
			problem := Unverifiable(fmt.Sprintf(
				"the language profile asks the toolchain for %q, which is not a variable name", name)).
				WithClass(ClassDefect)
			return "", &problem
		}
		answer, cached := answers[name]
		if !cached {
			var problem *Verdict
			answer, problem = c.toolEnv(dir, tool, name)
			if problem != nil {
				return "", problem
			}
			answers[name] = answer
		}
		expanded = expanded[:start] + answer + rest[end+1:]
	}
	problem := Unverifiable("the language profile's sandbox placeholders do not terminate").
		WithClass(ClassDefect)
	return "", &problem
}

// toolEnv asks the toolchain for one of its own paths.
//
// This is the operator's toolchain, not repository content: the program is
// resolved through the run's PATH, and a program that resolves into the run
// directory is refused. Otherwise a profile whose declared command is a
// repository-relative binary ("vendor/bin/phpunit") would run checkout code on
// the host, unsandboxed, with the operator's environment, just to learn a path.
// Only the `env` subcommand is executed. The query runs in an empty directory
// so a go.mod above the caller cannot make the tool answer for another module.
func (c *Ctx) toolEnv(dir, tool, name string) (string, *Verdict) {
	if tool == "" {
		problem := Unverifiable(
			"the language profile asks the toolchain for a cache path but declares no command to run it with").
			WithClass(ClassDefect)
		return "", &problem
	}
	program, found := ResolveProgram(dir, tool)
	if !found {
		problem := Unverifiable(fmt.Sprintf(
			"the language profile names %q, which is not available in the run's PATH; "+
				"its cache paths cannot be resolved", tool)).
			WithClass(ClassEnvironment)
		return "", &problem
	}
	if insideDir(program, dir) {
		problem := Unverifiable(fmt.Sprintf(
			"the language profile's program %q resolves to %q, which is inside the run directory; "+
				"the toolchain that answers for a cache path must be the operator's, not the checkout's",
			tool, program)).
			WithClass(ClassDefect)
		return "", &problem
	}
	scratch, err := os.MkdirTemp(c.TmpDir, "toolenv-")
	if err != nil {
		problem := Unverifiable(fmt.Sprintf(
			"cannot create a working directory to query %q: %v", tool, err)).
			WithClass(ClassEnvironment)
		return "", &problem
	}
	defer func() { _ = os.Remove(scratch) }()

	ctx, cancel := context.WithTimeout(c.Context(), ToolEnvTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, program, "env", name)
	cmd.Dir = scratch
	cmd.Env = os.Environ()
	cmd.Stdin = nil
	output, err := cmd.Output()
	if err != nil {
		problem := Unverifiable(fmt.Sprintf(
			"the language profile asks %q for %s, and it did not answer: %v", tool, name, err)).
			WithClass(ClassEnvironment)
		return "", &problem
	}
	answer := strings.TrimSpace(string(output))
	if newline := strings.IndexByte(answer, '\n'); newline >= 0 {
		answer = strings.TrimSpace(answer[:newline])
	}
	if answer == "" {
		problem := Unverifiable(fmt.Sprintf(
			"the language profile asks %q for %s, and it reported nothing", tool, name)).
			WithClass(ClassEnvironment)
		return "", &problem
	}
	return answer, nil
}

// sandboxOwnedEnvKeys are the variables the sandbox's own guarantees are
// written in terms of. PATH decides which binary runs, and RunPath strips the
// operator's directories from it; HOME decides which init files and user site
// directories the run reads, and PYTHONNOUSERSITE exists only because HOME is
// the checkout. PYTHONNOUSERSITE and PYTHONDONTWRITEBYTECODE close two specific
// doors of their own (a committed usercustomize.py, a committed __pycache__),
// and GIT_TERMINAL_PROMPT keeps a run from waiting on a credential prompt. BASH_ENV
// and ENV name a start-up file a shell sources, which is host code by another
// name. A profile may move TMPDIR, but not these.
var sandboxOwnedEnvKeys = map[string]bool{
	"PATH":                    true,
	"HOME":                    true,
	"PYTHONNOUSERSITE":        true,
	"PYTHONDONTWRITEBYTECODE": true,
	"GIT_TERMINAL_PROMPT":     true,
	"BASH_ENV":                true,
	"ENV":                     true,
}

// dynamicLoaderPrefixes name the variables through which the loader pulls host
// code into every child process a run starts.
var dynamicLoaderPrefixes = []string{"LD_", "DYLD_"}

// sandboxOwnedEnvPrefixes are gone on purpose: a prefix like PYTHON also
// forbids PYTHONUNBUFFERED, which changes nothing about what runs. The variables
// that decide which code or program the run loads are named exactly below.
//
// sandboxLoaderKeys are those variables: an interpreter, a runtime, a version
// control system or a helper reads them to find code outside the checkout. The
// sandbox exists to run the checkout's code, not the host's, so a profile may not
// set them.
var sandboxLoaderKeys = map[string]bool{
	"PYTHONPATH":        true,
	"PYTHONSTARTUP":     true,
	"PYTHONHOME":        true,
	"PYTHONUSERBASE":    true,
	"NODE_OPTIONS":      true,
	"NODE_PATH":         true,
	"GIT_DIR":           true,
	"GIT_WORK_TREE":     true,
	"GIT_SSH":           true,
	"GIT_SSH_COMMAND":   true,
	"GIT_EXTERNAL_DIFF": true,
	"GIT_ASKPASS":       true,
	"GIT_CONFIG":        true,
	"GIT_CONFIG_GLOBAL": true,
	"GIT_CONFIG_SYSTEM": true,
	// GIT_CONFIG_COUNT is the family that defines config inline
	// (GIT_CONFIG_KEY_<n>/VALUE_<n>) and needs no file; blocking the count is
	// enough to refuse the family.
	"GIT_CONFIG_COUNT":    true,
	"JDK_JAVA_OPTIONS":    true,
	"PERLLIB":             true,
	"SSH_ASKPASS_REQUIRE": true,
	"PERL5LIB":            true,
	"PERL5OPT":            true,
	"PERL5DB":             true,
	"RUBYOPT":             true,
	"RUBYLIB":             true,
	"CLASSPATH":           true,
	"JAVA_TOOL_OPTIONS":   true,
	"_JAVA_OPTIONS":       true,
	"SSH_AUTH_SOCK":       true,
	"SSH_ASKPASS":         true,
	"GCONV_PATH":          true,
	"LOCPATH":             true,
}

// refuseOwnedEnvKey rejects a profile that overrides a key the sandbox owns.
func refuseOwnedEnvKey(key string) *Verdict {
	upper := strings.ToUpper(key)
	for _, prefix := range dynamicLoaderPrefixes {
		if strings.HasPrefix(upper, prefix) {
			problem := Unverifiable(fmt.Sprintf(
				"the language profile sets %s, which the dynamic loader reads: "+
					"it would pull host code into every process the run starts", key)).
				WithClass(ClassDefect)
			return &problem
		}
	}
	if sandboxLoaderKeys[upper] {
		problem := Unverifiable(fmt.Sprintf(
			"the language profile sets %s, which decides which program or code the run loads; "+
				"that is the sandbox's decision, not a profile's", key)).
			WithClass(ClassDefect)
		return &problem
	}
	if !sandboxOwnedEnvKeys[upper] {
		return nil
	}
	problem := Unverifiable(fmt.Sprintf(
		"the language profile sets %s, which the sandbox itself owns; "+
			"the run's path, its home and its interpreter guards are decided by the verifier, not by a profile", key)).
		WithClass(ClassDefect)
	return &problem
}

// maskedHostDirs are the host trees the sandbox covers with an empty tmpfs
// before the checkout is bound back in: every temp directory is a readable drop
// box, and /run holds the host's runtime sockets (D-Bus, systemd, docker).
// --unshare-net separates IP networking, not AF_UNIX pathnames.
//
// A read-only bind applied after the tmpfs re-exposes that exact subtree, so a
// profile may not name anything under these roots: the mask is the sandbox's
// own decision, and a profile is configuration, not a way around it.
var maskedHostDirs = []string{"/tmp", "/var/tmp", "/run", "/proc", "/sys", "/dev"}

// maskedHostPath reports whether clean lies at or under a masked host root.
func maskedHostPath(clean string) bool {
	for _, root := range maskedHostDirs {
		if clean == root || strings.HasPrefix(clean, root+string(os.PathSeparator)) {
			return true
		}
	}
	return false
}

// sensitiveHomeDirs are the directories under a home that hold credentials
// rather than caches. A toolchain answer that lands there is not a cache, and the
// sandbox keeps those trees closed either way.
var sensitiveHomeDirs = []string{".ssh", ".gnupg", ".aws", ".config", ".claude", ".docker", ".kube", ".netrc"}

// sensitiveHomePath reports whether clean lies in one of the credential
// directories below home.
func sensitiveHomePath(clean, home string) bool {
	for _, dir := range sensitiveHomeDirs {
		prefix := filepath.Join(home, dir)
		if clean == prefix || strings.HasPrefix(clean, prefix+string(os.PathSeparator)) {
			return true
		}
	}
	return false
}

// toolchainNamedValue reports whether a read-only value is one {toolenv:NAME}
// placeholder and nothing else: the path is then the one the language's own
// toolchain reported for itself, not a path the profile composed. An appended
// path segment, a "..", or a second placeholder all make it composed again.
func toolchainNamedValue(value string) bool {
	if !strings.HasPrefix(value, toolEnvPrefix) || !strings.HasSuffix(value, "}") {
		return false
	}
	if strings.Count(value, toolEnvPrefix) != 1 {
		return false
	}
	name := strings.TrimSuffix(strings.TrimPrefix(value, toolEnvPrefix), "}")
	return envNameShape.MatchString(name)
}

// refuseUnsafeMount rejects a cache root that would undo the sandbox.
//
// Five paths are refused: one that is the operator's home or a parent of it,
// because the sandbox covers the home with an empty tmpfs and a mount would put
// the SSH keys and credentials back; one under the home that the profile
// composed itself, unless it is a tool bin directory, because only the toolchain
// and a named bin directory may open a subtree there; one that is the run
// directory or a parent of it, because it would shadow the one writable tree;
// one that lies inside the verified repository, because that is repository
// content and not a host tree; and one under, or above, a tree the sandbox
// itself masks.
func refuseUnsafeMount(path, runDir, repo string, toolchainNamed bool) *Verdict {
	reject := func(reason string) *Verdict {
		problem := Unverifiable(reason).WithClass(ClassDefect)
		return &problem
	}
	clean := filepath.Clean(path)
	if !filepath.IsAbs(clean) {
		return reject(fmt.Sprintf(
			"the language profile mounts %q as a cache root; a cache root must be an absolute host path", path))
	}
	if clean == string(os.PathSeparator) {
		return reject(fmt.Sprintf(
			"the language profile mounts %q as a cache root; the filesystem root cannot be one", path))
	}
	if maskedHostPath(clean) {
		problem := Unverifiable(fmt.Sprintf(
			"the language profile mounts %q, which the sandbox masks with an empty tmpfs; "+
				"putting that tree back would hand a run the host's temp files and runtime sockets", path))
		if toolchainNamed {
			// The toolchain itself reported this path: the host's cache happens to
			// live in a masked tree. That is the environment, not the project's
			// mistake, so it stays an environment gap instead of a defect.
			problem = problem.WithClass(ClassEnvironment)
		} else {
			problem = problem.WithClass(ClassDefect)
		}
		return &problem
	}
	for _, masked := range maskedHostDirs {
		if strings.HasPrefix(masked, clean+string(os.PathSeparator)) {
			return reject(fmt.Sprintf(
				"the language profile mounts %q, which contains the masked tree %q; "+
					"a read-only bind above it would put that tree back", path, masked))
		}
	}
	if insideDir(clean, repo) {
		return reject(fmt.Sprintf(
			"the language profile mounts %q, which lies inside the verified repository; "+
				"a run reads the checkout under judgement, not the operator's working tree", path))
	}
	if home := resolvedOperatorHome(); home != "" {
		home = filepath.Clean(home)
		switch {
		case clean == home || strings.HasPrefix(home, clean+string(os.PathSeparator)):
			return reject(fmt.Sprintf(
				"the language profile mounts %q, which is the operator's home or a parent of it; "+
					"the sandbox hides the home and must not have it put back", path))
		case strings.HasPrefix(clean, home+string(os.PathSeparator)):
			if toolchainNamed {
				if sensitiveHomePath(clean, home) {
					// The toolchain's answer is host data, not repository content, but a
					// cache that points at the credentials directory is not a cache. It
					// stays an environment gap: the host's layout is not the project's
					// mistake, and the run can still be judged without it.
					problem := Unverifiable(fmt.Sprintf(
						"the language profile mounts %q, which is a credentials directory; "+
							"the sandbox keeps those closed, so the run stays an environment gap", path)).
						WithClass(ClassEnvironment)
					return &problem
				}
				break
			}
			if toolBinDir(clean, home) {
				break
			}
			// A cache inside the home exists (the Go module cache), and either the
			// toolchain reports that path for itself or it is a tool bin directory.
			// A profile that composed any other path could name ~/.ssh instead, and
			// the shield covers everything below the home that is not bound back
			// explicitly.
			return reject(fmt.Sprintf(
				"the language profile mounts %q under the operator's home; only a path the "+
					"language's own toolchain reports (a {toolenv:...} value on its own) or a "+
					"tool bin directory may be bound there", path))
		}
		if homeParent := filepath.Dir(home); homeParent == "/home" &&
			strings.HasPrefix(clean, homeParent+string(os.PathSeparator)) &&
			!strings.HasPrefix(clean, home+string(os.PathSeparator)) {
			// SandboxPrefix covers /home as well when the home lives below it, so
			// another account's home is masked too. This mirrors that rule: only
			// /home is a shared accounts root, not whatever contains the home.
			return reject(fmt.Sprintf(
				"the language profile mounts %q, which belongs to another account under %q; "+
					"the sandbox masks every home below that root", path, homeParent))
		}
	}
	if runDir != "" {
		run := filepath.Clean(runDir)
		if resolved, err := filepath.EvalSymlinks(run); err == nil {
			run = resolved
		}
		if clean == run || strings.HasPrefix(run, clean+string(os.PathSeparator)) {
			return reject(fmt.Sprintf(
				"the language profile mounts %q, which would shadow the writable run directory", path))
		}
	}
	return nil
}

// toolBinDir reports whether path is a tool bin directory below the home: a real
// subdirectory at least two levels down, whose last segment is "bin", and whose
// way down crosses no credential directory.
//
// That is where a toolchain installed for one account lives (bun, cargo, nvm).
// The naming is deliberate: it is a property the guard can check, not a promise
// the profile makes, and everything else below the home stays behind the shield.
func toolBinDir(clean, home string) bool {
	relative := strings.TrimPrefix(clean, home+string(os.PathSeparator))
	if relative == clean || relative == "" {
		return false
	}
	segments := strings.Split(relative, string(os.PathSeparator))
	if len(segments) < 2 || segments[len(segments)-1] != "bin" {
		return false
	}
	for index := 1; index <= len(segments); index++ {
		prefix := filepath.Join(append([]string{home}, segments[:index]...)...)
		if sensitiveHomePath(prefix, home) {
			return false
		}
	}
	return true
}

// refuseUnsafeCache rejects a persistent build cache root.
//
// The cache is the one host tree a profile may have mounted writable, so every
// rule of a read-only mount applies and one more: a root that is a top-level
// directory of the home is refused, because a cache is a directory of its own
// and the top level holds the account's own configuration.
//
// owned says the path lies below the verifier's own cache root. The two rules
// that keep a profile out of the repository and out of the checkout then do not
// apply: when the verifier itself runs confined, its home is the checkout, so
// its own cache base lies inside the tree it is verifying.
func refuseUnsafeCache(path, runDir, repo string, owned bool) *Verdict {
	reject := func(reason string) *Verdict {
		problem := Unverifiable(reason).WithClass(ClassDefect)
		return &problem
	}
	clean := filepath.Clean(path)
	if !filepath.IsAbs(clean) {
		return reject(fmt.Sprintf(
			"the language profile's cache root %q must be an absolute host path", path))
	}
	if clean == string(os.PathSeparator) {
		return reject("the language profile's cache root cannot be the filesystem root")
	}
	if maskedHostPath(clean) {
		return reject(fmt.Sprintf(
			"the language profile's cache root %q lies in a tree the sandbox masks; "+
				"binding it writable would hand the run the host's temp files", path))
	}
	for _, masked := range maskedHostDirs {
		if strings.HasPrefix(masked, clean+string(os.PathSeparator)) {
			return reject(fmt.Sprintf(
				"the language profile's cache root %q contains the masked tree %q; "+
					"a writable bind above it would open that tree", path, masked))
		}
	}
	if !owned && insideDir(clean, repo) {
		return reject(fmt.Sprintf(
			"the language profile's cache root %q lies inside the verified repository; "+
				"a cache that carries the checkout's state into the next verdict is no cache", path))
	}
	if runDir != "" && !owned {
		run := filepath.Clean(runDir)
		if resolved, err := filepath.EvalSymlinks(run); err == nil {
			run = resolved
		}
		switch {
		case clean == run || strings.HasPrefix(clean, run+string(os.PathSeparator)):
			return reject(fmt.Sprintf(
				"the language profile's cache root %q lies inside the throwaway run directory; "+
					"it would die with the run", path))
		case strings.HasPrefix(run, clean+string(os.PathSeparator)):
			return reject(fmt.Sprintf(
				"the language profile's cache root %q contains the throwaway run directory; "+
					"a writable mount above it would shadow the checkout", path))
		}
	}
	if home := resolvedOperatorHome(); home != "" {
		home = filepath.Clean(home)
		switch {
		case clean == home || strings.HasPrefix(home, clean+string(os.PathSeparator)):
			return reject(fmt.Sprintf(
				"the language profile's cache root %q is the operator's home or a parent of it; "+
					"the sandbox hides the home and must not have it put back writable", path))
		case strings.HasPrefix(clean, home+string(os.PathSeparator)):
			if sensitiveHomePath(clean, home) {
				return reject(fmt.Sprintf(
					"the language profile's cache root %q is a credentials directory; "+
						"a cache is not a place for keys", path))
			}
			relative := strings.TrimPrefix(clean, home+string(os.PathSeparator))
			if len(strings.Split(relative, string(os.PathSeparator))) < 2 {
				return reject(fmt.Sprintf(
					"the language profile's cache root %q is a top-level directory of the "+
						"operator's home; name a dedicated directory below it", path))
			}
		}
		if homeParent := filepath.Dir(home); homeParent == "/home" &&
			strings.HasPrefix(clean, homeParent+string(os.PathSeparator)) &&
			!strings.HasPrefix(clean, home+string(os.PathSeparator)) {
			return reject(fmt.Sprintf(
				"the language profile's cache root %q belongs to another account under %q; "+
					"the sandbox masks every home below that root", path, homeParent))
		}
	}
	return nil
}

// insideDir reports whether path resolves into dir. Both sides are resolved:
// a checkout that is reachable through a symlink must not let a host program
// pass for the operator's.
func insideDir(path, dir string) bool {
	if dir == "" {
		return false
	}
	resolvedPath, resolvedDir := path, dir
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		resolvedPath = resolved
	}
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		resolvedDir = resolved
	}
	resolvedDir = filepath.Clean(resolvedDir)
	return resolvedPath == resolvedDir ||
		strings.HasPrefix(resolvedPath, resolvedDir+string(os.PathSeparator))
}
