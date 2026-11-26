package daemon

import (
	"testing"
	"time"
)

// A restart must not fire the whole schedule. The scheduler keeps lastFire in
// memory; when it starts empty every interval job looks due at once and the whole
// schedule fires on every daemon start (Issue #176 on 2026-11-26: self-pulse-b
// delivered twice, 44 s apart, interval 900 s).
func TestScheduler_RestartDoesNotFireEverything(t *testing.T) {
	s := newScheduler(nil, func(ScheduledJob) {})

	lastRun := time.Now().Add(-90 * time.Second)
	job := ScheduledJob{
		ID:              "job-1",
		Name:            "interval-job",
		Enabled:         true,
		IntervalSeconds: 300,
		LastRun:         lastRun,
	}
	if err := s.AddJob(job); err != nil {
		t.Fatalf("AddJob: %v", err)
	}

	// 90 s after the last run, a 300 s job is not due.
	if due := s.dueJobs(time.Now()); len(due) != 0 {
		t.Fatalf("a job that ran 90 s ago must not be due again, got %d due", len(due))
	}
}

// A job that never ran (zero LastRun) must fire on start — the seed must not
// suppress first runs.
func TestScheduler_NeverRunJobIsDue(t *testing.T) {
	s := newScheduler(nil, func(ScheduledJob) {})

	job := ScheduledJob{
		ID:              "job-2",
		Name:            "never-ran",
		Enabled:         true,
		IntervalSeconds: 300,
	}
	if err := s.AddJob(job); err != nil {
		t.Fatalf("AddJob: %v", err)
	}

	if due := s.dueJobs(time.Now()); len(due) != 1 {
		t.Fatalf("a job that never ran must be due, got %d", len(due))
	}
}

// Once the interval has really elapsed the job fires, so the seed must not freeze
// the schedule.
func TestScheduler_OverdueJobFiresAfterSeed(t *testing.T) {
	s := newScheduler(nil, func(ScheduledJob) {})

	job := ScheduledJob{
		ID:              "job-3",
		Name:            "overdue",
		Enabled:         true,
		IntervalSeconds: 300,
		LastRun:         time.Now().Add(-400 * time.Second),
	}
	if err := s.AddJob(job); err != nil {
		t.Fatalf("AddJob: %v", err)
	}

	if due := s.dueJobs(time.Now()); len(due) != 1 {
		t.Fatalf("a job overdue by 100 s must fire, got %d", len(due))
	}
}
