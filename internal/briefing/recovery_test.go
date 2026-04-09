package briefing

import (
	"strings"
	"testing"

	"github.com/carsteneu/yesmem/internal/models"
)

func TestExtractUserRequests_Basic(t *testing.T) {
	msgs := []models.Message{
		{Role: "user", MessageType: "text", Content: "Fix the login bug"},
		{Role: "assistant", MessageType: "text", Content: "I'll look into it"},
		{Role: "user", MessageType: "text", Content: "Also check the signup flow"},
	}
	got := extractUserRequests(msgs)
	if len(got) != 2 {
		t.Fatalf("expected 2 requests, got %d", len(got))
	}
	if got[0] != "Fix the login bug" {
		t.Errorf("expected first request 'Fix the login bug', got %q", got[0])
	}
}

func TestExtractUserRequests_SkipsShortMessages(t *testing.T) {
	msgs := []models.Message{
		{Role: "user", MessageType: "text", Content: "ja"},
		{Role: "user", MessageType: "text", Content: "ok"},
		{Role: "user", MessageType: "text", Content: "Refactor the auth module"},
	}
	got := extractUserRequests(msgs)
	if len(got) != 1 {
		t.Fatalf("expected 1 request (short messages skipped), got %d", len(got))
	}
}

func TestExtractUserRequests_SkipsSystemContent(t *testing.T) {
	msgs := []models.Message{
		{Role: "user", MessageType: "text", Content: "<system-reminder>ignore this</system-reminder>"},
		{Role: "user", MessageType: "text", Content: "Deploy the new version"},
	}
	got := extractUserRequests(msgs)
	if len(got) != 1 {
		t.Fatalf("expected 1 request (system content skipped), got %d", len(got))
	}
	if got[0] != "Deploy the new version" {
		t.Errorf("unexpected: %q", got[0])
	}
}

func TestExtractUserRequests_TruncatesLongMessages(t *testing.T) {
	long := strings.Repeat("x", 200)
	msgs := []models.Message{
		{Role: "user", MessageType: "text", Content: long},
	}
	got := extractUserRequests(msgs)
	if len(got) != 1 {
		t.Fatalf("expected 1 request, got %d", len(got))
	}
	if len(got[0]) > 125 {
		t.Errorf("expected truncation at ~123 chars, got %d", len(got[0]))
	}
	if !strings.HasSuffix(got[0], "...") {
		t.Error("truncated message should end with ...")
	}
}

func TestExtractUserRequests_Max10(t *testing.T) {
	var msgs []models.Message
	for i := 0; i < 15; i++ {
		msgs = append(msgs, models.Message{Role: "user", MessageType: "text", Content: "Some request that is long enough"})
	}
	got := extractUserRequests(msgs)
	if len(got) != 10 {
		t.Errorf("expected max 10 requests, got %d", len(got))
	}
}

func TestExtractUserRequests_IgnoresToolUse(t *testing.T) {
	msgs := []models.Message{
		{Role: "user", MessageType: "tool_use", Content: "Read file.go"},
		{Role: "user", MessageType: "text", Content: "Check the proxy code"},
	}
	got := extractUserRequests(msgs)
	if len(got) != 1 {
		t.Fatalf("expected 1 (tool_use ignored), got %d", len(got))
	}
}

func TestExtractUserRequests_Empty(t *testing.T) {
	got := extractUserRequests(nil)
	if len(got) != 0 {
		t.Errorf("expected 0, got %d", len(got))
	}
}

func TestExtractFiles_Basic(t *testing.T) {
	msgs := []models.Message{
		{FilePath: "/src/main.go"},
		{FilePath: "/src/handler.go"},
		{FilePath: "/src/main.go"}, // duplicate
	}
	got := extractFiles(msgs)
	if len(got) != 2 {
		t.Fatalf("expected 2 unique files, got %d", len(got))
	}
}

func TestExtractFiles_Empty(t *testing.T) {
	msgs := []models.Message{
		{Role: "user", MessageType: "text", Content: "hello"},
	}
	got := extractFiles(msgs)
	if len(got) != 0 {
		t.Errorf("expected 0 files, got %d", len(got))
	}
}

