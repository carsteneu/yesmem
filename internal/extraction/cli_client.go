package extraction

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/carsteneu/yesmem/internal/models"
	"github.com/carsteneu/yesmem/internal/opencode"
)

// CLIClient calls a CLI binary for LLM completions via stdin.
// Supports multiple agents: Claude (claude -p), Codex (codex exec), opencode (opencode run).
type CLIClient struct {
	binary      string // path to CLI binary (e.g. "claude", "codex", "opencode")
	model       string // full model ID
	sourceAgent string // "claude" (default), "codex", "opencode" — drives CLI flag format

	MaxBudgetPerCall float64
	RateLimiter      *RateLimiter // optional: throttles concurrent LLM calls
}

// NewCLIClient creates a CLI-based LLM client for the given agent.
// sourceAgent must be "claude" (default), "codex", or "opencode".
func NewCLIClient(binary, model, sourceAgent string) *CLIClient {
	if binary == "" {
		binary = sourceAgent
	}
	if sourceAgent == "" {
		sourceAgent = models.SourceAgentClaude
	}
	return &CLIClient{
		binary:      binary,
		model:       model,
		sourceAgent: models.NormalizeSourceAgent(sourceAgent),
		RateLimiter: DefaultLLMRateLimiter,
	}
}

// SetMaxBudgetPerCall sets the per-call budget limit on CLI clients.
// No-op for non-CLI clients. Safe to call on any LLMClient.
func SetMaxBudgetPerCall(client LLMClient, usd float64) {
	// Unwrap GatedClient → BudgetClient → inner
	switch c := client.(type) {
	case *CLIClient:
		c.MaxBudgetPerCall = usd
	case *BudgetClient:
		SetMaxBudgetPerCall(c.inner, usd)
	case *GatedClient:
		SetMaxBudgetPerCall(c.Unwrap(), usd)
	}
}

func (c *CLIClient) Name() string  { return "cli" }
func (c *CLIClient) Model() string { return c.model }

// Complete sends a completion request via claude -p.
func (c *CLIClient) Complete(system, userMsg string, opts ...CallOption) (string, error) {
	system = adaptSystemPromptForAgent(c.sourceAgent, system)
	return c.run(system, userMsg, nil, opts...)
}

// CompleteJSON sends a completion request with JSON schema enforcement.
func (c *CLIClient) CompleteJSON(system, userMsg string, schema map[string]any, opts ...CallOption) (string, error) {
	system = adaptSystemPromptForAgent(c.sourceAgent, system)
	return c.run(system, userMsg, schema, opts...)
}

func (c *CLIClient) run(system, userMsg string, schema map[string]any, opts ...CallOption) (string, error) {
	// Acquire rate-limiter slot BEFORE starting the 300s clock.
	// Queue-wait time must not count against the call budget.
	if c.RateLimiter != nil {
		if err := c.RateLimiter.Acquire(context.Background()); err != nil {
			return "", err
		}
		defer c.RateLimiter.Release()
	}

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()

	if c.sourceAgent == models.SourceAgentClaude {
		return c.runClaude(ctx, system, userMsg, schema)
	}
	return c.runStdin(ctx, system, userMsg, schema, applyOpts(opts))
}

