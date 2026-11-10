package daemon

import (
	"strings"
	"testing"

	"github.com/carsteneu/yesmem/internal/config"
	"github.com/carsteneu/yesmem/internal/storage"
)

// TestDoneGuard_ProcesslessAgentIsNotUnpaused is the regression guard for YM-75:
// a compliant scratchpad must never flip a paused row back to "running" when no
// process of that agent verifies. crashRecovery owns running rows whose PID holds
// no agent process, and it reacts by quarantining the agent's learnings and
// starting a replacement — for an agent that had already written everything it
// had to say.
//
// The gate lives in checkYesloopDoneGuard (heartbeat.go): a paused agent whose
// process is definitively gone is skipped before the per-agent state machine runs.
func TestDoneGuard_ProcesslessAgentIsNotUnpaused(t *testing.T) {
	resetDoneGuardState()
	h, s := mustHandler(t)
	stubProcCmdline(t, agentProcCmdlineStub)

	// The state a paused agent's row is in after its process died: the status still
	// says paused, but no PID is bound (a dead spawn's row, a stopped backend, a
	// daemon restart).
	agent := storage.Agent{
		ID:            "dg-dead-1",
		Project:       "testproj",
		Section:       "yesloop-dg-dead-1",
		SessionID:     "sess-dg-dead-1",
		PID:           0,
		Status:        "paused",
		Progress:      "paused: DONE-GUARD: phase validation failed — Phase4 missing required field",
		Backend:       "opencode",
		SockPath:      "/nonexistent/dg-dead-1.sock",
		CallerSession: "caller-dg-dead-1",
	}
	if err := s.AgentCreate(agent); err != nil {
		t.Fatalf("AgentCreate: %v", err)
	}
	s.ScratchpadWrite("testproj", "yesloop-dg-dead-1", validV3Content, "")

	h.checkYesloopDoneGuard()

	got, err := s.AgentGet("dg-dead-1")
	if err != nil {
		t.Fatalf("get agent: %v", err)
	}
	if got.Status == "running" {
		t.Fatalf("process-less agent was unpaused to running: crashRecovery would quarantine it and start a replacement")
	}
	// Closing such a row (paused claims a live process, and nothing closes it) is a
	// separate finding, reported as Escaped — this test only pins YM-75.
	if got.Status != "paused" {
		t.Errorf("status = %q, want paused unchanged: the gate must skip the row entirely", got.Status)
	}
	if strings.Contains(got.Progress, "recovered") {
		t.Errorf("progress = %q, want no recovery note: the guard wrote to the row", got.Progress)
	}
}

// TestDoneGuard_LiveAgentIsStillUnpaused keeps the other side of the contract: a
// paused agent whose process verifies as its own is unpaused as before.
func TestDoneGuard_LiveAgentIsStillUnpaused(t *testing.T) {
	resetDoneGuardState()
	h, s := mustHandler(t)
	// Covers the process gate: a paused row whose process is still alive must
	// not be treated as gone. The verification stage is out of scope here.
	h.SetDoneVerifyConfig(&config.DoneVerifyConfig{Enabled: false})
	makeDoneGuardAgent(t, h, s, "dg-live-1", "sess-dg-live-1", validV3Content)
	s.AgentUpdate("dg-live-1", map[string]any{
		"status":   "paused",
		"progress": "paused: DONE-GUARD: phase validation failed — Phase4 missing required field",
	})

	h.checkYesloopDoneGuard()

	got, err := s.AgentGet("dg-live-1")
	if err != nil {
		t.Fatalf("get agent: %v", err)
	}
	if got.Status != "running" {
		t.Fatalf("status = %q, want running: the fixture's PID verifies as an opencode process", got.Status)
	}
	if !strings.Contains(got.Progress, "recovered by DONE-GUARD") {
		t.Errorf("progress = %q, want the recovery note", got.Progress)
	}
}
