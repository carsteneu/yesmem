package daemon

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"testing"

	"github.com/carsteneu/yesmem/internal/storage"
	"github.com/carsteneu/yesmem/internal/verify"
	"github.com/carsteneu/yesmem/internal/verify/engine"
)

// endToEndAgent creates a yesloop agent that claims DONE and writes its report
// into the section beside its own, the shape the skill asks for.
func endToEndAgent(t *testing.T, h *Handler, s *storage.Store, id, repo, report string) storage.Agent {
	t.Helper()
	const project = "testproj"
	section := "yesloop-" + id
	if err := s.AgentCreate(storage.Agent{
		ID:            id,
		Project:       project,
		Section:       section,
		SessionID:     "sess-" + id,
		PID:           testPID,
		Status:        "running",
		Backend:       "opencode",
		SockPath:      "/nonexistent/" + id + ".sock",
		CallerSession: "caller-" + id,
		WorkDir:       repo,
	}); err != nil {
		t.Fatalf("AgentCreate: %v", err)
	}
	content := doneClaimContent(t, s, project, section, report)
	if err := s.ScratchpadWrite(project, section, content, ""); err != nil {
		t.Fatalf("ScratchpadWrite: %v", err)
	}
	// A reporting agent is alive; the gate refuses to revive a row whose process
	// is gone.
	stubProcCmdline(t, agentProcCmdlineStub)
	agent, err := s.AgentGet(id)
	if err != nil {
		t.Fatalf("AgentGet: %v", err)
	}
	return *agent
}

// preCheck runs the real operation for one report, with the rule set the DONE gate
// requires. It is the call the CLI makes: same profile, same policy, same session
// and plan thread.
func preCheck(t *testing.T, h *Handler, agent storage.Agent, repo, report string) verifyClaimsResult {
	t.Helper()
	response := h.handleVerifyClaims(map[string]any{
		"repo":           repo,
		"report":         report,
		"profile":        verify.ProfileYesloopDone,
		"policy":         doneVerifyPolicyName,
		"strict":         true,
		"report_session": agent.SessionID,
		"plan_thread":    agent.SessionID,
	})
	if response.Error != "" {
		t.Fatalf("verify_claims: %s", response.Error)
	}
	var result verifyClaimsResult
	if err := json.Unmarshal(response.Result, &result); err != nil {
		t.Fatalf("unreadable answer: %v", err)
	}
	return result
}

// The whole point of the operation, driven end to end: a real verification decides
// the DONE claim, and the gate reuses its verdict instead of running anything. An
// honest report is released from the stored verdict, a refuted one is blocked with
// the findings the run produced.
func TestDoneGateEndToEnd(t *testing.T) {
	if engine.FindBwrap() == "" {
		t.Skip("bubblewrap is missing: the test-run claim of an honest report cannot run on this host")
	}
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 is missing: the fixture's unittest cannot run")
	}
	repo, head, unpushed := claimsRepo(t)

	honestReport := fmt.Sprintf("[COMMIT: %s]\n[BRANCH: main]\nTests run: python3 -m unittest test_ok.py -> exit 0\n", head)
	fakedReport := fmt.Sprintf("[COMMIT: %s]\n[BRANCH: main]\nTests run: python3 -m unittest test_ok.py -> exit 0\n", strings.Repeat("0", 40))
	notPushedReport := fmt.Sprintf("[COMMIT: %s]\n[BRANCH: main]\nTests run: python3 -m unittest test_ok.py -> exit 0\n", unpushed)

	cases := []struct {
		name         string
		report       string
		wantAccepted bool
		wantKind     string
	}{
		{name: "an honest report is released from the stored verdict", report: honestReport, wantAccepted: true},
		{name: "a faked commit hash is blocked with the finding", report: fakedReport, wantKind: "refuted"},
		{name: "an unpushed commit is blocked with the finding", report: notPushedReport, wantKind: "refuted"},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			resetDoneGuardState()
			resetDoneGateVerifyStates()
			h, s := mustHandler(t)

			agent := endToEndAgent(t, h, s, "e2e", repo, test.report)

			// The pre-check is the only place a verification runs.
			result := preCheck(t, h, agent, repo, test.report)
			if got := result.Status == "accepted"; got != test.wantAccepted {
				t.Fatalf("pre-check accepted = %v (kind %q, %s), want %v", got, result.Kind, result.Reason, test.wantAccepted)
			}
			verdict := h.verifyVerdictFor(preCheckKey(t, h, agent, test.report))
			if verdict == nil {
				t.Fatal("the run recorded no verdict under the gate's key: the DONE claim cannot be released from it")
			}
			if verdict.Mode != doneVerifyRequiredMode() {
				t.Fatalf("verdict mode = %q, want %q", verdict.Mode, doneVerifyRequiredMode())
			}

			// The gate reads that verdict. Nothing runs here: the slot is held for the
			// whole call, so an attempt to enter the operation would be refused.
			h.verifySlot <- struct{}{}
			handled := h.gateDoneVerify(agent, h.readAgentScratchpad(agent))
			<-h.verifySlot
			if !handled {
				t.Fatal("gateDoneVerify = false, want true: the claim is a compliant DONE report")
			}

			after, err := s.AgentGet(agent.ID)
			if err != nil {
				t.Fatalf("AgentGet: %v", err)
			}
			if test.wantAccepted {
				if after.Status != "running" {
					t.Errorf("status = %q, want running after an honest report", after.Status)
				}
				if state := h.doneGateVerifyStateFor(agent.ID); state == nil || !state.Reused {
					t.Errorf("state = %+v, want the reused accepted verdict", state)
				}
				return
			}
			if after.Status != "paused" {
				t.Errorf("status = %q, want paused: the report was blocked", after.Status)
			}
			if !strings.Contains(after.Progress, "DONE-VERIFY-BLOCKED ("+test.wantKind+")") {
				t.Errorf("progress = %q, want the class %q in the freeze reason", after.Progress, test.wantKind)
			}
			state := h.doneGateVerifyStateFor(agent.ID)
			if state == nil || state.Status != doneVerifyBlocked || len(state.Findings) == 0 {
				t.Fatalf("state = %+v, want blocked with the run's findings", state)
			}
			if len(state.Findings) != len(result.Findings) {
				t.Errorf("stored findings = %d, want the run's %d", len(state.Findings), len(result.Findings))
			}
			table := doneVerifyBlockedMessage(*after, state)
			if !strings.Contains(table, state.Findings[0].Kind) {
				t.Errorf("findings table %q does not name the first finding's kind %q", table, state.Findings[0].Kind)
			}
		})
	}
}