func (c *CLIClient) runClaude(ctx context.Context, system, userMsg string, schema map[string]any) (string, error) {
	sysFile, err := os.CreateTemp("", "yesmem-sys-*.txt")
	if err != nil {
		return "", fmt.Errorf("create temp file: %w", err)
	}
	defer os.Remove(sysFile.Name())

	if _, err := sysFile.WriteString(system); err != nil {
		sysFile.Close()
		return "", fmt.Errorf("write system prompt: %w", err)
	}
	sysFile.Close()

	// Write user message to temp file
	msgFile, err := os.CreateTemp("", "yesmem-msg-*.txt")
	if err != nil {
		return "", fmt.Errorf("create msg file: %w", err)
	}
	defer os.Remove(msgFile.Name())
	msgFile.WriteString(userMsg)
	msgFile.Close()

	// Build wrapper script — Go exec.Command can't pass --tools= correctly
	scriptFile, err := os.CreateTemp("", "yesmem-cli-*.sh")
	if err != nil {
		return "", fmt.Errorf("create script: %w", err)
	}
	defer os.Remove(scriptFile.Name())

	budgetFlag := ""
	if c.MaxBudgetPerCall > 0 {
		budgetFlag = fmt.Sprintf(" --max-budget-usd %.2f", c.MaxBudgetPerCall)
	}

	fmt.Fprintf(scriptFile, "#!/bin/sh\nunset ANTHROPIC_BASE_URL CLAUDECODE CLAUDE_CODE_ENTRYPOINT\nexec %s -p --model %s --system-prompt-file %s --max-turns 1 --no-session-persistence --output-format json%s --tools= --mcp-config '{\"mcpServers\":{}}' --strict-mcp-config < %s\n",
		c.binary, c.cliModelName(), sysFile.Name(), budgetFlag, msgFile.Name())
	scriptFile.Close()
	os.Chmod(scriptFile.Name(), 0755)

	cmd := exec.CommandContext(ctx, scriptFile.Name())

	// Unset nested-session guards AND proxy redirect
	cmd.Env = filterEnv(os.Environ(), "CLAUDECODE", "CLAUDE_CODE_ENTRYPOINT", "ANTHROPIC_BASE_URL")
	cmd.Env = append(cmd.Env, "TERM=dumb", "NO_COLOR=1", "YESMEM_DAEMON_CHILD=1")

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		stderrStr := stderr.String()
		if ctx.Err() == context.DeadlineExceeded {
			return "", fmt.Errorf("cli timeout (300s): %s", stderrStr)
		}
		// Check for rate limiting hints in stderr
		if strings.Contains(stderrStr, "rate") || strings.Contains(stderrStr, "limit") {
			return "", fmt.Errorf("rate_limit_error: %s", stderrStr)
		}
		return "", fmt.Errorf("cli error: %w: %s", err, stderrStr)
	}

	result := strings.TrimSpace(stdout.String())

	if result == "" {
		return "", fmt.Errorf("empty response from cli")
	}

	// Parse JSON response — extract result text and report usage
	result = c.extractAndReportUsage(result)

	return result, nil
}

// runStdin pipes system+user prompt via stdin to codex exec / opencode run.
// Simpler than runClaude: no temp files, no JSON output parsing.
// When schema is provided, it is serialized and appended to the prompt
// so that non-Anthropic LLMs know the exact JSON structure to output.
func (c *CLIClient) runStdin(ctx context.Context, system, userMsg string, schema map[string]any, o callOpts) (string, error) {
	prompt := system + "\n\n" + userMsg
	if schema != nil {
		schemaJSON, err := json.MarshalIndent(schema, "", "  ")
		if err == nil {
			prompt += "\n\nOUTPUT FORMAT: You must respond with a single JSON object that matches this schema exactly. Output ONLY the JSON, no markdown fences, no explanatory text:\n" + string(schemaJSON)
		}
	}

	cmdName := c.binary
	cmdArgs := c.stdinArgs(o.sessionID)

	baseEnv := filterEnv(os.Environ(), "ANTHROPIC_BASE_URL", "ANTHROPIC_API_KEY", "OPENAI_API_KEY")
	baseEnv = append(baseEnv, "TERM=dumb", "NO_COLOR=1", "YESMEM_DAEMON_CHILD=1")
	if o.tools {
		baseEnv = append(baseEnv, "YESMEM_ALLOW_CHILD_MCP=1")
	}
	if c.sourceAgent == models.SourceAgentOpencode {
		baseEnv = append(baseEnv,
			"OPENCODE_DISABLE_DEFAULT_PLUGINS=true",
			"OPENCODE_CONFIG_CONTENT="+buildOpencodeConfigOverride(homeDir()),
		)
	}

	cmd := exec.CommandContext(ctx, cmdName, cmdArgs...)
	cmd.Env = baseEnv
	// Use StdinPipe and explicitly close after a short delay to ensure
	// the child process has time to read the full prompt before EOF.
	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		return "", fmt.Errorf("stdin pipe: %w", err)
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("%s start: %w", c.sourceAgent, err)
	}

	// Write the full prompt to stdin, then close immediately (like shell pipe).
	if _, err := stdinPipe.Write([]byte(prompt)); err != nil {
		return "", fmt.Errorf("stdin write: %w", err)
	}
	stdinPipe.Close()

	if err := cmd.Wait(); err != nil {
		stderrStr := stderr.String()
		if ctx.Err() == context.DeadlineExceeded {
			return "", fmt.Errorf("%s timeout (300s): %s", c.sourceAgent, stderrStr)
		}
		if strings.Contains(stderrStr, "rate") || strings.Contains(stderrStr, "limit") {
			return "", fmt.Errorf("rate_limit_error: %s", stderrStr)
		}
		return "", fmt.Errorf("%s error: %w: %s", c.sourceAgent, err, stderrStr)
	}

	result := strings.TrimSpace(stdout.String())
	if result == "" {
		stderrStr := stderr.String()
		if stderrStr != "" {
			return "", fmt.Errorf("%s: empty response + stderr: %s", c.sourceAgent, stderrStr)
		}
		return "", fmt.Errorf("empty response from %s", c.sourceAgent)
	}
	if c.sourceAgent == models.SourceAgentOpencode {
		var err error
		result, err = parseOpencodeOutput(result, c.model)
		if err != nil {
			return "", fmt.Errorf("%s: %w", c.sourceAgent, err)
		}
	}
	return result, nil
}