func TestExtractFiles_PreservesOrder(t *testing.T) {
	msgs := []models.Message{
		{FilePath: "/b.go"},
		{FilePath: "/a.go"},
		{FilePath: "/c.go"},
	}
	got := extractFiles(msgs)
	if got[0] != "/b.go" || got[1] != "/a.go" || got[2] != "/c.go" {
		t.Errorf("order not preserved: %v", got)
	}
}

func TestExtractRecentConversation_Basic(t *testing.T) {
	msgs := []models.Message{
		{Role: "user", MessageType: "text", Content: "first"},
		{Role: "assistant", MessageType: "text", Content: "reply"},
		{Role: "user", MessageType: "text", Content: "second"},
		{Role: "assistant", MessageType: "text", Content: "reply2"},
		{Role: "user", MessageType: "text", Content: "third"},
	}
	got := extractRecentConversation(msgs, 3)
	if len(got) != 3 {
		t.Fatalf("expected 3, got %d", len(got))
	}
	// Last 3 of [first, reply, second, reply2, third] = [second, reply2, third]
	if got[0].Content != "second" {
		t.Errorf("expected 'second' as first of last 3, got %q", got[0].Content)
	}
	if got[2].Content != "third" {
		t.Errorf("expected 'third' as last, got %q", got[2].Content)
	}
}

func TestExtractRecentConversation_SkipsSystemContent(t *testing.T) {
	msgs := []models.Message{
		{Role: "user", MessageType: "text", Content: "<system-reminder>noise</system-reminder>"},
		{Role: "user", MessageType: "text", Content: "real message here"},
		{Role: "assistant", MessageType: "text", Content: "response"},
	}
	got := extractRecentConversation(msgs, 10)
	if len(got) != 2 {
		t.Fatalf("expected 2 (system skipped), got %d", len(got))
	}
}

func TestExtractRecentConversation_SkipsNonText(t *testing.T) {
	msgs := []models.Message{
		{Role: "assistant", MessageType: "tool_use", Content: "Read /foo"},
		{Role: "user", MessageType: "tool_result", Content: "content of foo"},
		{Role: "user", MessageType: "text", Content: "looks good"},
	}
	got := extractRecentConversation(msgs, 10)
	if len(got) != 1 {
		t.Fatalf("expected 1 text message, got %d", len(got))
	}
}

func TestExtractRecentConversation_Empty(t *testing.T) {
	got := extractRecentConversation(nil, 5)
	if len(got) != 0 {
		t.Errorf("expected 0, got %d", len(got))
	}
}

func TestFormatRecovery_Clear(t *testing.T) {
	sess := &models.Session{ID: "abc123"}
	msgs := []models.Message{
		{Role: "user", MessageType: "text", Content: "Fix the login bug in auth.go"},
		{Role: "assistant", MessageType: "text", Content: "Looking at auth.go now"},
		{FilePath: "/src/auth.go"},
	}

	got := formatRecovery(sess, msgs, false)

	if !strings.Contains(got, "vor Clear") {
		t.Error("expected 'vor Clear' header for non-compact mode")
	}
	if !strings.Contains(got, "Fix the login bug") {
		t.Error("expected user request in output")
	}
	if !strings.Contains(got, "/src/auth.go") {
		t.Error("expected file path in output")
	}
	if !strings.Contains(got, "abc123") {
		t.Error("expected session ID in output")
	}
}

func TestFormatRecovery_Compact(t *testing.T) {
	sess := &models.Session{ID: "def456"}
	msgs := []models.Message{
		{Role: "user", MessageType: "text", Content: "Refactor the proxy module"},
		{Role: "assistant", MessageType: "text", Content: "Starting with proxy.go"},
	}

	got := formatRecovery(sess, msgs, true)

	if !strings.Contains(got, "vor Compact") {
		t.Error("expected 'vor Compact' header for compact mode")
	}
}

