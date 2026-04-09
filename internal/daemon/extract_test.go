package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/carsteneu/yesmem/internal/models"
)

// --- truncID ---

func TestExtract_TruncID_Long(t *testing.T) {
	got := truncID("abcdefghijklmnop")
	if got != "abcdefgh" {
		t.Errorf("truncID long: got %q, want %q", got, "abcdefgh")
	}
}

func TestExtract_TruncID_Short(t *testing.T) {
	got := truncID("abc")
	if got != "abc" {
		t.Errorf("truncID short: got %q, want %q", got, "abc")
	}
}

func TestExtract_TruncID_Exact8(t *testing.T) {
	got := truncID("12345678")
	if got != "12345678" {
		t.Errorf("truncID exact 8: got %q, want %q", got, "12345678")
	}
}

func TestExtract_TruncID_Empty(t *testing.T) {
	got := truncID("")
	if got != "" {
		t.Errorf("truncID empty: got %q, want %q", got, "")
	}
}

// --- FilterByMaxAge ---

func TestExtract_FilterByMaxAge_Disabled(t *testing.T) {
	sessions := []models.Session{
		{ID: "old", StartedAt: time.Now().AddDate(0, 0, -100)},
		{ID: "new", StartedAt: time.Now()},
	}

	// maxAgeDays <= 0 returns all sessions unchanged
	for _, days := range []int{0, -1, -99} {
		got := FilterByMaxAge(sessions, days)
		if len(got) != 2 {
			t.Errorf("FilterByMaxAge(%d): got %d sessions, want 2", days, len(got))
		}
	}
}

func TestExtract_FilterByMaxAge_FiltersOld(t *testing.T) {
	sessions := []models.Session{
		{ID: "ancient", StartedAt: time.Now().AddDate(0, 0, -200)},
		{ID: "old", StartedAt: time.Now().AddDate(0, 0, -31)},
		{ID: "recent", StartedAt: time.Now().AddDate(0, 0, -5)},
		{ID: "today", StartedAt: time.Now()},
	}

	got := FilterByMaxAge(sessions, 30)
	if len(got) != 2 {
		t.Fatalf("FilterByMaxAge(30): got %d sessions, want 2", len(got))
	}
	ids := map[string]bool{}
	for _, s := range got {
		ids[s.ID] = true
	}
	if !ids["recent"] || !ids["today"] {
		t.Errorf("expected recent+today, got %v", ids)
	}
}

func TestExtract_FilterByMaxAge_Empty(t *testing.T) {
	got := FilterByMaxAge(nil, 30)
	if len(got) != 0 {
		t.Errorf("FilterByMaxAge empty: got %d, want 0", len(got))
	}
}

func TestExtract_FilterByMaxAge_AllFiltered(t *testing.T) {
	sessions := []models.Session{
		{ID: "old1", StartedAt: time.Now().AddDate(-1, 0, 0)},
		{ID: "old2", StartedAt: time.Now().AddDate(0, -6, 0)},
	}
	got := FilterByMaxAge(sessions, 7)
	if len(got) != 0 {
		t.Errorf("expected all filtered, got %d", len(got))
	}
}

// --- countEvolutionScope ---

func TestExtract_CountEvolutionScope_Empty(t *testing.T) {
	if n := countEvolutionScope(nil); n != 0 {
		t.Errorf("nil scope: got %d, want 0", n)
	}
	if n := countEvolutionScope(map[string]map[string]struct{}{}); n != 0 {
		t.Errorf("empty scope: got %d, want 0", n)
	}
}

func TestExtract_CountEvolutionScope_MultiProject(t *testing.T) {
	scope := map[string]map[string]struct{}{
		"project-a": {"gotcha": {}, "pattern": {}, "decision": {}},
		"project-b": {"preference": {}},
	}
	if n := countEvolutionScope(scope); n != 4 {
		t.Errorf("countEvolutionScope: got %d, want 4", n)
	}
}

// --- SummarizeMessages ---

func TestExtract_SummarizeMessages_BasicRoles(t *testing.T) {
	msgs := []models.Message{
		{Role: "user", MessageType: "text", Content: "Hello there"},
		{Role: "assistant", MessageType: "text", Content: "Hi back"},
	}
	got := SummarizeMessages(msgs)
	if len(got) != 2 {
		t.Fatalf("got %d summaries, want 2", len(got))
	}
	if got[0] != "user: Hello there" {
		t.Errorf("summary[0]: got %q", got[0])
	}
	if got[1] != "assistant: Hi back" {
		t.Errorf("summary[1]: got %q", got[1])
	}
}

