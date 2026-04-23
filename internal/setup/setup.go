package setup

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/carsteneu/yesmem/skills"
	"time"

	"github.com/carsteneu/yesmem/internal/orchestrator"

	"github.com/carsteneu/yesmem/internal/buildinfo"
	"github.com/carsteneu/yesmem/internal/briefing"
	"github.com/carsteneu/yesmem/internal/config"
	"github.com/carsteneu/yesmem/internal/daemon"
	"github.com/carsteneu/yesmem/internal/extraction"
)

// Run executes the install. Non-interactive by default (uses sensible defaults).
// Pass interactive=true for the classic wizard with model/provider prompts.
func Run(interactive bool) error {
	home, _ := os.UserHomeDir()
	binaryPath, _ := os.Executable()
	dataDir := filepath.Join(home, ".claude", "yesmem")

	fmt.Println()
	fmt.Println("  YesMem Install", buildinfo.Version)
	fmt.Println("  ====================")
	fmt.Println("  Long-term memory for Claude Code")
	fmt.Println()

	if interactive {
		return runInteractive(home, dataDir, binaryPath)
	}
	return runDefaults(home, dataDir, binaryPath)
}

func runDefaults(home, dataDir, binaryPath string) error {
	chosenModel := DefaultExtractionModel
	autoExtract := true
	autoStart := runtime.GOOS == "linux" || runtime.GOOS == "darwin"
	chosenTerminal := orchestrator.DetectTerminal()

	envKey := os.Getenv("ANTHROPIC_API_KEY")
	userType := promptUserType(detectUserTypeDefault(home, envKey))
	fmt.Println()

	var provider, apiKey string
	switch userType {
	case "api":
		provider = "api"
		if envKey == "" {
			if s, err := readSettingsJSON(home); err == nil {
				if env, ok := s["env"].(map[string]any); ok {
					if k, ok := env["ANTHROPIC_API_KEY"].(string); ok && k != "" {
						envKey = k
					}
				}
			}
		}
		apiKey = promptAPIKey(envKey)
		if apiKey == "" {
			fmt.Println("  → No API key provided, falling back to Claude Code subscription")
			provider = "cli"
			apiKey, _ = findClaudeCodeKey(home)
		}
	default:
		provider = "cli"
		apiKey, _ = findClaudeCodeKey(home)
	}

	fmt.Println()
	fmt.Println("  Installing:")
	fmt.Printf("    Model:    %s\n", chosenModel)
	fmt.Printf("    Provider: %s\n", provider)
	fmt.Printf("    Config:   %s/config.yaml\n", dataDir)
	fmt.Println()

	binaryPath = ensurePermanentLocation(home, binaryPath)
	return executeSetup(home, dataDir, binaryPath, chosenModel, apiKey, provider, chosenTerminal, autoExtract, autoStart)
}

func runInteractive(home, dataDir, binaryPath string) error {

	// Step 1: Choose extraction model
	fmt.Println("[1/3] LLM Model for knowledge extraction:")
	fmt.Println()

	projectsDir := filepath.Join(home, ".claude", "projects")
	costs, sessions, _ := daemon.EstimateMonthlyFromDir(projectsDir)

	tierLabels := []string{"haiku", "sonnet", "opus"}

	var options []string
	if len(costs) > 0 && sessions > 0 {
		fmt.Printf("  (estimated from %d sessions/month)\n\n", sessions)
		options = []string{
			fmt.Sprintf("%s  — fast, ~$%.0f/month", tierLabels[0], costs[0].CostUSD),
			fmt.Sprintf("%s — balanced, ~$%.0f/month", tierLabels[1], costs[1].CostUSD),
			fmt.Sprintf("%s   — best quality, ~$%.0f/month", tierLabels[2], costs[2].CostUSD),
		}
	} else {
		options = []string{
			tierLabels[0] + "  — fast",
			tierLabels[1] + " — balanced",
			tierLabels[2] + "   — best quality",
		}
	}
	modelIdx := promptChoice(options, 1)
	models := []string{"haiku", "sonnet", "opus"}
	chosenModel := models[modelIdx]
	fmt.Println()

	// Step 2: Access method (filtered by model)
	fmt.Println("[2/3] LLM Access:")
	fmt.Println()

	providerIdx := promptChoice([]string{
		"Claude Code CLI (recommended) — uses your subscription, no API key",
		"Anthropic API — requires key from platform.claude.com",
		"OpenAI-compatible API — any OpenAI-compatible endpoint",
	}, 0)
	fmt.Println()

	provider := "cli"
	apiKey := ""
	switch providerIdx {
	case 1:
		provider = "api"
		envKey := os.Getenv("ANTHROPIC_API_KEY")
		if envKey == "" {
			if s, err := readSettingsJSON(home); err == nil {
				if env, ok := s["env"].(map[string]any); ok {
					if k, ok := env["ANTHROPIC_API_KEY"].(string); ok && k != "" {
						envKey = k
					}
				}
			}
		}
		if envKey == "" {
			if k, _ := findClaudeCodeKey(home); k != "" {
				envKey = k
			}
		}
		apiKey = promptAPIKey(envKey)
		if apiKey == "" {
			fmt.Println("  → No API key, falling back to CLI")
			provider = "cli"
		}
	case 2:
		provider = "openai_compatible"
		envKey := os.Getenv("OPENAI_API_KEY")
		apiKey = promptAPIKeyWithLabel("OpenAI-compatible", envKey)
		if apiKey == "" {
			fmt.Println("  → No API key, falling back to CLI")
			provider = "cli"
		} else {
			baseURL := promptBaseURL()
			if baseURL != "" {
				os.Setenv("OPENAI_BASE_URL", baseURL)
			}
		}
	default:
		ccKey, _ := findClaudeCodeKey(home)
		if ccKey != "" {
			apiKey = ccKey
		}
	}

	// Remap model labels for OpenAI providers
	if config.IsOpenAIProvider(provider) {
		tierLabels = []string{"gpt-5-mini", "gpt-5.2", "gpt-5.4"}
		fmt.Printf("  ℹ Using %s (OpenAI equivalent of %s)\n", tierLabels[modelIdx], chosenModel)
	}

	fmt.Println()

	// Step 3: Confirm
	fmt.Println("[3/3] Install now?")
	fmt.Println()
	if !promptYesNo("Proceed?", true) {
		fmt.Println("  Setup cancelled.")
		return nil
	}
	fmt.Println()

	// Auto-detect settings
	autoExtract := true
	autoStart := runtime.GOOS == "linux" || runtime.GOOS == "darwin"
	chosenTerminal := orchestrator.DetectTerminal()

	// Binary copy (before executeSetup so binaryPath is correct)
	binaryPath = ensurePermanentLocation(home, binaryPath)

	return executeSetup(home, dataDir, binaryPath, chosenModel, apiKey, provider, chosenTerminal, autoExtract, autoStart)
}

