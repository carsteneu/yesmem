package config

import (
	"fmt"
	"os"
	"strings"
)

type configMigration struct {
	key     string
	snippet string
}

var proxyMigrations = []configMigration{
	{
		key: "skill_eval_inject",
		snippet: `
  # Skill evaluation injection mode.
  # "true" = forced visible evaluation every turn (verbose)
  # "silent" = evaluate internally, output only on skill match (default)
  # "false" = disable skill-eval injection entirely
  skill_eval_inject: "silent"
`,
	},
	{
		key: "effort_floor",
		snippet: `
  # Minimum effort level for model responses.
  # Options: "" (off), "low", "medium", "high", "max"
  # effort_floor: ""
`,
	},
	{
		key: "auto_configure_providers",
		snippet: `
    # Automatically discover and configure provider routing from opencode config.
    auto_configure_providers: true
`,
	},
}

const opencodeDBKey = "opencode_db"

const opencodeDBSnippet = `
  # Path to opencode's SQLite database for session indexing.
  # Default: ~/.local/share/opencode/opencode.db
  opencode_db: ~/.local/share/opencode/opencode.db
`

const modelFeaturesBlock = `
  # --- Per-Model Feature Gates ---
  # Control which yesmem behavioral features are active per model/provider.
  # Keys are model name prefixes matched case-insensitively (longest wins).
  # Models not listed fall back to feature_defaults.
  #
  # Gate reference:
  #   skill_eval      = Inject [skill-eval] block — checks which skills apply to the task
  #   briefing        = Inject yesmem briefing at session start (learnings, recent sessions)
  #   rules_reminder  = Periodic reminder of project rules/guidelines from CLAUDE.md/OPENCODE.md
  #   plan_checkpoint = Inject plan checkpoint reminders during long implementation sessions
  #   think_reminder       = Inject hybrid_search() hint (check memory before assuming)
  #   think_reminder_min_chars = Min user text length to trigger reminder (0=always)
  model_features:
    claude:
      skill_eval: true
      briefing: true
      rules_reminder: true
      plan_checkpoint: true
      think_reminder: true
    deepseek:
      skill_eval: true
      briefing: true
      think_reminder: true
      think_reminder_min_chars: 10
      rules_reminder: true
    gpt:
      skill_eval: true
      briefing: true
      think_reminder: false
      rules_reminder: true
    openai:
      skill_eval: true
      briefing: true
      think_reminder: false
      rules_reminder: true

  feature_defaults:
    # Fallback for models not listed above.
    # Defaults: all on — new models get full features until proven otherwise.
    skill_eval: true
    briefing: true
    rules_reminder: true
    plan_checkpoint: true
    think_reminder: true
`

const deepseekPricingSnippet = `
    deepseek-v4-flash: { input: 0.14, output: 0.56 }
    deepseek-v4-pro:   { input: 0.28, output: 1.12 }
`

