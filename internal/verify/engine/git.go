package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"syscall"
	"time"
)

const gitTimeout = 60 * time.Second

var (
	commitHashShape = regexp.MustCompile(`\A[0-9a-fA-F]{4,64}\z`)
	resolvedShape   = regexp.MustCompile(`\A[0-9a-fA-F]{40,64}\z`)
	branchNameShape = regexp.MustCompile(`\A[A-Za-z0-9][A-Za-z0-9._/-]*\z`)
)

// GitResult is one finished git invocation.
type GitResult struct {
	Stdout string
	Stderr string
	Code   int
}

// Output is what a verdict shows for a failed git call.
func (r GitResult) Output() string {
	return strings.TrimSpace(r.Stdout + r.Stderr)
}

// gitEnv keeps the caller's environment but never lets an ambient GIT_*
// variable redirect the check: object dirs, namespaces, config and templates
// are all dropped, and replace-objects and grafts are neutralised because they
// would rewrite the ancestry the verdicts rest on.
func gitEnv() []string {
	env := make([]string, 0, len(os.Environ())+3)
	for _, entry := range os.Environ() {
		if strings.HasPrefix(entry, "GIT_") {
			continue
		}
		env = append(env, entry)
	}
	return append(env,
		"GIT_TERMINAL_PROMPT=0",
		"GIT_NO_REPLACE_OBJECTS=1",
		"GIT_GRAFT_FILE="+os.DevNull,
	)
}

// RepoArgs is a git command line against the inspected repository.
//
// Repo-owned configuration must not execute programs during verification:
// hooks and fsmonitor are neutralised. The commit-graph is a derivable cache a
// hostile repository can forge, so the object store is read directly.
func RepoArgs(ctx *Ctx, args ...string) []string {
	return append([]string{
		"git",
		"-c", "core.hooksPath=" + os.DevNull,
		"-c", "core.fsmonitor=false",
		"-c", "core.commitGraph=false",
		"-C", ctx.Repo,
	}, args...)
}

// Git runs one git command against the inspected repository.
func (c *Ctx) Git(args ...string) GitResult {
	return c.GitTimeout(gitTimeout, args...)
}

// GitTimeout runs one git command with an explicit timeout, shortened to the
// run's remaining budget: the budget bounds the whole run, not only the
// command claims.
func (c *Ctx) GitTimeout(timeout time.Duration, args ...string) GitResult {
	return runGit(c.Context(), RepoArgs(c, args...), c.boundedTimeout(timeout))
}

// boundedTimeout shortens a timeout to the run's remaining budget.
func (c *Ctx) boundedTimeout(timeout time.Duration) time.Duration {
	remaining, bounded := c.Remaining()
	if !bounded || remaining >= timeout {
		return timeout
	}
	if remaining <= 0 {
		return time.Millisecond
	}
	return remaining
}

// Ambiguous reports whether a hex prefix matches more than one object.
//
// A short hash that resolves ambiguously is not a missing commit: refuting it
// would reject a true statement.
func (c *Ctx) Ambiguous(prefix string) bool {
	result := c.Git("rev-parse", "--disambiguate="+prefix)
	if result.Code != 0 {
		return false
	}
	count := 0
	for _, line := range strings.Split(strings.TrimSpace(result.Stdout), "\n") {
		if strings.TrimSpace(line) != "" {
			count++
		}
	}
	return count > 1
}

// GitIn runs one git command against an explicit directory, with the same
// neutralisation of repository-owned configuration as RepoArgs.
func (c *Ctx) GitIn(dir string, args ...string) GitResult {
	return runGit(c.Context(), append([]string{
		"git",
		"-c", "core.hooksPath=" + os.DevNull,
		"-c", "core.fsmonitor=false",
		"-c", "core.commitGraph=false",
		"-C", dir,
	}, args...), gitTimeout)
}

// GitInTimeout runs one git command against an explicit directory with its own
// timeout.
func (c *Ctx) GitInTimeout(dir string, timeout time.Duration, args ...string) GitResult {
	return runGit(c.Context(), append([]string{
		"git",
		"-c", "core.hooksPath=" + os.DevNull,
		"-c", "core.fsmonitor=false",
		"-c", "core.commitGraph=false",
		"-C", dir,
	}, args...), c.boundedTimeout(timeout))
}