func executeSetup(home, dataDir, binaryPath, model, apiKey, provider, terminal string, autoExtract, autoStart bool) error {
	hookDir := filepath.Join(home, ".claude", "hooks")
	lang := detectSystemLanguage()

	fmt.Println("  Installing...")
	fmt.Println()

	// 1. Create directories
	withSpinner("Creating directories", func() (string, error) {
		for _, d := range []string{dataDir, filepath.Join(dataDir, "archive"), hookDir} {
			os.MkdirAll(d, 0755)
		}
		return "", nil
	})

	// 2. Write config.yaml
	withSpinner("Writing config.yaml", func() (string, error) {
		cfgContent := generateConfig(model, autoExtract, apiKey, provider, terminal)
		return model, os.WriteFile(filepath.Join(dataDir, "config.yaml"), []byte(cfgContent), 0644)
	})

	// 3. Translate UI strings (best-effort, 10s timeout)
	if lang != "en" && apiKey != "" {
		withSpinner(fmt.Sprintf("Translating UI to %s", lang), func() (string, error) {
			done := make(chan error, 1)
			go func() { done <- translateStrings(dataDir, lang, apiKey, model, provider) }()
			select {
			case err := <-done:
				if err != nil {
					return "using English", nil
				}
				return "", nil
			case <-time.After(10 * time.Second):
				return "using English", nil
			}
		})
	}

	// 4. Update settings.json
	withSpinner("Updating settings.json", func() (string, error) {
		settings, err := readSettingsJSON(home)
		if err != nil {
			return "", fmt.Errorf("read: %w", err)
		}
		savePreInstallState(dataDir, settings, home)
		setCleanupPeriod(settings)
		disableAutoCompact(settings)
		setProxyEnvVar(settings)
		registerStatusLine(settings, binaryPath)
		registerMCPInSettings(settings, binaryPath)
		registerMCPPermissions(settings)
		registerHooks(settings, binaryPath)
		if apiKey != "" && provider == "api" {
			env, ok := settings["env"].(map[string]any)
			if !ok {
				env = map[string]any{}
			}
			env["ANTHROPIC_API_KEY"] = apiKey
			settings["env"] = env
		}
		return "", writeSettingsJSON(home, settings)
	})

	// 6. Register in ~/.claude.json + set primaryApiKey
	withSpinner("Registering MCP in claude.json", func() (string, error) {
		if err := registerMCPInClaudeJSON(home, binaryPath); err != nil {
			return "", err
		}
		if apiKey != "" && provider == "api" {
			return "", clearClaudeJSONAuth(home)
		}
		return "", nil
	})

	// 7. Install bundled commands
	withSpinner("Installing commands", func() (string, error) {
		installed, err := installBundledCommands(home, lang, apiKey, model, provider)
		if err != nil {
			return "", err
		}
		if installed > 0 {
			return fmt.Sprintf("%d commands", installed), nil
		}
		return "up to date", nil
	})

	// 7b. Install bundled skills
	withSpinner("Installing skills", func() (string, error) {
		installed, err := installBundledSkills(home)
		if err != nil {
			return "", err
		}
		if installed > 0 {
			return fmt.Sprintf("%d skills", installed), nil
		}
		return "up to date", nil
	})

	// 8. Write .mcp.json
	withSpinner("Writing .mcp.json", func() (string, error) {
		return "", mergeMcpJSON(filepath.Join(home, ".mcp.json"), binaryPath)
	})

	// 9b. Install codebase-memory-mcp CLI for code intelligence
	withSpinner("Installing codebase-memory-mcp", func() (string, error) {
		return ensureCBMBinary(dataDir)
	})

	// 10. Auto-start service
	if autoStart {
		withSpinner("Setting up auto-start", func() (string, error) {
			if runtime.GOOS == "linux" {
				return "systemd", setupSystemd(home, binaryPath)
			} else if runtime.GOOS == "darwin" {
				return "launchd", setupLaunchd(home, binaryPath)
			}
			return "", nil
		})
	}

	// Shell profile env var
	ensureProxyEnvVar(home)

	// 11. Start daemon + proxy
	withSpinner("Starting daemon", func() (string, error) {
		if autoStart && runtime.GOOS == "linux" {
			if err := exec.Command("systemctl", "--user", "start", "yesmem").Run(); err != nil {
				startDaemonDirect(binaryPath)
				return "direct", nil
			}
			return "systemd", nil
		} else if autoStart && runtime.GOOS == "darwin" {
			plist := filepath.Join(home, "Library", "LaunchAgents", "com.yesmem.daemon.plist")
			if err := exec.Command("launchctl", "load", plist).Run(); err != nil {
				startDaemonDirect(binaryPath)
				return "direct", nil
			}
			return "launchd", nil
		}
		startDaemonDirect(binaryPath)
		return "direct", nil
	})

	withSpinner("Starting proxy", func() (string, error) {
		if autoStart && runtime.GOOS == "linux" {
			if err := exec.Command("systemctl", "--user", "start", "yesmem-proxy").Run(); err != nil {
				startProxyDirect(binaryPath)
				return "direct", nil
			}
			return "systemd", nil
		} else if autoStart && runtime.GOOS == "darwin" {
			proxyPlist := filepath.Join(home, "Library", "LaunchAgents", "com.yesmem.proxy.plist")
			if err := exec.Command("launchctl", "load", proxyPlist).Run(); err != nil {
				startProxyDirect(binaryPath)
				return "direct", nil
			}
			return "launchd", nil
		}
		startProxyDirect(binaryPath)
		return "direct", nil
	})

	// 12. Watch import progress
	fmt.Println()
	imported := watchImportProgress(dataDir, 10*time.Minute)

	// 13. LLM health check
	if imported > 0 {
		fmt.Println()
		fmt.Printf("  ✓ %d sessions imported (search ready)\n", imported)
		withSpinner("Verifying LLM connection", func() (string, error) {
			return "", verifyLLMConnection(apiKey, model, provider)
		})
	}

	fmt.Println()
	fmt.Println("  ══════════════════════════════════════")
	fmt.Println("  Setup complete! Daemon is running.")
	fmt.Println()
	fmt.Println("  Next: Open a new Claude Code session.")
	fmt.Println("  YesMem tools are automatically available.")
	fmt.Println()
	fmt.Printf("  Config:  %s/config.yaml\n", dataDir)
	fmt.Printf("  Logs:    %s/logs/\n", dataDir)

	// Check if yesmem is reachable in current PATH
	if _, err := exec.LookPath("yesmem"); err != nil {
		fmt.Println()
		fmt.Println("  ┌─────────────────────────────────────────────┐")
		fmt.Println("  │  To use 'yesmem' in this terminal, run:     │")
		fmt.Println("  │                                             │")
		fmt.Println("  │    source ~/.bashrc                         │")
		fmt.Println("  │                                             │")
		fmt.Println("  │  (or just open a new terminal)              │")
		fmt.Println("  └─────────────────────────────────────────────┘")
	}

	fmt.Println("  ══════════════════════════════════════")
	fmt.Println()

	return nil
}