// parseOpencodeOutput parses opencode ndjson output (--format json):
// collects all type=text part.text fragments and reports usage from type=step_finish.
// Returns an error when no text events are found (empty completion).
func parseOpencodeOutput(output string, model string) (string, error) {
	if output == "" {
		return "", fmt.Errorf("opencode emitted no text events")
	}
	var textParts []string
	hasText := false
	lines := strings.Split(output, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var event struct {
			Type string `json:"type"`
			Part struct {
				Text   string `json:"text"`
				Tokens struct {
					Total  int `json:"total"`
					Input  int `json:"input"`
					Output int `json:"output"`
					Cache  struct {
						Read  int `json:"read"`
						Write int `json:"write"`
					} `json:"cache"`
				} `json:"tokens"`
			} `json:"part"`
		}
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			continue
		}
		switch event.Type {
		case "text":
			if event.Part.Text != "" {
				textParts = append(textParts, event.Part.Text)
				hasText = true
			}
		case "step_finish":
			if OnUsage != nil && event.Part.Tokens.Total > 0 {
				billableIn := event.Part.Tokens.Input + event.Part.Tokens.Cache.Write
				OnUsage(model, billableIn, event.Part.Tokens.Output)
			}
		}
	}
	if !hasText {
		return "", fmt.Errorf("opencode emitted no text events")
	}
	return strings.Join(textParts, ""), nil
}

// stdinArgs returns CLI arguments for piping prompt via stdin.
// opencode: "run" reads from stdin when no argument given.
// codex: "exec" reads from stdin by default.
func (c *CLIClient) stdinArgs(sessionID string) []string {
	switch c.sourceAgent {
	case models.SourceAgentCodex:
		return []string{"exec"}
	case models.SourceAgentOpencode:
		args := []string{"run", "--format", "json"}
		// Pass --model so opencode honors the requested model on both fresh
		// sessions and resumes. Without this, opencode falls back to its
		// opencode.json default and ignores the model field from llm().
		// opencode's --model flag expects "providerID/modelID" format;
		// a bare name like "big-pickle" produces "Unexpected server error".
		if c.model != "" {
			m := c.model
			if !strings.Contains(m, "/") {
				m = "opencode/" + m
			}
			args = append(args, "--model", m)
		}
		if sessionID != "" {
			args = append(args, "--session", sessionID)
		}
		return args
	default:
		return []string{"exec"}
	}
}

// cliModelName maps full model IDs to CLI-friendly names.
func (c *CLIClient) cliModelName() string {
	switch {
	case strings.Contains(c.model, "haiku"):
		return "haiku"
	case strings.Contains(c.model, "sonnet"):
		return "sonnet"
	case strings.Contains(c.model, "opus"):
		return "opus"
	default:
		return c.model
	}
}

