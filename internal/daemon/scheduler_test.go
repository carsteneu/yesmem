package daemon

import (
	"fmt"
	"testing"
	"time"
)

func TestCronMatch_EveryMinute(t *testing.T) {
	c, err := parseCron("* * * * *")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 4, 21, 10, 30, 0, 0, time.UTC)
	if !c.matches(now) {
		t.Error("* * * * * should match every minute")
	}
}

func TestCronMatch_SpecificTime(t *testing.T) {
	c, err := parseCron("30 8 * * *")
	if err != nil {
		t.Fatal(err)
	}
	yes := time.Date(2026, 4, 21, 8, 30, 0, 0, time.UTC)
	no := time.Date(2026, 4, 21, 8, 31, 0, 0, time.UTC)
	if !c.matches(yes) {
		t.Error("should match 8:30")
	}
	if c.matches(no) {
		t.Error("should not match 8:31")
	}
}

func TestCronMatch_Weekday(t *testing.T) {
	c, err := parseCron("0 9 * * 1-5")
	if err != nil {
		t.Fatal(err)
	}
	mon := time.Date(2026, 4, 20, 9, 0, 0, 0, time.UTC) // Monday
	sat := time.Date(2026, 4, 25, 9, 0, 0, 0, time.UTC) // Saturday
	if !c.matches(mon) {
		t.Errorf("should match Monday, got weekday=%d", mon.Weekday())
	}
	if c.matches(sat) {
		t.Errorf("should not match Saturday, got weekday=%d", sat.Weekday())
	}
}

func TestCronMatch_StepInterval(t *testing.T) {
	c, err := parseCron("*/15 * * * *")
	if err != nil {
		t.Fatal(err)
	}
	at0 := time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC)
	at15 := time.Date(2026, 4, 21, 10, 15, 0, 0, time.UTC)
	at7 := time.Date(2026, 4, 21, 10, 7, 0, 0, time.UTC)
	if !c.matches(at0) {
		t.Error("*/15 should match :00")
	}
	if !c.matches(at15) {
		t.Error("*/15 should match :15")
	}
	if c.matches(at7) {
		t.Error("*/15 should not match :07")
	}
}

func TestCronParse_Invalid(t *testing.T) {
	cases := []string{
		"",
		"* *",
		"* * * * * *",
		"61 * * * *",
		"* 25 * * *",
		"abc * * * *",
	}
	for _, tc := range cases {
		if _, err := parseCron(tc); err == nil {
			t.Errorf("expected error for %q", tc)
		}
	}
}

func TestScheduler_AddRemoveList(t *testing.T) {
	s := NewScheduler(nil)
	job := ScheduledJob{
		ID:      "test-1",
		Name:    "test job",
		Cron:    "0 8 * * *",
		Prompt:  "do something",
		Enabled: true,
	}
	if err := s.AddJob(job); err != nil {
		t.Fatal(err)
	}
	jobs := s.ListJobs()
	if len(jobs) != 1 {
		t.Fatalf("want 1 job, got %d", len(jobs))
	}
	if jobs[0].Name != "test job" {
		t.Errorf("want 'test job', got %q", jobs[0].Name)
	}

	if err := s.AddJob(job); err == nil {
		t.Error("duplicate add should error")
	}

	if err := s.RemoveJob("test-1"); err != nil {
		t.Fatal(err)
	}
	if len(s.ListJobs()) != 0 {
		t.Error("want 0 jobs after remove")
	}

	if err := s.RemoveJob("nonexistent"); err == nil {
		t.Error("remove nonexistent should error")
	}
}

func TestScheduler_DueJobs(t *testing.T) {
	s := NewScheduler(nil)
	s.AddJob(ScheduledJob{
		ID: "j1", Name: "every minute", Cron: "* * * * *",
		Prompt: "run", Enabled: true,
	})
	s.AddJob(ScheduledJob{
		ID: "j2", Name: "disabled", Cron: "* * * * *",
		Prompt: "skip", Enabled: false,
	})

	now := time.Date(2026, 4, 21, 10, 30, 0, 0, time.UTC)
	due := s.dueJobs(now)
	if len(due) != 1 {
		t.Fatalf("want 1 due job, got %d", len(due))
	}
	if due[0].ID != "j1" {
		t.Errorf("want j1, got %s", due[0].ID)
	}

	// second call same minute: no duplicates
	due2 := s.dueJobs(now)
	if len(due2) != 0 {
		t.Errorf("want 0 due jobs on same minute, got %d", len(due2))
	}
}

