package setup

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	yesmemSkills "github.com/carsteneu/yesmem/skills"
	"gopkg.in/yaml.v3"
)

// rulesmdHeader is the RULES.md header section included at the top of every generated file.
const rulesmdHeader = `You are a rule enforcement system that evaluates tool calls before execution.

For each tool about to be executed, you receive:
- The tool name and its arguments
- Your recent messages for session context

You must respond with exactly ONE word:
- BLOCK   — the tool call violates a rule and must be prevented
- SUGGEST — the tool call should proceed but a skill or guideline applies
- PASS    — the tool call is compliant with all rules

## Rules
The following rules are non-negotiable. Evaluate each tool call against EVERY rule.

`

// rulesmdBestPractices contains universal, language-agnostic best practices.
// Agent-121 research will refine this list.
var rulesmdBestPractices = []string{
	"## Commits & Git",
	"Never auto-commit without explicit user instruction. Commits are the user's decision — only commit when the user explicitly asks ('commit', 'push', 'do it'). No LLM signature in commit messages.",
	"Never commit secrets, API keys, credentials, or environment files (.env) to the repository.",
	"Before git push: verify the test suite passed (`make test` or equivalent).",
	"Implementation must happen on a feature branch, never directly on main. Check with `git symbolic-ref HEAD` before editing.",
	"Use Conventional Commits: `feat:`, `fix:`, `refactor:`, `docs:`, `test:`, `chore:` prefix. Scope optional in parentheses. No multiline subjects.",
	"",
	"## Implementation",
	"Check for relevant Skill before acting — use Skill tool BEFORE Bash/Write/Edit/Read.",
	"No workarounds — proper solution or none. Pragmatic yes, sloppy never.",
	"One concern per file — follow existing patterns in the codebase, don't invent new structures.",
	"Follow existing code conventions — mimic style, use existing libraries, never assume a library is available.",
	"Execute the user's request, not adjacent improvements. Flag adjacent issues as separate notes — never silently bundle unrelated changes.",
	"Never add comments unless explicitly asked or the code is genuinely non-obvious.",
	"Refactors must preserve API contracts. Never silently remove parameters, optional hooks, or extension points consumers may depend on.",
	"",
	"## Quality",
	"After code changes: run the test suite immediately — do not defer.",
	"Never ignore errors or warnings — find and fix the root cause.",
	"Address root causes, not symptoms. Never use `--no-verify`, silent `try/except`, or other workarounds that mask errors.",
	"Before reporting task complete: run the verification command and cite the actual output. If verification was skipped, explicitly say so — never imply success.",
	"Never claim 'all tests pass' when output shows failures.",
	"Report tool results literally — errors, empty output, and partial results must be stated exactly, not smoothed over or summarized away.",
	"",
	"## BEFORE Answering / Acting",
	"ALWAYS search memory (yesmem-search, hybrid_search) before answering questions about past work, architecture, prior decisions, or before proposing fixes.",
	"At session start or returning after a break: use yesmem-orientation skill.",
	"After ANY decision, correction, gotcha, or discovery: use yesmem-remember skill.",
	"",
	"## Shell & Commands",
	"Bash commands: always single-line, chain with && or ;. No heredoc, no multiline.",
	"API calls & Bash: always set timeouts, never longer than 20 seconds.",
	"Never manually copy binaries (e.g., `cp yesmem ~/.local/bin/`). Always use `make deploy` instead.",
	"Block destructive Bash: no `rm -rf` outside scratch/temp dirs, no `git push --force` to protected branches, no `DROP TABLE` or schema migration without backup check.",
	"",
	"## Memory & Search",
	"Memory queries (search, hybrid_search, deep_search) must use German language — the knowledge base is primarily German text.",
	"",
}

// skillCatalogEntry represents one skill in the RULES.md YAML catalog.
type skillCatalogEntry struct {
	ID       int      `yaml:"id"`
	Skill    string   `yaml:"skill"`
	Priority string   `yaml:"priority"`
	Triggers []string `yaml:"triggers"`
	Rule     string   `yaml:"rule"`
}

