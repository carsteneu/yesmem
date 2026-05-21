package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/carsteneu/yesmem/internal/models"
	"github.com/carsteneu/yesmem/internal/storage"
)

// disableSandbox forces NewSandbox(...).Available() to return false by removing
// ai-jail from PATH and short-circuiting the network-download fallback. Used
// to assert that auto-correct fails closed when the sandbox isn't available.
func disableSandbox(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("YESMEM_DISABLE_AIJAIL_DOWNLOAD", "1")
}

// stubClaudeReturning installs a fake `claude` on PATH that prints body to
// stdout and exits 0. autoCorrectBashCap will see this as a successful LLM
// fix and proceed to the test-run + persist phase. PATH retains /bin and
// /usr/bin so bash is findable for the unsandboxed-fallback path; without
// that, the test would pass for the wrong reason (exec("bash") would fail
// regardless of FallbackUnsandboxed).
func stubClaudeReturning(t *testing.T, body string) {
	t.Helper()
	dir := t.TempDir()
	quoted := "'" + strings.ReplaceAll(body, "'", `'\''`) + "'"
	script := "#!/bin/sh\nprintf '%s\\n' " + quoted + "\n"
	path := filepath.Join(dir, "claude")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write claude stub: %v", err)
	}
	t.Setenv("PATH", dir+":/bin:/usr/bin")
}

// neutralizeClaude points PATH at an empty tempdir so that exec.LookPath("claude")
// inside diagnoseBashError / autoCorrectBashCap fails fast. Keeps these tests
// deterministic regardless of whether the dev machine has `claude` installed.
func neutralizeClaude(t *testing.T) {
	t.Helper()
	t.Setenv("PATH", t.TempDir())
}

// When a bash error has no matching scheduled job (or AutoCorrect=false) and
// CapName is empty, processBashJobErrors falls into diagnoseBashError. With claude
// unavailable, diagnose returns false, so the run row is intentionally LEFT
// unprocessed for a later retry. This regression-protects that retry semantics.
func TestProcessBashErrors_DiagnoseFailureLeavesUnprocessed(t *testing.T) {
	neutralizeClaude(t)
	h, s := mustHandler(t)

	if err := s.SaveBashJobRun(storage.BashJobRun{
		JobID:    "diag-only",
		JobName:  "broken-script",
		Command:  "exit 1",
		Status:   "error",
		ExitCode: 1,
		Output:   "boom",
		ErrorMsg: "exit status 1",
	}); err != nil {
		t.Fatalf("save run: %v", err)
	}

	h.processBashJobErrors()

	runs, err := s.GetUnprocessedBashErrors(10)
	if err != nil {
		t.Fatalf("get unprocessed: %v", err)
	}
	if len(runs) != 1 {
		t.Errorf("diagnose-only path with claude unavailable must NOT mark processed; got %d unprocessed", len(runs))
	}
}

// When a scheduled job has AutoCorrect=true and a CapName, processBashJobErrors
// takes the auto-correct branch which always marks the run processed — even if the
// cap can't be resolved or the LLM call fails. The user-visible contract: a row
// only re-appears in GetUnprocessedBashErrors if the diagnose-only fallback ran.
func TestProcessBashErrors_AutoCorrectMarksProcessedEvenWhenCapMissing(t *testing.T) {
	neutralizeClaude(t)
	h, s := mustHandler(t)

	h.scheduler = NewScheduler(func(_ ScheduledJob) {})
	if err := h.scheduler.AddJob(ScheduledJob{
		ID:          "ac-job",
		Name:        "auto-correct-test",
		Cron:        "*/5 * * * *",
		Enabled:     true,
		Mode:        "bash",
		CapName:     "never-existed",
		AutoCorrect: true,
	}); err != nil {
		t.Fatalf("add job: %v", err)
	}

	if err := s.SaveBashJobRun(storage.BashJobRun{
		JobID:    "ac-job",
		JobName:  "auto-correct-test",
		CapName:  "never-existed",
		Command:  "exit 7",
		Status:   "error",
		ExitCode: 7,
		Output:   "fail",
		ErrorMsg: "exit status 7",
	}); err != nil {
		t.Fatalf("save run: %v", err)
	}

	h.processBashJobErrors()

	runs, err := s.GetUnprocessedBashErrors(10)
	if err != nil {
		t.Fatalf("get unprocessed: %v", err)
	}
	if len(runs) != 0 {
		t.Errorf("auto-correct branch should always mark processed; got %d unprocessed", len(runs))
	}
}

