package engine

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	// DefaultTestTimeout bounds one command run. The run's own budget can
	// shorten it, never lengthen it. It carries headroom rather than a measured
	// minimum: a project that checks its whole Go suite spends over five minutes
	// inside the sandbox once the machine is busy (measured 2026-11-10 on a
	// loaded box, yesmem profile with 44 packages), and the same run finishes
	// under five on an idle one. A cap that is merely tight turns a green
	// project into an unverifiable DONE, which is the worse failure of the two.
	DefaultTestTimeout = 900 * time.Second
	// SandboxProbeTimeout bounds the check that bubblewrap can start at all.
	SandboxProbeTimeout = 10 * time.Second
	// TailBytes is how much output a verdict shows.
	TailBytes = 64 << 10
	// HeadBytes is how much of the start of an output is kept for a classifier.
	// Compilers print their diagnostics first, so a marker search over the tail
	// alone would miss them.
	HeadBytes = 16 << 10
	// MaxOutputBytes is the largest output a run may produce before it is
	// killed. More is refused, never truncated into a verdict.
	MaxOutputBytes = 16 << 20
)

var (
	urlShape         = regexp.MustCompile(`\A[A-Za-z][A-Za-z0-9+.-]*://`)
	optionNameShape  = regexp.MustCompile(`\A[A-Za-z0-9][A-Za-z0-9-]*\z`)
	wrapperCommands  = map[string]bool{"make": true, "npm": true, "yarn": true, "bun": true, "pnpm": true, "npx": true, "composer": true}
	shellMetachars   = " \t\n\r;&|$`<>(){}'\"\\"
	tokenMetachars   = " \t\n\r;&|$`<>(){}[]"
	dangerousOptions = map[string]bool{
		"eval": true, "exec": true, "config": true, "script-shell": true,
		"node-options": true, "preload": true, "require": true, "loader": true,
		"experimental-loader": true, "import": true, "test-reporter": true,
		"toolexec": true,
	}
)

// FindBwrap is the bubblewrap binary, or "" when it is not available.
//
// The lookup uses the run's PATH, never the caller's: a binary named bwrap in
// an operator-writable directory would report "sandboxed" while confining
// nothing. A bwrap under the operator's home is refused for the same reason.
func FindBwrap() string {
	resolved, found := LookPathIn(RunPath(), "bwrap")
	if !found || underOperatorHome(resolved) {
		return ""
	}
	return resolved
}

// underOperatorHome reports whether a path lives in the operator's home.
func underOperatorHome(path string) bool {
	home := operatorHome()
	if home == "" {
		return false
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		resolved = path
	}
	return resolved == home || strings.HasPrefix(resolved, home+string(os.PathSeparator))
}

// ResolveProgram resolves the program a run will execute.
//
// An absolute path stays as it is, a repository-relative path (bin/console,
// vendor/bin/phpunit) must exist in the checkout, and a bare name must be
// found in the run's PATH. Go's own exec.Command would resolve the name
// against the caller's PATH, which is exactly the directory the run must not
// trust.
func ResolveProgram(checkoutDir, program string) (string, bool) {
	if filepath.IsAbs(program) {
		return program, true
	}
	if strings.Contains(program, string(os.PathSeparator)) {
		candidate := filepath.Join(checkoutDir, program)
		if info, err := os.Stat(candidate); err != nil || info.IsDir() {
			return "", false
		}
		return candidate, true
	}
	resolved, found := LookPathIn(RunPath(), program)
	if !found {
		return "", false
	}
	return resolved, true
}

// SandboxPrefix is the bubblewrap wrapper for one run.
//
// The filesystem root is bound read-only and only the throwaway checkout is
// writable, plus the cache roots a profile declares: those are the one writable
// tree that outlives a run, and they are bound after the read-only mounts so the
// order of the arguments matches the intent. The run gets its own network, PID
// and UTS namespaces, so it can neither reach the host network nor see host
// processes. --die-with-parent keeps a sandbox from outliving the verifier, and
// an empty /run hides the host's runtime sockets (D-Bus, systemd, docker):
// --unshare-net separates IP networking, not AF_UNIX pathname sockets.
func SandboxPrefix(bwrap, checkout string, extraReadOnly, extraWritable [][2]string, cwd string) []string {
	prefix := []string{
		bwrap,
		"--die-with-parent",
		"--ro-bind", "/", "/",
		"--dev", "/dev",
		"--proc", "/proc",
		"--tmpfs", "/run",
		// Every host temp directory is a readable drop box: a socket or a
		// copied secret there would land in a verdict. The run gets its own
		// TMPDIR inside the checkout.
		"--tmpfs", "/tmp",
		"--tmpfs", "/var/tmp",
	}
	if home := operatorHome(); home != "" {
		// The read-only root leaves the operator's home readable: SSH keys,
		// tool credentials, this verifier's own store. A repository-controlled
		// command must not be able to print them into a verdict, so the home
		// is covered by an empty tmpfs before the checkout is bound back in.
		prefix = append(prefix, "--tmpfs", home)
		if strings.HasPrefix(home, "/home/") {
			// Every other account's home is reachable through the same root.
			prefix = append(prefix, "--tmpfs", "/home")
		}
	}
	prefix = append(prefix, "--bind", checkout, checkout)
	for _, mount := range extraReadOnly {
		prefix = append(prefix, "--ro-bind", mount[0], mount[1])
	}
	for _, mount := range extraWritable {
		prefix = append(prefix, "--bind", mount[0], mount[1])
	}
	if cwd == "" {
		cwd = checkout
	}
	return append(prefix,
		"--unshare-net",
		"--unshare-pid",
		"--unshare-uts",
		"--chdir", cwd,
		"--",
	)
}