// mergeMcpJSON reads existing .mcp.json, adds/updates the yesmem entry,
// and writes back. If the file doesn't exist or is corrupt, creates from scratch.
func mergeMcpJSON(path string, binaryPath string) error {
	var cfg map[string]any

	data, err := os.ReadFile(path)
	if err == nil {
		if jsonErr := json.Unmarshal(data, &cfg); jsonErr != nil {
			cfg = nil
		}
	}
	if cfg == nil {
		cfg = map[string]any{}
	}

	servers, ok := cfg["mcpServers"].(map[string]any)
	if !ok {
		servers = map[string]any{}
	}
	servers["yesmem"] = map[string]any{
		"command": binaryPath,
		"args":    []string{"mcp"},
	}
	cfg["mcpServers"] = servers

	out, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(out, '\n'), 0644)
}

func startDaemonDirect(binaryPath string) {
	daemonCmd := exec.Command(binaryPath, "daemon", "--replace")
	daemonCmd.Stdout = nil
	daemonCmd.Stderr = nil
	if err := daemonCmd.Start(); err != nil {
		fmt.Printf("⚠ %v\n", err)
		fmt.Printf("  Start manually: %s daemon\n", binaryPath)
	} else {
		daemonCmd.Process.Release()
		fmt.Println("✓ (running in background, restart manually after reboot)")
	}
}

func startProxyDirect(binaryPath string) {
	proxyCmd := exec.Command(binaryPath, "proxy")
	proxyCmd.Stdout = nil
	proxyCmd.Stderr = nil
	if err := proxyCmd.Start(); err != nil {
		fmt.Printf("⚠ %v\n", err)
		fmt.Printf("  Start manually: %s proxy\n", binaryPath)
	} else {
		proxyCmd.Process.Release()
		fmt.Println("✓ (running in background, restart manually after reboot)")
	}
}

