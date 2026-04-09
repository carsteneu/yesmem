package proxy

import (
	"testing"
)

// --- extractWorkingDirectory ---

func TestExtractWorkingDirectory_ClaudeCode(t *testing.T) {
	req := map[string]any{
		"system": []any{
			map[string]any{
				"type": "text",
				"text": "You are Claude.\n\nPrimary working directory: /home/testuser/projects/myapp\nPlatform: linux",
			},
		},
	}
	got := extractWorkingDirectory(req)
	if got != "/home/testuser/projects/myapp" {
		t.Errorf("extractWorkingDirectory = %q, want /home/testuser/projects/myapp", got)
	}
}

func TestExtractWorkingDirectory_CodexCWDTag(t *testing.T) {
	req := map[string]any{
		"messages": []any{
			map[string]any{
				"role": "user",
				"content": []any{
					map[string]any{
						"type": "text",
						"text": "<environment_context>\n  <cwd>/home/testuser/projects/myapp</cwd>\n  <shell>bash</shell>\n</environment_context>",
					},
				},
			},
		},
	}
	got := extractWorkingDirectory(req)
	if got != "/home/testuser/projects/myapp" {
		t.Errorf("extractWorkingDirectory = %q, want /home/testuser/projects/myapp", got)
	}
}

func TestExtractWorkingDirectory_CodexStringContent(t *testing.T) {
	req := map[string]any{
		"messages": []any{
			map[string]any{
				"role":    "user",
				"content": "<environment_context>\n  <cwd>/tmp/project</cwd>\n</environment_context>",
			},
		},
	}
	got := extractWorkingDirectory(req)
	if got != "/tmp/project" {
		t.Errorf("extractWorkingDirectory = %q, want /tmp/project", got)
	}
}

func TestExtractWorkingDirectory_NoMatch(t *testing.T) {
	req := map[string]any{
		"messages": []any{
			map[string]any{"role": "user", "content": "hello"},
		},
	}
	got := extractWorkingDirectory(req)
	if got != "" {
		t.Errorf("extractWorkingDirectory = %q, want empty", got)
	}
}

func TestExtractWorkingDirectory_EmptyRequest(t *testing.T) {
	got := extractWorkingDirectory(map[string]any{})
	if got != "" {
		t.Errorf("extractWorkingDirectory = %q, want empty", got)
	}
}

// --- extractProjectName ---

func TestExtractProjectName_ClaudeCode(t *testing.T) {
	req := map[string]any{
		"system": []any{
			map[string]any{
				"type": "text",
				"text": "Primary working directory: /home/testuser/projects/myapp\n",
			},
		},
	}
	got := extractProjectName(req)
	if got != "myapp" {
		t.Errorf("extractProjectName = %q, want myapp", got)
	}
}

func TestExtractProjectName_Codex(t *testing.T) {
	req := map[string]any{
		"messages": []any{
			map[string]any{
				"role": "user",
				"content": []any{
					map[string]any{
						"type": "text",
						"text": "<environment_context>\n  <cwd>/home/testuser/projects/my-app</cwd>\n</environment_context>",
					},
				},
			},
		},
	}
	got := extractProjectName(req)
	if got != "my-app" {
		t.Errorf("extractProjectName = %q, want my-app", got)
	}
}

func TestExtractProjectName_Empty(t *testing.T) {
	got := extractProjectName(map[string]any{})
	if got != "" {
		t.Errorf("extractProjectName = %q, want empty", got)
	}
}

// --- DeriveThreadID ---

func TestDeriveThreadID_ClaudeCode(t *testing.T) {
	req := map[string]any{
		"system": []any{
			map[string]any{
				"type": "text",
				"text": "Primary working directory: /home/testuser/projects/myapp\nIs a git repository: true",
			},
		},
		"messages": []any{
			map[string]any{"role": "user", "content": "hello"},
		},
	}
	id := DeriveThreadID(req)
	if id == "" {
		t.Fatal("DeriveThreadID returned empty for Claude Code request")
	}
	if len(id) != 16 {
		t.Errorf("DeriveThreadID len = %d, want 16", len(id))
	}

	// Same request → same ID (stability)
	id2 := DeriveThreadID(req)
	if id != id2 {
		t.Errorf("DeriveThreadID not stable: %q != %q", id, id2)
	}
}

