package daemon

import (
	"fmt"
	"log"
	"strings"
)

// The bookkeeping guards (idle, stagnation, broken tool call, done-verify proof)
// report a standstill and let the agent keep running. Only the DONE verdict
// paths pause, because there the agent genuinely has to wait for an answer; see
// yesloopPauseWhitelist for the closed list and its guard test.

// guardNudgeProgressPrefix marks the row note a nudge leaves behind. It is not a
// pause prefix: the status stays running, and no guard skips a nudged row.
const guardNudgeProgressPrefix = "guard nudge: "

// guardNudgeNoticePrefix marks the single notice a nudge sends to the
// orchestrator. The text asks nothing of the reader: it is information.
const guardNudgeNoticePrefix = "GUARD NUDGE: "

// retiredGuardPausePrefixes are the progress texts the four guards wrote before
// they became nudges. Rows carrying one are in the database and no guard watches
// a paused row, so a relay releases them (handler_agents.go): without that they
// would stay paused for good.
func retiredGuardPausePrefixes() []string {
	return []string{
		"paused: " + yesloopIdleMarker,
		"paused: yesloop-stagnation escalation:",
		"paused: yesloop-broken-toolcall escalation:",
		"paused: yesloop-done-verify escalation:",
	}
}

// matchesRetiredGuardPause reports whether a row's progress is one of them.
func matchesRetiredGuardPause(progress string) bool {
	for _, prefix := range retiredGuardPausePrefixes() {
		if strings.HasPrefix(progress, prefix) {
			return true
		}
	}
	return false
}

// nudgeAgent records a guard's finding on a running row without stopping the
// agent, and reports whether the note was written.
//
// The write is a compare-and-set on the status and progress it just read: a DONE
// pause can land between the read and the write, and overwriting that row's
// progress would hide the hold from the gate that placed it. A caller that gets
// false must not tell the orchestrator the agent is running.
func (h *Handler) nudgeAgent(id, reason string) bool {
	a, err := h.store.AgentGet(id)
	if err != nil {
		log.Printf("[guard-nudge] reading agent %s failed: %v", safeAgentText(id), safeAgentText(err.Error()))
		return false
	}
	if a == nil || a.Status != "running" {
		return false
	}
	note := guardNudgeProgressPrefix + safeAgentText(reason)
	changed, err := h.store.AgentUpdateIfMatches(id, "running", a.Progress, map[string]any{"progress": note})
	if err != nil {
		log.Printf("[guard-nudge] noting agent %s failed: %v", safeAgentText(id), safeAgentText(err.Error()))
		return false
	}
	return changed
}

// skipNudgeNote is the log line a caller writes when the row moved under the
// decision. It is not an error: a DONE pause is allowed to win.
func skipNudgeNote(guard, id, section string) {
	log.Printf("[%s] agent %s (%s) nudge skipped: the row changed after the decision", guard,
		safeAgentText(id), safeAgentText(section))
}

// nudgeNotice renders the one informational notice a guard owes the orchestrator.
// A guard that could not leave the note stays quiet instead of claiming a state
// it did not write.
func nudgeNotice(summary string) string {
	return fmt.Sprintf("%s%s. The agent keeps running, nothing waits for you.", guardNudgeNoticePrefix, summary)
}