// cliResult represents the JSON output from claude -p --output-format json.
type cliResult struct {
	Type         string  `json:"type"`
	Subtype      string  `json:"subtype"`
	Result       string  `json:"result"`
	IsError      bool    `json:"is_error"`
	TotalCostUSD float64 `json:"total_cost_usd"`
	Usage        struct {
		InputTokens              int `json:"input_tokens"`
		OutputTokens             int `json:"output_tokens"`
		CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
		CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	} `json:"usage"`
}

// extractAndReportUsage parses the CLI JSON output, reports token usage
// via OnUsage callback, and returns the LLM response text.
// Falls back to raw output if JSON parsing fails.
func (c *CLIClient) extractAndReportUsage(output string) string {
	var resp cliResult
	if err := json.Unmarshal([]byte(output), &resp); err != nil || resp.Result == "" {
		// Fallback: not valid JSON or no result — return raw output
		return output
	}

	// Report real token usage (same callback as API client)
	if OnUsage != nil {
		// Billable input = non-cached + cache creation (cache reads are discounted)
		inputTokens := resp.Usage.InputTokens + resp.Usage.CacheCreationInputTokens
		OnUsage(c.model, inputTokens, resp.Usage.OutputTokens)
	}

	if resp.TotalCostUSD > 0 {
		log.Printf("CLI call cost: $%.4f (in: %d, out: %d, cache_create: %d, cache_read: %d)",
			resp.TotalCostUSD,
			resp.Usage.InputTokens,
			resp.Usage.OutputTokens,
			resp.Usage.CacheCreationInputTokens,
			resp.Usage.CacheReadInputTokens)
	}

	return resp.Result
}

// adaptSystemPromptForAgent replaces Claude-specific references in a system prompt
// for non-Claude agents (opencode, codex). Claude agents return the prompt unchanged.
func adaptSystemPromptForAgent(sourceAgent, system string) string {
	if sourceAgent == models.SourceAgentClaude || sourceAgent == "" {
		return system
	}
	s := strings.ReplaceAll(system, "Claude Code session", "session")
	s = strings.ReplaceAll(s, "CLAUDE", "THE ASSISTANT")
	s = strings.ReplaceAll(s, "Claude", "the assistant")
	return s
}

// filterEnv returns env without the specified keys.
func filterEnv(env []string, exclude ...string) []string {
	result := make([]string, 0, len(env))
	for _, e := range env {
		skip := false
		for _, key := range exclude {
			if strings.HasPrefix(e, key+"=") {
				skip = true
				break
			}
		}
		if !skip {
			result = append(result, e)
		}
	}
	return result
}

// opencodeProviderOptions is the subset of a provider's options block that we
// rewrite when routing the opencode CLI subprocess through the yesmem proxy.
// Field declaration order determines JSON key order — baseURL before headers
// keeps the output stable and human-readable.
type opencodeProviderOptions struct {
	BaseURL string            `json:"baseURL"`
	Headers map[string]string `json:"headers,omitempty"`
}

// opencodeProviderEntry is one provider entry inside the override. Models and
// other provider-specific fields are preserved as-is so the opencode CLI still
// sees the user's model definitions.
type opencodeProviderEntry map[string]any

// opencodeConfigOverride is the full OPENCODE_CONFIG_CONTENT value. The mcp
// block disables recursion (child yesmem mcp → daemon socket), the provider
// block routes every provider through the yesmem proxy.
type opencodeConfigOverride struct {
	MCP      opencodeMCPConfig                `json:"mcp"`
	Provider map[string]opencodeProviderEntry `json:"provider"`
}

// opencodeMCPConfig mirrors the hardcoded mcp block from the pre-dynamic recipe
// (Learning #73012). Disabling default plugins + defining our own MCP keeps
// the subprocess from recursing into the daemon socket.
type opencodeMCPConfig struct {
	Yesmem struct {
		Type        string            `json:"type"`
		Command     []string          `json:"command"`
		Enabled     bool              `json:"enabled"`
		Timeout     int               `json:"timeout"`
		Environment map[string]string `json:"environment"`
	} `json:"yesmem"`
}

// defaultOpencodeMCPBlock is the mcp subtree byte-identical to the pre-dynamic
// recipe. Used in defaultOpencodeFallbackConfig below; the assembled override
// path goes through opencodeMCPBlockValue() (typed struct) instead.
const defaultOpencodeMCPBlock = `"mcp":{"yesmem":{"type":"local","command":["yesmem","mcp"],"enabled":true,"timeout":120000,"environment":{"YESMEM_SOURCE_AGENT":"opencode"}}}`

