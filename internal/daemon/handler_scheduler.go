package daemon

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"os/exec"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/carsteneu/yesmem/internal/capfile"
	"github.com/carsteneu/yesmem/internal/storage"
)

func (h *Handler) handleSchedule(params map[string]any) Response {
	action := stringOr(params, "action", "")
	switch action {
	case "create":
		return h.scheduleCreate(params)
	case "list":
		return h.scheduleList(params)
	case "delete":
		return h.scheduleDelete(params)
	case "run":
		return h.scheduleRun(params)
	default:
		return errorResponse(fmt.Sprintf("schedule: unknown action %q (create|list|delete|run)", action))
	}
}

func (h *Handler) scheduleCreate(params map[string]any) Response {
	name := stringOr(params, "name", "")
	cron := stringOr(params, "cron", "")
	prompt := stringOr(params, "prompt", "")
	mode := stringOr(params, "mode", "agent")
	capName := stringOr(params, "cap_name", "")
	scriptName := stringOr(params, "script_name", "")
	allowedPorts := stringOr(params, "allowed_ports", "80,443")
	autoCorrect := true
	if v, ok := params["auto_correct"].(bool); ok {
		autoCorrect = v
	}
	enabled := true
	if v, ok := params["enabled"].(bool); ok {
		enabled = v
	}
	recurring := true
	if v, ok := params["recurring"].(bool); ok {
		recurring = v
	}
	intervalSeconds := intOr(params, "interval_seconds", 0)
	model := stringOr(params, "model", "")

	if mode != "agent" && mode != "headless" && mode != "bash" {
		return errorResponse(fmt.Sprintf("schedule create: mode must be 'agent', 'headless', or 'bash', got %q", mode))
	}

	if mode == "bash" && capName == "" && prompt == "" {
		return errorResponse("schedule create: bash mode requires 'cap_name' or 'prompt'")
	}

	if name == "" || (cron == "" && intervalSeconds <= 0) {
		return errorResponse("schedule create: name and (cron or interval_seconds) are required")
	}
	if mode != "bash" && prompt == "" {
		return errorResponse("schedule create: prompt is required for agent/headless mode")
	}

	sandboxStr := stringOr(params, "sandbox", "")
	defaultProfile := h.defaultSandboxProfile
	if defaultProfile == ProfileNone {
		defaultProfile = ProfileStandard
	}
	sandbox, err := resolveJobSandbox(sandboxStr, defaultProfile)
	if err != nil {
		return errorResponse(fmt.Sprintf("schedule create: %v", err))
	}

	id := fmt.Sprintf("sched-%s-%d", name, time.Now().UnixMilli()%100000)

	if cron != "" {
		if _, err := parseCron(cron); err != nil {
			return errorResponse(fmt.Sprintf("schedule create: invalid cron %q: %v", cron, err))
		}
	}

	job := ScheduledJob{
		ID: id, Name: name, Cron: cron, Prompt: prompt, Enabled: enabled, Recurring: recurring, Mode: mode,
		CapName: capName, ScriptName: scriptName, AutoCorrect: autoCorrect, AllowedPorts: allowedPorts,
		Sandbox: sandbox, IntervalSeconds: intervalSeconds, Model: model,
	}
	if h.scheduler != nil {
		if err := h.scheduler.AddJob(job); err != nil {
			return errorResponse(fmt.Sprintf("schedule create: %v", err))
		}
	}

	row := storage.ScheduledJobRow{
		ID: id, Name: name, Cron: cron, Prompt: prompt, Enabled: enabled, Recurring: recurring, Mode: mode,
		CapName: capName, ScriptName: scriptName, AutoCorrect: autoCorrect, AllowedPorts: allowedPorts,
		Sandbox: sandbox.String(), IntervalSeconds: intervalSeconds, Model: model,
		CreatedAt: time.Now(),
	}
	if err := h.store.SaveScheduledJob(row); err != nil {
		return errorResponse(fmt.Sprintf("schedule create: db: %v", err))
	}

	return jsonResponse(map[string]any{"id": id, "name": name, "cron": cron, "mode": mode, "cap_name": capName, "script_name": scriptName, "auto_correct": autoCorrect, "allowed_ports": allowedPorts, "status": "created"})
}

