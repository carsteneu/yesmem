
package locomo

import (
	"strings"
	"testing"
)

const miniDataset = `[{
	"sample_id": "1",
	"conversation": {
		"speaker_a": "Alice",
		"speaker_b": "Bob",
		"session_1": [
			{"speaker": "speaker_a", "dia_id": "1", "text": "Hi Bob, how was your trip to Paris?"},
			{"speaker": "speaker_b", "dia_id": "2", "text": "Paris was amazing! I went in March."}
		],
		"session_1_date_time": "2023-03-15T10:00:00",
		"session_2": [
			{"speaker": "speaker_a", "dia_id": "3", "text": "Did you try the food in Paris?"},
			{"speaker": "speaker_b", "dia_id": "4", "text": "Yes, the croissants were incredible."}
		],
		"session_2_date_time": "2023-03-20T14:00:00"
	},
	"qa": [
		{"question": "Where did Bob travel?", "answer": "Paris", "category": 1, "evidence": ["1","2"]},
		{"question": "When did Bob go to Paris?", "answer": "March", "category": 2, "evidence": ["2"]},
		{"question": "What food did Bob enjoy in Paris?", "answer": "Croissants", "category": 1, "evidence": ["4"]},
		{"question": "Did Alice visit London?", "answer": "There is no information about Alice visiting London.", "category": 5, "evidence": []}
	]
}]`

func TestParseDataset(t *testing.T) {
	samples, err := ParseDataset(strings.NewReader(miniDataset))
	if err != nil {
		t.Fatalf("ParseDataset: %v", err)
	}
	if len(samples) != 1 {
		t.Fatalf("expected 1 sample, got %d", len(samples))
	}

	s := samples[0]
	if s.ID != "1" {
		t.Errorf("sample ID = %q, want %q", s.ID, "1")
	}

	// Should have 2 sessions extracted from dynamic session_N keys
	if len(s.Sessions) != 2 {
		t.Fatalf("expected 2 sessions, got %d", len(s.Sessions))
	}

	// Session 1
	sess1 := s.Sessions[0]
	if sess1.Index != 1 {
		t.Errorf("session 1 index = %d, want 1", sess1.Index)
	}
	if len(sess1.Turns) != 2 {
		t.Fatalf("session 1: expected 2 turns, got %d", len(sess1.Turns))
	}
	if sess1.Turns[0].Speaker != "speaker_a" {
		t.Errorf("turn 0 speaker = %q, want %q", sess1.Turns[0].Speaker, "speaker_a")
	}
	if sess1.Turns[0].DiaID != "1" {
		t.Errorf("turn 0 dia_id = %q, want %q", sess1.Turns[0].DiaID, "1")
	}
	if sess1.DateTime != "2023-03-15T10:00:00" {
		t.Errorf("session 1 datetime = %q, want %q", sess1.DateTime, "2023-03-15T10:00:00")
	}

	// Session 2
	sess2 := s.Sessions[1]
	if sess2.Index != 2 {
		t.Errorf("session 2 index = %d, want 2", sess2.Index)
	}
	if len(sess2.Turns) != 2 {
		t.Fatalf("session 2: expected 2 turns, got %d", len(sess2.Turns))
	}
	if sess2.Turns[1].Text != "Yes, the croissants were incredible." {
		t.Errorf("session 2 turn 1 text = %q", sess2.Turns[1].Text)
	}

	// Speaker names
	if s.SpeakerA != "Alice" {
		t.Errorf("speaker_a = %q, want %q", s.SpeakerA, "Alice")
	}
	if s.SpeakerB != "Bob" {
		t.Errorf("speaker_b = %q, want %q", s.SpeakerB, "Bob")
	}

	// QA pairs
	if len(s.QA) != 4 {
		t.Fatalf("expected 4 QA pairs, got %d", len(s.QA))
	}
	if s.QA[0].Question != "Where did Bob travel?" {
		t.Errorf("qa[0] question = %q", s.QA[0].Question)
	}
	if s.QA[0].Answer != "Paris" {
		t.Errorf("qa[0] answer = %q", s.QA[0].Answer)
	}
	if s.QA[0].Category != 1 {
		t.Errorf("qa[0] category = %d, want 1", s.QA[0].Category)
	}
	if len(s.QA[0].Evidence) != 2 {
		t.Errorf("qa[0] evidence len = %d, want 2", len(s.QA[0].Evidence))
	}

	// Category 5 QA (unanswerable) should have empty evidence
	if len(s.QA[3].Evidence) != 0 {
		t.Errorf("qa[3] evidence should be empty, got %d", len(s.QA[3].Evidence))
	}
}

func TestParseDatasetScoredQA(t *testing.T) {
	samples, err := ParseDataset(strings.NewReader(miniDataset))
	if err != nil {
		t.Fatalf("ParseDataset: %v", err)
	}

	scored := samples[0].ScoredQA()

	// Should filter out category 5 — only categories 1-4 remain
	if len(scored) != 3 {
		t.Fatalf("ScoredQA: expected 3, got %d", len(scored))
	}

	// Verify no category 5 slipped through
	for _, qa := range scored {
		if qa.Category == 5 {
			t.Errorf("ScoredQA should not contain category 5")
		}
	}

	// Verify category names (defined in report.go)
	if name := CategoryName(1); name != "Single-hop" {
		t.Errorf("CategoryName(1) = %q, want %q", name, "Single-hop")
	}
	if name := CategoryName(2); name != "Multi-hop" {
		t.Errorf("CategoryName(2) = %q, want %q", name, "Multi-hop")
	}
	if name := CategoryName(3); name != "Temporal" {
		t.Errorf("CategoryName(3) = %q, want %q", name, "Temporal")
	}
	if name := CategoryName(4); name != "Open-domain" {
		t.Errorf("CategoryName(4) = %q, want %q", name, "Open-domain")
	}
	if name := CategoryName(5); name != "Adversarial" {
		t.Errorf("CategoryName(5) = %q, want %q", name, "Adversarial")
	}
	if name := CategoryName(99); name != "Unknown(99)" {
		t.Errorf("CategoryName(99) = %q, want %q", name, "Unknown(99)")
	}
}