// skillFrontmatter holds the YAML frontmatter of a SKILL.md file.
type skillFrontmatter struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
}

// loadSkillCatalog extracts skill YAML frontmatter from bundled skills,
// Superpowers skills (~/.cache/opencode/packages/superpowers*/), and user skills
// (~/.claude/skills/), returning deduplicated RULES.md catalog entries.
func loadSkillCatalog() ([]skillCatalogEntry, error) {
	var catalog []skillCatalogEntry
	seen := map[string]bool{}
	baseID := 25

	// 1. Bundled yesmem skills
	entries, err := yesmemSkills.BundledSkills.ReadDir("bundled-skills")
	if err == nil {
		for _, e := range entries {
			if !e.IsDir() { continue }
			data, err := yesmemSkills.BundledSkills.ReadFile("bundled-skills/" + e.Name() + "/SKILL.md")
			if err != nil { continue }
			fm, err := parseSkillFrontmatter(string(data))
			if err != nil || fm.Name == "" { continue }
			if seen[fm.Name] { continue }
			seen[fm.Name] = true
			catalog = append(catalog, skillCatalogEntry{
				ID: baseID, Skill: fm.Name, Priority: "MUST",
				Triggers: extractTriggers(fm.Description), Rule: fm.Description,
			})
			baseID++
		}
	}

	// 2. Superpowers skills from filesystem
	superpowersRoot := resolveSuperpowersRoot()
	if superpowersRoot != "" {
		extDir := filepath.Join(superpowersRoot, "skills")
		if dirs, err := os.ReadDir(extDir); err == nil {
			for _, d := range dirs {
				if !d.IsDir() { continue }
				data, err := os.ReadFile(filepath.Join(extDir, d.Name(), "SKILL.md"))
				if err != nil { continue }
				fm, err := parseSkillFrontmatter(string(data))
				if err != nil || fm.Name == "" { continue }
				if seen[fm.Name] { continue }
				seen[fm.Name] = true
				catalog = append(catalog, skillCatalogEntry{
					ID: baseID, Skill: fm.Name, Priority: "MUST",
					Triggers: extractTriggers(fm.Description), Rule: fm.Description,
				})
				baseID++
			}
		}
	}

	// 3. User skills (~/.claude/skills/)
	if home, err := os.UserHomeDir(); err == nil {
		userSkillsDir := filepath.Join(home, ".claude", "skills")
		if dirs, err := os.ReadDir(userSkillsDir); err == nil {
			for _, d := range dirs {
				if !d.IsDir() { continue }
				data, err := os.ReadFile(filepath.Join(userSkillsDir, d.Name(), "SKILL.md"))
				if err != nil { continue }
				fm, err := parseSkillFrontmatter(string(data))
				if err != nil || fm.Name == "" { continue }
				if seen[fm.Name] { continue }
				seen[fm.Name] = true
				catalog = append(catalog, skillCatalogEntry{
					ID: baseID, Skill: fm.Name, Priority: "MUST",
					Triggers: extractTriggers(fm.Description), Rule: fm.Description,
				})
				baseID++
			}
		}
	}

	sort.Slice(catalog, func(i, j int) bool { return catalog[i].ID < catalog[j].ID })
	return catalog, nil
}

// resolveSuperpowersRoot finds the Superpowers npm package directory.
// Searches common package manager cache directories for "superpowers@".
func resolveSuperpowersRoot() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	dirs := []string{
		filepath.Join(home, ".cache", "opencode", "packages"),
		filepath.Join(home, ".cache", "claude", "packages"),
		filepath.Join(home, ".npm", "_npx"),
	}
	for _, packagesDir := range dirs {
		ents, err := os.ReadDir(packagesDir)
		if err != nil {
			continue
		}
		for _, e := range ents {
			if !e.IsDir() {
				continue
			}
			if strings.HasPrefix(e.Name(), "superpowers@") {
				return filepath.Join(packagesDir, e.Name(), "node_modules", "superpowers")
			}
		}
	}
	return ""
}