func TestDeriveThreadID_Codex_DeveloperMessage(t *testing.T) {
	req := map[string]any{
		"messages": []any{
			map[string]any{
				"role":    "developer",
				"content": "You are Codex, a coding agent based on GPT-5.",
			},
			map[string]any{
				"role":    "user",
				"content": "<environment_context>\n  <cwd>/home/testuser/projects/myapp</cwd>\n</environment_context>",
			},
			map[string]any{
				"role":    "user",
				"content": "hello",
			},
		},
	}
	id := DeriveThreadID(req)
	if id == "" {
		t.Fatal("DeriveThreadID returned empty for Codex request")
	}
	if len(id) != 16 {
		t.Errorf("DeriveThreadID len = %d, want 16", len(id))
	}

	// Stability
	id2 := DeriveThreadID(req)
	if id != id2 {
		t.Errorf("DeriveThreadID not stable: %q != %q", id, id2)
	}
}

func TestDeriveThreadID_Codex_FallbackToCWD(t *testing.T) {
	// No developer/system message → falls back to <cwd> extraction
	req := map[string]any{
		"messages": []any{
			map[string]any{
				"role": "user",
				"content": []any{
					map[string]any{
						"type": "text",
						"text": "<environment_context>\n  <cwd>/home/testuser/projects/myapp</cwd>\n</environment_context>",
					},
				},
			},
		},
	}
	id := DeriveThreadID(req)
	if id == "" {
		t.Fatal("DeriveThreadID returned empty — CWD fallback failed")
	}
	if len(id) != 16 {
		t.Errorf("DeriveThreadID len = %d, want 16", len(id))
	}
}

func TestDeriveThreadID_DifferentProjects(t *testing.T) {
	req1 := map[string]any{
		"system": []any{
			map[string]any{"type": "text", "text": "Primary working directory: /home/testuser/project-a\n"},
		},
		"messages": []any{
			map[string]any{"role": "user", "content": "hello"},
		},
	}
	req2 := map[string]any{
		"system": []any{
			map[string]any{"type": "text", "text": "Primary working directory: /home/testuser/project-b\n"},
		},
		"messages": []any{
			map[string]any{"role": "user", "content": "hello"},
		},
	}
	id1 := DeriveThreadID(req1)
	id2 := DeriveThreadID(req2)
	if id1 == id2 {
		t.Errorf("different projects got same thread ID: %q", id1)
	}
}

func TestDeriveThreadID_Empty(t *testing.T) {
	id := DeriveThreadID(map[string]any{})
	if id != "" {
		t.Errorf("DeriveThreadID for empty req = %q, want empty", id)
	}
}

// --- isOpenAIProxySubagent ---

func TestIsOpenAIProxySubagent_SDKEntrypoint(t *testing.T) {
	req := map[string]any{
		"system": []any{
			map[string]any{"type": "text", "text": "cc_entrypoint=sdk-ts some other stuff"},
		},
	}
	msgs := []any{map[string]any{"role": "user", "content": "hello"}}
	if !isOpenAIProxySubagent(msgs, req) {
		t.Error("sdk-ts entrypoint should be detected as subagent")
	}
}

func TestIsOpenAIProxySubagent_CLIEntrypoint(t *testing.T) {
	req := map[string]any{
		"system": []any{
			map[string]any{"type": "text", "text": "cc_entrypoint=cli some other stuff"},
		},
	}
	msgs := []any{map[string]any{"role": "user", "content": "hello"}}
	if isOpenAIProxySubagent(msgs, req) {
		t.Error("cli entrypoint should NOT be detected as subagent")
	}
}

func TestIsOpenAIProxySubagent_HaikuModel(t *testing.T) {
	req := map[string]any{
		"model": "claude-haiku-4-5-20251001",
	}
	msgs := []any{map[string]any{"role": "user", "content": "hello"}}
	if !isOpenAIProxySubagent(msgs, req) {
		t.Error("haiku model should be detected as subagent")
	}
}

func TestIsOpenAIProxySubagent_NormalRequest(t *testing.T) {
	req := map[string]any{
		"model": "gpt-5.4",
	}
	msgs := []any{map[string]any{"role": "user", "content": "hello"}}
	if isOpenAIProxySubagent(msgs, req) {
		t.Error("normal gpt-5.4 request should NOT be subagent")
	}
}