func TestExtract_SummarizeMessages_SkipsNonText(t *testing.T) {
	msgs := []models.Message{
		{Role: "user", MessageType: "text", Content: "question"},
		{Role: "assistant", MessageType: "tool_use", Content: "bash ls"},
		{Role: "assistant", MessageType: "text", Content: "answer"},
		{Role: "user", MessageType: "text", Content: ""},
	}
	got := SummarizeMessages(msgs)
	if len(got) != 2 {
		t.Errorf("got %d summaries, want 2 (tool_use and empty skipped)", len(got))
	}
}

func TestExtract_SummarizeMessages_TruncatesAssistant(t *testing.T) {
	long := strings.Repeat("x", 600)
	msgs := []models.Message{
		{Role: "assistant", MessageType: "text", Content: long},
	}
	got := SummarizeMessages(msgs)
	if len(got) != 1 {
		t.Fatalf("got %d summaries, want 1", len(got))
	}
	// "assistant: " = 11 chars, content limit = 500, total 511, then "..."
	if len(got[0]) != 500+3 {
		t.Errorf("assistant truncation: len=%d, want %d", len(got[0]), 503)
	}
	if !strings.HasSuffix(got[0], "...") {
		t.Error("expected ... suffix")
	}
}

func TestExtract_SummarizeMessages_UserGetsMoreRoom(t *testing.T) {
	long := strings.Repeat("y", 900)
	msgs := []models.Message{
		{Role: "user", MessageType: "text", Content: long},
	}
	got := SummarizeMessages(msgs)
	if len(got) != 1 {
		t.Fatalf("got %d summaries, want 1", len(got))
	}
	// "user: " = 6 chars, content limit = 800, total 806, then "..."
	if len(got[0]) != 800+3 {
		t.Errorf("user truncation: len=%d, want %d", len(got[0]), 803)
	}
}

func TestExtract_SummarizeMessages_Empty(t *testing.T) {
	got := SummarizeMessages(nil)
	if len(got) != 0 {
		t.Errorf("got %d, want 0", len(got))
	}
}

// --- CleanNarrativeResponse ---

func TestExtract_CleanNarrativeResponse_Plain(t *testing.T) {
	got := CleanNarrativeResponse("  Hello world  ")
	if got != "Hello world" {
		t.Errorf("got %q, want %q", got, "Hello world")
	}
}

func TestExtract_CleanNarrativeResponse_CodeFences(t *testing.T) {
	got := CleanNarrativeResponse("```\nNarrative here\n```")
	if got != "Narrative here" {
		t.Errorf("got %q, want %q", got, "Narrative here")
	}
}

