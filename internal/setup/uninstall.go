package setup

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/carsteneu/yesmem/skills"
)

// Uninstall removes all YesMem registrations and optionally data.
func Uninstall() error {
	home, _ := os.UserHomeDir()

	fmt.Println()
	fmt.Println("  YesMem Uninstall")
	fmt.Println("  ================")
	fmt.Println()

	// Show what will be removed
	fmt.Println("  This will remove:")
	fmt.Println("  ✓ MCP server registration from settings.json + claude.json")
	fmt.Println("  ✓ SessionStart hook")
	fmt.Println("  ✓ UserPromptSubmit micro-reminder")
	fmt.Println("  ✓ PreToolUse hook (gotcha warnings)")
	fmt.Println("  ✓ PostToolUseFailure hook (auto-learn)")
	fmt.Println("  ✓ cleanupPeriodDays setting")
	fmt.Println("  ✓ autoCompactEnabled (restore previous value)")
	fmt.Println("  ✓ ANTHROPIC_BASE_URL from settings.json env + shell profiles")
	fmt.Println("  ✓ Hook script (~/.claude/hooks/yesmem-briefing.sh)")
	fmt.Println("  ✓ .mcp.json entries")
	fmt.Println("  ✓ Codex provider + MCP entries in ~/.codex/config.toml")
	fmt.Println("  ✓ Codex instructions (~/.codex/instructions/yesmem.md)")
	if runtime.GOOS == "linux" {
		fmt.Println("  ✓ systemd user services (daemon + proxy)")
	} else if runtime.GOOS == "darwin" {
		fmt.Println("  ✓ launchd plists (daemon + proxy)")
	}
	fmt.Println("  ✓ yesmem binary")
	fmt.Println()

	deleteData := promptYesNo("Also delete all data (~/.claude/yesmem/)?", false)
	fmt.Println()

	if !promptYesNo("Proceed with uninstall?", false) {
		fmt.Println("  Cancelled.")
		return nil
	}
	fmt.Println()

	// Kill daemon + proxy
	fmt.Print("  Stopping daemon... ")
	exec.Command("pkill", "-f", "yesmem daemon").Run()
	fmt.Println("✓")
	fmt.Print("  Stopping proxy... ")
	exec.Command("pkill", "-f", "yesmem proxy").Run()
	fmt.Println("✓")

	// Remove from settings.json
	fmt.Print("  Cleaning settings.json... ")
	if err := cleanSettingsJSON(home); err != nil {
		fmt.Printf("⚠ %v\n", err)
	} else {
		fmt.Println("✓")
	}

	// Remove hook script
	fmt.Print("  Removing hook script... ")
	os.Remove(filepath.Join(home, ".claude", "hooks", "yesmem-briefing.sh"))
	fmt.Println("✓")

	// Remove bundled commands installed by YesMem
	fmt.Print("  Removing bundled commands... ")
	removeBundledCommands(home)
	fmt.Println("✓")

	// Remove .mcp.json entries
	fmt.Print("  Cleaning .mcp.json... ")
	cleanMCPJSON(filepath.Join(home, ".mcp.json"))
	fmt.Println("✓")

	// Remove MCP from ~/.claude.json + restore primaryApiKey
	fmt.Print("  Cleaning claude.json... ")
	dataDir := filepath.Join(home, ".claude", "yesmem")
	restorePrimaryApiKeyFromState(dataDir, home)
	if err := cleanClaudeJSON(home); err != nil {
		fmt.Printf("⚠ %v\n", err)
	} else {
		fmt.Println("✓")
	}

	fmt.Print("  Cleaning Codex config... ")
	if err := removeCodexSetup(home); err != nil {
		fmt.Printf("⚠ %v\n", err)
	} else {
		fmt.Println("✓")
	}

	// Remove ANTHROPIC_BASE_URL from shell profiles
	fmt.Print("  Cleaning shell profiles... ")
	if err := removeProxyFromShellProfiles(home); err != nil {
		fmt.Printf("⚠ %v\n", err)
	} else {
		fmt.Println("✓")
	}

	// Remove systemd/launchd
	if runtime.GOOS == "linux" {
		fmt.Print("  Removing systemd services... ")
		for _, svc := range []string{"yesmem", "yesmem-proxy"} {
			exec.Command("systemctl", "--user", "stop", svc).Run()
			exec.Command("systemctl", "--user", "disable", svc).Run()
			os.Remove(filepath.Join(home, ".config", "systemd", "user", svc+".service"))
		}
		exec.Command("systemctl", "--user", "daemon-reload").Run()
		fmt.Println("✓")
	} else if runtime.GOOS == "darwin" {
		fmt.Print("  Removing launchd plists... ")
		for _, name := range []string{"com.yesmem.daemon", "com.yesmem.proxy"} {
			plist := filepath.Join(home, "Library", "LaunchAgents", name+".plist")
			exec.Command("launchctl", "unload", plist).Run()
			os.Remove(plist)
		}
		fmt.Println("✓")
	}

	// Remove PID + socket
	fmt.Print("  Cleaning runtime files... ")
	os.Remove(filepath.Join(dataDir, "daemon.sock"))
	os.Remove(filepath.Join(dataDir, "daemon.pid"))
	fmt.Println("✓")

	// Optionally delete data
	if deleteData {
		fmt.Print("  Deleting all data... ")
		os.RemoveAll(dataDir)
		fmt.Println("✓")
		// Clean up backup file created during install
		backupPath := filepath.Join(home, ".claude.json.bak")
		if _, err := os.Stat(backupPath); err == nil {
			fmt.Print("  Removing claude.json backup... ")
			os.Remove(backupPath)
			fmt.Println("✓")
		}
	}

	// Delete binary (self-delete — works on Linux, inode stays open until process exits)
	fmt.Print("  Removing binary... ")
	binaryPath, err := os.Executable()
	if err == nil {
		binaryPath, _ = filepath.EvalSymlinks(binaryPath)
		os.Remove(binaryPath)
	}
	fmt.Println("✓")

	fmt.Println()
	fmt.Println("  ══════════════════════════════════════")
	fmt.Println("  YesMem uninstalled.")
	if !deleteData {
		fmt.Printf("  Data preserved at %s\n", dataDir)
		fmt.Println("  Run with data deletion to fully remove.")
	}
	fmt.Println("  ══════════════════════════════════════")
	fmt.Println()

	return nil
}

