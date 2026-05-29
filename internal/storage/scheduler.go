package storage

import (
	"database/sql"
	"time"
)

type ScheduledJobRow struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Cron        string    `json:"cron"`
	Prompt      string    `json:"prompt"`
	Enabled     bool      `json:"enabled"`
	Recurring   bool      `json:"recurring"`
	Mode        string    `json:"mode"`
	CapName     string    `json:"cap_name"`
	ScriptName  string    `json:"script_name"`
	AutoCorrect bool      `json:"auto_correct"`
	AllowedPorts string   `json:"allowed_ports"`
	Sandbox      string   `json:"sandbox"`
	IntervalSeconds int       `json:"interval_seconds"`
	Model           string    `json:"model"`
	Backend         string    `json:"backend"`
	LastRun         time.Time `json:"last_run,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
}

func (s *Store) SaveScheduledJob(job ScheduledJobRow) error {
	mode := job.Mode
	if mode == "" {
		mode = "agent"
	}
	_, err := s.db.Exec(
		`INSERT OR REPLACE INTO scheduled_jobs (id, name, cron, prompt, enabled, recurring, mode, cap_name, script_name, auto_correct, allowed_ports, sandbox, interval_seconds, model, backend, last_run, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		job.ID, job.Name, job.Cron, job.Prompt, job.Enabled, job.Recurring, mode, job.CapName, job.ScriptName, job.AutoCorrect, job.AllowedPorts, job.Sandbox, job.IntervalSeconds, job.Model, job.Backend, job.LastRun, job.CreatedAt,
	)
	return err
}

func (s *Store) DeleteScheduledJob(id string) error {
	res, err := s.db.Exec(`DELETE FROM scheduled_jobs WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) ListScheduledJobs() ([]ScheduledJobRow, error) {
	rows, err := s.db.Query(`SELECT id, name, cron, prompt, enabled, COALESCE(recurring, 1), COALESCE(mode, 'agent'), COALESCE(cap_name, ''), COALESCE(script_name, ''), COALESCE(auto_correct, 1), COALESCE(allowed_ports, '80,443'), COALESCE(sandbox, 'standard'), COALESCE(interval_seconds, 0), COALESCE(model, ''), COALESCE(backend, ''), COALESCE(last_run, ''), created_at FROM scheduled_jobs ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var jobs []ScheduledJobRow
	for rows.Next() {
		var j ScheduledJobRow
		var lastRun string
		var autoCorrect int
		if err := rows.Scan(&j.ID, &j.Name, &j.Cron, &j.Prompt, &j.Enabled, &j.Recurring, &j.Mode, &j.CapName, &j.ScriptName, &autoCorrect, &j.AllowedPorts, &j.Sandbox, &j.IntervalSeconds, &j.Model, &j.Backend, &lastRun, &j.CreatedAt); err != nil {
			continue
		}
		if lastRun != "" {
			j.LastRun, _ = time.Parse(time.RFC3339, lastRun)
		}
		j.AutoCorrect = autoCorrect == 1
		jobs = append(jobs, j)
	}
	return jobs, nil
}

func (s *Store) UpdateJobLastRun(id string, t time.Time) error {
	_, err := s.db.Exec(`UPDATE scheduled_jobs SET last_run = ? WHERE id = ?`, t, id)
	return err
}

type BashJobRun struct {
	ID        int64     `json:"id"`
	JobID     string    `json:"job_id"`
	JobName   string    `json:"job_name"`
	CapName    string    `json:"cap_name"`
	ScriptName string    `json:"script_name"`
	Command    string    `json:"command"`
	Status    string    `json:"status"`
	ExitCode  int       `json:"exit_code"`
	Output    string    `json:"output"`
	ErrorMsg  string    `json:"error_msg"`
	Processed bool      `json:"processed"`
	CreatedAt time.Time `json:"created_at"`
}

func (s *Store) SaveBashJobRun(run BashJobRun) error {
	_, err := s.db.Exec(`INSERT INTO bash_job_runs (job_id, job_name, cap_name, script_name, command, status, exit_code, output, error_msg)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		run.JobID, run.JobName, run.CapName, run.ScriptName, run.Command,
		run.Status, run.ExitCode, run.Output, run.ErrorMsg)
	return err
}

func (s *Store) GetBashJobRuns(jobID string, limit int) ([]BashJobRun, error) {
	rows, err := s.db.Query(`SELECT id, job_id, job_name, cap_name, COALESCE(script_name, ''), command, status, exit_code, output, error_msg, processed, created_at
		FROM bash_job_runs WHERE job_id = ? ORDER BY created_at DESC LIMIT ?`, jobID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanBashJobRuns(rows)
}

func (s *Store) GetUnprocessedBashErrors(limit int) ([]BashJobRun, error) {
	rows, err := s.db.Query(`SELECT id, job_id, job_name, cap_name, COALESCE(script_name, ''), command, status, exit_code, output, error_msg, processed, created_at
		FROM bash_job_runs WHERE status = 'error' AND processed = 0 ORDER BY created_at ASC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanBashJobRuns(rows)
}

func (s *Store) MarkBashJobRunProcessed(id int64) error {
	_, err := s.db.Exec(`UPDATE bash_job_runs SET processed = 1 WHERE id = ?`, id)
	return err
}

func scanBashJobRuns(rows *sql.Rows) ([]BashJobRun, error) {
	var runs []BashJobRun
	for rows.Next() {
		var r BashJobRun
		var proc int
		if err := rows.Scan(&r.ID, &r.JobID, &r.JobName, &r.CapName, &r.ScriptName, &r.Command,
			&r.Status, &r.ExitCode, &r.Output, &r.ErrorMsg, &proc, &r.CreatedAt); err != nil {
			return nil, err
		}
		r.Processed = proc == 1
		runs = append(runs, r)
	}
	return runs, nil
}
