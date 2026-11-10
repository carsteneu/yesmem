package claimtypes

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/carsteneu/yesmem/internal/verify/engine"
)

const (
	// ComputeTimeout bounds one compute run.
	ComputeTimeout = 300 * time.Second
	// MaxComputeBytes is the largest stdout the verifier will hash. More is
	// refused, never truncated into a verdict: the hash streams, so the limit
	// bounds time, not memory.
	MaxComputeBytes = 64 << 20
)

var (
	computeMarkerRe = regexp.MustCompile(`\[COMPUTE:(?P<body>[^\]\[]*?)\]`)
	sha256Shape     = regexp.MustCompile(`\A[0-9a-fA-F]{64}\z`)
)

// MatchCompute parses the [COMPUTE] markers of one line.
//
// A COMPUTE claim asserts that the named command prints exactly the claimed
// bytes on stdout: any finite computation becomes checkable without new code
// per problem, because the hash covers the bytes, the commit pins the code and
// the allowlist pins which commands may run at all.
func MatchCompute(line string) []engine.Claim {
	var claims []engine.Claim
	for _, match := range computeMarkerRe.FindAllStringSubmatch(line, -1) {
		command, digest, ok := splitArrow(match[1])
		if !ok {
			// An arrow-less marker names no claim.
			continue
		}
		claims = append(claims, engine.Claim{
			Kind:    "compute",
			Command: trimQuotes(command),
			SHA256:  trimQuotes(digest),
		})
	}
	return claims
}

