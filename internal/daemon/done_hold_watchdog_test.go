package daemon

import (
	"strings"
	"testing"
	"time"

	"github.com/carsteneu/yesmem/internal/storage"
)

// Stillheit ist unmoeglich: a withheld DONE must reach the orchestrator, and a
// hold that nobody decides must escalate on its own. Three escapes remain if it
// does not: the agent disappears, the daemon restarts, or the message is
// broadcast instead of sent.

// backdateDONEHold moves the persisted clock of one hold into the past, so a
// test crosses the watchdog window without waiting it out.
func backdateDONEHold(t *testing.T, s *storage.Store, agentID string, since time.Time) {
	t.Helper()
	if _, err := s.DB().Exec(`UPDATE done_verify_holds SET held_since = ? WHERE agent_id = ?`,
		since.Format(time.RFC3339), agentID); err != nil {
		t.Fatalf("backdate the hold of %s: %v", agentID, err)
	}
}

// withheldDONE is the setup every test here shares: an agent whose DONE payload
// was withheld by the delivery check, so the row is paused and the hold exists.
func withheldDONE(t *testing.T, h *Handler, s *storage.Store, id, repo string) storage.Agent {
	t.Helper()
	agent := endToEndAgent(t, h, s, id, repo, "[COMMIT: "+strings.Repeat("0", 40)+"]\n")
	// The refusal is the sender's answer on the call itself; the daemon holds
	// the message and pauses the row.
	hold := sendDONE(t, h, agent, doneDeliveryPayload)
	if hold.Error == "" {
		t.Fatalf("the DONE payload was delivered without a verdict: %+v", hold)
	}
	after, err := s.AgentGet(id)
	if err != nil {
		t.Fatalf("AgentGet: %v", err)
	}
	return *after
}

// A withheld DONE is a state change the orchestrator reads at once. The DONE
// itself stays where it is; what arrives is the class, the fact that the
// message did not reach it, and that the agent is held with the command.
func TestWithheldDONENotifiesTheOrchestratorImmediately(t *testing.T) {
	resetDoneGuardState()
	resetDoneGateVerifyStates()
	h, s := mustHandler(t)
	repo, _, _ := claimsRepo(t)

	agent := withheldDONE(t, h, s, "held-notice", repo)

	messages := orchestratorMessages(t, s, agent.CallerSession)
	if len(messages) != 1 {
		t.Fatalf("the orchestrator received %d messages, want 1: %v", len(messages), messages)
	}
	notice := messages[0]
	for _, want := range []string{"DONE-WITHHELD", "defect", "withheld", "verify-claims"} {
		if !strings.Contains(notice, want) {
			t.Errorf("the notice does not carry %q: %q", want, notice)
		}
	}
}

// The hold is a fact about an attempt, and an attempt outlives a daemon process.
// The in-memory state is re-derived from the report; the clock is not.
func TestWithheldDONEPersistsTheHold(t *testing.T) {
	resetDoneGuardState()
	resetDoneGateVerifyStates()
	h, s := mustHandler(t)
	repo, _, _ := claimsRepo(t)

	agent := withheldDONE(t, h, s, "held-row", repo)

	hold, found, err := s.DONEHold(agent.ID)
	if err != nil {
		t.Fatalf("DONEHold: %v", err)
	}
	if !found {
		t.Fatal("a withheld DONE left no persisted hold: a restart would restart the clock")
	}
	if hold.Fingerprint == "" || hold.Kind != "defect" || hold.HeldSince == "" {
		t.Errorf("hold = %+v, want fingerprint, class and clock", hold)
	}
}

// A hold inside its window is not news: the agent is working on the report.
func TestHoldWatchdogKeepsQuietInsideTheWindow(t *testing.T) {
	resetDoneGuardState()
	resetDoneGateVerifyStates()
	h, s := mustHandler(t)
	repo, _, _ := claimsRepo(t)

	agent := withheldDONE(t, h, s, "held-quiet", repo)
	before := len(orchestratorMessages(t, s, agent.CallerSession))

	h.checkDONEHoldWatchdog()

	if after := len(orchestratorMessages(t, s, agent.CallerSession)); after != before {
		t.Fatalf("the watchdog spoke inside the window: %d -> %d messages", before, after)
	}
}

