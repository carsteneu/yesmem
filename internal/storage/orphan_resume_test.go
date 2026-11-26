package storage

import (
	"testing"
)

// seedOrphan writes one marked row with the fields the resume decision reads.
func seedOrphan(t *testing.T, s *Store, id string, restartCount, maxRestarts int,
	stoppedAt, lastActivity string) {
	t.Helper()
	if _, err := s.db.Exec(`INSERT INTO agents
		(id, project, section, session_id, status, progress, orphan_reattachable,
		 opencode_session_id, work_dir, restart_count, max_restarts,
		 stopped_at, last_activity_at)
		VALUES (?, '/p', 'sec', ?, 'stopped', 'orphaned-reattachable', 1,
		 ?, '/p', ?, ?, ?, ?)`,
		id, "ses-"+id, "oc-"+id, restartCount, maxRestarts, stoppedAt, lastActivity); err != nil {
		t.Fatalf("seed %s: %v", id, err)
	}
}

// A marked row whose backend session produced work AFTER the stop does not need
// a resume: it already moved on outside the row. Clearing the marker is the
// correct outcome — resuming it would restart finished work.
func TestAutoResumeDecision_WorkAfterStopClearsMarker(t *testing.T) {
	s := testStore(t)
	seedOrphan(t, s, "agent-a", 0, 3,
		"2026-11-26T09:05:03Z", "2026-11-26T09:10:00Z")

	decision, err := s.OrphanResumeDecision("agent-a")
	if err != nil {
		t.Fatalf("decision: %v", err)
	}
	if decision != OrphanResumeClearMarker {
		t.Fatalf("expected clear-marker, got %v", decision)
	}
}

// A marked row whose work stopped at the stop time still needs a resume.
func TestAutoResumeDecision_NoWorkAfterStopResumes(t *testing.T) {
	s := testStore(t)
	seedOrphan(t, s, "agent-b", 0, 3,
		"2026-11-26T09:05:03Z", "2026-11-26T09:02:00Z")

	decision, err := s.OrphanResumeDecision("agent-b")
	if err != nil {
		t.Fatalf("decision: %v", err)
	}
	if decision != OrphanResumeAttempt {
		t.Fatalf("expected attempt, got %v", decision)
	}
}

// The budget is the guard against a resume loop: once restart_count reaches
// max_restarts the row is abandoned loudly instead of resumed forever.
func TestAutoResumeDecision_BudgetExhaustedGivesUp(t *testing.T) {
	s := testStore(t)
	seedOrphan(t, s, "agent-c", 3, 3,
		"2026-11-26T09:05:03Z", "2026-11-26T09:02:00Z")

	decision, err := s.OrphanResumeDecision("agent-c")
	if err != nil {
		t.Fatalf("decision: %v", err)
	}
	if decision != OrphanResumeGiveUp {
		t.Fatalf("expected give-up, got %v", decision)
	}
}

// A row with no timestamps at all must not crash the decision — missing data is
// treated as "needs a resume", the conservative choice (the session exists on
// disk, so trying is harmless; not trying leaves it frozen).
func TestAutoResumeDecision_MissingTimestampsResumes(t *testing.T) {
	s := testStore(t)
	seedOrphan(t, s, "agent-d", 0, 3, "", "")

	decision, err := s.OrphanResumeDecision("agent-d")
	if err != nil {
		t.Fatalf("decision: %v", err)
	}
	if decision != OrphanResumeAttempt {
		t.Fatalf("expected attempt, got %v", decision)
	}
}

// The two columns do NOT share a format: stopped_at carries its zone
// (T...+01:00), last_activity_at is local time with a space separator and no
// zone. This is the real-world shape and the case that used to be wrong:
// agent-09 stopped 09:05+01:00, last activity 09:02 local — three minutes BEFORE
// the stop, so it needs a resume, not a cleared marker.
func TestAutoResumeDecision_MixedFormatsNoWorkAfterStop(t *testing.T) {
	s := testStore(t)
	seedOrphan(t, s, "agent-mixed", 0, 3,
		"2026-11-26T09:05:03+01:00", "2026-11-26 09:02:00")

	decision, err := s.OrphanResumeDecision("agent-mixed")
	if err != nil {
		t.Fatalf("decision: %v", err)
	}
	if decision != OrphanResumeAttempt {
		t.Fatalf("activity predates the stop, expected attempt, got %v", decision)
	}
}

