package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/carsteneu/yesmem/internal/daemon"
	"github.com/carsteneu/yesmem/internal/verify"
)

// verify-claims judges a report against the world through the daemon: the same
// operation the DONE gate uses, reachable without python and without speaking the
// socket protocol by hand. Read-only, and the exit code comes from the verdict.
//
// The operation always inspects the tree's HEAD, so --head is a precondition, not a
// second revision: a human who names a commit the checkout is not standing on gets
// told that instead of a judgement about another tree.

// verifyClaimsFlags is the command line. Kept apart from the run so the mapping from
// flags to RPC parameters is testable without a daemon.
type verifyClaimsFlags struct {
	repo       string
	reportFile string
	project    string
	section    string
	base       string
	head       string
	profile    string
	policy     string
	budget     string
	// reportSession and planThread are part of the RPC contract: an [EVIDENCE:] or
	// [PLAN:] claim is judged against them, and without them a report in the gate's own
	// shape is unverifiable through the human path.
	reportSession string
	planThread    string
	strict        bool
	asJSON        bool
	// init is the profile mode: it detects the stack, proves its test command in
	// the sandbox and writes .yesmem/verify.yaml only if that run passed. It
	// needs no report and no daemon.
	init bool
}

// verifyClaimsAnswer is the daemon's answer, as far as the CLI reads it.
type verifyClaimsAnswer struct {
	Status     string                   `json:"status"`
	Kind       string                   `json:"kind"`
	Reason     string                   `json:"reason"`
	Findings   []verifyClaimsCLIFinding `json:"findings"`
	DurationMS int64                    `json:"duration_ms"`
	Repo       string                   `json:"repo"`
	ReportFrom string                   `json:"report_from"`
}

type verifyClaimsCLIFinding struct {
	Kind    string `json:"kind"`
	Status  string `json:"status"`
	Class   string `json:"class"`
	Reason  string `json:"reason"`
	Command string `json:"command"`
}

func runVerifyClaims(args []string) {
	flags, err := parseVerifyClaimsFlags(args, os.Stderr)
	if err != nil {
		os.Exit(2)
	}
	if flags == nil {
		return // help was requested
	}
	if flags.init {
		if err := verifyInitConflict(flags); err != nil {
			fmt.Fprintf(os.Stderr, "verify-claims: %v\n", err)
			os.Exit(2)
		}
		os.Exit(runVerifyInit(flags.repo, os.Stdout, os.Stderr))
	}

	params, err := flags.params(os.Stdin)
	if err != nil {
		fmt.Fprintf(os.Stderr, "verify-claims: %v\n", err)
		os.Exit(2)
	}

	deadline, err := flags.clientDeadline()
	if err != nil {
		fmt.Fprintf(os.Stderr, "verify-claims: %v\n", err)
		os.Exit(2)
	}
	if err := verifyClaimsHeadCheck(flags.repo, flags.head); err != nil {
		fmt.Fprintf(os.Stderr, "verify-claims: %v\n", err)
		os.Exit(2)
	}

	client, err := daemon.DialTimeout(yesmemDataDir(), 5*time.Second)
	if err != nil {
		fmt.Fprintf(os.Stderr, "verify-claims: connect to daemon: %v\n", err)
		os.Exit(2)
	}
	defer client.Close()
	client.SetDeadline(time.Now().Add(deadline))

	raw, err := client.Call("verify_claims", params)
	if err != nil {
		fmt.Fprintf(os.Stderr, "verify-claims: %v\n", err)
		os.Exit(2)
	}

	var answer verifyClaimsAnswer
	if err := json.Unmarshal(raw, &answer); err != nil {
		fmt.Fprintf(os.Stderr, "verify-claims: unreadable answer: %v\n", err)
		os.Exit(2)
	}

	if flags.asJSON {
		printJSON(raw)
	} else {
		fmt.Print(verifyClaimsText(answer))
	}
	os.Exit(verifyClaimsExitCode(answer))
}

