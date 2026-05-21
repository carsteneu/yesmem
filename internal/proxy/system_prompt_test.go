package proxy

import (
	"runtime"
	"strings"
	"testing"
)

func TestFillSystemTemplate_NewPlaceholders(t *testing.T) {
	tmpl := []byte("Date:{{.TodayDate}} Email:{{.UserEmail}} Host:{{.HostAgentName}}\n" +
		"ProjectMd:{{.ClaudeMdProject}} UserMd:{{.ClaudeMdUser}}")
	ctx := TemplateContext{
		TodayDate:       "2026-05-20",
		UserEmail:       "x@y.de",
		HostAgentName:   "Claude Code",
		ClaudeMdProject: "# Rules",
		ClaudeMdUser:    "# User Pref",
	}
	out := string(fillSystemTemplate(tmpl, ctx))
	if !strings.Contains(out, "Date:2026-05-20") {
		t.Errorf("missing TodayDate in output: %s", out)
	}
	if !strings.Contains(out, "Email:x@y.de") {
		t.Errorf("missing UserEmail in output: %s", out)
	}
	if !strings.Contains(out, "Host:Claude Code") {
		t.Errorf("missing HostAgentName in output: %s", out)
	}
	if !strings.Contains(out, "ProjectMd:# Rules") {
		t.Errorf("missing ClaudeMdProject in output: %s", out)
	}
	if !strings.Contains(out, "UserMd:# User Pref") {
		t.Errorf("missing ClaudeMdUser in output: %s", out)
	}
	if strings.Contains(out, "{{.") {
		t.Errorf("unfilled placeholder in output: %s", out)
	}
}

func TestFillSystemTemplate_EmptyFieldsLeaveNothing(t *testing.T) {
	tmpl := []byte("X{{.ClaudeMdProject}}Y{{.ClaudeMdUser}}Z")
	out := string(fillSystemTemplate(tmpl, TemplateContext{}))
	if out != "XYZ" {
		t.Errorf("expected empty fields to produce no output, got: %s", out)
	}
}

func TestFillSystemTemplate_UnknownModelOld(t *testing.T) {
	tpl := []byte("Model: {{.ModelDisplayName}}")
	got := string(fillSystemTemplate(tpl, TemplateContext{ModelID: "unknown-model", ModelDisplayName: "unknown-model"}))
	if !strings.Contains(got, "unknown-model") {
		t.Errorf("expected raw model ID for unknown model, got: %s", got)
	}
}

func TestFillSystemTemplateOld(t *testing.T) {
	tpl := []byte(`Working dir: {{.WorkingDir}}
Git: {{.IsGitRepo}}
Platform: {{.Platform}}
Shell: {{.Shell}}
OS Version: {{.OSVersion}}
Model: {{.ModelDisplayName}}
Model ID: {{.ModelID}}`)

	got := fillSystemTemplate(tpl, TemplateContext{
		WorkingDir:       "/home/test/project",
		IsGitRepo:        "false",
		Platform:         runtime.GOOS,
		Shell:            "bash",
		OSVersion:        osVersionCache,
		ModelID:          "deepseek-v4-pro",
		ModelDisplayName: "DeepSeek V4 Pro",
	})
	sgot := string(got)

	if !strings.Contains(sgot, "Working dir: /home/test/project") {
		t.Errorf("missing WorkingDir: %s", sgot)
	}
	if !strings.Contains(sgot, "Model: DeepSeek V4 Pro") {
		t.Errorf("missing ModelDisplayName: %s", sgot)
	}
	if !strings.Contains(sgot, "Model ID: deepseek-v4-pro") {
		t.Errorf("missing ModelID: %s", sgot)
	}
	if !strings.Contains(sgot, "Platform: "+runtime.GOOS) {
		t.Errorf("missing Platform: %s", sgot)
	}
	if !strings.Contains(sgot, "Git:") {
		t.Errorf("missing IsGitRepo: %s", sgot)
	}
	if !strings.Contains(sgot, "Shell:") {
		t.Errorf("missing Shell: %s", sgot)
	}
	if !strings.Contains(sgot, "OS Version:") {
		t.Errorf("missing OSVersion: %s", sgot)
	}
	if strings.Contains(sgot, "{{.") {
		t.Errorf("unfilled placeholder in output: %s", sgot)
	}
}

func TestFillSystemTemplate_UnknownModel(t *testing.T) {
	tpl := []byte("Model: {{.ModelDisplayName}}")
	got := string(fillSystemTemplate(tpl, TemplateContext{ModelID: "unknown-model", ModelDisplayName: "unknown-model"}))
	if !strings.Contains(got, "unknown-model") {
		t.Errorf("expected raw model ID for unknown model, got: %s", got)
	}
}

func TestLoadSystemPromptFromPath_Missing(t *testing.T) {
		got := loadSystemPromptFromPath("/nonexistent/path")
	if got != nil {
		t.Error("expected nil for missing file")
	}
}

func TestModelDisplayName(t *testing.T) {
	if n := modelDisplayName("deepseek-v4-pro"); n != "DeepSeek V4 Pro" {
		t.Errorf("got %q", n)
	}
	if n := modelDisplayName("unknown"); n != "unknown" {
		t.Errorf("got %q", n)
	}
}

