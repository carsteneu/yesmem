package locomo

import (
	"testing"
)

func TestToolRotationSchedule(t *testing.T) {
	allTools := buildSearchTools()

	tests := []struct {
		round      int
		wantTools  int
		wantName   string // only checked when wantTools == 1
		wantChoice string
	}{
		{0, 1, "hybrid_search", "required"},
		{1, 1, "deep_search", "required"},
		{2, 1, "keyword_search", "required"},
		{3, 3, "", "auto"},
		{4, 3, "", "auto"},
		{10, 3, "", "auto"},
	}

	for _, tt := range tests {
		tools, choice := toolsForRound(tt.round, allTools)
		if len(tools) != tt.wantTools {
			t.Errorf("round %d: expected %d tools, got %d", tt.round, tt.wantTools, len(tools))
		}
		if tt.wantTools == 1 && tools[0].Function.Name != tt.wantName {
			t.Errorf("round %d: expected tool %q, got %q", tt.round, tt.wantName, tools[0].Function.Name)
		}
		if choice != tt.wantChoice {
			t.Errorf("round %d: expected choice %q, got %q", tt.round, tt.wantChoice, choice)
		}
	}
}
