package daemon

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)

type ScheduledJob struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Cron      string    `json:"cron"`
	Prompt    string    `json:"prompt"`
	Enabled   bool      `json:"enabled"`
	Recurring bool      `json:"recurring"`
	Mode      string    `json:"mode"` // "agent", "headless", or "bash"
	CapName     string  `json:"cap_name,omitempty"`
	ScriptName  string  `json:"script_name,omitempty"`
	AutoCorrect bool    `json:"auto_correct"`
	AllowedPorts string         `json:"allowed_ports,omitempty"`
	Sandbox      SandboxProfile `json:"sandbox"`
	IntervalSeconds int           `json:"interval_seconds,omitempty"`
	Model           string        `json:"model,omitempty"`
	Backend         string        `json:"backend,omitempty"`
	LastRun         time.Time     `json:"last_run,omitempty"`
	NextRun         time.Time     `json:"next_run,omitempty"`
}

type JobExecutor func(job ScheduledJob)

type Scheduler struct {
	mu       sync.Mutex
	jobs     map[string]*ScheduledJob
	crons    map[string]cronExpr
	lastFire map[string]time.Time
	running  map[string]bool
	executor JobExecutor
}

func NewScheduler(executor JobExecutor) *Scheduler {
	return &Scheduler{
		jobs:     make(map[string]*ScheduledJob),
		crons:    make(map[string]cronExpr),
		lastFire: make(map[string]time.Time),
		running:  make(map[string]bool),
		executor: executor,
	}
}

func (s *Scheduler) AddJob(job ScheduledJob) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.jobs[job.ID]; exists {
		return fmt.Errorf("job %q already exists", job.ID)
	}
	c, err := parseCron(job.Cron)
	if err != nil && job.IntervalSeconds <= 0 {
		return fmt.Errorf("invalid cron %q: %w", job.Cron, err)
	}
	s.jobs[job.ID] = &job
	if err == nil {
		s.crons[job.ID] = c
	}
	return nil
}

func (s *Scheduler) RemoveJob(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.jobs[id]; !exists {
		return fmt.Errorf("job %q not found", id)
	}
	delete(s.jobs, id)
	delete(s.crons, id)
	delete(s.lastFire, id)
	delete(s.running, id)
	return nil
}

func (s *Scheduler) JobDone(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.running, id)
}

func (s *Scheduler) IsRunning(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running[id]
}

func (s *Scheduler) ListJobs() []ScheduledJob {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]ScheduledJob, 0, len(s.jobs))
	for _, j := range s.jobs {
		result = append(result, *j)
	}
	return result
}

func (s *Scheduler) dueJobs(now time.Time) []ScheduledJob {
	s.mu.Lock()
	defer s.mu.Unlock()
	var due []ScheduledJob
	for id, job := range s.jobs {
		if !job.Enabled {
			continue
		}
		if s.running[id] {
			continue
		}
		if job.IntervalSeconds > 0 {
			interval := time.Duration(job.IntervalSeconds) * time.Second
			if last, ok := s.lastFire[id]; ok && now.Sub(last) < interval {
				continue
			}
			due = append(due, *job)
			s.lastFire[id] = now
			s.running[id] = true
			job.LastRun = now
			continue
		}
		truncated := now.Truncate(time.Minute)
		if last, ok := s.lastFire[id]; ok && !last.Before(truncated) {
			continue
		}
		if c, ok := s.crons[id]; ok && c.matches(truncated) {
			due = append(due, *job)
			s.lastFire[id] = truncated
			s.running[id] = true
			job.LastRun = truncated
		}
	}
	return due
}

func (s *Scheduler) Tick(now time.Time) {
	due := s.dueJobs(now)
	var removeIDs []string
	for _, job := range due {
		if s.executor != nil {
			go s.executor(job)
		}
		if !job.Recurring {
			removeIDs = append(removeIDs, job.ID)
		}
	}
	for _, id := range removeIDs {
		s.RemoveJob(id)
	}
}

// --- Cron parser ---

type cronField struct {
	values map[int]bool
}

func (f cronField) match(v int) bool {
	return f.values[v]
}

type cronExpr struct {
	minute, hour, dom, month, dow cronField
}

func (c cronExpr) matches(t time.Time) bool {
	return c.minute.match(t.Minute()) &&
		c.hour.match(t.Hour()) &&
		c.dom.match(t.Day()) &&
		c.month.match(int(t.Month())) &&
		c.dow.match(int(t.Weekday()))
}

func parseCron(expr string) (cronExpr, error) {
	parts := strings.Fields(expr)
	if len(parts) != 5 {
		return cronExpr{}, fmt.Errorf("need 5 fields, got %d", len(parts))
	}
	minute, err := parseField(parts[0], 0, 59)
	if err != nil {
		return cronExpr{}, fmt.Errorf("minute: %w", err)
	}
	hour, err := parseField(parts[1], 0, 23)
	if err != nil {
		return cronExpr{}, fmt.Errorf("hour: %w", err)
	}
	dom, err := parseField(parts[2], 1, 31)
	if err != nil {
		return cronExpr{}, fmt.Errorf("day-of-month: %w", err)
	}
	month, err := parseField(parts[3], 1, 12)
	if err != nil {
		return cronExpr{}, fmt.Errorf("month: %w", err)
	}
	dow, err := parseField(parts[4], 0, 6)
	if err != nil {
		return cronExpr{}, fmt.Errorf("day-of-week: %w", err)
	}
	return cronExpr{minute: minute, hour: hour, dom: dom, month: month, dow: dow}, nil
}

func parseField(field string, min, max int) (cronField, error) {
	values := map[int]bool{}

	for _, part := range strings.Split(field, ",") {
		part = strings.TrimSpace(part)
		if part == "*" {
			for i := min; i <= max; i++ {
				values[i] = true
			}
			continue
		}
		if strings.HasPrefix(part, "*/") {
			step, err := strconv.Atoi(part[2:])
			if err != nil || step <= 0 {
				return cronField{}, fmt.Errorf("invalid step %q", part)
			}
			for i := min; i <= max; i += step {
				values[i] = true
			}
			continue
		}
		if strings.Contains(part, "-") {
			bounds := strings.SplitN(part, "-", 2)
			lo, err1 := strconv.Atoi(bounds[0])
			hi, err2 := strconv.Atoi(bounds[1])
			if err1 != nil || err2 != nil || lo < min || hi > max || lo > hi {
				return cronField{}, fmt.Errorf("invalid range %q", part)
			}
			for i := lo; i <= hi; i++ {
				values[i] = true
			}
			continue
		}
		v, err := strconv.Atoi(part)
		if err != nil || v < min || v > max {
			return cronField{}, fmt.Errorf("invalid value %q (range %d-%d)", part, min, max)
		}
		values[v] = true
	}

	if len(values) == 0 {
		return cronField{}, fmt.Errorf("empty field")
	}
	return cronField{values: values}, nil
}