func generateConfig(model string, autoExtract bool, apiKey, provider, terminal string) string {
	lang := detectSystemLanguage()
	langs := fmt.Sprintf("[%s, en]", lang)
	if lang == "en" {
		langs = "[en]"
	}

	keyLine := "${ANTHROPIC_API_KEY}"
	if apiKey != "" {
		keyLine = apiKey
	}

	apiBlock := "api_key: " + keyLine
	if provider == "openai" || provider == "openai_compatible" {
		openaiKey := apiKey
		if openaiKey == "" {
			openaiKey = "${OPENAI_API_KEY}"
		}
		apiBlock = "api_key: " + keyLine + "\n  openai_api_key: " + openaiKey + "\n  openai_base_url: ${OPENAI_BASE_URL}"
	}

	return fmt.Sprintf(`# ============================================================================
# YesMem Configuration
# Long-term memory system for Claude Code
# ============================================================================

# --- Extraction Pipeline ---
# Sessions are analyzed in multiple passes to extract knowledge.
# Pass 1: Summarization (summarize_model) — compresses session chunks into summaries
# Pass 2: Extraction (model) — extracts structured learnings from summaries
# Quality: Refinement (quality_model) — deduplicates, rates, resolves contradictions
# Narrative: Session handovers, persona, profiles (narrative_model)
extraction:

  # Pass 1 — Summarize model. Compresses raw session chunks into focused summaries.
  # These summaries become the input for Pass 2 extraction.
  # Haiku is ideal: summarization is a compression task, not creative.
  # Shortnames: "haiku", "sonnet", "opus" — or full Anthropic model ID
  summarize_model: haiku

  # Pass 2 — Extraction model. Reads summaries and extracts structured learnings.
  # This is the model you chose during setup.
  # Shortnames: "haiku", "sonnet", "opus" — or full Anthropic model ID
  # Haiku (~$6/mo)  = finds obvious things, cheapest
  # Sonnet (~$21/mo) = understands context, good quality/cost balance
  # Opus (~$104/mo)  = detects nuance, irony, implicit knowledge
  model: %s

  # Narrative generation — session handovers, project profiles, persona traits.
  # Needs strong language understanding for coherent narrative text.
  narrative_model: opus

  # Quality refinement — deduplicates, rates relevance, resolves contradictions.
  # Also used for Persona synthesis. Falls back to narrative_model if not set.
  quality_model: sonnet

  # How much session content is sent to the LLM:
  # "full"        = everything (best quality, higher cost)
  # "prefiltered" = rule-based pre-filter (saves tokens, may miss things)
  mode: full

  # Sessions larger than chunk_size tokens are split into parts
  # and processed sequentially. Larger chunks = better context, more cost.
  chunk_size: 25000

  # Auto-extract learnings when a new session ends?
  # If false: manual extraction via "yesmem extract" required.
  auto_extract: %v

  # Only extract sessions from the last N days. 0 = all sessions.
  # Useful for initial setup to avoid processing years of history.
  # 30 days ≈ $15-30 vs potentially $500+ for all.
  max_age_days: 0

  # Max sessions to extract per daemon run. Prevents long-running extraction
  # jobs from blocking the daemon. 0 = unlimited.
  max_per_run: 30

  # Skip sessions younger than N hours. Active sessions are handled by the
  # forked agent proxy (real-time extraction). Default: 24.
  min_session_age_hours: 24

# --- LLM Backend & Budgets ---
llm:
  # LLM backend for extraction, evolution, and narrative generation.
  # "auto" = if API key is available → use HTTP API, otherwise → use Claude CLI (default)
  # "api"  = Anthropic HTTP API only (requires API key, see api section below)
  # "cli"  = Claude CLI only (uses your Pro/Max/Team subscription quota, no API key needed)
  #
  # How to choose:
  # - You have an API key → use "auto" or "api"
  # - You have a Pro/Max/Team plan without API key → set provider: "cli"
  #   The CLI provider calls the "claude" binary directly, so no API key is required.
  #   Make sure "claude" is in your PATH, or set claude_binary below.
  provider: %s

  # Path to claude binary (only needed for provider: "cli" if not in PATH)
  # claude_binary: /usr/local/bin/claude

  # Daily spending limits per model tier (USD). Prevents cost explosion
  # with many parallel sessions or large backlogs. 0 = no limit.
  # Only applies to provider: "api" — CLI usage is covered by your subscription.
  daily_budget_extract_usd: 5.0    # Budget for Pass 1 (extraction) + evolution
  daily_budget_quality_usd: 2.0    # Budget for narrative + Pass 2 (quality)

  # Max cost per single LLM call (USD). Safety net against runaway prompts.
  # max_budget_per_call_usd: 0.50

# --- Evolution ---
# Automatic evolution of knowledge over time.
evolution:

  # Auto-detect contradictions between learnings and resolve them?
  # e.g. older learning says "Feature X is broken", newer says "Feature X was fixed"
  # → older gets marked as superseded.
  auto_resolve: true

  # How long "unfinished" learnings (open tasks, TODOs) stay active (days).
  # After expiry they are archived. 0 = stay active forever.
  unfinished_ttl_days: 30

# --- Briefing ---
# The briefing is generated at each session start and injected as system context.
# It gives Claude access to relevant knowledge from past sessions.
briefing:

  # How many recent sessions appear with full details (messages, summary).
  # Older sessions appear as one-liners only.
  detailed_sessions: 3

  # Time window for the "other projects" section (days).
  # Shows activity in other projects so Claude can make cross-connections.
  # 90 days survives longer vacations without losing context.
  other_projects_days: 90

  # Max briefing size in tokens. Safety limit so the briefing doesn't
  # eat up all available context window space for the actual work.
  max_tokens: 5000

  # Similarity threshold for deduplicating learnings (0.0 - 1.0).
  # Lower = more aggressive dedup (more gets recognized as duplicate).
  # Higher = more conservative (only near-identical learnings are merged).
  dedup_threshold: 0.4

  # Max learnings per category (gotcha, decision, pattern, etc.)
  # shown in the briefing. Rest is available via get_learnings().
  max_per_category: 5

  # Languages for stop-word filtering in dedup (ISO 639-1 codes).
  # Stop words ("the", "a", "der", "die", "das") are ignored when comparing.
  languages: %s

  # Include open/unfinished tasks in the briefing?
  # remind_open_work: true

  # Include synthesized user profile in the briefing.
  # The profile summarizes the user's role, expertise, and communication style.
  user_profile: true

# --- Auto-Update ---
# Automatically check for and install new YesMem versions from GitHub Releases.
update:

  # Enable automatic updates. When true, the daemon periodically checks
  # for new releases and installs them automatically.
  auto_update: true

  # How often to check for updates (Go duration: "6h", "12h", "24h").
  check_interval: "6h"

  # Release channel. Currently only "stable" is supported.
  channel: "stable"

# --- Proxy (Infinite Thread) ---
# The proxy sits between Claude Code and the Anthropic API.
# It compresses old messages when the context window fills up,
# allowing conversations to grow indefinitely without context loss.
proxy:
  enabled: true

  # Port the proxy listens on. Claude Code connects here instead of
  # directly to the Anthropic API (via ANTHROPIC_BASE_URL=http://localhost:9099).
  listen: ":9099"

  # Upstream API — where the proxy forwards requests to.
  target: "https://api.anthropic.com"

  # Upstream API for OpenAI-format requests (used when provider is openai/openai_compatible).
  # openai_target: "https://api.openai.com"

  # Compress when conversation context exceeds this token count.
  # Lower = more frequent compression (saves cost, loses more detail).
  # Higher = less frequent compression (better context, higher cost).
  token_threshold: 250000

  # Model-specific thresholds override the global token_threshold.
  # Keys are matched as substrings against the model name in each request.
  # Example: "opus" matches "claude-opus-4-6", "gpt-5" matches "gpt-5.4-codex".
  token_thresholds:
    opus: 180000
    sonnet: 180000
    haiku: 130000
    gpt-5: 180000
    codex: 180000

  # Stub down to this floor token count during compaction.
  # Lower = more aggressive compression per cycle.
  token_minimum_threshold: 100000

  # How many recent messages stay uncompressed during compaction.
  # These are never compressed so the current working context is preserved.
  keep_recent: 10

  # Sawtooth caching keeps a frozen cached prefix between stub-cycles.
  # Disable only for debugging or if upstream cache semantics change.
  sawtooth_enabled: true

  # Cache TTL for all injected cache_control blocks.
  # "ephemeral" = 5 minutes, cheapest and safest
  # "1h"        = 1 hour, survives pauses but costs more on cache writes
  #
  # Important: all cache_control blocks in a request are normalized to this TTL.
  cache_ttl: "ephemeral"

  # Cache keepalive prevents prompt cache expiry during idle periods.
  # Sends periodic no-op API calls to maintain the cached prefix lifespan.
  cache_keepalive_enabled: true

  # Keepalive mode: "auto" (detect from API response), "5m", "1h".
  # auto: checks ephemeral_1h_input_tokens in response to determine TTL.
  # 5m/1h: fixed mode, no detection, pings at corresponding interval.
  cache_keepalive_mode: "auto"

  # Pings per idle phase when cache TTL is 5 minutes.
  cache_keepalive_pings_5m: 6

  # Pings per idle phase when cache TTL is 1 hour.
  cache_keepalive_pings_1h: 1

  # Scale down input_tokens reported to Claude Code to suppress "Context low" warning.
  # Claude Code has a hardcoded 180k token budget and warns at ~160k (89%%).
  # Since the proxy manages context compression, CC's warning is misleading.
  # 0 = disabled (report real tokens), 0.7 = report 70%% of actual tokens.
  # At 0.7, CC sees ~112k when real usage is 160k — well below the warning threshold.
  usage_deflation_factor: 0.7

  # Strip the disclaimer that Claude Code adds to CLAUDE.md content:
  # "IMPORTANT: this context may or may not be relevant to your tasks"
  # This disclaimer subordinates CLAUDE.md/MEMORY.md instructions, making the model
  # treat them as optional. Stripping it gives user instructions full authority.
  prompt_ungate: true

  # System prompt rewriting (based on Claude Code source analysis):
  # prompt_rewrite: strips output-throttling directives ("Output efficiency", "short and concise")
  #   and injects quality directives that Anthropic uses internally but withholds from external users
  #   (verification before completion, false claims mitigation, collaborator mode, explanations).
  prompt_rewrite: false

  # Prompt enhancements (YesMem-specific):
  # prompt_enhance: reinforces CLAUDE.md/MEMORY.md authority, adds comment discipline guidance,
  #   and injects persona-based tone preferences (verbose/concise) from the persona system.
  prompt_enhance: false

# --- Forked Agents (Background Learning Extraction) ---
# Spawns async API calls after each assistant response to extract learnings,
# evaluate injected memories, and detect contradictions — without blocking
# the main conversation.
forked_agents:
  enabled: true

  # Model for fork calls. Empty = same model as the main thread (recommended).
  # Override with: haiku, sonnet, opus (resolved via LLM provider).
  model: ""

  # Minimum tokens in conversation before first fork fires.
  # Lower = more frequent extraction, higher cost.
  token_growth_trigger: 20000

  # Maximum forks per session. 0 = unlimited.
  max_forks_per_session: 0

  # Maximum USD cost per session for fork calls. 0 = unlimited.
  max_cost_per_session: 0.0

  # Detailed fork logging in proxy.log (gate decisions, extracted learnings, evaluations).
  debug: false

# --- Embedding (Semantic Search) ---
# Vector embeddings enable semantic search over learnings.
# "Find similar concepts" instead of just exact text search.
embedding:
  # "static" = built-in multilingual static embeddings (default, fast, no dependencies)
  # "none"   = disable vector search
  provider: sse
  search:
    # "" = auto (brute_force under threshold, IVF above)
    # "brute_force" = always brute-force cosine scan
    # "ivf" = always IVF index (even under threshold)
    method: ""
    ivf_threshold: 5000
    ivf:
      # k: 0 = auto (sqrt(n))
      nprobe: 15

# --- API ---
api:
  # Anthropic API key for extraction, evolution and narrative generation.
  # Lookup order (first match wins):
  #   1. ANTHROPIC_API_KEY environment variable
  #   2. This config field (api_key below)
  #   3. Auto-detected from Claude Code's config (~/.claude/config.json → primaryApiKey)
  #
  # If you don't have an API key (e.g. Pro/Max/Team plan), leave this empty
  # and set llm.provider to "cli" — that uses the Claude CLI and your
  # subscription quota instead of direct API calls.
  %s

# --- Model Pricing ---
# Per-million-token pricing for budget tracking.
# Override here when prices change — no rebuild needed.
# Keys are matched by substring (e.g. "sonnet" matches "claude-sonnet-4-6").
pricing:
  haiku:      { input: 1.0, output: 5.0 }
  sonnet:     { input: 3.0, output: 15.0 }
  opus:       { input: 5.0, output: 25.0 }
  gpt-5-mini: { input: 0.25, output: 2.0 }
  gpt-5.2:    { input: 1.75, output: 14.0 }
  gpt-5.4:    { input: 2.5, output: 15.0 }

# --- Cognitive Signals ---
# The signal bus captures Claude's self-reflection during conversations.
# Signal tools (_signal_learning_used, _signal_knowledge_gap, etc.) are injected
# into the tool list and processed asynchronously after each response.
signals:

  # Enable cognitive signal processing. When disabled, signal tools are not
  # injected and any signal tool calls from Claude are silently ignored.
  enabled: true

  # Signal processing mode:
  # "reflection" = separate async API call to process signals (default)
  mode: reflection

  # Model for reflection calls. Haiku is sufficient (classification task).
  # Options: haiku (default), sonnet, opus, or full model ID.
  model: haiku

  # How often to trigger signal reflection. 1 = every response, 3 = every 3rd.
  # Higher values reduce cost but miss more signals.
  every_n_turns: 1

# --- ClaudeMD (Operative Reference) ---
# Auto-generates a per-project operative reference file (yesmem-ops.md) that gets
# included in CLAUDE.md. Contains architecture decisions, known pitfalls, patterns
# and deployment notes distilled from session history.
claudemd:

  # Enable auto-generation of operative reference files.
  enabled: true

  # Max learnings per category included in the operative reference.
  # Categories with more learnings are trimmed to the most relevant ones.
  max_per_category:
    gotcha: 15
    pattern: 10
    decision: 10
    explicit_teaching: 5
    pivot_moment: 5

  # How often to regenerate the operative reference.
  # Supports Go duration strings: "2h", "30m", "24h", etc.
  refresh_interval: "2h"

  # Minimum number of sessions a project needs before generating a reference.
  # Prevents creating sparse/useless files for one-off projects.
  min_sessions: 3

  # Output filename (placed in project's .claude/ directory).
  output_file: "yesmem-ops.md"

  # Model for operative reference generation. Empty = use narrative_model.
  # model: ""

# --- Paths (optional) ---
# All paths have sensible defaults under ~/.claude/yesmem/.
# Only set these if you need a custom location.
# paths:
#   db: ~/.claude/yesmem/yesmem.db
#   bleve_index: ~/.claude/yesmem/bleve-index
#   archive: ~/.claude/yesmem/archive
#   claude_projects: ~/.claude/projects

# --- Agents ---
# Controls how agent terminals are spawned.
agents:
  # Terminal emulator for agent windows.
  # Options: ghostty, kitty, gnome-terminal, alacritty, wezterm, xterm
  # Empty = auto-detect (uses x-terminal-emulator fallback)
  terminal: %s

  # Safety limits for spawned agents.
  # max_runtime: 30m       # Max wall-clock time per agent
  # max_turns: 50          # Max conversation turns per agent
  # max_depth: 3           # Max agent nesting depth (agents spawning agents)
  # token_budget: 0        # Max tokens (input+output) per agent. 0 = no limit.
`, model, autoExtract, provider, langs, apiBlock, terminal)
}

