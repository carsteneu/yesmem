package proxy

import (
	"encoding/json"
	"testing"
)

func TestLearningEvaluation_FlexInt(t *testing.T) {
	tests := []struct {
		json    string
		wantID  int64
		wantErr bool
	}{
		{`{"learning_id": 123, "verdict": "valid", "reason": "x", "action": "keep", "impact_score": 0.5}`, 123, false},
		{`{"learning_id": "456", "verdict": "valid", "reason": "x", "action": "keep", "impact_score": 0.5}`, 456, false},
		{`{"learning_id": "", "verdict": "valid", "reason": "x", "action": "keep", "impact_score": 0.5}`, 0, true},
		{`{"learning_id": "not_a_number", "verdict": "valid", "reason": "x", "action": "keep", "impact_score": 0.5}`, 0, true},
	}
	for _, tt := range tests {
		var e LearningEvaluation
		err := json.Unmarshal([]byte(tt.json), &e)
		if tt.wantErr {
			if err == nil {
				t.Errorf("expected error for %s, got nil", tt.json)
			}
			continue
		}
		if err != nil {
			t.Errorf("unexpected error: %v", err)
			continue
		}
		if e.LearningID != tt.wantID {
			t.Errorf("expected learning_id=%d, got %d", tt.wantID, e.LearningID)
		}
	}

	var e LearningEvaluation
	json.Unmarshal([]byte(`{"learning_id": 789, "verdict": "valid", "reason": "r", "action": "a", "impact_score": 0.9}`), &e)
	out, _ := json.Marshal(e)
	var back map[string]any
	json.Unmarshal(out, &back)
	if v, ok := back["learning_id"].(float64); !ok || int64(v) != 789 {
		t.Errorf("round-trip learning_id: got %v", back["learning_id"])
	}
}

func TestContradictionDetected_FlexInt(t *testing.T) {
	jsonStr := `{"learning_a": "100", "learning_b": 200, "description": "conflict"}`
	var c ContradictionDetected
	if err := json.Unmarshal([]byte(jsonStr), &c); err != nil {
		t.Fatal(err)
	}
	if c.LearningA != 100 || c.LearningB != 200 {
		t.Errorf("expected (100,200), got (%d,%d)", c.LearningA, c.LearningB)
	}

	out, _ := json.Marshal(c)
	var back map[string]any
	json.Unmarshal(out, &back)
	if v, ok := back["learning_a"].(float64); !ok || int64(v) != 100 {
		t.Errorf("round-trip learning_a: got %v", back["learning_a"])
	}
}