// The agent that sends a DONE and then disappears is the case the hold alone
// cannot answer: nobody corrects the report, so the daemon has to hand the case
// over instead of waiting for a round that will never come.
func TestHoldWatchdogEscalatesAnUndecidedHold(t *testing.T) {
	resetDoneGuardState()
	resetDoneGateVerifyStates()
	h, s := mustHandler(t)
	repo, _, _ := claimsRepo(t)

	agent := withheldDONE(t, h, s, "held-silent", repo)
	backdateDONEHold(t, s, agent.ID, time.Now().Add(-h.doneVerifyMaxWait()-time.Minute))
	before := len(orchestratorMessages(t, s, agent.CallerSession))

	h.checkDONEHoldWatchdog()

	messages := orchestratorMessages(t, s, agent.CallerSession)
	if len(messages) != before+1 {
		t.Fatalf("the watchdog sent %d messages, want 1: %v", len(messages)-before, messages[before:])
	}
	escalation := messages[len(messages)-1]
	for _, want := range []string{"DONE-UNDECIDED", "defect", "verify-claims", agent.Section} {
		if !strings.Contains(escalation, want) {
			t.Errorf("the escalation does not carry %q: %q", want, escalation)
		}
	}
	hold, found, err := s.DONEHold(agent.ID)
	if err != nil || !found {
		t.Fatalf("DONEHold: found=%v err=%v", found, err)
	}
	if hold.WatchdogAt == "" {
		t.Error("the escalation was not recorded: it would repeat on every tick")
	}

	// One escalation per hold, not one per tick.
	h.checkDONEHoldWatchdog()
	if after := len(orchestratorMessages(t, s, agent.CallerSession)); after != len(messages) {
		t.Errorf("the watchdog repeated the escalation: %d -> %d", len(messages), after)
	}
}

// A restart loses the in-memory state. The clock is what the hold kept, so the
// new process escalates at the deadline instead of starting a fresh one.
func TestHoldSurvivesADaemonRestart(t *testing.T) {
	resetDoneGuardState()
	resetDoneGateVerifyStates()
	h, s := mustHandler(t)
	repo, _, _ := claimsRepo(t)

	agent := withheldDONE(t, h, s, "held-restart", repo)
	backdateDONEHold(t, s, agent.ID, time.Now().Add(-h.doneVerifyMaxWait()-time.Minute))

	// The restart: a new handler, the same store, no in-memory state.
	resetDoneGateVerifyStates()
	restarted := NewHandler(s, nil)
	restarted.dataDir = t.TempDir()
	restarted.disableAgentProcesses = true
	before := len(orchestratorMessages(t, s, agent.CallerSession))

	restarted.checkDONEHoldWatchdog()

	messages := orchestratorMessages(t, s, agent.CallerSession)
	if len(messages) != before+1 {
		t.Fatalf("after a restart the hold went silent: %d -> %d messages", before, len(messages))
	}
	if !strings.Contains(messages[len(messages)-1], "DONE-UNDECIDED") {
		t.Errorf("the restart escalation is not the hold's: %q", messages[len(messages)-1])
	}
}

// A released report is not held any more. Leaving the row behind would make the
// watchdog escalate a case that already has its verdict.
func TestReleaseDropsThePersistedHold(t *testing.T) {
	resetDoneGuardState()
	resetDoneGateVerifyStates()
	h, s := mustHandler(t)
	repo, _, _ := claimsRepo(t)
	report := "[COMMIT: " + strings.Repeat("0", 40) + "]\n"

	agent := endToEndAgent(t, h, s, "held-released", repo, report)
	startFakeInjectSocket(t, los24dAgentSock(t, h, "held-released")+".inject")
	_ = sendDONE(t, h, agent, doneDeliveryPayload)
	if _, found, _ := s.DONEHold(agent.ID); !found {
		t.Fatal("setup: the withheld DONE left no hold")
	}

	acceptedVerdict(t, h, agent, report)
	if !h.gateDoneVerify(agent, h.readAgentScratchpad(agent)) {
		t.Fatal("gateDoneVerify = false, want true: the claim is in scope")
	}
	if _, found, err := s.DONEHold(agent.ID); err != nil || found {
		t.Errorf("the released agent still carries a hold: found=%v err=%v", found, err)
	}
}