// Empty queue must be a no-op, not a panic, since the heartbeat calls this every
// tick whether or not there is work.
func TestProcessBashErrors_NoErrorsIsNoop(t *testing.T) {
	h, _ := mustHandler(t)
	h.processBashJobErrors()
}

// findScheduledJob is called from processBashJobErrors before any scheduler is
// guaranteed to be attached (daemon bootstrap order). It must return nil rather
// than panic when h.scheduler is nil.
func TestFindScheduledJob_NilScheduler(t *testing.T) {
	h, _ := mustHandler(t)
	if got := h.findScheduledJob("anything"); got != nil {
		t.Errorf("expected nil from findScheduledJob with no scheduler, got %+v", got)
	}
}

// findScheduledJob returns the matching job by ID and nil for unknown IDs. This
// is the lookup that drives whether auto-correct fires for a given run.
func TestFindScheduledJob_Match(t *testing.T) {
	h, _ := mustHandler(t)
	h.scheduler = NewScheduler(func(_ ScheduledJob) {})
	if err := h.scheduler.AddJob(ScheduledJob{
		ID:          "find-me",
		Name:        "x",
		Cron:        "*/5 * * * *",
		Enabled:     true,
		Mode:        "bash",
		AutoCorrect: true,
	}); err != nil {
		t.Fatalf("add: %v", err)
	}
	got := h.findScheduledJob("find-me")
	if got == nil || got.ID != "find-me" || !got.AutoCorrect {
		t.Errorf("findScheduledJob(\"find-me\") = %+v", got)
	}
	if h.findScheduledJob("missing") != nil {
		t.Error("missing job should be nil")
	}
}

// When the sandbox is unavailable (ai-jail missing AND download disabled), the
// auto-correct loop must NOT persist the LLM-proposed body. The test-run is
// supposed to verify the fix inside ai-jail; falling back to direct shell
// execution would run unreviewed LLM output on the host. This test fails
// against today's code (FallbackUnsandboxed=true) and is the regression guard
// for T0 of the auto-correct hardening plan.
func TestAutoCorrect_DoesNotPersistWhenSandboxUnavailable(t *testing.T) {
	disableSandbox(t)
	stubClaudeReturning(t, "echo fixed; exit 0")

	h, s := mustHandler(t)
	h.scheduler = NewScheduler(func(_ ScheduledJob) {})
	if err := h.scheduler.AddJob(ScheduledJob{
		ID:          "j1",
		Name:        "broken",
		Cron:        "*/5 * * * *",
		Enabled:     true,
		Mode:        "bash",
		CapName:     "broken_cap",
		AutoCorrect: true,
	}); err != nil {
		t.Fatalf("add job: %v", err)
	}

	const original = "exit 1"
	saveResp := h.Handle(Request{
		Method: "save_cap",
		Params: map[string]any{
			"name":         "broken_cap",
			"description":  "intentionally failing cap for T0 regression",
			"handler_bash": original,
			"project":      "yesmem",
		},
	})
	if saveResp.Error != "" {
		t.Fatalf("save_cap: %v", saveResp.Error)
	}

	if err := s.SaveBashJobRun(storage.BashJobRun{
		JobID:    "j1",
		JobName:  "broken",
		CapName:  "broken_cap",
		Command:  original,
		Status:   "error",
		ExitCode: 1,
		ErrorMsg: "exit 1",
	}); err != nil {
		t.Fatalf("save run: %v", err)
	}

	h.processBashJobErrors()

	caps, err := h.store.GetActiveLearnings("cap", "yesmem", "", "", 0)
	if err != nil {
		t.Fatalf("read caps: %v", err)
	}
	if len(caps) == 0 {
		t.Fatalf("no caps in store after save_cap")
	}
	var found bool
	for _, l := range caps {
		if !strings.Contains(l.Context, "broken_cap") {
			continue
		}
		found = true
		if !strings.Contains(l.Context, original) {
			t.Errorf("active cap lost original body %q; context:\n%s", original, l.Context)
		}
		if strings.Contains(l.Context, "echo fixed") {
			t.Errorf("active cap was overwritten with LLM-proposed body despite sandbox unavailable; context:\n%s", l.Context)
		}
	}
	if !found {
		t.Fatalf("cap %q not found in active learnings", "broken_cap")
	}
}

// TestAutoCorrect_MinimalDiffPersistsDirectly asserts that a small fix
// (≤5 new lines, ≥0.85 token similarity) is persisted in place via save_cap
// with source='auto_correct_accepted'. The test is gated on the claude-CLI
// mock harness that Task 7 will introduce.
func TestAutoCorrect_MinimalDiffPersistsDirectly(t *testing.T) {
	t.Skip("requires claude-CLI mock harness — implement once Task 7 adds Cmd-injection seam")
}