// SandboxDisplay shows a sandboxed command line in a verdict.
//
// The operator's home path is rendered as <home>: a verdict travels to logs and
// messages, and the account layout is not part of the judgement.
func SandboxDisplay(command string) string {
	return SandboxDisplayWith(command, nil, nil)
}

// SandboxDisplayWith is SandboxDisplay including the mounts the run got: the
// verdict is the audit trail, so a reader must be able to see which host trees
// the run was allowed to read, and which cache root it was allowed to write. A
// mount inside the home keeps the same <home> rendering as the rest of the line.
func SandboxDisplayWith(command string, mounts, caches [][2]string) string {
	args := SandboxPrefix("bwrap", "<checkout>", mounts, caches, "")
	// Two forms of the home reach this line: the prefix carries the home as the
	// shield argument, the mounts carry it resolved. Both are the account
	// layout, and neither belongs in a verdict.
	homes := make([]string, 0, 2)
	for _, candidate := range []string{operatorHome(), resolvedOperatorHome()} {
		if candidate == "" {
			continue
		}
		duplicate := false
		for _, seen := range homes {
			if seen == candidate {
				duplicate = true
				break
			}
		}
		if !duplicate {
			homes = append(homes, candidate)
		}
	}
	rendered := make([]string, 0, len(args))
	for _, arg := range args {
		for _, home := range homes {
			if arg == home || strings.HasPrefix(arg, home+string(os.PathSeparator)) {
				// Keep the subtree: a mounted cache root must stay readable in
				// the verdict, and only the account layout is not part of it.
				arg = "<home>" + strings.TrimPrefix(arg, home)
				break
			}
		}
		rendered = append(rendered, arg)
	}
	return FormatCommand(rendered) + " " + command
}

// SandboxProbe returns nil when bubblewrap can start the sandbox.
//
// A bubblewrap that exists but cannot create its namespaces is not available
// in any meaningful sense; probing separates "the sandbox does not work" from
// "the command failed", which would otherwise turn every run into a false
// refutation.
func SandboxProbe(bwrap, checkout string) error {
	return sandboxProbeWith(bwrap, checkout, nil, nil, nil)
}

// sandboxProbeWith probes the sandbox a run will actually use, mounts and
// environment included: a cache root that cannot be bound fails here, before
// the command starts, and reads as "the sandbox does not work" rather than as a
// test outcome.
func sandboxProbeWith(bwrap, checkout string, mounts, caches [][2]string, extraEnv []string) error {
	probe := "/bin/true"
	if _, err := os.Stat(probe); err != nil {
		probe = "/bin/sh"
	}
	args := append(SandboxPrefix(bwrap, checkout, mounts, caches, ""), probe)
	if probe == "/bin/sh" {
		args = append(args, "-c", "exit 0")
	}
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Dir = checkout
	cmd.Env = TestEnvWith(checkout, extraEnv)

	done := make(chan error, 1)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("the sandbox probe could not run: %w", err)
	}
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			return fmt.Errorf("the sandbox probe failed: %w", err)
		}
		return nil
	case <-time.After(SandboxProbeTimeout):
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		<-done
		return fmt.Errorf("the sandbox probe timed out after %s", SandboxProbeTimeout)
	}
}

// TestEnv is the minimal environment of a run: the command must not read the
// verifier's environment, and HOME points at the throwaway checkout.
//
// TMPDIR is a subdirectory of the checkout, never its root: a toolchain that
// treats its own module root as the system temp directory (Go does) would
// otherwise refuse to work, and an honest report would be refuted.
func TestEnv(checkout string) []string {
	return TestEnvWith(checkout, nil)
}

// TestEnvWith is TestEnv plus the language profile's environment. A key the
// profile sets replaces the minimal one of the same name: the profile speaks
// for the toolchain, and two entries for one key would let the child pick.
func TestEnvWith(checkout string, extra []string) []string {
	env := []string{
		"PATH=" + RunPath(),
		"HOME=" + checkout,
		"TMPDIR=" + scratchDir(checkout),
		"GIT_TERMINAL_PROMPT=0",
		// Without this, HOME=<checkout> would let a committed
		// .local/lib/python*/site-packages/usercustomize.py inject code.
		"PYTHONNOUSERSITE=1",
		"PYTHONDONTWRITEBYTECODE=1",
	}
	for _, key := range []string{"LANG", "LC_ALL", "LC_CTYPE"} {
		if value, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+value)
		}
	}
	for _, entry := range extra {
		key, _, found := strings.Cut(entry, "=")
		if !found {
			continue
		}
		kept := env[:0]
		for _, existing := range env {
			if existingKey, _, _ := strings.Cut(existing, "="); existingKey == key {
				continue
			}
			kept = append(kept, existing)
		}
		env = append(kept, entry)
	}
	return env
}

