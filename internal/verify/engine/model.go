// Package engine holds the verifier's machinery: the judgement model, the git
// and checkout helpers and the sandboxed runner. Nothing here knows a claim
// kind, and nothing here knows a harness: the report text is the interface.
package engine

import (
	"context"
	"time"
)

// Status is the outcome of checking one claim against the world.
type Status string

const (
	StatusConfirmed    Status = "CONFIRMED"
	StatusRefuted      Status = "REFUTED"
	StatusUnverifiable Status = "UNVERIFIABLE"
)

// Class says why a claim did not end Confirmed. It is empty exactly when the
// claim was not left unconfirmed (Confirmed or Refuted).
//
// Defect is a form violation: the claim cannot be bound or executed as
// written. Environment and Limit name a missing capability and an exhausted
// budget. Unverifiable is the residual: the check ran and could not decide.
type Class string

const (
	ClassNone         Class = ""
	ClassDefect       Class = "defect"
	ClassEnvironment  Class = "environment"
	ClassLimit        Class = "limit"
	ClassUnverifiable Class = "unverifiable"
)

// Claim is one verifiable assertion extracted from a report. The fields are
// typed instead of a map so a checker cannot read a key that no parser sets.
type Claim struct {
	Kind string
	Line int
	Raw  string

	Commit      string        // commit_exists, and the bound commit of dependents
	Branch      string        // branch_pushed
	Merge       string        // merge: the reported value, branch or status token
	Deploy      string        // deploy: the reported value
	Command     string        // tests_green, tests_exit, compute, lint
	ClaimedExit int           // tests_exit; meaningful only when HasExit
	HasExit     bool          // tests_green and tests_exit carry an exit code
	Files       []string      // diff_scope
	Token       string        // unknown_marker: the token no type claims
	Path        string        // artifact
	SHA256      string        // compute, artifact
	Profile     string        // profile
	Missing     []string      // profile: the requirement groups the report missed
	Evidence    []EvidenceRef // evidence_ids: the reported sub-phase sessions
	Plan        string        // plan_items: the plan the report names
	// Allowance is the count a quality claim declares as tolerable (mutation:
	// surviving mutants). HasAllowance separates "none declared" from "zero
	// declared".
	Allowance    int
	HasAllowance bool
}

// EvidenceRef is one reported evidence slot: the sub-phase it answers and the
// session id the report claims for it.
type EvidenceRef struct {
	Key string
	ID  string
}

// Finding is one broken rule, in the shape a caller reports it. Class separates
// the report's fault from the store's: a finding with ClassEnvironment is a
// message about the machine, not about the agent.
type Finding struct {
	Field  string
	Class  Class
	Detail string
}

// SessionRecord is the harness-neutral view of a session a report cites. It is
// what a session store can answer about any agent platform: the id, the session
// it descends from and the agent type it ran as.
type SessionRecord struct {
	ID        string
	ParentID  string
	AgentType string
}

// SessionStore resolves one session id. found=false means the store answered
// and has no such session, which is the report's fault; an error means the
// store could not answer, which is an environment gap and is never read as a
// falsified claim.
type SessionStore interface {
	Session(id string) (record SessionRecord, found bool, err error)
}

// EvidencePolicy pins the reading order of the evidence slots and the agent
// types allowed per slot. It is supplied by the caller, so the claim type
// carries no platform's agent names.
type EvidencePolicy struct {
	Order   []string
	Allowed map[string][]string
}

// PlanItem is one structured plan step. An item without a recognised status
// counts as open:  the plan is the loop's own record of what is unfinished.
type PlanItem struct {
	Content string
	Status  string
}

// PlanSource reads the structured plan of one thread. found=false means the
// store answered and that thread has no plan at all. A plan claim asks it for
// every thread the agent carries (Ctx.PlanThreadCandidates).
type PlanSource interface {
	Plan(thread string) (items []PlanItem, found bool, err error)
}

// Verdict is the judged claim. Class is empty for Confirmed and Refuted.
type Verdict struct {
	Kind      string
	Status    Status
	Class     Class
	Reason    string
	Command   string
	Output    string
	Sandboxed *bool
}

// Verdict constructors keep the four fields that every caller sets in the
// same order, so a reason can never drift into the command slot.
func Confirmed(reason string) Verdict {
	return Verdict{Status: StatusConfirmed, Reason: reason}
}

func Refuted(reason string) Verdict {
	return Verdict{Status: StatusRefuted, Reason: reason}
}

func Unverifiable(reason string) Verdict {
	return Verdict{Status: StatusUnverifiable, Class: ClassUnverifiable, Reason: reason}
}

func (v Verdict) WithCommand(command string) Verdict {
	v.Command = command
	return v
}

func (v Verdict) WithOutput(output string) Verdict {
	v.Output = output
	return v
}

func (v Verdict) WithClass(class Class) Verdict {
	if v.Status == StatusUnverifiable {
		v.Class = class
	}
	return v
}

func (v Verdict) WithSandboxed(sandboxed bool) Verdict {
	v.Sandboxed = &sandboxed
	return v
}