// detectSystemLanguage reads the system locale and returns ISO 639-1 code.
// Falls back to "en" if locale cannot be determined.
func detectSystemLanguage() string {
	// Try LANG, LANGUAGE, LC_ALL in order
	for _, env := range []string{"LANG", "LANGUAGE", "LC_ALL"} {
		val := os.Getenv(env)
		if val == "" || val == "C" || val == "POSIX" {
			continue
		}
		// Extract language code: "de_DE.UTF-8" → "de"
		code := strings.Split(val, "_")[0]
		code = strings.Split(code, ".")[0]
		code = strings.ToLower(code)
		if len(code) == 2 {
			return code
		}
	}
	return "en"
}

// testAPIKeyForSonnet makes a minimal API call to check if the key supports Sonnet.
func testAPIKeyForSonnet(apiKey string) bool {
	client := extraction.NewClient(apiKey, "claude-sonnet-4-6")
	_, err := client.Complete("Reply with OK.", "test")
	return err == nil
}

// findClaudeCodeKey reads the internal Claude Code key from ~/.claude.json.
// This key only supports Haiku-level API calls — NOT Sonnet/Opus.
// Used silently for cheap tasks (translate, discover) when provider is CLI.
func findClaudeCodeKey(home string) (string, string) {
	data, err := os.ReadFile(filepath.Join(home, ".claude.json"))
	if err != nil {
		return "", ""
	}
	var cfg map[string]any
	if json.Unmarshal(data, &cfg) != nil {
		return "", ""
	}
	if k, ok := cfg["primaryApiKey"].(string); ok {
		return k, "claude-code"
	}
	return "", ""
}