// scratchDir is the writable temp directory of one run, inside the checkout so
// it disappears with it.
func scratchDir(checkout string) string {
	dir := filepath.Join(checkout, ".verify-tmp")
	_ = os.MkdirAll(dir, 0o700)
	return dir
}

// SandboxTmpDir is the private tmpfs every sandbox gets, and the temp directory
// a language profile may point a run at.
//
// The default temp directory of a run lives inside the checkout, a path the
// caller chooses and which is therefore arbitrarily deep. A suite that binds a
// unix socket or derives a file name from its temp path breaks there: the
// kernel refuses a socket path beyond about 108 bytes, and a flattened name is
// limited by the filesystem. Both failures read as a failing suite, which would
// refute an honest report. This path is short and still private to the run.
const SandboxTmpDir = "/tmp"

// operatorHome is the home directory the sandbox hides, or "" when there is
// none to hide.
func operatorHome() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" || home == string(os.PathSeparator) {
		// A daemon started by systemd may carry no HOME; the passwd entry is
		// the home the run must not see either.
		if current, userErr := user.Current(); userErr == nil {
			home = current.HomeDir
		}
	}
	if home == "" || home == string(os.PathSeparator) {
		return ""
	}
	return home
}

// resolvedOperatorHome is the home the sandbox hides, with symlinks resolved.
//
// The shield and the mounts must be judged against the same path: a relocated
// account (a symlinked home) would otherwise let one side name a tree the other
// does not cover.
func resolvedOperatorHome() string {
	home := operatorHome()
	if home == "" {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(home); err == nil {
		return resolved
	}
	return home
}

// RunPath is the PATH a run gets: the caller's PATH without the directories
// under the operator's home.
//
// A user-writable directory ahead of the system paths can shadow the runner
// itself (~/.local/bin/python3, a virtualenv shim), which would let code
// outside the checked commit decide the verdict. The host's own toolchain
// sometimes lives there, so the filter is a documented boundary of this gate,
// not a claim that no shadowing is possible.
func RunPath() string {
	home := operatorHome()
	var kept []string
	for _, dir := range strings.Split(os.Getenv("PATH"), string(os.PathListSeparator)) {
		if dir == "" || !filepath.IsAbs(dir) {
			continue
		}
		if home != "" && (dir == home || strings.HasPrefix(dir, home+string(os.PathSeparator))) {
			continue
		}
		kept = append(kept, dir)
	}
	if len(kept) == 0 {
		// A PATH that was entirely operator-owned leaves the standard system
		// directories, never an empty search path.
		return "/usr/local/bin:/usr/bin:/bin"
	}
	return strings.Join(kept, string(os.PathListSeparator))
}

// LookPathIn resolves a program against one explicit PATH string.
func LookPathIn(path, program string) (string, bool) {
	if strings.Contains(program, string(os.PathSeparator)) {
		return "", false
	}
	for _, dir := range strings.Split(path, string(os.PathListSeparator)) {
		if dir == "" || !filepath.IsAbs(dir) {
			continue
		}
		if resolved, ok := programIn(dir, program); ok {
			return resolved, true
		}
	}
	return "", false
}

// programIn reports the executable named program in dir, if there is one.
func programIn(dir, program string) (string, bool) {
	candidate := filepath.Join(dir, program)
	info, err := os.Stat(candidate)
	if err != nil || info.IsDir() || info.Mode().Perm()&0o111 == 0 {
		return "", false
	}
	return candidate, true
}

// resolveProgramForSandbox resolves the program a confined run will execute.
//
// RunPath keeps the operator's writable directories out of the lookup on the
// host, but the PATH of the calling process can still name a tree the sandbox
// masks: a toolchain that prepends its own bin directory does exactly that, and
// Go does. Choosing such a candidate killed the run at exec, which reads as a
// failing suite and refutes an honest report. A candidate behind a mask is
// therefore skipped unless a mount re-opens its directory; when none is
// reachable, the verdict names the blocked one instead of blaming the report.
func resolveProgramForSandbox(dir, program string, mounts [][2]string) (string, bool, *Verdict) {
	if filepath.IsAbs(program) {
		resolved := program
		if target, err := filepath.EvalSymlinks(program); err == nil {
			resolved = target
		}
		if maskedInSandbox(resolved, dir) && !mountCovers(filepath.Dir(resolved), mounts) {
			return "", false, maskedProgramVerdict(resolved)
		}
		return program, true, nil
	}
	if strings.Contains(program, string(os.PathSeparator)) {
		candidate := filepath.Join(dir, program)
		if info, err := os.Stat(candidate); err != nil || info.IsDir() {
			return "", false, nil
		}
		return candidate, true, nil
	}
	// The mounted tool directories come after the run's own PATH: a mount can
	// supply a program the system does not have, never shadow one it has.
	entries := strings.Split(RunPath(), string(os.PathListSeparator))
	for _, mount := range mounts {
		if filepath.Base(mount[1]) == "bin" && !containsString(entries, mount[1]) {
			entries = append(entries, mount[1])
		}
	}
	blocked := ""
	for _, entry := range entries {
		if entry == "" || !filepath.IsAbs(entry) {
			continue
		}
		candidate, ok := programIn(entry, program)
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
		return candidate, true, nil
	}
	if blocked != "" {
		return "", false, maskedProgramVerdict(blocked)
	}
	return "", false, nil
}

// maskedProgramVerdict names a program the confinement hides.
func maskedProgramVerdict(path string) *Verdict {
	problem := Unverifiable(fmt.Sprintf(
		"the program %q resolves to %q, which the sandbox masks; the profile mounts no "+
			"directory for it, so the run cannot start there", filepath.Base(path), path)).
		WithClass(ClassEnvironment)
	return &problem
}

// mountCovers reports whether a mount re-opens dir, or a tree above it.
func mountCovers(dir string, mounts [][2]string) bool {
	for _, mount := range mounts {
		if insideDir(dir, mount[0]) {
			return true
		}
	}
	return false
}

// IsAllowed reports whether a command starts with an allowlisted prefix.
func IsAllowed(command string, allowlist []string) bool {
	normalized := strings.Join(strings.Fields(command), " ")
	for _, prefix := range allowlist {
		if normalized == prefix || strings.HasPrefix(normalized, prefix+" ") {
			return true
		}
	}
	return false
}

// AllowedByTokens matches an allowlist on the argv tokens that actually
// execute.
//
// Token equality, not string prefix: an allowlisted prefix must be exactly the
// leading tokens of the argv, so the string that is compared is the string that
// will run. No whitespace normalisation may let an allowlisted-looking command
// execute as something else.
func AllowedByTokens(argv, allowlist []string) bool {
	for _, prefix := range allowlist {
		tokens, err := SplitCommand(prefix)
		if err != nil || len(tokens) == 0 {
			continue
		}
		if len(argv) < len(tokens) {
			continue
		}
		matched := true
		for index, token := range tokens {
			if argv[index] != token {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}
	return false
}

// PrepareCommand validates a report command: the allowlist gate, the argv parse
// and the escape rules are the same for every command claim; only the allowlist
// differs.
func PrepareCommand(command string, allowlist []string) (argv []string, strict bool, verdict *Verdict) {
	if !IsAllowed(command, allowlist) {
		problem := Unverifiable(fmt.Sprintf("command is not in the allowlist: %q", command)).
			WithCommand(command).
			WithClass(ClassEnvironment)
		return nil, false, &problem
	}
	return prepareAllowedCommand(command)
}

// PrepareProfileCommand is PrepareCommand for a claim family whose command the
// project's own profile names: the allowlist stays the gate, and exactly those
// profile entries are admitted beside it.
func PrepareProfileCommand(command string, allowlist, profileCommands []string) (argv []string, strict bool, verdict *Verdict) {
	if !IsAllowed(command, allowlist) && !IsProfileCommand(command, profileCommands) {
		reason := fmt.Sprintf("command is not in the allowlist: %q", command)
		if len(profileCommands) > 0 {
			// The agent retries the same shape and spends another run when the
			// refusal does not say what the profile would have accepted.
			reason += "; the project profile allows exactly " + quoteCommands(profileCommands)
		}
		problem := Unverifiable(reason).
			WithCommand(command).
			WithClass(ClassEnvironment)
		return nil, false, &problem
	}
	return prepareAllowedCommand(command)
}

// IsProfileCommand reports whether a command is exactly one of the project's own
// entries.
//
// Token equality over the whole argv, not a prefix and no pattern: the profile
// names the command its stack runs, and an extra argument changes what runs, so
// only the entry itself is admitted.
func IsProfileCommand(command string, profileCommands []string) bool {
	argv, err := SplitCommand(command)
	if err != nil || len(argv) == 0 {
		return false
	}
	for _, entry := range profileCommands {
		expected, err := SplitCommand(entry)
		if err != nil || len(expected) == 0 || len(expected) != len(argv) {
			continue
		}
		matched := true
		for index := range expected {
			if argv[index] != expected[index] {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}
	return false
}

// quoteCommands names the profile's entries in a reason, quoted so a command
// with arguments stays readable.
func quoteCommands(commands []string) string {
	quoted := make([]string, 0, len(commands))
	for _, command := range commands {
		quoted = append(quoted, fmt.Sprintf("%q", command))
	}
	return strings.Join(quoted, ", ")
}

// prepareAllowedCommand is the argv parse and the escape rules every admitted
// command passes.
func prepareAllowedCommand(command string) (argv []string, strict bool, verdict *Verdict) {
	argv, err := SplitCommand(command)
	if err != nil {
		problem := Unverifiable("could not parse command: " + err.Error()).
			WithCommand(command).
			WithClass(ClassDefect)
		return nil, false, &problem
	}
	if len(argv) == 0 {
		problem := Unverifiable("empty command").
			WithCommand(command).
			WithClass(ClassDefect)
		return nil, false, &problem
	}
	strict = IsWrapper(argv)
	for _, arg := range argv {
		if ArgEscapesCheckout(arg, strict) {
			// Only the working directory is confined; an argument pointing
			// outside the fresh checkout would run code the verified commit
			// never contained.
			problem := Unverifiable(fmt.Sprintf("unsafe command argument: %q", arg)).
				WithCommand(command).
				WithClass(ClassDefect)
			return nil, strict, &problem
		}
	}
	return argv, strict, nil
}

// IsWrapper reports whether a command re-parses its arguments through a shell.
func IsWrapper(argv []string) bool {
	return len(argv) > 0 && wrapperCommands[filepath.Base(argv[0])]
}

// ArgEscapesCheckout reports whether one argument can reach outside the
// checkout.
func ArgEscapesCheckout(arg string, strict bool) bool {
	_, ok := pathCandidates(arg, strict)
	return !ok
}

// SymlinkEscape returns an argument piece whose real path leaves the checkout.
//
// A committed symlink would otherwise run code outside the checkout even though
// the argument is lexically harmless.
func SymlinkEscape(argv []string, checkout string, strict bool) string {
	root, err := filepath.EvalSymlinks(checkout)
	if err != nil {
		root = checkout
	}
	for _, arg := range argv {
		pieces, ok := pathCandidates(arg, strict)
		if !ok {
			continue
		}
		for _, piece := range pieces {
			if piece == "" || strings.HasPrefix(piece, "-") {
				continue
			}
			resolved, err := filepath.EvalSymlinks(filepath.Join(root, piece))
			if err != nil {
				continue
			}
			if resolved != root && !strings.HasPrefix(resolved, root+string(os.PathSeparator)) {
				return piece
			}
		}
	}
	return ""
}

// pathCandidates returns the path-like pieces of one argv element. The second
// result is false when the element must be rejected, and a rejected element
// carries no pieces at all; an accepted element with nothing path-like carries
// an empty slice.
//
// A report-supplied argument must not reach outside the fresh checkout. The
// verifier runs no shell, but wrappers hand values and arguments to one, so for
// those commands every token is held to shell-word rules; direct runners parse
// their own argv and only need the path rules. Plain tokens, key=value tokens,
// long options with abbreviations, attached and clustered short-option values,
// whitespace and comma-split values are all checked.
func pathCandidates(arg string, strict bool) ([]string, bool) {
	metachars := tokenMetachars
	if !strict {
		metachars = ""
	}
	norm := strings.ReplaceAll(arg, "\\", "/")
	if norm == "-" || norm == "--" {
		return []string{}, true
	}

	var pieces []string
	switch {
	case strings.HasPrefix(norm, "--"):
		name, value, hasValue := strings.Cut(norm[2:], "=")
		if !optionNameShape.MatchString(name) {
			return nil, false
		}
		if dangerousOption(name, true) {
			return nil, false
		}
		if strings.ContainsAny(norm[2:], shellMetachars) {
			return nil, false
		}
		if hasValue {
			pieces = append([]string{value}, splitValues(value)...)
		}
	case strings.HasPrefix(norm, "-"):
		body := norm[1:]
		if body == "" {
			return []string{}, true
		}
		if dangerousOption(strings.SplitN(body, "=", 2)[0], false) {
			return nil, false
		}
		if strings.ContainsAny(body, shellMetachars) {
			return nil, false
		}
		if value, _, hasValue := strings.Cut(body, "="); hasValue {
			// Attached forms: -C/abs, -C../x=1. Both readings are checked.
			pieces = append([]string{value}, splitValues(value)...)
			pieces = append(pieces, body[1:])
		} else if len(body) > 1 {
			// Attached and clustered forms: -sVALUE, -C.., -kf/path. Every
			// suffix that follows an option letter is a possible value.
			for index := 0; index < len(body); index++ {
				pieces = append(pieces, body[index+1:])
			}
		}
	default:
		if strings.ContainsAny(norm, metachars) {
			return nil, false
		}
		head, value, hasValue := strings.Cut(norm, "=")
		if hasValue {
			pieces = append([]string{head, value}, splitValues(value)...)
		} else {
			pieces = []string{norm}
		}
	}

	for _, piece := range pieces {
		if strings.HasPrefix(piece, "-") && piece != "-" && piece != "--" {
			if _, ok := pathCandidates(piece, strict); !ok {
				return nil, false
			}
			continue
		}
		if candidateEscapes(piece, strict) {
			return nil, false
		}
	}
	if pieces == nil {
		return []string{}, true
	}
	return pieces, true
}

func splitValues(value string) []string {
	var out []string
	for _, part := range strings.Split(value, ",") {
		if part != "" {
			out = append(out, part)
		}
	}
	for _, part := range strings.Fields(value) {
		out = append(out, part)
	}
	return out
}

func dangerousOption(name string, allowShortPrefix bool) bool {
	name = strings.ToLower(name)
	if dangerousOptions[name] {
		return true
	}
	if allowShortPrefix {
		// Any abbreviation of a long option ("--ev" is "--eval").
		if len(name) < 2 {
			return false
		}
	} else if len(name) < 4 {
		return false
	}
	for option := range dangerousOptions {
		if strings.HasPrefix(option, name) {
			return true
		}
	}
	return false
}

func candidateEscapes(piece string, strict bool) bool {
	if strings.HasPrefix(piece, "/") {
		return true
	}
	for _, part := range strings.Split(piece, "/") {
		if part == ".." {
			return true
		}
	}
	if urlShape.MatchString(piece) {
		return true
	}
	// ~user expands via passwd inside shells and tools, not via HOME.
	return strict && strings.Contains(piece, "~")
}

// SplitCommand splits a report command into argv like a POSIX shell would for
// quoting purposes. It never expands anything: a report command is data.
func SplitCommand(command string) ([]string, error) {
	var (
		args    []string
		current strings.Builder
		started bool
		quote   rune
	)
	for _, char := range command {
		switch {
		case quote == '\'':
			if char == '\'' {
				quote = 0
				continue
			}
			current.WriteRune(char)
		case quote == '"':
			if char == '"' {
				quote = 0
				continue
			}
			if char == '\\' {
				current.WriteRune(char)
				started = true
				continue
			}
			current.WriteRune(char)
		case char == '\'' || char == '"':
			quote = char
			started = true
		case char == '\\':
			quote = '\\'
			started = true
		case quote == '\\':
			quote = 0
			current.WriteRune(char)
			started = true
		case char == ' ' || char == '\t' || char == '\n' || char == '\r':
			if started {
				args = append(args, current.String())
				current.Reset()
				started = false
			}
		default:
			current.WriteRune(char)
			started = true
		}
	}
	if quote != 0 && quote != '\\' {
		return nil, fmt.Errorf("unterminated quote")
	}
	if started {
		args = append(args, current.String())
	}
	return args, nil
}

// RunOutcome is the observable result of one sandboxed run in a throwaway
// checkout. The checkout is already removed when the caller sees this, so only
// its path string is meaningful.
type RunOutcome struct {
	CommandDesc string
	Code        int
	RawOutput   string
	// Head is the start of the output, kept for classifiers that look for a
	// marker a long test summary would push out of the tail.
	Head      string
	Output    string
	Sandboxed bool
	Note      string
}

// cappedWriter keeps the tail of a stream, the head of a stream and counts
// everything.
type cappedWriter struct {
	mu       sync.Mutex
	tail     []byte
	head     []byte
	total    int64
	exceeded bool
	// kill stops the run once it has produced more than MaxOutputBytes. The
	// limit is meant to stop work, not to measure it.
	kill func()
}

func (w *cappedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.total += int64(len(p))
	if w.total > MaxOutputBytes && !w.exceeded {
		w.exceeded = true
		if w.kill != nil {
			w.kill()
		}
	}
	if room := HeadBytes - len(w.head); room > 0 {
		take := room
		if take > len(p) {
			take = len(p)
		}
		w.head = append(w.head, p[:take]...)
	}
	if len(p) >= TailBytes {
		w.tail = append(w.tail[:0], p[len(p)-TailBytes:]...)
	} else {
		w.tail = append(w.tail, p...)
		if len(w.tail) > TailBytes {
			w.tail = w.tail[len(w.tail)-TailBytes:]
		}
	}
	return len(p), nil
}

func (w *cappedWriter) snapshot() (raw string, exceeded bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return string(w.tail), w.exceeded
}

// headSnapshot is the start of the stream.
//
// A classifier that looks for a marker cannot rely on the tail alone: a compiler
// prints its diagnostics first and a long test summary after them, so the marker
// would fall out of a 64 KiB tail.
func (w *cappedWriter) headSnapshot() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return string(w.head)
}

// RunInCheckout runs argv in a throwaway checkout of commit, sandboxed.
//
// This is the shared engine of every command claim: the sandbox decision, the
// clone and checkout of the claimed commit, the optional preRun hook, the
// sandbox probe, the run itself and the capped read-back all live here once. A
// failure before or during the start returns the verdict to report.
func (c *Ctx) RunInCheckout(label, commandText, commit string, argv []string) (*RunOutcome, *Verdict) {
	return c.RunInCheckoutHook(label, commandText, commit, argv, nil)
}

// RunInCheckoutHook is RunInCheckout with a hook that runs inside the checkout
// before the sandbox starts. A returned verdict aborts the run.
func (c *Ctx) RunInCheckoutHook(label, commandText, commit string, argv []string, preRun func(dir, commandDesc string) *Verdict) (*RunOutcome, *Verdict) {
	mode, bwrap, abort := c.sandboxSetup(commandText)
	if abort != nil {
		return nil, abort
	}

	checkout, failure := c.NewCheckout(label, commit)
	if failure != nil {
		return nil, failure
	}
	defer checkout.Close()
	commandDesc := checkout.Prefix + " && " + commandText

	if preRun != nil {
		if abort := preRun(checkout.Dir, commandDesc); abort != nil {
			return nil, abort
		}
	}
	return c.runCommand(mode, bwrap, checkout.Dir, checkout.Prefix, commandText, argv, 0)
}

// RunInDir runs argv in an existing directory with the same confinement, the
// same limits and the same environment as a checkout run.
//
// It exists for a claim that runs many commands against one throwaway tree:
// cloning per run would also drop the build cache the tree carries, and a
// mutation claim runs the suite once per mutant.
func (c *Ctx) RunInDir(dir, commandText string, argv []string) (*RunOutcome, *Verdict) {
	return c.RunInDirWithin(dir, commandText, argv, 0)
}

// RunInDirWithin is RunInDir with an explicit timeout. A zero timeout means the
// package default; the run's own budget can shorten either, never lengthen it.
func (c *Ctx) RunInDirWithin(dir, commandText string, argv []string, timeout time.Duration) (*RunOutcome, *Verdict) {
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		problem := Unverifiable(fmt.Sprintf("run directory does not exist: %s", dir)).
			WithCommand(commandText).
			WithClass(ClassEnvironment)
		return nil, &problem
	}
	if len(argv) == 0 {
		problem := Unverifiable("empty command").
			WithCommand(commandText).
			WithClass(ClassDefect)
		return nil, &problem
	}
	mode, bwrap, abort := c.sandboxSetup(commandText)
	if abort != nil {
		return nil, abort
	}
	return c.runCommand(mode, bwrap, dir, "<checkout>", commandText, argv, timeout)
}

// sandboxSetup resolves the confinement of one run: the mode check, the
// bubblewrap lookup and the budget check. A returned verdict aborts the run.
func (c *Ctx) sandboxSetup(commandText string) (mode SandboxMode, bwrap string, abort *Verdict) {
	mode = c.Sandbox
	if mode == "" {
		mode = SandboxRequire
	}
	if mode != SandboxRequire && mode != SandboxOff {
		// A typo like "Require" must not degrade into an unsandboxed run.
		problem := Unverifiable(fmt.Sprintf("unknown sandbox mode: %q", mode)).
			WithCommand(commandText).
			WithClass(ClassEnvironment)
		return mode, "", &problem
	}
	if mode == SandboxRequire {
		bwrap = FindBwrap()
		if bwrap == "" {
			// require is a hard gate: without bubblewrap the command is never
			// run, not even unsandboxed. A fallback would be silent by
			// construction.
			problem := Unverifiable(
				"sandbox required but bubblewrap is not available in PATH; refusing to run the command unsandboxed").
				WithCommand(commandText).
				WithClass(ClassEnvironment)
			return mode, "", &problem
		}
	}
	if c.BudgetSpent() {
		problem := Unverifiable(fmt.Sprintf(
			"the run budget was exhausted before %q could start (built-in limit)", commandText)).
			WithCommand(commandText).
			WithClass(ClassLimit)
		return mode, bwrap, &problem
	}
	return mode, bwrap, nil
}

// runCommand is the shared core of every command run: the sandbox probe, the
// program resolution, the run itself and the capped read-back.
func (c *Ctx) runCommand(mode SandboxMode, bwrap, dir, displayPrefix, commandText string, argv []string, limit time.Duration) (*RunOutcome, *Verdict) {
	commandDesc := displayPrefix + " && " + commandText
	var sandboxError error
	// The language profile's cache mounts belong to the confined run: they are
	// resolved for the sandbox only, so an unsandboxed caller keeps the plain
	// environment it had.
	var mounts [][2]string
	var caches [][2]string
	var extraEnv []string
	if bwrap != "" {
		var abort *Verdict
		mounts, caches, extraEnv, abort = c.SandboxExtras(dir, argv[0])
		if abort != nil {
			named := abort.WithCommand(commandDesc)
			return nil, &named
		}
		// The declared tools are checked against the sandbox that is about to
		// start, and the mounted tool directories join the run's PATH: without
		// this check a missing program reads as a failing suite, which would
		// refute a report for a boundary of the machine.
		if abort := c.requireTools(dir, argv[0], mounts); abort != nil {
			named := abort.WithCommand(commandDesc)
			return nil, &named
		}
		extraEnv = append(extraEnv, toolPathEnv(mounts)...)
		sandboxError = sandboxProbeWith(bwrap, dir, mounts, caches, extraEnv)
	}
	if mode == SandboxRequire && sandboxError != nil {
		problem := Unverifiable(fmt.Sprintf(
			"sandbox required but bubblewrap could not start a sandbox: %v; refusing to run the command unsandboxed",
			sandboxError)).
			WithCommand(commandDesc).
			WithClass(ClassEnvironment)
		return nil, &problem
	}

	// The program is resolved once, for both modes: the sandbox shares the
	// read-only root and gets the same PATH, so a name that resolves here
	// resolves there, and a name that does not is an environment gap in both.
	// A confined run asks for the sandbox's view: a candidate behind a mask
	// cannot be executed there, and choosing it would look like a failing suite.
	resolved, found := "", false
	if bwrap != "" {
		var blocked *Verdict
		resolved, found, blocked = resolveProgramForSandbox(dir, argv[0], mounts)
		if blocked != nil {
			named := blocked.WithCommand(commandDesc)
			return nil, &named
		}
	} else {
		resolved, found = ResolveProgram(dir, argv[0])
	}
	if !found {
		problem := Unverifiable(fmt.Sprintf(
			"command %q is not available in the checkout or the run's PATH", argv[0])).
			WithCommand(commandDesc).
			WithClass(ClassEnvironment)
		return nil, &problem
	}
	argv = append([]string{resolved}, argv[1:]...)

	sandboxed := bwrap != "" && sandboxError == nil
	runArgv := argv
	runEnv := []string(nil)
	note := "not sandboxed: --sandbox=off"
	if sandboxed {
		runArgv = append(SandboxPrefix(bwrap, dir, mounts, caches, ""), argv...)
		runEnv = extraEnv
		note = "sandboxed with bwrap"
		commandDesc = displayPrefix + " && " + SandboxDisplayWith(commandText, mounts, caches)
	}
	noteSuffix := " (" + note + ")"

	timeout := limit
	if timeout <= 0 {
		timeout = DefaultTestTimeout
	}
	if remaining, bounded := c.Remaining(); bounded && remaining < timeout {
		timeout = remaining
	}

	writer := &cappedWriter{}
	cmd := exec.CommandContext(c.Context(), runArgv[0], runArgv[1:]...)
	cmd.Dir = dir
	cmd.Env = TestEnvWith(dir, runEnv)
	cmd.Stdout = writer
	cmd.Stderr = writer
	cmd.Stdin = nil
	// Own process group so a timeout or a cancel takes the whole tree down.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// A child that leaves the process group (setsid) keeps the output pipe
	// open; WaitDelay closes it after the kill so Wait returns regardless.
	cmd.WaitDelay = 5 * time.Second
	writer.kill = func() { killProcessGroup(cmd) }

	if err := cmd.Start(); err != nil {
		return nil, describeStartFailure(err, commandDesc, noteSuffix)
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	cancel := c.Context().Done()

	timedOut, cancelled := false, false
	select {
	case err := <-done:
		if err != nil {
			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) {
				return nil, describeStartFailure(err, commandDesc, noteSuffix)
			}
		}
	case <-cancel:
		cancelled = true
		killProcessGroup(cmd)
		<-done
	case <-timer.C:
		timedOut = true
		killProcessGroup(cmd)
		<-done
	}
	raw, exceeded := writer.snapshot()

	if cancelled {
		problem := Unverifiable("the run was cancelled before the command finished").
			WithCommand(commandDesc).
			WithOutput(LastLines(raw, 5)).
			WithSandboxed(sandboxed)
		return nil, &problem
	}
	if timedOut {
		problem := Unverifiable(fmt.Sprintf("command timed out after %s (built-in time limit)", timeout)).
			WithCommand(commandDesc).
			WithOutput(LastLines(raw, 5)).
			WithSandboxed(sandboxed).
			WithClass(ClassLimit)
		return nil, &problem
	}
	if exceeded {
		problem := Unverifiable(fmt.Sprintf(
			"command output exceeded the limit of %d bytes (built-in output limit); "+
				"the run cannot be verified from truncated output", MaxOutputBytes)).
			WithCommand(commandDesc).
			WithOutput(LastLines(raw, 5)).
			WithSandboxed(sandboxed).
			WithClass(ClassLimit)
		return nil, &problem
	}

	return &RunOutcome{
		CommandDesc: commandDesc,
		Code:        cmd.ProcessState.ExitCode(),
		RawOutput:   raw,
		Head:        writer.headSnapshot(),
		Output:      LastLines(raw, 5),
		Sandboxed:   sandboxed,
		Note:        noteSuffix,
	}, nil
}

func describeStartFailure(err error, commandDesc, note string) *Verdict {
	var problem Verdict
	if _, ok := err.(*exec.Error); ok {
		problem = Unverifiable("command not found: " + err.Error() + note)
	} else if _, ok := err.(*os.PathError); ok {
		problem = Unverifiable("command not executable: " + err.Error() + note)
	} else {
		problem = Unverifiable("could not start the command: " + err.Error() + note)
	}
	problem = problem.WithCommand(commandDesc).WithClass(ClassEnvironment)
	return &problem
}

func killProcessGroup(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
		_ = cmd.Process.Kill()
	}
}
