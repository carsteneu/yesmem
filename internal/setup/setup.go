package setup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/carsteneu/yesmem/caps"
	"github.com/carsteneu/yesmem/skills"
	"gopkg.in/yaml.v3"

	"github.com/carsteneu/yesmem/internal/capfile"

	"github.com/carsteneu/yesmem/internal/orchestrator"

	"github.com/carsteneu/yesmem/internal/briefing"
	"github.com/carsteneu/yesmem/internal/buildinfo"
	"github.com/carsteneu/yesmem/internal/config"
	"github.com/carsteneu/yesmem/internal/daemon"
	"github.com/carsteneu/yesmem/internal/extraction"
	"github.com/carsteneu/yesmem/internal/sanitize"
	"github.com/carsteneu/yesmem/internal/textutil"
)

// Run executes the install. Non-interactive by default (uses sensible defaults).
// Pass interactive=true for the classic wizard with model/provider prompts.
func Run(interactive bool) error {
	home, err := UserHome()
	if err != nil {
		return err
	}
	binaryPath, _ := os.Executable()
	dataDir := filepath.Join(home, ".claude", "yesmem")

	fmt.Println()
	fmt.Println("  YesMem Install", buildinfo.Version)
	fmt.Println("  ====================")
	fmt.Println("  Long-term memory for coding agents")
	fmt.Println()

	if interactive {
		return runInteractive(home, dataDir, binaryPath)
	}
	return runDefaults(home, dataDir, binaryPath)
}

