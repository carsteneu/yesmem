package setup

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
)

var reader = bufio.NewReader(os.Stdin)

// promptUserType asks whether the install should use the Claude Code subscription
// (via `claude` CLI) or a direct Anthropic API key. Returns "cli" or "api".
// defaultType selects which option is pre-highlighted ("cli" or "api").
func promptUserType(defaultType string) string {
	defaultIdx := 0
	if defaultType == "api" {
		defaultIdx = 1
	}
	fmt.Println("  YesMem needs an LLM for knowledge extraction.")
	fmt.Println("  How should it authenticate?")
	fmt.Println()
	idx := promptChoice([]string{
		"Claude Code subscription — uses `claude` CLI, no separate key",
		"Anthropic API key — direct API, separate billing",
	}, defaultIdx)
	if idx == 1 {
		return "api"
	}
	return "cli"
}

// promptYesNo asks a yes/no question with a default.
func promptYesNo(question string, defaultYes bool) bool {
	hint := "[Y/n]"
	if !defaultYes {
		hint = "[y/N]"
	}
	fmt.Printf("  %s %s: ", question, hint)

	input, _ := reader.ReadString('\n')
	input = strings.TrimSpace(strings.ToLower(input))

	if input == "" {
		return defaultYes
	}
	return input == "y" || input == "yes" || input == "j" || input == "ja"
}

// promptChoice asks the user to choose from numbered options.
func promptChoice(options []string, defaultIdx int) int {
	for i, opt := range options {
		marker := "  "
		if i == defaultIdx {
			marker = "→ "
		}
		fmt.Printf("  %s%d. %s\n", marker, i+1, opt)
	}
	fmt.Printf("  Choose [1-%d, default=%d]: ", len(options), defaultIdx+1)

	input, _ := reader.ReadString('\n')
	input = strings.TrimSpace(input)

	if input == "" {
		return defaultIdx
	}

	n, err := strconv.Atoi(input)
	if err != nil || n < 1 || n > len(options) {
		return defaultIdx
	}
	return n - 1
}

// promptAPIKey asks for an Anthropic API key with validation.
// Only called when user chose "Anthropic API" provider.
func promptAPIKey(existing string) string {
	if existing != "" {
		masked := existing[:10] + "..." + existing[len(existing)-4:]
		fmt.Printf("  Found API key in $ANTHROPIC_API_KEY: %s\n", masked)
		fmt.Println()
		choice := promptChoice([]string{"Keep this key", "Enter a different key"}, 0)
		if choice == 0 {
			fmt.Println("  ✓ Using existing key")
			return existing
		}
	}

	fmt.Println("  Enter your API key from platform.claude.com")
	fmt.Println("  → https://platform.claude.com/dashboard/api-keys")
	fmt.Println()
	fmt.Println("  YesMem uninstall will restore your previous authentication.")
	fmt.Println()

	for {
		key := promptString("API key", "")
		if key == "" {
			if promptYesNo("Continue without API key?", false) {
				return ""
			}
			continue
		}
		if !strings.HasPrefix(key, "sk-ant-") {
			fmt.Println("  ✗ Invalid format — key must start with 'sk-ant-'")
			continue
		}
		fmt.Println("  ✓ Key accepted")
		return key
	}
}

// promptAPIKeyWithLabel asks for an API key with a provider-specific label.
func promptAPIKeyWithLabel(providerLabel, existing string) string {
	if existing != "" {
		masked := existing
		if len(existing) > 14 {
			masked = existing[:10] + "..." + existing[len(existing)-4:]
		}
		fmt.Printf("  Found API key in env: %s\n", masked)
		fmt.Println()
		choice := promptChoice([]string{"Keep this key", "Enter a different key"}, 0)
		if choice == 0 {
			fmt.Println("  ✓ Using existing key")
			return existing
		}
	}

	fmt.Printf("  Enter your %s API key:\n", providerLabel)
	fmt.Println()

	for {
		key := promptString("API key", "")
		if key == "" {
			if promptYesNo("Continue without API key?", false) {
				return ""
			}
			continue
		}
		fmt.Println("  ✓ Key accepted")
		return key
	}
}

// promptBaseURL asks for an OpenAI-compatible base URL.
func promptBaseURL() string {
	fmt.Println("  Enter the base URL of your OpenAI-compatible endpoint:")
	fmt.Println("  (e.g. https://api.together.xyz/v1 or http://localhost:11434/v1)")
	fmt.Println()
	return promptString("Base URL", "")
}

// promptString asks for a string input with a default value.
func promptString(question, defaultVal string) string {
	if defaultVal != "" {
		fmt.Printf("  %s [%s]: ", question, defaultVal)
	} else {
		fmt.Printf("  %s: ", question)
	}

	input, _ := reader.ReadString('\n')
	input = strings.TrimSpace(input)

	if input == "" {
		return defaultVal
	}
	return input
}

// withSpinner runs fn while showing ⏳ label..., then replaces with ✓ or ⚠.
// fn returns an optional detail string (shown in parens) and an error.
func withSpinner(label string, fn func() (string, error)) {
	fmt.Printf("  ⏳ %s...", label)
	detail, err := fn()
	fmt.Printf("\r\033[2K")
	if err != nil {
		fmt.Printf("  ⚠ %s: %v\n", label, err)
	} else if detail != "" {
		fmt.Printf("  ✓ %s (%s)\n", label, detail)
	} else {
		fmt.Printf("  ✓ %s\n", label)
	}
}
