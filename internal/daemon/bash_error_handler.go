package daemon

import (
	"context"
	"fmt"
	"log"
	"os/exec"
	"strings"
	"time"

	"github.com/carsteneu/yesmem/internal/models"
	"github.com/carsteneu/yesmem/internal/storage"
)

// Auto-correct rate limiting (T4): bash-job auto-correct is rate-limited so
// a cap with a persistent bug can't burn through the LLM budget. Two gates,
// checked in priority order:
//
//  1. per-cap cooldown — once an attempt has fired for a cap, further attempts
//     on that same cap are skipped until cooldown expires.
//  2. per-tick budget — at most autoCorrectMaxPerTick attempts per call to
//     processBashJobErrors, even across different caps.
//
// Both gates emit a broadcast containing the word "cooldown" so external
// listeners can react uniformly.
const (
	autoCorrectCooldownDuration     = 5 * time.Minute
	autoCorrectMaxPerTick           = 1
	autoCorrectMaxGenerationsPerDay = 3
	autoCorrectGenerationWindow     = 24 * time.Hour
	autoCorrectDefaultTestTimeout   = 10
)

// autoCorrectTestRunTimeout returns the timeout (in seconds) used for the
// auto-correct test-run as a function of the job's IntervalSeconds. Sub-minute
// intervals get a tight timeout matching the interval so a fast-poll job is
// never tested longer than its own cadence; everything else (zero, negative,
// or >= 60) falls back to the default so a long-cron job does not produce a
// minute-long test-run.
func autoCorrectTestRunTimeout(intervalSec int) int {
	if intervalSec > 0 && intervalSec < 60 {
		return intervalSec
	}
	return autoCorrectDefaultTestTimeout
}

func (h *Handler) processBashJobErrors() {
	errors, err := h.store.GetUnprocessedBashErrors(5)
	if err != nil || len(errors) == 0 {
		return
	}

	perTickCount := 0

	for _, run := range errors {
		job := h.findScheduledJob(run.JobID)
		autoCorrect := job != nil && job.AutoCorrect
		ports := "80,443"
		if job != nil && job.AllowedPorts != "" {
			ports = job.AllowedPorts
		}

		handled := true
		if autoCorrect && run.CapName != "" {
			if proceed, reason := h.tryStartAutoCorrect(run.CapName, perTickCount); proceed {
				perTickCount++
				h.autoCorrectBashCap(run, job, ports)
				h.finishAutoCorrect()
			} else {
				h.broadcastBashError("yesmem", fmt.Sprintf(
					"[bash-job] %q FAILED, auto-correct skipped (%s)\nError: %s",
					run.JobName, reason, run.ErrorMsg))
			}
		} else {
			handled = h.diagnoseBashError(run)
		}

		if handled {
			if err := h.store.MarkBashJobRunProcessed(run.ID); err != nil {
				log.Printf("[bash-errors] failed to mark run %d processed: %v", run.ID, err)
			}
		}
	}
}

// tryStartAutoCorrect atomically checks rate-limit gates and reserves the
// auto-correct slot for the named cap. On (true, "") the caller MUST run
// autoCorrectBashCap and then call finishAutoCorrect. On (false, reason)
// the caller MUST broadcast a "cooldown" skip message; the run is still
// marked processed by the surrounding loop.
func (h *Handler) tryStartAutoCorrect(capName string, perTickCount int) (proceed bool, reason string) {
	h.autoCorrectMu.Lock()
	defer h.autoCorrectMu.Unlock()

	now := time.Now()
	if until, ok := h.autoCorrectCooldown[capName]; ok && until.After(now) {
		return false, fmt.Sprintf("cooldown until %s", until.Format(time.RFC3339))
	}
	if gens, err := h.store.CountAutoCorrectGenerations(capName, now.Add(-autoCorrectGenerationWindow)); err == nil && gens >= autoCorrectMaxGenerationsPerDay {
		return false, fmt.Sprintf("cooldown: generation limit %d/24h reached", autoCorrectMaxGenerationsPerDay)
	}
	if perTickCount >= autoCorrectMaxPerTick {
		return false, fmt.Sprintf("cooldown: per-tick budget %d exhausted", autoCorrectMaxPerTick)
	}
	if h.autoCorrectRunning {
		return false, "cooldown: another auto-correct in flight"
	}
	h.autoCorrectRunning = true
	h.autoCorrectCooldown[capName] = now.Add(autoCorrectCooldownDuration)
	return true, ""
}

// finishAutoCorrect releases the cross-tick semaphore reserved by
// tryStartAutoCorrect. The per-cap cooldown timestamp set during reservation
// is intentionally NOT cleared here — that is what rate-limits the next tick.
func (h *Handler) finishAutoCorrect() {
	h.autoCorrectMu.Lock()
	h.autoCorrectRunning = false
	h.autoCorrectMu.Unlock()
}