func runDefaults(home, dataDir, binaryPath string) error {
	// Step 1: Detect installed agents
	agents := detectAgents(home)

	fmt.Println("  Detecting agents...")
	hasOpenCode := false
	hasClaude := false
	hasCodex := false
	for _, a := range agents {
		fmt.Printf("    \u2192 \u2713 %s\n", a)
		switch a {
		case "opencode":
			hasOpenCode = true
		case "claude":
			hasClaude = true
		case "codex":
			hasCodex = true
		}
	}

	// Multi-Agent: if OpenCode AND Claude/Codex detected, let user choose
	if hasOpenCode && (hasClaude || hasCodex) {
		fmt.Println("  Multiple agents detected.")
		fmt.Println()
		options := []string{"OpenCode (choose model — free tier or paid provider)"}
		if hasClaude {
			options = append(options, "Claude Code (Sonnet/Opus via Anthropic)")
		}
		if hasCodex {
			options = append(options, "Codex (GPT-5.5)")
		}
		fmt.Println("  Which backend should YesMem use?")
		idx := promptChoice(options, 0)
		fmt.Println()
		switch idx {
		case 0:
			return runOpenCodeSetup(home, dataDir, binaryPath)
		case 1:
			if hasClaude {
				return runNonInteractiveClaude(home, dataDir, binaryPath)
			}
			return runNonInteractiveCodex(home, dataDir, binaryPath)
		case 2:
			return runNonInteractiveCodex(home, dataDir, binaryPath)
		}
	}

	// OpenCode only — interactive provider/model setup
	if hasOpenCode {
		fmt.Println("  OpenCode detected → interactive provider selection")
		return runOpenCodeSetup(home, dataDir, binaryPath)
	}

	// If Claude + Codex detected — non-interactive defaults using Anthropic via CLI
	if hasClaude && hasCodex {
		fmt.Println("  Claude + Codex \u2192 non-interactive defaults")
		return runNonInteractiveClaudeCodex(home, dataDir, binaryPath)
	}

	// If Codex only — non-interactive defaults with OpenAI-compatible
	if hasCodex {
		fmt.Println("  Codex only \u2192 non-interactive defaults")
		return runNonInteractiveCodex(home, dataDir, binaryPath)
	}

	// If Claude only — non-interactive defaults
	if hasClaude {
		fmt.Println("  Claude Code \u2192 non-interactive defaults")
		return runNonInteractiveClaude(home, dataDir, binaryPath)
	}

	// No agents found — fall back to original interactive wizard
	fmt.Println("  No agents detected \u2192 interactive setup")
	fmt.Println()
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
		if envKey == "" {
			if k, _ := findClaudeCodeKey(home); k != "" {
				envKey = k
			}
		}
		if envKey == "" {
			envKey = readExistingConfigKey(dataDir)
		}
		apiKey = promptAPIKey(envKey)
		if apiKey == "" {
			fmt.Println("  \u2192 No API key provided, falling back to Claude Code subscription")
			provider = "cli"
			apiKey, _ = findClaudeCodeKey(home)
		}
	case "opencode":
		provider = "openai_compatible"
		apiKey = readAuthJSONKey(home, "deepseek")
		if apiKey == "" {
			apiKey = os.Getenv("DEEPSEEK_API_KEY")
		}
		if apiKey == "" {
			apiKey = os.Getenv("OPENAI_API_KEY")
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
	// runDefaults "opencode" userType assumes DeepSeek (see providerKeys below).
	// Pass the DeepSeek baseURL so derivePrimaryAndSmallModel produces the two-tier pair.
	baseURLForDerive := ""
	if provider == "openai_compatible" {
		baseURLForDerive = "https://api.deepseek.com/v1"
	}
	primaryModel, smallModel := derivePrimaryAndSmallModel(provider, chosenModel, "", baseURLForDerive)
	providerKeys := map[string]string{}
	if provider == "openai_compatible" && apiKey != "" {
		providerKeys["deepseek"] = apiKey
	}
	return executeSetup(home, dataDir, binaryPath, chosenModel, apiKey, provider, chosenTerminal, autoExtract, autoStart, primaryModel, smallModel, providerKeys, apiKey, "", "", "", "")
}

func runOpenCodeSetup(home, dataDir, binaryPath string) error {
	baseURL, apiKey, providerID, extractionModel, narrativeModel, err := resolveOpenCodeProvider(home)
	if err != nil {
		return fmt.Errorf("resolve OpenCode provider: %w", err)
	}

	chosenModel := extractionModel
	if chosenModel == "" {
		chosenModel = DefaultExtractionModel
	}
	qualityModel := narrativeModel
	if qualityModel == "" {
		qualityModel = "sonnet"
	}
	// summarizeModel uses the lighter extraction model (not hardcoded haiku)
	// so DeepSeek users get deepseek-v4-flash, Anthropic users get haiku, etc.
	summarizeModel := chosenModel

	// Free-tier users (no apiKey) use the opencode binary via CLIClient,
	// which handles auth internally via OAuth/session — no HTTP API key needed.
	// Paid users use openai_compatible for direct HTTP calls.
	provider := "openai_compatible"
	if apiKey == "" {
		provider = "opencode"
	}

	autoExtract := true
	autoStart := runtime.GOOS == "linux" || runtime.GOOS == "darwin"
	chosenTerminal := orchestrator.DetectTerminal()

	fmt.Println()
	fmt.Println("  Installing:")
	fmt.Printf("    Extraction model:   %s\n", chosenModel)
	fmt.Printf("    Narrative model:    %s\n", narrativeModel)
	fmt.Printf("    LLM Provider:       %s\n", provider)
	if apiKey != "" {
		fmt.Printf("    API Base URL:       %s\n", baseURL)
	} else {
		fmt.Printf("    Auth:               opencode binary (free tier)\n")
	}
	fmt.Printf("    Config:             %s/config.yaml\n", dataDir)
	fmt.Println()

	binaryPath = ensurePermanentLocation(home, binaryPath)
	primaryModel, smallModel := derivePrimaryAndSmallModel(provider, chosenModel, providerID, baseURL)
	providerKeys := map[string]string{}
	if apiKey != "" && providerID != "" {
		providerKeys[providerID] = apiKey
	}
	return executeSetup(home, dataDir, binaryPath, chosenModel, apiKey, provider, chosenTerminal, autoExtract, autoStart, primaryModel, smallModel, providerKeys, apiKey, baseURL, narrativeModel, qualityModel, summarizeModel)
}

// runNonInteractiveClaudeCodex handles Claude + Codex detected: use Anthropic via CLI.
// Codex's default model is opus-4-7, used as narrative_model.
func runNonInteractiveClaudeCodex(home, dataDir, binaryPath string) error {
	apiKey, _ := findClaudeCodeKey(home)

	autoExtract := true
	autoStart := runtime.GOOS == "linux" || runtime.GOOS == "darwin"
	chosenTerminal := orchestrator.DetectTerminal()

	chosenModel := "sonnet"
	narrativeModel := "opus-4-7"
	qualityModel := "sonnet"
	summarizeModel := "haiku"

	fmt.Println()
	fmt.Println("  Installing:")
	fmt.Printf("    Extraction model:   %s\n", chosenModel)
	fmt.Printf("    Narrative model:    %s\n", narrativeModel)
	fmt.Printf("    LLM Provider:       cli\n")
	fmt.Printf("    Config:             %s/config.yaml\n", dataDir)
	fmt.Println()

	binaryPath = ensurePermanentLocation(home, binaryPath)
	primaryModel, smallModel := derivePrimaryAndSmallModel("cli", chosenModel, "", "")
	return executeSetup(home, dataDir, binaryPath, chosenModel, apiKey, "cli", chosenTerminal, autoExtract, autoStart, primaryModel, smallModel, nil, "", "", narrativeModel, qualityModel, summarizeModel)
}

// runNonInteractiveCodex handles Codex only: use OpenAI-compatible defaults.
func runNonInteractiveCodex(home, dataDir, binaryPath string) error {
	apiKey := readAuthJSONKey(home, "openai")
	if apiKey == "" {
		apiKey = os.Getenv("OPENAI_API_KEY")
	}

	autoExtract := true
	autoStart := runtime.GOOS == "linux" || runtime.GOOS == "darwin"
	chosenTerminal := orchestrator.DetectTerminal()

	chosenModel := "gpt-5.5-codex"
	narrativeModel := "gpt-5.5-codex"
	qualityModel := "gpt-5.5-codex"
	summarizeModel := "gpt-5.5-codex"
	baseURL := "https://api.openai.com/v1"

	fmt.Println()
	fmt.Println("  Installing:")
	fmt.Printf("    Extraction model:   %s\n", chosenModel)
	fmt.Printf("    LLM Provider:       openai_compatible\n")
	fmt.Printf("    API Base URL:       %s\n", baseURL)
	fmt.Printf("    Config:             %s/config.yaml\n", dataDir)
	fmt.Println()

	binaryPath = ensurePermanentLocation(home, binaryPath)
	primaryModel, smallModel := derivePrimaryAndSmallModel("openai_compatible", chosenModel, "openai", baseURL)
	return executeSetup(home, dataDir, binaryPath, chosenModel, apiKey, "openai_compatible", chosenTerminal, autoExtract, autoStart, primaryModel, smallModel, nil, apiKey, baseURL, narrativeModel, qualityModel, summarizeModel)
}

// runNonInteractiveClaude handles Claude only: use Anthropic via CLI.
func runNonInteractiveClaude(home, dataDir, binaryPath string) error {
	apiKey, _ := findClaudeCodeKey(home)

	autoExtract := true
	autoStart := runtime.GOOS == "linux" || runtime.GOOS == "darwin"
	chosenTerminal := orchestrator.DetectTerminal()

	chosenModel := "sonnet"
	narrativeModel := "opus"
	qualityModel := "sonnet"
	summarizeModel := "haiku"

	fmt.Println()
	fmt.Println("  Installing:")
	fmt.Printf("    Extraction model:   %s\n", chosenModel)
	fmt.Printf("    Narrative model:    %s\n", narrativeModel)
	fmt.Printf("    LLM Provider:       cli\n")
	fmt.Printf("    Config:             %s/config.yaml\n", dataDir)
	fmt.Println()

	binaryPath = ensurePermanentLocation(home, binaryPath)
	primaryModel, smallModel := derivePrimaryAndSmallModel("cli", chosenModel, "", "")
	return executeSetup(home, dataDir, binaryPath, chosenModel, apiKey, "cli", chosenTerminal, autoExtract, autoStart, primaryModel, smallModel, nil, "", "", narrativeModel, qualityModel, summarizeModel)
}

func runInteractive(home, dataDir, binaryPath string) error {

	// Step 1: Choose extraction model
	fmt.Println("[1/3] LLM Model for knowledge extraction:")
	fmt.Println()

	projectsDir := claudeProjectsDirFromConfig(dataDir)
	costs, sessions, _ := daemon.EstimateMonthlyFromDir(projectsDir)

	var options []string
	if len(costs) > 0 && sessions > 0 {
		fmt.Printf("  (estimated from %d sessions/month)\n\n", sessions)
		options = []string{
			fmt.Sprintf("Claude Haiku  — fast, ~$%.0f/month", costs[0].CostUSD),
			fmt.Sprintf("Claude Sonnet — balanced, ~$%.0f/month", costs[1].CostUSD),
			fmt.Sprintf("Claude Opus   — best quality, ~$%.0f/month", costs[2].CostUSD),
			"DeepSeek V4 Pro  — best via YesMem Proxy (opencode)",
			"DeepSeek V4 Flash — faster via YesMem Proxy (opencode)",
			"GPT-5 Mini       — cheapest via YesMem Proxy (opencode)",
			"GPT-5.2          — balanced via YesMem Proxy (opencode)",
		}
	} else {
		options = []string{
			"Claude Haiku  — fast",
			"Claude Sonnet — balanced",
			"Claude Opus   — best quality",
			"DeepSeek V4 Pro  — best via YesMem Proxy (opencode)",
			"DeepSeek V4 Flash — faster via YesMem Proxy (opencode)",
			"GPT-5 Mini       — cheapest via YesMem Proxy (opencode)",
			"GPT-5.2          — balanced via YesMem Proxy (opencode)",
		}
	}
	modelIdx := promptChoice(options, 1)
	type extractModel struct {
		model    string
		provider string
	}
	extractOpts := []extractModel{
		{"haiku", ""},
		{"sonnet", ""},
		{"opus", ""},
		{"deepseek-reasoner", "opencode"},
		{"deepseek-chat", "opencode"},
		{"gpt-5-mini", "opencode"},
		{"gpt-5.2", "opencode"},
	}
	chosen := extractOpts[modelIdx]
	chosenModel := chosen.model
	forceProvider := chosen.provider
	fmt.Println()

	// Step 2: Access method
	provider := forceProvider
	if provider == "" {
		provider = "cli"
	}
	// opencode CLI is not needed — the daemon can call the API directly
	// via openai_compatible. Default to that for DeepSeek/GPT models.
	if provider == "opencode" {
		provider = "openai_compatible"
	}
	apiKey := ""
	providerKeys := map[string]string{}
	fmt.Println("[2/3] LLM Access:")
	fmt.Println()

	providerChoices := []string{
		"Claude Code CLI (recommended) — uses your subscription, no API key",
		"Anthropic API — requires key from platform.claude.com",
		"OpenAI-compatible API — any OpenAI-compatible endpoint",
	}
	opencodeFound := false
	if _, err := exec.LookPath("opencode"); err == nil {
		opencodeFound = true
	}
	if !opencodeFound {
		if _, err := exec.LookPath("codex"); err == nil {
			opencodeFound = true
		}
	}
	if opencodeFound {
		providerChoices = append(providerChoices, "Opencode CLI — uses your local opencode/codex binary, no API key")
	}
	hasOpencode := opencodeFound
	providerIdx := promptChoice(providerChoices, 0)
	fmt.Println()

	opencodeIdx := 3 // index of opencode option when present

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
		if envKey == "" {
			envKey = readExistingConfigKey(dataDir)
		}
		apiKey = promptAPIKey(envKey)
		if apiKey == "" {
			fmt.Println("  → No API key, falling back to CLI")
			provider = "cli"
		}
	case 2:
		provider = "openai_compatible"
		envKey := os.Getenv("OPENAI_API_KEY")
		if envKey == "" {
			envKey = readAuthJSONKey(home, "deepseek")
		}
		if envKey == "" {
			envKey = os.Getenv("DEEPSEEK_API_KEY")
		}
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
		if hasOpencode && providerIdx == opencodeIdx {
			provider = "opencode"
		} else {
			ccKey, _ := findClaudeCodeKey(home)
			if ccKey != "" {
				apiKey = ccKey
			}
		}
	}

	fmt.Println()

	// Step 2b: Choose primary model for conversations
	fmt.Println("[2.5/3] Which model should OpenCode use for conversations?")
	fmt.Println()

	modelChoices := []string{
		"DeepSeek V4 Pro (via YesMem Proxy) — recommended, 1M context",
		"DeepSeek V4 Flash (via YesMem Proxy) — faster, lighter",
		"GPT-5.5 (via YesMem Proxy) — OpenAI-compatible",
	}
	type modelSel struct{ model, small string }
	modelOpts := []modelSel{
		{"deepseek/deepseek-reasoner", "deepseek/deepseek-chat"},
		{"deepseek/deepseek-chat", "deepseek/deepseek-chat"},
		{"openai/gpt-5.5", "openai/gpt-5.5"},
	}
	mIdx := promptChoice(modelChoices, 0)
	fmt.Println()

	primaryModel := modelOpts[mIdx].model
	smallModel := modelOpts[mIdx].small

	// Step 3: Confirm
	fmt.Println("[3/3] Install now?")
	fmt.Println()
	if !promptYesNo("Proceed?", true) {
		fmt.Println("  Setup cancelled.")
		return nil
	}

	// When using a non-Claude model (DeepSeek, GPT) via opencode,
	// prompt for the provider's API key so the proxy can route requests.
	if forceProvider == "opencode" || provider == "opencode" || provider == "openai_compatible" {
		providerID := modelToProviderID(chosenModel)
		if providerID != "" {
			fmt.Println()
			fmt.Printf("  The extraction model %q needs a %s API key.\n", chosenModel, providerID)
			fmt.Println()
			envVar := providerEnvVar(providerID)
			envKey := os.Getenv(envVar)
			key := promptAPIKeyWithLabel(strings.Title(providerID), envKey)
			if key != "" {
				providerKeys[providerID] = key
			} else {
				fmt.Printf("  ⚠ No %s API key provided. The proxy won't be able to route requests.\n", providerID)
				fmt.Println("    You can add it later in ~/.local/share/opencode/auth.json")
			}
		}
	}

	fmt.Println()
	// Auto-detect settings
	autoExtract := true
	autoStart := runtime.GOOS == "linux" || runtime.GOOS == "darwin"
	chosenTerminal := orchestrator.DetectTerminal()

	// Binary copy (before executeSetup so binaryPath is correct)
	binaryPath = ensurePermanentLocation(home, binaryPath)

	return executeSetup(home, dataDir, binaryPath, chosenModel, apiKey, provider, chosenTerminal, autoExtract, autoStart, primaryModel, smallModel, providerKeys, apiKey, "", "", "", "")
}