func TestIsOpenAIProxySubagent_EmptyMessages(t *testing.T) {
	if isOpenAIProxySubagent(nil, nil) {
		t.Error("nil messages should not be subagent")
	}
	if isOpenAIProxySubagent([]any{}, nil) {
		t.Error("empty messages should not be subagent")
	}
}

func TestIsOpenAIProxySubagent_NoSystem(t *testing.T) {
	// Codex request: no system blocks, no haiku → not subagent
	req := map[string]any{
		"model": "gpt-5.4",
		"messages": []any{
			map[string]any{"role": "user", "content": "hello"},
		},
	}
	msgs := []any{map[string]any{"role": "user", "content": "hello"}}
	if isOpenAIProxySubagent(msgs, req) {
		t.Error("Codex request without system should NOT be subagent")
	}
}

// --- Collapsed message roundtrip ---

func TestTranslateResponses_CollapsedArchiveBlock(t *testing.T) {
	// Simulate: Pipeline collapses old messages into an archive block.
	// The archive block is in Anthropic internal format. It must survive
	// reverse-translation back to Responses API format.
	anthReq := map[string]any{
		"model": "gpt-5.4",
		"system": []any{
			map[string]any{"type": "text", "text": "You are helpful."},
		},
		"messages": []any{
			// Collapsed archive block (produced by CollapseOldMessages)
			map[string]any{
				"role": "user",
				"content": "[Archiv: Messages 1-50 (50 msgs) — collapsed]\nTools: Bash(12), Read(8)\nFiles: proxy.go(5), handler.go(3)",
			},
			// Assistant acknowledgment
			map[string]any{
				"role": "assistant",
				"content": "Understood, I have the context from the archived messages.",
			},
			// Current conversation
			map[string]any{"role": "user", "content": "What's the status?"},
		},
	}

	respReq, err := translateAnthropicToResponses(anthReq)
	if err != nil {
		t.Fatalf("translateAnthropicToResponses: %v", err)
	}

	input, ok := respReq["input"].([]any)
	if !ok {
		t.Fatal("input not []any")
	}
	if len(input) != 3 {
		t.Fatalf("input = %d, want 3", len(input))
	}

	// Archive block should survive as user message
	m0, _ := input[0].(map[string]any)
	if m0["role"] != "user" {
		t.Errorf("input[0].role = %v, want user", m0["role"])
	}

	// Check archive text is preserved
	var archiveText string
	switch c := m0["content"].(type) {
	case string:
		archiveText = c
	case []any:
		if len(c) > 0 {
			if block, ok := c[0].(map[string]any); ok {
				archiveText, _ = block["text"].(string)
			}
		}
	}
	if archiveText == "" {
		t.Error("archive block text lost in translation")
	}
	if len(archiveText) < 20 {
		t.Errorf("archive text too short: %q", archiveText)
	}
}

func TestTranslateResponses_CollapsedWithToolCalls(t *testing.T) {
	// Collapsed archive + fresh tool call interaction
	anthReq := map[string]any{
		"model": "gpt-5.4",
		"messages": []any{
			map[string]any{
				"role":    "user",
				"content": "[Archiv: Messages 1-20]\nTools: Read(5)",
			},
			map[string]any{
				"role":    "assistant",
				"content": "Got it.",
			},
			map[string]any{"role": "user", "content": "Read /tmp/foo"},
			map[string]any{"role": "assistant", "content": []any{
				map[string]any{
					"type": "tool_use", "id": "call_1",
					"name": "read", "input": map[string]any{"path": "/tmp/foo"},
				},
			}},
			map[string]any{"role": "user", "content": []any{
				map[string]any{
					"type": "tool_result", "tool_use_id": "call_1",
					"content": "file contents",
				},
			}},
		},
	}

	respReq, err := translateAnthropicToResponses(anthReq)
	if err != nil {
		t.Fatalf("translate: %v", err)
	}

	input, _ := respReq["input"].([]any)
	// archive(user), ack(assistant), user, function_call, function_call_output
	if len(input) != 5 {
		t.Fatalf("input = %d, want 5", len(input))
	}

	// Verify tool call survived
	fc, _ := input[3].(map[string]any)
	if fc["type"] != "function_call" {
		t.Errorf("input[3].type = %v, want function_call", fc["type"])
	}
	fco, _ := input[4].(map[string]any)
	if fco["type"] != "function_call_output" {
		t.Errorf("input[4].type = %v, want function_call_output", fco["type"])
	}
}
