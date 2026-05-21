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
		c := NewCLIClient("/usr/bin/claude", tt.model, "")
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
	c := NewCLIClient("claude", "claude-haiku-4-5-20251001", "")
	if c.Name() != "cli" {
		t.Errorf("expected 'cli', got %q", c.Name())
	}
	if c.Model() != "claude-haiku-4-5-20251001" {
		t.Errorf("expected model, got %q", c.Model())
	}
}

func TestAdaptSystemPromptForAgent_ClaudeUnchanged(t *testing.T) {
	prompt := "Distinguish: Did the USER say something, or did CLAUDE suggest it?"
	result := adaptSystemPromptForAgent("claude", prompt)
	if result != prompt {
		t.Errorf("claude agent should not modify prompt: got %q", result)
	}
}

func TestAdaptSystemPromptForAgent_EmptyAgentUnchanged(t *testing.T) {
	prompt := "Distinguish: Did the USER say something, or did CLAUDE suggest it?"
	result := adaptSystemPromptForAgent("", prompt)
	if result != prompt {
		t.Errorf("empty agent should not modify prompt: got %q", result)
	}
}

func TestAdaptSystemPromptForAgent_OpencodeReplacesClaude(t *testing.T) {
	prompt := "Distinguish: Did the USER say something, or did CLAUDE suggest it? Only what the user said, not what Claude suggested."
	result := adaptSystemPromptForAgent("opencode", prompt)
	if contains(result, "CLAUDE") {
		t.Errorf("opencode agent should replace CLAUDE: got %q", result)
	}
	if contains(result, "Claude") {
		t.Errorf("opencode agent should replace Claude: got %q", result)
	}
	if !contains(result, "THE ASSISTANT") {
		t.Errorf("opencode agent should use THE ASSISTANT: got %q", result)
	}
	if !contains(result, "the assistant") {
		t.Errorf("opencode agent should use the assistant: got %q", result)
	}
}

func TestAdaptSystemPromptForAgent_CodexReplacesClaude(t *testing.T) {
	prompt := "not when Claude suggests it"
	result := adaptSystemPromptForAgent("codex", prompt)
	if result == prompt {
		t.Errorf("codex agent should modify prompt: got %q", result)
	}
	if contains(result, "Claude") {
		t.Errorf("codex agent should replace Claude: got %q", result)
	}
}

func TestAdaptSystemPromptForAgent_ClaudeCodeSession(t *testing.T) {
	prompt := "You read an excerpt from a Claude Code session (user + assistant messages)."
	result := adaptSystemPromptForAgent("opencode", prompt)
	if contains(result, "Claude Code session") {
		t.Errorf("opencode agent should replace Claude Code session: got %q", result)
	}
	if !contains(result, "session") {
		t.Errorf("result should still contain 'session': got %q", result)
	}
}

func TestAdaptSystemPromptForAgent_Idempotent(t *testing.T) {
	prompt := "Distinguish: Did the USER say something, or did CLAUDE suggest it?"
	first := adaptSystemPromptForAgent("opencode", prompt)
	second := adaptSystemPromptForAgent("opencode", first)
	if first != second {
		t.Errorf("adapt should be idempotent: first=%q second=%q", first, second)
	}
}

func TestRunDispatch_OpencodeUsesStdin(t *testing.T) {
	c := NewCLIClient("opencode", "deepseek-v4-pro", "opencode")
	if c.sourceAgent != "opencode" {
		t.Fatalf("expected sourceAgent=opencode, got %q", c.sourceAgent)
	}
}

func TestRunDispatch_ClaudeUsesRunClaude(t *testing.T) {
	c := NewCLIClient("claude", "claude-sonnet-4-6", "claude")
	if c.sourceAgent != "claude" {
		t.Fatalf("expected sourceAgent=claude, got %q", c.sourceAgent)
	}
}

func TestStdinArgs_OpencodeIncludesFormatJSON(t *testing.T) {
	c := NewCLIClient("opencode", "deepseek-v4-pro", "opencode")
	args := c.stdinArgs("")
	if len(args) < 3 {
		t.Fatalf("expected at least 3 args, got %d: %v", len(args), args)
	}
	if args[0] != "run" {
		t.Errorf("expected 'run', got %q", args[0])
	}
	if args[1] != "--format" || args[2] != "json" {
		t.Errorf("expected --format json after run, got %v", args[1:3])
	}
	foundPure := false
	for _, a := range args {
		if a == "--pure" {
			foundPure = true
			break
		}
	}
	if !foundPure {
		t.Errorf("expected --pure flag in args: %v", args)
	}
}