func executeSetup(home, dataDir, binaryPath, model, apiKey, provider, terminal string, autoExtract, autoStart bool, primaryModel, smallModel string, providerKeys map[string]string, openaiKey, openaiBaseURL, narrativeModel, qualityModel, summarizeModel string) error {
	hookDir := filepath.Join(home, ".claude", "hooks")
	lang := detectSystemLanguage()

	fmt.Println("  Installing...")
	fmt.Println()

	// 1. Create directories
	withSpinner("Creating directories", func() (string, error) {
		// dataDir and its archive subdirectory hold the database, config.yaml
		// with the API keys and auth_token: owner-only, and hardened for an
		// existing installation too, because MkdirAll leaves the mode of a
		// directory it did not create (audit 16.10.2026, YM-328).
		archiveDir := filepath.Join(dataDir, "archive")
		for _, d := range []string{dataDir, archiveDir} {
			os.MkdirAll(d, 0o700)
			if err := os.Chmod(d, 0o700); err != nil {
				log.Printf("setup: chmod %s: %v", d, err)
			}
		}
		os.MkdirAll(hookDir, 0755)
		return "", nil
	})

	// 2. Check and write config.yaml. The order lives in writeConfigForInstall: the check must read the
	// file this run replaces, not the template it writes (YM-463). Its lines are buffered because the
	// spinner prints its label without a newline, and a report inside it would read as part of the label.
	var checkOut, checkWarn bytes.Buffer
	withSpinner("Writing config.yaml", func() (string, error) {
		cfgContent := generateConfig(model, autoExtract, apiKey, openaiKey, openaiBaseURL, provider, terminal, narrativeModel, qualityModel, summarizeModel)
		return model, writeConfigForInstall(dataDir, cfgContent, &checkOut, &checkWarn)
	})
	fmt.Print(checkOut.String())
	fmt.Fprint(os.Stderr, checkWarn.String())

	// 2b. Bootstrap SYSTEM.md (if not present)
	withSpinner("Writing system prompt template", func() (string, error) {
		created, err := daemon.EnsureSystemPromptTemplate(dataDir)
		if err != nil {
			return "", err
		}
		if created {
			return "created", nil
		}
		return "already exists", nil
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
		installed, err := installBundledCommands(home, dataDir, lang, apiKey, model, provider)
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
		installed, err := InstallBundledSkills(home)
		if err != nil {
			return "", err
		}
		if installed > 0 {
			return fmt.Sprintf("%d skills", installed), nil
		}
		return "up to date", nil
	})

	// 7b.2 Install opencode commands (for opencode TUI users)
	withSpinner("Installing opencode commands", func() (string, error) {
		installed, err := InstallOpencodeCommands(home)
		if err != nil {
			return "", err
		}
		if installed > 0 {
			return fmt.Sprintf("%d commands", installed), nil
		}
		return "up to date", nil
	})

	// 7c. Install bundled capabilities. A cap the bundle was not derived from is
	// kept and its conflict printed after the step: the spinner owns its own line,
	// and a skip nobody sees is what this path exists to end (YM-450).
	var capConflicts []CapConflict
	withSpinner("Installing capabilities", func() (string, error) {
		installed, conflicts, err := InstallBundledCaps(los35eCapsDirForSetup(dataDir))
		capConflicts = conflicts
		if err != nil {
			return "", err
		}
		if installed > 0 {
			return fmt.Sprintf("%d caps", installed), nil
		}
		return "up to date", nil
	})
	PrintCapConflicts(os.Stdout, capConflicts)

	// 7d. Install opencode plugin (only if opencode is installed).
	// Threads primaryModel/smallModel through so the user's wizard choice is
	// written atomically — prevents the hardcoded deepseek defaults from
	// winning over the user's selection (step 7d2 alone cannot overwrite
	// because deepMergeJSON preserves existing scalars).
	if detectOpencodeBinary() != "" {
		// 7d-pre. Migrate any legacy opencode.json (yesmem-managed) to
		// opencode.jsonc so opencode actually reads what we write. opencode's
		// globalConfigFile() prefers jsonc; without this step, a user upgrading
		// from an older yesmem (which wrote opencode.json) would have their
		// provider/MCP config silently ignored on next opencode start.
		withSpinner("Migrating opencode config", func() (string, error) {
			if err := migrateOpencodeJsonToJsonc(home); err != nil {
				return "", err
			}
			return "ok", nil
		})

		withSpinner("Installing opencode plugin", func() (string, error) {
			return installOpencodePlugin(home, binaryPath, primaryModel, smallModel)
		})
	}

	// 7d2. Set model preference (from wizard or default).
	// Redundant when 7d ran with non-empty primaryModel, but kept as a separate
	// step for paths where opencode is not installed (no 7d call) or future
	// callers that skip the plugin install.
	if primaryModel != "" {
		withSpinner("Setting model preference", func() (string, error) {
			if err := mergeOpencodeSettingsWith(home, primaryModel, smallModel, binaryPath); err != nil {
				return "", err
			}
			return primaryModel, nil
		})
	}

	// 7d3. Write provider API keys to auth.json (DeepSeek, OpenAI, etc.)
	if len(providerKeys) > 0 {
		withSpinner("Writing provider credentials", func() (string, error) {
			if err := writeAuthJSON(home, providerKeys); err != nil {
				return "", err
			}
			ids := make([]string, 0, len(providerKeys))
			for id := range providerKeys {
				ids = append(ids, id)
			}
			return strings.Join(ids, ", "), nil
		})
	}

	// 7d4. Configure codex MCP tool approvals (per‑tool, avoids interactive picker)
	if detectCodexBinary() != "" {
		withSpinner("Configuring codex approvals", func() (string, error) {
			if err := ensureCodexSetup(home, binaryPath); err != nil {
				return "", err
			}
			return fmt.Sprintf("%d tools", len(yesmemCodexToolNames)), nil
		})
	}

	// 7e. Seed the project's deviation file. The standard rules live in the installation, so nothing numbered
	// is generated into the checkout: a project states only where it differs. A checkout with a complete
	// RULES.md of its own is left untouched (it wins alone in guard.LoadRules).
	if cwd, err := os.Getwd(); err == nil {
		withSpinner("Seeding PROJECT_RULES.md", func() (string, error) {
			path, err := seedProjectRules(cwd)
			if err != nil {
				return "", err
			}
			if path != "" {
				return filepath.Base(path), nil
			}
			return "nothing to declare", nil
		})
	}

	// 7f. Install the installation's own RULES.md: the one place the standard rules are maintained, and the
	// copy the guard enforces from (guard.LoadInstalledRules). A copy the operator edited is kept, and only a
	// provably untouched one is replaced, so a new standard rule can reach the machine.
	withSpinner("Installing RULES.md", func() (string, error) {
		path, err := installRULESmd(dataDir)
		if err != nil {
			return "", err
		}
		if path != "" {
			return filepath.Join("yesmem", filepath.Base(path)), nil
		}
		return "kept", nil
	})

	// 8. Write .mcp.json
	withSpinner("Writing .mcp.json", func() (string, error) {
		return "", mergeMcpJSON(filepath.Join(home, ".mcp.json"), binaryPath)
	})

	// 9b. Install codebase-memory-mcp CLI for code intelligence
	withSpinner("Installing codebase-memory-mcp", func() (string, error) {
		return ensureCBMBinary(dataDir)
	})

	// 9c. Its daemon needs a unit as well: started by whichever client comes first, it has
	// no version, no restart, and every scan without it pays the cold start.
	if runtime.GOOS == "linux" {
		withSpinner("Setting up the code graph daemon", func() (string, error) {
			if _, err := EnsureCBMUnit(home, CBMBinaryPath(dataDir)); err != nil {
				return "", err
			}
			return "systemd", nil
		})
	}

	// 10. Auto-start service
	if autoStart {
		withSpinner("Setting up auto-start", func() (string, error) {
			if runtime.GOOS == "linux" {
				return "systemd", setupSystemd(home, binaryPath, dataDir)
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
			return "", verifyLLMConnection(dataDir, apiKey, model, provider, openaiBaseURL)
		})
	}

	// 14. Report the rule guard's resolution. The guard picks its model at runtime, so this is a report of
	// that resolution, not a value written anywhere: config.yaml keeps no model name for it.
	printGuardStatus(dataDir, provider)

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

// writeAuthJSON merges provider API keys into opencode's auth.json.
// Path: ~/.local/share/opencode/auth.json
// Format: {"deepseek": {"key": "sk-..."}, "openai": {"key": "sk-..."}}
func writeAuthJSON(home string, providerKeys map[string]string) error {
	if len(providerKeys) == 0 {
		return nil
	}
	authPath := filepath.Join(home, ".local", "share", "opencode", "auth.json")
	if err := os.MkdirAll(filepath.Dir(authPath), 0755); err != nil {
		return fmt.Errorf("create auth dir: %w", err)
	}

	var entries map[string]map[string]string
	data, err := os.ReadFile(authPath)
	if err == nil {
		if jsonErr := json.Unmarshal(data, &entries); jsonErr != nil {
			entries = nil
		}
	}
	if entries == nil {
		entries = make(map[string]map[string]string)
	}

	for providerID, key := range providerKeys {
		entries[providerID] = map[string]string{"key": key}
	}

	out, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return err
	}
	return writeSecretFile(authPath, append(out, '\n'))
}

// modelToProviderID maps an extraction model to its provider ID for auth.json.
func modelToProviderID(model string) string {
	switch {
	case strings.HasPrefix(model, "deepseek"):
		return "deepseek"
	case strings.HasPrefix(model, "gpt"):
		return "openai"
	default:
		return ""
	}
}

// derivePrimaryAndSmallModel picks opencode's primaryModel/smallModel pair based
// on the chosen provider, model, providerID (from resolveOpenCodeProvider), and
// baseURL. Replaces the former hardcoded deepseek-reasoner/chat default that
// was wrong for every non-DeepSeek user (big-pickle, glm-5.2, claude, gpt).
//
// Mapping:
//   - provider "opencode" (free-tier): opencode/<chosenModel> for both
//   - provider "cli" or "api" (Anthropic): anthropic/<chosenModel>, anthropic/haiku
//   - provider "openai_compatible" with deepseek baseURL: two-tier deepseek-reasoner + deepseek-chat
//   - provider "openai_compatible" with providerID hint: <providerID>/<chosenModel> for both
//   - provider "openai_compatible" with openai.com baseURL: openai/<chosenModel> for both
//   - fallback: bare chosenModel for both (daemon resolves)
func derivePrimaryAndSmallModel(provider, chosenModel, providerID, baseURL string) (primaryModel, smallModel string) {
	switch {
	case provider == "opencode":
		return "opencode/" + chosenModel, "opencode/" + chosenModel
	case provider == "cli" || provider == "api":
		return "anthropic/" + chosenModel, "anthropic/haiku"
	case provider == "openai_compatible" && strings.Contains(strings.ToLower(baseURL), "deepseek.com"):
		// DeepSeek two-tier: intentionally overrides chosenModel. opencode.json's
		// deepseek provider block only knows the reasoner/chat IDs, not the
		// deepseek-v4-pro/flash names the user picked in resolveOpenCodeProvider.
		return "deepseek/deepseek-reasoner", "deepseek/deepseek-chat"
	case provider == "openai_compatible" && providerID != "":
		return providerID + "/" + chosenModel, providerID + "/" + chosenModel
	case provider == "openai_compatible" && strings.Contains(strings.ToLower(baseURL), "openai.com"):
		return "openai/" + chosenModel, "openai/" + chosenModel
	default:
		return chosenModel, chosenModel
	}
}

// providerEnvVar returns the environment variable name for a provider's API key.
func providerEnvVar(providerID string) string {
	switch providerID {
	case "deepseek":
		return "DEEPSEEK_API_KEY"
	case "openai":
		return "OPENAI_API_KEY"
	default:
		return ""
	}
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

// backupAndWriteConfigFile keeps a copy of an existing config.yaml and writes
// the fresh template. setup rewrites the file wholesale, so a copy is the only
// way back to the previous keys. Both writes go through config.WriteFile: the
// file and its copy carry the API keys, so both are owner-only and atomic, the
// same rule the runtime writer follows (YM-268).
func backupAndWriteConfigFile(dataDir, content string) error {
	cfgPath := filepath.Join(dataDir, "config.yaml")
	backupDir := filepath.Join(dataDir, "backup")
	// A Stat error other than "does not exist" means the question cannot be
	// answered, and the template write below would then replace a file nobody
	// backed up (EACCES, ENOTDIR, a dangling link).
	_, statErr := os.Stat(cfgPath)
	cfgExists := statErr == nil
	if statErr != nil && !os.IsNotExist(statErr) {
		return fmt.Errorf("stat config.yaml before writing it: %w", statErr)
	}
	if cfgExists {
		// Copies of the key file go into their own directory, owner-only like the
		// file itself (the parent data dir is 0700).
		if err := os.MkdirAll(backupDir, 0o700); err != nil {
			return fmt.Errorf("create config backup dir: %w", err)
		}
		// MkdirAll leaves an existing directory as it is, so an install created
		// before this mode was set keeps a 0755 backup dir holding key copies.
		if err := os.Chmod(backupDir, 0o700); err != nil {
			log.Printf("setup: config backup dir mode: %v", err)
		}
		// A second setup run in the same second must not overwrite the first
		// copy: the timestamp alone collides, so the name carries a counter.
		backupPath, err := config.NextBackupPath(filepath.Join(backupDir, "config.yaml"))
		if err != nil {
			return fmt.Errorf("config backup: %w", err)
		}
		data, err := os.ReadFile(cfgPath)
		if err != nil {
			return fmt.Errorf("read config for the backup: %w", err)
		}
		// No copy, no overwrite: the template write below would replace the only
		// copy of the API keys. Skipping the copy quietly was a silent loss.
		if err := config.WriteFile(backupPath, data); err != nil {
			return fmt.Errorf("write config backup: %w", err)
		}
	}
	// The installer keeps its copies in a directory of their own, where the
	// runtime prune never looks. Without this the pile of key copies grows with
	// every setup and update run.
	config.PruneBackups(backupDir, "config.yaml")
	return config.WriteFile(cfgPath, []byte(content))
}

func generateConfig(model string, autoExtract bool, apiKey, openaiKey, openaiBaseURL, provider, terminal, narrativeModel, qualityModel, summarizeModel string) string {
	lang := detectSystemLanguage()
	langs := fmt.Sprintf("[%s, en]", lang)
	if lang == "en" {
		langs = "[en]"
	}

	// agents.default_backend depends on provider
	agentsBackend := "claude"
	if provider == "openai_compatible" {
		agentsBackend = "opencode"
	}

	// Default model values for backward compatibility
	if narrativeModel == "" {
		narrativeModel = "opus"
	}
	if qualityModel == "" {
		qualityModel = "sonnet"
	}
	if summarizeModel == "" {
		summarizeModel = "haiku"
	}

	keyLine := "${ANTHROPIC_API_KEY}"
	if apiKey != "" {
		keyLine = apiKey
	}

	apiBlock := "api_key: " + keyLine
	llmBlock := "provider: " + provider
	if provider == "openai" || provider == "openai_compatible" {
		oaiKey := openaiKey
		if oaiKey == "" {
			oaiKey = apiKey
		}
		if oaiKey == "" {
			oaiKey = "${OPENAI_API_KEY}"
		}
		oaiURL := openaiBaseURL
		if oaiURL == "" {
			oaiURL = "${OPENAI_BASE_URL}"
		}
		apiBlock = "api_key: " + keyLine + "\n  openai_api_key: " + oaiKey + "\n  openai_base_url: " + oaiURL
		llmBlock = "provider: " + provider + "\n  openai_base_url: " + oaiURL + "\n  complete_provider: opencode"
	} else {
		llmBlock = "provider: " + provider + "\n  complete_provider: \"\""
	}

	return fmt.Sprintf(`# ============================================================================
# YesMem Configuration
# Long-term memory system for coding agents
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
  summarize_model: %s

  # Pass 2 — Extraction model. Reads summaries and extracts structured learnings.
  # This is the model you chose during setup.
  # Shortnames: "haiku", "sonnet", "opus" — or full Anthropic model ID
  # Haiku (~$6/mo)  = finds obvious things, cheapest
  # Sonnet (~$21/mo) = understands context, good quality/cost balance
  # Opus (~$104/mo)  = detects nuance, irony, implicit knowledge
  model: %s

  # Narrative generation — session handovers, project profiles, persona traits.
  # Needs strong language understanding for coherent narrative text.
  narrative_model: %s

  # Quality refinement — deduplicates, rates relevance, resolves contradictions.
  # Also used for Persona synthesis. Falls back to narrative_model if not set.
  quality_model: %s

  # How much session content is sent to the LLM:
  # "full"        = everything (best quality, higher cost)
  # "prefiltered" = rule-based pre-filter (saves tokens, may miss things)
  mode: prefiltered

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
  #
  # ⚠ OAuth tokens (from Claude Code login) are exclusively for Claude Code
  #   and Claude.ai — using them with provider: "api" violates Anthropic's ToS.
  #   If you logged into Claude Code via OAuth, use provider: "cli" only.
  %s

  # Path to claude binary (only needed for provider: "cli" if not in PATH)
  claude_binary: ""

  # Daily spending limits per model tier (USD). Prevents cost explosion
  # with many parallel sessions or large backlogs. 0 = no limit.
  # Only applies to provider: "api" — CLI usage is covered by your subscription.
  daily_budget_extract_usd: 20.0   # Budget for Pass 1 (extraction) + evolution
  daily_budget_quality_usd: 10.0   # Budget for narrative + Pass 2 (quality)

  # Max cost per single LLM call (USD). Safety net against runaway prompts.
  max_budget_per_call_usd: 1.0

# --- DONE verification ---
# The gate reads the verdict a verify_claims run reached before the DONE: the
# report is proved, not believed. budget is the default run budget of that call,
# max_wait how long it waits for the single verification slot. model is part of
# the schema for symmetry with rule_guard; a run calls no model.
done_verify:
  enabled: true
  model: ""
  budget: 5m
  max_wait: 10m

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
  remind_open_work: true

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

  # Upstream API for OpenAI-format requests.
  openai_target: "https://api.openai.com"

  # Per-provider target URLs for OpenAI-compatible providers.
  # Auto-discovery fills this from opencode config when auto_configure_providers is true.
  provider_targets: {}

  # Automatically discover and configure provider routing from opencode config.
  auto_configure_providers: true

  # Compress when conversation context exceeds this token count.
  token_threshold: 250000

  # Model-specific thresholds override the global token_threshold.
  token_thresholds:
    opus: 180000
    sonnet: 180000
    haiku: 130000
    gpt-5: 180000
    codex: 180000
    deepseek: 600000
    glm-5.2: 500000

  token_minimum_threshold: 100000
  keep_recent: 10
  sawtooth_enabled: true
  cache_ttl: "ephemeral"

  cache_keepalive_enabled: true
  cache_keepalive_mode: "5m"
  cache_keepalive_min_messages: 10
  cache_keepalive_pings_5m: 5
  cache_keepalive_pings_1h: 1
  usage_deflation_factor: 0.7
  reset_cache: false

  # --- Prompt Feature Flags (top-level, applied to all models) ---
  prompt_ungate: true
  prompt_rewrite: false
  prompt_enhance: false
  effort_floor: ""
  skill_eval_inject: "silent"
  code_nav_mode: "block"

  # --- Prompt Profile Flags ---
  # Profile-aware prompt injection layers. shared_prompt is the base for ALL
  # profiles. claude_prompt/codex_prompt/opencode_prompt override per-profile.
  # A missing or false flag in a profile means "inherit from shared_prompt".
  prompt_beweislast: true
  prompt_output_discipline: true
  prompt_scope_discipline: true
  prompt_delegation_contract: true
  prompt_clarify_first: true
  prompt_code_tools_first: true
  prompt_coding_discipline: true
  prompt_fable: true
  prompt_pattern_suggest: true
  prompt_tool_prefs: true
  prompt_wiki_first: true

  shared_prompt:
    prompt_beweislast: true
    prompt_output_discipline: true
    prompt_scope_discipline: true
    prompt_delegation_contract: false
    prompt_clarify_first: true
    prompt_code_tools_first: true
    prompt_coding_discipline: true
    prompt_fable: true
    prompt_enhance: false
    prompt_pattern_suggest: false
    prompt_rewrite: false
    prompt_tool_prefs: false
    prompt_ungate: false
    prompt_wiki_first: false

  # Per-profile overrides (empty = inherit from shared_prompt + flat fields)
  claude_prompt: null
  codex_prompt: null
  opencode_prompt: null

  # --- Per-Model Feature Gates ---
  feature_defaults:
    assoc_context: true
    briefing: true
    loop_warning: true
    plan_checkpoint: true
    rules_reminder: true
    skill_eval: true
    think_reminder: true
    think_reminder_min_chars: 0
    timestamps: true

  model_features:
    claude:
      assoc_context: true
      briefing: true
      loop_warning: true
      plan_checkpoint: true
      rules_reminder: true
      skill_eval: true
      think_reminder: true
      think_reminder_min_chars: 0
      timestamps: false
    deepseek:
      assoc_context: true
      briefing: true
      loop_warning: true
      plan_checkpoint: false
      rules_reminder: true
      skill_eval: true
      think_reminder: true
      think_reminder_min_chars: 0
      timestamps: true
    glm:
      assoc_context: true
      briefing: true
      loop_warning: true
      plan_checkpoint: false
      rules_reminder: true
      skill_eval: true
      think_reminder: true
      think_reminder_min_chars: 0
      timestamps: true
    gpt:
      assoc_context: true
      briefing: true
      loop_warning: true
      plan_checkpoint: false
      rules_reminder: true
      skill_eval: true
      think_reminder: false
      think_reminder_min_chars: 0
      timestamps: false
    openai:
      assoc_context: true
      briefing: true
      loop_warning: true
      plan_checkpoint: false
      rules_reminder: true
      skill_eval: true
      think_reminder: false
      think_reminder_min_chars: 0
      timestamps: false

  # --- Custom System Prompt ---
  custom_system_prompt:
    enabled_opencode: true
    enabled_claude_code: false
    enabled_codex: false
    template_path: ~/.claude/yesmem/SYSTEM.md
    model_templates: {}

# --- Forked Agents (Background Learning Extraction) ---
# Spawns async API calls after each assistant response to extract learnings,
# evaluate injected memories, and detect contradictions — without blocking
# the main conversation.
forked_agents:
  enabled: false
  model: sonnet
  debug: false

  # Minimum tokens in conversation before first fork fires.
  # Lower = more frequent extraction, higher cost.
  token_growth_trigger: 20000

  # Maximum forks per session. 0 = unlimited.
  max_forks_per_session: 50

  # Maximum USD cost per session for fork calls. 0 = unlimited.
  max_cost_per_session: 5

  # Detailed fork logging in proxy.log (gate decisions, extracted learnings, evaluations).
  debug: false

# --- Embedding (Semantic Search) ---
embedding:
  provider: sse
  search:
    method: ""
    ivf_threshold: 50000
    ivf:
      k: 0
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
  haiku:             { input: 1.0, output: 5.0 }
  sonnet:            { input: 3.0, output: 15.0 }
  opus:              { input: 5.0, output: 25.0 }
  gpt-5-mini:        { input: 0.25, output: 2.0 }
  gpt-5.2:           { input: 1.75, output: 14.0 }
  gpt-5.4:           { input: 2.5, output: 15.0 }
  deepseek-v4-flash: { input: 0.14, output: 0.56 }
  deepseek-v4-pro:   { input: 0.28, output: 1.12 }

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
  model: ""

# --- Caps Directory (optional) ---
# Custom directory for capability files (CAP.md). Empty = use ~/.claude/caps/.
caps_dir: ""

# --- Sandbox (Agent Security) ---
# Default sandbox profile for spawned agents.
# Options: "none" (no sandbox), "standard" (network-restricted), "strict" (filesystem + network restricted)
default_sandbox_profile: ""

# --- Secrets Sanitization ---
# Redact secrets (API keys, tokens, passwords) from extraction content.
secrets_sanitization:
  enabled: false
  allowed_exceptions: []

# --- HTTP Server (optional) ---
# HTTP API server for external tool integration. Default: disabled.
# When enabled, exposes endpoints for query, remember, and search operations.
# auth_token is required when enabled — protects against unauthorized access.
http:
  enabled: false
  listen: "127.0.0.1:9377"
  auth_token: ""

# --- Diagnostics (optional) ---
# Debug endpoints for troubleshooting memory growth. Off by default.
# pprof binds loopback only (never 0.0.0.0) and exposes allocation sites and
# goroutine stacks, not session content. YESMEM_PPROF=1 in the environment
# overrides the pprof flag.
diagnostics:
  pprof: false
  pprof_listen: "127.0.0.1:7071"

# --- Paths ---
# All paths have sensible defaults under ~/.claude/yesmem/.
# Only set these if you need a custom location.
paths:
  db: ~/.claude/yesmem/yesmem.db
  archive: ~/.claude/yesmem/archive
  claude_projects: ~/.claude/projects
  opencode_db: ~/.local/share/opencode/opencode.db

# --- Indexer ---
# Directories excluded from session indexing.
exclude_projects: []

# --- Agents ---
# Controls how agent terminals are spawned.
agents:
  # Terminal emulator for agent windows.
  # Options: ghostty, kitty, gnome-terminal, alacritty, wezterm, xterm
  # Empty = auto-detect (uses x-terminal-emulator fallback)
  terminal: %s

  # Default backend for spawned agents: claude or opencode
  default_backend: %s

  # Terminal for showing yesmem-agents session output (viewer).
  # Falls back to terminal if empty.
  viewer_terminal: ""

  # Safety limits for spawned agents. 0 = unlimited.
  max_runtime: ""
  max_turns: 0
  max_depth: 0
  # Max output tokens per agent. The unit is output tokens, not input: input is
  # cumulative and includes cache reads, so a budget in that unit would end an
  # agent after a few turns. 0 = no limit (nothing applies a default).
  token_budget: 0

  # TUI theme per agent type (section prefix). The yesmem theme family is
  # installed to ~/.config/opencode/themes; any opencode theme name works,
  # an empty value disables the theme for that type.
  themes:
    yesloop: yesmem-loop
    yesresearch: yesmem-research
    subresearch: yesmem-subresearch
    orchestrator: yesmem-dark
  `, summarizeModel, model, narrativeModel, qualityModel, autoExtract, llmBlock, langs, apiBlock, terminal, agentsBackend)
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

// redactedForDataDir wraps c in the secret redactor when the user enabled
// secrets_sanitization. Setup builds its own clients outside the daemon factory
// (internal/daemon/llm_client_factory.go), so the switch has to be applied here
// too. dataDir is the yesmem data directory that holds config.yaml.
//
// A missing or unreadable config.yaml leaves the client unwrapped, same as
// redacted() in package main: config.Load falls back to its defaults there, and
// the default has the switch off.
func redactedForDataDir(dataDir string, c extraction.LLMClient) extraction.LLMClient {
	if c == nil {
		return nil
	}
	cfg, err := config.Load(filepath.Join(dataDir, "config.yaml"))
	if err != nil || cfg == nil || !cfg.SecretsSanitization.Enabled {
		return c
	}
	return extraction.NewSanitizingClient(c, sanitize.NewSecretRedactor(cfg.SecretsSanitization.AllowedExceptions))
}

// claudeProjectsDirFromConfig is the directory Claude Code names its project
// folders in. Setup and uninstall used to build ~/.claude/projects themselves,
// so a configured paths.claude_projects was ignored on both paths. A missing
// config.yaml yields the home default, same as the daemon.
func claudeProjectsDirFromConfig(dataDir string) string {
	cfg, _ := config.Load(filepath.Join(dataDir, "config.yaml"))
	if cfg == nil {
		// Load's contract allows a nil config with an error. Uninstall deletes
		// files, so a panic here would land in the middle of a removal.
		return ""
	}
	return cfg.ClaudeProjectsDir()
}

func readExistingConfigKey(dataDir string) string {
	data, err := os.ReadFile(filepath.Join(dataDir, "config.yaml"))
	if err != nil {
		return ""
	}
	var cfg struct {
		API struct {
			APIKey string `yaml:"api_key"`
		} `yaml:"api"`
	}
	if yaml.Unmarshal(data, &cfg) != nil {
		return ""
	}
	return cfg.API.APIKey
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
	client = redactedForDataDir(dataDir, client)

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

// buildDaemonUnit renders the systemd user unit for the daemon. Both services
// are local-first (Unix socket, SQLite, index) and must not be delayed by
// network-online.target — net-dependent work happens asynchronously after start.
//
// Type=notify plus WatchdogSec=90: the daemon reports READY once its socket
// serves and then pings every 30s, but only after a probe confirmed the socket
// still answers. Restart=always alone never fires for a process that lives on with
// a dead socket. TimeoutStartSec carries slack for a cold start (schema open,
// index, agent recovery) — it must not be the same 90s the watchdog uses, or a
// slow start looks like a hang. A binary that predates the notify protocol never
// sends READY and will not start under this unit: unit and binary move together
// (the post-update migration runs the new binary).
func buildDaemonUnit(binaryPath, display, xauth, dbus, proxyURL string) string {
	return fmt.Sprintf(`[Unit]
Description=YesMem — Long-term memory for coding agents

[Service]
Type=notify
NotifyAccess=main
WatchdogSec=90
TimeoutStartSec=300
ExecStart=%s daemon --replace
Restart=always
RestartSec=10
Environment="DISPLAY=%s"
Environment="XAUTHORITY=%s"
Environment="DBUS_SESSION_BUS_ADDRESS=%s"
Environment="ANTHROPIC_BASE_URL=%s"

[Install]
WantedBy=default.target
`, binaryPath, display, xauth, dbus, proxyURL)
}

// proxyBaseURL turns the proxy's listen address into the URL an agent tool has to
// dial. The unit declares it because a systemd daemon only inherits what the unit
// names: the value otherwise comes from the login session's manager environment
// and disappears after a reboot. A sandboxed claude without it talks straight to
// Anthropic, so memory injection, ledger and cost accounting stop silently.
func proxyBaseURL(listen string) string {
	listen = strings.TrimSpace(listen)
	if listen == "" {
		listen = config.DefaultProxyListen
	}
	host, port, err := net.SplitHostPort(listen)
	if err != nil || port == "" {
		host, port = "", strings.TrimPrefix(listen, ":")
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "localhost"
	}
	return "http://" + net.JoinHostPort(host, port)
}

// migrateDaemonProxyEnv adds the proxy URL to an installed unit that predates it.
// Additive only: an explicit value stays, because someone chose it.
func migrateDaemonProxyEnv(content, proxyURL string) string {
	if proxyURL == "" || indexOfDirective(content, `Environment="ANTHROPIC_BASE_URL=`) >= 0 {
		return content
	}
	if indexOfDirective(content, "ANTHROPIC_BASE_URL=") >= 0 {
		return content
	}
	anchor := indexOfDirective(content, `Environment="DBUS_SESSION_BUS_ADDRESS=`)
	if anchor < 0 {
		anchor = indexOfDirective(content, "[Service]")
	}
	if anchor < 0 {
		return content
	}
	return insertUnitLines(strings.Split(content, "\n"), anchor, `Environment="ANTHROPIC_BASE_URL=`+proxyURL+`"`)
}

// buildProxyUnit renders the systemd user unit for the proxy. It only listens
// on localhost and forwards on demand, so it must not block on network setup.
func buildProxyUnit(binaryPath string) string {
	return fmt.Sprintf(`[Unit]
Description=YesMem Proxy — Infinite-thread context for coding agents

[Service]
Type=simple
ExecStart=%s proxy
Restart=always
RestartSec=10

[Install]
WantedBy=default.target
`, binaryPath)
}

// MigrateSystemdUnits removes After/Wants=network-online.target from already
// installed unit files (both services are local-first) and upgrades the daemon
// unit to the notify type. Idempotent — only rewrites files that still carry the
// obsolete network dependency or the old type. Called from the post-update
// migration so existing installs heal on the next `yesmem update`.
func MigrateSystemdUnits(home, dataDir string) (int, error) {
	serviceDir := filepath.Join(home, ".config", "systemd", "user")
	cfg, _ := config.Load(filepath.Join(dataDir, "config.yaml"))
	proxyURL := proxyBaseURL(cfg.Proxy.Listen)
	changed := 0
	for _, name := range []string{"yesmem.service", "yesmem-proxy.service"} {
		path := filepath.Join(serviceDir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return changed, err
		}
		content := string(data)
		updated := content
		if strings.Contains(updated, "network-online.target") {
			updated = stripNetworkOneline(updated)
		}
		if name == "yesmem.service" {
			notifyUnit := migrateDaemonUnitWatchdog(updated)
			if notifyUnit != updated {
				// Type=notify is a one-way door: a downgraded binary never sends
				// READY, so the unit blocks the start for TimeoutStartSec and then
				// fails, and the binary that could undo it is the one that cannot
				// start. Keep the unit as it was, once, before the rewrite.
				if err := keepUnitBackup(path, content); err != nil {
					return changed, err
				}
			}
			updated = notifyUnit
			if unitRunsThisBinary(updated) {
				updated = migrateDaemonProxyEnv(updated, proxyURL)
			}
		}
		if updated == content {
			continue
		}
		if err := os.WriteFile(path, []byte(updated), 0644); err != nil {
			return changed, err
		}
		changed++
	}
	return changed, nil
}

// UnitBackupSuffix marks the copy a migration keeps of a unit it changes in a way
// it cannot undo. systemd only reads files ending in .service, so the copy is
// inert where it lies.
const UnitBackupSuffix = ".pre-migrate.bak"

// UnitBackupPath is where the pre-migration copy of a unit lives.
func UnitBackupPath(unitPath string) string { return unitPath + UnitBackupSuffix }

// keepUnitBackup writes the unit as it was before the migration, and never
// overwrites an existing copy: a second run must keep the original, not the state
// of the run before. Lstat and O_EXCL are deliberate: a symlink at the backup path
// is not a copy (following it would write to whatever it points at), and the
// mode comes from the unit itself, so a hardened unit does not become a readable
// copy just because it was backed up.
func keepUnitBackup(unitPath, content string) error {
	backup := UnitBackupPath(unitPath)
	if info, err := os.Lstat(backup); err == nil {
		if info.Mode().IsRegular() {
			return nil
		}
		return fmt.Errorf("pre-migration copy %s exists but is not a regular file — move it away and run the migration again", backup)
	}
	mode := os.FileMode(0o600)
	if info, err := os.Stat(unitPath); err == nil {
		mode = info.Mode().Perm()
	}
	f, err := os.OpenFile(backup, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return fmt.Errorf("keep pre-migration copy of %s (a notify unit cannot be started by an older binary, so the migration is not written without one): %w", filepath.Base(unitPath), err)
	}
	if _, err := f.WriteString(content); err != nil {
		f.Close()
		return fmt.Errorf("write pre-migration copy %s: %w", backup, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close pre-migration copy %s: %w", backup, err)
	}
	return nil
}

// systemdReloadTimeout bounds one systemctl invocation. `systemctl --user` talks to
// the user manager over D-Bus; when that manager is wedged the call never returns,
// and a migration without a deadline would sit there with no output forever.
const systemdReloadTimeout = 20 * time.Second

// systemctlRun runs one systemctl command under a context. Seamed so a test can
// assert the deadline without a user manager.
var systemctlRun = func(ctx context.Context, args ...string) error {
	return exec.CommandContext(ctx, "systemctl", args...).Run()
}

// ReloadSystemdUnits makes systemd re-read the unit files a migration rewrote. It
// is best effort for the caller: a reload that fails leaves the old definition in
// memory, not a broken unit on disk.
func ReloadSystemdUnits() error {
	ctx, cancel := context.WithTimeout(context.Background(), systemdReloadTimeout)
	defer cancel()
	if err := systemctlRun(ctx, "--user", "daemon-reload"); err != nil {
		return fmt.Errorf("systemctl --user daemon-reload: %w", err)
	}
	return nil
}

// migrationSelfPath is the binary that is running the migration. A unit whose
// ExecStart names a different binary is left alone: Type=notify only works with a
// daemon that sends READY, and the only binary we can vouch for is ourselves.
var migrationSelfPath = func() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		return resolved
	}
	return filepath.Clean(exe)
}()

// unitExecStart returns the program a unit starts, or "" when the unit names none.
func unitExecStart(content string) string {
	for _, line := range strings.Split(content, "\n") {
		value, found := strings.CutPrefix(strings.TrimSpace(line), "ExecStart=")
		if !found {
			continue
		}
		fields := strings.Fields(value)
		if len(fields) == 0 {
			return ""
		}
		return fields[0]
	}
	return ""
}

// unitRunsThisBinary reports whether the unit starts the binary that is running the
// migration. An unverifiable answer counts as no.
func unitRunsThisBinary(content string) bool {
	execStart := unitExecStart(content)
	if execStart == "" || migrationSelfPath == "" {
		return false
	}
	return filepath.Clean(execStart) == filepath.Clean(migrationSelfPath)
}

// migrateDaemonUnitWatchdog upgrades an installed daemon unit to the notify type
// with the watchdog the binary reports since YM-312: READY=1 once the socket
// serves, WATCHDOG=1 only while it answers. Additive only — a unit that already
// names a deadline or a type other than the default keeps it, because that value
// was a decision; it only gains the start deadline it may still be missing.
func migrateDaemonUnitWatchdog(content string) string {
	if !unitRunsThisBinary(content) {
		// Someone else's binary is installed: rewriting the unit would make the
		// service wait for a READY that never comes.
		return content
	}
	if watchdogAt := indexOfDirective(content, "WatchdogSec="); watchdogAt >= 0 {
		if indexOfDirective(content, "TimeoutStartSec=") >= 0 {
			return content
		}
		lines := strings.Split(content, "\n")
		return insertUnitLines(lines, watchdogAt, "TimeoutStartSec=300")
	}
	lines := strings.Split(content, "\n")
	typeIdx, serviceIdx, explicitOther := -1, -1, false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		switch {
		case trimmed == "[Service]":
			if serviceIdx < 0 {
				serviceIdx = i
			}
		case trimmed == "Type=simple":
			if typeIdx < 0 {
				typeIdx = i
			}
		case strings.HasPrefix(trimmed, "Type="):
			explicitOther = true
		}
	}
	if typeIdx >= 0 {
		lines[typeIdx] = "Type=notify"
		return insertUnitLines(lines, typeIdx, "NotifyAccess=main", "WatchdogSec=90", "TimeoutStartSec=300", notifyRollbackHint)
	}
	if explicitOther || serviceIdx < 0 {
		return content
	}
	// Units written before Type= was explicit rely on systemd's default.
	return insertUnitLines(lines, serviceIdx, notifyRollbackHint, "Type=notify", "NotifyAccess=main", "WatchdogSec=90", "TimeoutStartSec=300")
}

// notifyRollbackHint sits in the migrated unit because that is where the person
// looks when the service will not start after a downgrade. It names the copy the
// migration kept (UnitBackupSuffix) and the reload that makes systemd read it.
const notifyRollbackHint = "# yesmem: notify type and watchdog added; to run an older binary put back yesmem.service.pre-migrate.bak and call systemctl --user daemon-reload"

// indexOfDirective returns the index of the first line that sets a directive.
func indexOfDirective(content, directive string) int {
	for i, line := range strings.Split(content, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), directive) {
			return i
		}
	}
	return -1
}

