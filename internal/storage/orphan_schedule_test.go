package storage

import (
	"strings"
	"testing"
)

// The bug this test reproduces (2026-11-26): an agent that is RUNNING when the
// daemon restarts gets orphaned, then resumed. If every resume — successful or
// not — counts against the budget, the agent is abandoned after three restarts
// while it was functioning the whole time. agent-12 stood at restart_count=3/3
// with zero failed resumes.
//
// Two things must hold:
//  1. a successful resume must not spend the budget
//  2. a row whose section is schedule-driven (`sched-%`) must not be resumed at
//     all — the schedule spawns a replacement, so a resumed predecessor would
//     never receive another input
func TestAutoResumeOrphans_RunningAgentSurvivesRestarts(t *testing.T) {
	s := testStore(t)

	// A row that was already resumed successfully twice — as in the live case.
	seedOrphan(t, s, "agent-live", 2, 3,
		"2026-11-26T09:05:03Z", "2026-11-26T09:02:00Z")

	attempted, cleared, gaveUp, superseded := s.AutoResumeOrphans(
		[]OrphanedAgent{{ID: "agent-live", Section: "wake-main"}},
		func(OrphanedAgent) error { return nil },
		func(string, ...any) {},
	)
	if gaveUp != 0 {
		t.Fatalf("a functioning agent must not be abandoned, got gaveUp=%d", gaveUp)
	}
	if attempted != 1 {
		t.Fatalf("expected the row to be resumed, got attempted=%d", attempted)
	}
	_ = cleared
	_ = superseded

	// The next restart must resume it again instead of giving up.
	decision, err := s.OrphanResumeDecision("agent-live")
	if err != nil {
		t.Fatalf("decision: %v", err)
	}
	if decision != OrphanResumeAttempt {
		t.Fatalf("after a successful resume the next restart must attempt again, got %v", decision)
	}
}

// A schedule-driven section gets a fresh agent from the next cron run, so a
// resumed predecessor would never be spoken to again. It is superseded by the
// schedule, not resumed and not left marked.
func TestAutoResumeOrphans_ScheduleSectionIsSuperseded(t *testing.T) {
	s := testStore(t)
	seedOrphanInSection(t, s, "agent-sched", "sched-wake-pulse", 0, 3,
		"2026-11-26T09:05:03Z", "2026-11-26T09:02:00Z")

	called := false
	attempted, _, gaveUp, superseded := s.AutoResumeOrphans(
		[]OrphanedAgent{{ID: "agent-sched", Section: "sched-wake-pulse"}},
		func(OrphanedAgent) error { called = true; return nil },
		func(string, ...any) {},
	)
	if called {
		t.Fatal("a schedule-driven row must not be resumed")
	}
	if attempted != 0 || gaveUp != 0 || superseded != 1 {
		t.Fatalf("expected 0/0/1, got attempted=%d gaveUp=%d superseded=%d", attempted, gaveUp, superseded)
	}

	var marker int
	var progress string
	if err := s.db.QueryRow(`SELECT COALESCE(orphan_reattachable,0), COALESCE(progress,'')
		FROM agents WHERE id='agent-sched'`).Scan(&marker, &progress); err != nil {
		t.Fatalf("read: %v", err)
	}
	if marker != 0 {
		t.Fatalf("expected marker cleared, got %d", marker)
	}
	if !strings.Contains(progress, "superseded by schedule") {
		t.Fatalf("progress should name the reason, got %q", progress)
	}
}
