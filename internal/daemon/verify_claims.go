package daemon

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/carsteneu/yesmem/internal/storage"
	"github.com/carsteneu/yesmem/internal/verify"
	"github.com/carsteneu/yesmem/internal/verify/engine"
)

// verify_claims is the verification as a daemon operation: a report in, a
// judgement out, in the same response envelope as guard_check. The DONE gate is
// one caller; a harness, a CLI or a human may be another, so nothing here knows
// about yesloop phases or agent rows.
//
// The report is the interface. Everything else (repository, base revision,
// profile, policy, budget) is the caller's statement about how to judge it.

// verifyClaimsResult is the operation's answer.
//
// Status is the only field a caller has to read: "accepted" means nothing
// blocked. Kind and Reason name the blocker, ExitCode is the reference
// implementation's code (kept for comparability, never the criterion), and
// Findings carry the verdicts that blocked with their command and output.
type verifyClaimsResult struct {
	Status     string                `json:"status"`
	Kind       string                `json:"kind,omitempty"`
	Reason     string                `json:"reason,omitempty"`
	Findings   []verifyClaimFinding  `json:"findings,omitempty"`
	Summary    map[verify.Status]int `json:"summary"`
	Classes    map[verify.Class]int  `json:"classes"`
	ExitCode   int                   `json:"exit_code"`
	DurationMS int64                 `json:"duration_ms"`
	Repo       string                `json:"repo"`
	ReportFrom string                `json:"report_from"`
}

// verifyClaimFinding is one verdict the caller has to act on.
type verifyClaimFinding struct {
	Kind    string `json:"kind"`
	Line    int    `json:"line,omitempty"`
	Status  string `json:"status"`
	Class   string `json:"class,omitempty"`
	Reason  string `json:"reason"`
	Command string `json:"command,omitempty"`
	Output  string `json:"output,omitempty"`
}

// maxReportedFindings bounds the findings list. A report with many claims must
// not produce an unbounded answer, and the summary counts them all anyway.
const maxReportedFindings = 20

// handleVerifyClaims judges one report.
//
// The path is serialized and bounded: a verification can run a real test suite,
// and several at once would compete with each other for the same machine. A
// caller waits up to done_verify.max_wait for the single slot; when it never
// frees, the call records a limit verdict, because the run judged nothing and the
// report is not at fault.
func (h *Handler) handleVerifyClaims(params map[string]any) Response {
	request, source, err := h.verifyClaimsRequest(params)
	if err != nil {
		return errorResponse("verify_claims: " + err.Error())
	}

	// The key and the in-flight mark come before the wait: a DONE claim that lands
	// while this call is still queued behind another run must hold the agent, not
	// call its report unchecked.
	keyContext := h.verifyContextForRequest(request)
	mode := verifyVerdictMode(request.Profile, stringOr(params, "policy", ""))
	beginVerifyRun(keyContext.key)
	defer endVerifyRun(keyContext.key)

	if !h.acquireVerifySlot(h.doneVerifyMaxWait()) {
		reason := "another verification was still running after " +
			h.doneVerifyMaxWait().Round(time.Second).String() + "; the run did not start"
		state := verdictFor(keyContext, mode,
			verifyClaimsResult{Status: "blocked", Kind: "limit", Reason: reason}, request, 0)
		// The bound that expired is the wait, not the run's budget: the limit message
		// is those two numbers, and the run never started.
		state.Bound = h.doneVerifyMaxWait()
		h.recordVerifyVerdict(state)
		return errorResponse("verify_claims: " + reason)
	}
	defer h.releaseVerifySlot()

	started := time.Now()
	report, err := verify.Verify(context.Background(), request)
	if err != nil {
		// The run judged nothing. Recording that keeps the gate consistent: a DONE
		// claim for this report must not wait for a verdict that will never come, and
		// "error" is the class the fail-open rules already give a failed run.
		h.recordVerifyVerdict(&doneGateVerifyState{
			Fingerprint:   keyContext.key,
			Status:        doneVerifyBlocked,
			Kind:          "error",
			Reason:        err.Error(),
			Mode:          mode,
			Bound:         request.Budget,
			ReportDigest:  keyContext.reportDigest,
			Repo:          keyContext.repo,
			Head:          keyContext.head,
			Worktree:      keyContext.worktree,
			Branch:        keyContext.branch,
			PlanRevision:  keyContext.planRevision,
			ReportSession: keyContext.reportSession,
			RecordedAt:    time.Now().UnixNano(),
		})
		return errorResponse("verify_claims: " + err.Error())
	}
	took := time.Since(started)
	result := newVerifyClaimsResult(report, source, request.Repo, took)
	// Every verdict is stored under its key, whichever entry asked for it: the DONE
	// gate reuses it instead of running the check a second time.
	h.recordVerifyVerdict(verdictFor(keyContext, mode, result, request, took))
	return jsonResponse(result)
}