// parseSkillFrontmatter extracts YAML frontmatter from a SKILL.md file.
func parseSkillFrontmatter(content string) (*skillFrontmatter, error) {
	// Frontmatter is between --- markers at the start of the file
	if !strings.HasPrefix(content, "---\n") {
		return nil, fmt.Errorf("no frontmatter")
	}

	end := strings.Index(content[4:], "\n---")
	if end < 0 {
		return nil, fmt.Errorf("unclosed frontmatter")
	}

	fmStr := content[4 : 4+end]
	var fm skillFrontmatter
	if err := yaml.Unmarshal([]byte(fmStr), &fm); err != nil {
		return nil, err
	}
	return &fm, nil
}

// extractTriggers parses trigger keywords from a skill description.
// It looks for "Trigger on" clauses and extracts the quoted/listed keywords.
func extractTriggers(desc string) []string {
	var triggers []string

	// Find "Trigger on" section
	triggerIdx := strings.Index(desc, "Trigger on")
	if triggerIdx >= 0 {
		triggerPart := desc[triggerIdx:]
		// Find the closing period or end of sentence
		if endIdx := strings.Index(triggerPart, "."); endIdx >= 0 {
			triggerPart = triggerPart[:endIdx]
		}
		// Remove the "Trigger on" prefix and split
		triggerPart = strings.TrimPrefix(triggerPart, "Trigger on")
		triggerPart = strings.TrimSpace(triggerPart)

		// Split by comma or "or"
		for _, part := range strings.FieldsFunc(triggerPart, func(r rune) bool {
			return r == ',' || r == ';'
		}) {
			part = strings.TrimSpace(part)
			// Remove quotes
			part = strings.Trim(part, `"`)
			// Remove leading "or "
			part = strings.TrimPrefix(part, "or ")
			part = strings.TrimSpace(part)
			if part != "" {
				triggers = append(triggers, part)
			}
		}
	}

	// Also extract from "Use when" pattern (Superpowers-style triggers).
	// Take the whole phrase as a single trigger — do not split by commas,
	// since these are natural-language sentences, not keyword lists.
	useIdx := strings.Index(desc, "Use when")
	if useIdx >= 0 {
		usePart := desc[useIdx:]
		// Cut at "Trigger on" boundary if present (it's the keyword list)
		if trigIdx := strings.Index(usePart, "Trigger on"); trigIdx >= 0 {
			usePart = usePart[:trigIdx]
		} else if endIdx := strings.Index(usePart, "."); endIdx >= 0 {
			usePart = usePart[:endIdx]
		}
		usePart = strings.TrimPrefix(usePart, "Use when")
		usePart = strings.TrimSpace(usePart)
		if usePart != "" {
			triggers = append(triggers, usePart)
		}
	}

	// Also derive from description keywords (common terms between
	// "description: Use when..." and "Trigger on")
	// Extract action-related words from description
	descLower := strings.ToLower(desc)
	keywordPairs := map[string][]string{
		"brainstorming":              {"neue Funktion", "neues Feature", "neue Komponente", "Funktionalität hinzufügen", "Verhalten ändern", "kreative Arbeit", "Design", "Architektur-Änderung", "new feature", "new component", "adding functionality", "modifying behavior", "creative work", "design decision", "before implementing"},
		"test-driven-development":    {"Feature implementieren", "Bugfix", "Bug", "neues Feature", "Implementierung", "Code-Änderung", "fix", "beheben", "implementing", "write code", "TDD"},
		"systematic-debugging":       {"Bug", "Fehler", "Test schlägt fehl", "test failure", "unerwartet", "crash", "broken", "funktioniert nicht", "Fehlermeldung", "debug", "debugging"},
		"verification-before-completion": {"fertig", "done", "complete", "commit", "PR", "Pull Request", "merge", "deploy", "passing", "fixed", "verified"},
		"writing-plans":             {"Plan", "mehrschrittig", "Architektur", "Spezifikation", "mehrere Schritte", "Multi-Step", "Implementierungsplan", "spec"},
		"yesmem-orientation":        {"where were we?", "what's open?", "wo waren wir?", "neue Session", "Projektwechsel", "zurück nach Pause", "Session-Start", "project state"},
		"yesmem-search":             {"past work", "architecture question", "prior decision", "how does X work", "remember this approach", "earlier discussion", "previous session", "search memory"},
		"yesmem-remember":           {"remember this", "merk dir", "gotcha", "decision made", "correction", "discovery", "learned something"},
		"yesmem-config":             {"pin this", "merk dir als Regel", "persistent instructions", "persona", "config change", "token_threshold"},
		"yesmem-cap-builder":        {"save_cap", "cap_store", "make this reusable", "build-tool", "CAP.md", "reusable tool"},
		"yesmem-docs":               {"API behavior", "function signature", "idiomatic pattern", "docs_search", "check documentation", "indexed docs"},
		"yesmem-agents":             {"/schwarm", "parallel work", "agent coordination", "multi-agent", "swarm", "spawn agent"},
		"yesmem-sessions":           {"last week", "yesterday session", "past conversation", "letzte Session", "last time we", "session history"},
		"subagent-driven-development": {"implementation plan", "independent tasks", "dispatch agent", "parallel agents"},
		"dispatching-parallel-agents": {"parallel tasks", "independent work", "no shared state", "no dependencies", "run concurrently"},
		"executing-plans":            {"execution plan", "review checkpoints", "written plan", "implement plan"},
		"finishing-a-development-branch": {"implementation complete", "all tests pass", "ready to merge", "finish branch", "done implementing"},
		"receiving-code-review":      {"code review feedback", "review comments", "PR feedback", "review suggestion", "unclear feedback"},
		"requesting-code-review":     {"review my code", "check my work", "before merging", "verify requirements", "code review please"},
		"using-git-worktrees":        {"feature isolation", "isolated workspace", "git worktree", "new branch work", "parallel branch"},
		"using-superpowers":          {"starting conversation", "new session", "first message", "begin work", "fresh start"},
		"writing-skills":             {"create skill", "write skill", "edit skill", "new skill", "update skill", "verify skill"},
		"reddit_fetch":               {"reddit post", "reddit URL", "reddit thread", "fetch reddit", "r/"},
		"sync-to-public":             {"sync public", "push to github", "public repo", "create PR", "pr erstellen", "pull request"},
	}

	for _, keywords := range keywordPairs {
		for _, kw := range keywords {
			if strings.Contains(descLower, kw) {
				triggers = append(triggers, kw)
			}
		}
	}

	// Deduplicate
	seen := map[string]bool{}
	var unique []string
	for _, t := range triggers {
		if !seen[t] {
			seen[t] = true
			unique = append(unique, t)
		}
	}

	return unique
}

