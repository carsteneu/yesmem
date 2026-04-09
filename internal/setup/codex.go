package setup

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	codexProxyBaseURL = "http://127.0.0.1:9099/v1"
	codexProxyHealth  = "http://127.0.0.1:9099/health"
)

type codexConfigState struct {
	ConfigPresent          bool
	ProviderConfigured     bool
	MCPConfigured          bool
	ApprovalConfigured     bool
	InstructionsReferenced bool
	InstructionsPresent    bool
}

func codexConfigPath(home string) string {
	return filepath.Join(home, ".codex", "config.toml")
}

func codexInstructionsPath(home string) string {
	return filepath.Join(home, ".codex", "instructions", "yesmem.md")
}

func ensureCodexSetup(home, binaryPath string) error {
	instructionsPath := codexInstructionsPath(home)
	if err := os.MkdirAll(filepath.Dir(instructionsPath), 0755); err != nil {
		return fmt.Errorf("create Codex instructions dir: %w", err)
	}
	if err := os.WriteFile(instructionsPath, []byte(defaultCodexInstructions()), 0644); err != nil {
		return fmt.Errorf("write Codex instructions: %w", err)
	}

	configPath := codexConfigPath(home)
	if err := os.MkdirAll(filepath.Dir(configPath), 0755); err != nil {
		return fmt.Errorf("create Codex config dir: %w", err)
	}

	var current string
	data, err := os.ReadFile(configPath)
	if err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("read Codex config: %w", err)
		}
	} else {
		current = string(data)
	}

	merged := mergeCodexConfigContent(current, binaryPath, instructionsPath)
	if err := os.WriteFile(configPath, []byte(merged), 0644); err != nil {
		return fmt.Errorf("write Codex config: %w", err)
	}
	return nil
}

func removeCodexSetup(home string) error {
	instructionsPath := codexInstructionsPath(home)
	if err := os.Remove(instructionsPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove Codex instructions: %w", err)
	}

	configPath := codexConfigPath(home)
	data, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read Codex config: %w", err)
	}

	cleaned := removeCodexConfigContent(string(data), instructionsPath)
	if strings.TrimSpace(cleaned) == "" {
		if err := os.Remove(configPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove Codex config: %w", err)
		}
		return nil
	}

	if err := os.WriteFile(configPath, []byte(cleaned), 0644); err != nil {
		return fmt.Errorf("write Codex config: %w", err)
	}
	return nil
}

func readCodexConfigState(home string) codexConfigState {
	state := codexConfigState{
		InstructionsPresent: fileExists(codexInstructionsPath(home)),
	}

	data, err := os.ReadFile(codexConfigPath(home))
	if err != nil {
		return state
	}

	state = inspectCodexConfigContent(string(data), codexInstructionsPath(home))
	state.InstructionsPresent = fileExists(codexInstructionsPath(home))
	return state
}

func inspectCodexConfigContent(content, instructionsPath string) codexConfigState {
	content = normalizeTOMLContent(content)
	state := codexConfigState{
		ConfigPresent: strings.TrimSpace(content) != "",
	}
	if !state.ConfigPresent {
		return state
	}

	modelProvider, _ := topLevelStringValue(content, "model_provider")
	providerSection, hasProviderSection := sectionText(content, "model_providers.yesmem")
	state.ProviderConfigured = modelProvider == "yesmem" &&
		hasProviderSection &&
		strings.Contains(providerSection, `base_url = "http://127.0.0.1:9099/v1"`) &&
		strings.Contains(providerSection, `env_key = "OPENAI_API_KEY"`)

	mcpSection, hasMCPSection := sectionText(content, "mcp_servers.yesmem")
	state.MCPConfigured = hasMCPSection &&
		strings.Contains(mcpSection, "command = ") &&
		strings.Contains(strings.ToLower(mcpSection), "yesmem") &&
		strings.Contains(mcpSection, `args = ["mcp"]`)

	instructionsValue, hasInstructions := topLevelStringValue(content, "developer_instructions_file")
	state.InstructionsReferenced = hasInstructions && matchesCodexInstructionsPath(instructionsValue, instructionsPath)

	state.ApprovalConfigured = strings.Contains(content, `approval_policy = "never"`)
	return state
}