// TestAutoCorrect_SubstantialDiffCreatesProposalAndDoesNotChangeActiveCap
// asserts that a large rewrite is staged as a cap_proposed learnings row
// tagged 'pending_approval' while the active cap row is left unchanged.
func TestAutoCorrect_SubstantialDiffCreatesProposalAndDoesNotChangeActiveCap(t *testing.T) {
	t.Skip("requires claude-CLI mock harness — implement once Task 7 adds Cmd-injection seam")
}

// TestPersistProposalForReview_FindsBashByRuntime drives persistProposalForReview
// directly against a real cap seeded through save_cap. After Cap-Spec v1.1 the
// bash body lives on a ScriptMeta with Kind=='tool'|'handler' and Runtime=='bash';
// scanning Scripts[i].Kind=='bash' silently misses every script and the proposal
// path collapses with "no bash script to replace". Asserts that the proposal row
// is created with the swapped body, that the active cap is left untouched, and
// that project metadata is propagated onto the proposal.
func TestPersistProposalForReview_FindsBashByRuntime(t *testing.T) {
	h, _ := mustHandler(t)
	saveResp := h.Handle(Request{
		Method: "save_cap",
		Params: map[string]any{
			"name":         "demo_persist",
			"description":  "demo cap for persistProposalForReview",
			"handler_bash": "echo old\nexit 0\n",
			"project":      "yesmem",
			"tags":         []any{"demo"},
			"_from_disk":   true,
		},
	})
	if saveResp.Error != "" {
		t.Fatalf("seed save_cap: %s", saveResp.Error)
	}

	run := storage.BashJobRun{
		ID:       42,
		JobID:    "j1",
		JobName:  "broken",
		CapName:  "demo_persist",
		ErrorMsg: "exit 1",
	}
	caps, err := h.store.GetActiveLearnings("cap", "", "", "", 0)
	if err != nil {
		t.Fatalf("GetActiveLearnings: %v", err)
	}
	var active *models.Learning
	for i := range caps {
		if caps[i].TriggerRule == "cap:demo_persist" {
			active = &caps[i]
			break
		}
	}
	if active == nil {
		t.Fatalf("seeded cap demo_persist not found among active learnings")
	}
	propID, err := h.persistProposalForReview(run, active, "echo fixed\nexit 0\n")
	if err != nil {
		t.Fatalf("persistProposalForReview: %v", err)
	}
	if propID <= 0 {
		t.Fatalf("expected proposal ID > 0, got %d", propID)
	}

	prop, err := h.store.GetLearning(propID)
	if err != nil {
		t.Fatalf("get proposal: %v", err)
	}
	if prop.Category != "cap_proposed" {
		t.Errorf("category=%q, want cap_proposed", prop.Category)
	}
	if prop.TriggerRule != "cap_proposed:demo_persist" {
		t.Errorf("trigger=%q, want cap_proposed:demo_persist", prop.TriggerRule)
	}
	if prop.Project != "yesmem" {
		t.Errorf("project=%q, want yesmem (inherited from active)", prop.Project)
	}

	meta, err := ParseCapMeta(prop.Context)
	if err != nil {
		t.Fatalf("parse proposal CapMeta: %v", err)
	}
	var newBody string
	for _, sc := range meta.Scripts {
		if sc.Runtime == "bash" {
			newBody = sc.Body
			break
		}
	}
	if !strings.Contains(newBody, "echo fixed") {
		t.Errorf("proposal bash body not swapped to fixed version: %q", newBody)
	}

	// The active cap must still hold the original body — proposals do not mutate the live row.
	caps, err = h.store.GetActiveLearnings("cap", "yesmem", "", "", 0)
	if err != nil {
		t.Fatalf("read active caps: %v", err)
	}
	var activeBody string
	for i := range caps {
		m, err := ParseCapMeta(caps[i].Context)
		if err != nil || m.Name != "demo_persist" {
			continue
		}
		for _, sc := range m.Scripts {
			if sc.Runtime == "bash" {
				activeBody = sc.Body
				break
			}
		}
		break
	}
	if !strings.Contains(activeBody, "echo old") {
		t.Errorf("active cap body wrongly mutated: %q", activeBody)
	}
}

