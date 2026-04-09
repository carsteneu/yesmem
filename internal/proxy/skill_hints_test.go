package proxy

import (
	"strings"
	"testing"
)

const skillHintStart = "[skill-hint]"
const skillHintEnd = "[/skill-hint]"

func TestStripSkillHints_RemovesFromOlderMessages(t *testing.T) {
	msgs := []any{
		map[string]any{"role": "user", "content": "first message"},
		map[string]any{"role": "assistant", "content": "ok"},
		map[string]any{"role": "user", "content": "second message\n" + skillHintStart + "\nMöglicherweise relevant: /systematic-debugging\n" + skillHintEnd},
		map[string]any{"role": "assistant", "content": "I'll use systematic-debugging"},
		map[string]any{"role": "user", "content": "current message"}, // last user msg — not stripped
	}

	result := stripSkillHints(msgs)

	// Message at index 2 should have hint stripped
	msg2 := result[2].(map[string]any)
	content2, _ := msg2["content"].(string)
	if strings.Contains(content2, skillHintStart) {
		t.Errorf("old skill hint should be stripped, got %q", content2)
	}
	if !strings.Contains(content2, "second message") {
		t.Error("original content should be preserved")
	}

	// Last user message should be untouched
	msg4 := result[4].(map[string]any)
	content4, _ := msg4["content"].(string)
	if content4 != "current message" {
		t.Errorf("last message should be unchanged, got %q", content4)
	}
}

func TestStripSkillHints_HandlesContentBlocks(t *testing.T) {
	msgs := []any{
		map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "text", "text": "user query"},
			map[string]any{"type": "text", "text": "\n" + skillHintStart + "\nRelevant: /tdd\n" + skillHintEnd},
		}},
		map[string]any{"role": "assistant", "content": "ok"},
		map[string]any{"role": "user", "content": "current"},
	}

	result := stripSkillHints(msgs)

	// First user message should have hint block removed
	msg0 := result[0].(map[string]any)
	blocks, ok := msg0["content"].([]any)
	if !ok {
		t.Fatal("expected content blocks")
	}
	for _, block := range blocks {
		b, _ := block.(map[string]any)
		text, _ := b["text"].(string)
		if strings.Contains(text, skillHintStart) {
			t.Errorf("skill hint block should be stripped, got %q", text)
		}
	}
}

func TestStripSkillHints_NoHints_Passthrough(t *testing.T) {
	msgs := []any{
		map[string]any{"role": "user", "content": "no hints here"},
		map[string]any{"role": "assistant", "content": "ok"},
	}

	result := stripSkillHints(msgs)
	msg0 := result[0].(map[string]any)
	if msg0["content"] != "no hints here" {
		t.Error("message without hints should be unchanged")
	}
}

func TestBuildSkillEvalBlock(t *testing.T) {
	block := buildSkillEvalBlock()

	if !strings.Contains(block, "[skill-eval]") {
		t.Error("block should have start marker")
	}
	if !strings.Contains(block, "[/skill-eval]") {
		t.Error("block should have end marker")
	}
	if !strings.Contains(block, "MANDATORY SKILL ACTIVATION") {
		t.Error("block should contain mandatory instruction")
	}
	if !strings.Contains(block, "EVALUATE") || !strings.Contains(block, "ACTIVATE") || !strings.Contains(block, "PROCEED") {
		t.Error("block should contain all three steps")
	}
	if !strings.Contains(block, "Skill(") {
		t.Error("block should reference Skill() tool")
	}
	if !strings.Contains(block, "/command") {
		t.Error("block should mention slash commands")
	}
}

func TestSkillHintTracker_BasicFlow(t *testing.T) {
	tracker := newSkillHintTracker()

	// Initially no skills active
	if tracker.isActive("thread1", "tdd") {
		t.Error("should not be active initially")
	}

	// Mark skill as active
	tracker.markActive("thread1", "tdd")
	if !tracker.isActive("thread1", "tdd") {
		t.Error("should be active after marking")
	}

	// Different thread should not be affected
	if tracker.isActive("thread2", "tdd") {
		t.Error("different thread should not be active")
	}

	// Get active list
	active := tracker.activeSkills("thread1")
	if len(active) != 1 || active[0] != "tdd" {
		t.Errorf("active skills = %v, want [tdd]", active)
	}
}

func TestDetectSkillActivation(t *testing.T) {
	// Message history where Claude used the Skill tool
	msgs := []any{
		map[string]any{"role": "user", "content": "implement the feature"},
		map[string]any{"role": "assistant", "content": []any{
			map[string]any{"type": "text", "text": "I'll use the TDD skill."},
			map[string]any{"type": "tool_use", "id": "tu_1", "name": "Skill", "input": map[string]any{"skill": "test-driven-development"}},
		}},
		map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "tool_result", "tool_use_id": "tu_1", "content": "Skill loaded: test-driven-development"},
		}},
		map[string]any{"role": "user", "content": "now do the thing"},
	}

	skills := detectSkillActivations(msgs)
	if len(skills) != 1 {
		t.Fatalf("expected 1 skill activation, got %d", len(skills))
	}
	if skills[0] != "test-driven-development" {
		t.Errorf("expected test-driven-development, got %q", skills[0])
	}
}

func TestDetectSkillActivation_NoSkills(t *testing.T) {
	msgs := []any{
		map[string]any{"role": "user", "content": "hello"},
		map[string]any{"role": "assistant", "content": []any{
			map[string]any{"type": "tool_use", "id": "tu_1", "name": "Read", "input": map[string]any{"file_path": "/app/main.go"}},
		}},
	}

	skills := detectSkillActivations(msgs)
	if len(skills) != 0 {
		t.Errorf("expected 0 skill activations, got %d: %v", len(skills), skills)
	}
}

func TestDetectSkillActivation_Multiple(t *testing.T) {
	msgs := []any{
		map[string]any{"role": "assistant", "content": []any{
			map[string]any{"type": "tool_use", "id": "tu_1", "name": "Skill", "input": map[string]any{"skill": "brainstorming"}},
		}},
		map[string]any{"role": "user", "content": "ok"},
		map[string]any{"role": "assistant", "content": []any{
			map[string]any{"type": "tool_use", "id": "tu_2", "name": "Skill", "input": map[string]any{"skill": "tdd"}},
		}},
	}

	skills := detectSkillActivations(msgs)
	if len(skills) != 2 {
		t.Fatalf("expected 2 skill activations, got %d", len(skills))
	}
	names := map[string]bool{}
	for _, s := range skills {
		names[s] = true
	}
	if !names["brainstorming"] || !names["tdd"] {
		t.Errorf("expected brainstorming + tdd, got %v", skills)
	}
}

func TestBuildSkillInjectionBlock(t *testing.T) {
	content := "# TDD Skill\n\nWrite tests first."
	block := buildSkillInjectionBlock("test-driven-development", content)

	if !strings.Contains(block, "[skill:test-driven-development]") {
		t.Error("block should have skill start marker")
	}
	if !strings.Contains(block, "[/skill:test-driven-development]") {
		t.Error("block should have skill end marker")
	}
	if !strings.Contains(block, content) {
		t.Error("block should contain full skill content")
	}
}