// parseVerifyClaimsFlags parses the command line. A nil result with a nil error means
// -h was asked for and the usage is already printed.
func parseVerifyClaimsFlags(args []string, stderr io.Writer) (*verifyClaimsFlags, error) {
	flags := &verifyClaimsFlags{}
	fs := flag.NewFlagSet("verify-claims", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&flags.repo, "repo", "", "repository the claims are checked against (default: the current directory)")
	fs.StringVar(&flags.reportFile, "report", "", "file holding the report text, - for stdin")
	fs.StringVar(&flags.project, "project", "", "project of the scratchpad section that holds the report")
	fs.StringVar(&flags.section, "section", "", "scratchpad section that holds the report")
	fs.StringVar(&flags.base, "base", "", "base revision a Files-in-scope claim is compared from (default: merge-base); refused with --profile yesloop-done, which derives it")
	fs.StringVar(&flags.head, "head", "", "revision the checkout must stand on; the operation always judges HEAD")
	fs.StringVar(&flags.profile, "profile", "", "claim classes the report must contain, e.g. yesloop-done")
	fs.StringVar(&flags.policy, "policy", "", "class policy: yesloop-done tolerates an unprovable deploy, empty blocks every unverifiable claim")
	fs.StringVar(&flags.budget, "budget", "", "run budget as a Go duration, e.g. 5m (default: the daemon's 5m)")
	fs.StringVar(&flags.reportSession, "report-session", "", "session the report belongs to; a cited subagent must descend from it (ignored when a section is given: its agent owns it)")
	fs.StringVar(&flags.planThread, "plan-thread", "", "thread whose plan a [PLAN:] claim is checked against (ignored when a section is given: its agent owns it)")
	fs.BoolVar(&flags.strict, "strict", false, "reserve exit 0 for a report whose every claim was proven")
	fs.BoolVar(&flags.asJSON, "json", false, "print the daemon's answer as JSON")
	fs.BoolVar(&flags.init, "init", false, "detect the project's stack, prove its test command in the sandbox, and write .yesmem/verify.yaml only if that run passed")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "Usage: yesmem verify-claims [flags]")
		fmt.Fprintln(stderr, "  Judges a report against the repository: exit 0 accepted, 1 blocked, 2 usage or transport.")
		fmt.Fprintln(stderr, "  The report comes from --report <file|-> or from a scratchpad section (--project and --section).")
		fmt.Fprintln(stderr, "  --init writes the project profile instead: exit 0 written, 1 nothing written (unrecognised stack or failed proof), 2 usage or transport.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil, nil
		}
		return nil, err
	}
	if flags.repo == "" {
		wd, err := os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("cannot derive the repository from the working directory: %v", err)
		}
		flags.repo = wd
	}
	return flags, nil
}

// params turns the flags into the RPC parameters. A report source is required: an
// empty report would verify nothing and read as a pass.
func (f *verifyClaimsFlags) params(stdin io.Reader) (map[string]any, error) {
	params := map[string]any{"repo": f.repo}
	if f.reportFile != "" {
		text, err := readReportFile(f.reportFile, stdin)
		if err != nil {
			return nil, err
		}
		params["report"] = text
	} else {
		if f.section == "" || f.project == "" {
			return nil, errors.New("report required: pass --report <file|-> or both --project and --section")
		}
		params["section"] = f.section
		params["project"] = f.project
	}
	for key, value := range map[string]string{
		"base":           f.base,
		"profile":        f.profile,
		"policy":         f.policy,
		"budget":         f.budget,
		"report_session": f.reportSession,
		"plan_thread":    f.planThread,
	} {
		if value != "" {
			params[key] = value
		}
	}
	if f.strict {
		params["strict"] = true
	}
	return params, nil
}

// maxReportBytes is the verifier's own ceiling (verify.MaxReportBytes). The read is
// bounded here as well: a pipe that never ends must not be buffered whole in the CLI and
// then encoded into one socket frame.
const maxReportBytes = 4 << 20

