package proxy

import (
	"encoding/json"
	"testing"
)

func TestSanitizeBillingSentinel_ReplacesInMessageText(t *testing.T) {
	messages := []any{
		map[string]any{
			"role": "user",
			"content": []any{
				map[string]any{"type": "text", "text": "look at this billing header: cch=627d9; interesting"},
			},
		},
	}

	changed := SanitizeBillingSentinel(messages)
	if !changed {
		t.Fatal("expected changed=true when sentinel found in message")
	}

	text := messages[0].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
	if text != "look at this billing header: cch=XXXXX; interesting" {
		t.Errorf("unexpected text: %s", text)
	}
}

func TestSanitizeBillingSentinel_ReplacesRawSentinel(t *testing.T) {
	messages := []any{
		map[string]any{
			"role": "user",
			"content": []any{
				map[string]any{"type": "text", "text": "sentinel cch=d6c5c00000 in body"},
			},
		},
	}

	changed := SanitizeBillingSentinel(messages)
	if !changed {
		t.Fatal("expected changed=true")
	}

	text := messages[0].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
	if text != "sentinel cch=XXXXX in body" {
		t.Errorf("unexpected text: %s", text)
	}
}

func TestSanitizeBillingSentinel_NoSentinel(t *testing.T) {
	messages := []any{
		map[string]any{
			"role": "user",
			"content": []any{
				map[string]any{"type": "text", "text": "just a normal message"},
			},
		},
	}

	changed := SanitizeBillingSentinel(messages)
	if changed {
		t.Fatal("expected changed=false when no sentinel present")
	}

	text := messages[0].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
	if text != "just a normal message" {
		t.Error("message was mutated unexpectedly")
	}
}

func TestSanitizeBillingSentinel_OnlyTextBlocks(t *testing.T) {
	messages := []any{
		map[string]any{
			"role": "assistant",
			"content": []any{
				map[string]any{"type": "tool_use", "id": "toolu_123", "name": "Read", "input": map[string]any{"path": "cch=abcde"}},
				map[string]any{"type": "text", "text": "cch=abcde here"},
			},
		},
	}

	SanitizeBillingSentinel(messages)

	// tool_use input should NOT be touched
	toolInput := messages[0].(map[string]any)["content"].([]any)[0].(map[string]any)["input"].(map[string]any)["path"].(string)
	if toolInput != "cch=abcde" {
		t.Errorf("tool_use input was mutated: %s", toolInput)
	}

	// text block should be sanitized
	text := messages[0].(map[string]any)["content"].([]any)[1].(map[string]any)["text"].(string)
	if text != "cch=XXXXX here" {
		t.Errorf("text block not sanitized: %s", text)
	}
}

func TestSanitizeBillingSentinel_MultipleMessages(t *testing.T) {
	messages := []any{
		map[string]any{
			"role": "user",
			"content": []any{
				map[string]any{"type": "text", "text": "no sentinel here"},
			},
		},
		map[string]any{
			"role": "assistant",
			"content": []any{
				map[string]any{"type": "text", "text": "response with cch=ff00a stuff"},
			},
		},
		map[string]any{
			"role": "user",
			"content": "plain string content without sentinel",
		},
	}

	changed := SanitizeBillingSentinel(messages)
	if !changed {
		t.Fatal("expected changed=true for message with sentinel")
	}

	// First message untouched
	text0 := messages[0].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
	if text0 != "no sentinel here" {
		t.Errorf("msg 0 mutated: %s", text0)
	}

	// Second message sanitized
	text1 := messages[1].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
	if text1 != "response with cch=XXXXX stuff" {
		t.Errorf("msg 1 not sanitized: %s", text1)
	}

	// Third message (string content) untouched — we only process []any content blocks
	text2 := messages[2].(map[string]any)["content"].(string)
	if text2 != "plain string content without sentinel" {
		t.Errorf("msg 2 string content mutated: %s", text2)
	}
}

func TestSanitizeBillingSentinel_ToolResultStringContent(t *testing.T) {
	// Bug: CC's Bun fork replaces first cch= in serialized JSON body.
	// If a tool_result contains cch= (e.g., a Reddit post about CC cache),
	// the wrong occurrence gets corrupted → different hash per request → cache miss.
	messages := []any{
		map[string]any{
			"role": "user",
			"content": []any{
				map[string]any{
					"type":        "tool_result",
					"tool_use_id": "toolu_123",
					"content":     "pinning to 2.1.68 avoids the cch=079a4 sentinel",
				},
			},
		},
	}

	changed := SanitizeBillingSentinel(messages)
	if !changed {
		t.Fatal("expected changed=true for tool_result with cch= in string content")
	}

	block := messages[0].(map[string]any)["content"].([]any)[0].(map[string]any)
	got := block["content"].(string)
	want := "pinning to 2.1.68 avoids the cch=XXXXX sentinel"
	if got != want {
		t.Errorf("tool_result string content not sanitized:\n  got:  %s\n  want: %s", got, want)
	}
}