func TestExtract_CleanNarrativeResponse_Empty(t *testing.T) {
	got := CleanNarrativeResponse("   ")
	if got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestExtract_CleanNarrativeResponse_OnlyFences(t *testing.T) {
	got := CleanNarrativeResponse("```\n```")
	if got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

// --- isNarrativeTooShort ---

func TestExtract_IsNarrativeTooShort(t *testing.T) {
	cases := []struct {
		name  string
		input string
		short bool
	}{
		{"empty", "", true},
		{"one sentence", "Hello world.", true},
		{"two sentences", "First. Second.", true},
		{"three sentences", "First. Second. Third.", false},
		{"exclamation marks", "One! Two! Three!", false},
		{"mixed punctuation", "One. Two! Three?", false},
		{"question only two", "What? Why?", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := isNarrativeTooShort(tc.input)
			if got != tc.short {
				t.Errorf("isNarrativeTooShort(%q) = %v, want %v", tc.input, got, tc.short)
			}
		})
	}
}

// --- sessionTimeRange ---

func TestExtract_SessionTimeRange_Valid(t *testing.T) {
	ts := time.Date(2026, 3, 15, 14, 30, 0, 0, time.UTC)
	s := models.Session{StartedAt: ts}
	got := sessionTimeRange(s)
	if got != "2026-03-15 14:30" {
		t.Errorf("got %q, want %q", got, "2026-03-15 14:30")
	}
}

func TestExtract_SessionTimeRange_Zero(t *testing.T) {
	s := models.Session{}
	got := sessionTimeRange(s)
	if got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

// --- timeSince ---

func TestExtract_TimeSince_Minutes(t *testing.T) {
	got := timeSince(time.Now().Add(-15 * time.Minute))
	if !strings.HasSuffix(got, " min ago") {
		t.Errorf("expected 'N min ago', got %q", got)
	}
	if !strings.Contains(got, "15") {
		t.Errorf("expected ~15 in %q", got)
	}
}

func TestExtract_TimeSince_Hours(t *testing.T) {
	got := timeSince(time.Now().Add(-5 * time.Hour))
	if got != "5h ago" {
		t.Errorf("got %q, want %q", got, "5h ago")
	}
}

func TestExtract_TimeSince_Days(t *testing.T) {
	got := timeSince(time.Now().Add(-72 * time.Hour))
	if got != "3 days ago" {
		t.Errorf("got %q, want %q", got, "3 days ago")
	}
}

// --- BuildNarrativeUserMessage ---

func TestExtract_BuildNarrativeUserMessage_Basic(t *testing.T) {
	msgs := []string{"user: Hello", "assistant: World"}
	got := BuildNarrativeUserMessage(msgs, "myproject")
	if !strings.Contains(got, "Project: myproject") {
		t.Error("missing project header")
	}
	if !strings.Contains(got, "Session log:") {
		t.Error("missing session log")
	}
	if !strings.Contains(got, "user: Hello") {
		t.Error("missing first message")
	}
}

func TestExtract_BuildNarrativeUserMessageWithTime(t *testing.T) {
	msgs := []string{"user: test"}
	got := BuildNarrativeUserMessageWithTime(msgs, "proj", "2026-03-15 14:30")
	if !strings.Contains(got, "Session period: 2026-03-15 14:30") {
		t.Error("missing session time range")
	}
}

func TestExtract_BuildNarrativeUserMessage_Truncation(t *testing.T) {
	// Build messages that exceed maxNarrativeInputChars (25000)
	longMsg := strings.Repeat("A", 5000)
	msgs := make([]string, 10)
	for i := range msgs {
		msgs[i] = longMsg
	}
	got := BuildNarrativeUserMessage(msgs, "proj")
	if !strings.Contains(got, "[... truncated ...]") {
		t.Error("expected truncation marker for oversized input")
	}
	if len(got) > maxNarrativeInputChars+200 {
		t.Errorf("output too long: %d chars", len(got))
	}
}

// --- EstimateExtractionCost ---

func TestExtract_EstimateExtractionCost_Empty(t *testing.T) {
	est := EstimateExtractionCost(nil, nil, "haiku")
	if est.Sessions != 0 || est.EstCostUSD != 0 {
		t.Errorf("empty sessions should produce zero estimate, got %+v", est)
	}
}

func TestExtract_EstimateExtractionCost_Haiku(t *testing.T) {
	sessions := make([]models.Session, 10)
	est := EstimateExtractionCost(nil, sessions, "haiku")
	if est.Sessions != 10 {
		t.Errorf("sessions: got %d, want 10", est.Sessions)
	}
	if est.TotalChunks != 10*AvgChunksPerSession {
		t.Errorf("chunks: got %d, want %d", est.TotalChunks, 10*AvgChunksPerSession)
	}
	if est.EstCostUSD <= 0 {
		t.Error("expected positive cost estimate")
	}
}

func TestExtract_EstimateExtractionCost_OpusCostsMore(t *testing.T) {
	sessions := make([]models.Session, 5)
	haiku := EstimateExtractionCost(nil, sessions, "haiku")
	opus := EstimateExtractionCost(nil, sessions, "opus")
	if opus.EstCostUSD <= haiku.EstCostUSD {
		t.Errorf("opus ($%.2f) should cost more than haiku ($%.2f)", opus.EstCostUSD, haiku.EstCostUSD)
	}
}

// --- modelPricing ---

func TestExtract_ModelPricing(t *testing.T) {
	cases := []struct {
		model          string
		wantInput      float64
		wantOutput     float64
	}{
		{"haiku", HaikuInputPerM, HaikuOutputPerM},
		{"sonnet", SonnetInputPerM, SonnetOutputPerM},
		{"opus", OpusInputPerM, OpusOutputPerM},
		{"unknown", HaikuInputPerM, HaikuOutputPerM},
		{"", HaikuInputPerM, HaikuOutputPerM},
	}
	for _, tc := range cases {
		t.Run(tc.model, func(t *testing.T) {
			inp, out := modelPricing(tc.model)
			if inp != tc.wantInput || out != tc.wantOutput {
				t.Errorf("modelPricing(%q) = (%.1f, %.1f), want (%.1f, %.1f)",
					tc.model, inp, out, tc.wantInput, tc.wantOutput)
			}
		})
	}
}

// --- FormatCostEstimate ---

func TestExtract_FormatCostEstimate_Empty(t *testing.T) {
	got := FormatCostEstimate(CostEstimate{}, "api")
	if got != "No sessions need extraction." {
		t.Errorf("got %q", got)
	}
}

func TestExtract_FormatCostEstimate_API(t *testing.T) {
	est := CostEstimate{
		Sessions:        10,
		AvgChunks:       4,
		TotalChunks:     40,
		EstTokensInput:  1_000_000,
		EstTokensOutput: 100_000,
		EstCostUSD:      1.50,
	}
	got := FormatCostEstimate(est, "api")
	if !strings.Contains(got, "Sessions:     10") {
		t.Error("missing session count")
	}
	if !strings.Contains(got, "$1.50") {
		t.Error("missing cost")
	}
	if strings.Contains(got, "subscription") {
		t.Error("should not mention subscription for api backend")
	}
}

func TestExtract_FormatCostEstimate_CLI(t *testing.T) {
	est := CostEstimate{Sessions: 5, AvgChunks: 4, TotalChunks: 20, EstTokensInput: 500_000, EstTokensOutput: 50_000}
	got := FormatCostEstimate(est, "cli")
	if !strings.Contains(got, "subscription") {
		t.Error("CLI backend should mention subscription quota")
	}
}

// --- Session filtering logic from runInitialExtraction ---
// Tests the inline filtering logic: ExtractedAt.IsZero() && MessageCount > 5

func TestExtract_SessionFilterLogic(t *testing.T) {
	now := time.Now()
	sessions := []models.Session{
		{ID: "extracted", ExtractedAt: now, MessageCount: 20},
		{ID: "short", ExtractedAt: time.Time{}, MessageCount: 3},
		{ID: "borderline", ExtractedAt: time.Time{}, MessageCount: 5},
		{ID: "pending-a", ExtractedAt: time.Time{}, MessageCount: 10},
		{ID: "pending-b", ExtractedAt: time.Time{}, MessageCount: 50},
	}

	var toExtract []models.Session
	for _, s := range sessions {
		if s.ExtractedAt.IsZero() && s.MessageCount > 5 {
			toExtract = append(toExtract, s)
		}
	}

	if len(toExtract) != 2 {
		t.Fatalf("got %d pending, want 2", len(toExtract))
	}
	ids := map[string]bool{}
	for _, s := range toExtract {
		ids[s.ID] = true
	}
	if !ids["pending-a"] || !ids["pending-b"] {
		t.Errorf("expected pending-a and pending-b, got %v", ids)
	}
	// Boundary: messageCount == 5 should NOT be included
	if ids["borderline"] {
		t.Error("borderline (5 msgs) should be excluded (need >5)")
	}
}

// --- MaxPerRun capping ---

func TestExtract_MaxPerRunCapping(t *testing.T) {
	sessions := make([]models.Session, 20)
	for i := range sessions {
		sessions[i] = models.Session{
			ID:        strings.Repeat("s", 8) + string(rune('a'+i)),
			StartedAt: time.Now().Add(-time.Duration(i) * time.Hour),
		}
	}

	maxPerRun := 5
	if len(sessions) > maxPerRun {
		sessions = sessions[:maxPerRun]
	}
	if len(sessions) != 5 {
		t.Fatalf("got %d, want 5 after cap", len(sessions))
	}
}

// --- BuildNarrativeUserMessageWithContext ---

func TestExtract_BuildNarrativeUserMessageWithContext(t *testing.T) {
	msgs := []string{"user: test message"}
	ctx := &ProjectContext{
		RecentSessions: []string{"[vor 2h] debugging proxy", "[vor 1 Tag] refactoring"},
		OpenWork:       []string{"fix rate limiting", "add tests"},
		IntensityTrend: "rising",
	}
	got := BuildNarrativeUserMessageWithContext(msgs, "yesmem", "2026-03-15 14:30", ctx)

	if !strings.Contains(got, "Recent sessions in project:") {
		t.Error("missing recent sessions header")
	}
	if !strings.Contains(got, "debugging proxy") {
		t.Error("missing recent session content")
	}
	if !strings.Contains(got, "Open tasks:") {
		t.Error("missing open work header")
	}
	if !strings.Contains(got, "fix rate limiting") {
		t.Error("missing open work item")
	}
	if !strings.Contains(got, "Emotional trend: rising") {
		t.Error("missing intensity trend")
	}
}

func TestExtract_BuildNarrativeUserMessageWithContext_NilContext(t *testing.T) {
	msgs := []string{"user: hello"}
	got := BuildNarrativeUserMessageWithContext(msgs, "proj", "", nil)
	// Should still work, just without context sections
	if !strings.Contains(got, "Session log:") {
		t.Error("missing session log")
	}
	if strings.Contains(got, "Recent sessions") {
		t.Error("nil context should not produce context sections")
	}
}

// --- DB-query functions ---

func TestExtract_CountPendingNarratives(t *testing.T) {
	_, s := mustHandler(t)
	count, err := countPendingNarratives(s)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0 on fresh DB, got %d", count)
	}
}

func TestExtract_CountPendingProfiles(t *testing.T) {
	_, s := mustHandler(t)
	count, err := countPendingProfiles(s)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0 on fresh DB, got %d", count)
	}
}

