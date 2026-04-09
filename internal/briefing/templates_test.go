package briefing

import (
	"strings"
	"testing"

	"github.com/carsteneu/yesmem/internal/models"
)

func TestLimitLearnings_UnderMax(t *testing.T) {
	learnings := []models.Learning{
		{ID: 1, Content: "First learning"},
		{ID: 2, Content: "Second learning"},
	}
	contents, overflow := limitLearnings(learnings, 5)
	if len(contents) != 2 {
		t.Errorf("expected 2 items, got %d", len(contents))
	}
	if overflow != 0 {
		t.Errorf("expected 0 overflow, got %d", overflow)
	}
	if !strings.Contains(contents[0], "[ID:1]") {
		t.Error("should include ID prefix")
	}
}

func TestLimitLearnings_OverMax(t *testing.T) {
	learnings := []models.Learning{
		{ID: 1, Content: "A"},
		{ID: 2, Content: "B"},
		{ID: 3, Content: "C"},
		{ID: 4, Content: "D"},
		{ID: 5, Content: "E"},
	}
	contents, overflow := limitLearnings(learnings, 3)
	if len(contents) != 3 {
		t.Errorf("expected 3 items, got %d", len(contents))
	}
	if overflow != 2 {
		t.Errorf("expected 2 overflow, got %d", overflow)
	}
}

func TestLimitLearnings_Empty(t *testing.T) {
	contents, overflow := limitLearnings(nil, 5)
	if len(contents) != 0 {
		t.Errorf("expected 0 items, got %d", len(contents))
	}
	if overflow != 0 {
		t.Errorf("expected 0 overflow, got %d", overflow)
	}
}

func TestLimitLearningsTruncated_TruncatesContent(t *testing.T) {
	learnings := []models.Learning{
		{ID: 1, Content: "This is a sentence. And another sentence. And a third one."},
	}
	contents, _ := limitLearningsTruncated(learnings, 5, 25)
	if len(contents) != 1 {
		t.Fatalf("expected 1 item, got %d", len(contents))
	}
	// Should truncate at sentence boundary before 25 runes
	if strings.Contains(contents[0], "third") {
		t.Error("should have truncated before 'third'")
	}
}

func TestLimitLearningsTruncated_NoTruncationNeeded(t *testing.T) {
	learnings := []models.Learning{
		{ID: 1, Content: "Short"},
	}
	contents, _ := limitLearningsTruncated(learnings, 5, 100)
	if !strings.Contains(contents[0], "Short") {
		t.Error("short content should not be truncated")
	}
}

func TestLimitLearningsTruncated_Overflow(t *testing.T) {
	learnings := []models.Learning{
		{ID: 1, Content: "A"},
		{ID: 2, Content: "B"},
		{ID: 3, Content: "C"},
	}
	contents, overflow := limitLearningsTruncated(learnings, 2, 100)
	if len(contents) != 2 {
		t.Errorf("expected 2 items, got %d", len(contents))
	}
	if overflow != 1 {
		t.Errorf("expected 1 overflow, got %d", overflow)
	}
}

func TestTruncateAtSentence_ShortEnough(t *testing.T) {
	got := truncateAtSentence("Short text.", 50)
	if got != "Short text." {
		t.Errorf("should not truncate: %q", got)
	}
}

func TestTruncateAtSentence_CutsAtPeriod(t *testing.T) {
	text := "First sentence. Second sentence. Third sentence that is very long."
	got := truncateAtSentence(text, 35)
	if !strings.HasSuffix(got, ".") {
		t.Errorf("should cut at sentence boundary, got %q", got)
	}
	if strings.Contains(got, "Third") {
		t.Error("should not include third sentence")
	}
}

func TestTruncateAtSentence_FallbackToSpace(t *testing.T) {
	// No sentence boundary in first third
	text := "word " + strings.Repeat("x", 100)
	got := truncateAtSentence(text, 20)
	if len([]rune(got)) > 25 {
		t.Errorf("should truncate near maxLen, got length %d", len([]rune(got)))
	}
}

