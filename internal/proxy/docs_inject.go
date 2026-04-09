package proxy

import "sync"

const docsHintInterval = 10000 // tokens between docs hint re-injections

// docsCp tracks docs hint injection state per thread (independent of plan checkpoints).
var docsCp = &struct {
	mu             sync.Mutex
	lastTokenCount map[string]int // threadID → totalTokens at last injection
}{lastTokenCount: make(map[string]int)}

// docsHintInject checks if a docs-available reminder should be injected.
// Works independently of plan checkpoints — ensures subagents and sessions
// without active plans still get docs reminders.
// Returns the hint text, or "" if not yet time or no docs indexed.
func (s *Server) docsHintInject(threadID string, totalTokens int, project string) string {
	docsCp.mu.Lock()
	lastTokens := docsCp.lastTokenCount[threadID]
	if lastTokens == 0 {
		docsCp.lastTokenCount[threadID] = totalTokens
		docsCp.mu.Unlock()
		return ""
	}
	if totalTokens-lastTokens < docsHintInterval {
		docsCp.mu.Unlock()
		return ""
	}
	docsCp.lastTokenCount[threadID] = totalTokens
	docsCp.mu.Unlock()

	return s.getDocsHint(project)
}
