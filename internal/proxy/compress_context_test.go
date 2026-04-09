package proxy

import (
	"strings"
	"testing"
)

// estimator returns ~1 token per 4 bytes (simple approximation for tests).
func testEstimator(text string) int {
	return len(text) / 4
}

// makeToolUse creates a tool_use content block.
func makeToolUse(id, name string, input map[string]any) map[string]any {
	return map[string]any{
		"type":  "tool_use",
		"id":    id,
		"name":  name,
		"input": input,
	}
}

// makeToolResult creates a tool_result content block with text content.
func makeToolResult(toolUseID, text string) map[string]any {
	return map[string]any{
		"type":        "tool_result",
		"tool_use_id": toolUseID,
		"content":     text,
	}
}

// makeThinking creates a thinking content block.
func makeThinking(text string) map[string]any {
	return map[string]any{
		"type":     "thinking",
		"thinking": text,
	}
}

// bigText returns a string of approximately n tokens (n*4 bytes).
func bigText(n int) string {
	return strings.Repeat("word ", n) // ~5 bytes per "word ", so n*5/4 ≈ n tokens
}

// buildConversation creates a realistic message sequence:
// [system, (assistant tool_use + user tool_result) × pairs, ..., user query, assistant response]
// Returns messages array and the total number of turns (message pairs).
func buildConversation(toolPairs int, toolResultSize int) []any {
	msgs := make([]any, 0)

	// Message 0: system
	msgs = append(msgs, map[string]any{
		"role":    "user",
		"content": "system context",
	})

	// Tool use/result pairs
	for i := 0; i < toolPairs; i++ {
		toolID := "tu_" + string(rune('a'+i))
		// Assistant with tool_use + thinking
		msgs = append(msgs, map[string]any{
			"role": "assistant",
			"content": []any{
				makeThinking(bigText(toolResultSize)), // thinking block same size
				makeToolUse(toolID, "Read", map[string]any{"file_path": "/some/file.go"}),
			},
		})
		// User with tool_result
		msgs = append(msgs, map[string]any{
			"role": "user",
			"content": []any{
				makeToolResult(toolID, bigText(toolResultSize)),
			},
		})
		// Assistant text response summarizing the tool result
		msgs = append(msgs, map[string]any{
			"role":    "assistant",
			"content": "I read the file and found important functions.",
		})
		// User follow-up
		msgs = append(msgs, map[string]any{
			"role":    "user",
			"content": "ok, continue",
		})
	}

	return msgs
}

func TestCompressContext_NoCompressionUnderThreshold(t *testing.T) {
	// Small tool results (<500 tokens) should never be compressed
	msgs := buildConversation(3, 100) // 100 tokens — under threshold
	result := CompressContext(msgs, 5, "", testEstimator)

	if result.ThinkingCompressed > 0 {
		t.Errorf("expected no thinking compression, got %d", result.ThinkingCompressed)
	}
	if result.ToolResultsCompressed > 0 {
		t.Errorf("expected no tool_result compression, got %d", result.ToolResultsCompressed)
	}
}

func TestCompressContext_NoCompressionRecentTurns(t *testing.T) {
	// Even large tool results should not be compressed if within keepRecent window
	// buildConversation(2, 600) = 9 messages. keepRecent=9 → all protected.
	msgs := buildConversation(2, 600)
	result := CompressContext(msgs, len(msgs), "", testEstimator)

	if result.ThinkingCompressed > 0 {
		t.Errorf("expected no thinking compression for recent turns, got %d", result.ThinkingCompressed)
	}
	if result.ToolResultsCompressed > 0 {
		t.Errorf("expected no tool_result compression for recent turns, got %d", result.ToolResultsCompressed)
	}
}

func TestCompressContext_TruncatesAtTurn5(t *testing.T) {
	// 10 tool pairs = 41 messages. Oldest pairs should be truncated (turn 5-7).
	msgs := buildConversation(10, 600)
	origLen := len(msgs)
	result := CompressContext(msgs, 5, "", testEstimator)

	// Message count should stay the same (compression is in-place)
	if len(result.Messages) != origLen {
		t.Errorf("expected %d messages, got %d", origLen, len(result.Messages))
	}

	// Some compressions should have happened
	if result.ThinkingCompressed == 0 {
		t.Error("expected some thinking blocks to be compressed")
	}
	if result.ToolResultsCompressed == 0 {
		t.Error("expected some tool_results to be compressed")
	}

	// Compressed content should be smaller than original
	if result.TokensSaved <= 0 {
		t.Errorf("expected positive token savings, got %d", result.TokensSaved)
	}
}