// translateStrings uses the configured LLM to translate UI strings.
func translateStrings(dataDir, langCode, apiKey, model, provider string) error {
	modelID := config.ResolveModelIDForProvider(provider, "haiku", "claude-haiku-4-5-20251001", "gpt-5-mini")

	client, err := extraction.NewLLMClient(provider, apiKey, modelID, "", "")
	if err != nil {
		return fmt.Errorf("llm client: %w", err)
	}

	prompt := briefing.BuildTranslationPrompt(langCode)

	response, err := client.Complete(
		"You are a translator. Return ONLY valid YAML, no explanation.",
		prompt,
	)
	if err != nil {
		return fmt.Errorf("api call: %w", err)
	}

	translated, err := briefing.ParseTranslationResponse(response)
	if err != nil {
		return fmt.Errorf("parse response: %w", err)
	}

	return briefing.SaveStrings(filepath.Join(dataDir, "strings.yaml"), translated)
}

func setupSystemd(home, binaryPath string) error {
	serviceDir := filepath.Join(home, ".config", "systemd", "user")
	os.MkdirAll(serviceDir, 0755)

	// Daemon unit
	daemonUnit := fmt.Sprintf(`[Unit]
Description=YesMem — Long-term memory for Claude Code
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=%s daemon --replace
Restart=always
RestartSec=10

[Install]
WantedBy=default.target
`, binaryPath)
	if err := os.WriteFile(filepath.Join(serviceDir, "yesmem.service"), []byte(daemonUnit), 0644); err != nil {
		return err
	}

	// Proxy unit — separate process, survives terminal closes
	proxyUnit := fmt.Sprintf(`[Unit]
Description=YesMem Proxy — Infinite-thread context for Claude Code
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=%s proxy
Restart=always
RestartSec=10

[Install]
WantedBy=default.target
`, binaryPath)
	if err := os.WriteFile(filepath.Join(serviceDir, "yesmem-proxy.service"), []byte(proxyUnit), 0644); err != nil {
		return err
	}

	exec.Command("systemctl", "--user", "daemon-reload").Run()
	exec.Command("systemctl", "--user", "enable", "yesmem").Run()
	exec.Command("systemctl", "--user", "enable", "yesmem-proxy").Run()
	return nil
}

