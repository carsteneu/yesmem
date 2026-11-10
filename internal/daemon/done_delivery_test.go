package daemon

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/carsteneu/yesmem/internal/storage"
	"github.com/carsteneu/yesmem/internal/verify"
	"github.com/carsteneu/yesmem/internal/verify/engine"
)

// The delivery side of the DONE contract: a send_to that announces DONE reaches
// the orchestrator only when the verdict registry already holds a fresh accepted
// judgement for exactly this report. Everything else is withheld and goes back to
// the sender.

// doneDeliveryPayload is the marker line the skill prescribes for Phase 6.
const doneDeliveryPayload = "[DONE] [DEPLOY: no] [BRANCH: main] [MERGE: no] the section is complete"

// sendDONE is an agent's Phase 6 call: the proven identity is the agent's own
// session, the target is its orchestrator.
func sendDONE(t *testing.T, h *Handler, agent storage.Agent, content string) Response {
	t.Helper()
	return h.Handle(Request{Method: "send_to", Params: map[string]any{
		"target":      agent.CallerSession,
		"content":     content,
		"_session_id": agent.SessionID,
	}})
}

// sendDONETo is the same call addressed somewhere else, so a test can show that
// the hold follows the orchestrator and not the payload alone.
func sendDONETo(t *testing.T, h *Handler, agent storage.Agent, target, content string) Response {
	t.Helper()
	return h.Handle(Request{Method: "send_to", Params: map[string]any{
		"target":      target,
		"content":     content,
		"_session_id": agent.SessionID,
	}})
}

// deliveredDONEs are the messages at one address that actually announce DONE.
// The check is the reader's own: the leading marker line, not the predicate the
// production code uses.
func deliveredDONEs(t *testing.T, s *storage.Store, address string) []string {
	t.Helper()
	var found []string
	for _, content := range deliveredMessages(t, s, address) {
		line := strings.TrimSpace(content)
		if strings.HasPrefix(line, "[DONE]") || strings.HasPrefix(line, "DONE:") {
			found = append(found, content)
		}
	}
	return found
}

func deliveredMessages(t *testing.T, s *storage.Store, address string) []string {
	t.Helper()
	msgs, err := s.GetChannelMessages([]string{address})
	if err != nil {
		t.Fatalf("GetChannelMessages(%s): %v", address, err)
	}
	contents := make([]string, 0, len(msgs))
	for _, m := range msgs {
		contents = append(contents, m.Content)
	}
	return contents
}

// A fresh accepted verdict is the whole point of the reversal: the message leaves
// the daemon in the same call, carries the marker that it is verdict-backed, and
// costs nothing but a lookup. The single verification slot is taken for the whole
// call, so a run at delivery time would be refused instead of hidden.
func TestDONEDeliveryFreshVerdictShipsWithTheVerdictLine(t *testing.T) {
	resetDoneGateVerifyStates()
	t.Cleanup(resetDoneGateVerifyStates)

	h, s := mustHandler(t)
	repo, head, _ := claimsRepo(t)
	report := "[COMMIT: " + head + "]\n[BRANCH: main]\n"
	agent := endToEndAgent(t, h, s, "deliver-ok", repo, report)
	acceptedVerdict(t, h, agent, report)

	h.verifySlot <- struct{}{}
	response := sendDONE(t, h, agent, doneDeliveryPayload)
	<-h.verifySlot
	if response.Error != "" {
		t.Fatalf("a verified DONE was refused: %s", response.Error)
	}

	delivered := deliveredDONEs(t, s, agent.CallerSession)
	if len(delivered) != 1 {
		t.Fatalf("delivered DONEs = %d, want exactly 1: %v", len(delivered), delivered)
	}
	if !strings.HasPrefix(delivered[0], "[DONE]") {
		t.Errorf("delivered payload = %q, want the sender's own text at the front", delivered[0])
	}
	if !strings.Contains(delivered[0], "verdict: accepted (pre-check)") {
		t.Errorf("delivered payload = %q, want it marked as verdict-backed", delivered[0])
	}

	after, err := s.AgentGet(agent.ID)
	if err != nil {
		t.Fatalf("AgentGet: %v", err)
	}
	if after.Status != "running" {
		t.Errorf("agent status = %q, want running: a released DONE does not pause anyone", after.Status)
	}
	// The row carries the released verdict, so the 30s tick that follows finds the
	// decision already taken and does not announce a second DONE-VERIFIED.
	state := h.doneGateVerifyStateFor(agent.ID)
	if state == nil || state.Status != doneVerifyAccepted || !state.Reused {
		t.Fatalf("gate state = %+v, want the reused accepted verdict", state)
	}
	if state.Notified == "" {
		t.Error("the released verdict is not marked as announced: the guard tick will report it again")
	}
}