func TestSanitizeBillingSentinel_ToolResultArrayContent(t *testing.T) {
	messages := []any{
		map[string]any{
			"role": "user",
			"content": []any{
				map[string]any{
					"type":        "tool_result",
					"tool_use_id": "toolu_456",
					"content": []any{
						map[string]any{"type": "text", "text": "output with cch=abcde hash"},
					},
				},
			},
		},
	}

	changed := SanitizeBillingSentinel(messages)
	if !changed {
		t.Fatal("expected changed=true for tool_result with cch= in nested text block")
	}

	nested := messages[0].(map[string]any)["content"].([]any)[0].(map[string]any)["content"].([]any)[0].(map[string]any)
	got := nested["text"].(string)
	want := "output with cch=XXXXX hash"
	if got != want {
		t.Errorf("tool_result nested text not sanitized:\n  got:  %s\n  want: %s", got, want)
	}
}

func TestSanitizeBillingSentinel_ToolResultNoSentinel(t *testing.T) {
	messages := []any{
		map[string]any{
			"role": "user",
			"content": []any{
				map[string]any{
					"type":        "tool_result",
					"tool_use_id": "toolu_789",
					"content":     "clean tool output without any hash",
				},
			},
		},
	}

	changed := SanitizeBillingSentinel(messages)
	if changed {
		t.Fatal("expected changed=false for tool_result without sentinel")
	}
}

// TestSanitizeBillingSentinel_PrefixStability simulates the real-world scenario:
// CC's Bun fork replaces the first cch= match in the serialized JSON body with a
// different hash on every request. If our sanitization doesn't normalize ALL cch=
// occurrences (across all block types), the message prefix changes between turns
// and Anthropic's prompt cache never gets a read hit.
//
// This test builds a realistic message array with cch= in various block types,
// simulates two consecutive requests with different Bun-injected hashes,
// runs SanitizeBillingSentinel on both, and verifies the shared prefix is
// byte-identical.
func TestSanitizeBillingSentinel_PrefixStability(t *testing.T) {
	// Build a realistic conversation with cch= in tricky locations
	buildMessages := func(bunHash string) []any {
		return []any{
			// [0] normal user message (no cch=)
			map[string]any{
				"role": "user",
				"content": []any{
					map[string]any{"type": "text", "text": "hello world"},
				},
			},
			// [1] assistant response
			map[string]any{
				"role": "assistant",
				"content": []any{
					map[string]any{"type": "text", "text": "I'll help you with that."},
				},
			},
			// [2] user with tool_result containing cch= (the actual bug case)
			map[string]any{
				"role": "user",
				"content": []any{
					map[string]any{
						"type":        "tool_result",
						"tool_use_id": "toolu_abc",
						"content":     "Reddit post: pinning to 2.1.68 avoids the cch=" + bunHash + " sentinel and the resume regression",
					},
				},
			},
			// [3] assistant discussing cache
			map[string]any{
				"role": "assistant",
				"content": []any{
					map[string]any{"type": "text", "text": "The billing header uses cch=" + bunHash + " for cache keying."},
				},
			},
			// [4] user with tool_result containing nested text blocks with cch=
			map[string]any{
				"role": "user",
				"content": []any{
					map[string]any{
						"type":        "tool_result",
						"tool_use_id": "toolu_def",
						"content": []any{
							map[string]any{"type": "text", "text": "line 1: normal output"},
							map[string]any{"type": "text", "text": "line 2: the header builder replaces cch=" + bunHash + " in the request body"},
						},
					},
				},
			},
			// [5] assistant (clean)
			map[string]any{
				"role":    "assistant",
				"content": []any{map[string]any{"type": "text", "text": "understood"}},
			},
			// [6] user with mixed content: text + tool_result
			map[string]any{
				"role": "user",
				"content": []any{
					map[string]any{"type": "text", "text": "check this cch=" + bunHash + " value"},
					map[string]any{
						"type":        "tool_result",
						"tool_use_id": "toolu_ghi",
						"content":     "tool output mentions cch=" + bunHash + " too",
					},
				},
			},
		}
	}

	// Simulate two consecutive requests with different Bun-injected hashes
	msgs1 := buildMessages("079a4")
	msgs2 := buildMessages("6a2a5")

	SanitizeBillingSentinel(msgs1)
	SanitizeBillingSentinel(msgs2)

	// Every message in the shared prefix must be byte-identical after sanitization
	for i := 0; i < len(msgs1); i++ {
		j1, _ := json.Marshal(msgs1[i])
		j2, _ := json.Marshal(msgs2[i])
		if string(j1) != string(j2) {
			t.Errorf("message[%d] differs between requests after sanitization:\n  req1: %s\n  req2: %s",
				i, truncate(string(j1), 200), truncate(string(j2), 200))
		}
	}
}

func TestSanitizeBillingSentinel_Idempotent(t *testing.T) {
	messages := []any{
		map[string]any{
			"role": "user",
			"content": []any{
				map[string]any{"type": "text", "text": "cch=abcde test"},
			},
		},
	}

	SanitizeBillingSentinel(messages)
	changed := SanitizeBillingSentinel(messages)

	// Second call: cch=XXXXX doesn't match [0-9a-f] pattern → no change
	if changed {
		t.Fatal("second call should return false (already sanitized)")
	}
}