// runGit runs git without a shell; a timeout becomes a non-zero result. The
// command gets its own process group, so a hung git and its children die
// together.
func runGit(ctx context.Context, args []string, timeout time.Duration) GitResult {
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Env = gitEnv()
	cmd.Stdin = nil
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// The output is capped like every other stream in the package: a git call
	// on a hostile repository must not grow the heap without bound, and a
	// truncated list must never be read as a complete one.
	stdout := &cappedWriter{}
	stderrWriter := &cappedWriter{}
	cmd.Stdout = stdout
	cmd.Stderr = stderrWriter
	// A child that leaves the process group keeps the pipe open; WaitDelay
	// closes it after the kill so Wait returns regardless.
	cmd.WaitDelay = 5 * time.Second

	done := make(chan error, 1)
	if err := cmd.Start(); err != nil {
		return GitResult{Stderr: err.Error(), Code: -1}
	}
	stderrWriter.kill = func() { killGitProcess(cmd) }
	go func() { done <- cmd.Wait() }()

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case err := <-done:
		code := 0
		if err != nil {
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) {
				code = exitErr.ExitCode()
			} else {
				code = -1
			}
		}
		stdoutText, stdoutExceeded := stdout.snapshot()
		stderrText, stderrExceeded := stderrWriter.snapshot()
		if stdoutExceeded || stderrExceeded {
			// A truncated result must not be read as a complete one.
			return GitResult{Stdout: stdoutText, Stderr: "output exceeded the limit", Code: 124}
		}
		return GitResult{Stdout: stdoutText, Stderr: stderrText, Code: code}
	case <-timer.C:
		killGitProcess(cmd)
		<-done
		stdoutText, _ := stdout.snapshot()
		return GitResult{
			Stdout: stdoutText,
			Stderr: fmt.Sprintf("timed out after %s", timeout),
			Code:   124,
		}
	case <-ctx.Done():
		killGitProcess(cmd)
		<-done
		stdoutText, _ := stdout.snapshot()
		return GitResult{
			Stdout: stdoutText,
			Stderr: "cancelled: " + ctx.Err().Error(),
			Code:   124,
		}
	}
}

func killGitProcess(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	_ = cmd.Process.Kill()
}

// RepoGuard returns a verdict when the repository is unusable, else nil.
func RepoGuard(ctx *Ctx) *Verdict {
	if ctx.Repo == "" {
		verdict := Unverifiable("no repository given").WithClass(ClassEnvironment)
		return &verdict
	}
	if info, err := os.Stat(ctx.Repo); err != nil || !info.IsDir() {
		verdict := Unverifiable(fmt.Sprintf("repo path does not exist: %s", ctx.Repo)).WithClass(ClassEnvironment)
		return &verdict
	}
	if result := ctx.Git("rev-parse", "--git-dir"); result.Code != 0 {
		verdict := Unverifiable(fmt.Sprintf("not a git repository: %s", ctx.Repo)).
			WithCommand(FormatCommand(RepoArgs(ctx, "rev-parse", "--git-dir"))).
			WithOutput(result.Output()).
			WithClass(ClassEnvironment)
		return &verdict
	}
	return nil
}

// RevParse resolves any revision to a concrete commit hash.
func (c *Ctx) RevParse(revision string) (string, bool) {
	if revision == "" || strings.HasPrefix(revision, "-") {
		return "", false
	}
	result := c.Git("rev-parse", "--verify", "--quiet", revision+"^{commit}")
	resolved := strings.TrimSpace(result.Stdout)
	if result.Code == 0 && resolvedShape.MatchString(resolved) {
		return resolved, true
	}
	return "", false
}

// ResolveCommit resolves a report-supplied commit, which must be a hex object
// id before git sees it.
func (c *Ctx) ResolveCommit(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if !commitHashShape.MatchString(value) {
		return "", false
	}
	return c.RevParse(value)
}

// CommitHashShape reports whether a value has the shape of a commit hash.
func CommitHashShape(value string) bool {
	return commitHashShape.MatchString(value)
}