func TestStdinArgs_OpencodeWithSessionIncludesFormatJSON(t *testing.T) {
	c := NewCLIClient("opencode", "deepseek-v4-pro", "opencode")
	args := c.stdinArgs("ses_test123")
	if len(args) < 5 {
		t.Fatalf("expected at least 5 args, got %d: %v", len(args), args)
	}
	foundSession := false
	foundFormat := false
	for i, a := range args {
		if a == "--session" && i+1 < len(args) && args[i+1] == "ses_test123" {
			foundSession = true
		}
		if a == "--format" && i+1 < len(args) && args[i+1] == "json" {
			foundFormat = true
		}
	}
	if !foundSession {
		t.Errorf("expected --session ses_test123 in args: %v", args)
	}
	if !foundFormat {
		t.Errorf("expected --format json in args: %v", args)
	}
}

func TestParseOpencodeOutput_TextOnly(t *testing.T) {
	input := `{"type":"text","timestamp":123,"sessionID":"ses_x","part":{"text":"Hallo."}}
{"type":"step_finish","timestamp":124,"sessionID":"ses_x","part":{"reason":"stop","tokens":{"total":100,"input":95,"output":5}}}` + "\n"
	got, err := parseOpencodeOutput(input, "test-model")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "Hallo." {
		t.Errorf("expected 'Hallo.', got %q", got)
	}
}

func TestParseOpencodeOutput_MultipleTextParts(t *testing.T) {
	input := `{"type":"text","timestamp":123,"sessionID":"ses_x","part":{"text":"Teil 1"}}
{"type":"text","timestamp":124,"sessionID":"ses_x","part":{"text":" Teil 2"}}
{"type":"step_finish","timestamp":125,"sessionID":"ses_x","part":{"reason":"stop","tokens":{"total":200,"input":180,"output":20}}}` + "\n"
	got, err := parseOpencodeOutput(input, "test-model")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "Teil 1 Teil 2" {
		t.Errorf("expected 'Teil 1 Teil 2', got %q", got)
	}
}

func TestParseOpencodeOutput_IgnoresNonTextEvents(t *testing.T) {
	input := `{"type":"step_start","timestamp":123,"sessionID":"ses_x","part":{"type":"step-start"}}
{"type":"tool_use","timestamp":124,"sessionID":"ses_x","part":{"tool":"some_tool","callID":"call_00"}}
{"type":"text","timestamp":125,"sessionID":"ses_x","part":{"text":"Antwort"}}
{"type":"step_finish","timestamp":126,"sessionID":"ses_x","part":{"reason":"stop","tokens":{"total":150,"input":140,"output":10}}}` + "\n"
	got, err := parseOpencodeOutput(input, "test-model")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "Antwort" {
		t.Errorf("expected 'Antwort', got %q", got)
	}
}

func TestParseOpencodeOutput_EmptyInput(t *testing.T) {
	_, err := parseOpencodeOutput("", "test-model")
	if err == nil {
		t.Fatal("expected error for empty input")
	}
}

func TestParseOpencodeOutput_NoTextParts(t *testing.T) {
	input := `{"type":"step_start","timestamp":123,"sessionID":"ses_x","part":{"type":"step-start"}}
{"type":"step_finish","timestamp":124,"sessionID":"ses_x","part":{"reason":"stop","tokens":{"total":50,"input":50,"output":0}}}` + "\n"
	_, err := parseOpencodeOutput(input, "test-model")
	if err == nil {
		t.Fatal("expected error when no text events present")
	}
}

func TestParseOpencodeOutput_OnUsageCallback(t *testing.T) {
	var capturedModel string
	var capturedIn, capturedOut int
	prevOnUsage := OnUsage
	OnUsage = func(model string, in, out int) {
		capturedModel = model
		capturedIn = in
		capturedOut = out
	}
	defer func() { OnUsage = prevOnUsage }()

	input := `{"type":"text","timestamp":123,"sessionID":"ses_x","part":{"text":"ok"}}
{"type":"step_finish","timestamp":124,"sessionID":"ses_x","part":{"reason":"stop","tokens":{"total":100,"input":42,"output":7,"cache":{"read":10,"write":5}}}}` + "\n"
	got, err := parseOpencodeOutput(input, "t-model")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "ok" {
		t.Errorf("expected 'ok', got %q", got)
	}
	if capturedModel != "t-model" {
		t.Errorf("expected model 't-model', got %q", capturedModel)
	}
	if capturedIn != 47 {
		t.Errorf("expected billable input 47 (42 raw + 5 cache write), got %d", capturedIn)
	}
	if capturedOut != 7 {
		t.Errorf("expected output 7, got %d", capturedOut)
	}
}

func contains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