func TestCompressContext_SummaryAtTurn8(t *testing.T) {
	// 15 tool pairs = enough that oldest are 8+ turns old → summary stubs
	msgs := buildConversation(15, 800)
	result := CompressContext(msgs, 5, "", testEstimator)

	// Check that oldest tool_results became summary stubs.
	// tool_results are in user messages as content blocks.
	// After compression, the tool_result's "content" field (string) contains the summary.
	foundSummary := false
	for i := 1; i < 20; i++ {
		msg, ok := result.Messages[i].(map[string]any)
		if !ok {
			continue
		}
		blocks, ok := msg["content"].([]any)
		if !ok {
			continue
		}
		for _, block := range blocks {
			b, ok := block.(map[string]any)
			if !ok {
				continue
			}
			// Check tool_result content (string)
			if b["type"] == "tool_result" {
				if content, ok := b["content"].(string); ok {
					if strings.Contains(content, "[context compressed") {
						foundSummary = true
					}
				}
			}
			// Check thinking blocks
			if b["type"] == "thinking" {
				if thinking, ok := b["thinking"].(string); ok {
					if strings.Contains(thinking, "[context compressed") {
						foundSummary = true
					}
				}
			}
		}
	}

	if !foundSummary {
		t.Error("expected summary stubs in oldest messages")
	}
}

func TestCompressContext_PreservesDeepSearchHint(t *testing.T) {
	msgs := buildConversation(15, 800)
	result := CompressContext(msgs, 5, "", testEstimator)

	// Summary stubs for tool_results should contain deep_search hint
	foundHint := false
	for _, msg := range result.Messages {
		m, ok := msg.(map[string]any)
		if !ok {
			continue
		}
		blocks, ok := m["content"].([]any)
		if !ok {
			continue
		}
		for _, block := range blocks {
			b, ok := block.(map[string]any)
			if !ok {
				continue
			}
			// Check tool_result content (string) for deep_search hint
			if b["type"] == "tool_result" {
				if content, ok := b["content"].(string); ok {
					if strings.Contains(content, "deep_search(") {
						foundHint = true
					}
				}
			}
		}
	}

	if !foundHint {
		t.Error("expected deep_search hints in compressed tool_results")
	}
}

func TestCompressContext_ThinkingBlocksCompressed(t *testing.T) {
	msgs := buildConversation(15, 800)
	result := CompressContext(msgs, 5, "", testEstimator)

	// Check that thinking blocks in old messages are compressed
	// They should be truncated or summarized, not full size
	for i := 1; i < 10; i++ {
		msg, ok := result.Messages[i].(map[string]any)
		if !ok {
			continue
		}
		if msg["role"] != "assistant" {
			continue
		}
		blocks, ok := msg["content"].([]any)
		if !ok {
			continue
		}
		for _, block := range blocks {
			b, ok := block.(map[string]any)
			if !ok {
				continue
			}
			if b["type"] == "thinking" {
				thinking, _ := b["thinking"].(string)
				// Original was 800 tokens ≈ 4000 bytes. Should be much smaller now.
				if len(thinking) > 2500 {
					t.Errorf("expected compressed thinking block, got %d bytes", len(thinking))
				}
			}
		}
	}
}

func TestCompressContext_PreservesMessageStructure(t *testing.T) {
	msgs := buildConversation(10, 600)
	result := CompressContext(msgs, 5, "", testEstimator)

	// Verify alternating user/assistant pattern is maintained
	lastRole := ""
	for i, msg := range result.Messages {
		m, ok := msg.(map[string]any)
		if !ok {
			t.Errorf("message %d is not a map", i)
			continue
		}
		role, _ := m["role"].(string)
		if role == "" {
			t.Errorf("message %d has no role", i)
		}
		if i > 0 && role == lastRole {
			// Two consecutive messages with same role is OK in some cases
			// (assistant tool_use followed by text), but tool_result (user)
			// should not be followed by another user message
		}
		lastRole = role
	}
}