// No verdict means no delivery. The sender is told why and given the one command
// that produces a verdict; the orchestrator gets the state change, never the DONE.
func TestDONEDeliveryWithoutVerdictWithholdsTheMessage(t *testing.T) {
	resetDoneGateVerifyStates()
	t.Cleanup(resetDoneGateVerifyStates)

	h, s := mustHandler(t)
	repo, head, _ := claimsRepo(t)
	report := "[COMMIT: " + head + "]\n[BRANCH: main]\n"
	agent := endToEndAgent(t, h, s, "deliver-unchecked", repo, report)

	response := sendDONE(t, h, agent, doneDeliveryPayload)
	if response.Error == "" {
		t.Fatal("an unchecked DONE was delivered: the orchestrator would believe a verification that never ran")
	}
	if got := deliveredDONEs(t, s, agent.CallerSession); len(got) != 0 {
		t.Fatalf("delivered DONEs = %d, want 0: %v", len(got), got)
	}

	// The sender holds the defect and the command, not a bare refusal.
	for _, want := range []string{"DONE-VERIFY-BLOCKED", "not been verified", "yesmem verify-claims --repo", reportSectionArg(agent)} {
		if !strings.Contains(response.Error, want) {
			t.Errorf("sender message %q does not carry %q", response.Error, want)
		}
	}

	after, err := s.AgentGet(agent.ID)
	if err != nil {
		t.Fatalf("AgentGet: %v", err)
	}
	if after.Status != "paused" {
		t.Errorf("agent status = %q, want paused: the attempt is held", after.Status)
	}
	if !strings.HasPrefix(after.Progress, "paused: DONE-VERIFY-BLOCKED (defect)") {
		t.Errorf("progress = %q, want the defect hold", after.Progress)
	}

	state := h.doneGateVerifyStateFor(agent.ID)
	if state == nil || state.Status != doneVerifyBlocked || state.Kind != "defect" {
		t.Fatalf("gate state = %+v, want a blocked defect", state)
	}
	if state.BlockedRounds != 1 {
		t.Errorf("blocked rounds = %d, want 1: a withheld DONE is one rejected attempt", state.BlockedRounds)
	}
	if state.Precondition == "" {
		t.Error("the stored state carries no command: the agent cannot produce the verdict the gate reads")
	}

	// The orchestrator sees the state change and nothing else.
	notices := orchestratorMessages(t, s, agent.CallerSession)
	if len(notices) != 1 {
		t.Fatalf("orchestrator notices = %d, want 1: %v", len(notices), notices)
	}
	if !strings.Contains(notices[0], "withheld") || !strings.Contains(notices[0], "defect") {
		t.Errorf("orchestrator notice %q does not name the withhold and the class", notices[0])
	}
	if strings.Contains(notices[0], "[DEPLOY:") || strings.Contains(notices[0], "[MERGE:") {
		t.Errorf("orchestrator notice %q carries the DONE payload", notices[0])
	}
}

// reportSectionArg is the report-section argument the printed command must
// carry, so the reader sees that the command names its own section.
func reportSectionArg(agent storage.Agent) string {
	return "'" + verifyReportSection(agent.Section) + "'"
}