// projectTypeRules maps dependency files to language-specific rules.
var projectTypeRules = map[string][]string{
	"go.mod": {
		"Go: always TDD — write tests first, then implementation (`*_test.go`).",
		"Go: use standard library over third-party dependencies when possible.",
		"Go: never use `panic` in library code — return errors.",
		"Go: one exported symbol per file is a smell — keep files focused.",
	},
	"package.json": {
		"TypeScript/JavaScript: prefer strict types, avoid `any`.",
		"TypeScript/JavaScript: one component per file, one concern per file.",
		"TypeScript/JavaScript: no unused imports or dead code — clean up before commit.",
	},
	"Cargo.toml": {
		"Rust: use `Result<T, E>` over `unwrap()` or `expect()` in library code.",
		"Rust: run `cargo clippy` and `cargo fmt` before commit.",
		"Rust: prefer `&str` over `String` for function parameters when possible.",
	},
	"pyproject.toml": {
		"Python: use type hints on all function signatures.",
		"Python: run `ruff check` and `ruff format` before commit.",
		"Python: prefer dataclasses over raw dicts for structured data.",
	},
	"requirements.txt": {
		"Python: use type hints on all function signatures.",
		"Python: run `ruff check` before commit.",
	},
}

// detectProjectType scans projectDir for known dependency files.
func detectProjectType(projectDir string) []string {
	var types []string
	for file := range projectTypeRules {
		if _, err := os.Stat(filepath.Join(projectDir, file)); err == nil {
			types = append(types, file)
		}
	}
	return types
}

