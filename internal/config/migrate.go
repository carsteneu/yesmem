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
  #   skill_eval               = Inject [skill-eval] block — checks which skills apply
  #   briefing                 = Inject yesmem briefing at session start
  #   rules_reminder           = Periodic reminder of project rules/guidelines
  #   plan_checkpoint          = Inject plan checkpoint reminders
  #   think_reminder           = Inject hybrid_search() hint (check memory before assuming)
  #   think_reminder_min_chars = Min user text length to trigger reminder (0=always)
  #   timestamps               = Inject [HH:MM:SS] [msg:N] [+Δ] markers
  #   assoc_context            = Inject [assoc-context] from hybrid_search (frozen per msg:N)
  #   loop_warning             = Inject [loop-warning] when loop detection fires
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

  feature_defaults:
    # Fallback for models not listed above.
    # Most features on; assoc_context off by default (injection quality varies per model).
    assoc_context: false
    briefing: true
    loop_warning: true
    plan_checkpoint: true
    rules_reminder: true
    skill_eval: true
    think_reminder: true
    think_reminder_min_chars: 0
    timestamps: true
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

	// ━━ Per-key gate migration: fill missing gate keys in existing model_features entries and feature_defaults ━━
	content, gatesAdded := migrateModelFeaturesGates(content)
	added += gatesAdded

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
// Newline-safe: snippet boundaries are normalized so injected content never glues
// onto the preceding or following line (previously caused "10embedding:"-style
// corruption when snippets lacked leading/trailing newlines).
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

	snippet = strings.Trim(snippet, "\n")
	if insertIdx >= 0 {
		before := strings.Join(lines[:insertIdx], "\n")
		after := strings.Join(lines[insertIdx:], "\n")
		return before + "\n" + snippet + "\n" + after
	}
	return strings.TrimRight(content, "\n") + "\n" + snippet
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

// gateDefaults defines the value injected for each missing gate key during
// per-key migration of existing model_features entries and feature_defaults.
// loop_warning=true is the single exception: loop detection ran ungated before
// the gate existed, so false would regress effective behavior. All other gates
// default to false (the plain-bool zero value, made explicit).
var gateDefaults = map[string]string{
	"assoc_context":            "false",
	"briefing":                 "false",
	"loop_warning":             "true",
	"plan_checkpoint":          "false",
	"rules_reminder":           "false",
	"skill_eval":               "false",
	"think_reminder":           "false",
	"think_reminder_min_chars": "0",
	"timestamps":               "false",
}

// canonicalGateOrder is the insertion order for missing gate keys (alphabetical).
var canonicalGateOrder = []string{
	"assoc_context",
	"briefing",
	"loop_warning",
	"plan_checkpoint",
	"rules_reminder",
	"skill_eval",
	"think_reminder",
	"think_reminder_min_chars",
	"timestamps",
}

// injectMissingGates scans a YAML sub-section named by sectionHeader (e.g.,
// "claude:") and appends any missing gate keys at the section's key indent.
// Returns the modified content and the number of keys added. Idempotent.
//
// Key indent is auto-detected from the first existing key in the section;
// falls back to sectionIndent+2 for empty sections. Missing keys are appended
// at the section end (before any sibling that dedents to or past sectionIndent).
func injectMissingGates(content, sectionHeader string) (string, int) {
	lines := strings.Split(content, "\n")
	inSection := false
	sectionIndent := -1
	keyIndent := ""
	insertAt := -1
	existing := map[string]bool{}

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !inSection {
			// Section header match tolerates inline comments (e.g., "claude: # preset").
			// The YAML key is the token before the first colon; trailing comment is ignored.
			if key := strings.SplitN(trimmed, ":", 2); len(key) == 2 && key[0]+":" == sectionHeader {
				rest := strings.TrimSpace(key[1])
				if rest == "" || strings.HasPrefix(rest, "#") {
					inSection = true
					sectionIndent = len(line) - len(strings.TrimLeft(line, " \t"))
					insertAt = i + 1
				}
			}
			continue
		}
		if len(trimmed) == 0 || strings.HasPrefix(trimmed, "#") {
			continue
		}
		indent := 0
		if len(line) > 0 && (line[0] == ' ' || line[0] == '\t') {
			indent = len(line) - len(strings.TrimLeft(line, " \t"))
		}
		if indent <= sectionIndent {
			break
		}
		existing[strings.SplitN(trimmed, ":", 2)[0]] = true
		if keyIndent == "" {
			keyIndent = line[:indent]
		}
		insertAt = i + 1
	}

	if !inSection {
		return content, 0
	}
	if keyIndent == "" {
		keyIndent = strings.Repeat(" ", sectionIndent+2)
	}

	var toInsert []string
	for _, key := range canonicalGateOrder {
		if !existing[key] {
			toInsert = append(toInsert, keyIndent+key+": "+gateDefaults[key])
		}
	}
	if len(toInsert) == 0 {
		return content, 0
	}

	newLines := append(append([]string{}, lines[:insertAt]...), toInsert...)
	newLines = append(newLines, lines[insertAt:]...)
	return strings.Join(newLines, "\n"), len(toInsert)
}

