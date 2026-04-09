package daemon

import (
	"strings"
	"testing"
	"time"

	"github.com/carsteneu/yesmem/internal/extraction"
	"github.com/carsteneu/yesmem/internal/models"
	"github.com/carsteneu/yesmem/internal/storage"
)

// capturingMockClient is a mock LLMClient that captures prompts and returns canned responses.
type capturingMockClient struct {
	lastSystem      string
	lastUserMessage string
	completeResp    string
	jsonResp        string
}

func (m *capturingMockClient) Complete(system, userMsg string, opts ...extraction.CallOption) (string, error) {
	m.lastSystem = system
	m.lastUserMessage = userMsg
	return m.completeResp, nil
}

func (m *capturingMockClient) CompleteJSON(system, userMsg string, schema map[string]any, opts ...extraction.CallOption) (string, error) {
	m.lastSystem = system
	m.lastUserMessage = userMsg
	return m.jsonResp, nil
}

func (m *capturingMockClient) Name() string  { return "mock" }
func (m *capturingMockClient) Model() string { return "mock-model" }

func TestSynthesizePersonaDirectiveIncludesPivots(t *testing.T) {
	s, err := storage.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	// Seed well-evidenced traits so synthesis proceeds
	s.UpsertPersonaTrait(&models.PersonaTrait{
		UserID: "default", Dimension: "communication", TraitKey: "language",
		TraitValue: "de", Confidence: 0.9, Source: "auto_extracted", EvidenceCount: 5,
	})
	s.UpsertPersonaTrait(&models.PersonaTrait{
		UserID: "default", Dimension: "workflow", TraitKey: "autonomy",
		TraitValue: "high", Confidence: 0.8, Source: "auto_extracted", EvidenceCount: 4,
	})

	// Seed pivot_moment learnings
	s.InsertLearning(&models.Learning{
		Category: "pivot_moment", Content: "User sagte 'immer optimal' — tiefe Praeferenz erkannt",
		Project: "memory", Confidence: 1.0, CreatedAt: time.Now(),
		Source: "llm_extracted", HitCount: 3, EmotionalIntensity: 0.8,
	})
	s.InsertLearning(&models.Learning{
		Category: "pivot_moment", Content: "User fragte 'wie schaffen wir dass du yesmem oefter nutzt'",
		Project: "memory", Confidence: 1.0, CreatedAt: time.Now(),
		Source: "llm_extracted", HitCount: 2, EmotionalIntensity: 0.7,
	})

	mock := &capturingMockClient{
		completeResp: "335 Sessions. Der Mensch der 'immer optimal' gesagt hat.\n\nHARTE REGELN:\n- Deutsch, du",
	}

	synthesizePersonaDirective(s, mock)

	// The user message sent to the LLM should contain the pivot content
	if mock.lastUserMessage == "" {
		t.Fatal("mock should have received a user message")
	}
	if !strings.Contains(mock.lastUserMessage, "immer optimal") {
		t.Error("user message should contain first pivot moment content")
	}
	if !strings.Contains(mock.lastUserMessage, "yesmem oefter nutzt") {
		t.Error("user message should contain second pivot moment content")
	}
	if !strings.Contains(mock.lastUserMessage, "Key moments") {
		t.Error("user message should contain Key moments section header")
	}
}

func TestSynthesizePersonaDirectiveNoPivots(t *testing.T) {
	s, err := storage.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	// Seed traits but NO pivot moments
	s.UpsertPersonaTrait(&models.PersonaTrait{
		UserID: "default", Dimension: "communication", TraitKey: "language",
		TraitValue: "de", Confidence: 0.9, Source: "auto_extracted", EvidenceCount: 5,
	})

	mock := &capturingMockClient{
		completeResp: "Test directive without pivots.",
	}

	synthesizePersonaDirective(s, mock)

	// Should still work, just without pivots section
	if mock.lastUserMessage == "" {
		t.Fatal("mock should have received a user message")
	}
	if strings.Contains(mock.lastUserMessage, "Schluesselmomente") {
		t.Error("user message should NOT contain Schluesselmomente when no pivots exist")
	}
}

func TestLoadTopPivotMoments(t *testing.T) {
	s, err := storage.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	// Insert 7 pivot moments
	for i := 0; i < 7; i++ {
		s.InsertLearning(&models.Learning{
			Category: "pivot_moment", Content: "Pivot moment number " + string(rune('A'+i)),
			Project: "test", Confidence: 1.0, CreatedAt: time.Now(),
			Source: "llm_extracted", HitCount: i, EmotionalIntensity: float64(i) * 0.1,
		})
	}

	pivots := loadTopPivotMoments(s, 5)
	if len(pivots) != 5 {
		t.Errorf("expected 5 pivots (limit), got %d", len(pivots))
	}
}

func TestLoadTopPivotMomentsEmpty(t *testing.T) {
	s, err := storage.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	pivots := loadTopPivotMoments(s, 5)
	if pivots != nil {
		t.Errorf("expected nil for empty store, got %d pivots", len(pivots))
	}
}
