package claudemd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/carsteneu/yesmem/internal/config"
	"github.com/carsteneu/yesmem/internal/extraction"
	"github.com/carsteneu/yesmem/internal/models"
	"github.com/carsteneu/yesmem/internal/storage"
)

type mockLLMClient struct {
	response  string
	err       error
	callCount int
}

func (m *mockLLMClient) Complete(system, user string, opts ...extraction.CallOption) (string, error) {
	m.callCount++
	return m.response, m.err
}
func (m *mockLLMClient) CompleteJSON(system, user string, schema map[string]any, opts ...extraction.CallOption) (string, error) {
	m.callCount++
	return m.response, m.err
}
func (m *mockLLMClient) Name() string  { return "mock" }
func (m *mockLLMClient) Model() string { return "mock-model" }

func seedLearnings(t *testing.T, s *storage.Store, project string, n int, category string) {
	t.Helper()
	for i := 0; i < n; i++ {
		_, err := s.InsertLearning(&models.Learning{
			SessionID:  "test-session",
			Category:   category,
			Content:    fmt.Sprintf("learning %s #%d", category, i),
			Project:    project,
			Confidence: 1.0,
			CreatedAt:  time.Now(),
			Source:     "llm_extracted",
		})
		if err != nil {
			t.Fatalf("seed learning: %v", err)
		}
	}
}

func mustOpenStore(t *testing.T) *storage.Store {
	t.Helper()
	s, err := storage.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestGeneratorWritesFile(t *testing.T) {
	cfg := &config.ClaudeMdConfig{
		MaxPerCategory: map[string]int{"gotcha": 5},
		OutputFileName: "yesmem-ops.md",
	}
	gen := NewGenerator(mustOpenStore(t), &mockLLMClient{response: "## Bekannte Fallen\n- test gotcha"}, cfg)

	dir := t.TempDir()
	outPath, err := gen.write(dir, "testproj", "## Bekannte Fallen\n- test gotcha")
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "testproj") {
		t.Error("output missing project name in header")
	}
	if !strings.Contains(string(data), "test gotcha") {
		t.Error("output missing content")
	}
	if !strings.Contains(string(data), "Auto-generated") {
		t.Error("output missing auto-generated header")
	}
	// file must be in .claude/
	if filepath.Base(filepath.Dir(outPath)) != ".claude" {
		t.Errorf("expected output in .claude/, got %s", outPath)
	}
}

func TestGeneratorNeedsRefreshNoState(t *testing.T) {
	cfg := &config.ClaudeMdConfig{MaxPerCategory: map[string]int{}}
	gen := NewGenerator(mustOpenStore(t), nil, cfg)
	needs, err := gen.NeedsRefresh("unknown-project")
	if err != nil {
		t.Fatal(err)
	}
	if !needs {
		t.Error("should need refresh when no state exists")
	}
}

func TestGeneratorWriteMissingDir(t *testing.T) {
	cfg := &config.ClaudeMdConfig{OutputFileName: "yesmem-ops.md"}
	gen := NewGenerator(mustOpenStore(t), nil, cfg)
	_, err := gen.write("/nonexistent/path", "proj", "content")
	if err == nil {
		t.Error("expected error for non-existent project dir")
	}
}

func TestGenerateFullPipeline(t *testing.T) {
	store := mustOpenStore(t)
	seedLearnings(t, store, "testproj", 3, "gotcha")

	dir := t.TempDir()
	mock := &mockLLMClient{response: "## Fallen\n- gotcha 1\n- gotcha 2"}
	cfg := &config.ClaudeMdConfig{
		MaxPerCategory: map[string]int{"gotcha": 5},
		OutputFileName: "yesmem-ops.md",
	}
	gen := NewGenerator(store, mock, cfg)

	err := gen.Generate("testproj", dir)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	// LLM was called
	if mock.callCount != 1 {
		t.Errorf("LLM callCount: got %d, want 1", mock.callCount)
	}

	// File was written
	outPath := filepath.Join(dir, ".claude", "yesmem-ops.md")
	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	if !strings.Contains(string(data), "gotcha 1") {
		t.Error("output missing LLM content")
	}

	// State was saved
	state, err := store.GetClaudeMdState("testproj")
	if err != nil {
		t.Fatal(err)
	}
	if state == nil {
		t.Fatal("state should have been saved after Generate")
	}
	if state.LearningsHash == "" {
		t.Error("state hash should not be empty")
	}
	if state.OutputPath != outPath {
		t.Errorf("state output path: got %q, want %q", state.OutputPath, outPath)
	}
}

func TestGenerateSkipsWhenHashUnchanged(t *testing.T) {
	store := mustOpenStore(t)
	seedLearnings(t, store, "proj", 2, "gotcha")

	dir := t.TempDir()
	mock := &mockLLMClient{response: "## Content"}
	cfg := &config.ClaudeMdConfig{
		MaxPerCategory: map[string]int{"gotcha": 5},
		OutputFileName: "yesmem-ops.md",
	}
	gen := NewGenerator(store, mock, cfg)

	// First call — should generate
	if err := gen.Generate("proj", dir); err != nil {
		t.Fatal(err)
	}
	if mock.callCount != 1 {
		t.Fatalf("first call: LLM callCount %d, want 1", mock.callCount)
	}

	// Second call — same learnings, should skip
	if err := gen.Generate("proj", dir); err != nil {
		t.Fatal(err)
	}
	if mock.callCount != 1 {
		t.Errorf("second call: LLM callCount %d, want still 1 (hash unchanged)", mock.callCount)
	}
}