func setupLaunchd(home, binaryPath string) error {
	plistDir := filepath.Join(home, "Library", "LaunchAgents")
	os.MkdirAll(plistDir, 0755)

	// Daemon plist
	daemonPlist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>com.yesmem.daemon</string>
	<key>ProgramArguments</key>
	<array>
		<string>%s</string>
		<string>daemon</string>
	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<true/>
</dict>
</plist>
`, binaryPath)
	if err := os.WriteFile(filepath.Join(plistDir, "com.yesmem.daemon.plist"), []byte(daemonPlist), 0644); err != nil {
		return err
	}

	// Proxy plist
	proxyPlist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>com.yesmem.proxy</string>
	<key>ProgramArguments</key>
	<array>
		<string>%s</string>
		<string>proxy</string>
	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<true/>
</dict>
</plist>
`, binaryPath)
	return os.WriteFile(filepath.Join(plistDir, "com.yesmem.proxy.plist"), []byte(proxyPlist), 0644)
}

// ensureProxyEnvVar adds ANTHROPIC_BASE_URL to shell profiles (Unix) or user env (Windows).
func ensureProxyEnvVar(home string) error {
	if runtime.GOOS == "windows" {
		// Check if already set
		if val := os.Getenv("ANTHROPIC_BASE_URL"); val == "http://localhost:9099" {
			return nil
		}
		cmd := exec.Command("setx", "ANTHROPIC_BASE_URL", "http://localhost:9099")
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("setx: %s: %w", strings.TrimSpace(string(out)), err)
		}
		return nil
	}

	envLine := "export ANTHROPIC_BASE_URL=http://localhost:9099"
	marker := "ANTHROPIC_BASE_URL"

	for _, rcFile := range []string{".bashrc", ".zshrc", ".profile"} {
		rcPath := filepath.Join(home, rcFile)
		data, err := os.ReadFile(rcPath)
		if err != nil {
			continue // file doesn't exist, skip
		}
		if strings.Contains(string(data), marker) {
			continue // already set
		}
		// Append
		f, err := os.OpenFile(rcPath, os.O_APPEND|os.O_WRONLY, 0644)
		if err != nil {
			return fmt.Errorf("append to %s: %w", rcFile, err)
		}
		fmt.Fprintf(f, "\n# YesMem infinite-thread proxy\n%s\n", envLine)
		f.Close()
	}
	return nil
}

// discoverSkills is deprecated — skill discovery has been removed.
// Skills are now activated via [skill-eval] injection in the proxy.
func discoverSkills(dataDir, apiKey, model string) int {
	return 0
}

// verifyLLMConnection makes a single minimal LLM call to verify the API key and provider work.
// Returns nil on success, error with diagnostic message on failure.
func verifyLLMConnection(apiKey, model, provider string) error {
	modelID := config.ResolveModelIDForProvider(provider, "haiku", "claude-haiku-4-5-20251001", "gpt-5-mini")
	client, err := extraction.NewLLMClient(provider, apiKey, modelID, "", "")
	if err != nil {
		return fmt.Errorf("could not create LLM client: %w", err)
	}
	_, err = client.Complete("Reply with OK.", "ping")
	if err != nil {
		return fmt.Errorf("LLM call failed: %w", err)
	}
	return nil
}

// installBundledCommands copies embedded skill files to ~/.claude/commands/.
// Uses SHA-256 hash to skip unchanged files. If lang != "de" and apiKey is set,
// translates content via LLM before installing. Returns number of installed/updated commands.
func installBundledCommands(home, lang, apiKey, model, provider string) (int, error) {
	cmdDir := filepath.Join(home, ".claude", "commands")
	os.MkdirAll(cmdDir, 0755)

	entries, err := skills.BundledCommands.ReadDir(".")
	if err != nil {
		return 0, fmt.Errorf("read bundled commands: %w", err)
	}

	installed := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		data, err := skills.BundledCommands.ReadFile(e.Name())
		if err != nil {
			continue
		}
		// Translate if non-German and API key available (best-effort, 10s timeout)
		if lang != "de" && lang != "" && apiKey != "" {
			done := make(chan string, 1)
			go func() {
				if translated, err := translateCommand(string(data), lang, apiKey, model, provider); err == nil {
					done <- translated
				} else {
					done <- ""
				}
			}()
			select {
			case result := <-done:
				if result != "" {
					data = []byte(result)
				}
			case <-time.After(10 * time.Second):
				// Skip translation, install original
			}
		}
		dst := filepath.Join(cmdDir, e.Name())
		newHash := fmt.Sprintf("%x", sha256.Sum256(data))
		if existing, err := os.ReadFile(dst); err == nil {
			oldHash := fmt.Sprintf("%x", sha256.Sum256(existing))
			if oldHash == newHash {
				continue
			}
		}
		if err := os.WriteFile(dst, data, 0644); err != nil {
			return installed, fmt.Errorf("write %s: %w", e.Name(), err)
		}
		installed++
	}
	return installed, nil
}

