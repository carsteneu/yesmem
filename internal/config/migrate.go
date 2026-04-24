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
}

// MigrateConfig reads an existing config.yaml and inserts any missing
// proxy-section fields at the end of the proxy: block.
// Returns the number of fields added.
func MigrateConfig(path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, fmt.Errorf("read config: %w", err)
	}

	content := string(data)
	if !strings.Contains(content, "proxy:") {
		return 0, nil
	}

	var toAdd []string
	for _, m := range proxyMigrations {
		if strings.Contains(content, m.key) {
			continue
		}
		toAdd = append(toAdd, m.snippet)
	}
	if len(toAdd) == 0 {
		return 0, nil
	}

	lines := strings.Split(content, "\n")
	insertIdx := -1
	inProxy := false
	for i, line := range lines {
		if strings.HasPrefix(line, "proxy:") {
			inProxy = true
			continue
		}
		if inProxy && len(line) > 0 && line[0] != ' ' && line[0] != '#' && line[0] != '\t' {
			insertIdx = i
			break
		}
	}

	insert := strings.Join(toAdd, "")
	if insertIdx >= 0 {
		before := strings.Join(lines[:insertIdx], "\n")
		after := strings.Join(lines[insertIdx:], "\n")
		content = before + insert + after
	} else {
		content += insert
	}

	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		return 0, fmt.Errorf("write config: %w", err)
	}

	return len(toAdd), nil
}
