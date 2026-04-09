package proxy

import (
	"fmt"
	"strings"
	"testing"
)

func TestStripReminders_KeepsLastMessage(t *testing.T) {
	msgs := []any{
		map[string]any{"role": "system", "content": "system prompt"},
		map[string]any{"role": "user", "content": "hello <system-reminder>\nMANDATORY SKILL ACTIVATION SEQUENCE\nStep 1...\n</system-reminder> world"},
		map[string]any{"role": "assistant", "content": "hi"},
		map[string]any{"role": "user", "content": "test <system-reminder>\nMANDATORY SKILL ACTIVATION SEQUENCE\nStep 1...\n</system-reminder> again"},
	}

	result := StripReminders(msgs, 10) // requestIdx=10, all messages are old

	// Last user message should keep its reminder
	lastContent, _ := result[3].(map[string]any)["content"].(string)
	if !strings.Contains(lastContent, "MANDATORY SKILL") {
		t.Error("last message should keep its system-reminder intact")
	}
	if !strings.Contains(lastContent, "again") {
		t.Error("last message should keep its non-reminder content")
	}
}

func TestStripReminders_ReplacesOldSkillCheck(t *testing.T) {
	msgs := []any{
		map[string]any{"role": "system", "content": "system prompt"},
		map[string]any{"role": "user", "content": "hello <system-reminder>\nUserPromptSubmit hook success: INSTRUCTION: MANDATORY SKILL ACTIVATION SEQUENCE\nStep 1...\n</system-reminder> world"},
		map[string]any{"role": "assistant", "content": "hi"},
		map[string]any{"role": "user", "content": "latest"},
	}

	result := StripReminders(msgs, 20)

	content, _ := result[1].(map[string]any)["content"].(string)
	if strings.Contains(content, "MANDATORY SKILL") {
		t.Error("old skill-check reminder should be stripped")
	}
	if !strings.Contains(content, "[skill-check]") {
		t.Errorf("old skill-check should be replaced with keyword, got: %s", content)
	}
	if !strings.Contains(content, "hello") || !strings.Contains(content, "world") {
		t.Error("non-reminder content should be preserved")
	}
}

func TestStripReminders_ReplacesOldYesMem(t *testing.T) {
	msgs := []any{
		map[string]any{"role": "system", "content": "system prompt"},
		map[string]any{"role": "user", "content": "<system-reminder>\nPreToolUse:Edit hook additional context: YesMem Gotchas:\n- Deploy-Gotcha: yesmem proxy...\n- Briefing...\n</system-reminder>"},
		map[string]any{"role": "assistant", "content": "ok"},
		map[string]any{"role": "user", "content": "latest"},
	}

	result := StripReminders(msgs, 20)

	content, _ := result[1].(map[string]any)["content"].(string)
	if strings.Contains(content, "Deploy-Gotcha") {
		t.Error("old yesmem gotchas should be stripped")
	}
	if !strings.Contains(content, "[yesmem-context]") {
		t.Errorf("should be replaced with keyword, got: %s", content)
	}
}

func TestStripReminders_KeepsRecentFileChange(t *testing.T) {
	diff := "Note: /home/user/memory/yesmem/internal/proxy/proxy.go was modified, either by the user or by a linter.\nHere are the relevant changes (shown with line numbers):\n     1→package proxy\n     2→import (\n     3→    \"bytes\"\n"
	msgs := []any{
		map[string]any{"role": "system", "content": "system prompt"},
		map[string]any{"role": "user", "content": fmt.Sprintf("do it <system-reminder>\n%s\n</system-reminder>", diff)},
		map[string]any{"role": "assistant", "content": "done"},
		map[string]any{"role": "user", "content": "latest"},
	}

	// requestIdx=2, message is from request 1 → age=1, < 3 → keep full
	result := StripReminders(msgs, 2)

	content, _ := result[1].(map[string]any)["content"].(string)
	if !strings.Contains(content, "package proxy") {
		t.Error("recent file-change diff should be kept intact")
	}
}

