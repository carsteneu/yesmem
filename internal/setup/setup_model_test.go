package setup

import "testing"

func TestDerivePrimaryAndSmallModel(t *testing.T) {
	tests := []struct {
		name        string
		provider    string
		chosenModel string
		providerID  string
		baseURL     string
		wantPrimary string
		wantSmall   string
	}{
		{
			name:        "FreeTier big-pickle",
			provider:    "opencode",
			chosenModel: "big-pickle",
			providerID:  "opencode",
			baseURL:     "https://opencode.ai/zen/v1",
			wantPrimary: "opencode/big-pickle",
			wantSmall:   "opencode/big-pickle",
		},
		{
			name:        "DeepSeek paid two-tier",
			provider:    "openai_compatible",
			chosenModel: "deepseek-v4-flash",
			providerID:  "deepseek",
			baseURL:     "https://api.deepseek.com/v1",
			wantPrimary: "deepseek/deepseek-reasoner",
			wantSmall:   "deepseek/deepseek-chat",
		},
		{
			name:        "GLM paid via zai-coding-plan",
			provider:    "openai_compatible",
			chosenModel: "glm-5.2",
			providerID:  "zai-coding-plan",
			baseURL:     "https://api.z.ai/v1",
			wantPrimary: "zai-coding-plan/glm-5.2",
			wantSmall:   "zai-coding-plan/glm-5.2",
		},
		{
			name:        "Claude+Codex via CLI (Anthropic)",
			provider:    "cli",
			chosenModel: "sonnet",
			providerID:  "",
			baseURL:     "",
			wantPrimary: "anthropic/sonnet",
			wantSmall:   "anthropic/haiku",
		},
		{
			name:        "Anthropic via API key (provider=api)",
			provider:    "api",
			chosenModel: "sonnet",
			providerID:  "",
			baseURL:     "",
			wantPrimary: "anthropic/sonnet",
			wantSmall:   "anthropic/haiku",
		},
		{
			name:        "Codex-only OpenAI direct",
			provider:    "openai_compatible",
			chosenModel: "gpt-5.5-codex",
			providerID:  "",
			baseURL:     "https://api.openai.com/v1",
			wantPrimary: "openai/gpt-5.5-codex",
			wantSmall:   "openai/gpt-5.5-codex",
		},
		{
			name:        "OpenAI-compatible with providerID hint",
			provider:    "openai_compatible",
			chosenModel: "custom-model",
			providerID:  "acme",
			baseURL:     "https://api.acme.com/v1",
			wantPrimary: "acme/custom-model",
			wantSmall:   "acme/custom-model",
		},
		{
			name:        "Unknown provider falls back to bare model",
			provider:    "unknown",
			chosenModel: "weird-model",
			providerID:  "",
			baseURL:     "",
			wantPrimary: "weird-model",
			wantSmall:   "weird-model",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotPrimary, gotSmall := derivePrimaryAndSmallModel(tt.provider, tt.chosenModel, tt.providerID, tt.baseURL)
			if gotPrimary != tt.wantPrimary {
				t.Errorf("primaryModel = %q, want %q", gotPrimary, tt.wantPrimary)
			}
			if gotSmall != tt.wantSmall {
				t.Errorf("smallModel = %q, want %q", gotSmall, tt.wantSmall)
			}
		})
	}
}