// A verdict that no longer matches the tree is not a verdict for this DONE. The
// block names what moved instead of claiming that nothing was ever checked.
func TestDONEDeliveryWithholdsAStaleVerdictAndNamesWhatMoved(t *testing.T) {
	resetDoneGateVerifyStates()
	t.Cleanup(resetDoneGateVerifyStates)

	h, s := mustHandler(t)
	repo, _, _ := claimsRepo(t)
	report := "[COMMIT: 1f58c28]\n[BRANCH: main]\n"
	agent := endToEndAgent(t, h, s, "deliver-stale", repo, report)
	// A judgement the way a run reaches it: with the facts it was reached under,
	// so the stale block can compare them.
	recordVerdict(t, h, agent, report, &doneGateVerifyState{
		Status:        doneVerifyAccepted,
		ReportDigest:  digestText(report),
		ReportSession: agentReportSession(agent),
		Repo:          repo,
		Head:          verifyHead(repo),
		Branch:        "main",
	})

	// The checkout moves to another branch after the verdict: the same report text
	// about another question.
	moved := exec.Command("git", "-C", repo, "checkout", "-q", "-b", "topic")
	moved.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "HOME="+t.TempDir())
	if out, err := moved.CombinedOutput(); err != nil {
		t.Fatalf("git checkout: %v: %s", err, out)
	}

	response := sendDONE(t, h, agent, doneDeliveryPayload)
	if response.Error == "" {
		t.Fatal("a DONE with a stale verdict was delivered")
	}
	if !strings.Contains(response.Error, "the branch changed from main to topic") {
		t.Errorf("sender message %q does not name what moved", response.Error)
	}
	if got := deliveredDONEs(t, s, agent.CallerSession); len(got) != 0 {
		t.Fatalf("delivered DONEs = %d, want 0: %v", len(got), got)
	}
}

// A judgement under a weaker rule set judged fewer classes, so its acceptance
// proves less than what the DONE gate promises.
func TestDONEDeliveryWithholdsAVerdictOfAWeakerRuleSet(t *testing.T) {
	resetDoneGateVerifyStates()
	t.Cleanup(resetDoneGateVerifyStates)

	h, s := mustHandler(t)
	repo, head, _ := claimsRepo(t)
	report := "[COMMIT: " + head + "]\n[BRANCH: main]\n"
	agent := endToEndAgent(t, h, s, "deliver-weaker", repo, report)
	recordVerdict(t, h, agent, report, &doneGateVerifyState{
		Status: doneVerifyAccepted,
		Mode:   verifyVerdictMode(verify.ProfileYesloopDone, ""),
	})

	response := sendDONE(t, h, agent, doneDeliveryPayload)
	if response.Error == "" {
		t.Fatal("a DONE released by a weaker rule set was delivered")
	}
	if !strings.Contains(response.Error, "the DONE gate needs") {
		t.Errorf("sender message %q does not name the rule set it needs", response.Error)
	}
	if got := deliveredDONEs(t, s, agent.CallerSession); len(got) != 0 {
		t.Fatalf("delivered DONEs = %d, want 0: %v", len(got), got)
	}
}

// send_to is infrastructure. A message without a DONE payload must pass untouched
// and unchecked: same bytes, no hold, no pause, and no gate state that a later
// tick could act on.
func TestDONEDeliveryLeavesMessagesWithoutDONEPayloadAlone(t *testing.T) {
	resetDoneGateVerifyStates()
	t.Cleanup(resetDoneGateVerifyStates)

	h, s := mustHandler(t)
	repo, head, _ := claimsRepo(t)
	agent := endToEndAgent(t, h, s, "deliver-relay", repo, "[COMMIT: "+head+"]\n[BRANCH: main]\n")

	const relay = "Please check my Phase 3 notes, the second milestone is unclear."
	response := sendDONE(t, h, agent, relay)
	if response.Error != "" {
		t.Fatalf("a plain relay was refused: %s", response.Error)
	}
	delivered := deliveredMessages(t, s, agent.CallerSession)
	if len(delivered) != 1 || !strings.Contains(delivered[0], relay) {
		t.Fatalf("delivered = %v, want the relay text unchanged", delivered)
	}
	if strings.Contains(delivered[0], "verdict:") {
		t.Errorf("delivered relay = %q, want no verdict annotation on a non-DONE", delivered[0])
	}
	if state := h.doneGateVerifyStateFor(agent.ID); state != nil {
		t.Errorf("gate state = %+v, want none: a message without a DONE payload is not a DONE claim", state)
	}
	after, err := s.AgentGet(agent.ID)
	if err != nil {
		t.Fatalf("AgentGet: %v", err)
	}
	if after.Status != "running" {
		t.Errorf("agent status = %q, want running: a relay does not hold anyone", after.Status)
	}
}