func cleanSettingsJSON(home string) error {
	dataDir := filepath.Join(home, ".claude", "yesmem")
	settings, err := readSettingsJSON(home)
	if err != nil {
		return err
	}

	// Remove mcpServers.yesmem
	if mcp, ok := settings["mcpServers"].(map[string]any); ok {
		delete(mcp, "yesmem")
		if len(mcp) == 0 {
			delete(settings, "mcpServers")
		}
	}

	// Remove cleanupPeriodDays (only if it's our value)
	if val, ok := settings["cleanupPeriodDays"].(float64); ok && val == 99999 {
		delete(settings, "cleanupPeriodDays")
	}

	// Restore autoCompactEnabled to pre-install state
	if state := loadPreInstallState(dataDir); state != nil {
		if prev, exists := state["autoCompactEnabled"]; exists {
			if prev == nil {
				delete(settings, "autoCompactEnabled") // was absent before install
			} else {
				settings["autoCompactEnabled"] = prev
			}
		}
	} else {
		// No install-state.json → safe default: remove the key (let Claude Code use its default)
		delete(settings, "autoCompactEnabled")
	}

	// Remove ANTHROPIC_BASE_URL from env block
	removeProxyEnvVar(settings)

	// Restore ANTHROPIC_API_KEY to pre-install value
	restoreAPIKeyFromState(dataDir, settings)

	// Remove yesmem MCP permissions
	cleanMCPPermissions(settings)

	// Remove statusLine if it's yesmem's
	cleanStatusLine(settings)

	// Remove SessionStart hook entries containing yesmem
	if hooks, ok := settings["hooks"].(map[string]any); ok {
		removeYesmemHook(hooks, "SessionStart")
		removeYesmemFromCommand(hooks, "UserPromptSubmit")
		removeYesmemHook(hooks, "PreToolUse")
		removeYesmemHook(hooks, "PostToolUseFailure")
		removeYesmemHook(hooks, "PostToolUse")
	}

	return writeSettingsJSON(home, settings)
}