func (h *Handler) scheduleList(params map[string]any) Response {
	var jobs []ScheduledJob
	if h.scheduler != nil {
		jobs = h.scheduler.ListJobs()
	}
	result := make([]map[string]any, 0, len(jobs))
	for _, j := range jobs {
		entry := map[string]any{
			"id":            j.ID,
			"name":          j.Name,
			"cron":          j.Cron,
			"prompt":        j.Prompt,
			"enabled":       j.Enabled,
			"mode":          j.Mode,
			"cap_name":      j.CapName,
			"script_name":   j.ScriptName,
			"auto_correct":  j.AutoCorrect,
			"allowed_ports": j.AllowedPorts,
			"sandbox":       j.Sandbox.String(),
		}
		if !j.LastRun.IsZero() {
			entry["last_run"] = j.LastRun.Format(time.RFC3339)
		}
		result = append(result, entry)
	}
	return jsonResponse(map[string]any{"jobs": result, "count": len(result)})
}

func (h *Handler) scheduleDelete(params map[string]any) Response {
	id := stringOr(params, "id", "")
	if id == "" {
		return errorResponse("schedule delete: id is required")
	}

	if h.scheduler != nil {
		if err := h.scheduler.RemoveJob(id); err != nil {
			return errorResponse(fmt.Sprintf("schedule delete: %v", err))
		}
	}

	if err := h.store.DeleteScheduledJob(id); err != nil && err != storage.ErrNotFound {
		return errorResponse(fmt.Sprintf("schedule delete: db: %v", err))
	}

	h.headlessSessionsMu.Lock()
	delete(h.headlessSessions, id)
	h.headlessSessionsMu.Unlock()

	return jsonResponse(map[string]any{"id": id, "status": "deleted"})
}

func (h *Handler) scheduleRun(params map[string]any) Response {
	id := stringOr(params, "id", "")
	prompt := stringOr(params, "prompt", "")

	if id == "" && prompt == "" {
		return errorResponse("schedule run: id or prompt required")
	}

	if id != "" && h.scheduler != nil {
		for _, j := range h.scheduler.ListJobs() {
			if j.ID == id {
				go h.executeScheduledPrompt(j)
				return jsonResponse(map[string]any{"status": "running", "id": id, "mode": j.Mode})
			}
		}
		return errorResponse(fmt.Sprintf("schedule run: job %q not found", id))
	}

	go h.executeScheduledPrompt(ScheduledJob{
		ID: id, Name: "manual-run", Prompt: prompt, Mode: stringOr(params, "mode", "agent"),
	})

	return jsonResponse(map[string]any{"status": "running", "prompt": prompt})
}

func (h *Handler) executeScheduledPrompt(job ScheduledJob) {
	if job.Mode == "bash" {
		h.fireJobBash(&job)
		return
	}
	if job.Mode == "headless" {
		h.executeHeadless(job)
		return
	}
	h.executeAgent(job)
}

func headlessArgs(sessionID, model string) []string {
	args := []string{"-p", "--output-format", "json", "--verbose", "--max-turns", "3"}
	if model != "" {
		args = append(args, "--model", model)
	}
	if sessionID != "" {
		args = append(args, "--resume", sessionID)
	}
	return args
}

func parseHeadlessOutput(output []byte) (text string, sessionID string) {
	var obj struct {
		Type      string `json:"type"`
		Result    string `json:"result"`
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(output, &obj); err == nil && obj.Type == "result" {
		return obj.Result, obj.SessionID
	}

	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if err := json.Unmarshal([]byte(lines[i]), &obj); err == nil && obj.Type == "result" {
			return obj.Result, obj.SessionID
		}
	}

	return strings.TrimSpace(string(output)), ""
}

