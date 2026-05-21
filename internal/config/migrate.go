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
  #   think_reminder  = Inject hybrid_search() hint (check memory before assuming)
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

	if added == 0 {
		return 0, nil
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
