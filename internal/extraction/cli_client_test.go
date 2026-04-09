package extraction

import (
	"testing"
)

func TestCLIClientImplementsInterface(t *testing.T) {
	var _ LLMClient = (*CLIClient)(nil)
}

func TestAPIClientImplementsInterface(t *testing.T) {
	var _ LLMClient = (*Client)(nil)
}

func TestCLIModelName(t *testing.T) {
	tests := []struct {
		model string
		want  string
	}{
		{"claude-haiku-4-5-20251001", "haiku"},
		{"claude-sonnet-4-6", "sonnet"},
		{"claude-opus-4-6", "opus"},
		{"some-custom-model", "some-custom-model"},
	}

	for _, tt := range tests {
		c := NewCLIClient("/usr/bin/claude", tt.model)
		if got := c.cliModelName(); got != tt.want {
			t.Errorf("cliModelName(%q) = %q, want %q", tt.model, got, tt.want)
		}
	}
}

func TestFilterEnv(t *testing.T) {
	env := []string{
		"HOME=/home/user",
		"CLAUDECODE=1",
		"CLAUDE_CODE_ENTRYPOINT=mcp",
		"PATH=/usr/bin",
	}

	filtered := filterEnv(env, "CLAUDECODE", "CLAUDE_CODE_ENTRYPOINT")

	if len(filtered) != 2 {
		t.Fatalf("expected 2 env vars, got %d: %v", len(filtered), filtered)
	}

	for _, e := range filtered {
		if e == "CLAUDECODE=1" || e == "CLAUDE_CODE_ENTRYPOINT=mcp" {
			t.Errorf("should have been filtered: %s", e)
		}
	}
}

func TestNewLLMClientAutoNoKey(t *testing.T) {
	// Auto mode without API key and no claude binary → nil, nil
	client, err := NewLLMClient("auto", "", "model", "/nonexistent/path/to/claude", "")
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if client != nil {
		t.Fatalf("expected nil client, got %v", client)
	}
}

func TestNewLLMClientAPI(t *testing.T) {
	client, err := NewLLMClient("api", "sk-test", "claude-haiku-4-5-20251001", "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if client == nil {
		t.Fatal("expected non-nil client")
	}
	if client.Name() != "api" {
		t.Errorf("expected 'api', got %q", client.Name())
	}
	if client.Model() != "claude-haiku-4-5-20251001" {
		t.Errorf("expected model ID, got %q", client.Model())
	}
}

func TestNewLLMClientAPINoKey(t *testing.T) {
	_, err := NewLLMClient("api", "", "model", "", "")
	if err == nil {
		t.Fatal("expected error for api without key")
	}
}

func TestNewLLMClientInvalidProvider(t *testing.T) {
	_, err := NewLLMClient("invalid", "", "", "", "")
	if err == nil {
		t.Fatal("expected error for invalid provider")
	}
}

func TestNewLLMClientOpenAI(t *testing.T) {
	client, err := NewLLMClient("openai", "sk-openai", "gpt-5.2", "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	oc, ok := client.(*OpenAIClient)
	if !ok {
		t.Fatalf("client type = %T, want *OpenAIClient", client)
	}
	if oc.Name() != "openai" {
		t.Fatalf("Name() = %q, want openai", oc.Name())
	}
	if oc.endpoint != defaultOpenAIResponsesURL {
		t.Fatalf("endpoint = %q, want %q", oc.endpoint, defaultOpenAIResponsesURL)
	}
}

func TestNewLLMClientOpenAICompatible(t *testing.T) {
	client, err := NewLLMClient("openai_compatible", "sk-openai", "gpt-5.2", "", "https://gateway.example")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	oc, ok := client.(*OpenAIClient)
	if !ok {
		t.Fatalf("client type = %T, want *OpenAIClient", client)
	}
	if oc.Name() != "openai_compatible" {
		t.Fatalf("Name() = %q, want openai_compatible", oc.Name())
	}
	if oc.endpoint != "https://gateway.example/v1/responses" {
		t.Fatalf("endpoint = %q", oc.endpoint)
	}
}

func TestCLIClientName(t *testing.T) {
	c := NewCLIClient("claude", "claude-haiku-4-5-20251001")
	if c.Name() != "cli" {
		t.Errorf("expected 'cli', got %q", c.Name())
	}
	if c.Model() != "claude-haiku-4-5-20251001" {
		t.Errorf("expected model, got %q", c.Model())
	}
}