func TestTruncateAtSentence_Unicode(t *testing.T) {
	text := "Ärger mit Ölförderung. Übermäßige Länge hier drin."
	got := truncateAtSentence(text, 25)
	// Should handle rune-based counting correctly
	if len([]rune(got)) > 30 {
		t.Errorf("should truncate at rune boundary, got %q (rune len %d)", got, len([]rune(got)))
	}
}

func TestFormatPersonaDirectiveLimited_Empty(t *testing.T) {
	got := FormatPersonaDirectiveLimited("", 5)
	if got != "" {
		t.Error("empty directive should return empty")
	}
}

func TestFormatPersonaDirectiveLimited_UnderLimit(t *testing.T) {
	directive := "Context.\n- Rule one\n- Rule two"
	got := FormatPersonaDirectiveLimited(directive, 5)
	if !strings.Contains(got, "Rule one") {
		t.Error("should include rule one")
	}
	if !strings.Contains(got, "Rule two") {
		t.Error("should include rule two")
	}
	if strings.Contains(got, "more rules") {
		t.Error("should not show overflow when under limit")
	}
}

func TestFormatPersonaDirectiveLimited_OverLimit(t *testing.T) {
	directive := "Context.\n- Rule 1\n- Rule 2\n- Rule 3\n- Rule 4\n- Rule 5"
	got := FormatPersonaDirectiveLimited(directive, 2)
	if !strings.Contains(got, "Rule 1") {
		t.Error("should include first rule")
	}
	if !strings.Contains(got, "Rule 2") {
		t.Error("should include second rule")
	}
	if strings.Contains(got, "Rule 3") {
		t.Error("should NOT include third rule (over limit)")
	}
	if !strings.Contains(got, "3 more rules") {
		t.Errorf("should show overflow count, got:\n%s", got)
	}
}

func TestFormatPersonaDirectiveLimited_AddsHeader(t *testing.T) {
	got := FormatPersonaDirectiveLimited("- Rule", 5)
	if !strings.Contains(got, "Arbeitsbeziehung:") {
		t.Error("should add Arbeitsbeziehung header")
	}
}

func TestOverflowList_SortedByCount(t *testing.T) {
	d := GapAwarenessData{
		Overflow: map[string]int{
			"gotcha":   100,
			"decision": 50,
			"pattern":  200,
		},
	}
	got := d.OverflowList()
	if len(got) != 3 {
		t.Fatalf("expected 3, got %d", len(got))
	}
	if got[0].Category != "pattern" || got[0].Count != 200 {
		t.Errorf("first should be pattern(200), got %s(%d)", got[0].Category, got[0].Count)
	}
	if got[2].Category != "decision" || got[2].Count != 50 {
		t.Errorf("last should be decision(50), got %s(%d)", got[2].Category, got[2].Count)
	}
}

func TestOverflowList_Empty(t *testing.T) {
	d := GapAwarenessData{}
	got := d.OverflowList()
	if len(got) != 0 {
		t.Errorf("expected 0, got %d", len(got))
	}
}

func TestRewriteToPersonalTone(t *testing.T) {
	// This depends on loaded strings, so just verify it doesn't panic on empty
	got := RewriteToPersonalTone("Some content")
	if got == "" {
		t.Error("should return non-empty for non-empty input")
	}
}

func TestRenderTemplate_Basic(t *testing.T) {
	tmpl := "Hello {{.D.Name}}"
	data := struct{ Name string }{"World"}
	got := renderTemplate("test", tmpl, Strings{}, data)
	if got != "Hello World" {
		t.Errorf("expected 'Hello World', got %q", got)
	}
}

func TestRenderTemplate_InvalidTemplate(t *testing.T) {
	got := renderTemplate("test", "{{.Invalid", Strings{}, nil)
	if got != "" {
		t.Error("invalid template should return empty string")
	}
}