// CheckCompute re-derives a [COMPUTE] claim by re-running the command on a
// throwaway checkout of the pinned commit and hashing its stdout.
func CheckCompute(claim engine.Claim, ctx *engine.Ctx) engine.Verdict {
	if guard := engine.RepoGuard(ctx); guard != nil {
		return *guard
	}
	commandText := strings.TrimSpace(claim.Command)
	if commandText == "" {
		return engine.Unverifiable("the claim names no command").WithClass(engine.ClassDefect)
	}
	claimed := trimQuotes(claim.SHA256)
	if !sha256Shape.MatchString(claimed) {
		return engine.Unverifiable(fmt.Sprintf("not a sha256 digest: %q", claimed)).
			WithCommand(commandText).
			WithClass(engine.ClassDefect)
	}
	value := strings.TrimSpace(claim.Commit)
	if value == "" {
		return engine.Unverifiable("no commit hash to pin the compute run").
			WithCommand(commandText).
			WithClass(engine.ClassDefect)
	}
	commit, ok := ctx.ResolveCommit(value)
	if !ok {
		return engine.Unverifiable(fmt.Sprintf(
			"commit %q does not resolve to a commit in %s", value, ctx.Repo)).
			WithCommand(commandText)
	}
	argv, err := engine.SplitCommand(commandText)
	if err != nil {
		return engine.Unverifiable("could not parse command: " + err.Error()).
			WithCommand(commandText).
			WithClass(engine.ClassDefect)
	}
	if len(argv) == 0 {
		return engine.Unverifiable("empty command").
			WithCommand(commandText).
			WithClass(engine.ClassDefect)
	}
	if !engine.AllowedByTokens(argv, ctx.ComputeAllowlist) {
		return engine.Unverifiable(fmt.Sprintf("command is not in the compute allowlist: %q", commandText)).
			WithCommand(commandText).
			WithClass(engine.ClassEnvironment)
	}
	strict := engine.IsWrapper(argv)
	for _, arg := range argv {
		if engine.ArgEscapesCheckout(arg, strict) {
			// Only the working directory is confined; an argument pointing
			// outside the checkout would run code the pinned commit never
			// contained.
			return engine.Unverifiable(fmt.Sprintf("unsafe command argument: %q", arg)).
				WithCommand(commandText).
				WithClass(engine.ClassDefect)
		}
	}

	mode := ctx.Sandbox
	if mode == "" {
		mode = engine.SandboxRequire
	}
	var bwrap string
	if mode == engine.SandboxRequire {
		bwrap = engine.FindBwrap()
		if bwrap == "" {
			return engine.Unverifiable(
				"sandbox required but bubblewrap is not available in PATH; refusing to run the command unsandboxed").
				WithCommand(commandText).
				WithClass(engine.ClassEnvironment)
		}
	}
	if ctx.BudgetSpent() {
		return engine.Unverifiable(fmt.Sprintf(
			"the run budget was exhausted before %q could start (built-in limit)", commandText)).
			WithCommand(commandText).
			WithClass(engine.ClassLimit)
	}

	checkout, failure := ctx.NewCheckout("compute", commit)
	if failure != nil {
		return *failure
	}
	defer checkout.Close()
	commandDesc := checkout.Prefix + " && " + commandText

	if escape := engine.SymlinkEscape(argv, checkout.Dir, strict); escape != "" {
		return engine.Unverifiable(fmt.Sprintf("command argument resolves outside the checkout: %q", escape)).
			WithCommand(commandDesc).
			WithClass(engine.ClassDefect)
	}
	// The program is resolved against the run's PATH, never the caller's: a
	// shim in an operator-writable directory would decide the digest.
	resolved, found := engine.ResolveProgram(checkout.Dir, argv[0])
	if !found {
		return engine.Unverifiable(fmt.Sprintf(
			"command %q is not available in the checkout or the run's PATH", argv[0])).
			WithCommand(commandDesc).
			WithClass(engine.ClassEnvironment)
	}
	argv = append([]string{resolved}, argv[1:]...)

	// The language profile's sandbox extras (cache mounts, build environment)
	// are deliberately not applied here: a compute claim judges the bytes a
	// command prints, and a profile-declared build environment would change
	// those bytes. This run keeps the plain sandbox.
	var sandboxError error
	if bwrap != "" {
		sandboxError = engine.SandboxProbe(bwrap, checkout.Dir)
	}
	if mode == engine.SandboxRequire && sandboxError != nil {
		return engine.Unverifiable(fmt.Sprintf(
			"sandbox required but bubblewrap could not start a sandbox: %v; refusing to run the command unsandboxed",
			sandboxError)).
			WithCommand(commandDesc).
			WithClass(engine.ClassEnvironment)
	}
	sandboxed := bwrap != "" && sandboxError == nil
	runArgv := argv
	note := " (not sandboxed: --sandbox=off)"
	if sandboxed {
		runArgv = append(engine.SandboxPrefix(bwrap, checkout.Dir, nil, nil, ""), argv...)
		note = " (sandboxed with bwrap)"
		commandDesc = checkout.Prefix + " && " + engine.SandboxDisplay(commandText)
	}

	timeout := ComputeTimeout
	if remaining, bounded := ctx.Remaining(); bounded && remaining < timeout {
		timeout = remaining
	}

	digest := sha256.New()
	stream := &hashingWriter{digest: digest, limit: MaxComputeBytes}
	stderr := &tailWriter{}

	cmd := exec.CommandContext(ctx.Context(), runArgv[0], runArgv[1:]...)
	cmd.Dir = checkout.Dir
	cmd.Env = engine.TestEnv(checkout.Dir)
	cmd.Stdout = stream
	cmd.Stderr = stderr
	cmd.Stdin = nil
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = 5 * time.Second
	stream.kill = func() {
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
	}

	if err := cmd.Start(); err != nil {
		return engine.Unverifiable("command not found: " + err.Error() + note).
			WithCommand(commandDesc).
			WithClass(engine.ClassEnvironment)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	timedOut, cancelled := false, false
	select {
	case <-done:
	case <-ctx.Context().Done():
		cancelled = true
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		<-done
	case <-timer.C:
		timedOut = true
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		<-done
	}

	total, exceeded := stream.result()
	if cancelled {
		return engine.Unverifiable("the run was cancelled before the command finished").
			WithCommand(commandDesc).
			WithSandboxed(sandboxed)
	}
	if timedOut {
		return engine.Unverifiable(fmt.Sprintf(
			"command timed out after %s (built-in time limit)", timeout)).
			WithCommand(commandDesc).
			WithSandboxed(sandboxed).
			WithClass(engine.ClassLimit)
	}
	if exceeded {
		return engine.Unverifiable(fmt.Sprintf(
			"command stdout exceeded the limit of %d bytes (built-in output limit)", MaxComputeBytes)).
			WithCommand(commandDesc).
			WithSandboxed(sandboxed).
			WithClass(engine.ClassLimit)
	}

	code := cmd.ProcessState.ExitCode()
	actual := hex.EncodeToString(digest.Sum(nil))
	output := fmt.Sprintf("sha256=%s bytes=%d exit=%d", actual, total, code)
	if tail := stderr.text(); strings.TrimSpace(tail) != "" {
		output += "\nstderr:\n" + engine.LastLines(tail, 5)
	}
	if code != 0 {
		// Completion gate: a run that did not complete is never turned into a
		// verdict, whatever its bytes look like.
		status := fmt.Sprintf("exited with status %d", code)
		if code < 0 {
			status = fmt.Sprintf("was killed by signal %d", -code)
		}
		return engine.Unverifiable(fmt.Sprintf(
			"command %s; no certificate from a run that did not complete%s", status, note)).
			WithCommand(commandDesc).
			WithOutput(output).
			WithSandboxed(sandboxed)
	}
	if actual == strings.ToLower(claimed) {
		return engine.Confirmed(fmt.Sprintf(
			"sha256 of stdout matches the claimed digest (exit %d, %d bytes)%s", code, total, note)).
			WithCommand(commandDesc).
			WithOutput(output).
			WithSandboxed(sandboxed)
	}
	return engine.Refuted(fmt.Sprintf("claimed sha256 %s, stdout has %s%s",
		strings.ToLower(claimed), actual, note)).
		WithCommand(commandDesc).
		WithOutput(output).
		WithSandboxed(sandboxed)
}

// hashingWriter hashes stdout while it streams and bounds it.
type hashingWriter struct {
	mu       sync.Mutex
	digest   hash.Hash
	total    int64
	limit    int64
	exceeded bool
	kill     func()
}

func (w *hashingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.total += int64(len(p))
	if w.total > w.limit {
		if !w.exceeded {
			w.exceeded = true
			if w.kill != nil {
				w.kill()
			}
		}
		return len(p), nil
	}
	_, _ = w.digest.Write(p)
	return len(p), nil
}

func (w *hashingWriter) result() (int64, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.total, w.exceeded
}

// tailWriter keeps the tail of stderr; the report only shows the last
// lines, so nothing larger is kept.
type tailWriter struct {
	mu  sync.Mutex
	buf []byte
}

func (w *tailWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(p) >= engine.TailBytes {
		w.buf = append(w.buf[:0], p[len(p)-engine.TailBytes:]...)
		return len(p), nil
	}
	w.buf = append(w.buf, p...)
	if len(w.buf) > engine.TailBytes {
		w.buf = w.buf[len(w.buf)-engine.TailBytes:]
	}
	return len(p), nil
}

func (w *tailWriter) text() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return string(w.buf)
}
