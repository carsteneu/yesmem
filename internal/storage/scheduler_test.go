package storage

import (
	"testing"
	"time"
)

func TestScheduledJobCapName(t *testing.T) {
	s := newTestStore(t)
	defer s.Close()

	job := ScheduledJobRow{
		ID:          "test-bash-1",
		Name:        "test-cap-runner",
		Cron:        "*/5 * * * *",
		Mode:        "bash",
		CapName:     "proxy_health",
		AutoCorrect: true,
		Enabled:     true,
		Recurring:   true,
	}
	if err := s.SaveScheduledJob(job); err != nil {
		t.Fatalf("save: %v", err)
	}

	jobs, err := s.ListScheduledJobs()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var found bool
	for _, j := range jobs {
		if j.ID == "test-bash-1" {
			found = true
			if j.CapName != "proxy_health" {
				t.Errorf("cap_name = %q, want %q", j.CapName, "proxy_health")
			}
			if j.Mode != "bash" {
				t.Errorf("mode = %q, want %q", j.Mode, "bash")
			}
			if !j.AutoCorrect {
				t.Error("auto_correct should be true")
			}
		}
	}
	if !found {
		t.Error("job not found after save")
	}
}

func TestBashJobRunStorage(t *testing.T) {
	s := newTestStore(t)
	defer s.Close()

	run := BashJobRun{
		JobID:    "test-job-1",
		JobName:  "broken-script",
		CapName:  "test_cap",
		Command:  "exit 1",
		Status:   "error",
		ExitCode: 1,
		Output:   "some output",
		ErrorMsg: "exit status 1",
	}
	if err := s.SaveBashJobRun(run); err != nil {
		t.Fatalf("save: %v", err)
	}

	runs, err := s.GetBashJobRuns("test-job-1", 10)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("expected 1 run, got %d", len(runs))
	}
	if runs[0].Status != "error" || runs[0].ExitCode != 1 {
		t.Errorf("unexpected run: %+v", runs[0])
	}
	if runs[0].Processed {
		t.Error("new run should not be processed")
	}

	errors, err := s.GetUnprocessedBashErrors(10)
	if err != nil {
		t.Fatalf("get errors: %v", err)
	}
	if len(errors) != 1 {
		t.Fatalf("expected 1 error, got %d", len(errors))
	}

	if err := s.MarkBashJobRunProcessed(errors[0].ID); err != nil {
		t.Fatalf("mark: %v", err)
	}

	errors2, _ := s.GetUnprocessedBashErrors(10)
	if len(errors2) != 0 {
		t.Errorf("expected 0 unprocessed, got %d", len(errors2))
	}
}

func TestPurgeBashJobRunsRetention(t *testing.T) {
	s := newTestStore(t)
	defer s.Close()

	now := time.Now()
	runs := []struct {
		name      string
		status    string
		processed bool
		createdAt time.Time
	}{
		{name: "old success", status: "ok", createdAt: now.Add(-25 * time.Hour)},
		{name: "boundary success", status: "ok", createdAt: now.Add(-24 * time.Hour)},
		{name: "recent success", status: "ok", createdAt: now.Add(-23 * time.Hour)},
		{name: "old processed error", status: "error", processed: true, createdAt: now.Add(-31 * 24 * time.Hour)},
		{name: "boundary processed error", status: "error", processed: true, createdAt: now.Add(-30 * 24 * time.Hour)},
		{name: "recent processed error", status: "error", processed: true, createdAt: now.Add(-29 * 24 * time.Hour)},
		{name: "old pending error", status: "error", createdAt: now.Add(-90 * 24 * time.Hour)},
	}
	for _, run := range runs {
		result, err := s.DB().Exec(`INSERT INTO bash_job_runs
			(job_id, job_name, command, status, processed, created_at)
			VALUES (?, ?, '', ?, ?, ?)`, run.name, run.name, run.status, run.processed,
			run.createdAt.UTC().Format(sqliteDateTimeFormat))
		if err != nil {
			t.Fatalf("insert %s: %v", run.name, err)
		}
		if _, err := result.LastInsertId(); err != nil {
			t.Fatalf("insert id %s: %v", run.name, err)
		}
	}

	deleted, err := s.PurgeBashJobRuns(now.Add(-24*time.Hour), now.Add(-30*24*time.Hour))
	if err != nil {
		t.Fatalf("purge: %v", err)
	}
	if deleted != 2 {
		t.Fatalf("deleted = %d, want 2", deleted)
	}

	rows, err := s.DB().Query(`SELECT job_name FROM bash_job_runs ORDER BY job_name`)
	if err != nil {
		t.Fatalf("query remaining: %v", err)
	}
	defer rows.Close()
	remaining := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan remaining: %v", err)
		}
		remaining[name] = true
	}
	for _, want := range []string{
		"boundary success", "boundary processed error",
		"recent success", "recent processed error",
		"old pending error",
	} {
		if !remaining[want] {
			t.Errorf("expected %q to remain", want)
		}
	}
}

func TestScheduledJobScriptName(t *testing.T) {
	s := newTestStore(t)
	defer s.Close()

	job := ScheduledJobRow{
		ID:         "test-bundle-1",
		Name:       "telegram-poll-runner",
		Cron:       "*/13 * * * * *",
		Mode:       "bash",
		CapName:    "telegram",
		ScriptName: "telegram_poll",
		Enabled:    true,
		Recurring:  true,
	}
	if err := s.SaveScheduledJob(job); err != nil {
		t.Fatalf("save: %v", err)
	}

	jobs, err := s.ListScheduledJobs()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var found bool
	for _, j := range jobs {
		if j.ID == "test-bundle-1" {
			found = true
			if j.ScriptName != "telegram_poll" {
				t.Errorf("script_name = %q, want %q", j.ScriptName, "telegram_poll")
			}
			if j.CapName != "telegram" {
				t.Errorf("cap_name = %q, want %q", j.CapName, "telegram")
			}
		}
	}
	if !found {
		t.Error("job not found after save")
	}
}

func TestBashJobRunScriptName(t *testing.T) {
	s := newTestStore(t)
	defer s.Close()

	run := BashJobRun{
		JobID:      "telegram-poll-1",
		JobName:    "telegram-poll-runner",
		CapName:    "telegram",
		ScriptName: "telegram_poll",
		Command:    "echo poll",
		Status:     "ok",
		ExitCode:   0,
	}
	if err := s.SaveBashJobRun(run); err != nil {
		t.Fatalf("save: %v", err)
	}

	runs, err := s.GetBashJobRuns("telegram-poll-1", 10)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("expected 1 run, got %d", len(runs))
	}
	if runs[0].ScriptName != "telegram_poll" {
		t.Errorf("script_name = %q, want %q", runs[0].ScriptName, "telegram_poll")
	}
	if runs[0].CapName != "telegram" {
		t.Errorf("cap_name = %q, want %q", runs[0].CapName, "telegram")
	}
}
