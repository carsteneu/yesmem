package proxy

import "testing"

func TestNormalizeThinkingType(t *testing.T) {
	tests := []struct {
		name     string
		req      map[string]any
		want     bool // modified?
		wantType string
		wantOC   map[string]any // output_config after
	}{
		{
			name: "opus-4-7 with enabled converts to adaptive",
			req: map[string]any{
				"model":    "claude-opus-4-7",
				"thinking": map[string]any{"type": "enabled", "budget_tokens": 10000},
			},
			want:     true,
			wantType: "adaptive",
			wantOC:   map[string]any{"effort": "high"},
		},
		{
			name: "opus-4-7 dated variant also converts",
			req: map[string]any{
				"model":    "claude-opus-4-7-20260415",
				"thinking": map[string]any{"type": "enabled", "budget_tokens": 5000},
			},
			want:     true,
			wantType: "adaptive",
			wantOC:   map[string]any{"effort": "high"},
		},
		{
			name: "already adaptive no change",
			req: map[string]any{
				"model":    "claude-opus-4-7",
				"thinking": map[string]any{"type": "adaptive"},
			},
			want: false,
		},
		{
			name: "old model keeps enabled",
			req: map[string]any{
				"model":    "claude-sonnet-4-5-20250514",
				"thinking": map[string]any{"type": "enabled", "budget_tokens": 10000},
			},
			want: false,
		},
		{
			name: "no thinking block no change",
			req: map[string]any{
				"model": "claude-opus-4-7",
			},
			want: false,
		},
		{
			name: "preserves existing output_config effort",
			req: map[string]any{
				"model":         "claude-opus-4-7",
				"thinking":      map[string]any{"type": "enabled", "budget_tokens": 8000},
				"output_config": map[string]any{"effort": "low"},
			},
			want:     true,
			wantType: "adaptive",
			wantOC:   map[string]any{"effort": "low"},
		},
		{
			name: "merges into existing output_config",
			req: map[string]any{
				"model":         "claude-opus-4-7",
				"thinking":      map[string]any{"type": "enabled", "budget_tokens": 3000},
				"output_config": map[string]any{"format": "json"},
			},
			want:     true,
			wantType: "adaptive",
			wantOC:   map[string]any{"format": "json", "effort": "high"},
		},
		{
			name: "sonnet-4-6 with enabled converts to adaptive",
			req: map[string]any{
				"model":    "claude-sonnet-4-6-20260415",
				"thinking": map[string]any{"type": "enabled", "budget_tokens": 10000},
			},
			want:     true,
			wantType: "adaptive",
			wantOC:   map[string]any{"effort": "high"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NormalizeThinkingType(tt.req)
			if got != tt.want {
				t.Fatalf("NormalizeThinkingType() = %v, want %v", got, tt.want)
			}
			if !tt.want {
				return
			}
			th, _ := tt.req["thinking"].(map[string]any)
			if th == nil {
				t.Fatal("thinking block missing after normalize")
			}
			if th["type"] != tt.wantType {
				t.Errorf("thinking.type = %q, want %q", th["type"], tt.wantType)
			}
			oc, _ := tt.req["output_config"].(map[string]any)
			if oc == nil {
				t.Fatal("output_config missing after normalize")
			}
			for k, v := range tt.wantOC {
				if oc[k] != v {
					t.Errorf("output_config[%s] = %v, want %v", k, oc[k], v)
				}
			}
		})
	}
}
