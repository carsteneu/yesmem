package daemon

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/carsteneu/yesmem/internal/storage"
	"github.com/carsteneu/yesmem/internal/verify"
)

// doneClaimContent is a scratchpad that claims completion: seven COMPLETE phase
// blocks, the send_to evidence the Phase 7 contract asks for, and a verify
// report beside it.
func doneClaimContent(t *testing.T, s *storage.Store, project, section, report string) string {
	t.Helper()
	content := "### Phase 1: ANALYZE\n**Status:** COMPLETE\n**Session id:** ses_agent\n\n" +
		"### Phase 2: PLAN\n**Status:** COMPLETE\n\n" +
		"### Phase 3: EXECUTE\n**Status:** COMPLETE\n\n" +
		"### Phase 4: VERIFY\n**Status:** COMPLETE\n\n" +
		"### Phase 5: REVIEW\n**Status:** COMPLETE\nself-review: done\ncold-review: ses_cold\nconsequence: ses_cons\nsecurity: ses_sec\n\n" +
		"### Phase 6: DAEMON-VERIFY\n**Status:** COMPLETE\n**Vorab-Prüfung:** yesmem verify-claims -> exit 0 (accepted)\n\n" +
		"### Phase 7: FINISH\n**Status:** COMPLETE\n**Deploy executed:** no\n**send_to orchestrator:** yes\n"
	if err := s.ScratchpadWrite(project, section+"-verify", report, ""); err != nil {
		t.Fatalf("ScratchpadWrite: %v", err)
	}
	return content
}

// blockedMessageFor renders the message for one verdict state.
func blockedMessageFor(state *doneGateVerifyState) string {
	return doneVerifyBlockedMessage(storage.Agent{ID: "gated", Section: "yesloop-gated", Project: "testproj"}, state)
}

// preCheckKey is the key the pre-check records for this agent's report. The test
// asks production for it, so a second derivation in the test cannot mask a
// mismatch between the two entry points.
func preCheckKey(t *testing.T, h *Handler, agent storage.Agent, report string) string {
	t.Helper()
	repo := agent.WorkDir
	if repo == "" {
		repo = agent.Project
	}
	return h.verifyContextFor(agent, repo, report).key
}

// recordVerdict stores a judgment about this agent's report the way a
// verify_claims run does: under the gate's key and with the gate's rule set.
func recordVerdict(t *testing.T, h *Handler, agent storage.Agent, report string, state *doneGateVerifyState) *doneGateVerifyState {
	t.Helper()
	state.Fingerprint = preCheckKey(t, h, agent, report)
	if state.Mode == "" {
		state.Mode = doneVerifyRequiredMode()
	}
	if state.RecordedAt == 0 {
		state.RecordedAt = time.Now().UnixNano()
	}
	h.recordVerifyVerdict(state)
	return state
}

func acceptedVerdict(t *testing.T, h *Handler, agent storage.Agent, report string) *doneGateVerifyState {
	t.Helper()
	return recordVerdict(t, h, agent, report, &doneGateVerifyState{Status: doneVerifyAccepted})
}

// writeReport replaces the report section beside the agent's pipeline section.
func writeReport(t *testing.T, s *storage.Store, agent storage.Agent, report string) {
	t.Helper()
	if err := s.ScratchpadWrite(agent.Project, verifyReportSection(agent.Section), report, ""); err != nil {
		t.Fatalf("ScratchpadWrite: %v", err)
	}
}

