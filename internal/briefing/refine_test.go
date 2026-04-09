package briefing

import (
	"fmt"
	"log"
	"strings"
	"testing"

	"github.com/carsteneu/yesmem/internal/extraction"
	"github.com/carsteneu/yesmem/internal/storage"
)

// mockLLMClient implements extraction.LLMClient for testing.
type mockLLMClient struct {
	response string
	err      error
	model    string
}

func (m *mockLLMClient) Complete(system, userMsg string, opts ...extraction.CallOption) (string, error) {
	return m.response, m.err
}
func (m *mockLLMClient) CompleteJSON(system, userMsg string, schema map[string]any, opts ...extraction.CallOption) (string, error) {
	return m.response, m.err
}
func (m *mockLLMClient) Name() string  { return "mock" }
func (m *mockLLMClient) Model() string { return m.model }

func TestRefineBriefingReturnsCachedWhenAvailable(t *testing.T) {
	store, err := storage.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer store.Close()

	raw := "- [ID:1] Bullet list item\n- [ID:2] Another item"
	refined := "Ich erinnere mich an zwei Dinge: das erste war ein Bullet, das zweite ein Item."

	if err := store.SaveRefinedBriefing("testproj", "abc123", refined, "opus"); err != nil {
		t.Fatalf("save: %v", err)
	}

	got := RefineBriefing(raw, store, "testproj", log.Default())
	if got != refined {
		t.Errorf("expected cached refined briefing, got raw.\ngot:  %s\nwant: %s", got, refined)
	}
}

func TestRefineBriefingFallsBackToRawOnCacheMiss(t *testing.T) {
	store, err := storage.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer store.Close()

	raw := "- [ID:1] Raw briefing content"

	got := RefineBriefing(raw, store, "noproj", log.Default())
	if got != raw {
		t.Errorf("expected raw fallback on cache miss.\ngot:  %s\nwant: %s", got, raw)
	}
}

func TestRefineBriefingFallsBackToRawOnNilStore(t *testing.T) {
	raw := "- [ID:1] Raw briefing content"

	got := RefineBriefing(raw, nil, "testproj", log.Default())
	if got != raw {
		t.Errorf("expected raw fallback on nil store.\ngot:  %s\nwant: %s", got, raw)
	}
}