// MigrateConfig reads an existing config.yaml and inserts any missing
// proxy-section fields, paths fields, and model_features section.
// Returns the number of fields/sections added.
func MigrateConfig(path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, fmt.Errorf("read config: %w", err)
	}

	content := string(data)
	added := 0

	// ━━ Proxy-section migrations ━━
	if strings.Contains(content, "proxy:") {
		var toAdd []string
		for _, m := range proxyMigrations {
			if strings.Contains(content, m.key) {
				continue
			}
			toAdd = append(toAdd, m.snippet)
		}
		if len(toAdd) > 0 {
			content = insertAtEndOfSection(content, "proxy:", strings.Join(toAdd, ""))
			added += len(toAdd)
		}
	}

	// ━━ Paths-section: opencode_db ━━
	if !strings.Contains(content, opencodeDBKey) {
		if strings.Contains(content, "paths:") {
			content = insertAtEndOfSection(content, "paths:", opencodeDBSnippet)
		} else {
			content = appendToEnd(content, "\npaths:"+opencodeDBSnippet)
		}
		added++
	}

	// ━━ model_features section (inside proxy:) ━━
	if !strings.Contains(content, "model_features:") {
		if strings.Contains(content, "proxy:") {
			content = insertAtEndOfSection(content, "proxy:", modelFeaturesBlock)
		} else {
			content = appendToEnd(content, "\nproxy:\n  enabled: true"+modelFeaturesBlock)
		}
		added++
	}

	// ━━ pricing section: deepseek entries ━━
	if !strings.Contains(content, "deepseek-v4-flash") {
		if strings.Contains(content, "pricing:") {
			content = insertAtEndOfSection(content, "pricing:", deepseekPricingSnippet)
			added++
		}
	}

	// ━━ agents section: default_backend ━━
	if !strings.Contains(content, "default_backend:") && strings.Contains(content, "agents:") {
		backend := "claude"
		if strings.Contains(content, "provider: openai_compatible") || strings.Contains(content, "provider: opencode") {
			backend = "opencode"
		}
		snippet := fmt.Sprintf(`
  # Default backend for spawned agents: claude or opencode
  default_backend: %s
`, backend)
		content = insertAtEndOfSection(content, "agents:", snippet)
		added++
	}

	// ━━ exclude_projects (top-level) ━━
	if !strings.Contains(content, "exclude_projects:") {
		user := os.Getenv("USER")
		if user == "" {
			user = os.Getenv("USERNAME") // Windows fallback
		}
		if user == "" {
			user = "user"
		}
		snippet := fmt.Sprintf(`
# --- Indexer ---
# Directories excluded from session indexing.
# Prevents home/tmp directories from accumulating internal sessions.
exclude_projects:
  - /home/%s
  - /tmp
`, user)
		content = appendToEnd(content, snippet)
		added++
	}

	// ━━ Missing top-level sections ━━
	if !strings.Contains(content, "caps_dir:") {
		content = appendToEnd(content, `
# --- Caps Directory ---
# Custom directory for capability files (CAP.md). Empty = use ~/.claude/caps/.
caps_dir: ""
`)
		added++
	}
	if !strings.Contains(content, "default_sandbox_profile:") {
		content = appendToEnd(content, `
# --- Sandbox ---
# Default sandbox profile for spawned agents.
default_sandbox_profile: ""
`)
		added++
	}
	if !strings.Contains(content, "secrets_sanitization:") {
		content = appendToEnd(content, `
# --- Secrets Sanitization ---
# Redact secrets from extraction content.
secrets_sanitization:
  enabled: false
  allowed_exceptions: []
`)
		added++
	}
	if !strings.Contains(content, "http:") {
		content = appendToEnd(content, `
# --- HTTP Server (optional) ---
http:
  enabled: false
  listen: "127.0.0.1:9377"
  auth_token: ""
`)
		added++
	}

	// ━━ Missing forked_agents fields ━━
	if strings.Contains(content, "forked_agents:") {
		var faAdds []string
		if !strings.Contains(content, "max_forks_per_session:") {
			faAdds = append(faAdds, "  max_forks_per_session: 50")
		}
		if !strings.Contains(content, "max_cost_per_session:") {
			faAdds = append(faAdds, "  max_cost_per_session: 5")
		}
		if len(faAdds) > 0 {
			content = insertAtEndOfSection(content, "forked_agents:", strings.Join(faAdds, "\n"))
			added += len(faAdds)
		}
	}

	// ━━ Missing proxy fields ━━
	if strings.Contains(content, "proxy:") {
		var pxAdds []string
		if !strings.Contains(content, "openai_target:") {
			pxAdds = append(pxAdds, "  openai_target: \"https://api.openai.com\"")
		}
		if !strings.Contains(content, "reset_cache:") {
			pxAdds = append(pxAdds, "  reset_cache: false")
		}
		if !strings.Contains(content, "cache_keepalive_min_messages:") {
			pxAdds = append(pxAdds, "  cache_keepalive_min_messages: 10")
		}
		if len(pxAdds) > 0 {
			content = insertAtEndOfSection(content, "proxy:", strings.Join(pxAdds, "\n"))
			added += len(pxAdds)
		}
	}

	// ━━ token_thresholds: deepseek/glm-5.2 ━━
	if strings.Contains(content, "token_thresholds:") {
		if !strings.Contains(content, "deepseek:") {
			content = insertAtEndOfSection(content, "token_thresholds:", "    deepseek: 600000")
			added++
		}
		if !strings.Contains(content, "glm-5.2:") {
			content = insertAtEndOfSection(content, "token_thresholds:", "    glm-5.2: 500000")
			added++
		}
	}

	// ━━ think_reminder_min_chars — inject into deepseek model_features ━━
	if strings.Contains(content, "model_features:") && !strings.Contains(content, "think_reminder_min_chars") {
		// Insert after the last "think_reminder: true" line inside a deepseek section
		content = injectThinkReminderMinChars(content, "deepseek:", "10")
		if !strings.Contains(content, "think_reminder_min_chars") {
			// fallback: also try at feature_defaults level with value 0
			content = injectThinkReminderMinChars(content, "feature_defaults:", "0")
		}
		if strings.Contains(content, "think_reminder_min_chars") {
			added++
		}
	}

	if added == 0 {
		return 0, nil
	}

	if err := backupFile(path); err != nil {
		return 0, fmt.Errorf("backup config: %w", err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		return 0, fmt.Errorf("write config: %w", err)
	}

	return added, nil
}