func TestExtract_CountPendingRefinedBriefings(t *testing.T) {
	_, s := mustHandler(t)
	count, err := countPendingRefinedBriefings(s)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0 on fresh DB, got %d", count)
	}
}

func TestExtract_BuildEvolutionScope_Empty(t *testing.T) {
	_, s := mustHandler(t)
	scope, err := buildEvolutionScope(s, nil)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if scope != nil {
		t.Error("expected nil for empty sessions")
	}
}

func TestExtract_BuildEvolutionScope_WithLearnings(t *testing.T) {
	_, s := mustHandler(t)
	s.InsertLearning(&models.Learning{
		Content: "test gotcha", Category: "gotcha", Source: "user_stated",
		SessionID: "sess-evo", Project: "proj", CreatedAt: time.Now(),
	})
	sessions := []models.Session{{ID: "sess-evo"}}
	scope, err := buildEvolutionScope(s, sessions)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if scope == nil {
		t.Fatal("expected non-nil scope")
	}
	if _, ok := scope["proj"]; !ok {
		t.Error("expected project 'proj' in scope")
	}
}

func TestExtract_BuildEvolutionScope_SkipsNarrative(t *testing.T) {
	_, s := mustHandler(t)
	s.InsertLearning(&models.Learning{
		Content: "narrative text", Category: "narrative", Source: "llm_extracted",
		SessionID: "sess-narr", Project: "proj", CreatedAt: time.Now(),
	})
	sessions := []models.Session{{ID: "sess-narr"}}
	scope, err := buildEvolutionScope(s, sessions)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if scope != nil && len(scope) > 0 {
		t.Error("narrative should be skipped in evolution scope")
	}
}