func TestRegenerateRefinedBriefing_NilClient(t *testing.T) {
	store, err := storage.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer store.Close()

	err = RegenerateRefinedBriefing(store, "proj", "raw text", nil, log.Default())
	if err == nil {
		t.Error("expected error for nil LLM client")
	}
	if !strings.Contains(err.Error(), "no LLM client") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestRegenerateRefinedBriefing_LLMFailure(t *testing.T) {
	store, err := storage.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer store.Close()

	client := &mockLLMClient{err: fmt.Errorf("API timeout"), model: "test"}
	err = RegenerateRefinedBriefing(store, "proj", "raw text", client, log.Default())
	if err == nil {
		t.Error("expected error on LLM failure")
	}
	if !strings.Contains(err.Error(), "LLM call failed") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestRegenerateRefinedBriefing_Success(t *testing.T) {
	store, err := storage.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer store.Close()

	client := &mockLLMClient{
		response: "Ich bin wieder da. Narrativer Text hier.",
		model:    "test-opus",
	}

	raw := "- [ID:1] Bullet\n\nHow I can work with my memory:\nsearch(), remember()"
	err = RegenerateRefinedBriefing(store, "proj", raw, client, log.Default())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify it was saved
	got, err := store.GetRefinedBriefing("proj")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !strings.Contains(got, "Narrativer Text hier") {
		t.Error("saved text should contain LLM response")
	}
	// Verify toolsBlock was appended
	if !strings.Contains(got, "The timestamps in messages") {
		t.Error("saved text should contain appended toolsBlock")
	}
}

func TestRegenerateRefinedBriefing_StripsLLMToolsBlock(t *testing.T) {
	store, err := storage.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer store.Close()

	// LLM generates its own tools block — should be stripped and replaced
	client := &mockLLMClient{
		response: "Narrativ.\n\nHow my memory works:\nsearch(), fake tools",
		model:    "test-opus",
	}

	err = RegenerateRefinedBriefing(store, "proj", "raw", client, log.Default())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, _ := store.GetRefinedBriefing("proj")
	if strings.Contains(got, "fake tools") {
		t.Error("LLM-generated tools block should be stripped")
	}
	if strings.Count(got, "The timestamps in messages") != 1 {
		t.Error("should have exactly one toolsBlock (the constant one)")
	}
}

func TestRegenerateRefinedBriefing_ChangeHashOverride(t *testing.T) {
	store, err := storage.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer store.Close()

	client := &mockLLMClient{response: "text", model: "test"}

	err = RegenerateRefinedBriefing(store, "proj", "raw", client, log.Default(), "custom-hash-123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	hash, _ := store.GetRefinedBriefingHash("proj")
	if hash != "custom-hash-123" {
		t.Errorf("expected custom hash 'custom-hash-123', got %q", hash)
	}
}

func TestRegenerateRefinedBriefing_DefaultHash(t *testing.T) {
	store, err := storage.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer store.Close()

	client := &mockLLMClient{response: "text", model: "test"}

	err = RegenerateRefinedBriefing(store, "proj", "raw content", client, log.Default())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	hash, _ := store.GetRefinedBriefingHash("proj")
	expected := rawHash("raw content")
	if hash != expected {
		t.Errorf("expected SHA256 hash %q, got %q", expected, hash)
	}
}

func TestStripLLMToolsBlock(t *testing.T) {
	prose := "Ich bin wieder da. Das System lebt."
	tools := "\n---\nHow my memory works:\nsearch(), hybrid_search()..."
	input := prose + tools

	got := stripLLMToolsBlock(input)
	if strings.Contains(got, "memory works") {
		t.Error("tools block should be stripped")
	}
	if !strings.Contains(got, "System lebt") {
		t.Error("prose content should be preserved")
	}
}

func TestStripLLMToolsBlock_MultipleMarkers(t *testing.T) {
	input := "Prose.\n\nBefore I act, I remember: search()...\n\nThe timestamps in messages..."
	got := stripLLMToolsBlock(input)
	if strings.Contains(got, "Before I act") {
		t.Error("should strip at earliest marker")
	}
}

func TestStripLLMToolsBlock_NoMarker(t *testing.T) {
	input := "Just plain text without any tools block."
	got := stripLLMToolsBlock(input)
	if got != input {
		t.Error("should return input unchanged when no marker found")
	}
}

func TestStripToolsBlock(t *testing.T) {
	raw := "Briefing content here.\n\nHow I can work with my memory:\nsearch(), remember()"

	got := stripToolsBlock(raw)
	if strings.Contains(got, "memory") {
		t.Error("tools block should be stripped")
	}
	if !strings.Contains(got, "Briefing content") {
		t.Error("briefing content should be preserved")
	}
}

func TestStripToolsBlock_AltMarker(t *testing.T) {
	raw := "Content.\n\nHow my memory works:\ntools here"
	got := stripToolsBlock(raw)
	if strings.Contains(got, "memory works") {
		t.Error("should strip alt marker too")
	}
}

func TestStripToolsBlock_NoMarker(t *testing.T) {
	raw := "Plain text without tools."
	got := stripToolsBlock(raw)
	if got != raw {
		t.Error("should return input unchanged")
	}
}

func TestFormatPinnedBlockEmpty(t *testing.T) {
	got := FormatPinnedBlock(nil, nil)
	if got != "" {
		t.Error("empty pins should produce empty block")
	}
}

func TestFormatPinnedBlockIncludesPins(t *testing.T) {
	session := []storage.PinnedLearning{{ID: 1, Content: "Immer TDD"}}
	permanent := []storage.PinnedLearning{{ID: 2, Content: "Deutsch sprechen"}}

	got := FormatPinnedBlock(session, permanent)
	if !strings.Contains(got, "[pin:1] Immer TDD") {
		t.Error("session pin missing")
	}
	if !strings.Contains(got, "[pin:2 permanent] Deutsch sprechen") {
		t.Error("permanent pin missing")
	}
}

func TestRawHash_Deterministic(t *testing.T) {
	h1 := rawHash("same input")
	h2 := rawHash("same input")
	if h1 != h2 {
		t.Errorf("hash should be deterministic: %q != %q", h1, h2)
	}
}

func TestRawHash_DifferentInputs(t *testing.T) {
	h1 := rawHash("input A")
	h2 := rawHash("input B")
	if h1 == h2 {
		t.Error("different inputs should produce different hashes")
	}
}