// Generic spawned agents announce their DONE in the same shape but have no report
// section and no verdict to look up. Holding them would deflect a working
// handover into a check that does not apply to them.
func TestDONEDeliveryLeavesANonLoopAgentAlone(t *testing.T) {
	resetDoneGateVerifyStates()
	t.Cleanup(resetDoneGateVerifyStates)

	h, s := mustHandler(t)
	repo, _, _ := claimsRepo(t)
	// A generic spawned agent: same handover sentence, a section the loop never
	// writes, and therefore no report section either.
	if err := s.AgentCreate(storage.Agent{
		ID: "deliver-plain", Project: "testproj", Section: "task-1",
		SessionID: "sess-deliver-plain", PID: testPID, Status: "running", Backend: "opencode",
		SockPath: "/nonexistent/deliver-plain.sock", CallerSession: "caller-deliver-plain",
		WorkDir: repo,
	}); err != nil {
		t.Fatalf("AgentCreate: %v", err)
	}
	plain, err := s.AgentGet("deliver-plain")
	if err != nil {
		t.Fatalf("AgentGet: %v", err)
	}

	response := sendDONETo(t, h, *plain, plain.CallerSession, "DONE: Section 'task-1' in project 'testproj' is complete.")
	if response.Error != "" {
		t.Fatalf("a non-loop DONE was refused: %s", response.Error)
	}
	if got := deliveredDONEs(t, s, plain.CallerSession); len(got) != 1 {
		t.Fatalf("delivered DONEs = %d, want 1: the hold is the loop's contract, not everyone's", len(got))
	}
	if state := h.doneGateVerifyStateFor(plain.ID); state != nil {
		t.Errorf("gate state = %+v, want none for a section outside the loop", state)
	}
}

// The orchestrator is the reader the reversal is about. A DONE addressed to a
// peer is not the delivery this holds.
func TestDONEDeliveryOnlyHoldsTheDONEAddressedToTheOrchestrator(t *testing.T) {
	resetDoneGateVerifyStates()
	t.Cleanup(resetDoneGateVerifyStates)

	h, s := mustHandler(t)
	repo, head, _ := claimsRepo(t)
	agent := endToEndAgent(t, h, s, "deliver-peer", repo, "[COMMIT: "+head+"]\n[BRANCH: main]\n")

	response := sendDONETo(t, h, agent, "peer-agent", doneDeliveryPayload)
	if response.Error != "" {
		t.Fatalf("a DONE to a peer was refused: %s", response.Error)
	}
	if got := deliveredDONEs(t, s, "peer-agent"); len(got) != 1 {
		t.Fatalf("delivered DONEs to the peer = %d, want 1", len(got))
	}
	if state := h.doneGateVerifyStateFor(agent.ID); state != nil {
		t.Errorf("gate state = %+v, want none: the orchestrator's delivery was not attempted", state)
	}
}

