package proxy

import "strings"

// hasBriefingTurn checks if messages already contain a briefing system-reminder marker.
func hasBriefingTurn(msgs []any) bool {
	if len(msgs) == 0 {
		return false
	}
	if first, ok := msgs[0].(map[string]any); ok {
		if content, ok := first["content"].(string); ok {
			if strings.Contains(content, "<system-reminder>") {
				return true
			}
		}
		if role, _ := first["role"].(string); role == "user" {
			if content, _ := first["content"].(string); strings.Contains(content, "<system-reminder>") {
				return true
			}
		}
	}
	return false
}
