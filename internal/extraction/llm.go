package extraction

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/carsteneu/yesmem/internal/models"
)

// LLMClient abstracts LLM calls across HTTP and CLI backends.
type LLMClient interface {
	Complete(system, userMsg string, opts ...CallOption) (string, error)
	CompleteJSON(system, userMsg string, schema map[string]any, opts ...CallOption) (string, error)
	Name() string  // backend/provider name for logging
	Model() string // full model ID
}

// CallOption configures a single LLM call.
type CallOption func(*callOpts)

type callOpts struct {
	maxTokens int    // 0 = use default (8192)
	sessionID string // opencode session to resume
}

// WithMaxTokens sets the max output tokens for this call.
func WithMaxTokens(n int) CallOption {
	return func(o *callOpts) { o.maxTokens = n }
}

// WithSession sets the session ID to resume (opencode CLI).
func WithSession(id string) CallOption {
	return func(o *callOpts) { o.sessionID = id }
}

func applyOpts(opts []CallOption) callOpts {
	o := callOpts{maxTokens: 8192}
	for _, fn := range opts {
		fn(&o)
	}
	return o
}

// NewLLMClient creates the appropriate client based on config.
// "api" stays the Anthropic HTTP alias for backward compatibility.
func NewLLMClient(provider, apiKey, model, claudeBinary, baseURL string) (LLMClient, error) {
	switch provider {
	case "api":
		if apiKey == "" {
			return nil, fmt.Errorf("llm.provider=api but no API key available")
		}
		return NewClient(apiKey, model), nil

	case "openai":
		if apiKey == "" {
			return nil, fmt.Errorf("llm.provider=openai but no API key available")
		}
		return NewOpenAIClient(apiKey, model, "", "openai"), nil

	case "openai_compatible":
		if apiKey == "" {
			return nil, fmt.Errorf("llm.provider=openai_compatible but no API key available")
		}
		return NewOpenAIClient(apiKey, model, baseURL, "openai_compatible"), nil

	case "cli":
		bin := resolveClaudeBinary(claudeBinary)
		if bin == "" {
			return nil, fmt.Errorf("llm.provider=cli but claude binary not found in PATH")
		}
		return NewCLIClient(bin, model, models.SourceAgentClaude), nil

	case "opencode":
		bin := resolveBinary("opencode")
		if bin == "" {
			return nil, fmt.Errorf("llm.provider=opencode but opencode binary not found in PATH")
		}
		return NewCLIClient(bin, model, models.SourceAgentOpencode), nil

	case "codex":
		bin := resolveBinary("codex")
		if bin == "" {
			return nil, fmt.Errorf("llm.provider=codex but codex binary not found in PATH")
		}
		return NewCLIClient(bin, model, models.SourceAgentCodex), nil

	case "auto", "":
		// API key available → use API
		if apiKey != "" {
			return NewClient(apiKey, model), nil
		}
		// Try CLI fallback
		bin := resolveClaudeBinary(claudeBinary)
		if bin != "" {
			return NewCLIClient(bin, model, models.SourceAgentClaude), nil
		}
		// Neither available
		return nil, nil

	default:
		return nil, fmt.Errorf("unknown llm.provider: %q (valid: auto, api, cli, opencode, codex, openai, openai_compatible)", provider)
	}
}

// resolveClaudeBinary finds the claude binary path.
func resolveClaudeBinary(configured string) string {
	if configured != "" {
		if _, err := exec.LookPath(configured); err == nil {
			return configured
		}
		return ""
	}
	return resolveBinary("claude")
}

func resolveBinary(name string) string {
	if path, err := exec.LookPath(name); err == nil {
		return path
	}
	home, _ := os.UserHomeDir()
	fallbacks := map[string]string{
		"opencode": filepath.Join(home, ".opencode", "bin", "opencode"),
	}
	if fb, ok := fallbacks[name]; ok {
		if _, err := os.Stat(fb); err == nil {
			return fb
		}
	}
	return ""
}