func readReportFile(path string, stdin io.Reader) (string, error) {
	source := stdin
	label := "stdin"
	if path != "-" {
		file, err := os.Open(path)
		if err != nil {
			return "", fmt.Errorf("cannot read the report: %v", err)
		}
		defer file.Close()
		source, label = file, path
	}
	data, err := io.ReadAll(io.LimitReader(source, maxReportBytes+1))
	if err != nil {
		return "", fmt.Errorf("cannot read the report from %s: %v", label, err)
	}
	if len(data) > maxReportBytes {
		return "", fmt.Errorf("the report from %s exceeds %d bytes", label, maxReportBytes)
	}
	return string(data), nil
}

// clientDeadline bounds the wait for the answer. The daemon may wait up to its
// max_wait for the single verification slot before the run's own budget starts,
// and the CLI can read neither value. Without --budget it therefore allows both
// ceilings: a shorter deadline would report a timeout while the verifier is still
// working and keep the slot taken. A caller that knows its budget passes it.
func (f *verifyClaimsFlags) clientDeadline() (time.Duration, error) {
	budget := 2 * verify.DefaultBudget
	if f.budget != "" {
		parsed, err := time.ParseDuration(f.budget)
		if err != nil {
			return 0, fmt.Errorf("invalid --budget %q: %v", f.budget, err)
		}
		if parsed <= 0 {
			return 0, fmt.Errorf("invalid --budget %q: it must be positive", f.budget)
		}
		budget = parsed
	}
	return budget + time.Minute, nil
}

// verifyClaimsHeadCheck refuses to judge a tree the human did not name.
func verifyClaimsHeadCheck(repo, head string) error {
	if head == "" {
		return nil
	}
	want, err := resolveRevision(repo, head)
	if err != nil {
		return err
	}
	got, err := resolveRevision(repo, "HEAD")
	if err != nil {
		return err
	}
	if want != got {
		return fmt.Errorf("the checkout stands on %s, not on %s: the operation judges HEAD, check out the named revision first", got, want)
	}
	return nil
}

func resolveRevision(repo, revision string) (string, error) {
	out, err := exec.Command("git", "-C", repo, "rev-parse", "--verify", "--quiet", revision+"^{commit}").Output()
	if err != nil {
		return "", fmt.Errorf("cannot resolve revision %q in %s", revision, repo)
	}
	resolved := strings.TrimSpace(string(out))
	if resolved == "" {
		return "", fmt.Errorf("revision %q does not resolve to a commit in %s", revision, repo)
	}
	return resolved, nil
}

// verifyClaimsExitCode turns the verdict into the process's exit code.
func verifyClaimsExitCode(answer verifyClaimsAnswer) int {
	if answer.Status == "accepted" {
		return 0
	}
	return 1
}

// verifyClaimsText is the human answer, the JSON answer's substance in one block.
func verifyClaimsText(answer verifyClaimsAnswer) string {
	var text strings.Builder
	if answer.Status == "accepted" {
		fmt.Fprintf(&text, "accepted: every claim was derived from %s (report from %s, %d ms)\n",
			answer.Repo, answer.ReportFrom, answer.DurationMS)
		return text.String()
	}
	fmt.Fprintf(&text, "blocked (%s): %s\n", answer.Kind, answer.Reason)
	for _, finding := range answer.Findings {
		class := ""
		if finding.Class != "" {
			class = " class=" + finding.Class
		}
		fmt.Fprintf(&text, "  %s %s%s: %s\n", finding.Kind, finding.Status, class, finding.Reason)
		if finding.Command != "" {
			fmt.Fprintf(&text, "    command: %s\n", finding.Command)
		}
	}
	return text.String()
}