func TestCronMatch_DayOfMonth(t *testing.T) {
	c, err := parseCron("0 0 15 * *")
	if err != nil {
		t.Fatal(err)
	}
	yes := time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC)
	no := time.Date(2026, 4, 16, 0, 0, 0, 0, time.UTC)
	if !c.matches(yes) {
		t.Error("should match 15th")
	}
	if c.matches(no) {
		t.Error("should not match 16th")
	}
}

func TestCronMatch_Month(t *testing.T) {
	c, err := parseCron("0 0 * 6 *")
	if err != nil {
		t.Fatal(err)
	}
	june := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	july := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	if !c.matches(june) {
		t.Error("should match June")
	}
	if c.matches(july) {
		t.Error("should not match July")
	}
}

func TestCronMatch_List(t *testing.T) {
	c, err := parseCron("0,30 * * * *")
	if err != nil {
		t.Fatal(err)
	}
	at0 := time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC)
	at30 := time.Date(2026, 4, 21, 10, 30, 0, 0, time.UTC)
	at15 := time.Date(2026, 4, 21, 10, 15, 0, 0, time.UTC)
	if !c.matches(at0) {
		t.Error("should match :00")
	}
	if !c.matches(at30) {
		t.Error("should match :30")
	}
	if c.matches(at15) {
		t.Error("should not match :15")
	}
}

func TestScheduler_OneShot_AutoDelete(t *testing.T) {
	fired := make(chan string, 5)
	s := NewScheduler(func(job ScheduledJob) {
		fired <- job.ID
	})
	s.AddJob(ScheduledJob{
		ID: "once", Name: "one-shot", Cron: "* * * * *",
		Prompt: "run once", Enabled: true, Recurring: false,
	})
	s.AddJob(ScheduledJob{
		ID: "repeat", Name: "recurring", Cron: "* * * * *",
		Prompt: "run always", Enabled: true, Recurring: true,
	})

	now := time.Date(2026, 4, 22, 10, 0, 0, 0, time.UTC)
	s.Tick(now)

	if len(s.ListJobs()) != 1 {
		t.Fatalf("want 1 job after tick (one-shot removed), got %d", len(s.ListJobs()))
	}
	if s.ListJobs()[0].ID != "repeat" {
		t.Errorf("want 'repeat' to survive, got %s", s.ListJobs()[0].ID)
	}
}

func TestScheduler_IntervalSeconds_FiresOnInterval(t *testing.T) {
	s := NewScheduler(nil)
	s.AddJob(ScheduledJob{
		ID: "poll", Name: "interval job", Cron: "",
		Prompt: "poll", Enabled: true, Recurring: true,
		IntervalSeconds: 15,
	})

	base := time.Date(2026, 4, 22, 10, 0, 0, 0, time.UTC)

	due := s.dueJobs(base)
	if len(due) != 1 {
		t.Fatalf("first tick should fire, got %d", len(due))
	}
	s.JobDone("poll")

	due = s.dueJobs(base.Add(5 * time.Second))
	if len(due) != 0 {
		t.Fatalf("5s later should not fire, got %d", len(due))
	}

	due = s.dueJobs(base.Add(15 * time.Second))
	if len(due) != 1 {
		t.Fatalf("15s later should fire, got %d", len(due))
	}
	s.JobDone("poll")

	due = s.dueJobs(base.Add(16 * time.Second))
	if len(due) != 0 {
		t.Fatalf("16s (1s since last) should not fire, got %d", len(due))
	}
}

func TestScheduler_IntervalSeconds_ZeroUsesCron(t *testing.T) {
	s := NewScheduler(nil)
	s.AddJob(ScheduledJob{
		ID: "cron", Name: "cron job", Cron: "* * * * *",
		Prompt: "run", Enabled: true, Recurring: true,
		IntervalSeconds: 0,
	})

	now := time.Date(2026, 4, 22, 10, 30, 0, 0, time.UTC)
	due := s.dueJobs(now)
	if len(due) != 1 {
		t.Fatalf("IntervalSeconds=0 should use cron, got %d due", len(due))
	}
}

