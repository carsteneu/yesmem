package daemon

import (
	"strings"
	"testing"
	"time"

	"github.com/carsteneu/yesmem/internal/storage"
)

// A nudge is information, not a hold: the row keeps running and carries the
// finding as a note. Only the DONE verdict paths are allowed to pause.
func TestNudgeAgent_NoteWithoutPause(t *testing.T) {
	resetIdleState()
	h, s := mustHandler(t)
	makeYesloopAgent(t, h, s, "nudge-1", "sess-nudge-1", testPID, true, time.Minute)

	if ok := h.nudgeAgent("nudge-1", "yesloop-idle escalation: no PROVEN marker"); !ok {
		t.Fatal("a running row must take the note")
	}

	row, err := s.AgentGet("nudge-1")
	if err != nil {
		t.Fatalf("AgentGet: %v", err)
	}
	if row.Status != "running" {
		t.Errorf("a nudge must not stop the agent, status = %q (progress=%q)", row.Status, row.Progress)
	}
	if !strings.HasPrefix(row.Progress, guardNudgeProgressPrefix) {
		t.Errorf("the note must carry the nudge prefix %q, got %q", guardNudgeProgressPrefix, row.Progress)
	}
	if !strings.Contains(row.Progress, "yesloop-idle escalation") {
		t.Errorf("the note must name the finding, got %q", row.Progress)
	}
}

// A paused row belongs to the pause that produced it: the DONE gate matches the
// progress prefix to know it still holds, so a nudge must not overwrite it.
func TestNudgeAgent_LeavesAPausedRowAlone(t *testing.T) {
	resetIdleState()
	h, s := mustHandler(t)
	makeYesloopAgent(t, h, s, "nudge-held", "sess-nudge-held", testPID, true, time.Minute)
	held := "paused: DONE-PENDING-VERIFY: the pre-check for this report is running; the verdict decides"
	if err := s.AgentUpdate("nudge-held", map[string]any{"status": "paused", "progress": held}); err != nil {
		t.Fatalf("AgentUpdate: %v", err)
	}

	if ok := h.nudgeAgent("nudge-held", "yesloop-idle escalation: no PROVEN marker"); ok {
		t.Error("a paused row must not take the note")
	}

	row, err := s.AgentGet("nudge-held")
	if err != nil {
		t.Fatalf("AgentGet: %v", err)
	}
	if row.Status != "paused" || row.Progress != held {
		t.Errorf("the hold must stay untouched, status = %q, progress = %q", row.Status, row.Progress)
	}
}

// assertGuardNudged is the witness the four bookkeeping guards owe: the row kept
// running, the finding is on the row, and the orchestrator got exactly one
// non-blocking notice that asks nothing of it.
func assertGuardNudged(t *testing.T, s *storage.Store, id, reasonMarker string) *storage.Agent {
	t.Helper()
	row, err := s.AgentGet(id)
	if err != nil {
		t.Fatalf("AgentGet(%s): %v", id, err)
	}
	if row.Status != "running" {
		t.Errorf("a guard must nudge, not stop: status = %q (progress=%q)", row.Status, row.Progress)
	}
	if !strings.HasPrefix(row.Progress, guardNudgeProgressPrefix) {
		t.Errorf("the row must carry the nudge note, got progress %q", row.Progress)
	}
	if !strings.Contains(row.Progress, reasonMarker) {
		t.Errorf("the note must name %q, got %q", reasonMarker, row.Progress)
	}

	msgs, err := s.GetChannelMessages([]string{"caller-" + id})
	if err != nil {
		t.Fatalf("GetChannelMessages: %v", err)
	}
	notices := 0
	for _, m := range msgs {
		if !strings.HasPrefix(m.Content, guardNudgeNoticePrefix) {
			continue
		}
		notices++
		if strings.Contains(m.Content, orchestratorPauseHint) {
			t.Errorf("a nudge notice must not tell the orchestrator to resume anything, got %q", m.Content)
		}
		if strings.Contains(m.Content, "paused") {
			t.Errorf("a nudge notice must not claim the agent was paused, got %q", m.Content)
		}
	}
	if notices != 1 {
		t.Errorf("want exactly one nudge notice for %s, got %d (%+v)", id, notices, msgs)
	}
	return row
}