// runVerifyInit is the --init mode: it writes the project's own profile, and
// only when the profile's own test command passed in the sandbox. It talks to no
// daemon.
//
// Exit 0 written, 1 nothing written (unrecognised stack or a proof that did not
// pass), 2 usage or an I/O failure.
func runVerifyInit(repo string, stdout, stderr io.Writer) int {
	tmpDir := filepath.Join(yesmemDataDir(), "tmp", "init")
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		fmt.Fprintf(stderr, "verify-claims --init: cannot create %s: %v\n", tmpDir, err)
		return 2
	}
	result, err := verify.InitProfile(verify.InitOptions{Repo: repo, TmpDir: tmpDir})
	if err != nil {
		fmt.Fprintf(stderr, "verify-claims --init: %v\n", err)
		if errors.Is(err, verify.ErrProfileExists) {
			// A refusal, not a failure: there was nothing for this run to write.
			return 1
		}
		return 2
	}
	return reportVerifyInit(result, stdout, stderr)
}

// verifyInitConflict rejects the report flags in the init mode. Ignoring them
// silently would hand back human text where JSON was asked for, and judge
// nothing where a report was passed.
func verifyInitConflict(flags *verifyClaimsFlags) error {
	if flags.reportFile != "" || flags.section != "" || flags.project != "" ||
		flags.profile != "" || flags.policy != "" || flags.budget != "" ||
		flags.base != "" || flags.head != "" ||
		flags.reportSession != "" || flags.planThread != "" ||
		flags.strict || flags.asJSON {
		return errors.New("--init writes the project profile; it takes no report, policy, profile, budget or revision flag")
	}
	return nil
}

// reportVerifyInit narrates one init run in the order the reader needs: what was
// detected, what the proof run did (its own last lines when it failed), what was
// written.
func reportVerifyInit(result verify.InitResult, stdout, stderr io.Writer) int {
	for _, detection := range result.Detected {
		fmt.Fprintf(stdout, "detected: %s (%s)\n", detection.Name, detection.Manifest)
	}
	for _, note := range result.Notes {
		fmt.Fprintf(stdout, "note: %s\n", note)
	}
	if result.Proven != nil {
		switch {
		case result.Proven.Reason != "":
			fmt.Fprintf(stdout, "proof: not run: %s\n", result.Proven.Reason)
		case result.Proven.Exit != 0:
			fmt.Fprintf(stdout, "proof failed: %s -> exit %d (%s)\n",
				result.Proven.Command, result.Proven.Exit, sandboxState(result.Proven.Sandboxed))
			fmt.Fprint(stdout, proofTail(result.Proven.Output))
		default:
			fmt.Fprintf(stdout, "proven: %s -> exit 0 (%s)\n",
				result.Proven.Command, sandboxState(result.Proven.Sandboxed))
		}
	}
	if result.Written == "" {
		if len(result.Detected) == 0 {
			// The note above ends in "for example:"; the file it means follows.
			fmt.Fprint(stdout, result.Example)
		}
		fmt.Fprintln(stderr, "verify-claims --init: nothing written")
		return 1
	}
	fmt.Fprintf(stdout, "written: %s\n", result.Written)
	return 0
}

// proofTail is the end of the proof run's output, where a failing suite says
// why. Bounded: a long log must not bury the finding the reader has to fix.
func proofTail(output string) string {
	trimmed := strings.TrimRight(output, "\n")
	if trimmed == "" {
		return ""
	}
	lines := strings.Split(trimmed, "\n")
	const keep = 20
	if len(lines) > keep {
		lines = lines[len(lines)-keep:]
	}
	var text strings.Builder
	for _, line := range lines {
		fmt.Fprintf(&text, "  %s\n", line)
	}
	return text.String()
}

// sandboxState is how the proof line reports the confinement. A run outside the
// sandbox would never pass SandboxRequire, and naming it keeps that visible
// rather than assumed.
func sandboxState(sandboxed bool) string {
	if sandboxed {
		return "sandboxed"
	}
	return "NOT sandboxed"
}