func TestStripReminders_SummarizesOldFileChange(t *testing.T) {
	diff := "Note: /home/user/memory/yesmem/internal/proxy/proxy.go was modified, either by the user or by a linter. This change was intentional, so make sure to take it into account as you proceed (ie. don't revert it unless the user asks you to). Don't tell the user this, since they are already aware. Here are the relevant changes (shown with line numbers):\n     1→package proxy\n     2→import (\n     3→    \"bytes\"\n     95→\t\t\t\tResponseHeaderTimeout: 60 * time.Second,\n     96→\t\t\t},\n"
	msgs := []any{
		map[string]any{"role": "system", "content": "system prompt"},
		map[string]any{"role": "user", "content": fmt.Sprintf("fix it <system-reminder>\n%s\n</system-reminder>", diff)},
		map[string]any{"role": "assistant", "content": "done"},
		map[string]any{"role": "user", "content": "latest"},
	}

	// requestIdx=10, message from request 1 → age=9, 3-10 → summarize with lines
	result := StripReminders(msgs, 10)

	content, _ := result[1].(map[string]any)["content"].(string)
	if strings.Contains(content, "package proxy") {
		t.Error("old file-change diff body should be stripped")
	}
	if !strings.Contains(content, "[file-changed: proxy.go") {
		t.Errorf("should contain file-changed keyword, got: %s", content)
	}
	if !strings.Contains(content, "fix it") {
		t.Error("non-reminder content should be preserved")
	}
}

func TestStripReminders_VeryOldFileChange(t *testing.T) {
	diff := "Note: /home/user/memory/yesmem/internal/proxy/proxy.go was modified, either by the user or by a linter. Here are the relevant changes (shown with line numbers):\n     1→package proxy\n     95→code\n"
	msgs := []any{
		map[string]any{"role": "system", "content": "system prompt"},
		map[string]any{"role": "user", "content": fmt.Sprintf("<system-reminder>\n%s\n</system-reminder>", diff)},
		map[string]any{"role": "assistant", "content": "ok"},
		map[string]any{"role": "user", "content": "latest"},
	}

	// requestIdx=20, message from request 1 → age=19, > 10 → minimal
	result := StripReminders(msgs, 20)

	content, _ := result[1].(map[string]any)["content"].(string)
	if !strings.Contains(content, "[file-changed: proxy.go]") {
		t.Errorf("very old file-change should be minimal, got: %s", content)
	}
}

func TestStripReminders_KeepsSessionStart(t *testing.T) {
	msgs := []any{
		map[string]any{"role": "system", "content": "system prompt"},
		map[string]any{"role": "user", "content": "hi <system-reminder>\nSessionStart:startup hook success: Success\n</system-reminder>"},
		map[string]any{"role": "assistant", "content": "hello"},
		map[string]any{"role": "user", "content": "latest"},
	}

	result := StripReminders(msgs, 50)

	content, _ := result[1].(map[string]any)["content"].(string)
	if !strings.Contains(content, "SessionStart") {
		t.Error("session-start reminders should never be stripped")
	}
}

func TestStripReminders_RemovesOldTaskReminder(t *testing.T) {
	msgs := []any{
		map[string]any{"role": "system", "content": "system prompt"},
		map[string]any{"role": "user", "content": "<system-reminder>\nThe task tools haven't been used recently. If you're working on tasks...\n</system-reminder>"},
		map[string]any{"role": "assistant", "content": "ok"},
		map[string]any{"role": "user", "content": "latest"},
	}

	result := StripReminders(msgs, 20)

	content, _ := result[1].(map[string]any)["content"].(string)
	if strings.Contains(content, "task tools") {
		t.Error("old task reminder should be removed")
	}
}

func TestStripReminders_MixedContent(t *testing.T) {
	msgs := []any{
		map[string]any{"role": "system", "content": "system prompt"},
		map[string]any{"role": "user", "content": "first part <system-reminder>\nMANDATORY SKILL ACTIVATION\n</system-reminder> middle <system-reminder>\nPreToolUse:Edit hook additional context: YesMem Gotchas:\n- gotcha1\n</system-reminder> last part"},
		map[string]any{"role": "assistant", "content": "ok"},
		map[string]any{"role": "user", "content": "latest"},
	}

	result := StripReminders(msgs, 20)

	content, _ := result[1].(map[string]any)["content"].(string)
	if !strings.Contains(content, "first part") || !strings.Contains(content, "middle") || !strings.Contains(content, "last part") {
		t.Errorf("non-reminder content should all be preserved, got: %s", content)
	}
	if strings.Contains(content, "MANDATORY") || strings.Contains(content, "gotcha1") {
		t.Error("reminder bodies should be stripped")
	}
}

