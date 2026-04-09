package proxy

import "testing"

func TestDetectPlanFileRead(t *testing.T) {
	tests := []struct {
		name     string
		messages []any
		want     string
	}{
		{
			name: "Read on plan file in yesdocs/superpowers/plans/",
			messages: []any{
				map[string]any{"role": "assistant", "content": []any{
					map[string]any{
						"type":  "tool_use",
						"id":    "toolu_1",
						"name":  "Read",
						"input": map[string]any{"file_path": "/home/testuser/projects/myapp/yesdocs/superpowers/plans/2026-03-30-feature.md"},
					},
				}},
			},
			want: "/home/testuser/projects/myapp/yesdocs/superpowers/plans/2026-03-30-feature.md",
		},
		{
			name: "Read on non-plan file",
			messages: []any{
				map[string]any{"role": "assistant", "content": []any{
					map[string]any{
						"type":  "tool_use",
						"id":    "toolu_2",
						"name":  "Read",
						"input": map[string]any{"file_path": "/home/testuser/projects/myapp/internal/proxy/proxy.go"},
					},
				}},
			},
			want: "",
		},
		{
			name: "No tool_use",
			messages: []any{
				map[string]any{"role": "user", "content": "hello"},
				map[string]any{"role": "assistant", "content": "hi"},
			},
			want: "",
		},
		{
			name: "plan in generic path",
			messages: []any{
				map[string]any{"role": "assistant", "content": []any{
					map[string]any{
						"type":  "tool_use",
						"id":    "toolu_3",
						"name":  "Read",
						"input": map[string]any{"file_path": "/tmp/my-plan.md"},
					},
				}},
			},
			want: "/tmp/my-plan.md",
		},
		{
			name: "plan in path but not .md — ignored",
			messages: []any{
				map[string]any{"role": "assistant", "content": []any{
					map[string]any{
						"type":  "tool_use",
						"id":    "toolu_4",
						"name":  "Read",
						"input": map[string]any{"file_path": "/tmp/plan.go"},
					},
				}},
			},
			want: "",
		},
		{
			name: "only scans last 4 messages",
			messages: func() []any {
				msgs := make([]any, 10)
				for i := range msgs {
					msgs[i] = map[string]any{"role": "user", "content": "filler"}
				}
				msgs[2] = map[string]any{"role": "assistant", "content": []any{
					map[string]any{
						"type":  "tool_use",
						"id":    "toolu_5",
						"name":  "Read",
						"input": map[string]any{"file_path": "/tmp/old-plan.md"},
					},
				}}
				return msgs
			}(),
			want: "",
		},
		{
			name: "empty messages",
			messages: []any{},
			want:     "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := detectPlanFileRead(tt.messages)
			if got != tt.want {
				t.Errorf("detectPlanFileRead() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestShouldNudgePlan(t *testing.T) {
	tests := []struct {
		name       string
		planFile   string
		activePlan string
		want       bool
	}{
		{"plan file + no active plan → nudge", "/yesdocs/plans/feature.md", "", true},
		{"plan file + active plan → no nudge", "/yesdocs/plans/feature.md", "some plan", false},
		{"no plan file → no nudge", "", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shouldNudgePlan(tt.planFile, tt.activePlan)
			if got != tt.want {
				t.Errorf("shouldNudgePlan(%q, %q) = %v, want %v", tt.planFile, tt.activePlan, got, tt.want)
			}
		})
	}
}