// TestAutoCorrect_CooldownSkipsRapidRetries verifies that when several failed
// runs of the same cap pile up in a single processBashJobErrors tick, only the
// first triggers an auto-correct attempt; the remaining are skipped (still
// marked processed) so the LLM is not hammered while a cap has a persistent
// bug. With the sandbox disabled, the first attempt broadcasts the SKIPPED
// (sandbox unavailable) message; the remaining two MUST broadcast a
// "cooldown" skip — that is the contract this test pins.
func TestAutoCorrect_CooldownSkipsRapidRetries(t *testing.T) {
	neutralizeClaude(t)
	disableSandbox(t)

	h, s := mustHandler(t)
	seedCap(t, h, "flaky", "yesmem", "exit 1\n", []string{"flaky"})

	h.scheduler = NewScheduler(func(_ ScheduledJob) {})
	if err := h.scheduler.AddJob(ScheduledJob{
		ID:          "j1",
		Name:        "flaky-job",
		Cron:        "*/5 * * * *",
		Enabled:     true,
		Mode:        "bash",
		CapName:     "flaky",
		AutoCorrect: true,
	}); err != nil {
		t.Fatalf("add job: %v", err)
	}

	for i := 0; i < 3; i++ {
		if err := s.SaveBashJobRun(storage.BashJobRun{
			JobID:    "j1",
			JobName:  "flaky-job",
			CapName:  "flaky",
			Command:  "exit 1",
			Status:   "error",
			ExitCode: 1,
			Output:   "",
			ErrorMsg: "exit status 1",
		}); err != nil {
			t.Fatalf("save run %d: %v", i, err)
		}
	}

	h.processBashJobErrors()

	runs, err := s.GetUnprocessedBashErrors(10)
	if err != nil {
		t.Fatalf("get unprocessed: %v", err)
	}
	if len(runs) != 0 {
		t.Errorf("all 3 runs must be marked processed; got %d unprocessed", len(runs))
	}

	msgs, err := s.GetUnreadBroadcasts("test-session", "yesmem")
	if err != nil {
		t.Fatalf("get broadcasts: %v", err)
	}
	cooldownCount := 0
	for _, m := range msgs {
		if strings.Contains(m.Content, "cooldown") {
			cooldownCount++
		}
	}
	if cooldownCount != 2 {
		t.Errorf("expected 2 cooldown-skip broadcasts (runs 2 and 3); got %d (broadcasts total: %d)", cooldownCount, len(msgs))
	}
}

// TestAutoCorrect_GenerationLimitBlocksAfter3PerDay verifies that once a cap
// has accumulated autoCorrectMaxGenerationsPerDay auto-correct outcomes
// (applied caps via Source='auto_correct_accepted' OR staged proposals via
// Source='auto_correct_proposal') within the last 24h, the next failed run
// for that cap is skipped with a "limit"-tagged broadcast — preventing a
// broken cap from burning unbounded LLM tokens. The gate runs after T4's
// per-cap cooldown but before the per-tick budget and the in-flight
// semaphore.
func TestAutoCorrect_GenerationLimitBlocksAfter3PerDay(t *testing.T) {
	neutralizeClaude(t)
	disableSandbox(t)

	h, s := mustHandler(t)
	seedCap(t, h, "buggy", "yesmem", "exit 1\n", []string{"buggy"})

	for i := 0; i < 3; i++ {
		_, err := s.InsertLearning(&models.Learning{
			Content:     fmt.Sprintf("auto-correct apply #%d", i),
			Category:    "cap",
			Source:      "auto_correct_accepted",
			TriggerRule: "cap:buggy",
			Project:     "yesmem",
			Confidence:  1.0,
			CreatedAt:   time.Now().Add(-time.Duration(i+1) * time.Hour),
		})
		if err != nil {
			t.Fatalf("seed prior accept #%d: %v", i, err)
		}
	}

	h.scheduler = NewScheduler(func(_ ScheduledJob) {})
	if err := h.scheduler.AddJob(ScheduledJob{
		ID:          "j1",
		Name:        "buggy-job",
		Cron:        "*/5 * * * *",
		Enabled:     true,
		Mode:        "bash",
		CapName:     "buggy",
		AutoCorrect: true,
	}); err != nil {
		t.Fatalf("add job: %v", err)
	}

	if err := s.SaveBashJobRun(storage.BashJobRun{
		JobID:    "j1",
		JobName:  "buggy-job",
		CapName:  "buggy",
		Command:  "exit 1",
		Status:   "error",
		ExitCode: 1,
		ErrorMsg: "exit status 1",
	}); err != nil {
		t.Fatalf("save run: %v", err)
	}

	h.processBashJobErrors()

	runs, err := s.GetUnprocessedBashErrors(10)
	if err != nil {
		t.Fatalf("get unprocessed: %v", err)
	}
	if len(runs) != 0 {
		t.Errorf("fresh run must be marked processed; got %d unprocessed", len(runs))
	}

	msgs, err := s.GetUnreadBroadcasts("test-session", "yesmem")
	if err != nil {
		t.Fatalf("get broadcasts: %v", err)
	}
	limitCount := 0
	for _, m := range msgs {
		if strings.Contains(m.Content, "limit") {
			limitCount++
		}
	}
	if limitCount != 1 {
		t.Errorf("expected 1 generation-limit broadcast; got %d (broadcasts total: %d)", limitCount, len(msgs))
	}
}