// The third withheld attempt of one attempt escalates, like the gate's third round:
// a correction is a new report but the same attempt, so the count survives the
// rewrite and the case leaves the loop after three failed corrections.
func TestDONEDeliveryEscalatesAfterThreeWithheldAttempts(t *testing.T) {
	resetDoneGateVerifyStates()
	t.Cleanup(resetDoneGateVerifyStates)

	h, s := mustHandler(t)
	repo, head, _ := claimsRepo(t)
	agent := endToEndAgent(t, h, s, "deliver-escalate", repo, "[COMMIT: "+head+"]\n[BRANCH: main]\n")

	for round := 1; round <= doneVerifyEscalateAfter; round++ {
		// The agent corrects its report and sends DONE again: another text, the same
		// missing pre-check.
		writeReport(t, s, agent, fmt.Sprintf("[COMMIT: %s]\n[BRANCH: main]\nFiles in scope: round %d\n", head, round))
		if response := sendDONE(t, h, agent, doneDeliveryPayload); response.Error == "" {
			t.Fatalf("round %d: an unchecked DONE was delivered", round)
		}
	}
	if got := deliveredDONEs(t, s, agent.CallerSession); len(got) != 0 {
		t.Fatalf("delivered DONEs = %d, want 0: %v", len(got), got)
	}

	after, err := s.AgentGet(agent.ID)
	if err != nil {
		t.Fatalf("AgentGet: %v", err)
	}
	if !escalatedHold(*after) {
		t.Fatalf("progress = %q, want the escalation marker", after.Progress)
	}
	state := h.doneGateVerifyStateFor(agent.ID)
	if state == nil || !state.Escalated {
		t.Fatalf("gate state = %+v, want the escalated state", state)
	}
	if state.BlockedRounds != doneVerifyEscalateAfter {
		t.Errorf("blocked rounds = %d, want %d", state.BlockedRounds, doneVerifyEscalateAfter)
	}

	// The orchestrator is told once, with the rounds, and never receives the DONE.
	var escalated []string
	for _, notice := range orchestratorMessages(t, s, agent.CallerSession) {
		if strings.Contains(notice, "ESCALAT") {
			escalated = append(escalated, notice)
		}
	}
	if len(escalated) != 1 {
		t.Fatalf("escalation notices = %d, want 1: %v", len(escalated), escalated)
	}

	// A fourth send is not another round: the case already left the loop.
	escalatedRounds := state.BlockedRounds
	if response := sendDONE(t, h, agent, doneDeliveryPayload); response.Error == "" {
		t.Fatal("an escalated DONE was delivered")
	}
	if again := h.doneGateVerifyStateFor(agent.ID); again.BlockedRounds != escalatedRounds {
		t.Errorf("rounds = %d after the escalation, want %d", again.BlockedRounds, escalatedRounds)
	}
}

// The round belongs to the decision, not to the message. The gate and the delivery
// path write the same counter, so a refusal that is only repeated must not add a
// round: otherwise one failed correction escalated the case, and the orchestrator
// heard the same state change twice.
func TestDONEDeliveryCountsOneRoundPerDecision(t *testing.T) {
	resetDoneGateVerifyStates()
	t.Cleanup(resetDoneGateVerifyStates)

	h, s := mustHandler(t)
	repo, head, _ := claimsRepo(t)
	agent := endToEndAgent(t, h, s, "deliver-once", repo, "[COMMIT: "+head+"]\n[BRANCH: main]\n")

	for attempt := 1; attempt <= doneVerifyEscalateAfter; attempt++ {
		response := sendDONE(t, h, agent, doneDeliveryPayload)
		if response.Error == "" {
			t.Fatalf("attempt %d: an unchecked DONE was delivered", attempt)
		}
		// Nothing was decided differently, so the sender is answered with the same
		// message and nothing new is counted.
		if !strings.Contains(response.Error, "not been verified") {
			t.Errorf("attempt %d: sender message %q lost the reason", attempt, response.Error)
		}
	}

	state := h.doneGateVerifyStateFor(agent.ID)
	if state == nil || state.BlockedRounds != 1 {
		t.Fatalf("state = %+v, want one round for one unchanged refusal", state)
	}
	if state.Escalated {
		t.Error("three identical sends escalated a report whose decision never changed")
	}
	if notices := orchestratorMessages(t, s, agent.CallerSession); len(notices) != 1 {
		t.Errorf("orchestrator notices = %d, want 1: the same refusal is one notice: %v", len(notices), notices)
	}
}