// verifyContextForRequest is the key of a direct run. It uses the request's own
// session and plan thread, which are the facts the report's evidence and plan
// claims were judged against.
func (h *Handler) verifyContextForRequest(request verify.Request) verifyContext {
	context := verifyContext{
		repo:          request.Repo,
		head:          verifyHead(request.Repo),
		worktree:      verify.WorktreeState(request.Repo),
		branch:        verifyBranch(request.Repo),
		planRevision:  h.verifyPlanFingerprint(request.PlanThread),
		reportSession: request.ReportSession,
		reportDigest:  digestText(request.Report),
	}
	context.key = verifyFingerprint(request.Repo, request.Report, request.ReportSession,
		request.PlanThread, context.head, context.worktree, context.planRevision, context.branch)
	return context
}

// verdictFor turns one run's answer into the stored judgement. The findings keep
// their command, because that is what makes them actionable, and drop their output,
// because a stored output would hold up to 64 KiB per finding until the process
// ends.
func verdictFor(context verifyContext, mode string, result verifyClaimsResult, request verify.Request, took time.Duration) *doneGateVerifyState {
	state := &doneGateVerifyState{
		Fingerprint:   context.key,
		Status:        doneVerifyAccepted,
		Mode:          mode,
		Bound:         request.Budget,
		Consumed:      took,
		ReportDigest:  context.reportDigest,
		Repo:          context.repo,
		Head:          context.head,
		Worktree:      context.worktree,
		Branch:        context.branch,
		PlanRevision:  context.planRevision,
		ReportSession: context.reportSession,
		RecordedAt:    time.Now().UnixNano(),
	}
	if result.Status == "accepted" {
		return state
	}
	state.Status = doneVerifyBlocked
	state.Kind = result.Kind
	state.Reason = result.Reason
	for _, finding := range result.Findings {
		state.Findings = append(state.Findings, verifyClaimFinding{
			Kind:    finding.Kind,
			Status:  finding.Status,
			Class:   finding.Class,
			Reason:  truncateCommand(finding.Reason),
			Command: truncateCommand(finding.Command),
		})
	}
	state.ProfileInitHint = wantsProfileInitHint(state.Findings, context.repo)
	return state
}

// rpcVerifyBudget is the default budget of one direct call, used when the gate's
// configuration names none. The call's budget is the only run budget left: the
// gate itself runs nothing, so done_verify.budget is read here.
const rpcVerifyBudget = 5 * time.Minute

// acquireVerifySlot takes the single verification slot, waiting at most wait. A
// zero wait reports whether the slot is free right now.
func (h *Handler) acquireVerifySlot(wait time.Duration) bool {
	if h.verifySlot == nil {
		return true
	}
	if wait <= 0 {
		select {
		case h.verifySlot <- struct{}{}:
			return true
		default:
			return false
		}
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case h.verifySlot <- struct{}{}:
		return true
	case <-timer.C:
		return false
	}
}

func (h *Handler) releaseVerifySlot() {
	if h.verifySlot == nil {
		return
	}
	select {
	case <-h.verifySlot:
	default:
	}
}

// verifyClaimsRequest turns the RPC parameters into a verification request.
//
// A missing report section is not an error: the run then has nothing to prove,
// the profile reports its requirements as unmet and the caller blocks. Silence
// must never read as success.
func (h *Handler) verifyClaimsRequest(params map[string]any) (verify.Request, string, error) {
	repo := stringOr(params, "repo", "")
	if repo == "" {
		return verify.Request{}, "", errors.New("repo is required")
	}
	if info, err := os.Stat(repo); err != nil || !info.IsDir() {
		return verify.Request{}, "", fmt.Errorf("repo %q is not an existing directory", repo)
	}

	report := stringOr(params, "report", "")
	source := "inline"
	section := stringOr(params, "section", "")
	project := stringOr(params, "project", "")
	if report == "" {
		if section == "" || project == "" {
			return verify.Request{}, "", errors.New("report or (project and section) is required")
		}
		sections, err := h.store.ScratchpadRead(project, section)
		if err != nil {
			return verify.Request{}, "", fmt.Errorf("cannot read section %q of %q: %w", section, project, err)
		}
		if len(sections) > 0 {
			report = sections[0].Content
		}
		source = "section"
	}

	policy, err := verifyPolicy(stringOr(params, "policy", ""))
	if err != nil {
		return verify.Request{}, "", err
	}
	budget, err := verifyBudget(stringOr(params, "budget", ""))
	if err != nil {
		return verify.Request{}, "", err
	}
	if budget == 0 {
		// The configured budget is the run budget of the one place a verification
		// runs. Without this, done_verify.budget would be a knob nothing reads.
		budget = h.doneVerifyBudget()
	}
	tmpDir := filepath.Join(h.dataDir, "tmp", "check")
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		return verify.Request{}, "", fmt.Errorf("cannot create %s: %w", tmpDir, err)
	}

	// The evidence rule needs the report's own session and the plan lookup needs a
	// thread. A section names the agent, and the agent owns both facts, so a
	// section call derives them; a caller's own values would otherwise file the
	// verdict under a key the DONE gate never looks up (and still report success).
	// An inline report call has no agent row: there the flags are the only source.
	session := stringOr(params, "report_session", stringOr(params, "_session_id", ""))
	thread := stringOr(params, "plan_thread", session)
	var threads []string
	if source == "section" {
		if agent := h.agentForReportSection(project, section); agent != nil {
			session = agentReportSession(*agent)
			// The verdict key is built from agentPlanThread, so that stays the
			// primary thread. The claim itself is judged against every identity
			// of the agent: the plan is stored under the backend session the
			// client reported, not under the daemon's session uuid.
			thread = agentPlanThread(*agent)
			threads = planThreadsForAgent(*agent)
		}
	}

	profile := stringOr(params, "profile", "")
	base := stringOr(params, "base", "")
	if base == "" {
		base = verifyMergeBase(repo)
	} else if profile == verify.ProfileYesloopDone {
		// A diff-scope claim is only worth what its base is worth, and in the loop's
		// rule set the party under judgement is the one that would choose it. The
		// merge base is the upstream, not a revision the report can pick.
		return verify.Request{}, "", errors.New(
			"the yesloop-done profile derives the diff base from the merge base; a caller-chosen base is not accepted")
	}

	return verify.Request{
		Repo:          repo,
		Base:          base,
		Report:        report,
		Profile:       profile,
		TmpDir:        tmpDir,
		Budget:        budget,
		Strict:        boolParam(params, "strict", false),
		Policy:        policy,
		Sessions:      h.verifySessionStore(),
		Evidence:      phase5EvidencePolicy(),
		ReportSession: session,
		Plans:         h.verifyPlanSource(),
		PlanThread:    thread,
		PlanThreads:   threads,
	}, source, nil
}