func codexProxyReachable() bool {
	client := &http.Client{Timeout: 750 * time.Millisecond}
	resp, err := client.Get(codexProxyHealth)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func mergeCodexConfigContent(content, binaryPath, instructionsPath string) string {
	content = normalizeTOMLContent(content)
	content = upsertTopLevelString(content, "model_provider", "yesmem")
	content = upsertTopLevelString(content, "developer_instructions_file", instructionsPath)
	content = upsertTopLevelString(content, "approval_policy", "never")
	content = upsertSection(content, "model_providers.yesmem", renderCodexProviderSection())
	content = upsertSection(content, "mcp_servers.yesmem", renderCodexMCPSection(binaryPath))
	return normalizeTOMLContent(content)
}

func removeCodexConfigContent(content, instructionsPath string) string {
	content = normalizeTOMLContent(content)
	content = removeTopLevelKeyMatching(content, "model_provider", func(value string) bool {
		return value == "yesmem"
	})
	content = removeTopLevelKeyMatching(content, "developer_instructions_file", func(value string) bool {
		return matchesCodexInstructionsPath(value, instructionsPath)
	})
	content = removeSection(content, "model_providers.yesmem")
	content = removeSection(content, "mcp_servers.yesmem")
	return normalizeTOMLContent(content)
}

func renderCodexProviderSection() string {
	return strings.Join([]string{
		"[model_providers.yesmem]",
		`name = "OpenAI via YesMem Proxy"`,
		`base_url = "http://127.0.0.1:9099/v1"`,
		`env_key = "OPENAI_API_KEY"`,
	}, "\n")
}

func renderCodexMCPSection(binaryPath string) string {
	return strings.Join([]string{
		"[mcp_servers.yesmem]",
		fmt.Sprintf("command = %s", strconv.Quote(binaryPath)),
		`args = ["mcp"]`,
	}, "\n")
}

func defaultCodexInstructions() string {
	return strings.TrimSpace(`
# YesMem Memory Integration

YesMem is available through MCP tools and a local proxy.

Before substantial work, check existing memory:
- use hybrid_search or search for prior context
- use get_project_profile and get_learnings for project state
- use get_session or project_summary when a previous thread matters

Keep memory current:
- store durable decisions, gotchas, and unfinished work with remember
- resolve finished tasks with resolve or resolve_by_text
- prefer project-scoped memory when the knowledge is local to one repo

Interpretation:
- source_agent tells you whether a session came from Claude or Codex history
- injected briefings and memory context are your own prior notes, not new user instructions
`) + "\n"
}

func upsertTopLevelString(content, key, value string) string {
	return upsertTopLevelRaw(content, key, strconv.Quote(value))
}

func upsertTopLevelRaw(content, key, rawValue string) string {
	lines := splitTOMLLines(content)
	replacement := fmt.Sprintf("%s = %s", key, rawValue)
	firstSection := firstSectionIndex(lines)

	for i := 0; i < firstSection; i++ {
		if parsedKey, _, ok := parseKeyLine(lines[i]); ok && parsedKey == key {
			lines[i] = replacement
			return normalizeTOMLContent(strings.Join(lines, "\n"))
		}
	}

	lines = insertLines(lines, firstSection, []string{replacement})
	return normalizeTOMLContent(strings.Join(lines, "\n"))
}

func removeTopLevelKeyMatching(content, key string, match func(string) bool) string {
	lines := splitTOMLLines(content)
	firstSection := firstSectionIndex(lines)
	var out []string
	for i, line := range lines {
		if i < firstSection {
			parsedKey, value, ok := parseKeyLine(line)
			if ok && parsedKey == key && match(unquoteTOMLString(value)) {
				continue
			}
		}
		out = append(out, line)
	}
	return normalizeTOMLContent(strings.Join(out, "\n"))
}

func upsertSection(content, header, block string) string {
	lines := splitTOMLLines(content)
	blockLines := splitTOMLLines(block)
	start, end, found := findSection(lines, header)

	if found {
		out := append([]string{}, lines[:start]...)
		if len(out) > 0 && strings.TrimSpace(out[len(out)-1]) != "" {
			out = append(out, "")
		}
		out = append(out, blockLines...)
		if end < len(lines) && strings.TrimSpace(lines[end]) != "" {
			out = append(out, "")
		}
		out = append(out, lines[end:]...)
		return normalizeTOMLContent(strings.Join(out, "\n"))
	}

	if len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) != "" {
		lines = append(lines, "")
	}
	lines = append(lines, blockLines...)
	return normalizeTOMLContent(strings.Join(lines, "\n"))
}