// restoreAPIKeyFromState restores ANTHROPIC_API_KEY in env to pre-install value.
func restoreAPIKeyFromState(dataDir string, settings map[string]any) {
	state := loadPreInstallState(dataDir)
	env, ok := settings["env"].(map[string]any)
	if state == nil {
		if ok {
			delete(env, "ANTHROPIC_API_KEY")
		}
		return
	}
	prev, exists := state["envAPIKey"]
	if !exists {
		return
	}
	if prev == nil {
		if ok {
			delete(env, "ANTHROPIC_API_KEY")
		}
	} else if prevStr, ok := prev.(string); ok {
		if env == nil {
			env = map[string]any{}
			settings["env"] = env
		}
		env["ANTHROPIC_API_KEY"] = prevStr
	}
}

// restorePrimaryApiKeyFromState restores primaryApiKey in ~/.claude.json to pre-install value.
func restorePrimaryApiKeyFromState(dataDir, home string) {
	state := loadPreInstallState(dataDir)
	if state == nil {
		return
	}
	prev, exists := state["primaryApiKey"]
	if !exists {
		return
	}
	path := filepath.Join(home, ".claude.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var cfg map[string]any
	if json.Unmarshal(data, &cfg) != nil {
		return
	}
	if prev == nil {
		delete(cfg, "primaryApiKey")
	} else if prevStr, ok := prev.(string); ok {
		cfg["primaryApiKey"] = prevStr
	}
	if oa, ok := state["oauthAccount"]; ok && oa != nil {
		cfg["oauthAccount"] = oa
	}
	out, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return
	}
	os.WriteFile(path, out, 0600)
}

// removeYesmemHook removes entire hook entries that reference yesmem.
func removeYesmemHook(hooks map[string]any, hookName string) {
	entries, ok := hooks[hookName].([]any)
	if !ok {
		return
	}

	var filtered []any
	for _, entry := range entries {
		m, ok := entry.(map[string]any)
		if !ok {
			filtered = append(filtered, entry)
			continue
		}
		isYesmem := false
		for _, h := range toHookSlice(m["hooks"]) {
			if cmd, ok := h["command"].(string); ok && strings.Contains(cmd, "yesmem") {
				isYesmem = true
			}
		}
		if !isYesmem {
			filtered = append(filtered, entry)
		}
	}

	if len(filtered) == 0 {
		delete(hooks, hookName)
	} else {
		hooks[hookName] = filtered
	}
}

// removeYesmemFromCommand removes yesmem from chained commands ("; yesmem micro-reminder").
func removeYesmemFromCommand(hooks map[string]any, hookName string) {
	entries, ok := hooks[hookName].([]any)
	if !ok {
		return
	}

	for _, entry := range entries {
		m, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		hooksList := toHookSlice(m["hooks"])
		for i, h := range hooksList {
			cmd, ok := h["command"].(string)
			if !ok {
				continue
			}
			// Remove "; /path/to/yesmem micro-reminder" from command chain
			parts := strings.Split(cmd, ";")
			var cleaned []string
			for _, p := range parts {
				if !strings.Contains(strings.TrimSpace(p), "yesmem") {
					cleaned = append(cleaned, p)
				}
			}
			hooksList[i]["command"] = strings.Join(cleaned, ";")
		}
		m["hooks"] = toAnySlice(hooksList)
	}
}

func cleanMCPJSON(path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var cfg map[string]any
	if json.Unmarshal(data, &cfg) != nil {
		return
	}
	if mcp, ok := cfg["mcpServers"].(map[string]any); ok {
		delete(mcp, "yesmem")
		if len(mcp) == 0 {
			os.Remove(path)
			return
		}
	}
	out, _ := json.MarshalIndent(cfg, "", "  ")
	os.WriteFile(path, out, 0644)
}

// cleanClaudeJSON removes the yesmem MCP entry from ~/.claude.json.
func cleanClaudeJSON(home string) error {
	path := filepath.Join(home, ".claude.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var config map[string]any
	if err := json.Unmarshal(data, &config); err != nil {
		return fmt.Errorf("parse claude.json: %w", err)
	}
	mcpServers, ok := config["mcpServers"].(map[string]any)
	if !ok {
		return nil
	}
	delete(mcpServers, "yesmem")
	if len(mcpServers) == 0 {
		delete(config, "mcpServers")
	}
	out, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, out, 0600)
}