// The whole path, driven the way a loop agent drives it: a real pre-check decides
// the report, then the DONE leaves the daemon with the verdict line and nothing
// runs at delivery time.
func TestDONEDeliveryEndToEnd(t *testing.T) {
	if engine.FindBwrap() == "" {
		t.Skip("bubblewrap is missing: the test-run claim of an honest report cannot run on this host")
	}
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 is missing: the fixture's unittest cannot run")
	}
	resetDoneGateVerifyStates()
	t.Cleanup(resetDoneGateVerifyStates)

	h, s := mustHandler(t)
	repo, head, _ := claimsRepo(t)
	report := "[COMMIT: " + head + "]\n[BRANCH: main]\nTests run: python3 -m unittest test_ok.py -> exit 0\n"
	agent := endToEndAgent(t, h, s, "deliver-e2e", repo, report)

	result := preCheck(t, h, agent, repo, report)
	if result.Status != "accepted" {
		t.Fatalf("pre-check = %s (%s): %s", result.Status, result.Kind, result.Reason)
	}

	h.verifySlot <- struct{}{}
	response := sendDONE(t, h, agent, doneDeliveryPayload)
	<-h.verifySlot
	if response.Error != "" {
		t.Fatalf("the verified DONE was refused: %s", response.Error)
	}
	delivered := deliveredDONEs(t, s, agent.CallerSession)
	if len(delivered) != 1 {
		t.Fatalf("delivered DONEs = %d, want 1: %v", len(delivered), delivered)
	}
	if !strings.Contains(delivered[0], "verdict: accepted (pre-check)") {
		t.Errorf("delivered payload = %q, want the verdict line", delivered[0])
	}
}

// The sender label is a free parameter, not a credential. Claiming a daemon
// address must not switch the delivery check off: the daemon's own notices prove no
// session and are excluded by that, not by their label.
func TestDONEDeliveryCannotBeSwitchedOffByTheSenderLabel(t *testing.T) {
	resetDoneGateVerifyStates()
	t.Cleanup(resetDoneGateVerifyStates)

	h, s := mustHandler(t)
	repo, head, _ := claimsRepo(t)
	agent := endToEndAgent(t, h, s, "deliver-spoof", repo, "[COMMIT: "+head+"]\n[BRANCH: main]\n")

	response := h.Handle(Request{Method: "send_to", Params: map[string]any{
		"target":      agent.CallerSession,
		"content":     doneDeliveryPayload,
		"sender":      daemonSender("yesloop"),
		"_session_id": agent.SessionID,
	}})
	if response.Error == "" {
		t.Fatal("a claimed daemon sender switched the hold off")
	}
	if got := deliveredDONEs(t, s, agent.CallerSession); len(got) != 0 {
		t.Fatalf("delivered DONEs = %d, want 0: %v", len(got), got)
	}
}

// A verdict that refused the report is not the same answer as "nothing was checked":
// the sender has to read what the run found, and the class and the numbers of a
// limit belong to that answer.
func TestDONEDeliveryReportsARefusedVerdictAsRefused(t *testing.T) {
	cases := []struct {
		name    string
		verdict *doneGateVerifyState
		want    []string
		notWant string
	}{
		{
			name: "a refuted report",
			verdict: &doneGateVerifyState{
				Status: doneVerifyBlocked,
				Kind:   "refuted",
				Reason: "tests_exit: the suite failed",
				Findings: []verifyClaimFinding{{
					Kind: "tests_exit", Status: "refuted", Class: "report",
					Command: "python3 -m unittest test_ok.py",
				}},
			},
			want:    []string{"did not survive verification", "tests_exit", "python3 -m unittest test_ok.py"},
			notWant: "has not been verified",
		},
		{
			name: "an exhausted budget",
			verdict: &doneGateVerifyState{
				Status:   doneVerifyBlocked,
				Kind:     "limit",
				Reason:   "the run did not start",
				Bound:    90 * time.Second,
				Consumed: 2 * time.Minute,
			},
			want:    []string{"limit, not the report", "limit 1m30s", "consumed 2m0s"},
			notWant: "has not been verified",
		},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			resetDoneGateVerifyStates()
			t.Cleanup(resetDoneGateVerifyStates)

			h, s := mustHandler(t)
			repo, head, _ := claimsRepo(t)
			report := "[COMMIT: " + head + "]\n[BRANCH: main]\n"
			agent := endToEndAgent(t, h, s, "deliver-refused", repo, report)
			recordVerdict(t, h, agent, report, test.verdict)

			response := sendDONE(t, h, agent, doneDeliveryPayload)
			if response.Error == "" {
				t.Fatal("the refused report's DONE was delivered")
			}
			for _, want := range test.want {
				if !strings.Contains(response.Error, want) {
					t.Errorf("sender message %q does not carry %q", response.Error, want)
				}
			}
			if strings.Contains(response.Error, test.notWant) {
				t.Errorf("sender message %q claims %q about a report that was verified", response.Error, test.notWant)
			}
			state := h.doneGateVerifyStateFor(agent.ID)
			if state == nil || state.AwaitingVerdict {
				t.Fatalf("state = %+v, want a refusal that carries a verdict", state)
			}
			if state.Kind != test.verdict.Kind {
				t.Errorf("class = %q, want the verdict's %q", state.Kind, test.verdict.Kind)
			}
		})
	}
}