// migrateModelFeaturesGates scans an existing model_features block plus the
// feature_defaults sibling and adds missing gate keys to each entry. Returns
// the modified content and the total number of keys added. Idempotent.
//
// Model entries are discovered dynamically (any sub-section header at indent
// == model_features+2 is processed), so custom model names in user configs
// also receive per-key migration. New model entries (e.g., glm) are NOT
// inserted — only existing entries are augmented; missing models fall back
// to feature_defaults at runtime.
func migrateModelFeaturesGates(content string) (string, int) {
	var headers []string

	lines := strings.Split(content, "\n")
	mfLine := -1
	mfIndent := -1
	for i, line := range lines {
		// Top-level header match tolerates inline comments (e.g., "model_features: # gates").
		// Inline values like "model_features: {}" are NOT section headers.
		trimmed := strings.TrimSpace(line)
		parts := strings.SplitN(trimmed, ":", 2)
		if len(parts) == 2 && parts[0]+":" == "model_features:" {
			rest := strings.TrimSpace(parts[1])
			if rest == "" || strings.HasPrefix(rest, "#") {
				mfLine = i
				mfIndent = len(line) - len(strings.TrimLeft(line, " \t"))
				break
			}
		}
	}

	if mfLine >= 0 {
		subIndent := mfIndent + 2
		for i := mfLine + 1; i < len(lines); i++ {
			line := lines[i]
			trimmed := strings.TrimSpace(line)
			if len(trimmed) == 0 || strings.HasPrefix(trimmed, "#") {
				continue
			}
			indent := len(line) - len(strings.TrimLeft(line, " \t"))
			if indent <= mfIndent {
				break
			}
			if indent == subIndent {
				// Sub-section header: "<name>:" optionally followed by inline comment.
				// Inline values like "deepseek: {}" are NOT section headers.
				parts := strings.SplitN(trimmed, ":", 2)
				if len(parts) == 2 {
					rest := strings.TrimSpace(parts[1])
					if rest == "" || strings.HasPrefix(rest, "#") {
						headers = append(headers, parts[0]+":")
					}
				}
			}
		}
	}

	// feature_defaults is a sibling of model_features, not nested.
	// If missing entirely, insert the full section with documented template
	// values before per-key migration runs. Rationale: a missing section
	// previously meant code-false fallback; a newly created visible section
	// should carry the documented defaults so ungated features (loop_warning)
	// don't regress. The template matches modelFeaturesBlock.feature_defaults.
	total := 0
	if !sectionExists(content, "feature_defaults:") {
		content = insertFeatureDefaultsSection(content)
		total += len(canonicalGateOrder)
	}

	headers = append(headers, "feature_defaults:")

	for _, h := range headers {
		var added int
		content, added = injectMissingGates(content, h)
		total += added
	}
	return content, total
}

// sectionExists reports whether a YAML sub-section header (e.g., "feature_defaults:")
// appears as a line, tolerant of inline comments ("feature_defaults: # fallback").
// Inline values ("feature_defaults: {}") are NOT treated as section headers.
func sectionExists(content, sectionHeader string) bool {
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		parts := strings.SplitN(trimmed, ":", 2)
		if len(parts) != 2 || parts[0]+":" != sectionHeader {
			continue
		}
		rest := strings.TrimSpace(parts[1])
		if rest == "" || strings.HasPrefix(rest, "#") {
			return true
		}
	}
	return false
}

// insertFeatureDefaultsSection inserts a full feature_defaults block with
// documented template values (NOT gateDefaults-false values) immediately after
// the model_features section ends. Indent mirrors the model_features header.
// If model_features is absent, the block is appended at content end as a
// sibling of the proxy section.
func insertFeatureDefaultsSection(content string) string {
	lines := strings.Split(content, "\n")
	mfLine := -1
	mfIndent := 0
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		parts := strings.SplitN(trimmed, ":", 2)
		if len(parts) == 2 && parts[0]+":" == "model_features:" {
			rest := strings.TrimSpace(parts[1])
			if rest == "" || strings.HasPrefix(rest, "#") {
				mfLine = i
				mfIndent = len(line) - len(strings.TrimLeft(line, " \t"))
				break
			}
		}
	}

	block := strings.Repeat(" ", mfIndent) + "feature_defaults:\n" +
		strings.Repeat(" ", mfIndent+2) + "assoc_context: false\n" +
		strings.Repeat(" ", mfIndent+2) + "briefing: true\n" +
		strings.Repeat(" ", mfIndent+2) + "loop_warning: true\n" +
		strings.Repeat(" ", mfIndent+2) + "plan_checkpoint: true\n" +
		strings.Repeat(" ", mfIndent+2) + "rules_reminder: true\n" +
		strings.Repeat(" ", mfIndent+2) + "skill_eval: true\n" +
		strings.Repeat(" ", mfIndent+2) + "think_reminder: true\n" +
		strings.Repeat(" ", mfIndent+2) + "think_reminder_min_chars: 0\n" +
		strings.Repeat(" ", mfIndent+2) + "timestamps: true"

	if mfLine < 0 {
		if len(lines) > 0 && lines[len(lines)-1] != "" {
			return content + "\n" + block
		}
		return content + block
	}

	insertAt := len(lines)
	for i := mfLine + 1; i < len(lines); i++ {
		line := lines[i]
		trimmed := strings.TrimSpace(line)
		if len(trimmed) == 0 || strings.HasPrefix(trimmed, "#") {
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, " \t"))
		if indent <= mfIndent {
			insertAt = i
			break
		}
	}

	newLines := append(append([]string{}, lines[:insertAt]...), block)
	newLines = append(newLines, lines[insertAt:]...)
	return strings.Join(newLines, "\n")
}
