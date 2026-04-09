package proxy

import (
	"strings"
	"testing"
	"time"
)

func TestInjectSkillsAfterArchive(t *testing.T) {
	// Simulate post-collapse messages: [system, archive, recent...]
	msgs := []any{
		map[string]any{"role": "system", "content": "system prompt"},
		map[string]any{"role": "user", "content": "[Archiv: Messages 1-50 (50 msgs)]"},
		map[string]any{"role": "assistant", "content": "continuing work"},
		map[string]any{"role": "user", "content": "next task"},
	}

	skills := []skillBlock{
		{Name: "tdd", Content: "# TDD\n\nRed green refactor."},
		{Name: "debugging", Content: "# Debugging\n\nSystematic approach."},
	}

	result := injectSkillsAfterArchive(msgs, skills)

	// Expected: system, archive, skill_user, skill_ack, recent...
	// = 4 original + 2 inserted = 6
	if len(result) != 6 {
		t.Fatalf("expected 6 messages, got %d", len(result))
	}

	// Index 0: system (unchanged)
	sys := result[0].(map[string]any)
	if sys["role"] != "system" {
		t.Error("index 0 should be system")
	}

	// Index 1: archive (unchanged)
	archive := result[1].(map[string]any)
	archiveContent, _ := archive["content"].(string)
	if !strings.Contains(archiveContent, "Archiv") {
		t.Error("index 1 should be archive block")
	}

	// Index 2: skill injection (user message)
	skillMsg := result[2].(map[string]any)
	if skillMsg["role"] != "user" {
		t.Errorf("index 2 should be user, got %v", skillMsg["role"])
	}
	skillContent, _ := skillMsg["content"].(string)
	if !strings.Contains(skillContent, "[skill:tdd]") {
		t.Error("skill injection should contain tdd skill")
	}
	if !strings.Contains(skillContent, "[skill:debugging]") {
		t.Error("skill injection should contain debugging skill")
	}

	// Index 3: assistant ack
	ack := result[3].(map[string]any)
	if ack["role"] != "assistant" {
		t.Errorf("index 3 should be assistant ack, got %v", ack["role"])
	}

	// Index 4-5: original recent messages
	recent1 := result[4].(map[string]any)
	if recent1["role"] != "assistant" {
		t.Error("index 4 should be original assistant message")
	}
	recent2 := result[5].(map[string]any)
	if recent2["role"] != "user" {
		t.Error("index 5 should be original user message")
	}
}

func TestInjectSkillsAfterArchive_NoSkills(t *testing.T) {
	msgs := []any{
		map[string]any{"role": "system", "content": "system"},
		map[string]any{"role": "user", "content": "[Archiv: Messages 1-50]"},
		map[string]any{"role": "user", "content": "recent"},
	}

	result := injectSkillsAfterArchive(msgs, nil)

	// No skills — messages unchanged
	if len(result) != 3 {
		t.Errorf("expected 3 messages (no change), got %d", len(result))
	}
}

func TestInjectSkillsAfterArchive_NoArchive(t *testing.T) {
	// No collapse happened — no archive block
	msgs := []any{
		map[string]any{"role": "system", "content": "system"},
		map[string]any{"role": "user", "content": "hello"},
		map[string]any{"role": "assistant", "content": "hi"},
	}

	skills := []skillBlock{
		{Name: "tdd", Content: "# TDD"},
	}

	result := injectSkillsAfterArchive(msgs, skills)

	// No archive block found — messages unchanged
	if len(result) != 3 {
		t.Errorf("expected 3 messages (no archive), got %d", len(result))
	}
}

func TestDetectExistingSkillBlocks(t *testing.T) {
	msgs := []any{
		map[string]any{"role": "system", "content": "system"},
		map[string]any{"role": "user", "content": "[Archiv: Messages 1-50]"},
		map[string]any{"role": "user", "content": "[skill:tdd]\n# TDD\n[/skill:tdd]\n\n[skill:debugging]\n# Debug\n[/skill:debugging]"},
		map[string]any{"role": "assistant", "content": "Skills loaded."},
		map[string]any{"role": "user", "content": "recent"},
	}

	names := detectExistingSkillBlocks(msgs)
	if len(names) != 2 {
		t.Fatalf("expected 2 existing skill blocks, got %d", len(names))
	}
	if !names["tdd"] || !names["debugging"] {
		t.Errorf("expected tdd + debugging, got %v", names)
	}
}

func TestCollapseWithSkillReinjection(t *testing.T) {
	// Build messages with a skill block that should survive collapse
	msgs := make([]any, 20)
	original := make([]any, 20)
	msgs[0] = map[string]any{"role": "system", "content": "system"}
	original[0] = msgs[0]

	for i := 1; i < 20; i++ {
		role := "user"
		if i%2 == 0 {
			role = "assistant"
		}
		msg := map[string]any{"role": role, "content": "msg " + string(rune('A'+i))}
		msgs[i] = msg
		original[i] = msg
	}

	// Collapse at index 15
	result := CollapseOldMessages(msgs, original, 15, time.Now(), time.Now(), nil, nil)

	// Verify basic collapse worked
	if len(result) < 3 {
		t.Fatalf("expected at least 3 messages after collapse, got %d", len(result))
	}

	// Now inject skills after archive
	skills := []skillBlock{{Name: "tdd", Content: "# TDD\n\nContent."}}
	withSkills := injectSkillsAfterArchive(result, skills)

	// Should have 2 more messages (skill user + ack)
	if len(withSkills) != len(result)+2 {
		t.Errorf("expected %d messages after skill injection, got %d", len(result)+2, len(withSkills))
	}

	// Verify skill block is at index 2 (after system + archive)
	skillMsg := withSkills[2].(map[string]any)
	skillContent, _ := skillMsg["content"].(string)
	if !strings.Contains(skillContent, "[skill:tdd]") {
		t.Error("skill block should be at index 2")
	}
}
