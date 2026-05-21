package daemon

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/carsteneu/yesmem/internal/models"
	"github.com/carsteneu/yesmem/internal/sanitize"
	"github.com/carsteneu/yesmem/internal/storage"
)

func TestHeadlessArgs_NoSession(t *testing.T) {
	args := headlessArgs("", "")
	want := []string{"-p", "--output-format", "json", "--verbose", "--max-turns", "3"}
	if len(args) != len(want) {
		t.Fatalf("got %d args, want %d: %v", len(args), len(want), args)
	}
	for i, a := range want {
		if args[i] != a {
			t.Errorf("arg[%d] = %q, want %q", i, args[i], a)
		}
	}
}

func TestHeadlessArgs_WithSession(t *testing.T) {
	args := headlessArgs("sess-abc123", "")
	want := []string{"-p", "--output-format", "json", "--verbose", "--max-turns", "3", "--resume", "sess-abc123"}
	if len(args) != len(want) {
		t.Fatalf("got %d args, want %d: %v", len(args), len(want), args)
	}
	for i, a := range want {
		if args[i] != a {
			t.Errorf("arg[%d] = %q, want %q", i, args[i], a)
		}
	}
}

func TestParseHeadlessOutput_ValidJSON(t *testing.T) {
	output := []byte(`{"type":"result","result":"Hello world","session_id":"sess-xyz789","cost_usd":0.01}`)
	text, sid := parseHeadlessOutput(output)
	if text != "Hello world" {
		t.Errorf("text = %q, want %q", text, "Hello world")
	}
	if sid != "sess-xyz789" {
		t.Errorf("session_id = %q, want %q", sid, "sess-xyz789")
	}
}

func TestParseHeadlessOutput_InvalidJSON(t *testing.T) {
	output := []byte("plain text response without json")
	text, sid := parseHeadlessOutput(output)
	if text != "plain text response without json" {
		t.Errorf("text = %q, want raw output", text)
	}
	if sid != "" {
		t.Errorf("session_id = %q, want empty", sid)
	}
}

func TestParseHeadlessOutput_StreamJSON(t *testing.T) {
	output := []byte("{\"type\":\"assistant\",\"message\":\"thinking...\"}\n{\"type\":\"result\",\"result\":\"Done\",\"session_id\":\"sess-final\"}\n")
	text, sid := parseHeadlessOutput(output)
	if text != "Done" {
		t.Errorf("text = %q, want %q", text, "Done")
	}
	if sid != "sess-final" {
		t.Errorf("session_id = %q, want %q", sid, "sess-final")
	}
}

func TestParseHeadlessOutput_EmptyResult(t *testing.T) {
	output := []byte(`{"type":"result","result":"","session_id":"sess-empty"}`)
	text, sid := parseHeadlessOutput(output)
	if text != "" {
		t.Errorf("text = %q, want empty", text)
	}
	if sid != "sess-empty" {
		t.Errorf("session_id = %q, want %q", sid, "sess-empty")
	}
}

