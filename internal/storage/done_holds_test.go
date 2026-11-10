package storage

import "testing"

// The DONE gate's hold lives in memory and is re-derived from the report on the
// next tick. What a restart cannot re-derive is time: when the hold began, and
// whether the watchdog already escalated it. These rows carry exactly that, so
// a restarted daemon does not restart the clock on a held DONE.

func TestDONEHoldKeepsItsClockForTheSameReport(t *testing.T) {
	s := mustOpen(t)

	first := DONEHold{
		AgentID:     "a1",
		Fingerprint: "key-1",
		Kind:        "refuted",
		Reason:      "the test claim is refuted",
		HeldSince:   "2026-11-04T10:00:00Z",
	}
	if err := s.SaveDONEHold(first); err != nil {
		t.Fatalf("SaveDONEHold: %v", err)
	}
	// A later tick writes the same report with a sharper reason. The clock must
	// not move: the agent has been held since the first write.
	second := first
	second.Reason = "the test claim is refuted (finding 2)"
	second.HeldSince = "2026-11-04T10:30:00Z"
	if err := s.SaveDONEHold(second); err != nil {
		t.Fatalf("SaveDONEHold (second): %v", err)
	}

	got, found, err := s.DONEHold("a1")
	if err != nil || !found {
		t.Fatalf("DONEHold: found=%v err=%v", found, err)
	}
	if got.HeldSince != first.HeldSince {
		t.Errorf("held_since = %q, want the first write %q", got.HeldSince, first.HeldSince)
	}
	if got.Reason != second.Reason {
		t.Errorf("reason = %q, want the latest %q", got.Reason, second.Reason)
	}
}

// A corrected report is a new attempt: its clock starts over, and the watchdog
// that escalated the old attempt is cleared with it.
func TestDONEHoldRestartsTheClockForACorrectedReport(t *testing.T) {
	s := mustOpen(t)

	if err := s.SaveDONEHold(DONEHold{
		AgentID: "a2", Fingerprint: "key-1", Kind: "defect",
		HeldSince: "2026-11-04T10:00:00Z",
	}); err != nil {
		t.Fatalf("SaveDONEHold: %v", err)
	}
	if _, err := s.MarkDONEHoldWatchdog("a2", "2026-11-04T10:10:00Z"); err != nil {
		t.Fatalf("MarkDONEHoldWatchdog: %v", err)
	}
	if err := s.SaveDONEHold(DONEHold{
		AgentID: "a2", Fingerprint: "key-2", Kind: "defect",
		HeldSince: "2026-11-04T11:00:00Z",
	}); err != nil {
		t.Fatalf("SaveDONEHold (corrected): %v", err)
	}

	got, found, err := s.DONEHold("a2")
	if err != nil || !found {
		t.Fatalf("DONEHold: found=%v err=%v", found, err)
	}
	if got.HeldSince != "2026-11-04T11:00:00Z" {
		t.Errorf("held_since = %q, want the new attempt's clock", got.HeldSince)
	}
	if got.WatchdogAt != "" {
		t.Errorf("watchdog_at = %q, want it cleared for the new attempt", got.WatchdogAt)
	}
}

func TestDONEHoldWatchdogIsRecordedOnce(t *testing.T) {
	s := mustOpen(t)

	if err := s.SaveDONEHold(DONEHold{
		AgentID: "a3", Fingerprint: "key", Kind: "defect", HeldSince: "2026-11-04T10:00:00Z",
	}); err != nil {
		t.Fatalf("SaveDONEHold: %v", err)
	}
	marked, err := s.MarkDONEHoldWatchdog("a3", "2026-11-04T10:20:00Z")
	if err != nil || !marked {
		t.Fatalf("MarkDONEHoldWatchdog: marked=%v err=%v", marked, err)
	}
	if again, err := s.MarkDONEHoldWatchdog("a3", "2026-11-04T10:30:00Z"); err != nil || again {
		t.Errorf("a second mark won the row: again=%v err=%v", again, err)
	}

	got, found, err := s.DONEHold("a3")
	if err != nil || !found {
		t.Fatalf("DONEHold: found=%v err=%v", found, err)
	}
	if got.WatchdogAt != "2026-11-04T10:20:00Z" {
		t.Errorf("watchdog_at = %q, want the escalation time", got.WatchdogAt)
	}
}

func TestDONEHoldsListAndDelete(t *testing.T) {
	s := mustOpen(t)

	for _, id := range []string{"b1", "b2"} {
		if err := s.SaveDONEHold(DONEHold{
			AgentID: id, Fingerprint: "key-" + id, Kind: "defect", HeldSince: "2026-11-04T10:00:00Z",
		}); err != nil {
			t.Fatalf("SaveDONEHold(%s): %v", id, err)
		}
	}
	holds, err := s.DONEHolds()
	if err != nil {
		t.Fatalf("DONEHolds: %v", err)
	}
	if len(holds) != 2 {
		t.Fatalf("DONEHolds = %d rows, want 2", len(holds))
	}

	if err := s.DeleteDONEHold("b1"); err != nil {
		t.Fatalf("DeleteDONEHold: %v", err)
	}
	if _, found, err := s.DONEHold("b1"); err != nil || found {
		t.Errorf("deleted hold still readable: found=%v err=%v", found, err)
	}
	holds, err = s.DONEHolds()
	if err != nil {
		t.Fatalf("DONEHolds: %v", err)
	}
	if len(holds) != 1 || holds[0].AgentID != "b2" {
		t.Fatalf("DONEHolds after delete = %+v, want only b2", holds)
	}
}

// A pending hold becomes a blocked one without the report changing. That
// transition is the moment the agent gets its correction round, so the clock
// has to start there: measuring from the pending hold would escalate an agent
// that never had a chance to correct anything.
func TestDONEHoldRestartsTheClockWhenTheDecisionChanges(t *testing.T) {
	s := mustOpen(t)

	if err := s.SaveDONEHold(DONEHold{
		AgentID: "a4", Fingerprint: "key", Status: "pending", HeldSince: "2026-11-04T10:00:00Z",
	}); err != nil {
		t.Fatalf("SaveDONEHold: %v", err)
	}
	if _, err := s.MarkDONEHoldWatchdog("a4", "2026-11-04T10:05:00Z"); err != nil {
		t.Fatalf("MarkDONEHoldWatchdog: %v", err)
	}
	if err := s.SaveDONEHold(DONEHold{
		AgentID: "a4", Fingerprint: "key", Status: "blocked", Kind: "refuted",
		HeldSince: "2026-11-04T10:20:00Z",
	}); err != nil {
		t.Fatalf("SaveDONEHold (blocked): %v", err)
	}

	got, found, err := s.DONEHold("a4")
	if err != nil || !found {
		t.Fatalf("DONEHold: found=%v err=%v", found, err)
	}
	if got.HeldSince != "2026-11-04T10:20:00Z" {
		t.Errorf("held_since = %q, want the blocked decision's clock", got.HeldSince)
	}
	if got.WatchdogAt != "" {
		t.Errorf("watchdog_at = %q, want it cleared with the new decision", got.WatchdogAt)
	}
	if got.Status != "blocked" {
		t.Errorf("status = %q, want the new decision", got.Status)
	}
}