// removeProxyFromShellProfiles removes the ANTHROPIC_BASE_URL export lines
// and YesMem PATH entries that were added during install.
func removeProxyFromShellProfiles(home string) error {
	for _, rcFile := range []string{".bashrc", ".zshrc", ".profile"} {
		rcPath := filepath.Join(home, rcFile)
		data, err := os.ReadFile(rcPath)
		if err != nil {
			continue
		}
		content := string(data)
		if !strings.Contains(content, "ANTHROPIC_BASE_URL") && !strings.Contains(content, "# YesMem binary") {
			continue
		}
		lines := strings.Split(content, "\n")
		var cleaned []string
		for i := 0; i < len(lines); i++ {
			trimmed := strings.TrimSpace(lines[i])
			// Skip the proxy comment line and the export line
			if trimmed == "# YesMem infinite-thread proxy" {
				// Also skip the next line if it's the export
				if i+1 < len(lines) && strings.Contains(lines[i+1], "ANTHROPIC_BASE_URL") {
					i++
				}
				continue
			}
			// Catch standalone export line (without preceding comment)
			if strings.Contains(trimmed, "export ANTHROPIC_BASE_URL=http://localhost:9099") {
				continue
			}
			// Skip the PATH comment line and the export line
			if trimmed == "# YesMem binary" {
				if i+1 < len(lines) && strings.Contains(lines[i+1], "export PATH=") {
					i++
				}
				continue
			}
			cleaned = append(cleaned, lines[i])
		}
		// Remove trailing empty lines that were left behind
		for len(cleaned) > 0 && strings.TrimSpace(cleaned[len(cleaned)-1]) == "" {
			cleaned = cleaned[:len(cleaned)-1]
		}
		cleaned = append(cleaned, "") // ensure final newline
		if err := os.WriteFile(rcPath, []byte(strings.Join(cleaned, "\n")), 0644); err != nil {
			return fmt.Errorf("write %s: %w", rcFile, err)
		}
	}
	return nil
}

// removeBundledCommands removes YesMem-installed skill files from ~/.claude/commands/.
// Only removes files that match names in the embedded skills directory.
func removeBundledCommands(home string) {
	cmdDir := filepath.Join(home, ".claude", "commands")
	entries, err := skills.BundledCommands.ReadDir(".")
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		os.Remove(filepath.Join(cmdDir, e.Name()))
	}
}

// mcpToolNames lists all yesmem MCP tool names for permission cleanup.
var mcpToolNames = []string{
	"search", "deep_search", "hybrid_search", "remember", "resolve", "resolve_by_text",
	"get_learnings", "get_session", "get_compacted_stubs", "get_project_profile",
	"get_self_feedback", "get_persona", "set_persona", "get_pins", "pin", "unpin",
	"get_coverage", "related_to_file", "list_projects", "project_summary",
	"scratchpad_write", "scratchpad_read", "scratchpad_list", "scratchpad_delete",
	"ingest_docs", "docs_search", "list_docs", "remove_docs",
	"send_to", "broadcast", "spawn_agent", "stop_agent", "stop_all_agents",
	"list_agents", "get_agent", "relay_agent", "resume_agent",
	"set_plan", "update_plan", "get_plan", "complete_plan",
	"set_config", "get_config", "expand_context", "whoami",
	"update_agent_status", "skip_indexing", "quarantine_session",
	"query_facts", "relate_learnings",
}

// cleanMCPPermissions removes all mcp__yesmem__* entries from permissions.allow.
func cleanMCPPermissions(settings map[string]any) {
	perms, ok := settings["permissions"].(map[string]any)
	if !ok {
		return
	}
	allow, ok := perms["allow"].([]any)
	if !ok {
		return
	}
	cleaned := make([]any, 0)
	for _, v := range allow {
		s, ok := v.(string)
		if !ok || !strings.HasPrefix(s, "mcp__yesmem__") {
			cleaned = append(cleaned, v)
		}
	}
	perms["allow"] = cleaned
}

// cleanStatusLine removes the statusLine entry if it belongs to yesmem.
func cleanStatusLine(settings map[string]any) {
	sl, ok := settings["statusLine"].(map[string]any)
	if !ok {
		return
	}
	cmd, _ := sl["command"].(string)
	if strings.Contains(cmd, "yesmem") {
		delete(settings, "statusLine")
	}
}