func (h *Handler) executeHeadless(job ScheduledJob) {
	section := "sched-" + job.Name
	wrappedPrompt := fmt.Sprintf("You are a scheduled task agent. Job: %s (ID: %s). Your ONLY job is:\n\n%s\n\nExecute ONLY this task. No orientation, no health checks, no file modifications. When done, say DONE and stop.", job.Name, job.ID, job.Prompt)

	h.headlessSessionsMu.Lock()
	sessionID := h.headlessSessions[job.ID]
	h.headlessSessionsMu.Unlock()
	args := headlessArgs(sessionID, job.Model)

	sb := NewSandbox(SandboxConfig{
		Enabled:             true,
		FallbackUnsandboxed: true,
		AllowedPorts:        parsePorts(job.AllowedPorts),
	})
	bin, wrappedArgs := sb.WrapExecArgs("claude", args, job.Sandbox)

	log.Printf("[scheduler] headless run for job %s (resume=%v, sandbox=%s)", job.ID, sessionID != "", job.Sandbox)
	cmd := exec.Command(bin, wrappedArgs...)
	cmd.Dir = h.jobWorkDir()
	cmd.Stdin = strings.NewReader(wrappedPrompt)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		log.Printf("[scheduler] headless job %s failed: %v (stderr: %s)", job.ID, err, h.redact(truncatStr(stderr.String(), 500)))
	}

	text, newSessionID := parseHeadlessOutput(stdout.Bytes())
	h.headlessSessionsMu.Lock()
	if newSessionID != "" {
		h.headlessSessions[job.ID] = newSessionID
	}
	sessionForLog := h.headlessSessions[job.ID]
	h.headlessSessionsMu.Unlock()

	result := h.redact(truncatStr(text, 10000))
	h.handleScratchpadWrite(map[string]any{
		"project": "yesmem",
		"section": section,
		"content": fmt.Sprintf("## Headless Result [%s]\n\n%s", job.Name, result),
	})
	log.Printf("[scheduler] headless job %s done (%d bytes, session=%s)", job.ID, stdout.Len(), sessionForLog)
}

func (h *Handler) executeAgent(job ScheduledJob) {
	section := "sched-" + job.Name

	h.handleStopAgent(map[string]any{
		"to":      section,
		"project": "yesmem",
	})

	taskContent := fmt.Sprintf("## SCHEDULED TASK [%s]\nJob-ID: %s\n\n", job.Name, job.ID) +
		"You are a scheduled task agent. Your ONLY job is:\n\n" +
		job.Prompt + "\n\n" +
		"Execute ONLY this task. No orientation, no health checks, no file modifications.\n" +
		"When done: write your result to this scratchpad section and stop.\n" +
		"If no task is found: write DONE and stop immediately."
	h.handleScratchpadWrite(map[string]any{
		"project": "yesmem",
		"section": section,
		"content": taskContent,
	})

	resp := h.handleSpawnAgent(map[string]any{
		"project":  "yesmem",
		"section":  section,
		"work_dir": h.jobWorkDir(),
	})
	if resp.Error != "" {
		log.Printf("[scheduler] spawn failed: %s", resp.Error)
		return
	}
	log.Printf("[scheduler] agent spawned for job %s (section=%s)", job.ID, section)
	wrappedPrompt := fmt.Sprintf("You are a scheduled task agent. Job: %s (ID: %s). Your ONLY job is:\n\n%s\n\nExecute ONLY this task. No orientation, no health checks, no file modifications. When done, say DONE and stop.", job.Name, job.ID, job.Prompt)
	h.handleRelayAgent(map[string]any{
		"to":      section,
		"content": wrappedPrompt,
		"project": "yesmem",
	})
	h.handleRelayAgent(map[string]any{
		"to":      section,
		"content": "",
		"project": "yesmem",
	})

	go h.watchScheduledAgent(section)
}

func (h *Handler) watchScheduledAgent(section string) {
	const idleTimeout = 10 * time.Minute
	const pollInterval = 15 * time.Second

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for range ticker.C {
		resp := h.handleGetAgent(map[string]any{
			"to":      section,
			"project": "yesmem",
		})
		if resp.Error != "" {
			log.Printf("[scheduler] agent %s gone: %s", section, resp.Error)
			return
		}
		var data map[string]any
		if err := json.Unmarshal(resp.Result, &data); err != nil {
			continue
		}
		status, _ := data["status"].(string)
		if status == "stopped" || status == "exited" {
			log.Printf("[scheduler] agent %s finished (status=%s)", section, status)
			return
		}
		lastStr, _ := data["last_activity_at"].(string)
		if lastStr == "" {
			continue
		}
		last, err := time.Parse("2006-01-02 15:04:05", lastStr)
		if err != nil {
			continue
		}
		idle := time.Since(last)
		if idle > idleTimeout {
			log.Printf("[scheduler] agent %s idle for %v (status=%s), stopping", section, idle.Round(time.Second), status)
			h.handleStopAgent(map[string]any{
				"to":      section,
				"project": "yesmem",
			})
			return
		}
	}
}

func prepareBashCommand(command string) string {
	if !strings.Contains(command, "store ") && !strings.Contains(command, "store'") && !strings.Contains(command, "llm ") {
		return command
	}
	return capfile.GenerateAdapterBash() + command
}

