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
)

// CLIClient calls the claude CLI binary for LLM completions.
// Used as fallback for Pro/Max subscribers without API keys.
type CLIClient struct {
	binary string // path to claude binary
	model  string // full model ID (e.g. "claude-haiku-4-5-20251001")

	// MaxBudgetPerCall is passed as --max-budget-usd to the CLI (safety net per call).
	// 0 = no per-call limit. Set via SetMaxBudgetPerCall().
	MaxBudgetPerCall float64
}

// NewCLIClient creates a CLI-based LLM client.
func NewCLIClient(binary, model string) *CLIClient {
	return &CLIClient{
		binary: binary,
		model:  model,
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
	return c.run(system, userMsg, nil)
}

// CompleteJSON sends a completion request with JSON schema enforcement.
func (c *CLIClient) CompleteJSON(system, userMsg string, schema map[string]any, opts ...CallOption) (string, error) {
	return c.run(system, userMsg, schema)
}

func (c *CLIClient) run(system, userMsg string, schema map[string]any) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	// Write system prompt to temp file (can be very long for extraction prompts)
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

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		stderrStr := stderr.String()
		if ctx.Err() == context.DeadlineExceeded {
			return "", fmt.Errorf("cli timeout (120s): %s", stderrStr)
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