// orchestratorNotices counts the messages the daemon sent to the agent's caller.
// Every notice is one stored channel message, so the count is the reader's own
// view, not an internal counter.
func orchestratorNotices(t *testing.T, s *storage.Store, caller string) int {
	t.Helper()
	var n int
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM agent_messages WHERE target = ?`, caller).Scan(&n); err != nil {
		t.Fatalf("count the notices to %q: %v", caller, err)
	}
	return n
}

// orchestratorMessages returns the notices the daemon sent to the agent's caller, in
// the order they were stored: the escalation is only as good as the text it carries.
func orchestratorMessages(t *testing.T, s *storage.Store, caller string) []string {
	t.Helper()
	rows, err := s.DB().Query(`SELECT content FROM agent_messages WHERE target = ? ORDER BY id`, caller)
	if err != nil {
		t.Fatalf("read the notices to %q: %v", caller, err)
	}
	defer rows.Close()
	var messages []string
	for rows.Next() {
		var content string
		if err := rows.Scan(&content); err != nil {
			t.Fatalf("scan a notice to %q: %v", caller, err)
		}
		messages = append(messages, content)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read the notices to %q: %v", caller, err)
	}
	return messages
}

// contentWrites are the writes that carried a message. The submit key travels on
// its own connection with an empty body, so it is not a message.
func contentWrites(rec *fakeInjectSocket) []string {
	var messages []string
	for _, write := range rec.allWrites() {
		if strings.TrimSpace(write) != "" {
			messages = append(messages, write)
		}
	}
	return messages
}

// requireMessages waits for the inject socket to record want messages and fails
// when they do not arrive.
func requireMessages(t *testing.T, rec *fakeInjectSocket, want int) []string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for len(contentWrites(rec)) < want && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	messages := contentWrites(rec)
	if len(messages) < want {
		t.Fatalf("the agent socket saw %d messages, want %d: %v", len(messages), want, messages)
	}
	return messages
}

func gitIn(t *testing.T, repo string, args ...string) string {
	t.Helper()
	full := append([]string{"-C", repo, "-c", "user.name=Claims", "-c", "user.email=claims@example.com"}, args...)
	cmd := exec.Command("git", full...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// A fresh accepted verdict releases the agent without a run and without a
// checkout. The witness that nothing ran is the verification slot: it is taken
// here, so any attempt to enter the operation would fail, and the release still
// happens.
func TestDoneGateReleasesAReportThePreCheckAccepted(t *testing.T) {
	resetDoneGuardState()
	resetDoneGateVerifyStates()
	h, s := mustHandler(t)
	repo, _, _ := claimsRepo(t)

	const id = "released"
	agent := endToEndAgent(t, h, s, id, repo, "[COMMIT: "+strings.Repeat("0", 40)+"]\n")
	rec := startFakeInjectSocket(t, los24dAgentSock(t, h, id)+".inject")
	verdict := acceptedVerdict(t, h, agent, "[COMMIT: "+strings.Repeat("0", 40)+"]\n")

	// The single slot is occupied: a run entering the operation now would be told
	// that another verification is running.
	h.verifySlot <- struct{}{}
	defer func() { <-h.verifySlot }()

	if !h.gateDoneVerify(agent, h.readAgentScratchpad(agent)) {
		t.Fatal("gateDoneVerify = false, want true: a compliant DONE claim is in scope")
	}
	after, err := s.AgentGet(id)
	if err != nil {
		t.Fatalf("AgentGet: %v", err)
	}
	if after.Status != "running" {
		t.Errorf("status = %q, want running: the stored verdict releases the agent", after.Status)
	}
	state := h.doneGateVerifyStateFor(id)
	if state == nil || state.Status != doneVerifyAccepted || !state.Reused {
		t.Fatalf("state = %+v, want the accepted verdict marked as reused", state)
	}
	if state.Fingerprint != verdict.Fingerprint {
		t.Errorf("fingerprint = %q, want the pre-check's key %q", state.Fingerprint, verdict.Fingerprint)
	}

	// The release names its origin, so no reader believes a run happened at DONE.
	// The fake socket reads the first 64 bytes, so the wire is checked for the
	// verdict word and the constant itself for the origin sentence.
	messages := requireMessages(t, rec, 1)
	if !strings.Contains(messages[0], "DONE-VERIFIED") {
		t.Errorf("the write is not the release: %q", messages[0])
	}
	for _, want := range []string{"Vorab-Prüfung", "nichts neu gerechnet"} {
		if !strings.Contains(doneVerifyReusedReleasePrompt, want) {
			t.Errorf("the release does not name the reused verdict (%q)", want)
		}
	}
	if got := orchestratorNotices(t, s, agent.CallerSession); got != 1 {
		t.Errorf("notices = %d, want 1: the release is a state change", got)
	}

	// A second tick has nothing left to decide and does not write again.
	if !h.gateDoneVerify(*after, h.readAgentScratchpad(*after)) {
		t.Fatal("gateDoneVerify = false on the second tick, want true")
	}
	time.Sleep(100 * time.Millisecond)
	if got := len(contentWrites(rec)); got != 1 {
		t.Errorf("the agent was written %d times, want 1: the release is not repeated", got)
	}
}

// The verdict belongs to the tree it judged. Residue that appears after the
// pre-check makes an accepted verdict a statement about another state, so the gate
// must not release on it: the agent runs the pre-check again and gets the file
// list, which is the whole point of judging the working tree.
func TestDoneGateDoesNotReuseAVerdictForAnotherWorktree(t *testing.T) {
	resetDoneGuardState()
	resetDoneGateVerifyStates()
	h, s := mustHandler(t)
	repo, _, _ := claimsRepo(t)
	report := "[COMMIT: " + strings.Repeat("0", 40) + "]\n"

	const id = "stale-worktree"
	agent := endToEndAgent(t, h, s, id, repo, report)
	startFakeInjectSocket(t, los24dAgentSock(t, h, id)+".inject")

	// The verdict the operation records: bound to the tree as it stood, with the
	// facts a later block reads.
	context := h.verifyContextFor(agent, repo, report)
	h.recordVerifyVerdict(&doneGateVerifyState{
		Fingerprint:   context.key,
		Status:        doneVerifyAccepted,
		Mode:          doneVerifyRequiredMode(),
		ReportDigest:  context.reportDigest,
		Head:          context.head,
		Branch:        context.branch,
		Worktree:      context.worktree,
		PlanRevision:  context.planRevision,
		ReportSession: context.reportSession,
		RecordedAt:    time.Now().UnixNano(),
	})

	// The residue the report cannot see, written after the verdict was recorded.
	if err := os.WriteFile(filepath.Join(repo, "leftover.go"),
		[]byte("package leftover\n"), 0o644); err != nil {
		t.Fatalf("write residue: %v", err)
	}

	h.verifySlot <- struct{}{}
	defer func() { <-h.verifySlot }()
	if !h.gateDoneVerify(agent, h.readAgentScratchpad(agent)) {
		t.Fatal("gateDoneVerify = false, want true: a compliant DONE claim is in scope")
	}

	after, err := s.AgentGet(id)
	if err != nil {
		t.Fatalf("AgentGet: %v", err)
	}
	if after.Status == "running" {
		t.Fatal("the agent was released on a verdict judged against another working tree")
	}
	state := h.doneGateVerifyStateFor(id)
	if state == nil || state.Status != doneVerifyBlocked {
		t.Fatalf("state = %+v, want a block for the changed tree", state)
	}
	if !strings.Contains(state.Reason, "working tree") {
		t.Errorf("reason = %q, want it to name the working tree", state.Reason)
	}
	if !strings.Contains(state.Precondition, "verify-claims") {
		t.Errorf("precondition = %q, want the command that judges the tree", state.Precondition)
	}
}

// A DONE claim without a pre-check is a defect: the gate no longer runs the
// verification itself, and it must say which command produces the verdict.
func TestDoneGateBlocksADoneWithoutAPreCheck(t *testing.T) {
	resetDoneGuardState()
	resetDoneGateVerifyStates()
	h, s := mustHandler(t)
	repo, _, _ := claimsRepo(t)

	const id = "unchecked"
	agent := endToEndAgent(t, h, s, id, repo, "[COMMIT: "+strings.Repeat("0", 40)+"]\n")

	if !h.gateDoneVerify(agent, h.readAgentScratchpad(agent)) {
		t.Fatal("gateDoneVerify = false, want true: an unchecked DONE claim is a verdict")
	}
	after, err := s.AgentGet(id)
	if err != nil {
		t.Fatalf("AgentGet: %v", err)
	}
	if after.Status != "paused" {
		t.Errorf("status = %q, want paused: the report was promoted to DONE unchecked", after.Status)
	}
	if !strings.HasPrefix(after.Progress, "paused: DONE-VERIFY-BLOCKED (defect):") {
		t.Errorf("progress = %q, want the defect block the guard layer matches", after.Progress)
	}
	state := h.doneGateVerifyStateFor(id)
	if state == nil || state.Kind != "defect" || !state.AwaitingVerdict {
		t.Fatalf("state = %+v, want a defect that awaits the verdict", state)
	}
	if len(doneGateVerdicts) != 0 {
		t.Fatalf("the verdict registry holds %d entries, want 0: the gate ran a check of its own", len(doneGateVerdicts))
	}

	message := doneVerifyBlockedMessage(*after, state)
	for _, want := range []string{
		"has not been verified",
		"yesmem verify-claims",
		"--profile " + verify.ProfileYesloopDone,
		"--policy " + doneVerifyPolicyName,
		"--section 'yesloop-" + id + "-verify'",
	} {
		if !strings.Contains(message, want) {
			t.Errorf("message %q does not carry %q", message, want)
		}
	}
	if strings.Contains(message, "did not survive verification") {
		t.Errorf("message %q claims a verification that never ran", message)
	}

	// The tick guard: the same missing precondition is not counted twice.
	if !h.gateDoneVerify(*after, h.readAgentScratchpad(*after)) {
		t.Fatal("gateDoneVerify = false on the second tick, want true")
	}
	if got := h.doneGateVerifyStateFor(id); got == nil || got.BlockedRounds != 1 {
		t.Errorf("state = %+v, want one counted round for one missing precondition", got)
	}
}

// A verdict only applies while the facts it was reached on hold. When the text,
// the tree or the branch moved, the gate says which one did, instead of asking
// the agent to reproduce a verdict it already has.
func TestDoneGateNamesWhyAStoredVerdictIsStale(t *testing.T) {
	const report = "[COMMIT: " + "0000000000000000000000000000000000000000" + "]\n"

	for _, tc := range []struct {
		name   string
		mutate func(t *testing.T, h *Handler, s *storage.Store, agent storage.Agent) string
		want   string
	}{
		{
			name: "the report text changed",
			mutate: func(t *testing.T, h *Handler, s *storage.Store, agent storage.Agent) string {
				rewritten := report + "\n[PLAN: somewhere]\n"
				writeReport(t, s, agent, rewritten)
				return rewritten
			},
			want: "the report text changed",
		},
		{
			name: "HEAD moved",
			mutate: func(t *testing.T, h *Handler, s *storage.Store, agent storage.Agent) string {
				gitIn(t, agent.WorkDir, "commit", "-q", "--allow-empty", "-m", "work after the verdict")
				return report
			},
			want: "HEAD moved",
		},
		{
			name: "the branch changed",
			mutate: func(t *testing.T, h *Handler, s *storage.Store, agent storage.Agent) string {
				gitIn(t, agent.WorkDir, "checkout", "-q", "-b", "feature")
				return report
			},
			want: "the branch changed",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resetDoneGuardState()
			resetDoneGateVerifyStates()
			h, s := mustHandler(t)
			repo, _, _ := claimsRepo(t)

			const id = "stale"
			agent := endToEndAgent(t, h, s, id, repo, report)
			// The agent carried an accepted verdict from an earlier tick...
			old := h.verifyContextFor(agent, repo, report)
			h.replaceDoneGateVerifyState(id, &doneGateVerifyState{
				Fingerprint:   old.key,
				Status:        doneVerifyAccepted,
				Mode:          doneVerifyRequiredMode(),
				ReportDigest:  old.reportDigest,
				Head:          old.head,
				Branch:        old.branch,
				PlanRevision:  old.planRevision,
				ReportSession: old.reportSession,
			})

			current := tc.mutate(t, h, s, agent)
			row, err := s.AgentGet(id)
			if err != nil {
				t.Fatalf("AgentGet: %v", err)
			}
			if !h.gateDoneVerify(*row, h.readAgentScratchpad(*row)) {
				t.Fatal("gateDoneVerify = false, want true")
			}
			state := h.doneGateVerifyStateFor(id)
			if state == nil || state.Status != doneVerifyBlocked || !state.AwaitingVerdict {
				t.Fatalf("state = %+v, want a defect that awaits a fresh verdict", state)
			}
			if !strings.Contains(state.StaleBecause, tc.want) {
				t.Errorf("stale reason = %q, want it to say %q", state.StaleBecause, tc.want)
			}
			message := doneVerifyBlockedMessage(*row, state)
			if !strings.Contains(message, tc.want) {
				t.Errorf("message %q does not name why the verdict no longer applies (%q)", message, tc.want)
			}
			if !strings.Contains(message, current[:20]) && !strings.Contains(message, "yesmem verify-claims") {
				t.Errorf("message %q does not name the way to a new verdict", message)
			}
		})
	}
}

// A verdict is diagnosed against the verdict itself when the agent has no state of
// its own: a plan written after the run moves the key, and the block must say that
// the report is fine and the judgement is not. The order "plan first, then the
// run" is the agent's contract; this is what a violation looks like.
func TestDoneGateNamesAPlanChangeWithoutAPriorAgentState(t *testing.T) {
	resetDoneGuardState()
	resetDoneGateVerifyStates()
	h, s := mustHandler(t)
	repo, _, _ := claimsRepo(t)

	const id = "plan-moved"
	const report = "[COMMIT: 0]\n[PLAN: testproj]\n"
	agent := endToEndAgent(t, h, s, id, repo, report)
	thread := agentPlanThread(agent)

	// The plan as the pre-check saw it.
	openPlan := &storage.PlanRow{
		ThreadID: thread, Content: "the work", Status: "active", Scope: "session", Project: "testproj",
		Items: `[{"Content":"finish the gate","Status":"in_progress"}]`,
	}
	if err := s.UpsertPlan(openPlan); err != nil {
		t.Fatalf("UpsertPlan: %v", err)
	}
	old := h.verifyContextFor(agent, repo, report)
	h.recordVerifyVerdict(&doneGateVerifyState{
		Fingerprint:   old.key,
		Status:        doneVerifyAccepted,
		Mode:          doneVerifyRequiredMode(),
		ReportDigest:  old.reportDigest,
		Repo:          repo,
		Head:          old.head,
		Branch:        old.branch,
		PlanRevision:  old.planRevision,
		ReportSession: old.reportSession,
	})

	// The agent closes the step after the run.
	closedPlan := *openPlan
	closedPlan.Items = `[{"Content":"finish the gate","Status":"completed"}]`
	if err := s.UpsertPlan(&closedPlan); err != nil {
		t.Fatalf("UpsertPlan: %v", err)
	}

	if !h.gateDoneVerify(agent, h.readAgentScratchpad(agent)) {
		t.Fatal("gateDoneVerify = false, want true")
	}
	state := h.doneGateVerifyStateFor(id)
	if state == nil || state.Status != doneVerifyBlocked || !state.AwaitingVerdict {
		t.Fatalf("state = %+v, want a block that awaits a fresh verdict", state)
	}
	if !strings.Contains(state.StaleBecause, "the plan changed") {
		t.Errorf("stale reason = %q, want the plan named", state.StaleBecause)
	}
	message := doneVerifyBlockedMessage(*agentRow(t, s, id), state)
	if !strings.Contains(message, "the plan changed") {
		t.Errorf("message %q does not name the plan change", message)
	}
	if strings.Contains(message, "Fix the report section") {
		t.Errorf("message %q sends the agent to an unchanged report", message)
	}
	if strings.Contains(message, "has not been verified") {
		t.Errorf("message %q claims nothing was checked although a verdict exists", message)
	}
	if !strings.Contains(message, "yesmem verify-claims") {
		t.Errorf("message %q does not name the command that produces a new verdict", message)
	}
}

// agentRow re-reads the agent row the gate has just written.
func agentRow(t *testing.T, s *storage.Store, id string) *storage.Agent {
	t.Helper()
	agent, err := s.AgentGet(id)
	if err != nil {
		t.Fatalf("AgentGet: %v", err)
	}
	return agent
}

// The hold while a pre-check runs is part of the same attempt. A tick in that
// window must not reset the rounds the agent already collected, or a correction
// that starts its run at the right moment could erase its way past the escalation.
func TestDoneGateKeepsTheRoundsWhileThePreCheckRuns(t *testing.T) {
	resetDoneGuardState()
	resetDoneGateVerifyStates()
	h, s := mustHandler(t)
	repo, _, _ := claimsRepo(t)

	const id = "rounds-inflight"
	agent := endToEndAgent(t, h, s, id, repo, "[COMMIT: 0]\n")
	los24dAgentSock(t, h, id)

	for round := 1; round <= 2; round++ {
		writeReport(t, s, agent, fmt.Sprintf("[COMMIT: %d]\n", round))
		row := agentRow(t, s, id)
		if !h.gateDoneVerify(*row, h.readAgentScratchpad(*row)) {
			t.Fatalf("gateDoneVerify = false in round %d, want true", round)
		}
	}
	if got := h.doneGateVerifyStateFor(id); got == nil || got.BlockedRounds != 2 {
		t.Fatalf("state = %+v, want two blocked rounds", got)
	}

	// The third correction, with its pre-check running while the DONE claim is
	// already readable.
	const third = "[COMMIT: 3]\n"
	writeReport(t, s, agent, third)
	key := preCheckKey(t, h, agent, third)
	beginVerifyRun(key)
	row := agentRow(t, s, id)
	if !h.gateDoneVerify(*row, h.readAgentScratchpad(*row)) {
		t.Fatal("gateDoneVerify = false while the pre-check runs, want true")
	}
	pending := h.doneGateVerifyStateFor(id)
	if pending == nil || pending.Status != doneVerifyPending || pending.BlockedRounds != 2 {
		t.Fatalf("state = %+v, want the hold to carry the two rounds", pending)
	}

	// The run refuses the corrected report: the third round counts and escalates.
	h.recordVerifyVerdict(&doneGateVerifyState{
		Fingerprint: key, Status: doneVerifyBlocked, Kind: "refuted",
		Reason: "the commit in the report does not exist", Mode: doneVerifyRequiredMode(),
	})
	endVerifyRun(key)
	row = agentRow(t, s, id)
	if !h.gateDoneVerify(*row, h.readAgentScratchpad(*row)) {
		t.Fatal("gateDoneVerify = false after the verdict, want true")
	}
	final := h.doneGateVerifyStateFor(id)
	if final == nil || final.BlockedRounds != doneVerifyEscalateAfter || !final.Escalated {
		t.Fatalf("state = %+v, want the third round escalated", final)
	}
}

// A report the pre-check refused is blocked with its findings, and nothing is
// recomputed: the findings are the run's, including the command that reproduces
// them.
func TestDoneGateReusesABlockedVerdict(t *testing.T) {
	resetDoneGuardState()
	resetDoneGateVerifyStates()
	h, s := mustHandler(t)
	repo, _, _ := claimsRepo(t)

	const id = "refuted"
	const report = "[COMMIT: " + "1111111111111111111111111111111111111111" + "]\n"
	agent := endToEndAgent(t, h, s, id, repo, report)
	recordVerdict(t, h, agent, report, &doneGateVerifyState{
		Status: doneVerifyBlocked,
		Kind:   "refuted",
		Reason: "1 claim(s) refuted: the commit does not exist",
		Findings: []verifyClaimFinding{{
			Kind:    "commit_exists",
			Status:  "refuted",
			Reason:  "commit 1111111 does not exist",
			Command: "git -C " + repo + " rev-parse --verify --quiet 1111111^{commit}",
		}},
	})

	if !h.gateDoneVerify(agent, h.readAgentScratchpad(agent)) {
		t.Fatal("gateDoneVerify = false, want true")
	}
	after, err := s.AgentGet(id)
	if err != nil {
		t.Fatalf("AgentGet: %v", err)
	}
	if after.Status != "paused" || !strings.Contains(after.Progress, "DONE-VERIFY-BLOCKED (refuted)") {
		t.Errorf("status/progress = %q/%q, want a refuted block", after.Status, after.Progress)
	}
	state := h.doneGateVerifyStateFor(id)
	if state == nil || len(state.Findings) != 1 || !state.Reused {
		t.Fatalf("state = %+v, want the stored finding reused", state)
	}
	message := doneVerifyBlockedMessage(*after, state)
	for _, want := range []string{"commit_exists", "does not exist", "rev-parse", "yesmem verify-claims"} {
		if !strings.Contains(message, want) {
			t.Errorf("message %q does not carry %q", message, want)
		}
	}
}

// A judgment reached under a weaker rule set proves less than the DONE gate
// requires: an empty profile does not demand the report's classes. Such a verdict
// is refused by name.
func TestDoneGateRefusesAVerdictFromAWeakerRuleSet(t *testing.T) {
	resetDoneGuardState()
	resetDoneGateVerifyStates()
	h, s := mustHandler(t)
	repo, _, _ := claimsRepo(t)

	const id = "weak"
	const report = "[COMMIT: " + "0" + "]\n"
	agent := endToEndAgent(t, h, s, id, repo, report)
	recordVerdict(t, h, agent, report, &doneGateVerifyState{
		Status: doneVerifyAccepted,
		Mode:   "/yesloop-done",
	})

	if !h.gateDoneVerify(agent, h.readAgentScratchpad(agent)) {
		t.Fatal("gateDoneVerify = false, want true")
	}
	state := h.doneGateVerifyStateFor(id)
	if state == nil || state.Status != doneVerifyBlocked || state.Kind != "defect" {
		t.Fatalf("state = %+v, want a defect: the verdict was judged under another rule set", state)
	}
	if !strings.Contains(state.Reason, "/yesloop-done") || !strings.Contains(state.Reason, doneVerifyRequiredMode()) {
		t.Errorf("reason = %q, want both rule sets named", state.Reason)
	}

	// The same unusable verdict is one decision, not one per tick: a refusal that
	// counted a round every 30 seconds would escalate a report nobody can fix.
	row, err := s.AgentGet(id)
	if err != nil {
		t.Fatalf("AgentGet: %v", err)
	}
	if !h.gateDoneVerify(*row, h.readAgentScratchpad(*row)) {
		t.Fatal("gateDoneVerify = false on the second tick, want true")
	}
	if got := h.doneGateVerifyStateFor(id); got == nil || got.BlockedRounds != 1 {
		t.Errorf("rounds = %+v, want the refusal counted once", got)
	}
}

// The pre-check may still be running when the DONE claim lands, because an agent
// writes its phase blocks before it starts the check. Holding the agent is the
// honest answer; blocking it would tell it to start the run it is in the middle
// of.
func TestDoneGateHoldsTheAgentWhileThePreCheckRuns(t *testing.T) {
	resetDoneGuardState()
	resetDoneGateVerifyStates()
	h, s := mustHandler(t)
	repo, _, _ := claimsRepo(t)

	const id = "running-check"
	const report = "[COMMIT: " + "0" + "]\n"
	agent := endToEndAgent(t, h, s, id, repo, report)
	rec := startFakeInjectSocket(t, los24dAgentSock(t, h, id)+".inject")
	key := preCheckKey(t, h, agent, report)
	beginVerifyRun(key)

	if !h.gateDoneVerify(agent, h.readAgentScratchpad(agent)) {
		t.Fatal("gateDoneVerify = false, want true")
	}
	after, err := s.AgentGet(id)
	if err != nil {
		t.Fatalf("AgentGet: %v", err)
	}
	if !strings.HasPrefix(after.Progress, "paused: DONE-PENDING-VERIFY:") {
		t.Fatalf("progress = %q, want the DONE-PENDING-VERIFY hold", after.Progress)
	}
	if state := h.doneGateVerifyStateFor(id); state == nil || state.Status != doneVerifyPending {
		t.Fatalf("state = %+v, want pending", state)
	}
	// The hold's instruction is the daemon's fixed text: the agent must know that
	// it waits, that the check is running, and what each verdict means.
	for _, part := range []string{
		"DONE-PENDING-VERIFY", "kein sleep, kein Polling", "prüft deinen Bericht jetzt",
		"Auf DONE-VERIFIED bist du fertig", "korrigierst du", "environment, limit oder error",
	} {
		if !strings.Contains(doneVerifyFrozenPrompt, part) {
			t.Errorf("the frozen prompt does not say %q", part)
		}
	}
	messages := requireMessages(t, rec, 1)
	if !strings.Contains(messages[0], "DONE-PENDING-VERIFY") {
		t.Errorf("the first write does not hold the agent: %q", messages[0])
	}

	// The hold is announced once. A second and third tick in the same window must
	// not write again: the frozen prompt says so explicitly, and every repeat would
	// wake the agent out of the hold it was just put into.
	for tick := 2; tick <= 3; tick++ {
		row, err := s.AgentGet(id)
		if err != nil {
			t.Fatalf("AgentGet: %v", err)
		}
		if !h.gateDoneVerify(*row, h.readAgentScratchpad(*row)) {
			t.Fatalf("gateDoneVerify = false on tick %d, want true", tick)
		}
	}
	time.Sleep(100 * time.Millisecond)
	if got := len(contentWrites(rec)); got != 1 {
		t.Errorf("the hold was written %d times, want 1: an unchanged hold is not news", got)
	}
	if got := orchestratorNotices(t, s, agent.CallerSession); got != 1 {
		t.Errorf("notices = %d, want 1: the hold is one state change", got)
	}

	// The run finishes and records its verdict. The next tick releases the agent.
	h.recordVerifyVerdict(&doneGateVerifyState{
		Fingerprint: key, Status: doneVerifyAccepted, Mode: doneVerifyRequiredMode(),
	})
	endVerifyRun(key)
	row, err := s.AgentGet(id)
	if err != nil {
		t.Fatalf("AgentGet: %v", err)
	}
	if !h.gateDoneVerify(*row, h.readAgentScratchpad(*row)) {
		t.Fatal("gateDoneVerify = false after the verdict, want true")
	}
	row, err = s.AgentGet(id)
	if err != nil {
		t.Fatalf("AgentGet: %v", err)
	}
	if row.Status != "running" {
		t.Errorf("status = %q, want running after the accepted verdict", row.Status)
	}
	messages = requireMessages(t, rec, 2)
	if !strings.Contains(messages[1], "DONE-VERIFIED") {
		t.Errorf("the second write is not the release: %q", messages[1])
	}
	if !strings.Contains(doneVerifyReusedReleasePrompt, "Vorab-Prüfung") {
		t.Error("the reused release does not name where its verdict came from")
	}
}

// The orchestrator owns the loop and is told about verdicts, not about ticks.
func TestDoneGateNotifiesTheOrchestratorOncePerStateChange(t *testing.T) {
	resetDoneGuardState()
	resetDoneGateVerifyStates()
	h, s := mustHandler(t)
	repo, _, _ := claimsRepo(t)

	const id = "quiet"
	const report = "[COMMIT: " + "0" + "]\n"
	agent := endToEndAgent(t, h, s, id, repo, report)
	caller := agent.CallerSession
	startFakeInjectSocket(t, los24dAgentSock(t, h, id)+".inject")

	if !h.gateDoneVerify(agent, h.readAgentScratchpad(agent)) {
		t.Fatal("gateDoneVerify = false, want true")
	}
	if got := orchestratorNotices(t, s, caller); got != 1 {
		t.Fatalf("notices = %d after the first tick, want 1: the block is a new state", got)
	}
	// Same missing verdict, same state: the orchestrator already knows.
	if !h.gateDoneVerify(agent, h.readAgentScratchpad(agent)) {
		t.Fatal("gateDoneVerify = false on the second tick, want true")
	}
	if got := orchestratorNotices(t, s, caller); got != 1 {
		t.Errorf("notices = %d after an unchanged tick, want 1", got)
	}

	// The verdict arrives, and that is a change.
	acceptedVerdict(t, h, agent, report)
	row, err := s.AgentGet(id)
	if err != nil {
		t.Fatalf("AgentGet: %v", err)
	}
	if !h.gateDoneVerify(*row, h.readAgentScratchpad(*row)) {
		t.Fatal("gateDoneVerify = false after the verdict, want true")
	}
	if got := orchestratorNotices(t, s, caller); got != 2 {
		t.Errorf("notices = %d after the accepted verdict, want 2", got)
	}
	row, _ = s.AgentGet(id)
	if !h.gateDoneVerify(*row, h.readAgentScratchpad(*row)) {
		t.Fatal("gateDoneVerify = false on the tick after the release, want true")
	}
	if got := orchestratorNotices(t, s, caller); got != 2 {
		t.Errorf("notices = %d after an unchanged release tick, want 2", got)
	}
}

// A hold whose run vanished must become a verdict. EndVerifyRun without a stored
// verdict is a run that died, and staying pending would freeze the agent with no
// message and no counted round.
func TestDoneGateBlocksWhenThePreCheckVanishes(t *testing.T) {
	resetDoneGuardState()
	resetDoneGateVerifyStates()
	h, s := mustHandler(t)
	repo, _, _ := claimsRepo(t)

	const id = "vanished"
	const report = "[COMMIT: " + "0" + "]\n"
	agent := endToEndAgent(t, h, s, id, repo, report)
	los24dAgentSock(t, h, id)
	key := preCheckKey(t, h, agent, report)

	beginVerifyRun(key)
	if !h.gateDoneVerify(agent, h.readAgentScratchpad(agent)) {
		t.Fatal("gateDoneVerify = false, want true")
	}
	if state := h.doneGateVerifyStateFor(id); state == nil || state.Status != doneVerifyPending {
		t.Fatalf("state = %+v, want the hold while the run works", state)
	}

	// The run ended without recording a verdict.
	endVerifyRun(key)
	row, err := s.AgentGet(id)
	if err != nil {
		t.Fatalf("AgentGet: %v", err)
	}
	if !h.gateDoneVerify(*row, h.readAgentScratchpad(*row)) {
		t.Fatal("gateDoneVerify = false after the run vanished, want true")
	}
	state := h.doneGateVerifyStateFor(id)
	if state == nil || state.Status != doneVerifyBlocked || !state.AwaitingVerdict {
		t.Fatalf("state = %+v, want a defect: the run is gone and no verdict exists", state)
	}
	if state.BlockedRounds != 1 {
		t.Errorf("rounds = %d, want the vanished run counted once", state.BlockedRounds)
	}
}

// The findings are the lever that lets a paused agent correct itself, and the
// verdict is what the instruction told it to wait for. A write that never landed
// is owed again once the socket is there, up to the delivery limit.
func TestDoneGateRepeatsALostVerdictMessage(t *testing.T) {
	resetDoneGuardState()
	resetDoneGateVerifyStates()
	h, s := mustHandler(t)
	repo, _, _ := claimsRepo(t)

	const id = "lost-findings"
	const report = "[COMMIT: " + "0" + "]\n"
	agent := endToEndAgent(t, h, s, id, repo, report)
	sockPath := los24dAgentSock(t, h, id) // no listener yet

	if !h.gateDoneVerify(agent, h.readAgentScratchpad(agent)) {
		t.Fatal("gateDoneVerify = false, want true")
	}
	blocked := h.doneGateVerifyStateFor(id)
	if blocked == nil || blocked.Status != doneVerifyBlocked || blocked.Notified != "" {
		t.Fatalf("state = %+v, want the block unmarked: it never reached the agent", blocked)
	}

	rec := startFakeInjectSocket(t, sockPath+".inject")
	row, err := s.AgentGet(id)
	if err != nil {
		t.Fatalf("AgentGet: %v", err)
	}
	if !h.gateDoneVerify(*row, h.readAgentScratchpad(*row)) {
		t.Fatal("gateDoneVerify = false on the next tick, want true")
	}
	writes := requireMessages(t, rec, 1)
	if !strings.Contains(writes[0], "DONE-VERIFY-BLOCKED") {
		t.Errorf("the delivered message is not the verdict: %q", writes[0])
	}
	if got := h.doneGateVerifyStateFor(id); got == nil || got.Notified == "" {
		t.Errorf("state = %+v, want the delivered verdict marked", got)
	}
}

// A socket that does not come up must cost a handful of attempts, not one per tick
// for the life of the row: the hold stands, but the gate stops poking the agent.
func TestDoneGateGivesUpPokingAGoneSocket(t *testing.T) {
	resetDoneGuardState()
	resetDoneGateVerifyStates()
	h, s := mustHandler(t)
	repo, _, _ := claimsRepo(t)

	const id = "gone"
	const report = "[COMMIT: " + "0" + "]\n"
	agent := endToEndAgent(t, h, s, id, repo, report)
	los24dAgentSock(t, h, id) // a path without a listener: every write fails

	for attempt := 1; attempt <= doneVerifyMaxDeliveries+1; attempt++ {
		if !h.gateDoneVerify(agent, h.readAgentScratchpad(agent)) {
			t.Fatalf("gateDoneVerify = false on attempt %d, want true", attempt)
		}
	}
	state := h.doneGateVerifyStateFor(id)
	if state == nil {
		t.Fatal("no state after the attempts")
	}
	if state.Deliveries != doneVerifyMaxDeliveries {
		t.Errorf("deliveries = %d, want %d: the gate stops after its attempts", state.Deliveries, doneVerifyMaxDeliveries)
	}
	if state.Notified == "" {
		t.Error("the notice was withdrawn after the last attempt, want it recorded: the gate has given up")
	}
}

// Three blocked rounds on one attempt mean the correction is not the agent's to
// make: the gate keeps it held, hands the case to the orchestrator with the rounds
// it collected, and the agent is told to wait. A correction is a new report text
// but the same attempt, so the count survives the rewrite: it is the agent that is
// stuck, not one text.
func TestDoneGateEscalatesAfterThreeBlockedRounds(t *testing.T) {
	resetDoneGuardState()
	resetDoneGateVerifyStates()
	h, s := mustHandler(t)
	repo, _, _ := claimsRepo(t)

	const id = "escalate"
	agent := endToEndAgent(t, h, s, id, repo, "[COMMIT: 0]\n")
	caller := agent.CallerSession
	rec := startFakeInjectSocket(t, los24dAgentSock(t, h, id)+".inject")

	for round := 1; round <= doneVerifyEscalateAfter; round++ {
		writeReport(t, s, agent, fmt.Sprintf("[COMMIT: %d]\n", round))
		row, err := s.AgentGet(id)
		if err != nil {
			t.Fatalf("AgentGet in round %d: %v", round, err)
		}
		if !h.gateDoneVerify(*row, h.readAgentScratchpad(*row)) {
			t.Fatalf("gateDoneVerify = false in round %d, want true", round)
		}
	}

	escalated := h.doneGateVerifyStateFor(id)
	if escalated == nil || !escalated.Escalated {
		t.Fatalf("state = %+v, want the third block escalated", escalated)
	}
	if escalated.BlockedRounds != doneVerifyEscalateAfter {
		t.Errorf("rounds = %d, want %d: a correction is a new report but the same attempt", escalated.BlockedRounds, doneVerifyEscalateAfter)
	}
	if len(escalated.RoundNotes) != doneVerifyEscalateAfter {
		t.Errorf("round notes = %v, want one per round", escalated.RoundNotes)
	}

	notices := orchestratorMessages(t, s, caller)
	if len(notices) == 0 {
		t.Fatal("the orchestrator was never told")
	}
	last := notices[len(notices)-1]
	for _, want := range []string{"DONE-VERIFY-ESCALATED", "was blocked 3 times", "defect", "Open question", verifyReportSection(agent.Section)} {
		if !strings.Contains(last, want) {
			t.Errorf("the escalation does not say %q: %q", want, last)
		}
	}

	messages := contentWrites(rec)
	if len(messages) == 0 || !strings.Contains(messages[len(messages)-1], "Eskaliert an den Orchestrator") {
		t.Fatalf("the last message to the agent is %q, want the escalation sentence", messages)
	}
	row, err := s.AgentGet(id)
	if err != nil {
		t.Fatalf("AgentGet: %v", err)
	}
	if row.Status != "paused" {
		t.Errorf("status = %q, want the agent held", row.Status)
	}
	if !strings.HasPrefix(row.Progress, "paused: DONE-VERIFY-BLOCKED") || !strings.Contains(row.Progress, "escalated") {
		t.Errorf("progress = %q, want a blocked line that names the escalation", row.Progress)
	}
	// Nothing is asked of the agent after the escalation, and no round is counted.
	before := len(contentWrites(rec))
	if !h.gateDoneVerify(*row, h.readAgentScratchpad(*row)) {
		t.Fatal("gateDoneVerify = false after the escalation, want true")
	}
	time.Sleep(100 * time.Millisecond)
	if got := len(contentWrites(rec)); got != before {
		t.Errorf("the agent was written %d times, want %d: nothing is asked after the escalation", got, before)
	}
}

// The count belongs to the attempt, and a passed verdict ends the attempt: a later
// report cannot inherit an escalation it never earned.
func TestDoneGateResetsTheBlockedRoundsOnAPassedReport(t *testing.T) {
	resetDoneGuardState()
	resetDoneGateVerifyStates()
	h, s := mustHandler(t)
	repo, _, _ := claimsRepo(t)

	const id = "reset"
	agent := endToEndAgent(t, h, s, id, repo, "[COMMIT: 0]\n")
	los24dAgentSock(t, h, id)

	for round := 1; round <= 2; round++ {
		writeReport(t, s, agent, fmt.Sprintf("[COMMIT: %d]\n", round))
		row, err := s.AgentGet(id)
		if err != nil {
			t.Fatalf("AgentGet in round %d: %v", round, err)
		}
		if !h.gateDoneVerify(*row, h.readAgentScratchpad(*row)) {
			t.Fatalf("gateDoneVerify = false in round %d, want true", round)
		}
	}
	if got := h.doneGateVerifyStateFor(id); got == nil || got.BlockedRounds != 2 {
		t.Fatalf("state = %+v, want two blocked rounds on record", got)
	}

	report := "[COMMIT: 3]\n"
	writeReport(t, s, agent, report)
	acceptedVerdict(t, h, agent, report)
	row, err := s.AgentGet(id)
	if err != nil {
		t.Fatalf("AgentGet: %v", err)
	}
	if !h.gateDoneVerify(*row, h.readAgentScratchpad(*row)) {
		t.Fatal("gateDoneVerify = false for the accepted report, want true")
	}
	passed := h.doneGateVerifyStateFor(id)
	if passed == nil || passed.Status != doneVerifyAccepted {
		t.Fatalf("state = %+v, want the accepted verdict", passed)
	}
	if passed.BlockedRounds != 0 || len(passed.RoundNotes) != 0 || passed.Escalated {
		t.Errorf("state = %+v, want a cleared counter after a passed report", passed)
	}
}

// The release is how the orchestrator answers, so the next attempt starts at zero
// instead of carrying two rounds toward an escalation nobody asked for.
func TestDoneGateResetsTheBlockedRoundsWhenTheOrchestratorReleasesTheAgent(t *testing.T) {
	resetDoneGuardState()
	resetDoneGateVerifyStates()
	h, s := mustHandler(t)
	repo, _, _ := claimsRepo(t)

	const id = "released-rounds"
	agent := endToEndAgent(t, h, s, id, repo, "[COMMIT: 0]\n")
	los24dAgentSock(t, h, id)

	for round := 1; round <= 2; round++ {
		writeReport(t, s, agent, fmt.Sprintf("[COMMIT: %d]\n", round))
		row, err := s.AgentGet(id)
		if err != nil {
			t.Fatalf("AgentGet in round %d: %v", round, err)
		}
		if !h.gateDoneVerify(*row, h.readAgentScratchpad(*row)) {
			t.Fatalf("gateDoneVerify = false in round %d, want true", round)
		}
	}

	// The orchestrator takes the row back; the gate no longer holds it.
	h.unpauseAgent(id, "TEST", "the orchestrator takes the case")
	writeReport(t, s, agent, "[COMMIT: 99]\n")
	row, err := s.AgentGet(id)
	if err != nil {
		t.Fatalf("AgentGet: %v", err)
	}
	if !h.gateDoneVerify(*row, h.readAgentScratchpad(*row)) {
		t.Fatal("gateDoneVerify = false after the release, want true")
	}
	if got := h.doneGateVerifyStateFor(id); got == nil || got.BlockedRounds != 1 {
		t.Errorf("state = %+v, want a fresh attempt counting from one", got)
	}
}

// An environment block is decided from the text as it is now, so it must replace
// any verdict of an older report. A refused write would leave the block unstored
// and every tick would pause, notify and relay the same message again.
func TestEnvironmentBlockReplacesTheVerdictOfAnOlderReport(t *testing.T) {
	resetDoneGuardState()
	resetDoneGateVerifyStates()
	h, s := mustHandler(t)
	repo, _, _ := claimsRepo(t)

	const id = "environment-after-verdict"
	agent := endToEndAgent(t, h, s, id, repo, "[COMMIT: 0]\n")
	content := h.readAgentScratchpad(agent)

	h.replaceDoneGateVerifyState(id, &doneGateVerifyState{
		Fingerprint: "verdict-on-an-older-report",
		Status:      doneVerifyBlocked,
		Kind:        "refuted",
		Reason:      "commit 0000000 does not exist",
	})

	// The store cannot answer, so the section read fails and the tick reaches the
	// environment block instead of a verdict lookup.
	s.Close()

	if !h.gateDoneVerify(agent, content) {
		t.Fatal("gateDoneVerify = false, want true: an unreadable section is handled here")
	}
	stored := h.doneGateVerifyStateFor(id)
	if stored == nil || stored.Kind != "environment" {
		t.Fatalf("state = %+v, want the environment block to replace the older verdict", stored)
	}
	if stored.Fingerprint == "verdict-on-an-older-report" {
		t.Fatalf("fingerprint = %q, want the block bound to the report judged now", stored.Fingerprint)
	}

	// The stored block is found again on the next tick and left alone.
	stored.Reason = "sentinel"
	h.replaceDoneGateVerifyState(id, stored)
	if !h.gateDoneVerify(agent, content) {
		t.Fatal("gateDoneVerify = false on the second tick, want true")
	}
	if got := h.doneGateVerifyStateFor(id); got == nil || got.Reason != "sentinel" {
		t.Errorf("state reason = %q, want the stored block left alone", got.Reason)
	}
}

// A read error must not discard a verdict that is being produced, and it must not
// freeze an agent that was already released. Both states are decided elsewhere.
func TestEnvironmentBlockLeavesAPendingOrAcceptedVerdictAlone(t *testing.T) {
	for _, status := range []string{doneVerifyPending, doneVerifyAccepted} {
		t.Run(status, func(t *testing.T) {
			resetDoneGuardState()
			resetDoneGateVerifyStates()
			h, s := mustHandler(t)
			repo, _, _ := claimsRepo(t)

			const id = "environment-during-verdict"
			agent := endToEndAgent(t, h, s, id, repo, "[COMMIT: 0]\n")
			content := h.readAgentScratchpad(agent)

			h.replaceDoneGateVerifyState(id, &doneGateVerifyState{
				Fingerprint: "verdict-of-the-report",
				Status:      status,
			})
			s.Close()

			if !h.gateDoneVerify(agent, content) {
				t.Fatal("gateDoneVerify = false, want true")
			}
			stored := h.doneGateVerifyStateFor(id)
			if stored == nil {
				t.Fatal("the state was dropped")
			}
			if stored.Status != status || stored.Fingerprint != "verdict-of-the-report" {
				t.Errorf("state = %+v, want the %s verdict left in place", stored, status)
			}
		})
	}
}

// An empty report section is the one case the agent cannot read its way out of:
// the block message has to carry the shape the verifier reads.
func TestDoneVerifyMessageCarriesTheReportTemplateWhenTheSectionIsEmpty(t *testing.T) {
	message := blockedMessageFor(&doneGateVerifyState{
		Status:      doneVerifyBlocked,
		Kind:        "nothing-confirmed",
		Reason:      "no claim was confirmed, so the report proves nothing",
		ReportEmpty: true,
	})
	for _, line := range []string{
		"[COMMIT:", "[BRANCH:", "[DEPLOY:", "Tests run:", "Files in scope:", "[EVIDENCE:", "[PLAN:",
	} {
		if !strings.Contains(message, line) {
			t.Errorf("message %q does not carry the pattern line %q", message, line)
		}
	}
	if !strings.Contains(message, "yesloop-gated-verify") {
		t.Errorf("message %q does not name the section to fill", message)
	}
}

// A report that states no claim cannot be verified, and the presence check answers
// before any verdict lookup. Its block carries the shape and the command, and no
// verdict is recorded.
func TestDoneGateBlocksAClaimlessReportWithoutARun(t *testing.T) {
	for _, tc := range []struct {
		name      string
		report    string
		drop      bool
		wantSay   string
		wantPause string
	}{
		{
			name:      "the section carries text but no claim",
			report:    "Everything is done.\nNo further remarks.\n",
			wantSay:   "states no claim the verifier reads",
			wantPause: "the report states no claim",
		},
		{
			name:      "the section is missing",
			drop:      true,
			wantSay:   "carries no text",
			wantPause: "the report section carries no text",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resetDoneGuardState()
			resetDoneGateVerifyStates()
			h, s := mustHandler(t)
			repo, _, _ := claimsRepo(t)

			const id = "presence"
			agent := endToEndAgent(t, h, s, id, repo, tc.report)
			section := "yesloop-" + id
			if tc.drop {
				if _, err := s.ScratchpadDelete("testproj", verifyReportSection(section)); err != nil {
					t.Fatalf("ScratchpadDelete: %v", err)
				}
			}

			if !h.gateDoneVerify(agent, h.readAgentScratchpad(agent)) {
				t.Fatal("gateDoneVerify = false, want true: an unstated report is a verdict")
			}
			if len(doneGateVerdicts) != 0 {
				t.Fatalf("the verdict registry holds %d entries, want 0: a report without a claim needs no run", len(doneGateVerdicts))
			}

			after, err := s.AgentGet(id)
			if err != nil {
				t.Fatalf("AgentGet: %v", err)
			}
			if after.Status != "paused" {
				t.Errorf("status = %q, want paused", after.Status)
			}
			if !strings.HasPrefix(after.Progress, "paused: DONE-VERIFY-BLOCKED (defect):") {
				t.Errorf("progress = %q, want the defect block the guard layer matches", after.Progress)
			}
			if !strings.Contains(after.Progress, tc.wantPause) {
				t.Errorf("progress = %q, want it to say %q", after.Progress, tc.wantPause)
			}

			state := h.doneGateVerifyStateFor(id)
			if state == nil || state.Status != doneVerifyBlocked || state.Kind != "defect" {
				t.Fatalf("state = %+v, want blocked as a defect", state)
			}

			message := doneVerifyBlockedMessage(*after, state)
			if !strings.Contains(message, reportTemplate) {
				t.Errorf("message %q does not carry the report pattern", message)
			}
			if strings.Contains(message, "did not survive verification") {
				t.Errorf("message %q claims a verification that never started", message)
			}
			if !strings.Contains(message, tc.wantSay) {
				t.Errorf("message %q does not say what is missing (%q)", message, tc.wantSay)
			}
			if !strings.Contains(message, "yesmem verify-claims") {
				t.Errorf("message %q does not name the command that produces a verdict", message)
			}

			// The same text reaches the same decision: the presence block is not a new
			// round on every tick.
			row, err := s.AgentGet(id)
			if err != nil {
				t.Fatalf("AgentGet: %v", err)
			}
			if !h.gateDoneVerify(*row, h.readAgentScratchpad(*row)) {
				t.Fatal("gateDoneVerify = false on the second tick, want true")
			}
			if got := h.doneGateVerifyStateFor(id); got == nil || got.BlockedRounds != 1 {
				t.Errorf("rounds = %+v, want the presence block counted once", got)
			}
		})
	}
}

// The presence block is bound to the report like every other verdict: a corrected
// report is a different report, and the verdict for it is what releases the agent.
func TestDoneGateReleasesACorrectedReportOnceAPreCheckAcceptsIt(t *testing.T) {
	resetDoneGuardState()
	resetDoneGateVerifyStates()
	h, s := mustHandler(t)
	repo, _, _ := claimsRepo(t)

	const id = "presence-fixed"
	agent := endToEndAgent(t, h, s, id, repo, "Nothing verifiable was written.\n")
	los24dAgentSock(t, h, id)

	if !h.gateDoneVerify(agent, h.readAgentScratchpad(agent)) {
		t.Fatal("gateDoneVerify = false on the unstated report, want true")
	}
	if blocked := h.doneGateVerifyStateFor(id); blocked == nil || blocked.Kind != "defect" {
		t.Fatalf("state = %+v, want the defect block", blocked)
	}

	// The agent rewrites the report and runs the pre-check. The new claim means a
	// new key, and the verdict for it releases it.
	rewritten := "[COMMIT: " + strings.Repeat("0", 40) + "]\n"
	writeReport(t, s, agent, rewritten)
	if !h.gateDoneVerify(agent, h.readAgentScratchpad(agent)) {
		t.Fatal("gateDoneVerify = false for the corrected report, want true")
	}
	if got := h.doneGateVerifyStateFor(id); got == nil || !got.AwaitingVerdict {
		t.Fatalf("state = %+v, want a block awaiting the new verdict", got)
	}

	acceptedVerdict(t, h, agent, rewritten)
	row, err := s.AgentGet(id)
	if err != nil {
		t.Fatalf("AgentGet: %v", err)
	}
	if !h.gateDoneVerify(*row, h.readAgentScratchpad(*row)) {
		t.Fatal("gateDoneVerify = false after the verdict, want true")
	}
	row, _ = s.AgentGet(id)
	if row.Status != "running" {
		t.Errorf("status = %q, want running once the corrected report has a verdict", row.Status)
	}
}

// A refutation is only actionable with the claim, the finding and the command
// that reproduces it. "check your report" is not an instruction an agent can
// follow.
func TestDoneVerifyMessageNamesTheClaimItsFindingAndTheCommand(t *testing.T) {
	const (
		reason  = "commit 0000000000000000000000000000000000000000 does not exist in /repo"
		command = "git -C /repo rev-parse --verify --quiet 0000000000000000000000000000000000000000^{commit}"
	)
	message := blockedMessageFor(&doneGateVerifyState{
		Status: doneVerifyBlocked,
		Kind:   "refuted",
		Reason: "1 claim(s) refuted: " + reason,
		Findings: []verifyClaimFinding{{
			Kind:    "commit_exists",
			Status:  "refuted",
			Reason:  reason,
			Command: command,
		}},
	})
	for _, want := range []string{"commit_exists", "refuted", reason, command} {
		if !strings.Contains(message, want) {
			t.Errorf("message %q does not name %q", message, want)
		}
	}
}

// An environment gap is not the agent's fault, and the message must not ask it
// to rewrite a report that was never judged. The command still follows: without
// the gate's own retry, it is the only lever back to a fresh verdict.
func TestDoneVerifyMessageBlamesTheMachineForAnEnvironmentGap(t *testing.T) {
	const command = "yesmem verify-claims --repo '/repo' --project 'p' --section 's-verify' --profile yesloop-done --policy yesloop-done --strict"
	message := blockedMessageFor(&doneGateVerifyState{
		Status:       doneVerifyBlocked,
		Kind:         "environment",
		Reason:       "bubblewrap is missing",
		Precondition: command,
	})
	if !strings.Contains(message, "bubblewrap is missing") {
		t.Errorf("message %q does not name the cause", message)
	}
	if !strings.Contains(message, "not the report") {
		t.Errorf("message %q does not place the responsibility", message)
	}
	if !strings.Contains(message, command) {
		t.Errorf("message %q does not name the way to a fresh verdict", message)
	}
	for _, absent := range []string{"Fix the report section", "[COMMIT:"} {
		if strings.Contains(message, absent) {
			t.Errorf("message %q carries %q, which asks the agent to act on a report that is not at fault", message, absent)
		}
	}
}

// An exhausted budget is a statement about the run, so the message is the two
// numbers and nothing else.
func TestDoneVerifyMessageNamesBudgetAndConsumedForALimit(t *testing.T) {
	message := blockedMessageFor(&doneGateVerifyState{
		Status:   doneVerifyBlocked,
		Kind:     "limit",
		Reason:   "the budget ran out",
		Bound:    5 * time.Minute,
		Consumed: 2*time.Minute + 30*time.Second,
	})
	for _, want := range []string{"5m0s", "2m30s"} {
		if !strings.Contains(message, want) {
			t.Errorf("message %q does not name %q", message, want)
		}
	}
	for _, absent := range []string{"Fix the report section", "[COMMIT:"} {
		if strings.Contains(message, absent) {
			t.Errorf("message %q carries %q, but a budget verdict says nothing about the report", message, absent)
		}
	}
}

// The status is the one interpolated field that skipped the sanitiser. It comes
// from a closed set today, but the message is the gate's output and must not
// depend on that staying true.
func TestDoneVerifyMessageSanitizesTheFindingStatus(t *testing.T) {
	message := blockedMessageFor(&doneGateVerifyState{
		Status: doneVerifyBlocked,
		Kind:   "refuted",
		Reason: "1 claim(s) refuted: no",
		Findings: []verifyClaimFinding{{
			Kind:   "commit_exists",
			Status: "refuted\x1b[31m\x07",
			Reason: "commit does not exist",
		}},
	})
	if strings.ContainsAny(message, "\x1b\x07") {
		t.Errorf("message %q carries control characters from the status field", message)
	}
	if !strings.Contains(message, "refuted") {
		t.Errorf("message %q lost the status word", message)
	}
}

// An environment gap or a limit can also come from a run that did happen, one
// claim at a time. Those findings carry the reason and the command, so they must
// stay in the message even though the report is not at fault.
func TestDoneVerifyMessageKeepsFindingsForAMachineVerdict(t *testing.T) {
	for _, kind := range []string{"environment", "limit"} {
		message := blockedMessageFor(&doneGateVerifyState{
			Status:   doneVerifyBlocked,
			Kind:     kind,
			Reason:   "1 claim(s) an environment gap: no upstream",
			Bound:    5 * time.Minute,
			Consumed: time.Minute,
			Findings: []verifyClaimFinding{{
				Kind:    "diff_scope",
				Status:  "unverifiable",
				Reason:  "no upstream to derive a base from",
				Command: "git -C /repo rev-parse origin/main",
			}},
		})
		if !strings.Contains(message, "no upstream to derive a base from") {
			t.Errorf("%s: message %q drops the finding's reason", kind, message)
		}
		if !strings.Contains(message, "git -C /repo rev-parse origin/main") {
			t.Errorf("%s: message %q drops the finding's command", kind, message)
		}
		if strings.Contains(message, "Fix the report section") {
			t.Errorf("%s: message %q sends the agent to a report that is not at fault", kind, message)
		}
	}
}

// A run that failed is a verdict about the machine as well, so it must not send
// the agent to a report nobody read.
func TestDoneVerifyMessageDoesNotSendAFailedRunToTheReport(t *testing.T) {
	message := blockedMessageFor(&doneGateVerifyState{
		Status: doneVerifyBlocked,
		Kind:   "error",
		Reason: "cannot create /home/x/.claude/yesmem/tmp/check: permission denied",
	})
	if !strings.Contains(message, "permission denied") {
		t.Errorf("message %q does not name the cause", message)
	}
	for _, absent := range []string{"Fix the report section", "[COMMIT:"} {
		if strings.Contains(message, absent) {
			t.Errorf("message %q carries %q, but a failed run judged no report", message, absent)
		}
	}
}

// The signature decides what the two readers hear. A limit's reason carries the
// consumed time, which varies by seconds, so it stays out; every other class keeps
// its cause, because a changed cause is a changed verdict.
func TestDoneVerifySignatureIgnoresTheLimitNumbersButNotTheCause(t *testing.T) {
	held := &doneGateVerifyState{
		Fingerprint: "report-1",
		Status:      doneVerifyBlocked,
		Kind:        "limit",
		Reason:      "the budget expired after 5m0s",
	}
	held.Notified = verifyVerdictSignature(held)

	retry := &doneGateVerifyState{
		Fingerprint: "report-1",
		Status:      doneVerifyBlocked,
		Kind:        "limit",
		Reason:      "the budget expired after 5m5s",
		Notified:    held.Notified,
	}
	if recordVerdictNotice(retry) {
		t.Error("a retry that only changed the reported time was announced as a new verdict")
	}

	broken := &doneGateVerifyState{
		Fingerprint: "report-1",
		Status:      doneVerifyBlocked,
		Kind:        "error",
		Reason:      "cannot create /home/x/.claude/yesmem/tmp/check: permission denied",
		Notified:    verifyVerdictSignature(&doneGateVerifyState{Fingerprint: "report-1", Status: doneVerifyBlocked, Kind: "error", Reason: "another verification was still running"}),
	}
	if !recordVerdictNotice(broken) {
		t.Error("a failed run with a new cause was not announced, want a message")
	}

	for name, state := range map[string]*doneGateVerifyState{
		"another class":   {Fingerprint: "report-1", Status: doneVerifyBlocked, Kind: "environment", Notified: held.Notified},
		"another finding": {Fingerprint: "report-1", Status: doneVerifyBlocked, Kind: "limit", Findings: []verifyClaimFinding{{Kind: "diff_scope", Status: "unverifiable"}}, Notified: held.Notified},
		"another report":  {Fingerprint: "report-2", Status: doneVerifyBlocked, Kind: "limit", Notified: held.Notified},
		"a verdict":       {Fingerprint: "report-1", Status: doneVerifyAccepted, Notified: held.Notified},
	} {
		if !recordVerdictNotice(state) {
			t.Errorf("%s was not announced, want a message", name)
		}
	}
}

// The digest an unreadable tree has is not residue. A transient git failure must
// not make the block tell the agent that uncommitted work appeared: a reason that
// names a file the verifier never saw sends it after a ghost.
func TestStaleBecauseDoesNotClaimResidueItDidNotSee(t *testing.T) {
	previous := &doneGateVerifyState{Worktree: "digest-of-a-clean-tree", Head: "h1", Branch: "main"}

	unreadable := verifyContext{worktree: "", head: "h1", branch: "main"}
	if got := staleBecause(previous, unreadable); strings.Contains(got, "working tree") {
		t.Errorf("reason = %q, want no claim about residue when the tree could not be read", got)
	}

	changed := verifyContext{worktree: "digest-with-residue", head: "h1", branch: "main"}
	if got := staleBecause(previous, changed); !strings.Contains(got, "working tree") {
		t.Errorf("reason = %q, want it to name the working tree", got)
	}
}