func TestFormatRecovery_CompactLimitsRequests(t *testing.T) {
	sess := &models.Session{ID: "s1"}
	var msgs []models.Message
	for i := 0; i < 8; i++ {
		msgs = append(msgs, models.Message{
			Role: "user", MessageType: "text",
			Content: strings.Repeat("task ", 5) + string(rune('A'+i)),
		})
	}

	got := formatRecovery(sess, msgs, true)

	// Compact limits to 3 user requests
	lines := strings.Split(got, "\n")
	taskCount := 0
	for _, l := range lines {
		if strings.HasPrefix(l, "- task") {
			taskCount++
		}
	}
	if taskCount > 3 {
		t.Errorf("compact mode should limit to 3 tasks, got %d", taskCount)
	}
}

func TestFormatRecovery_CompactLimitsFiles(t *testing.T) {
	sess := &models.Session{ID: "s1"}
	var msgs []models.Message
	for i := 0; i < 10; i++ {
		msgs = append(msgs, models.Message{FilePath: strings.Repeat("/file", 1) + string(rune('a'+i)) + ".go"})
	}

	got := formatRecovery(sess, msgs, true)

	if !strings.Contains(got, "+5 weitere") {
		t.Error("compact mode should show overflow for >5 files")
	}
}

func TestFormatRecovery_TruncatesLongContent(t *testing.T) {
	sess := &models.Session{ID: "s1"}
	long := strings.Repeat("x", 300)
	msgs := []models.Message{
		{Role: "user", MessageType: "text", Content: long},
		{Role: "assistant", MessageType: "text", Content: long},
	}

	got := formatRecovery(sess, msgs, true)

	// Compact mode truncates at 100 chars
	if strings.Contains(got, strings.Repeat("x", 150)) {
		t.Error("compact mode should truncate content at 100 chars")
	}
}

func TestFormatRecovery_NoMessages(t *testing.T) {
	sess := &models.Session{ID: "s1"}
	got := formatRecovery(sess, nil, false)

	// Should still have header and session reference
	if !strings.Contains(got, "vor Clear") {
		t.Error("should still have header even without messages")
	}
	if !strings.Contains(got, "s1") {
		t.Error("should still reference session ID")
	}
}

func TestGenerateRecovery_ReturnsBlock(t *testing.T) {
	store := setupStore(t)
	// Seed a session with messages for recovery
	store.UpsertSession(&models.Session{
		ID: "recover-me", Project: "/var/www/ccm19", ProjectShort: "ccm19",
		FirstMessage: "Fix the login bug", MessageCount: 3,
	})
	store.InsertMessages([]models.Message{
		{SessionID: "recover-me", Role: "user", MessageType: "text", Content: "Fix the login bug in auth.go"},
		{SessionID: "recover-me", Role: "assistant", MessageType: "text", Content: "Looking at auth.go now"},
	})

	gen := New(store, 3)
	gen.SetRecovery("recover-me", "clear")

	got := gen.GenerateRecovery()
	if got == "" {
		t.Fatal("GenerateRecovery() returned empty string, expected recovery block")
	}
	if !strings.Contains(got, "vor Clear") {
		t.Error("expected 'vor Clear' header")
	}
	if !strings.Contains(got, "recover-me") {
		t.Error("expected session ID in recovery block")
	}
}

func TestGenerateRecovery_EmptyWithoutConfig(t *testing.T) {
	store := setupStore(t)
	gen := New(store, 3)

	got := gen.GenerateRecovery()
	if got != "" {
		t.Errorf("expected empty string without recovery config, got %q", got)
	}
}

func TestGenerate_DoesNotContainRecovery(t *testing.T) {
	store := setupStore(t)
	store.UpsertSession(&models.Session{
		ID: "recover-me", Project: "/var/www/ccm19", ProjectShort: "ccm19",
		FirstMessage: "Fix the login bug", MessageCount: 3,
	})
	store.InsertMessages([]models.Message{
		{SessionID: "recover-me", Role: "user", MessageType: "text", Content: "Fix the login bug in auth.go"},
	})

	gen := New(store, 3)
	gen.SetRecovery("recover-me", "clear")

	text := gen.Generate("/var/www/ccm19")
	if strings.Contains(text, "vor Clear") {
		t.Error("Generate() should NOT contain recovery block — it must be injected post-refine")
	}
}