func (h *Handler) diagnoseBashError(run storage.BashJobRun) bool {
	prompt := fmt.Sprintf(
		"A scheduled bash job %q failed. Diagnose briefly (3 sentences max).\n\nCap: %s\nCommand: %s\nExit: %d\nError: %s\nOutput:\n%s",
		run.JobName, run.CapName, run.Command, run.ExitCode, run.ErrorMsg, truncatStr(run.Output, 2000))

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "claude", "--bare", "-p", prompt, "--model", "haiku", "--max-turns", "1")
	diagOut, err := cmd.CombinedOutput()
	if err != nil {
		log.Printf("[bash-errors] diagnosis for %s failed: %v", run.JobID, err)
		h.broadcastBashError("yesmem", fmt.Sprintf("[bash-job] %q FAILED (exit %d): %s", run.JobName, run.ExitCode, run.ErrorMsg))
		return false
	}

	h.broadcastBashError("yesmem", fmt.Sprintf("[bash-job] %q FAILED (exit %d)\nDiagnosis: %s", run.JobName, run.ExitCode, truncatStr(string(diagOut), 1000)))
	return true
}

func (h *Handler) autoCorrectBashCap(run storage.BashJobRun, job *ScheduledJob, allowedPorts string) {
	caps, err := h.store.GetActiveLearnings("cap", "", "", "", 0)
	if err != nil {
		h.diagnoseBashError(run)
		return
	}

	var currentHandler string
	var activeCap *models.Learning
	for i := range caps {
		meta, mErr := ParseCapMeta(caps[i].Context)
		if mErr != nil {
			continue
		}
		if meta.Name == run.CapName {
			if body, _, ok := findBashScript(&meta, run.ScriptName); ok {
				currentHandler = body
				activeCap = &caps[i]
			}
			break
		}
	}
	if currentHandler == "" {
		h.diagnoseBashError(run)
		return
	}

	sb := NewSandbox(SandboxConfig{Enabled: true, FallbackUnsandboxed: false, AllowedPorts: parsePorts(allowedPorts)})
	if !sb.Available() {
		h.broadcastBashError("yesmem", fmt.Sprintf("[bash-job] %q FAILED, auto-correct SKIPPED: sandbox unavailable (ai-jail missing)\nError: %s", run.JobName, run.ErrorMsg))
		return
	}

	prompt := fmt.Sprintf(
		"A scheduled bash cap %q failed. Fix the handler_bash script.\n\nCurrent handler_bash:\n%s\n\nError (exit %d): %s\nOutput:\n%s\n\nRespond ONLY with the corrected bash script, no explanation. If unfixable, respond with UNFIXABLE: reason.",
		run.CapName, currentHandler, run.ExitCode, run.ErrorMsg, truncatStr(run.Output, 2000))

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "claude", "--bare", "-p", prompt, "--model", "sonnet", "--max-turns", "1")
	fixOut, err := cmd.CombinedOutput()
	if err != nil {
		log.Printf("[bash-errors] auto-correct for %s failed: %v", run.CapName, err)
		h.diagnoseBashError(run)
		return
	}

	fixed := strings.TrimSpace(string(fixOut))
	if len(fixed) < 10 || strings.HasPrefix(fixed, "UNFIXABLE:") {
		h.broadcastBashError("yesmem", fmt.Sprintf("[bash-job] %q FAILED, auto-correct not possible\nError: %s\nResponse: %s", run.JobName, run.ErrorMsg, truncatStr(fixed, 500)))
		return
	}

	profile := ProfileStandard
	intervalSec := 0
	if job != nil {
		profile = job.Sandbox
		intervalSec = job.IntervalSeconds
	}
	_, testExit, testErr := sb.RunWithProfile(fixed, autoCorrectTestRunTimeout(intervalSec), profile)

	if testErr != nil || testExit != 0 {
		log.Printf("[bash-errors] corrected script for %s also failed: %v (exit=%d)", run.CapName, testErr, testExit)
		h.broadcastBashError("yesmem", fmt.Sprintf("[bash-job] %q FAILED, auto-correct unsuccessful\nError: %s\nSuggested fix:\n%s", run.JobName, run.ErrorMsg, truncatStr(fixed, 500)))
		return
	}

	diff := ClassifyCapDiff(currentHandler, fixed)

	if diff == DiffMinimal {
		h.Handle(Request{
			Method: "save_cap",
			Params: map[string]any{
				"name":         run.CapName,
				"handler_bash": fixed,
				"tested":       true,
				"test_date":    time.Now().Format(time.RFC3339),
				"source":       "auto_correct_accepted",
			},
		})
		h.broadcastBashError("yesmem", fmt.Sprintf("[bash-job] %q FAILED, AUTO-CORRECTED (minimal).\nOriginal error: %s\nCap %q handler_bash updated and tested.", run.JobName, run.ErrorMsg, run.CapName))
		return
	}

	proposalID, err := h.persistProposalForReview(run, activeCap, fixed)
	if err != nil {
		log.Printf("[bash-errors] proposal-persist for %s failed: %v", run.CapName, err)
		h.broadcastBashError("yesmem", fmt.Sprintf("[bash-job] %q AUTO-CORRECT proposal save failed: %v", run.JobName, err))
		return
	}
	preview := truncatStr(unifiedDiffPreview(currentHandler, fixed), 1500)
	h.broadcastBashError("yesmem", fmt.Sprintf(
		"[bash-job] %q AUTO-CORRECT PROPOSAL #%d for cap %q (substantial change). Active cap unchanged.\nOriginal error: %s\nDecide via cap_proposal_decide(id=%d, decision=accept|reject).\n\nDIFF:\n%s",
		run.JobName, proposalID, run.CapName, run.ErrorMsg, proposalID, preview,
	))
}