// ValidBranch reports whether a report-supplied branch is a plain ref name.
//
// Anything git could read as an option, a refspec or a revision expression is
// rejected before any git call sees it.
func ValidBranch(name string) bool {
	if name == "" || strings.HasPrefix(name, "-") || !branchNameShape.MatchString(name) {
		return false
	}
	if strings.Contains(name, "..") || strings.Contains(name, "@{") ||
		strings.HasSuffix(name, "/") || strings.HasSuffix(name, ".lock") {
		return false
	}
	for _, part := range strings.Split(name, "/") {
		if part == "" || strings.HasPrefix(part, ".") {
			return false
		}
	}
	return true
}

// IsAncestor asks git whether ancestor is reachable from descendant.
func (c *Ctx) IsAncestor(ancestor, descendant string) (bool, bool) {
	result := c.Git("merge-base", "--is-ancestor", ancestor, descendant)
	switch result.Code {
	case 0:
		return true, true
	case 1:
		return false, true
	default:
		return false, false
	}
}

// FormatCommand renders an argv for display in a verdict.
func FormatCommand(args []string) string {
	quoted := make([]string, 0, len(args))
	for _, arg := range args {
		quoted = append(quoted, quoteArg(arg))
	}
	return strings.Join(quoted, " ")
}

func quoteArg(arg string) string {
	if arg == "" {
		return "''"
	}
	safe := true
	for _, char := range arg {
		switch {
		case char >= 'a' && char <= 'z', char >= 'A' && char <= 'Z', char >= '0' && char <= '9':
		case strings.ContainsRune("._/-=:@+%^,", char):
		default:
			safe = false
		}
	}
	if safe {
		return arg
	}
	return "'" + strings.ReplaceAll(arg, "'", `'\''`) + "'"
}

// MergeBase returns the merge base of HEAD and ref in repo, or "" when git
// cannot derive one. It is the base a diff-scope claim is judged from: the work
// of a branch is the diff since it left its upstream.
func MergeBase(repo, ref string) string {
	if repo == "" || ref == "" {
		return ""
	}
	ctx := &Ctx{Repo: repo}
	result := ctx.Git("merge-base", "HEAD", ref)
	if result.Code != 0 {
		return ""
	}
	return strings.TrimSpace(result.Stdout)
}

// Head returns the commit the repository's working tree stands on, or "" when
// git cannot answer. A caller may bind a verdict to it, so work that lands after
// the verdict is judged again.
func Head(repo string) string {
	if repo == "" {
		return ""
	}
	ctx := &Ctx{Repo: repo}
	result := ctx.Git("rev-parse", "--verify", "--quiet", "HEAD^{commit}")
	if result.Code != 0 {
		return ""
	}
	return strings.TrimSpace(result.Stdout)
}

// Branch returns the name of the checked-out branch, or "" when git cannot
// answer. A verdict about a report that names a pushed branch belongs to that
// branch: switching the checkout invalidates the judgement like a moved HEAD.
// A detached HEAD answers "HEAD", which is a branch name no report can satisfy.
func Branch(repo string) string {
	if repo == "" {
		return ""
	}
	ctx := &Ctx{Repo: repo}
	result := ctx.Git("rev-parse", "--abbrev-ref", "HEAD")
	if result.Code != 0 {
		return ""
	}
	return strings.TrimSpace(result.Stdout)
}

// WorktreeState is a digest of the repository's uncommitted residue, or "" when
// git cannot answer. It belongs in a verdict key beside HEAD and the branch: an
// accepted verdict was judged against a clean tree, and residue that appeared
// afterwards makes it a statement about another state.
//
// The digest covers exactly what `git status --porcelain` reports, so an ignored
// file does not move it: the project excluded that file from its history.
//
// It runs on the gate's tick path, not inside a run, so it has no run budget: the
// call is bounded by gitTimeout, and a repository git cannot answer for yields the
// empty state rather than a wrong one.
func WorktreeState(repo string) string {
	if repo == "" {
		return ""
	}
	ctx := &Ctx{Repo: repo}
	// --no-optional-locks: the gate asks on every tick, and a status that
	// rewrites the index could collide with the agent's own commit.
	result := ctx.Git("--no-optional-locks", "status", "--porcelain", "--untracked-files=normal")
	if result.Code != 0 {
		return ""
	}
	digest := sha256.Sum256([]byte(result.Stdout))
	return hex.EncodeToString(digest[:])
}