// verifyPlanSource is the plan store the plan_items claim reads. Nil while the
// handler has no store: the claim then reports the missing capability instead of
// a recovered nil dereference.
func (h *Handler) verifyPlanSource() engine.PlanSource {
	if h.store == nil {
		return nil
	}
	return storagePlanSource{store: h.store}
}

// verifyPolicy maps the named policy onto the verifier's. An unknown name is an
// error: silently falling back to the strict policy would turn a tolerated class
// into a freeze, and falling back to the lenient one would accept a report the
// caller wanted blocked.
func verifyPolicy(name string) (verify.Policy, error) {
	switch name {
	case "":
		return verify.Policy{}, nil
	case "yesloop-done":
		return verify.YesloopDonePolicy(), nil
	}
	return verify.Policy{}, fmt.Errorf("unknown policy %q", name)
}

// verifyBudget parses the run budget. Empty means the verifier's default, which
// is bounded; an unparsable value is an error rather than a silent default.
func verifyBudget(value string) (time.Duration, error) {
	if value == "" {
		return 0, nil
	}
	budget, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("invalid budget %q: %w", value, err)
	}
	return budget, nil
}

// verifySessionStore picks the session source the evidence rule reads: the
// yesmem store is the basis, opencode's database only enriches it.
func (h *Handler) verifySessionStore() engine.SessionStore {
	if h.store == nil {
		return opencodeEvidenceStore{dbPath: h.ocDBPath}
	}
	return sessionEvidenceStore{store: h.store, ocDBPath: h.ocDBPath}
}

// phase5EvidencePolicy is the evidence policy of the yesloop review slots. It
// belongs to the caller, not to the claim type: the verifier must not carry one
// platform's agent names.
func phase5EvidencePolicy() engine.EvidencePolicy {
	return engine.EvidencePolicy{
		Order:   phase5EvidenceOrder,
		Allowed: allowedReviewAgents,
	}
}

// storagePlanSource reads structured plan items out of the yesmem store.
type storagePlanSource struct {
	store *storage.Store
}

func (s storagePlanSource) Plan(thread string) ([]engine.PlanItem, bool, error) {
	row, err := s.store.GetPlan(thread)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, false, nil
		}
		return nil, false, err
	}
	stored := parseStoredPlanItems(row.Items)
	items := make([]engine.PlanItem, 0, len(stored))
	for _, item := range stored {
		items = append(items, engine.PlanItem{Content: item.Content, Status: item.Status})
	}
	return items, true, nil
}

func newVerifyClaimsResult(report verify.Report, source, repo string, took time.Duration) verifyClaimsResult {
	result := verifyClaimsResult{
		Status:     "accepted",
		Summary:    report.Summary,
		Classes:    report.Classes,
		ExitCode:   report.ExitCode,
		DurationMS: took.Milliseconds(),
		Repo:       repo,
		ReportFrom: source,
	}
	if report.Decision.Accept {
		return result
	}
	result.Status = "blocked"
	result.Kind = report.Decision.Kind
	result.Reason = report.Decision.Reason
	for index, verdict := range report.Decision.Findings {
		if index == maxReportedFindings {
			break
		}
		result.Findings = append(result.Findings, verifyClaimFinding{
			Kind:    verdict.Kind,
			Status:  string(verdict.Status),
			Class:   string(verdict.Class),
			Reason:  verdict.Reason,
			Command: verdict.Command,
			Output:  verdict.Output,
		})
	}
	return result
}