// persistProposalForReview clones the active cap's CapMeta, swaps the bash
// script body to the proposed fix, and writes a NEW learnings row with
// category='cap_proposed' tagged 'pending_approval'. The active cap row
// is left untouched. Caller decides the final action via cap_proposal_decide.
//
// The caller passes the already-resolved active cap so we don't reload it
// from storage. If active is nil the call fails fast.
func (h *Handler) persistProposalForReview(run storage.BashJobRun, active *models.Learning, fixed string) (int64, error) {
	if active == nil {
		return 0, fmt.Errorf("no active cap %q to clone for proposal", run.CapName)
	}
	meta, err := ParseCapMeta(active.Context)
	if err != nil {
		return 0, fmt.Errorf("parse active CapMeta: %w", err)
	}
	swapped := false
	for i := range meta.Scripts {
		if meta.Scripts[i].Runtime != "bash" {
			continue
		}
		if run.ScriptName != "" && meta.Scripts[i].Name != run.ScriptName {
			continue
		}
		meta.Scripts[i].Body = fixed
		swapped = true
		break
	}
	if !swapped {
		return 0, fmt.Errorf("active cap %q has no bash script %q to replace", run.CapName, run.ScriptName)
	}
	ctxJSON, err := meta.ToJSON()
	if err != nil {
		return 0, fmt.Errorf("render proposal CapMeta: %w", err)
	}
	keywords := appendUniqueTag(active.Keywords, "pending_approval")
	if run.ScriptName != "" {
		keywords = appendUniqueTag(keywords, "script:"+run.ScriptName)
	}
	content := fmt.Sprintf("%s [auto-correct proposal from bash_job_runs.id=%d]", active.Content, run.ID)
	l := &models.Learning{
		Content:            content,
		Category:           "cap_proposed",
		Source:             "auto_correct_proposal",
		Project:            active.Project,
		Confidence:         1.0,
		Context:            ctxJSON,
		Domain:             active.Domain,
		TriggerRule:        "cap_proposed:" + run.CapName,
		Keywords:           keywords,
		AnticipatedQueries: []string{run.CapName, "proposal", "pending_approval", "cap_proposed"},
		CreatedAt:          time.Now(),
	}
	return h.store.InsertLearning(l)
}

func appendUniqueTag(existing []string, tag string) []string {
	for _, t := range existing {
		if t == tag {
			return existing
		}
	}
	return append(existing, tag)
}

// unifiedDiffPreview renders a small unified-diff-style preview suitable
// for embedding in a broadcast notification. Lines that match exactly are
// shown as context, divergent lines as -old / +new pairs.
func unifiedDiffPreview(oldBody, newBody string) string {
	oldLines := strings.Split(strings.TrimRight(oldBody, "\n"), "\n")
	newLines := strings.Split(strings.TrimRight(newBody, "\n"), "\n")
	var b strings.Builder
	b.WriteString("--- before\n+++ after\n")
	maxLen := len(oldLines)
	if len(newLines) > maxLen {
		maxLen = len(newLines)
	}
	for i := 0; i < maxLen; i++ {
		var oldLine, newLine string
		if i < len(oldLines) {
			oldLine = oldLines[i]
		}
		if i < len(newLines) {
			newLine = newLines[i]
		}
		if i < len(oldLines) && i < len(newLines) && oldLine == newLine {
			fmt.Fprintf(&b, " %s\n", oldLine)
			continue
		}
		if i < len(oldLines) {
			fmt.Fprintf(&b, "-%s\n", oldLine)
		}
		if i < len(newLines) {
			fmt.Fprintf(&b, "+%s\n", newLine)
		}
	}
	return b.String()
}

func (h *Handler) broadcastBashError(project, msg string) {
	log.Printf("[bash-errors] broadcast: %s", truncatStr(msg, 200))
	_, _ = h.store.SendBroadcast("scheduler", project, msg)
}

func (h *Handler) findScheduledJob(jobID string) *ScheduledJob {
	if h.scheduler == nil {
		return nil
	}
	for _, j := range h.scheduler.ListJobs() {
		if j.ID == jobID {
			return &j
		}
	}
	return nil
}
