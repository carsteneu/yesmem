package proxy

import (
	"encoding/json"
	"fmt"
	"log"
)

const rulesInjectionInterval = 40000 // tokens between re-injections

// rulesInject checks if rules should be re-injected based on cumulative token count.
// Called with the current total token count of the conversation.
// Returns the rules block to inject, or "" if not yet time.
func (s *Server) rulesInject(threadID string, totalTokens int, project string) string {
	s.rulesMu.Lock()
	if s.rulesTokenCount == nil {
		s.rulesTokenCount = make(map[string]int)
	}
	if s.rulesCollapseInjected == nil {
		s.rulesCollapseInjected = make(map[string]bool)
	}

	// Reset collapse flag — normal request flow resumes
	s.rulesCollapseInjected[threadID] = false

	lastInjected := s.rulesTokenCount[threadID] // total tokens at last injection
	if lastInjected == 0 {
		// First call — set baseline, don't inject yet (briefing covers initial context)
		s.rulesTokenCount[threadID] = totalTokens
		s.rulesMu.Unlock()
		return ""
	}

	if totalTokens-lastInjected < rulesInjectionInterval {
		s.rulesMu.Unlock()
		return ""
	}

	// Reset to current position
	s.rulesTokenCount[threadID] = totalTokens
	s.rulesMu.Unlock()

	return s.getRulesBlock(project)
}

// rulesInjectAfterCollapse injects rules once after collapse, then blocks until normal inject resets the flag.
func (s *Server) rulesInjectAfterCollapse(threadID, project string) string {
	s.rulesMu.Lock()
	if s.rulesTokenCount == nil {
		s.rulesTokenCount = make(map[string]int)
	}
	if s.rulesCollapseInjected == nil {
		s.rulesCollapseInjected = make(map[string]bool)
	}
	if s.rulesCollapseInjected[threadID] {
		s.rulesMu.Unlock()
		return ""
	}
	s.rulesCollapseInjected[threadID] = true
	s.rulesTokenCount[threadID] = 0
	s.rulesMu.Unlock()

	return s.getRulesBlock(project)
}

// getRulesBlock returns the cached rules block, fetching from daemon on first call.
func (s *Server) getRulesBlock(project string) string {
	s.rulesMu.RLock()
	if s.rulesBlock != "" {
		defer s.rulesMu.RUnlock()
		return s.rulesBlock
	}
	s.rulesMu.RUnlock()

	// Fetch from daemon
	block := s.fetchRulesFromDaemon(project)
	if block == "" {
		return ""
	}

	s.rulesMu.Lock()
	s.rulesBlock = block
	s.rulesMu.Unlock()

	return block
}

// invalidateRulesCache clears the cached rules block (e.g. after CLAUDE.md change).
func (s *Server) invalidateRulesCache() {
	s.rulesMu.Lock()
	s.rulesBlock = ""
	s.rulesMu.Unlock()
}

// fetchRulesFromDaemon queries the daemon for the condensed rules block.
func (s *Server) fetchRulesFromDaemon(project string) string {
	result, err := s.queryDaemon("get_rules_block", map[string]any{"project": project})
	if err != nil {
		return ""
	}

	var resp struct {
		Content string `json:"content"`
		Exists  bool   `json:"exists"`
	}
	if json.Unmarshal(result, &resp) != nil || !resp.Exists {
		return ""
	}

	log.Printf("[rules] loaded rules block: %d chars", len(resp.Content))
	return resp.Content
}

// formatRulesReminder wraps the condensed rules in a recognizable block.
// Fetches permanent pins from daemon and appends them.
// Adds a pointer to CLAUDE.md and docs_search for context beyond the reminder.
func (s *Server) formatRulesReminder(rules, project string) string {
	// Fetch permanent pins
	var pinBlock string
	if result, err := s.queryDaemon("get_pins", map[string]any{"project": project}); err == nil {
		var resp struct {
			Pins []struct {
				ID      int    `json:"id"`
				Scope   string `json:"scope"`
				Content string `json:"content"`
			} `json:"pins"`
		}
		if json.Unmarshal(result, &resp) == nil {
			var lines []string
			for _, p := range resp.Pins {
				if p.Scope == "permanent" {
					lines = append(lines, fmt.Sprintf("- [pin:%d permanent] %s", p.ID, p.Content))
				}
			}
			if len(lines) > 0 {
				pinBlock = "\n\n## Pinned Instructions\n" + joinLines(lines)
			}
		}
	}

	footer := "\n\n---\nThese rules are an excerpt. Additional project-specific instructions (architecture, routes, code patterns) are in your system prompt (CLAUDE.md). When unsure: docs_search() for details.\nTimestamps [HH:MM:SS] [msg:N] [+Δ] are embedded in every message — always reference them for time-related statements."
	return fmt.Sprintf("[Rules Reminder]\n%s%s%s\n[/Rules Reminder]", rules, pinBlock, footer)
}

func joinLines(lines []string) string {
	result := ""
	for i, l := range lines {
		if i > 0 {
			result += "\n"
		}
		result += l
	}
	return result
}
