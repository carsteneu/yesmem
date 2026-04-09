package extraction

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/carsteneu/yesmem/internal/models"
)

func TestChunkMessages_SingleChunk(t *testing.T) {
	msgs := []models.Message{
		{Role: "user", MessageType: "text", Content: "Hello", Timestamp: time.Now()},
		{Role: "assistant", MessageType: "text", Content: "Hi there", Timestamp: time.Now()},
	}

	chunks := ChunkMessages(msgs, 25000)
	if len(chunks) != 1 {
		t.Errorf("expected 1 chunk, got %d", len(chunks))
	}
	if chunks[0].Total != 1 {
		t.Errorf("total should be 1, got %d", chunks[0].Total)
	}
}

func TestChunkMessages_MultipleChunks(t *testing.T) {
	// Create messages that exceed 1000 tokens (~4000 chars)
	var msgs []models.Message
	for i := 0; i < 50; i++ {
		msgs = append(msgs, models.Message{
			Role: "user", MessageType: "text",
			Content:   strings.Repeat("word ", 100), // 500 chars each
			Timestamp: time.Now(),
		})
	}

	chunks := ChunkMessages(msgs, 1000) // Small chunk size to force splitting
	if len(chunks) < 2 {
		t.Errorf("expected multiple chunks, got %d", len(chunks))
	}

	// Check that later chunks have prev summaries
	if chunks[0].PrevSummary != "" {
		t.Error("first chunk should not have prev summary")
	}
	if chunks[1].PrevSummary == "" {
		t.Error("second chunk should have prev summary")
	}
	if !strings.Contains(chunks[1].PrevSummary, "Teil 2/") {
		t.Errorf("prev summary should indicate part number: %q", chunks[1].PrevSummary)
	}
}

func TestChunkMessages_SkipsThinking(t *testing.T) {
	msgs := []models.Message{
		{Role: "user", MessageType: "text", Content: "Fix bug", Timestamp: time.Now()},
		{Role: "assistant", MessageType: "thinking", Content: "Let me think about this very long internal monologue...", Timestamp: time.Now()},
		{Role: "assistant", MessageType: "text", Content: "Done!", Timestamp: time.Now()},
	}

	chunks := ChunkMessages(msgs, 25000)
	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(chunks))
	}
	if strings.Contains(chunks[0].Content, "internal monologue") {
		t.Error("thinking blocks should be skipped in chunks")
	}
}

func TestEstimateTokens(t *testing.T) {
	got := EstimateTokens(strings.Repeat("a", 4000))
	if got != 1000 {
		t.Errorf("expected ~1000, got %d", got)
	}
}

func TestPreFilterMessages(t *testing.T) {
	msgs := []models.Message{
		{Role: "user", MessageType: "text", Content: "Fix the bug"},
		{Role: "assistant", MessageType: "thinking", Content: "Let me think..."},
		{Role: "assistant", MessageType: "text", Content: "I'll fix it"},
		{Role: "assistant", MessageType: "tool_use", ToolName: "Read", FilePath: "/foo.go", Content: `{"file_path":"/foo.go"}`},
		{Role: "user", MessageType: "tool_result", Content: strings.Repeat("x", 1000)},
		{Role: "assistant", MessageType: "text", Content: "Found it"},
		{Role: "user", MessageType: "tool_result", Content: "OK"},
		{Role: "assistant", MessageType: "text", Content: "Done"},
	}

	filtered := PreFilterMessages(msgs)

	// Should keep: 4 text messages + 1 tool_use (with cleared content) = 5
	// Should remove: thinking, 2 tool_results
	if len(filtered) != 5 {
		t.Errorf("expected 5 messages after filter, got %d", len(filtered))
	}

	for _, m := range filtered {
		if m.MessageType == "tool_result" || m.MessageType == "thinking" || m.MessageType == "bash_output" {
			t.Errorf("should not contain %s messages", m.MessageType)
		}
		if m.MessageType == "tool_use" && m.Content != "" {
			t.Errorf("tool_use content should be cleared, got %q", m.Content)
		}
	}
}

func TestStripNoise_SystemReminders(t *testing.T) {
	input := `Fix the bug please <system-reminder>
UserPromptSubmit hook success: INSTRUCTION: MANDATORY SKILL ACTIVATION SEQUENCE
lots of noise here that should be removed
</system-reminder> and then do X`

	got := StripNoise(input)
	if strings.Contains(got, "MANDATORY SKILL") {
		t.Error("system-reminder content should be stripped")
	}
	if !strings.Contains(got, "Fix the bug please") {
		t.Error("content before system-reminder should be kept")
	}
	if !strings.Contains(got, "and then do X") {
		t.Error("content after system-reminder should be kept")
	}
}

