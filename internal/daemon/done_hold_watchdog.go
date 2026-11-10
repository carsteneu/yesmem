package daemon

import (
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/carsteneu/yesmem/internal/storage"
)

// The time witness of a held DONE.
//
// A hold is announced the moment it is placed and the agent is asked to correct
// its report. Three ways to silence remained: the agent disappears after the
// defect, the daemon restarts, or nobody ever decides. The first two are the
// same hole seen from two sides, a hold without a clock; the third is a hold
// that outlives everyone's patience. This watchdog closes all three: it reads
// the persisted holds and escalates an undecided one exactly once, at the
// window the gate already calls max_wait.

// doneHoldWindow is the time a hold may stay undecided before the daemon hands
// it to the orchestrator on its own. It is the larger of the two limits a run
// may spend: a pre-check that waits for its slot and then runs is longer than
// max_wait, and escalating that run would blame the agent for the queue.
func (h *Handler) doneHoldWindow() time.Duration {
	window := h.doneVerifyMaxWait()
	if budget := h.doneVerifyBudget(); budget > window {
		return budget
	}
	return window
}

// checkDONEHoldWatchdog is the heartbeat's pass over the held rows. It runs on
// the same cadence as the DONE-guard, so a hold is escalated within one tick of
// its window and never depends on the agent being alive.
//
// The pass cleans up even while the gate is switched off: a row from before the
// switch would otherwise be escalated on re-enable for a case somebody already
// decided by hand.
func (h *Handler) checkDONEHoldWatchdog() {
	if h.store == nil {
		return
	}
	holds, err := h.store.DONEHolds()
	if err != nil {
		log.Printf("[done-hold] reading the persisted holds failed: %v", safeAgentText(err.Error()))
		return
	}
	window := h.doneHoldWindow()
	enabled := h.doneVerifyEnabled()
	now := time.Now()
	for _, hold := range holds {
		agent, agentErr := h.store.AgentGet(hold.AgentID)
		if agentErr != nil || agent == nil {
			// The row is gone with its process; a hold without an agent is not
			// a case but a leftover.
			h.dropDONEHold(hold.AgentID)
			continue
		}
		if !heldRowIsLive(*agent) {
			// Released or resumed by the orchestrator: this hold has its answer,
			// whatever it was. The check is the status, not the status line — a
			// guard that pauses a row writes its own line, and that must not be
			// read as "the case is closed" (it is the moment it got louder).
			h.dropDONEHold(hold.AgentID)
			continue
		}
		if escalatedHold(*agent) {
			// The case already left the loop through the three-round path: the
			// status line is its witness, the row is not needed any more, and a
			// second announcement would be the fourth round.
			h.dropDONEHold(hold.AgentID)
			continue
		}
		if hold.WatchdogAt != "" {
			// Already escalated. The mark is the promise that this case does not
			// produce a message on every tick.
			continue
		}
		if !enabled {
			continue
		}
		heldSince, parseErr := time.Parse(time.RFC3339, hold.HeldSince)
		if parseErr != nil {
			log.Printf("[done-hold] agent %s: unreadable hold clock %q: %v",
				safeAgentText(hold.AgentID), safeAgentText(hold.HeldSince), safeAgentText(parseErr.Error()))
			continue
		}
		if now.Sub(heldSince) < window {
			continue
		}
		h.escalateUndecidedHold(*agent, hold, window)
	}
}

// heldRowIsLive reports whether a row still carries a case: paused by any guard.
// A running row was released, and a row that is neither is not a hold.
func heldRowIsLive(agent storage.Agent) bool {
	return agent.Status == "paused"
}

// escalateUndecidedHold hands an undecided case to the orchestrator: the class,
// what the hold said, and the command that produces the verdict this contract
// reads. The agent stays paused and is told nothing new: the decision is not
// its to make for the fourth time.
func (h *Handler) escalateUndecidedHold(agent storage.Agent, hold storage.DONEHold, window time.Duration) {
	// The mark is claimed before the message so two daemons cannot both send
	// it, and it is withdrawn when the message could not be delivered: a mark
	// without a message is silence with a receipt.
	won, err := h.store.MarkDONEHoldWatchdog(agent.ID, time.Now().Format(time.RFC3339))
	if err != nil {
		log.Printf("[done-hold] agent %s: claiming the escalation failed: %v",
			safeAgentText(agent.ID), safeAgentText(err.Error()))
		return
	}
	if !won {
		return
	}
	log.Printf("[done-hold] agent %s (%s): hold undecided since %s (window %s) — escalating",
		safeAgentText(agent.ID), safeAgentText(agent.Section), safeAgentText(hold.HeldSince), window)
	h.pauseAgent(agent.ID, fmt.Sprintf(
		"DONE-VERIFY-BLOCKED (%s, undecided): no decision within %s; the orchestrator decides, the agent waits",
		safeAgentText(hold.Kind), window))
	if !h.notifyOrchestrator(agent, doneUndecidedHoldMessage(agent, hold, window)) {
		log.Printf("[done-hold] agent %s: the escalation did not leave the daemon; the next tick retries",
			safeAgentText(agent.ID))
		if err := h.store.ClearDONEHoldWatchdog(agent.ID); err != nil {
			log.Printf("[done-hold] agent %s: withdrawing the escalation mark failed: %v",
				safeAgentText(agent.ID), safeAgentText(err.Error()))
		}
	}
}

// dropDONEHold removes a persisted hold whose case no longer exists. A failed
// delete is logged and retried on the next tick, because the row would
// otherwise be escalated for a row nobody holds.
func (h *Handler) dropDONEHold(agentID string) {
	if err := h.store.DeleteDONEHold(agentID); err != nil {
		log.Printf("[done-hold] dropping the hold of %s failed: %v",
			safeAgentText(agentID), safeAgentText(err.Error()))
	}
}

// doneUndecidedHoldMessage is the escalation the watchdog owes the orchestrator:
// that the DONE never arrived, since when it is held, what the class says, and
// the command that produces a verdict.
func doneUndecidedHoldMessage(agent storage.Agent, hold storage.DONEHold, window time.Duration) string {
	repo := agent.WorkDir
	if repo == "" {
		repo = agent.Project
	}
	var builder strings.Builder
	if hold.Kind == doneVerifyPending {
		fmt.Fprintf(&builder,
			"DONE-UNDECIDED (%s): agent %s (%s) reported DONE and the pre-check for its report has not finished since %s (window %s). The DONE never reached you. The agent stays paused, and the verdict decides.",
			safeAgentText(hold.Kind), safeAgentText(agent.ID), safeAgentText(agent.Section),
			safeAgentText(hold.HeldSince), window)
	} else {
		fmt.Fprintf(&builder,
			"DONE-UNDECIDED: agent %s (%s) sent a DONE payload, the message never reached you, and the hold has been undecided since %s (window %s). Class: %s. Reason: %s.",
			safeAgentText(agent.ID), safeAgentText(agent.Section), safeAgentText(hold.HeldSince),
			window, safeAgentText(hold.Kind), safeAgentText(hold.Reason))
		fmt.Fprintf(&builder,
			"\nThe agent is still held and has not corrected the report %s: nothing was discarded, and nothing will happen without a decision.",
			safeAgentText(verifyReportSection(agent.Section)))
	}
	builder.WriteString("\nThe command that produces a verdict:\n  ")
	// The command is built from the agent row, whose work directory is not
	// filtered at spawn time. It is rendered through the field neutraliser so a
	// newline cannot open a second line in the reader's message.
	builder.WriteString(safeAgentText(verifyClaimsCommand(agent, repo)))
	return builder.String()
}
