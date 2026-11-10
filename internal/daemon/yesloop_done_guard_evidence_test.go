package daemon

import (
	"testing"
	"time"

	"github.com/carsteneu/yesmem/internal/config"
	"github.com/carsteneu/yesmem/internal/storage"
)

// --- Phase 5 evidence verification wiring in the DONE-guard state machine ---
//
// When ValidatePhaseBlocks reports a compliant scratchpad and the agent record
// carries an opencode session id, the guard must verify the reported review
// subagent ids (Phase5Evidence) against opencode's session DB. Failures feed
// the same refire/pause machine as format failures; agents without an
// opencode session id skip verification.

// makeEvidenceGuardAgent creates a yesloop agent whose scratchpad is
// validV3Content and whose opencode session capture succeeded/failed.
func makeEvidenceGuardAgent(t *testing.T, h *Handler, s *storage.Store, id string, withOpencodeSession bool) {
	t.Helper()
	agent := storage.Agent{
		ID:            id,
		Project:       "testproj",
		Section:       "yesloop-" + id,
		SessionID:     "sess-" + id,
		PID:           testPID,
		Status:        "running",
		SockPath:      "/nonexistent/" + id + ".sock",
		CallerSession: "caller-" + id,
	}
	if withOpencodeSession {
		agent.OpencodeSessionID = "ses_loop_" + id
	}
	if err := s.AgentCreate(agent); err != nil {
		t.Fatalf("AgentCreate: %v", err)
	}
	s.ScratchpadWrite("testproj", "yesloop-"+id, validV3Content, "")
}

func stubEvidenceRows(t *testing.T, rows map[string][2]string) {
	t.Helper()
	orig := queryOpencodeSession
	queryOpencodeSession = func(dbPath, sessionID string) (bool, string, string, error) {
		if row, ok := rows[sessionID]; ok {
			return true, row[0], row[1], nil
		}
		return false, "", "", nil
	}
	t.Cleanup(func() { queryOpencodeSession = orig })
}

// evidenceRowsForParent returns rows keyed by the ses_ ids reported in
// validV3Content whose parent_id matches the agent's own opencode session id
// and whose agent types are in the allowed sets.
func evidenceRowsForParent(parent string) map[string][2]string {
	return map[string][2]string{
		"ses_coldfix": {parent, "reviewer"},
		"ses_consfix": {parent, "general"},
		"ses_secfix":  {parent, "reviewer"},
	}
}

// TestDoneGuard_EvidenceFailure_Relayed: a compliant-format scratchpad whose
// evidence fails verification engages the refire state machine instead of
// passing cleanly.
func TestDoneGuard_EvidenceFailure_Relayed(t *testing.T) {
	resetDoneGuardState()
	h, s := mustHandler(t)
	h.ocDBPath = evidenceDBPath(t)
	makeEvidenceGuardAgent(t, h, s, "dg-ev1", true)
	stubEvidenceRows(t, map[string][2]string{})

	h.checkYesloopDoneGuard()

	if !hasDoneGuardState("dg-ev1", doneGuardStateRefiring) {
		t.Errorf("evidence failure must engage the refire state machine (agent not tracked as refiring)")
	}
}

func TestDoneGuard_EvidenceFailure_Pauses_After_Max_Refires(t *testing.T) {
	resetDoneGuardState()
	h, s := mustHandler(t)
	h.ocDBPath = evidenceDBPath(t)
	makeEvidenceGuardAgent(t, h, s, "dg-ev2", true)
	stubEvidenceRows(t, map[string][2]string{})

	for attempt := 0; attempt < 4; attempt++ {
		setDoneGuardLastRelayAt("dg-ev2", time.Now().Add(-2*doneGuardRefireInterval))
		h.checkYesloopDoneGuard()
	}

	agent, _ := s.AgentGet("dg-ev2")
	if agent == nil || agent.Status != "paused" {
		t.Errorf("evidence failure must pause after max refires, got status=%v", agent)
	}
}

func TestDoneGuard_EvidenceOK_Passes(t *testing.T) {
	resetDoneGuardState()
	h, s := mustHandler(t)
	// Covers the Phase 5 evidence check; the verification stage that follows a
	// compliant DONE claim is done_gate_verify_test.go's subject.
	h.SetDoneVerifyConfig(&config.DoneVerifyConfig{Enabled: false})
	h.ocDBPath = evidenceDBPath(t)
	makeEvidenceGuardAgent(t, h, s, "dg-ev3", true)
	stubEvidenceRows(t, evidenceRowsForParent("ses_loop_dg-ev3"))

	h.checkYesloopDoneGuard()

	if hasDoneGuardState("dg-ev3", doneGuardStateRefiring) {
		t.Errorf("verified evidence must not be tracked as failing")
	}
	agent, _ := s.AgentGet("dg-ev3")
	if agent.Status != "running" {
		t.Errorf("verified evidence must leave agent running, got status=%q", agent.Status)
	}
}

func TestDoneGuard_EvidenceSkipped_WithoutOpencodeSessionID(t *testing.T) {
	resetDoneGuardState()
	h, s := mustHandler(t)
	// Covers the skip-on-empty contract of the evidence check; the verification
	// stage is out of scope here (done_gate_verify_test.go).
	h.SetDoneVerifyConfig(&config.DoneVerifyConfig{Enabled: false})
	h.ocDBPath = evidenceDBPath(t)
	makeEvidenceGuardAgent(t, h, s, "dg-ev4", false)
	// No verification may run: with an empty OpencodeSessionID the DB must
	// not be consulted. Any lookup attempt here would return row-missing and
	// fail the agent — the skip-on-empty contract prevents that.
	stubEvidenceRows(t, map[string][2]string{})

	h.checkYesloopDoneGuard()

	if hasDoneGuardState("dg-ev4", doneGuardStateRefiring) {
		t.Errorf("agent without opencode session id must skip evidence verification")
	}
	agent, _ := s.AgentGet("dg-ev4")
	if agent.Status != "running" {
		t.Errorf("agent without opencode session id must stay running, got status=%q", agent.Status)
	}
}
