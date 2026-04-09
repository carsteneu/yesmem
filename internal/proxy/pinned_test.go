package proxy

import (
	"testing"
)

// === Task #13: Pinned Memory Classes ===

func TestIsActiveDebug_ErrorFollowedByFix(t *testing.T) {
	msgs := []any{
		map[string]any{"role": "user", "content": "start"},
		map[string]any{
			"role": "user",
			"content": []any{
				map[string]any{
					"type":        "tool_result",
					"tool_use_id": "t1",
					"is_error":    true,
					"content":     "compilation failed",
				},
			},
		},
		map[string]any{"role": "assistant", "content": "I'll fix that error now"},
	}
	// Message at index 1 (error) should be protected
	if !isActiveDebug(msgs, 1) {
		t.Error("error message should be detected as active debug")
	}
	// Message at index 2 (fix response) should also be protected
	if !isActiveDebug(msgs, 2) {
		t.Error("fix response after error should be protected")
	}
}

func TestIsActiveDebug_NormalToolResult(t *testing.T) {
	msgs := []any{
		map[string]any{"role": "user", "content": "start"},
		map[string]any{
			"role": "user",
			"content": []any{
				map[string]any{
					"type":        "tool_result",
					"tool_use_id": "t1",
					"content":     "file contents here",
				},
			},
		},
	}
	if isActiveDebug(msgs, 1) {
		t.Error("normal tool result should NOT be active debug")
	}
}

func TestIsTaskList(t *testing.T) {
	tests := []struct {
		text string
		want bool
	}{
		{"- [ ] Fix the bug\n- [x] Write tests", true},
		{"TODO: implement this", true},
		{"- [ ] First task", true},
		{"just a normal message", false},
		{"", false},
	}

	for _, tt := range tests {
		got := isTaskList(tt.text)
		if got != tt.want {
			t.Errorf("isTaskList(%q) = %v, want %v", tt.text, got, tt.want)
		}
	}
}

func TestIsProtectedExtended(t *testing.T) {
	// Active debug should be protected
	msgs := []any{
		map[string]any{"role": "user", "content": "start"},
		map[string]any{
			"role": "user",
			"content": []any{
				map[string]any{
					"type":        "tool_result",
					"tool_use_id": "t1",
					"is_error":    true,
					"content":     "exit code 1",
				},
			},
		},
	}
	if !isProtectedExtended(msgs, 1, nil) {
		t.Error("error tool_result should be protected")
	}

	// Task list should be protected
	msgs2 := []any{
		map[string]any{"role": "user", "content": "start"},
		map[string]any{"role": "user", "content": "- [ ] Fix bug\n- [ ] Add tests"},
	}
	if !isProtectedExtended(msgs2, 1, nil) {
		t.Error("task list should be protected")
	}
}