// ClaimType is one optional claim kind: the marker tokens it claims, how a
// report line turns into claims of it, and how such a claim is re-derived.
// A new kind is one file in the claimtypes package plus one entry in its
// registry; the parser and the dispatcher stay untouched.
type ClaimType struct {
	Kind string
	// Markers are the report tokens this kind claims. A token no registered
	// type claims is a defect: silently ignoring it would let a report
	// smuggle an unknown claim shape past the verifier.
	Markers []string
	// Match parses one report line into claims of this kind.
	Match func(line string) []Claim
	// Check re-derives one claim against the world. It never returns an
	// empty Status: the dispatcher rejects that as a broken checker.
	Check func(Claim, *Ctx) Verdict
	// NeedsRepo declares that Check reads a git repository.
	NeedsRepo bool
	// BindsCommit declares that the parser binds the claim to the report's
	// single hash-shaped commit.
	BindsCommit bool
}

// SandboxMode selects how command claims are confined.
//
// There is no "auto": a mode that silently runs unsandboxed when bubblewrap
// is missing is exactly the fallback the design forbids.
type SandboxMode string

const (
	// SandboxRequire refuses to run a command when bubblewrap is missing.
	SandboxRequire SandboxMode = "require"
	// SandboxOff runs without a sandbox. Only a caller that knowingly
	// accepts unsandboxed execution may pass it.
	SandboxOff SandboxMode = "off"
)

// Ctx carries everything a checker may consult. It deliberately holds no
// harness state: a claim type that could read a session store would tie the
// verifier to one platform.
type Ctx struct {
	Repo             string
	TmpDir           string
	Base             string
	ArtifactRoot     string
	Allowlist        []string
	ComputeAllowlist []string
	LintAllowlist    []string
	Sandbox          SandboxMode
	// Deadline bounds the whole run, not one command: a report with many
	// command claims must not outlive its budget.
	Deadline time.Time
	// Caller carries the caller's cancellation: a cancelled run stops
	// between claims and kills the running command.
	Caller context.Context
	// Now is injectable so a budget test does not have to sleep.
	Now func() time.Time
	// Sessions resolves the session ids of an evidence claim. Nil means the
	// caller has no session store, which the claim reports as an environment gap.
	Sessions SessionStore
	// Evidence pins the reading order and the allowed agent types of an
	// evidence claim.
	Evidence EvidencePolicy
	// ReportSession is the session the report belongs to: every cited
	// subagent must descend from it.
	ReportSession string
	// Plans reads the structured plan of PlanThread. Nil means the caller has
	// no plan store.
	Plans PlanSource
	// PlanThread names the plan a plan_items claim is checked against. The
	// report states the project; the thread is the caller's knowledge.
	PlanThread string
	// PlanThreads lists the threads that may hold the same agent's plan, in the
	// order they are consulted. An agent carries more than one identity (the
	// daemon's session uuid, the backend's session id, the proxy thread derived
	// from it) and set_plan stores its plan under whichever the client reported:
	// a lookup that consults one of them misses plans that exist. The caller
	// owns the list and may only name the reporting agent's own threads, because
	// a foreign thread would let a report be judged against a plan it did not
	// write. PlanThread is consulted after the list unless the list already names
	// it: it is the thread the verdict key is built from, not a preference about
	// which plan is the current one.
	PlanThreads []string
	// Languages is the language data a quality claim works with. It is
	// configuration, not knowledge: the claim logic looks a file's extension up
	// here and never branches on a language name.
	Languages []Language
	// ProfileTestCommands are the test commands the project's own profile
	// names, and ProfileCoverageCommands its coverage commands. Each is
	// admitted for exactly the claim family that needs it, by token equality.
	// Both stay empty without a profile file, which leaves the allowlists as
	// they are.
	ProfileTestCommands     []string
	ProfileCoverageCommands []string
}

// PlanThreadCandidates is the ordered list of threads a plan lookup consults:
// the caller's own order first, then the primary thread when the list does not
// already name it, without duplicates and without empty entries. It is a method
// so that every claim type reads the same order.
func (c *Ctx) PlanThreadCandidates() []string {
	var threads []string
	seen := make(map[string]bool, len(c.PlanThreads)+1)
	for _, thread := range append(append([]string{}, c.PlanThreads...), c.PlanThread) {
		if thread == "" || seen[thread] {
			continue
		}
		seen[thread] = true
		threads = append(threads, thread)
	}
	return threads
}

// Context is the caller's context, never nil.
func (c *Ctx) Context() context.Context {
	if c.Caller != nil {
		return c.Caller
	}
	return context.Background()
}

// Cancelled reports whether the caller cancelled the run.
func (c *Ctx) Cancelled() bool {
	return c.Context().Err() != nil
}

// Now returns the current time through the injectable clock.
func (c *Ctx) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// Remaining is the budget left and whether the run has a deadline at all.
// A run without a budget reports bounded=false, which is not the same as a run
// whose budget is gone.
func (c *Ctx) Remaining() (time.Duration, bool) {
	if c.Deadline.IsZero() {
		return 0, false
	}
	left := c.Deadline.Sub(c.now())
	if left < 0 {
		return 0, true
	}
	return left, true
}

// BudgetSpent reports whether the run's budget is gone.
func (c *Ctx) BudgetSpent() bool {
	if c.Deadline.IsZero() {
		return false
	}
	return !c.now().Before(c.Deadline)
}