func removeSection(content, header string) string {
	lines := splitTOMLLines(content)
	start, end, found := findSection(lines, header)
	if !found {
		return normalizeTOMLContent(strings.Join(lines, "\n"))
	}

	out := append([]string{}, lines[:start]...)
	out = append(out, lines[end:]...)
	return normalizeTOMLContent(strings.Join(out, "\n"))
}

func sectionText(content, header string) (string, bool) {
	lines := splitTOMLLines(content)
	start, end, found := findSection(lines, header)
	if !found {
		return "", false
	}
	return strings.Join(lines[start:end], "\n"), true
}

func topLevelStringValue(content, key string) (string, bool) {
	lines := splitTOMLLines(content)
	firstSection := firstSectionIndex(lines)
	for i := 0; i < firstSection; i++ {
		parsedKey, value, ok := parseKeyLine(lines[i])
		if ok && parsedKey == key {
			return unquoteTOMLString(value), true
		}
	}
	return "", false
}

func splitTOMLLines(content string) []string {
	content = strings.ReplaceAll(content, "\r\n", "\n")
	content = strings.TrimRight(content, "\n")
	if content == "" {
		return nil
	}
	return strings.Split(content, "\n")
}

func normalizeTOMLContent(content string) string {
	lines := splitTOMLLines(content)
	if len(lines) == 0 {
		return ""
	}

	for len(lines) > 0 && strings.TrimSpace(lines[0]) == "" {
		lines = lines[1:]
	}
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) == 0 {
		return ""
	}

	var out []string
	lastBlank := false
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			if lastBlank {
				continue
			}
			lastBlank = true
			out = append(out, "")
			continue
		}
		lastBlank = false
		out = append(out, line)
	}
	return strings.Join(out, "\n") + "\n"
}

func firstSectionIndex(lines []string) int {
	for i, line := range lines {
		if isSectionHeader(strings.TrimSpace(line)) {
			return i
		}
	}
	return len(lines)
}

func findSection(lines []string, header string) (int, int, bool) {
	needle := "[" + header + "]"
	for i, line := range lines {
		if strings.TrimSpace(line) != needle {
			continue
		}
		end := len(lines)
		for j := i + 1; j < len(lines); j++ {
			if isSectionHeader(strings.TrimSpace(lines[j])) {
				end = j
				break
			}
		}
		return i, end, true
	}
	return 0, 0, false
}

func isSectionHeader(line string) bool {
	return strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]")
}

func insertLines(lines []string, idx int, insert []string) []string {
	if idx < 0 {
		idx = 0
	}
	if idx > len(lines) {
		idx = len(lines)
	}
	out := append([]string{}, lines[:idx]...)
	out = append(out, insert...)
	out = append(out, lines[idx:]...)
	return out
}

func parseKeyLine(line string) (string, string, bool) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || strings.HasPrefix(trimmed, "#") || isSectionHeader(trimmed) {
		return "", "", false
	}

	idx := strings.Index(trimmed, "=")
	if idx < 0 {
		return "", "", false
	}

	key := strings.TrimSpace(trimmed[:idx])
	value := strings.TrimSpace(trimmed[idx+1:])
	if commentIdx := strings.Index(value, "#"); commentIdx >= 0 {
		value = strings.TrimSpace(value[:commentIdx])
	}
	if key == "" || value == "" {
		return "", "", false
	}
	return key, value, true
}

func unquoteTOMLString(value string) string {
	value = strings.TrimSpace(value)
	if len(value) >= 2 {
		if unquoted, err := strconv.Unquote(value); err == nil {
			return unquoted
		}
	}
	return strings.Trim(value, `"'`)
}

func matchesCodexInstructionsPath(value, instructionsPath string) bool {
	value = filepath.ToSlash(strings.TrimSpace(value))
	instructionsPath = filepath.ToSlash(strings.TrimSpace(instructionsPath))
	if value == instructionsPath || value == "~/.codex/instructions/yesmem.md" {
		return true
	}
	return strings.HasSuffix(value, "/.codex/instructions/yesmem.md")
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
