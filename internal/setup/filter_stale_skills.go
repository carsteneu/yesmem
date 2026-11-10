package setup

import (
	"os"
	"path/filepath"
	"strings"
)

// loadAgentLoadableSkillNames returns the set of skill names that the agent
// can actually activate via its platform's Skill tool. A skill is loadable
// when its SKILL.md exists in one of the host-platform skill directories:
//
//   - User skills: ~/.claude/skills/<name>/SKILL.md
//   - Superpowers skills: <superpowers-root>/skills/<name>/SKILL.md
//
// Bundled yesmem skills in the repo (skills/bundled-skills/) are NOT counted
// here — they must be deployed to one of the host directories to become
// agent-loadable. This prevents the guard from suggesting skills the agent
// cannot activate.
func loadAgentLoadableSkillNames() map[string]bool {
	names := map[string]bool{}

	if home, err := os.UserHomeDir(); err == nil {
		userDir := filepath.Join(home, ".claude", "skills")
		scanSkillDir(userDir, names)
	}

	if root := resolveSuperpowersRoot(); root != "" {
		scanSkillDir(filepath.Join(root, "skills"), names)
	}

	return names
}

// LoadAgentLoadableSkillNames is the exported wrapper for use by hooks.RunGuard.
func LoadAgentLoadableSkillNames() map[string]bool {
	return loadAgentLoadableSkillNames()
}

// scanSkillDir reads <dir>/<name>/SKILL.md for each subdir and registers the
// frontmatter `name` field in the provided set.
func scanSkillDir(dir string, names map[string]bool) {
	dirs, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, d.Name(), "SKILL.md"))
		if err != nil {
			continue
		}
		fm, err := parseSkillFrontmatter(string(data))
		if err != nil || fm.Name == "" {
			continue
		}
		names[fm.Name] = true
	}
}

// FilterStaleSkillRules strips catalog entries from a RULES.md document whose
// `skill:` field names a skill that is not loadable on this machine. This is
// a backwards-compat shim: a catalogue is only refreshed by setup when its
// provenance proves nobody edited it, so a RULES.md baked at an earlier time
// may still reference skills whose SKILL.md have since been removed. Without
// this filter the guard keeps suggesting un-activatable skills on every tool
// call.
//
// If available is empty (e.g. filesystem inaccessible), the input is
// returned unchanged — pass through conservatively rather than risk
// dropping all catalog rules.
func FilterStaleSkillRules(content string, available map[string]bool) string {
	return filterStaleSkillRulesWith(content, available)
}

// filterStaleSkillRulesWith is the testable core. It walks the catalog
// section line-by-line, dropping entries whose skill is not in available.
// Catalog section starts at "## Skill Catalog" and ends at the next "## "
// header or EOF.
func filterStaleSkillRulesWith(content string, available map[string]bool) string {
	if len(available) == 0 {
		return content
	}

	lines := strings.Split(content, "\n")
	var out []string
	inCatalog := false
	skipUntilNextEntry := false

	for i := 0; i < len(lines); i++ {
		line := lines[i]

		if strings.HasPrefix(line, "## ") {
			inCatalog = strings.HasPrefix(line, "## Skill Catalog")
			skipUntilNextEntry = false
			out = append(out, line)
			continue
		}

		if !inCatalog {
			out = append(out, line)
			continue
		}

		// Entry boundary: a line starting with "  - id:" begins a new entry.
		// We peek ahead for its "skill:" field to decide keep vs drop.
		if strings.HasPrefix(line, "  - id:") {
			skill := peekSkillName(lines[i:])
			if skill != "" && !available[skill] {
				skipUntilNextEntry = true
				continue
			}
			skipUntilNextEntry = false
			out = append(out, line)
			continue
		}

		if skipUntilNextEntry {
			// Drop all lines until the next entry boundary
			continue
		}

		out = append(out, line)
	}

	return strings.Join(out, "\n")
}

// peekSkillName scans forward from an entry's first line ("  - id:") to
// find the value of the "skill:" field within the same entry. Returns ""
// if no skill field is present. Stops at the next entry boundary ("  - id:"
// line after the first, or any "## " section header) so it never returns
// a skill name from a following entry.
func peekSkillName(entryLines []string) string {
	if len(entryLines) == 0 {
		return ""
	}
	// Skip the first line (the entry's own "  - id:" boundary marker).
	for _, l := range entryLines[1:] {
		if strings.HasPrefix(l, "  - id:") {
			break // next entry starts here — stop without finding a skill
		}
		if strings.HasPrefix(l, "## ") || strings.HasPrefix(l, "- id:") {
			break
		}
		trimmed := strings.TrimSpace(l)
		if strings.HasPrefix(trimmed, "skill:") {
			return strings.TrimSpace(strings.TrimPrefix(trimmed, "skill:"))
		}
	}
	return ""
}