// The status line is not the hold. Another guard may overwrite it (DONE-GUARD
// pauses a row whose blocks do not validate), and the hold must survive that:
// dropping it there deletes the only clock the case has, and the case is then
// silent forever.
func TestHoldSurvivesAnotherGuardsPause(t *testing.T) {
	resetDoneGuardState()
	resetDoneGateVerifyStates()
	h, s := mustHandler(t)
	repo, _, _ := claimsRepo(t)

	agent := withheldDONE(t, h, s, "held-reguarded", repo)
	backdateDONEHold(t, s, agent.ID, time.Now().Add(-h.doneHoldWindow()-time.Minute))

	// The DONE-guard takes the row over and pauses it with its own prefix.
	h.pauseAgent(agent.ID, "DONE-GUARD: phase validation failed — Phase4:RED proof")

	h.checkDONEHoldWatchdog()

	if _, found, err := s.DONEHold(agent.ID); err != nil || !found {
		t.Fatalf("the hold was dropped by another guard's pause: found=%v err=%v", found, err)
	}
	if got := orchestratorMessages(t, s, agent.CallerSession); len(got) == 0 {
		t.Fatal("the withheld DONE went silent after another guard paused the row")
	}
}

// A case that already left the loop (three blocked rounds, escalated) is not
// announced a second time by the watchdog, and its escalation marker stays
// readable: that line is the only witness a restart has.
func TestHoldWatchdogSkipsAnEscalatedCase(t *testing.T) {
	resetDoneGuardState()
	resetDoneGateVerifyStates()
	h, s := mustHandler(t)
	repo, _, _ := claimsRepo(t)

	agent := withheldDONE(t, h, s, "held-escalated", repo)
	h.escalateBlockedReport(agent, h.doneGateVerifyStateFor(agent.ID))
	after, err := s.AgentGet(agent.ID)
	if err != nil {
		t.Fatalf("AgentGet: %v", err)
	}
	before := len(orchestratorMessages(t, s, agent.CallerSession))
	backdateDONEHold(t, s, agent.ID, time.Now().Add(-h.doneHoldWindow()-time.Minute))

	h.checkDONEHoldWatchdog()

	if got := len(orchestratorMessages(t, s, agent.CallerSession)); got != before {
		t.Errorf("the watchdog repeated an escalated case: %d -> %d messages", before, got)
	}
	still, err := s.AgentGet(agent.ID)
	if err != nil {
		t.Fatalf("AgentGet: %v", err)
	}
	if !escalatedHold(*still) {
		t.Errorf("the escalation marker is gone after the watchdog ran: %q", still.Progress)
	}
	_ = after
}

// A watchdog that cannot reach an orchestrator must not mark the case as told:
// the mark is the promise not to repeat, and an undelivered message with a mark
// is silence with a receipt.
func TestHoldWatchdogLeavesTheMarkWhenTheOrchestratorIsUnreachable(t *testing.T) {
	resetDoneGuardState()
	resetDoneGateVerifyStates()
	h, s := mustHandler(t)
	repo, _, _ := claimsRepo(t)

	agent := withheldDONE(t, h, s, "held-no-caller", repo)
	// A row without a caller address has nowhere to send to. The tick path
	// accepts such a row; only the delivery path requires the address.
	if _, err := s.DB().Exec(`UPDATE agents SET caller_session = '' WHERE id = ?`, agent.ID); err != nil {
		t.Fatalf("clear the caller session: %v", err)
	}
	noCaller, err := s.AgentGet(agent.ID)
	if err != nil {
		t.Fatalf("AgentGet: %v", err)
	}
	backdateDONEHold(t, s, agent.ID, time.Now().Add(-h.doneHoldWindow()-time.Minute))

	h.checkDONEHoldWatchdog()

	hold, found, err := s.DONEHold(agent.ID)
	if err != nil || !found {
		t.Fatalf("DONEHold: found=%v err=%v", found, err)
	}
	if hold.WatchdogAt != "" {
		t.Errorf("the escalation was marked though nobody could receive it (%q)", hold.WatchdogAt)
	}
	_ = noCaller
}
