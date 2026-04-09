package proxy

import (
	"testing"
)

func TestScanAssistantSignals_ThinkingWithIDs(t *testing.T) {
	messages := []any{
		map[string]any{
			"role": "assistant",
			"content": []any{
				map[string]any{
					"type":     "thinking",
					"thinking": "I see [ID:123] mentions the deploy order and [ID:456] covers the retry logic.",
				},
			},
		},
	}
	got := scanAssistantSignals(messages)
	if len(got.UsedIDs) != 2 {
		t.Fatalf("expected 2 UsedIDs, got %d: %v", len(got.UsedIDs), got.UsedIDs)
	}
	wantIDs := map[int64]bool{123: true, 456: true}
	for _, id := range got.UsedIDs {
		if !wantIDs[id] {
			t.Errorf("unexpected ID %d", id)
		}
	}
}

func TestScanAssistantSignals_TextWithHTMLComments(t *testing.T) {
	messages := []any{
		map[string]any{
			"role": "assistant",
			"content": []any{
				map[string]any{
					"type": "text",
					"text": "Here's my answer.\n<!-- [IDs: 789, 101] -->",
				},
			},
		},
	}
	got := scanAssistantSignals(messages)
	if len(got.UsedIDs) != 2 {
		t.Fatalf("expected 2 UsedIDs, got %d: %v", len(got.UsedIDs), got.UsedIDs)
	}
	wantIDs := map[int64]bool{789: true, 101: true}
	for _, id := range got.UsedIDs {
		if !wantIDs[id] {
			t.Errorf("unexpected ID %d", id)
		}
	}
}

func TestScanAssistantSignals_GapDetection(t *testing.T) {
	messages := []any{
		map[string]any{
			"role": "assistant",
			"content": []any{
				map[string]any{
					"type": "text",
					"text": "Done.\n<!-- [gap: SQLite WAL mode] -->",
				},
			},
		},
	}
	got := scanAssistantSignals(messages)
	if len(got.GapTopics) != 1 {
		t.Fatalf("expected 1 GapTopic, got %d: %v", len(got.GapTopics), got.GapTopics)
	}
	if got.GapTopics[0] != "SQLite WAL mode" {
		t.Errorf("expected 'SQLite WAL mode', got %q", got.GapTopics[0])
	}
}

func TestScanAssistantSignals_ContradictionDetection(t *testing.T) {
	messages := []any{
		map[string]any{
			"role": "assistant",
			"content": []any{
				map[string]any{
					"type": "text",
					"text": "Done.\n<!-- [contradiction: 123 vs 456: deploy order wrong] -->",
				},
			},
		},
	}
	got := scanAssistantSignals(messages)
	if len(got.Contradictions) != 1 {
		t.Fatalf("expected 1 Contradiction, got %d: %v", len(got.Contradictions), got.Contradictions)
	}
	if got.Contradictions[0] != "123 vs 456: deploy order wrong" {
		t.Errorf("expected '123 vs 456: deploy order wrong', got %q", got.Contradictions[0])
	}
}

func TestScanAssistantSignals_Empty(t *testing.T) {
	// No messages at all.
	got := scanAssistantSignals([]any{})
	if len(got.UsedIDs) != 0 || len(got.GapTopics) != 0 || len(got.Contradictions) != 0 {
		t.Errorf("expected empty signals, got %+v", got)
	}

	// Only user messages — no assistant.
	messages := []any{
		map[string]any{
			"role":    "user",
			"content": "Hello",
		},
	}
	got = scanAssistantSignals(messages)
	if len(got.UsedIDs) != 0 || len(got.GapTopics) != 0 || len(got.Contradictions) != 0 {
		t.Errorf("expected empty signals for user-only messages, got %+v", got)
	}
}

func TestScanAssistantSignals_Dedup(t *testing.T) {
	// Same ID appears in both thinking and text blocks.
	messages := []any{
		map[string]any{
			"role": "assistant",
			"content": []any{
				map[string]any{
					"type":     "thinking",
					"thinking": "Referencing [ID:42] for context.",
				},
				map[string]any{
					"type": "text",
					"text": "Answer here.\n<!-- [IDs: 42] -->",
				},
			},
		},
	}
	got := scanAssistantSignals(messages)
	if len(got.UsedIDs) != 1 {
		t.Fatalf("expected 1 UsedID after dedup, got %d: %v", len(got.UsedIDs), got.UsedIDs)
	}
	if got.UsedIDs[0] != 42 {
		t.Errorf("expected ID 42, got %d", got.UsedIDs[0])
	}
}

func TestScanAssistantSignals_StopsAtUserMessage(t *testing.T) {
	// Assistant message before the last user turn should NOT be scanned.
	messages := []any{
		map[string]any{
			"role": "assistant",
			"content": []any{
				map[string]any{
					"type": "text",
					"text": "Old answer with [ID:999].",
				},
			},
		},
		map[string]any{
			"role":    "user",
			"content": "New question",
		},
		map[string]any{
			"role": "assistant",
			"content": []any{
				map[string]any{
					"type": "text",
					"text": "New answer.\n<!-- [IDs: 7] -->",
				},
			},
		},
	}
	got := scanAssistantSignals(messages)
	if len(got.UsedIDs) != 1 {
		t.Fatalf("expected 1 UsedID (only from last turn), got %d: %v", len(got.UsedIDs), got.UsedIDs)
	}
	if got.UsedIDs[0] != 7 {
		t.Errorf("expected ID 7, got %d", got.UsedIDs[0])
	}
	// ID 999 must not be present.
	for _, id := range got.UsedIDs {
		if id == 999 {
			t.Error("ID 999 from pre-user-turn assistant message should not be included")
		}
	}
}

func TestScanAssistantSignals_PlainStringContent(t *testing.T) {
	// content is a plain string, not a []any block list.
	messages := []any{
		map[string]any{
			"role":    "assistant",
			"content": "Plain text with [ID:55] and <!-- [gap: WAL checkpointing] -->.",
		},
	}
	got := scanAssistantSignals(messages)
	if len(got.UsedIDs) != 1 || got.UsedIDs[0] != 55 {
		t.Errorf("expected UsedIDs=[55], got %v", got.UsedIDs)
	}
	if len(got.GapTopics) != 1 || got.GapTopics[0] != "WAL checkpointing" {
		t.Errorf("expected GapTopics=['WAL checkpointing'], got %v", got.GapTopics)
	}
}