// installBundledSkills copies embedded skill directories to ~/.claude/skills/.
// Uses SHA-256 hash to skip unchanged files. Returns number of installed/updated files.
func installBundledSkills(home string) (int, error) {
	skillsDir := filepath.Join(home, ".claude", "skills")
	installed := 0

	entries, err := skills.BundledSkills.ReadDir("bundled-skills")
	if err != nil {
		return 0, fmt.Errorf("read bundled skills: %w", err)
	}

	for _, skillDir := range entries {
		if !skillDir.IsDir() {
			continue
		}
		dstDir := filepath.Join(skillsDir, skillDir.Name())
		os.MkdirAll(dstDir, 0755)

		files, err := skills.BundledSkills.ReadDir(filepath.Join("bundled-skills", skillDir.Name()))
		if err != nil {
			continue
		}
		for _, f := range files {
			if f.IsDir() {
				continue
			}
			data, err := skills.BundledSkills.ReadFile(filepath.Join("bundled-skills", skillDir.Name(), f.Name()))
			if err != nil {
				continue
			}
			dst := filepath.Join(dstDir, f.Name())
			newHash := fmt.Sprintf("%x", sha256.Sum256(data))
			if existing, err := os.ReadFile(dst); err == nil {
				oldHash := fmt.Sprintf("%x", sha256.Sum256(existing))
				if oldHash == newHash {
					continue
				}
			}
			if err := os.WriteFile(dst, data, 0644); err != nil {
				return installed, fmt.Errorf("write skill %s/%s: %w", skillDir.Name(), f.Name(), err)
			}
			installed++
		}
	}
	return installed, nil
}

// translateCommand translates a skill document to the target language via LLM.
func translateCommand(content, langCode, apiKey, model, provider string) (string, error) {
	modelID := config.ResolveModelIDForProvider(provider, "haiku", "claude-haiku-4-5-20251001", "gpt-5-mini")

	client, err := extraction.NewLLMClient(provider, apiKey, modelID, "", "")
	if err != nil {
		return "", fmt.Errorf("llm client: %w", err)
	}

	prompt := fmt.Sprintf(`Translate the following skill document to %s.

Rules:
- Keep ALL markdown formatting, code blocks, tables, and structure intact
- Keep technical terms (Scratchpad, send_to, spawn_agent, etc.) untranslated
- Keep code examples and function names untranslated
- Translate headings, descriptions, rules, and explanatory text
- Return ONLY the translated document, no explanation

Document:
%s`, langCode, content)

	response, err := client.Complete("You are a technical translator. Return ONLY the translated markdown document.", prompt)
	if err != nil {
		return "", fmt.Errorf("translate: %w", err)
	}
	return response, nil
}

const cbmRepo = "DeusData/codebase-memory-mcp"

// ensureCBMBinary downloads the codebase-memory-mcp CLI binary if not present.
// Downloads the latest release from GitHub for the current platform.
func ensureCBMBinary(dataDir string) (string, error) {
	cliDir := filepath.Join(dataDir, "cli")
	binPath := filepath.Join(cliDir, "codebase-memory-mcp")

	// Already installed — check version
	if _, err := os.Stat(binPath); err == nil {
		out, err := exec.Command(binPath, "--version").Output()
		if err == nil {
			return strings.TrimSpace(string(out)) + " (existing)", nil
		}
	}

	os.MkdirAll(cliDir, 0755)

	// Determine platform
	goos := runtime.GOOS
	goarch := runtime.GOARCH
	asset := fmt.Sprintf("codebase-memory-mcp-%s-%s.tar.gz", goos, goarch)

	// Download latest release asset via GitHub redirect
	url := fmt.Sprintf("https://github.com/%s/releases/latest/download/%s", cbmRepo, asset)
	client := &http.Client{Timeout: 300 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return "", fmt.Errorf("download CBM: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("download CBM: HTTP %d for %s", resp.StatusCode, asset)
	}

	// Write to temp file with progress display
	tmpTar := filepath.Join(os.TempDir(), asset)
	f, err := os.Create(tmpTar)
	if err != nil {
		return "", fmt.Errorf("create temp: %w", err)
	}
	spinChars := []rune{'⠋', '⠙', '⠹', '⠸', '⠼', '⠴', '⠦', '⠧', '⠇', '⠏'}
	downloaded := int64(0)
	stopProgress := make(chan struct{})
	go func() {
		tick := time.NewTicker(200 * time.Millisecond)
		defer tick.Stop()
		i := 0
		for {
			select {
			case <-stopProgress:
				return
			case <-tick.C:
				mb := float64(downloaded) / (1024 * 1024)
				fmt.Fprintf(os.Stderr, "\r\033[2K  %c Downloading codebase-memory-mcp (%.1f MB)...", spinChars[i%len(spinChars)], mb)
				i++
			}
		}
	}()
	buf := make([]byte, 32*1024)
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if _, writeErr := f.Write(buf[:n]); writeErr != nil {
				f.Close()
				close(stopProgress)
				fmt.Fprintf(os.Stderr, "\r\033[2K")
				return "", fmt.Errorf("write temp: %w", writeErr)
			}
			downloaded += int64(n)
		}
		if readErr != nil {
			if readErr == io.EOF {
				break
			}
			f.Close()
			close(stopProgress)
			fmt.Fprintf(os.Stderr, "\r\033[2K")
			return "", fmt.Errorf("write temp: %w", readErr)
		}
	}
	close(stopProgress)
	fmt.Fprintf(os.Stderr, "\r\033[2K")
	f.Close()

	// Extract with tar (single binary inside)
	out, err := exec.Command("tar", "xzf", tmpTar, "-C", cliDir).CombinedOutput()
	os.Remove(tmpTar)
	if err != nil {
		return "", fmt.Errorf("extract CBM: %s: %w", string(out), err)
	}

	os.Chmod(binPath, 0755)

	// Verify
	version, err := exec.Command(binPath, "--version").Output()
	if err != nil {
		return "", fmt.Errorf("verify CBM: %w", err)
	}

	return strings.TrimSpace(string(version)), nil
}