// Same mixed formats, but the activity is genuinely after the stop:
// agent-07 stopped 07:01:12+01:00, last activity 07:04:53 local.
func TestAutoResumeDecision_MixedFormatsWorkAfterStop(t *testing.T) {
	s := testStore(t)
	seedOrphan(t, s, "agent-mixed2", 0, 3,
		"2026-11-26T07:01:12+01:00", "2026-11-26 07:04:53")

	decision, err := s.OrphanResumeDecision("agent-mixed2")
	if err != nil {
		t.Fatalf("decision: %v", err)
	}
	if decision != OrphanResumeClearMarker {
		t.Fatalf("activity is after the stop, expected clear-marker, got %v", decision)
	}
}

// A string comparison would always favour stopped_at (' ' sorts before 'T'), so
// this case pins that the decision is time-based, not lexical.
func TestAutoResumeDecision_NotAStringComparison(t *testing.T) {
	s := testStore(t)
	// Lexically "2026-11-26 10:00:00" > "2026-11-26T09:00:00+01:00" is false,
	// but as times 10:00 local (09:00 UTC) is after 09:00+01:00 (08:00 UTC).
	seedOrphan(t, s, "agent-lex", 0, 3,
		"2026-11-26T09:00:00+01:00", "2026-11-26 10:00:00")

	decision, err := s.OrphanResumeDecision("agent-lex")
	if err != nil {
		t.Fatalf("decision: %v", err)
	}
	if decision != OrphanResumeClearMarker {
		t.Fatalf("time comparison says after, expected clear-marker, got %v", decision)
	}
}
func TestAutoResumeDecision_ZeroMaxRestartsUsesDefaultBudget(t *testing.T) {
	s := testStore(t)

	// One attempt below the default budget: still allowed.
	seedOrphan(t, s, "agent-zero-under", defaultOrphanResumeBudget-1, 0,
		"2026-11-26T09:05:03Z", "2026-11-26T09:02:00Z")
	d, err := s.OrphanResumeDecision("agent-zero-under")
	if err != nil {
		t.Fatalf("decision: %v", err)
	}
	if d != OrphanResumeAttempt {
		t.Fatalf("expected attempt below the default budget, got %v", d)
	}

	// At the default budget: give up, even though max_restarts says 0.
	seedOrphan(t, s, "agent-zero-at", defaultOrphanResumeBudget, 0,
		"2026-11-26T09:05:03Z", "2026-11-26T09:02:00Z")
	d, err = s.OrphanResumeDecision("agent-zero-at")
	if err != nil {
		t.Fatalf("decision: %v", err)
	}
	if d != OrphanResumeGiveUp {
		t.Fatalf("expected give-up at the default budget with max_restarts=0, got %v", d)
	}
}

// Recording an attempt must increment restart_count — the count of ATTEMPTS, not
// successes, otherwise a resume that dies in seconds never fills the budget.
func TestRecordOrphanResumeAttempt_IncrementsCounter(t *testing.T) {
	s := testStore(t)
	seedOrphan(t, s, "agent-e", 0, 3,
		"2026-11-26T09:05:03Z", "2026-11-26T09:02:00Z")

	if err := s.RecordOrphanResumeAttempt("agent-e"); err != nil {
		t.Fatalf("record: %v", err)
	}
	var n int
	if err := s.db.QueryRow(`SELECT restart_count FROM agents WHERE id = 'agent-e'`).Scan(&n); err != nil {
		t.Fatalf("read: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected restart_count 1, got %d", n)
	}
}

// Clearing the marker must leave the row's retention semantics intact: status
// stays, only orphan_reattachable goes to 0 so the prune can collect it later.
func TestClearOrphanMarker_KeepsRow(t *testing.T) {
	s := testStore(t)
	seedOrphan(t, s, "agent-f", 0, 3,
		"2026-11-26T09:05:03Z", "2026-11-26T09:10:00Z")

	if err := s.ClearOrphanMarker("agent-f", "work continued after the stop"); err != nil {
		t.Fatalf("clear: %v", err)
	}
	var status string
	var marker int
	if err := s.db.QueryRow(
		`SELECT status, COALESCE(orphan_reattachable,0) FROM agents WHERE id = 'agent-f'`,
	).Scan(&status, &marker); err != nil {
		t.Fatalf("read: %v", err)
	}
	if marker != 0 {
		t.Fatalf("expected marker cleared, got %d", marker)
	}
	if status != "stopped" {
		t.Fatalf("expected status kept at stopped, got %q", status)
	}
}