// TestAutoCorrectTestRunTimeout pins the timeout selected for the auto-correct
// test-run as a function of the job's interval. Sub-minute intervals get a
// tight timeout matching the interval, anything else falls back to the default
// so a long-cron job does not produce a minute-long test-run.
func TestAutoCorrectTestRunTimeout(t *testing.T) {
	cases := []struct {
		name        string
		intervalSec int
		want        int
	}{
		{"zero defaults to 10", 0, 10},
		{"negative defaults to 10", -5, 10},
		{"sub-minute uses interval", 15, 15},
		{"just-under-60 uses interval", 59, 59},
		{"60 falls back to default", 60, 10},
		{"minutes fall back to default", 120, 10},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := autoCorrectTestRunTimeout(tc.intervalSec); got != tc.want {
				t.Errorf("intervalSec=%d: got %d, want %d", tc.intervalSec, got, tc.want)
			}
		})
	}
}

// TestPersistProposalForReview_MultiBashTargetsScriptByName seeds a bundle cap
// with two bash handlers (poll + reply) and verifies that persistProposalForReview
// patches only the script named in run.ScriptName. The other bash script must
// keep its original body, and the proposal's Keywords must carry "script:reply"
// so handleCapProposalDecide can route the accept back to the same script.
func TestPersistProposalForReview_MultiBashTargetsScriptByName(t *testing.T) {
	h, _ := mustHandler(t)

	scriptsJSON := `[` +
		`{"name":"poll","kind":"handler","runtime":"bash","lang":"bash","body":"echo poll-original\nexit 0\n"},` +
		`{"name":"reply","kind":"handler","runtime":"bash","lang":"bash","body":"echo reply-original\nexit 0\n"}` +
		`]`
	saveResp := h.Handle(Request{
		Method: "save_cap",
		Params: map[string]any{
			"name":        "tg_bundle",
			"description": "two bash handlers",
			"scripts":     scriptsJSON,
			"project":     "yesmem",
		},
	})
	if saveResp.Error != "" {
		t.Fatalf("seed save_cap: %s", saveResp.Error)
	}

	caps, err := h.store.GetActiveLearnings("cap", "yesmem", "", "", 0)
	if err != nil {
		t.Fatalf("GetActiveLearnings: %v", err)
	}
	var active *models.Learning
	for i := range caps {
		if caps[i].TriggerRule == "cap:tg_bundle" {
			active = &caps[i]
			break
		}
	}
	if active == nil {
		t.Fatalf("seeded cap not found")
	}

	run := storage.BashJobRun{
		ID:         99,
		JobID:      "j1",
		JobName:    "reply_tick",
		CapName:    "tg_bundle",
		ScriptName: "reply",
		ErrorMsg:   "timeout",
	}
	propID, err := h.persistProposalForReview(run, active, "echo reply-fixed\nexit 0\n")
	if err != nil {
		t.Fatalf("persistProposalForReview: %v", err)
	}

	prop, err := h.store.GetLearning(propID)
	if err != nil {
		t.Fatalf("get proposal: %v", err)
	}
	if err := h.store.LoadJunctionData(prop); err != nil {
		t.Fatalf("LoadJunctionData: %v", err)
	}
	meta, err := ParseCapMeta(prop.Context)
	if err != nil {
		t.Fatalf("parse proposal CapMeta: %v", err)
	}
	var pollBody, replyBody string
	for _, sc := range meta.Scripts {
		switch sc.Name {
		case "poll":
			pollBody = sc.Body
		case "reply":
			replyBody = sc.Body
		}
	}
	if !strings.Contains(replyBody, "echo reply-fixed") {
		t.Errorf("reply body not patched, got %q", replyBody)
	}
	if !strings.Contains(pollBody, "echo poll-original") {
		t.Errorf("poll body wrongly mutated, got %q", pollBody)
	}

	scriptKeyword := ""
	for _, k := range prop.Keywords {
		if strings.HasPrefix(k, "script:") {
			scriptKeyword = k
			break
		}
	}
	if scriptKeyword != "script:reply" {
		t.Errorf("missing or wrong script keyword: got %q, want %q", scriptKeyword, "script:reply")
	}
}