// insertAtEndOfSection inserts snippet at the end of a YAML section (before the next top-level key).
func insertAtEndOfSection(content, sectionKey, snippet string) string {
	lines := strings.Split(content, "\n")
	insertIdx := -1
	inSection := false
	for i, line := range lines {
		if strings.HasPrefix(line, sectionKey) {
			inSection = true
			continue
		}
		if inSection && len(line) > 0 && line[0] != ' ' && line[0] != '#' && line[0] != '\t' {
			insertIdx = i
			break
		}
	}

	if insertIdx >= 0 {
		before := strings.Join(lines[:insertIdx], "\n")
		after := strings.Join(lines[insertIdx:], "\n")
		return before + snippet + after
	}
	return content + snippet
}

// appendToEnd appends snippet to the end of the content.
func appendToEnd(content, snippet string) string {
	content = strings.TrimRight(content, "\n")
	return content + "\n" + snippet
}

// injectThinkReminderMinChars inserts think_reminder_min_chars: <value> after the
// last think_reminder line inside a model_features sub-section (e.g., "deepseek:").
// Returns unchanged content if the field already exists or the target section is not found.
func injectThinkReminderMinChars(content, targetSection, value string) string {
	lines := strings.Split(content, "\n")
	inSection := false
	sectionIndent := -1
	lastThinkLine := -1

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == targetSection {
			inSection = true
			sectionIndent = len(line) - len(strings.TrimLeft(line, " \t"))
			continue
		}
		if inSection {
			indent := 0
			if len(line) > 0 && (line[0] == ' ' || line[0] == '\t') {
				indent = len(line) - len(strings.TrimLeft(line, " \t"))
			} else if len(strings.TrimSpace(line)) > 0 && !strings.HasPrefix(strings.TrimSpace(line), "#") {
				// new top-level or peer key — left the section
				break
			}
			if indent <= sectionIndent && len(strings.TrimSpace(line)) > 0 && !strings.HasPrefix(strings.TrimSpace(line), "#") {
				break
			}
			if strings.HasPrefix(trimmed, "think_reminder:") {
				lastThinkLine = i
			}
			if strings.HasPrefix(trimmed, "think_reminder_min_chars:") {
				return content // already exists
			}
		}
	}

	if lastThinkLine < 0 {
		return content
	}

	// Use the same indentation as the think_reminder line
	thinkIndent := ""
	thinkLine := lines[lastThinkLine]
	for _, ch := range thinkLine {
		if ch == ' ' || ch == '\t' {
			thinkIndent += string(ch)
		} else {
			break
		}
	}

	newLine := thinkIndent + "think_reminder_min_chars: " + value
	lines = append(lines[:lastThinkLine+1], append([]string{newLine}, lines[lastThinkLine+1:]...)...)
	return strings.Join(lines, "\n")
}