func (h *Handler) fireJobBash(job *ScheduledJob) {
	log.Printf("[scheduler] bash run for job %s (cap=%s)", job.ID, job.CapName)

	command, scriptSandbox, err := h.resolveBashCommand(job)
	if err != nil {
		h.storeBashRun(job, "", "", err, -1)
		return
	}

	command = prepareBashCommand(command)

	sb := NewSandbox(SandboxConfig{
		Enabled:             true,
		FallbackUnsandboxed: true,
		AllowedPorts:        parsePorts(job.AllowedPorts),
	})
	timeout := 20
	if strings.Contains(command, "claude ") || strings.Contains(command, "llm ") {
		timeout = 600
	}

	effectiveProfile := job.Sandbox
	if scriptSandbox != "" {
		profile, perr := ParseSandboxProfile(scriptSandbox)
		if perr != nil {
			log.Printf("[scheduler] cap=%s script=%s invalid sandbox %q, using job profile: %v",
				job.CapName, job.ScriptName, scriptSandbox, perr)
		} else {
			if profile != job.Sandbox {
				log.Printf("[scheduler] cap=%s script=%s sandbox override: job=%s -> script=%s",
					job.CapName, job.ScriptName, job.Sandbox.String(), profile.String())
			}
			effectiveProfile = profile
		}
	}

	output, exitCode, runErr := sb.RunWithProfile(command, timeout, effectiveProfile)

	h.storeBashRun(job, command, output, runErr, exitCode)

	if runErr != nil {
		log.Printf("[scheduler] bash job %s FAILED (exit=%d): %v", job.ID, exitCode, runErr)
	} else {
		log.Printf("[scheduler] bash job %s OK (%d bytes)", job.ID, len(output))
	}
}

func (h *Handler) resolveBashCommand(job *ScheduledJob) (string, string, error) {
	if job.CapName != "" {
		caps, err := h.store.GetActiveLearnings("cap", "", "", "", 0)
		if err != nil {
			return "", "", fmt.Errorf("cap lookup: %w", err)
		}
		for _, l := range caps {
			meta, mErr := ParseCapMeta(l.Context)
			if mErr != nil {
				continue
			}
			if meta.Name == job.CapName {
				if body, sandbox, ok := findBashScript(&meta, job.ScriptName); ok {
					return body, sandbox, nil
				}
				if job.ScriptName != "" {
					return "", "", fmt.Errorf("cap %q has no bash script named %q", job.CapName, job.ScriptName)
				}
				return "", "", fmt.Errorf("cap %q has no bash script", job.CapName)
			}
		}
		return "", "", fmt.Errorf("cap %q not found", job.CapName)
	}
	if job.Prompt != "" {
		return job.Prompt, "", nil
	}
	return "", "", fmt.Errorf("bash job has neither cap_name nor prompt")
}

func (h *Handler) storeBashRun(job *ScheduledJob, command, output string, err error, exitCode int) {
	status := "ok"
	errMsg := ""
	if err != nil {
		status = "error"
		errMsg = err.Error()
	}
	if status == "ok" && len(output) == 0 {
		return
	}
	if dbErr := h.store.SaveBashJobRun(storage.BashJobRun{
		JobID:      job.ID,
		JobName:    job.Name,
		CapName:    job.CapName,
		ScriptName: job.ScriptName,
		Command:    h.redact(command),
		Status:     status,
		ExitCode:   exitCode,
		Output:     h.redact(truncatStr(output, 10000)),
		ErrorMsg:   h.redact(errMsg),
	}); dbErr != nil {
		log.Printf("[scheduler] failed to save bash run for job %s: %v", job.ID, dbErr)
	}
}

func truncatStr(s string, max int) string {
	if len(s) <= max {
		return s
	}
	truncated := s[:max]
	for len(truncated) > 0 && !utf8.Valid([]byte(truncated)) {
		truncated = truncated[:len(truncated)-1]
	}
	return truncated + "..."
}

func resolveJobSandbox(param string, defaultProfile SandboxProfile) (SandboxProfile, error) {
	if param == "" {
		return defaultProfile, nil
	}
	return ParseSandboxProfile(param)
}

func parsePorts(s string) []int {
	if s == "" {
		return []int{80, 443}
	}
	var ports []int
	for _, part := range strings.Split(s, ",") {
		p, err := strconv.Atoi(strings.TrimSpace(part))
		if err == nil && p > 0 && p <= 65535 {
			ports = append(ports, p)
		}
	}
	if len(ports) == 0 {
		return []int{80, 443}
	}
	return ports
}

func (h *Handler) jobWorkDir() string {
	return h.resolveAgentWorkDir("yesmem", "", "claude")
}