// A pre-check that is still running is not a rejected attempt. Counting it would
// let three impatient sends escalate a report nobody has judged yet, and the
// escalation would then lock out the verdict that is on its way. The hold is also
// only a deferral: when the verdict lands, the release asks for the payload again.
func TestDONEDeliveryDoesNotCountARunningPreCheck(t *testing.T) {
	resetDoneGateVerifyStates()
	t.Cleanup(resetDoneGateVerifyStates)

	h, s := mustHandler(t)
	repo, head, _ := claimsRepo(t)
	const id = "deliver-inflight"
	report := "[COMMIT: " + head + "]\n[BRANCH: main]\n"
	agent := endToEndAgent(t, h, s, id, repo, report)
	rec := startFakeInjectSocket(t, los24dAgentSock(t, h, id)+".inject")
	key := preCheckKey(t, h, agent, report)
	beginVerifyRun(key)

	response := sendDONE(t, h, agent, doneDeliveryPayload)
	if response.Error == "" {
		t.Fatal("a DONE was delivered while its pre-check was running")
	}
	if !strings.Contains(response.Error, "DONE-PENDING-VERIFY") {
		t.Errorf("sender message %q does not tell the agent to wait", response.Error)
	}
	state := h.doneGateVerifyStateFor(id)
	if state == nil || state.Status != doneVerifyPending {
		t.Fatalf("state = %+v, want the pending hold", state)
	}
	if state.BlockedRounds != 0 {
		t.Errorf("blocked rounds = %d, want 0: a running pre-check is not a rejection", state.BlockedRounds)
	}
	if got := deliveredDONEs(t, s, agent.CallerSession); len(got) != 0 {
		t.Fatalf("delivered DONEs = %d, want 0: %v", len(got), got)
	}

	// The run returns accepted, and the tick lands before the agent re-sends.
	endVerifyRun(key)
	acceptedVerdict(t, h, agent, report)
	row, err := s.AgentGet(id)
	if err != nil {
		t.Fatalf("AgentGet: %v", err)
	}
	if !h.gateDoneVerify(*row, h.readAgentScratchpad(*row)) {
		t.Fatal("gateDoneVerify = false, want true")
	}
	writes := contentWrites(rec)
	if len(writes) == 0 {
		t.Fatal("the agent was written nothing after the release")
	}
	if last := writes[len(writes)-1]; !strings.Contains(last, "aber die Zustellung") {
		t.Errorf("release message %q does not ask for the payload the hold deferred", last)
	}
}