func TestReplaceFirstSystemBlock(t *testing.T) {
	req := map[string]any{
		"system": []any{
			map[string]any{"type": "text", "text": "original system prompt"},
			map[string]any{"type": "text", "text": "second block"},
		},
		"messages": []any{
			map[string]any{"role": "user", "content": "hi"},
		},
	}
	replaceFirstSystemBlock(req, "CUSTOM SYSTEM PROMPT")
	system := req["system"].([]any)
	first := system[0].(map[string]any)
	if first["text"] != "CUSTOM SYSTEM PROMPT" {
		t.Errorf("expected first block replaced, got %v", first)
	}
	second := system[1].(map[string]any)
	if second["text"] != "second block" {
		t.Errorf("expected second block unchanged, got %v", second)
	}
}

func TestReplaceFirstSystemBlock_NoSystemKey(t *testing.T) {
	req := map[string]any{
		"messages": []any{
			map[string]any{"role": "user", "content": "hi"},
		},
	}
	replaceFirstSystemBlock(req, "new prompt")
	// should not panic
}

func TestShellName(t *testing.T) {
	if n := shellName(); n == "" {
		t.Error("shellName returned empty")
	}
}

func TestIsGitRepo(t *testing.T) {
	if g := isGitRepo(""); g != "false" {
		t.Errorf("expected false for empty dir, got %s", g)
	}
	if g := isGitRepo("/nonexistent"); g != "false" {
		t.Errorf("expected false for nonexistent dir, got %s", g)
	}
}

func TestExtractSkillBlock_Found(t *testing.T) {
	req := map[string]any{
		"system": []any{
			map[string]any{"type": "text", "text": `You are an interactive agent.
<available_skills>
  <skill><name>foo</name></skill>
  <skill><name>bar</name></skill>
</available_skills>
Some more text.`},
		},
		"messages": []any{
			map[string]any{"role": "user", "content": "hi"},
		},
	}
	got := extractSkillBlock(req)
	if !strings.Contains(got, "<available_skills>") {
		t.Errorf("expected skill block, got: %s", got)
	}
	if !strings.Contains(got, "<skill><name>foo</name></skill>") {
		t.Errorf("expected foo skill in block, got: %s", got)
	}
	if !strings.HasPrefix(got, "\n\n") {
		t.Errorf("expected leading newlines, got: %s", got)
	}
}

func TestExtractSkillBlock_NotFound(t *testing.T) {
	req := map[string]any{
		"system": []any{
			map[string]any{"type": "text", "text": "Just plain text, no skills here."},
		},
	}
	got := extractSkillBlock(req)
	if got != "" {
		t.Errorf("expected empty, got: %s", got)
	}
}

func TestExtractSkillBlock_NoSystemKey(t *testing.T) {
	req := map[string]any{"messages": []any{}}
	got := extractSkillBlock(req)
	if got != "" {
		t.Errorf("expected empty, got: %s", got)
	}
}

func TestBuildSystemContext_FillsHostAndDate(t *testing.T) {
	ctx := buildSystemContext(buildSystemContextOpts{
		WorkingDir:       "/tmp/test",
		ModelID:          "deepseek-v4-pro",
		ModelDisplayName: "DeepSeek V4 Pro",
		HostAgentName:    "OpenCode",
	})
	if ctx.HostAgentName != "OpenCode" {
		t.Errorf("expected HostAgentName=OpenCode, got %q", ctx.HostAgentName)
	}
	if ctx.TodayDate == "" {
		t.Error("expected TodayDate to be set")
	}
	if ctx.ModelID != "deepseek-v4-pro" {
		t.Errorf("expected ModelID=deepseek-v4-pro, got %q", ctx.ModelID)
	}
	if ctx.ModelDisplayName != "DeepSeek V4 Pro" {
		t.Errorf("expected ModelDisplayName=DeepSeek V4 Pro, got %q", ctx.ModelDisplayName)
	}
	if ctx.Platform == "" {
		t.Error("expected Platform to be set")
	}
	if ctx.Shell == "" {
		t.Error("expected Shell to be set")
	}
}

func TestBuildSystemContext_EmptyWorkingDir(t *testing.T) {
	ctx := buildSystemContext(buildSystemContextOpts{
		WorkingDir:    "",
		ModelID:       "unknown",
		HostAgentName: "Claude Code",
	})
	if ctx.ClaudeMdProject != "" {
		t.Errorf("expected empty ClaudeMdProject for empty dir, got %q", ctx.ClaudeMdProject)
	}
	if ctx.IsGitRepo != "false" {
		t.Errorf("expected IsGitRepo=false for empty dir, got %q", ctx.IsGitRepo)
	}
	if ctx.HostAgentName != "Claude Code" {
		t.Errorf("expected HostAgentName=Claude Code, got %q", ctx.HostAgentName)
	}
}

func TestExpandHome(t *testing.T) {
	got := expandHome("~/test")
	if !strings.HasPrefix(got, "/") {
		t.Errorf("expected absolute path, got %q", got)
	}
	if !strings.HasSuffix(got, "/test") {
		t.Errorf("expected suffix /test, got %q", got)
	}
	got2 := expandHome("/absolute/path")
	if got2 != "/absolute/path" {
		t.Errorf("expected /absolute/path unchanged, got %q", got2)
	}
}

func TestReadFileSafe_Missing(t *testing.T) {
	got := readFileSafe("/nonexistent/file/xyz123.md")
	if got != "" {
		t.Errorf("expected empty string for missing file, got %q", got)
	}
}

func TestReadOSVersion(t *testing.T) {
	ver := readOSVersion()
	if ver == "" {
		t.Error("expected non-empty OS version")
	}
}