func TestDueJobs_RunningExcluded(t *testing.T) {
	s := NewScheduler(nil)
	s.AddJob(ScheduledJob{
		ID: "j1", Name: "test", Enabled: true, Recurring: true,
		IntervalSeconds: 1,
	})
	s.running["j1"] = true
	now := time.Date(2026, 4, 22, 10, 0, 0, 0, time.UTC)
	due := s.dueJobs(now)
	if len(due) != 0 {
		t.Fatalf("expected 0 due jobs (running), got %d", len(due))
	}
}

func TestDueJobs_RunningClearedThenDue(t *testing.T) {
	s := NewScheduler(nil)
	s.AddJob(ScheduledJob{
		ID: "j1", Name: "test", Enabled: true, Recurring: true,
		IntervalSeconds: 1,
	})
	now := time.Date(2026, 4, 22, 10, 0, 0, 0, time.UTC)
	due1 := s.dueJobs(now)
	if len(due1) != 1 {
		t.Fatalf("expected 1 due job, got %d", len(due1))
	}
	if !s.IsRunning("j1") {
		t.Fatal("expected job to be marked running")
	}
	due2 := s.dueJobs(now)
	if len(due2) != 0 {
		t.Fatalf("expected 0 due jobs (still running), got %d", len(due2))
	}
	s.JobDone("j1")
	if s.IsRunning("j1") {
		t.Fatal("expected job to no longer be running after JobDone")
	}
	later := now.Add(2 * time.Second)
	due3 := s.dueJobs(later)
	if len(due3) != 1 {
		t.Fatalf("expected 1 due after done+interval, got %d", len(due3))
	}
}

func TestDueJobs_RunningGatesBeforeTimeCheck(t *testing.T) {
	s := NewScheduler(nil)
	s.AddJob(ScheduledJob{
		ID: "j1", Name: "test", Enabled: true, Recurring: true,
		IntervalSeconds: 5,
	})
	now := time.Date(2026, 4, 22, 10, 0, 0, 0, time.UTC)
	due1 := s.dueJobs(now)
	if len(due1) != 1 {
		t.Fatalf("expected 1 due, got %d", len(due1))
	}
	later := now.Add(10 * time.Second)
	due2 := s.dueJobs(later)
	if len(due2) != 0 {
		t.Fatalf("expected 0 due (still running despite interval), got %d", len(due2))
	}
	s.JobDone("j1")
	due3 := s.dueJobs(later.Add(1 * time.Second))
	if len(due3) != 1 {
		t.Fatalf("expected 1 due after done, got %d", len(due3))
	}
}

func TestDueJobs_MultipleJobsIndependentRunning(t *testing.T) {
	s := NewScheduler(nil)
	s.AddJob(ScheduledJob{
		ID: "a", Name: "alpha", Enabled: true, Recurring: true,
		IntervalSeconds: 1,
	})
	s.AddJob(ScheduledJob{
		ID: "b", Name: "beta", Enabled: true, Recurring: true,
		IntervalSeconds: 1,
	})
	now := time.Date(2026, 4, 22, 10, 0, 0, 0, time.UTC)
	due := s.dueJobs(now)
	if len(due) != 2 {
		t.Fatalf("expected 2 due, got %d", len(due))
	}
	if !s.IsRunning("a") || !s.IsRunning("b") {
		t.Fatal("expected both jobs running")
	}
	s.JobDone("a")
	later := now.Add(2 * time.Second)
	due2 := s.dueJobs(later)
	if len(due2) != 1 {
		t.Fatalf("expected 1 due (a released, b still running), got %d", len(due2))
	}
	if due2[0].ID != "a" {
		t.Fatalf("expected job a due, got %s", due2[0].ID)
	}
}

func TestNewScheduler_NilExecutor(t *testing.T) {
	s := NewScheduler(nil)
	if s == nil {
		t.Fatal("NewScheduler should not return nil")
	}
	_ = fmt.Sprintf("%v", s) // no panic
}