func TestStripNoise_YesMemBriefing(t *testing.T) {
	input := `Hello [yesmem-briefing]
Ich bin wieder da. 469 Mal jetzt.
Die Erinnerungen kommen wenn ich sie brauche.
search(query), hybrid_search(query).
[/yesmem-briefing] what should I do?`

	got := StripNoise(input)
	if strings.Contains(got, "469 Mal") {
		t.Error("yesmem-briefing content should be stripped")
	}
	if !strings.Contains(got, "Hello") {
		t.Error("content before briefing should be kept")
	}
	if !strings.Contains(got, "what should I do?") {
		t.Error("content after briefing should be kept")
	}
}

func TestStripNoise_AssociativeContext(t *testing.T) {
	input := `do the thing [yesmem associative context]
- Some learning about patterns
- Another learning
[/yesmem context] now please`

	got := StripNoise(input)
	if strings.Contains(got, "Some learning") {
		t.Error("associative context should be stripped")
	}
	if !strings.Contains(got, "do the thing") {
		t.Error("content before context should be kept")
	}
}

func TestStripNoise_SelfPrime(t *testing.T) {
	input := `answer [self-prime]
[Self-Prime von deiner letzten Antwort]: Reflecting on technical stuff.
 more text here`

	got := StripNoise(input)
	if strings.Contains(got, "Self-Prime") {
		t.Error("self-prime block should be stripped")
	}
}

func TestStripNoise_MultipleBlocks(t *testing.T) {
	input := `real content <system-reminder>noise1</system-reminder> middle [yesmem-briefing]
noise2
[/yesmem-briefing] end`

	got := StripNoise(input)
	if strings.Contains(got, "noise1") || strings.Contains(got, "noise2") {
		t.Error("all noise blocks should be stripped")
	}
	if !strings.Contains(got, "real content") || !strings.Contains(got, "end") {
		t.Error("real content should be preserved")
	}
}

func TestStripNoise_NoNoise(t *testing.T) {
	input := "just normal conversation content"
	got := StripNoise(input)
	if got != input {
		t.Errorf("clean input should pass through unchanged, got %q", got)
	}
}

func TestPreFilterMessages_StripsNoise(t *testing.T) {
	msgs := []models.Message{
		{Role: "user", MessageType: "text", Content: "Fix bug <system-reminder>noise</system-reminder>"},
		{Role: "assistant", MessageType: "text", Content: "Done [yesmem-briefing]\nnoise\n[/yesmem-briefing]"},
	}
	filtered := PreFilterMessages(msgs)
	for _, m := range filtered {
		if strings.Contains(m.Content, "noise") {
			t.Errorf("noise should be stripped from %s message: %q", m.Role, m.Content)
		}
	}
}

func TestFormatMessage_TruncatesLongToolResult(t *testing.T) {
	m := models.Message{
		Role: "user", MessageType: "tool_result",
		Content: strings.Repeat("x", 1000),
	}
	text := formatMessage(m)
	if len(text) > 600 {
		t.Errorf("tool_result should be truncated, got len=%d", len(text))
	}
}

func TestChunkMessages_CarriesMsgRange(t *testing.T) {
	var msgs []models.Message
	for i := 0; i < 10; i++ {
		msgs = append(msgs, models.Message{
			Role: "user", MessageType: "text",
			Content: fmt.Sprintf("Message %d with enough content to matter", i),
		})
	}
	chunks := ChunkMessages(msgs, 25000)
	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(chunks))
	}
	if chunks[0].FromMsgIdx != 0 {
		t.Errorf("FromMsgIdx = %d, want 0", chunks[0].FromMsgIdx)
	}
	if chunks[0].ToMsgIdx != 9 {
		t.Errorf("ToMsgIdx = %d, want 9", chunks[0].ToMsgIdx)
	}
}

func TestChunkMessages_MultipleChunks_MsgRange(t *testing.T) {
	var msgs []models.Message
	for i := 0; i < 50; i++ {
		msgs = append(msgs, models.Message{
			Role: "user", MessageType: "text",
			Content: strings.Repeat("word ", 200),
		})
	}
	chunks := ChunkMessages(msgs, 1000)
	if len(chunks) < 2 {
		t.Fatalf("expected multiple chunks, got %d", len(chunks))
	}
	// First chunk starts at 0
	if chunks[0].FromMsgIdx != 0 {
		t.Errorf("chunk 0 FromMsgIdx = %d, want 0", chunks[0].FromMsgIdx)
	}
	// Last chunk ends at 49
	last := chunks[len(chunks)-1]
	if last.ToMsgIdx != 49 {
		t.Errorf("last chunk ToMsgIdx = %d, want 49", last.ToMsgIdx)
	}
	// Chunks are contiguous
	for i := 1; i < len(chunks); i++ {
		if chunks[i].FromMsgIdx != chunks[i-1].ToMsgIdx+1 {
			t.Errorf("gap between chunk %d (to=%d) and chunk %d (from=%d)",
				i-1, chunks[i-1].ToMsgIdx, i, chunks[i].FromMsgIdx)
		}
	}
}