func TestExtract_ExtractionNeeded_NotFound(t *testing.T) {
	_, s := mustHandler(t)
	// Unknown session → can't check → returns true (assume needed)
	if !extractionNeeded(s, "nonexistent-session") {
		t.Error("nonexistent session should return true (assume extraction needed)")
	}
}

func TestExtract_IsLikelyGitRepo(t *testing.T) {
	dir := t.TempDir()
	gitDir := filepath.Join(dir, ".git")
	os.MkdirAll(gitDir, 0755)

	if !isLikelyGitRepo(dir) {
		t.Error("expected true for dir with .git")
	}
	subDir := filepath.Join(dir, "sub", "deep")
	os.MkdirAll(subDir, 0755)
	if !isLikelyGitRepo(subDir) {
		t.Error("expected true for subdir of git repo")
	}
}

func TestExtract_IsLikelyGitRepo_NonRepo(t *testing.T) {
	dir := t.TempDir()
	if isLikelyGitRepo(dir) {
		t.Error("temp dir without .git should not be a git repo")
	}
}

func TestExtract_ListProjectsMissingRefinedBriefings(t *testing.T) {
	_, s := mustHandler(t)
	missing, err := listProjectsMissingRefinedBriefings(s)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if len(missing) != 0 {
		t.Errorf("expected 0 missing on fresh DB, got %d", len(missing))
	}
}