func TestResolveJobSandbox(t *testing.T) {
	tests := []struct {
		name    string
		param   string
		dflt    SandboxProfile
		want    SandboxProfile
		wantErr bool
	}{
		{"empty uses default", "", ProfileStandard, ProfileStandard, false},
		{"explicit none", "none", ProfileStandard, ProfileNone, false},
		{"explicit standard", "standard", ProfileNone, ProfileStandard, false},
		{"explicit strict", "strict", ProfileStandard, ProfileStrict, false},
		{"invalid", "yolo", ProfileStandard, ProfileNone, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveJobSandbox(tt.param, tt.dflt)
			if tt.wantErr && err == nil {
				t.Fatal("expected error")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestHeadlessArgs_WithModel(t *testing.T) {
	args := headlessArgs("", "claude-opus-4-7")
	found := false
	for i, a := range args {
		if a == "--model" && i+1 < len(args) && args[i+1] == "claude-opus-4-7" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected --model claude-opus-4-7 in args: %v", args)
	}
}

func TestHeadlessArgs_EmptyModelOmitted(t *testing.T) {
	args := headlessArgs("", "")
	for _, a := range args {
		if a == "--model" {
			t.Error("empty model should not produce --model flag")
		}
	}
}

func TestPrepareBashCommand_InjectsPreamble(t *testing.T) {
	cmd := `TOKEN=$(store '{"capability":"telegram","action":"query","table":"config"}' | jq -r '.[0].value')
echo $TOKEN`
	result := prepareBashCommand(cmd)
	if result == cmd {
		t.Error("should prepend adapter preamble")
	}
	if !strings.Contains(result, "store()") {
		t.Error("preamble should define store() function")
	}
	if !strings.HasSuffix(result, cmd) {
		t.Error("original command should be at the end")
	}
}

func TestPrepareBashCommand_NoStoreNoPreamble(t *testing.T) {
	cmd := "echo hello"
	result := prepareBashCommand(cmd)
	if result != cmd {
		t.Errorf("should not modify command without store(), got %q", result)
	}
}

func TestStoreBashRun_RedactsOutputWhenRedactorSet(t *testing.T) {
	s, err := storage.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	h := &Handler{
		store:    s,
		redactor: sanitize.NewSecretRedactor(nil),
	}
	job := &ScheduledJob{
		ID:      "test-redact",
		CapName: "test",
	}
	out := "leaked sk-ant-api03-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa here"
	h.storeBashRun(job, "echo x", out, nil, 0)
	runs, err := s.GetBashJobRuns(job.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) == 0 {
		t.Fatal("no run stored")
	}
	if strings.Contains(runs[0].Output, "sk-ant-api03") {
		t.Fatalf("output not redacted: %q", runs[0].Output)
	}
	if !strings.Contains(runs[0].Output, "[REDACTED:anthropic_api_key]") {
		t.Fatalf("expected marker in output, got %q", runs[0].Output)
	}
}

func TestStoreBashRun_RedactsCommandWhenRedactorSet(t *testing.T) {
	s, err := storage.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	h := &Handler{
		store:    s,
		redactor: sanitize.NewSecretRedactor(nil),
	}
	job := &ScheduledJob{ID: "test-cmd-redact", CapName: "test"}
	cmd := `echo "token: sk-ant-api03-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"`
	h.storeBashRun(job, cmd, "ok", nil, 0)
	runs, err := s.GetBashJobRuns(job.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) == 0 {
		t.Fatal("no run stored")
	}
	if strings.Contains(runs[0].Command, "sk-ant-api03") {
		t.Fatalf("command not redacted: %q", runs[0].Command)
	}
	if !strings.Contains(runs[0].Command, "[REDACTED:anthropic_api_key]") {
		t.Fatalf("expected marker in command, got %q", runs[0].Command)
	}
}

func TestStoreBashRun_RedactsErrorMsgWhenRedactorSet(t *testing.T) {
	s, err := storage.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	h := &Handler{
		store:    s,
		redactor: sanitize.NewSecretRedactor(nil),
	}
	job := &ScheduledJob{ID: "test-err-redact", CapName: "test"}
	bashErr := errors.New("exec failed: stderr contained sk-ant-api03-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa from upstream")
	h.storeBashRun(job, "cmd", "", bashErr, 1)
	runs, err := s.GetBashJobRuns(job.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) == 0 {
		t.Fatal("no run stored")
	}
	if strings.Contains(runs[0].ErrorMsg, "sk-ant-api03") {
		t.Fatalf("error_msg not redacted: %q", runs[0].ErrorMsg)
	}
	if !strings.Contains(runs[0].ErrorMsg, "[REDACTED:anthropic_api_key]") {
		t.Fatalf("expected marker in error_msg, got %q", runs[0].ErrorMsg)
	}
}

// seedCapWithSandbox stores a cap learning whose bash script carries the given sandbox value.
func seedCapWithSandbox(t *testing.T, h *Handler, capName, scriptName, body, sandbox string) {
	t.Helper()
	meta := CapMeta{
		Name:        capName,
		Description: "Test: " + capName,
		Scripts: []ScriptMeta{
			{Name: scriptName, Kind: "handler", Runtime: "bash", Body: body, Sandbox: sandbox},
		},
		Version: 1,
		Tested:  true,
	}
	ctx, err := meta.ToJSON()
	if err != nil {
		t.Fatalf("marshal meta: %v", err)
	}
	l := &models.Learning{
		Content:     capName + " — Test: " + capName,
		Category:    "cap",
		Source:      "user_stated",
		Project:     "global",
		Context:     ctx,
		TriggerRule: "cap:" + capName,
	}
	if _, err := h.store.InsertLearning(l); err != nil {
		t.Fatalf("insert cap: %v", err)
	}
}

func TestResolveBashCommand_ReturnsScriptSandbox(t *testing.T) {
	h, _ := mustHandler(t)
	seedCapWithSandbox(t, h, "heartbeat", "run", "echo ok", "none")

	job := &ScheduledJob{CapName: "heartbeat", ScriptName: "run"}
	body, scriptSandbox, err := h.resolveBashCommand(job)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if body != "echo ok" {
		t.Errorf("body = %q, want %q", body, "echo ok")
	}
	if scriptSandbox != "none" {
		t.Errorf("scriptSandbox = %q, want %q", scriptSandbox, "none")
	}
}

func TestResolveBashCommand_ScriptSandboxEmpty(t *testing.T) {
	h, _ := mustHandler(t)
	seedCapWithSandbox(t, h, "my_cap", "fetch", "curl https://example.com", "")

	job := &ScheduledJob{CapName: "my_cap", ScriptName: "fetch"}
	body, scriptSandbox, err := h.resolveBashCommand(job)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if body != "curl https://example.com" {
		t.Errorf("body = %q, want %q", body, "curl https://example.com")
	}
	if scriptSandbox != "" {
		t.Errorf("scriptSandbox = %q, want empty (inherit)", scriptSandbox)
	}
}

func TestJobWorkDir_DelegatesToResolveAgentWorkDir(t *testing.T) {
	h, _ := mustHandler(t)
	got := h.jobWorkDir()
	if got == "/home/chief/memory/yesmem" {
		t.Fatalf("jobWorkDir() returned hardcoded dev path %q — must come from project resolver", got)
	}
	want := h.resolveAgentWorkDir("yesmem", "", "claude")
	if got != want {
		t.Errorf("jobWorkDir() = %q, want %q (canonical resolver)", got, want)
	}
}

func TestHandlerScheduler_NoHardcodedDevPath(t *testing.T) {
	src, err := os.ReadFile("handler_scheduler.go")
	if err != nil {
		t.Fatalf("read handler_scheduler.go: %v", err)
	}
	if strings.Contains(string(src), "/home/chief/memory/yesmem") {
		t.Error("handler_scheduler.go contains hardcoded /home/chief/memory/yesmem path — use h.jobWorkDir() / resolveAgentWorkDir instead")
	}
}

// TestScheduleCreate_IntervalSecondsWithoutCron verifies that schedule create
// accepts an empty cron when interval_seconds > 0. The scheduler at fire time
// uses interval_seconds when set, so cron is unused and should not be parsed.
// Without the fix, parseCron("") returns "need 5 fields, got 0".
func TestScheduleCreate_IntervalSecondsWithoutCron(t *testing.T) {
	h, _ := mustHandler(t)
	seedCapWithSandbox(t, h, "telegram", "telegram_poll", "echo poll", "")

	resp := h.Handle(Request{
		Method: "schedule",
		Params: map[string]any{
			"action":           "create",
			"name":             "telegram-poll",
			"interval_seconds": float64(15),
			"mode":             "bash",
			"cap_name":         "telegram",
			"script_name":      "telegram_poll",
		},
	})
	if resp.Error != "" {
		t.Fatalf("schedule create with empty cron + interval_seconds=15 should succeed, got error: %s", resp.Error)
	}
}

// TestScheduleCreate_EmptyCronAndZeroInterval ensures the existing validation
// still rejects jobs with neither cron nor interval_seconds set.
func TestScheduleCreate_EmptyCronAndZeroInterval(t *testing.T) {
	h, _ := mustHandler(t)
	resp := h.Handle(Request{
		Method: "schedule",
		Params: map[string]any{
			"action": "create",
			"name":   "no-schedule",
			"mode":   "bash",
			"prompt": "echo hi",
		},
	})
	if resp.Error == "" {
		t.Fatal("expected error for missing cron and interval_seconds, got success")
	}
}

func TestStoreBashRun_SkipsIdleRuns(t *testing.T) {
	s, err := storage.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	h := &Handler{store: s}
	job := &ScheduledJob{ID: "test-idle", CapName: "test"}

	h.storeBashRun(job, "echo hi", "", nil, 0)

	runs, err := s.GetBashJobRuns(job.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 0 {
		t.Fatalf("expected 0 runs for idle run, got %d", len(runs))
	}
}

func TestStoreBashRun_StillStoresErrors(t *testing.T) {
	s, err := storage.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	h := &Handler{store: s}
	job := &ScheduledJob{ID: "test-err", CapName: "test"}

	h.storeBashRun(job, "failing cmd", "", errors.New("boom"), 1)

	runs, err := s.GetBashJobRuns(job.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) == 0 {
		t.Fatal("expected error run to be stored, got 0")
	}
	if runs[0].Status != "error" {
		t.Fatalf("expected status=error, got %q", runs[0].Status)
	}
}

func TestStoreBashRun_StillStoresRealOutput(t *testing.T) {
	s, err := storage.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	h := &Handler{store: s}
	job := &ScheduledJob{ID: "test-real", CapName: "test"}

	h.storeBashRun(job, "echo done", "Combined fire: polls=5 stored=2 replies=1 fails=0", nil, 0)

	runs, err := s.GetBashJobRuns(job.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) == 0 {
		t.Fatal("expected real-output run to be stored, got 0")
	}
}