// insertUnitLines puts directives directly behind a line, keeping the file's line
// endings and everything else untouched.
func insertUnitLines(lines []string, idx int, added ...string) string {
	out := make([]string, 0, len(lines)+len(added))
	out = append(out, lines[:idx+1]...)
	out = append(out, added...)
	out = append(out, lines[idx+1:]...)
	return strings.Join(out, "\n")
}

// stripNetworkOneline removes After=/Wants=network-online.target directives
// from a unit file, leaving everything else untouched.
func stripNetworkOneline(content string) string {
	var kept []string
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "After=network-online.target" || trimmed == "Wants=network-online.target" {
			continue
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n")
}

func setupSystemd(home, binaryPath, dataDir string) error {
	serviceDir := filepath.Join(home, ".config", "systemd", "user")
	os.MkdirAll(serviceDir, 0755)

	// Agent terminals (gnome-terminal, etc.) need the graphical session env to
	// render a window. systemd-user services do not inherit it, so capture at
	// setup time and bake into the unit.
	display := os.Getenv("DISPLAY")
	if display == "" {
		display = ":0"
	}
	xauth := os.Getenv("XAUTHORITY")
	if xauth == "" {
		xauth = filepath.Join(home, ".Xauthority")
	}
	dbus := os.Getenv("DBUS_SESSION_BUS_ADDRESS")
	if dbus == "" {
		dbus = fmt.Sprintf("unix:path=/run/user/%d/bus", os.Getuid())
	}
	cfg, _ := config.Load(filepath.Join(dataDir, "config.yaml"))

	// Daemon unit
	daemonUnit := buildDaemonUnit(binaryPath, display, xauth, dbus, proxyBaseURL(cfg.Proxy.Listen))
	if err := os.WriteFile(filepath.Join(serviceDir, "yesmem.service"), []byte(daemonUnit), 0644); err != nil {
		return err
	}

	// Proxy unit — separate process, survives terminal closes
	proxyUnit := buildProxyUnit(binaryPath)
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

// resolveLLMModelIDForVerify resolves the model ID for the setup verify call.
// The caller-supplied `model` is threaded through so provider-specific model
// IDs (e.g. "big-pickle" for opencode free-tier) reach the CLI subprocess
// instead of being silently replaced by the Anthropic haiku default.
// Empty model falls back to the "haiku" tier shortname, preserving prior
// behavior for callers that leave the model unset.
func resolveLLMModelIDForVerify(provider, model string) string {
	effective := strings.TrimSpace(model)
	if effective == "" {
		effective = "haiku"
	}
	return config.ResolveModelIDForProvider(provider, effective, "claude-haiku-4-5-20251001", "gpt-5-mini")
}

// discoverSkills is deprecated — skill discovery has been removed.
// Skills are now activated via [skill-eval] injection in the proxy.
func discoverSkills(dataDir, apiKey, model string) int {
	return 0
}

// verifyLLMConnection makes a single minimal LLM call to verify the API key and provider work.
// Returns nil on success, error with diagnostic message on failure.
// baseURL is threaded through so openai_compatible providers (DeepSeek, GLM, etc.)
// route to their real upstream instead of defaulting to api.openai.com.
func verifyLLMConnection(dataDir, apiKey, model, provider, baseURL string) error {
	modelID := resolveLLMModelIDForVerify(provider, model)
	client, err := extraction.NewLLMClient(provider, apiKey, modelID, "", baseURL)
	if err != nil {
		return fmt.Errorf("could not create LLM client: %w", err)
	}
	client = redactedForDataDir(dataDir, client)
	// opencode CLI subprocess needs a real system prompt to produce text output
	// (without it, the opencode binary often returns empty "no text events")
	systemMsg := "Reply with OK."
	if provider == "opencode" {
		systemMsg = "You are a connection-test assistant. The user will send 'ping'. Respond with exactly 'OK' and nothing else. No tool calls, no thinking, just the word OK."
	}
	_, err = client.Complete(systemMsg, "ping")
	if err != nil {
		return fmt.Errorf("LLM call failed: %w", err)
	}
	return nil
}

// installBundledCommands copies embedded skill files to ~/.claude/commands/.
// Uses SHA-256 hash to skip unchanged files. If lang != "de" and apiKey is set,
// translates content via LLM before installing. Returns number of installed/updated commands.
func installBundledCommands(home, dataDir, lang, apiKey, model, provider string) (int, error) {
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
				if translated, err := translateCommand(dataDir, string(data), lang, apiKey, model, provider); err == nil {
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

// InstallBundledSkills copies embedded skill directories to ~/.claude/skills/.
// Uses SHA-256 hash to skip unchanged files. Returns number of installed/updated files.
func InstallBundledSkills(home string) (int, error) {
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

// InstallOpencodeCommands copies embedded command files to ~/.config/opencode/commands/.
// Uses SHA-256 hash to skip unchanged files. Returns number of installed/updated files.
func InstallOpencodeCommands(home string) (int, error) {
	cmdsDir := filepath.Join(home, opencodeCommandsRelDir)
	installed := 0

	// A commands directory that is itself a symlink carries every write into the
	// link target. os.MkdirAll would accept it, and the uninstall sweep refuses
	// the same shape.
	if fi, err := os.Lstat(cmdsDir); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		return 0, fmt.Errorf("opencode commands dir %s is a symlink, refusing to install into it", cmdsDir)
	}
	if err := os.MkdirAll(cmdsDir, 0755); err != nil {
		return 0, fmt.Errorf("create opencode commands dir: %w", err)
	}

	// The name list is the same one the uninstall sweep uses, so it is also
	// filtered for plain entry names: a bundle entry carrying a path separator
	// must never reach filepath.Join (YM-386).
	names, err := bundledOpencodeCommandNames()
	if err != nil {
		return 0, err
	}
	var errs []error
	for _, name := range names {
		data, err := skills.BundledOpencodeCommands.ReadFile(filepath.Join("bundled-commands", "opencode", name))
		if err != nil {
			continue
		}
		dst := filepath.Join(cmdsDir, name)
		// os.WriteFile follows a symlink at the destination, so a link planted at
		// a command name would carry the content into its target. One such file
		// must not stop the others: dotfile setups link single commands on
		// purpose, and the run reports what it skipped.
		if fi, err := os.Lstat(dst); err == nil && fi.Mode()&os.ModeSymlink != 0 {
			errs = append(errs, fmt.Errorf("opencode command %s is a symlink, refusing to overwrite its target", dst))
			continue
		}
		newHash := fmt.Sprintf("%x", sha256.Sum256(data))
		if existing, err := os.ReadFile(dst); err == nil {
			oldHash := fmt.Sprintf("%x", sha256.Sum256(existing))
			if oldHash == newHash {
				continue
			}
		}
		if err := os.WriteFile(dst, data, 0644); err != nil {
			errs = append(errs, fmt.Errorf("write opencode command %s: %w", name, err))
			continue
		}
		installed++
	}
	return installed, errors.Join(errs...)
}

// los35eCapsDirForSetup resolves the CAP.md directory for the install at dataDir.
// The configured caps_dir is deliberately not read here: step 2 of this same run
// rewrites config.yaml from the template (caps_dir: ""), so the file on disk at
// this point is the one this run just wrote. yesmem update resolves it from the
// live config instead.
//
// The data dir case the YM-353 commit named, a data dir outside ~/.claude moving
// the installer's default, cannot occur on this path: Run pins dataDir to
// ~/.claude/yesmem. The derivation is shared with the daemon and the update path
// because those do run against a configured data dir, and because two derivations
// of the same path drift apart silently, which they had (YM-359).
func los35eCapsDirForSetup(dataDir string) string {
	return config.CapsDirPath("", dataDir)
}

// InstallBundledCaps installs the embedded CAP.md files into capsDir and returns
// how many it wrote and which caps it refused to write. The error concerns the caps
// dir itself only; a cap that cannot be reconciled is a conflict, not a failure.
//
// capsDir must be absolute: a relative path lands under the caller's working
// directory, where nothing imports it, and the export path refuses the same case
// (daemon.cap_sync.WriteCapToDisk).
func InstallBundledCaps(capsDir string) (int, []CapConflict, error) {
	manifest, err := loadBundledCapManifest()
	if err != nil {
		return 0, nil, err
	}
	return installBundledCaps(capsDir, manifest)
}

// installBundledCaps is the installer with the bundled provenance passed in, so a
// test can describe a bundle that lists some caps and not others.
//
// A bundled body replaces the installed one only on evidence that the bundle is its
// successor: the provenance file of the installation (or, for an install that has
// none yet, the bundled file) must name the hash of the body sitting there.
// Everything else is kept and reported. The frontmatter version is no part of that
// decision any more: it counts local revisions, drifted far ahead of the bundles,
// and every setup run reported success while six caps stayed inert (YM-450, YM-446).
func installBundledCaps(capsDir string, bundled capManifest) (int, []CapConflict, error) {
	if !filepath.IsAbs(capsDir) {
		return 0, nil, fmt.Errorf("caps dir %q is not absolute", capsDir)
	}
	// The root directory carries the same mode as the cap directories inside it and
	// is repaired here: an install created before YM-353 has it 0755, and the
	// listing alone names every installed cap.
	if err := capfile.EnsureDirOwnerOnly(capsDir); err != nil {
		return 0, nil, fmt.Errorf("create caps dir: %w", err)
	}

	entries, err := caps.BundledCaps.ReadDir("bundled-caps")
	if err != nil {
		return 0, nil, fmt.Errorf("read bundled caps: %w", err)
	}

	manifestPath := filepath.Join(capsDir, capManifestName)
	state, stateUsable := loadInstalledCapManifest(manifestPath)
	tracked := false
	installed := 0
	var conflicts []CapConflict

	for _, capDir := range entries {
		if !capDir.IsDir() {
			continue
		}
		name := capDir.Name()
		data, err := caps.BundledCaps.ReadFile(filepath.Join("bundled-caps", name, "CAP.md"))
		if err != nil {
			continue
		}
		targetDir := filepath.Join(capsDir, name)
		targetPath := filepath.Join(targetDir, "CAP.md")
		// Owner-only, like the file inside it: the listing names every installed cap.
		// The helper refuses a symlink, because os.Chmod would follow it (YM-353).
		if err := capfile.EnsureDirOwnerOnly(targetDir); err != nil {
			log.Printf("setup: cap dir %s: %v", targetDir, err)
			continue
		}

		bundledHash := textutil.BodyDigest(data)
		// The installation's own entry wins: it records what this installation
		// actually took. The bundled entry is the only evidence a bundle can carry
		// for an installation that predates the manifest, and it is a claim only when
		// it names a body other than the bundle itself. A self-hash says nothing
		// (that case is decided by the content comparison below), so presenting it as
		// a derivation would tell the operator the bundle descends from a body it was
		// never cut from.
		claim := state.Find(name)
		if claim == nil {
			claim = bundled.Find(name)
			if claim != nil && claim.DerivedFromHash == bundledHash {
				claim = nil
			}
		}
		// Neither the hash nor the mode repair may follow a link: a link planted at
		// this path would feed the decision foreign content and hand the repair to a
		// foreign file (YM-359). A read that fails for any other reason (a link, a
		// hard-linked body, an unreadable file) is no evidence that the bundle is the
		// successor, and such a file must not be written over either.
		existing, readErr := capfile.ReadNoFollow(targetPath)
		if readErr != nil && !os.IsNotExist(readErr) {
			log.Printf("setup: cap %s: %v (not installed)", name, readErr)
			continue
		}
		haveInstalled := readErr == nil

		switch {
		case !haveInstalled:
			// Nothing on disk: fresh install, and the bundled body is tracked from
			// here on.
		case textutil.BodyDigest(existing) == bundledHash:
			// Same body, at most a different line-ending style: the digest ignores line
			// endings and trailing newlines, so a CRLF checkout or a newline an editor
			// added is not news. It keeps everything else, case and indentation
			// included: inside a bash or python body those are the program. Nothing is
			// written, but the mode may need repairing (a CAP.md exported before
			// YM-346 carries 0644) and a cap installed before this manifest existed
			// is tracked from here on.
			if chmodErr := capfile.RepairFileOwnerOnly(targetPath); chmodErr != nil {
				log.Printf("setup: mode %s: %v", targetPath, chmodErr)
			}
			if state.Record(name, bundledHash) {
				tracked = true
			}
			continue
		case claim != nil && claim.DerivedFromHash == textutil.BodyDigest(existing):
			// The bundle is the successor of exactly this body. This is the update
			// path and it ignores the version: a live body at revision 603 is replaced
			// by a bundle carrying 195 when the bundle descends from it.
		default:
			// Either the installation holds a body the bundle does not descend from,
			// or nothing vouches for the bundle at all (a cap added to the bundle
			// under a name this installation already uses). Both are the same
			// situation: no evidence that writing would keep the local work. Writing
			// would roll back every change made after the bundle was cut, and skipping
			// it without a word is what kept six caps inert for months. So: keep the
			// body, repair its mode, report the hash pair.
			_, parseErr := capfile.Parse(existing)
			conflict := CapConflict{
				Name:          name,
				InstalledHash: textutil.BodyDigest(existing),
				Unparseable:   parseErr != nil,
			}
			if claim != nil {
				conflict.ManifestHash = claim.DerivedFromHash
			} else {
				conflict.Untracked = true
			}
			conflicts = append(conflicts, conflict)
			if chmodErr := capfile.RepairFileOwnerOnly(targetPath); chmodErr != nil {
				log.Printf("setup: mode %s: %v", targetPath, chmodErr)
			}
			continue
		}

		// Owner-only: a cap body names endpoints, paths and command lines, and the
		// daemon imports scripts from this directory (YM-346). The write refuses a
		// linked target, so the bundled body never lands outside the caps tree.
		if err := capfile.WriteOwnerOnly(targetPath, data); err != nil {
			log.Printf("setup: write %s: %v", targetPath, err)
			continue
		}
		installed++
		if state.Record(name, bundledHash) {
			tracked = true
		}
	}

	// A file that exists but did not parse is left untouched: it may hold a
	// reconciliation someone typed by hand.
	if tracked && stateUsable {
		if err := saveInstalledCapManifest(manifestPath, state); err != nil {
			log.Printf("setup: %v", err)
		}
	}
	return installed, conflicts, nil
}

// translateCommand translates a skill document to the target language via LLM.
func translateCommand(dataDir, content, langCode, apiKey, model, provider string) (string, error) {
	modelID := config.ResolveModelIDForProvider(provider, "haiku", "claude-haiku-4-5-20251001", "gpt-5-mini")

	client, err := extraction.NewLLMClient(provider, apiKey, modelID, "", "")
	if err != nil {
		return "", fmt.Errorf("llm client: %w", err)
	}
	client = redactedForDataDir(dataDir, client)

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

// ━━━ Opencode Plugin Setup ━━━

// detectOpencodeBinary returns the opencode binary path if installed, or "".
func detectOpencodeBinary() string {
	if path, err := exec.LookPath("opencode"); err == nil {
		return path
	}
	// The fallback needs a home directory. Without one there is no candidate
	// path: probing a relative ".opencode/bin/opencode" would look in the working
	// directory (YM-350).
	home, err := UserHome()
	if err != nil {
		return ""
	}
	fb := filepath.Join(home, ".opencode", "bin", "opencode")
	if _, err := os.Stat(fb); err == nil {
		return fb
	}
	return ""
}

// installOpencodePlugin prepares the plugin artifacts and the config for the
// user's opencode major version. Even when the plugin sources are unavailable,
// the provider/mcp/compaction settings are still merged.
//
// primaryModel/smallModel are threaded through to mergeOpencodeSettingsWith so the
// user's wizard choice is written in the same pass. Without this, step 7d would
// write hardcoded deepseek-reasoner defaults that step 7d2 cannot overwrite
// (deepMergeJSON preserves existing scalars).
func installOpencodePlugin(home, binaryPath, primaryModel, smallModel string) (string, error) {
	major := opencodeVersionFn()
	_, detail, installErr := installOpencodePluginFor(home, major)
	cfgErr := mergeOpencodeSettingsWith(home, primaryModel, smallModel, binaryPath)

	// Window plugin: registers which session a terminal window shows so
	// spawned agents open on the caller's desktop. It and the themes are
	// independent of the config merge, so a failed merge must not skip them.
	if _, err := InstallWindowPlugin(home); err != nil {
		fmt.Fprintf(os.Stderr, "  opencode window plugin not installed: %v\n", err)
	}
	// Agent themes: the type-accented theme family spawned agent TUIs select.
	if _, err := InstallThemes(home); err != nil {
		fmt.Fprintf(os.Stderr, "  opencode agent themes not installed: %v\n", err)
	}
	return detail, errors.Join(installErr, cfgErr)
}

// InstallOpencodePlugin installs the bundled plugin for the user's opencode
// major version and returns the number of files updated. Where the install was
// skipped by design, the reason is returned instead of a count.
func InstallOpencodePlugin(home string) (int, string, error) {
	return installOpencodePluginFor(home, opencodeVersionFn())
}

// readAuthJSONKey reads a provider API key from opencode's auth.json.
// Returns "" if the file doesn't exist or the provider isn't configured.
func readAuthJSONKey(home, provider string) string {
	authPath := filepath.Join(home, ".local", "share", "opencode", "auth.json")
	data, err := os.ReadFile(authPath)
	if err != nil {
		return ""
	}
	var auth map[string]struct {
		Type string `json:"type"`
		Key  string `json:"key"`
	}
	if err := json.Unmarshal(data, &auth); err != nil {
		return ""
	}
	if entry, ok := auth[provider]; ok {
		return entry.Key
	}
	return ""
}
