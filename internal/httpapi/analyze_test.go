package httpapi

import (
	"encoding/json"
	"testing"
)

func TestAnalyzeTurnRequestParsing(t *testing.T) {
	input := `{
		"session_id": "sess-abc",
		"project": "/tmp/proj",
		"injected_ids": [1, 2, 3],
		"messages": [{"role": "user", "content": "hello"}]
	}`
	var req AnalyzeTurnRequest
	if err := json.Unmarshal([]byte(input), &req); err != nil {
		t.Fatal(err)
	}
	if req.SessionID != "sess-abc" {
		t.Errorf("session_id = %q, want sess-abc", req.SessionID)
	}
	if req.Project != "/tmp/proj" {
		t.Errorf("project = %q, want /tmp/proj", req.Project)
	}
	if len(req.InjectedIDs) != 3 {
		t.Errorf("injected_ids len = %d, want 3", len(req.InjectedIDs))
	}
	if req.InjectedIDs[0] != 1 || req.InjectedIDs[1] != 2 || req.InjectedIDs[2] != 3 {
		t.Errorf("injected_ids = %v, want [1 2 3]", req.InjectedIDs)
	}
	if len(req.Messages) != 1 {
		t.Errorf("messages len = %d, want 1", len(req.Messages))
	}
}

func TestAnalyzeTurnRequestParsingEmpty(t *testing.T) {
	input := `{"session_id": "s1", "project": "p", "messages": []}`
	var req AnalyzeTurnRequest
	if err := json.Unmarshal([]byte(input), &req); err != nil {
		t.Fatal(err)
	}
	if len(req.InjectedIDs) != 0 {
		t.Errorf("injected_ids should be empty, got %v", req.InjectedIDs)
	}
}

func TestAnalyzeTurnCrossCheck(t *testing.T) {
	tests := []struct {
		name          string
		signalIDs     []int64
		injectedIDs   []int64
		wantUsedIDs   []int64
		wantNoiseIDs  []int64
	}{
		{
			name:         "all injected used",
			signalIDs:    []int64{1, 2, 3},
			injectedIDs:  []int64{1, 2, 3},
			wantUsedIDs:  []int64{1, 2, 3},
			wantNoiseIDs: []int64{},
		},
		{
			name:         "none used",
			signalIDs:    []int64{},
			injectedIDs:  []int64{1, 2, 3},
			wantUsedIDs:  []int64{},
			wantNoiseIDs: []int64{1, 2, 3},
		},
		{
			name:         "partial use",
			signalIDs:    []int64{1, 3},
			injectedIDs:  []int64{1, 2, 3},
			wantUsedIDs:  []int64{1, 3},
			wantNoiseIDs: []int64{2},
		},
		{
			name:         "signal mentions non-injected IDs — excluded from used",
			signalIDs:    []int64{1, 99},
			injectedIDs:  []int64{1, 2},
			wantUsedIDs:  []int64{1},
			wantNoiseIDs: []int64{2},
		},
		{
			name:         "empty injected",
			signalIDs:    []int64{5},
			injectedIDs:  []int64{},
			wantUsedIDs:  []int64{},
			wantNoiseIDs: []int64{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			usedIDs, noiseIDs := crossCheckIDs(tc.signalIDs, tc.injectedIDs)
			if !int64SliceEqual(usedIDs, tc.wantUsedIDs) {
				t.Errorf("usedIDs = %v, want %v", usedIDs, tc.wantUsedIDs)
			}
			if !int64SliceEqual(noiseIDs, tc.wantNoiseIDs) {
				t.Errorf("noiseIDs = %v, want %v", noiseIDs, tc.wantNoiseIDs)
			}
		})
	}
}

// int64SliceEqual compares two int64 slices treating nil and empty as equal.
func int64SliceEqual(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