// defaultOpencodeFallbackConfig is the byte-identical pre-dynamic fallback,
// used when the user's opencode.json cannot be read. Preserves the DeepSeek-only
// behavior so legacy users see no change when their config is missing.
const defaultOpencodeFallbackConfig = `{` + defaultOpencodeMCPBlock + `,"provider":{"deepseek":{"options":{"baseURL":"http://localhost:9099/v1","headers":{"x-yesmem-allow-mcp":"1"}}}}}`

// yesmemProxyBaseURL is the proxy endpoint the opencode CLI subprocess must
// route through to benefit from SYSTEM.md injection (Learning #73598).
const yesmemProxyBaseURL = "http://localhost:9099/v1"

// yesmemAllowMCPHeader activates SYSTEM.md injection in the proxy openai_handler
// when set on the outgoing request (Learning #73598).
const yesmemAllowMCPHeader = "x-yesmem-allow-mcp"

// buildOpencodeConfigOverride builds the OPENCODE_CONFIG_CONTENT JSON value by
// reading the user's opencode config (opencode.jsonc/json/config.json —
// resolved by opencode.ConfigPath) and rewriting every provider's baseURL to
// the yesmem proxy. Falls back to defaultOpencodeFallbackConfig when the
// user's opencode config is missing or unreadable, preserving the legacy
// DeepSeek-only behavior byte-for-byte.
//
// The override replaces the entire mcp and provider subtrees (Learning #71837):
// the mcp block disables MCP recursion, the provider block routes every
// provider through the proxy so any provider the user has configured
// (deepseek, opencode free-tier, zai-coding-plan, ollama, mistral, …) works
// without per-provider if/else.
func buildOpencodeConfigOverride(home string) string {
	path := opencode.ConfigPath(home)
	data, err := os.ReadFile(path)
	if err != nil {
		return defaultOpencodeFallbackConfig
	}

	var raw struct {
		Provider map[string]map[string]any `json:"provider"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return defaultOpencodeFallbackConfig
	}
	if len(raw.Provider) == 0 {
		return defaultOpencodeFallbackConfig
	}

	override := opencodeConfigOverride{
		MCP:      opencodeMCPBlockValue(),
		Provider: make(map[string]opencodeProviderEntry, len(raw.Provider)),
	}
	for providerID, entry := range raw.Provider {
		rewritten := make(opencodeProviderEntry, len(entry))
		for k, v := range entry {
			rewritten[k] = v
		}
		// Replace the options block wholesale. We discard any user-set nested
		// options fields (headers, timeout, apiKey, errorRetry, …) because the
		// proxy must win on baseURL and the x-yesmem-allow-mcp header is our
		// own signal. The proxy re-injects provider auth from daemon storage,
		// so dropping user headers is intentional, not a bug. Top-level fields
		// (npm, models, name, limit, interleaved, …) are preserved.
		opts := opencodeProviderOptions{
			BaseURL: yesmemProxyBaseURL,
			Headers: map[string]string{yesmemAllowMCPHeader: "1"},
		}
		rewritten["options"] = opts
		override.Provider[providerID] = rewritten
	}

	out, err := json.Marshal(override)
	if err != nil {
		return defaultOpencodeFallbackConfig
	}
	return string(out)
}

// opencodeMCPBlockValue returns the mcp subtree as a typed struct so the
// assembled override serializes byte-identical to defaultOpencodeMCPBlock.
func opencodeMCPBlockValue() opencodeMCPConfig {
	var m opencodeMCPConfig
	m.Yesmem.Type = "local"
	m.Yesmem.Command = []string{"yesmem", "mcp"}
	m.Yesmem.Enabled = true
	m.Yesmem.Timeout = 120000
	m.Yesmem.Environment = map[string]string{"YESMEM_SOURCE_AGENT": "opencode"}
	return m
}

// homeDir returns the current user's home directory. Falls back to "" on error
// (callers pass the empty string to buildOpencodeConfigOverride, which then
// can't read opencode.json and returns the fallback).
func homeDir() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return h
}