// GenerateRULESmd creates or updates RULES.md in projectDir.
// If the file already exists, it is not overwritten (user may have customized it).
// Returns the path of the written file, or "" if skipped.
func GenerateRULESmd(home, projectDir string) (string, error) {
	rulesPath := filepath.Join(projectDir, "RULES.md")

	// Don't overwrite existing RULES.md — it's project-specific and user-owned.
	if _, err := os.Stat(rulesPath); err == nil {
		return "", nil
	}

	var sb strings.Builder

	// Header
	sb.WriteString(rulesmdHeader)

	// Best Practices
	ruleNum := 1
	for _, line := range rulesmdBestPractices {
		if line == "" {
			sb.WriteString("\n")
			continue
		}
		if strings.HasPrefix(line, "## ") {
			sb.WriteString(line + "\n")
			continue
		}
		fmt.Fprintf(&sb, "%d. %s\n", ruleNum, line)
		ruleNum++
	}

	// Project-type specific rules
	types := detectProjectType(projectDir)
	if len(types) > 0 {
		sb.WriteString("\n## Project-Specific\n")
		for _, t := range types {
			rules, ok := projectTypeRules[t]
			if !ok {
				continue
			}
			for _, rule := range rules {
				fmt.Fprintf(&sb, "%d. %s\n", ruleNum, rule)
				ruleNum++
			}
		}
	}

	// Skill Catalog
	catalog, err := loadSkillCatalog()
	if err == nil && len(catalog) > 0 {
		sb.WriteString("\n## Skill Catalog\n")
		sb.WriteString("# Generated from bundled skills. Triggers match user intent.\n")
		sb.WriteString("rules:\n")

		for _, entry := range catalog {
			triggersYAML := marshalTriggersYAML(entry.Triggers)
			fmt.Fprintf(&sb, "  - id: %d\n", entry.ID)
			fmt.Fprintf(&sb, "    skill: %s\n", entry.Skill)
			fmt.Fprintf(&sb, "    priority: %s\n", entry.Priority)
			fmt.Fprintf(&sb, "    triggers: %s\n", triggersYAML)
			fmt.Fprintf(&sb, "    rule: \"%s\"\n", escapeYAMLString(entry.Rule))
		}
	}

	// Write file
	if err := os.MkdirAll(filepath.Dir(rulesPath), 0755); err != nil {
		return "", fmt.Errorf("create dir for RULES.md: %w", err)
	}
	if err := os.WriteFile(rulesPath, []byte(sb.String()), 0644); err != nil {
		return "", fmt.Errorf("write RULES.md: %w", err)
	}

	return rulesPath, nil
}

// marshalTriggersYAML returns a YAML-safe string representation of a trigger list.
func marshalTriggersYAML(triggers []string) string {
	if len(triggers) == 0 {
		return "[]"
	}
	quoted := make([]string, len(triggers))
	for i, t := range triggers {
		quoted[i] = escapeYAMLBracketString(t)
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}

// escapeYAMLBracketString escapes a string for use inside YAML brackets.
func escapeYAMLBracketString(s string) string {
	// Simple escaping: wrap in quotes, escape internal quotes
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "\"", "\\\"")
	return "\"" + s + "\""
}

// escapeYAMLString escapes a string for use as a YAML double-quoted value.
func escapeYAMLString(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "\"", "\\\"")
	s = strings.ReplaceAll(s, "\n", " ")
	return s
}