// The delivery decided the verdict, so the guard tick that follows must not decide
// it again: the orchestrator would otherwise hear the same release twice, and a
// second round would count against an agent that did nothing wrong.
func TestDONEGateStaysSilentAfterADeliveredDONE(t *testing.T) {
	resetDoneGateVerifyStates()
	t.Cleanup(resetDoneGateVerifyStates)

	h, s := mustHandler(t)
	repo, head, _ := claimsRepo(t)
	report := "[COMMIT: " + head + "]\n[BRANCH: main]\n"
	agent := endToEndAgent(t, h, s, "deliver-quiet", repo, report)
	acceptedVerdict(t, h, agent, report)

	if response := sendDONE(t, h, agent, doneDeliveryPayload); response.Error != "" {
		t.Fatalf("the verified DONE was refused: %s", response.Error)
	}
	before := orchestratorMessages(t, s, agent.CallerSession)

	row, err := s.AgentGet(agent.ID)
	if err != nil {
		t.Fatalf("AgentGet: %v", err)
	}
	if !h.gateDoneVerify(*row, h.readAgentScratchpad(*row)) {
		t.Fatal("gateDoneVerify = false, want true: the report is a compliant DONE claim")
	}
	after := orchestratorMessages(t, s, agent.CallerSession)
	if len(after) != len(before) {
		t.Fatalf("orchestrator notices = %d, want %d: the tick announced the same release again: %v", len(after), len(before), after)
	}
	if state := h.doneGateVerifyStateFor(agent.ID); state == nil || state.BlockedRounds != 0 {
		t.Fatalf("state = %+v, want the released state without rounds", state)
	}
	row, err = s.AgentGet(agent.ID)
	if err != nil {
		t.Fatalf("AgentGet: %v", err)
	}
	if row.Status != "running" {
		t.Errorf("status = %q, want running: the delivered DONE releases the row", row.Status)
	}
}

// A withheld DONE is only deferred, not cancelled: when the verdict arrives before
// the agent re-sends, the release has to ask for the payload instead of declaring
// the agent finished. Otherwise the handoff is verified and then silently lost.
func TestDONEDeliveryAsksForThePayloadAgainAfterAWithheldSend(t *testing.T) {
	resetDoneGateVerifyStates()
	t.Cleanup(resetDoneGateVerifyStates)

	h, s := mustHandler(t)
	repo, head, _ := claimsRepo(t)
	const id = "deliver-resend"
	report := "[COMMIT: " + head + "]\n[BRANCH: main]\n"
	agent := endToEndAgent(t, h, s, id, repo, report)
	rec := startFakeInjectSocket(t, los24dAgentSock(t, h, id)+".inject")

	if response := sendDONE(t, h, agent, doneDeliveryPayload); response.Error == "" {
		t.Fatal("an unchecked DONE was delivered")
	}
	// The pre-check lands, and the guard tick reaches the agent before it re-sends.
	acceptedVerdict(t, h, agent, report)
	row, err := s.AgentGet(id)
	if err != nil {
		t.Fatalf("AgentGet: %v", err)
	}
	if !h.gateDoneVerify(*row, h.readAgentScratchpad(*row)) {
		t.Fatal("gateDoneVerify = false, want true")
	}

	writes := contentWrites(rec)
	if len(writes) == 0 {
		t.Fatal("the agent was written nothing: it never learns that the payload is missing")
	}
	// The recorder keeps the head of each write, and the missing delivery is the
	// first thing the resend prompt says.
	last := writes[len(writes)-1]
	if !strings.Contains(last, "aber die Zustellung") {
		t.Errorf("release message %q does not name the missing DONE delivery", last)
	}
	if state := h.doneGateVerifyStateFor(id); state == nil || !state.PayloadWithheld {
		t.Errorf("state = %v, want the withheld payload recorded on the release", state)
	}

	notices := orchestratorMessages(t, s, agent.CallerSession)
	lastNotice := notices[len(notices)-1]
	if !strings.Contains(lastNotice, "withheld") {
		t.Errorf("orchestrator notice %q does not say the payload was withheld", lastNotice)
	}
	if got := deliveredDONEs(t, s, agent.CallerSession); len(got) != 0 {
		t.Fatalf("delivered DONEs = %d, want 0: the release does not deliver the payload", len(got))
	}
}