func TestGenerateRerunsWhenLearningsChange(t *testing.T) {
	store := mustOpenStore(t)
	seedLearnings(t, store, "proj", 2, "gotcha")

	dir := t.TempDir()
	mock := &mockLLMClient{response: "## Content"}
	cfg := &config.ClaudeMdConfig{
		MaxPerCategory: map[string]int{"gotcha": 10, "decision": 10},
		OutputFileName: "yesmem-ops.md",
	}
	gen := NewGenerator(store, mock, cfg)

	// First generate
	if err := gen.Generate("proj", dir); err != nil {
		t.Fatal(err)
	}

	// Add a clearly distinct learning — different category + unique content
	_, err := store.InsertLearning(&models.Learning{
		SessionID:  "new-session",
		Category:   "decision",
		Content:    "Architektur-Entscheidung: PostgreSQL statt MySQL verwenden wegen JSON-Support",
		Project:    "proj",
		Confidence: 1.0,
		CreatedAt:  time.Now(),
		Source:     "user_stated",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Second generate — should call LLM again
	if err := gen.Generate("proj", dir); err != nil {
		t.Fatal(err)
	}
	if mock.callCount != 2 {
		t.Errorf("LLM callCount after adding learning: got %d, want 2", mock.callCount)
	}
}

func TestGenerateNoLearnings(t *testing.T) {
	store := mustOpenStore(t)
	mock := &mockLLMClient{response: "should not be called"}
	cfg := &config.ClaudeMdConfig{
		MaxPerCategory: map[string]int{},
		OutputFileName: "yesmem-ops.md",
	}
	gen := NewGenerator(store, mock, cfg)

	err := gen.Generate("empty-project", t.TempDir())
	if err == nil {
		t.Error("expected error when no learnings exist")
	}
	if mock.callCount != 0 {
		t.Error("LLM should not be called when there are no learnings")
	}
}

func TestGenerateLLMError(t *testing.T) {
	store := mustOpenStore(t)
	seedLearnings(t, store, "proj", 2, "gotcha")

	mock := &mockLLMClient{err: fmt.Errorf("rate limited")}
	cfg := &config.ClaudeMdConfig{
		MaxPerCategory: map[string]int{"gotcha": 5},
		OutputFileName: "yesmem-ops.md",
	}
	gen := NewGenerator(store, mock, cfg)

	err := gen.Generate("proj", t.TempDir())
	if err == nil {
		t.Error("expected error when LLM fails")
	}
	if !strings.Contains(err.Error(), "rate limited") {
		t.Errorf("error should contain LLM error, got: %v", err)
	}

	// No state saved on failure
	state, _ := store.GetClaudeMdState("proj")
	if state != nil {
		t.Error("state should not be saved when LLM call fails")
	}
}

func TestNeedsRefreshHashChanged(t *testing.T) {
	store := mustOpenStore(t)
	seedLearnings(t, store, "proj", 3, "gotcha")

	cfg := &config.ClaudeMdConfig{
		MaxPerCategory: map[string]int{"gotcha": 10, "decision": 10},
		OutputFileName: "yesmem-ops.md",
	}
	gen := NewGenerator(store, &mockLLMClient{response: "ok"}, cfg)

	// Generate to save state with hash
	if err := gen.Generate("proj", t.TempDir()); err != nil {
		t.Fatal(err)
	}

	// Before adding learnings — should NOT need refresh
	needs, err := gen.NeedsRefresh("proj")
	if err != nil {
		t.Fatal(err)
	}
	if needs {
		t.Error("should not need refresh right after Generate")
	}

	// Add a clearly distinct learning — different category + unique content
	_, err = store.InsertLearning(&models.Learning{
		SessionID:  "new-session",
		Category:   "decision",
		Content:    "Deployment-Strategie: Blue-Green statt Rolling-Update wegen Zero-Downtime-Anforderung",
		Project:    "proj",
		Confidence: 1.0,
		CreatedAt:  time.Now(),
		Source:     "user_stated",
	})
	if err != nil {
		t.Fatal(err)
	}

	needs, err = gen.NeedsRefresh("proj")
	if err != nil {
		t.Fatal(err)
	}
	if !needs {
		t.Error("should need refresh after new learning added")
	}
}

func TestHashLearningsDeterministic(t *testing.T) {
	learnings := []models.Learning{
		{ID: 1, Content: "first"},
		{ID: 2, Content: "second"},
	}
	h1 := hashLearnings(learnings)
	h2 := hashLearnings(learnings)
	if h1 != h2 {
		t.Errorf("hash not deterministic: %q != %q", h1, h2)
	}

	// Different content = different hash
	learnings2 := []models.Learning{
		{ID: 1, Content: "first"},
		{ID: 3, Content: "third"},
	}
	h3 := hashLearnings(learnings2)
	if h1 == h3 {
		t.Error("different learnings should produce different hash")
	}
}