func TestStripRemindersFromText_Direct(t *testing.T) {
	text := "hello <system-reminder>\nUserPromptSubmit hook success: INSTRUCTION: MANDATORY SKILL ACTIVATION SEQUENCE\n</system-reminder> world"
	result := stripRemindersFromText(text, 20)
	t.Logf("input:  %q", text)
	t.Logf("output: %q", result)
	if strings.Contains(result, "MANDATORY") {
		t.Errorf("should have stripped MANDATORY, got: %q", result)
	}
}

func TestStripReminders_ContentBlocks(t *testing.T) {
	// Messages can have []any content (block format) not just string
	msgs := []any{
		map[string]any{"role": "system", "content": "system prompt"},
		map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "text", "text": "hello <system-reminder>\nUserPromptSubmit hook success: MANDATORY SKILL ACTIVATION\n</system-reminder> world"},
			map[string]any{"type": "text", "text": "clean block"},
		}},
		map[string]any{"role": "assistant", "content": "ok"},
		map[string]any{"role": "user", "content": "latest"},
	}

	result := StripReminders(msgs, 20)

	newMsg, _ := result[1].(map[string]any)
	newBlocks, ok := newMsg["content"].([]any)
	if !ok {
		t.Fatalf("expected []any content after strip, got %T", newMsg["content"])
	}
	firstBlock, _ := newBlocks[0].(map[string]any)
	text, _ := firstBlock["text"].(string)
	t.Logf("stripped block text: %q", text)
	if strings.Contains(text, "MANDATORY") {
		t.Errorf("reminder in block content should be stripped, got: %q", text)
	}
	if !strings.Contains(text, "hello") || !strings.Contains(text, "world") {
		t.Error("non-reminder text in block should be preserved")
	}
}

func TestStripReminders_RemovesOldSkillEval(t *testing.T) {
	msgs := []any{
		map[string]any{"role": "system", "content": "system prompt"},
		map[string]any{"role": "user", "content": "fix bug <system-reminder>\n[skill-eval]\nINSTRUCTION: MANDATORY SKILL ACTIVATION SEQUENCE\nStep 1 — EVALUATE: For each available skill...\nStep 2 — ACTIVATE: IF any YES → Use Skill(name)...\nStep 3 — PROCEED: Only after evaluation is complete.\n[/skill-eval]\n</system-reminder>"},
		map[string]any{"role": "assistant", "content": "ok"},
		map[string]any{"role": "user", "content": "latest"},
	}

	result := StripReminders(msgs, 20)

	content, _ := result[1].(map[string]any)["content"].(string)
	if strings.Contains(content, "MANDATORY SKILL ACTIVATION") {
		t.Error("old skill-eval reminder should be stripped")
	}
	if !strings.Contains(content, "[skill-check]") {
		t.Errorf("old skill-eval should be replaced with [skill-check], got: %s", content)
	}
	if !strings.Contains(content, "fix bug") {
		t.Error("non-reminder content should be preserved")
	}
}

func TestStripReminders_PreservesNarrativeBlock(t *testing.T) {
	// The session narrative that comes via hooks should be preserved
	msgs := []any{
		map[string]any{"role": "system", "content": "system prompt"},
		map[string]any{"role": "user", "content": "start <system-reminder>\nSessionStart:resume hook success: Success\n</system-reminder><system-reminder>\nDu bist die Fortsetzung von 3 sessions:\n\n[vor 2 Min] Session summary...\nPuls: hoch · reflektiv · Nächstes: X\n</system-reminder>"},
		map[string]any{"role": "assistant", "content": "hi"},
		map[string]any{"role": "user", "content": "latest"},
	}

	result := StripReminders(msgs, 50)

	content, _ := result[1].(map[string]any)["content"].(string)
	if !strings.Contains(content, "Fortsetzung von 3 sessions") {
		t.Error("narrative/briefing block should never be stripped")
	}
	if !strings.Contains(content, "SessionStart") {
		t.Error("session-start should be preserved")
	}
}
