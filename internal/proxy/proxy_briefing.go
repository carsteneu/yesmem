package proxy

import (
	"encoding/json"
	"strings"
	"time"
)

// briefingData holds both briefing text and code map from a single daemon RPC call.
type briefingData struct {
	Text    string
	CodeMap string
}

// loadBriefing fetches the briefing text and code map from the daemon via generate_briefing RPC.
func (s *Server) loadBriefing(project, projectDir string) briefingData {
	if project == "" {
		s.logger.Printf("[briefing] skipped: no project name")
		return briefingData{}
	}
	result, err := s.queryDaemon("generate_briefing", map[string]any{
		"project":     project,
		"project_dir": projectDir,
	})
	if err != nil {
		s.logger.Printf("%s[briefing] daemon error: %v%s", colorRed, err, colorReset)
		return briefingData{}
	}
	var resp struct {
		Text    string `json:"text"`
		CodeMap string `json:"code_map"`
	}
	if json.Unmarshal(result, &resp) != nil {
		text := strings.Trim(string(result), "\"")
		s.logger.Printf("[briefing] loaded (raw): %db", len(text))
		return briefingData{Text: text}
	}
	if resp.Text != "" {
		s.logger.Printf("[briefing] loaded: %db text + %db code_map for project=%s", len(resp.Text), len(resp.CodeMap), project)
	} else {
		s.logger.Printf("[briefing] empty response for project=%s", project)
	}
	return briefingData{Text: resp.Text, CodeMap: resp.CodeMap}
}

// refreshBriefing forces a briefing reload. Called during sawtooth stub-cycles.
func (s *Server) refreshBriefing(project, projectDir string) {
	if data := s.loadBriefing(project, projectDir); data.Text != "" {
		s.briefingMu.Lock()
		s.briefingText = data.Text
		s.codeMapText = data.CodeMap
		s.briefingMu.Unlock()
		s.logger.Printf("[briefing] refreshed during stub-cycle: %db text + %db codemap", len(data.Text), len(data.CodeMap))
	}
}

// recentLearningItem represents a recently remembered learning with its ID.
type recentLearningItem struct {
	ID   int64  `json:"id"`
	Text string `json:"text"`
}

// injectBriefingTurn prepends the briefing as a user/assistant turn pair at the
// beginning of the messages array. Returns true if injection happened.
// The turns are static per session → stable prefix → cacheable.
func (s *Server) injectBriefingTurn(req map[string]any, reqIdx int, proj, threadID string) bool {
	s.briefingMu.RLock()
	text := s.briefingText
	s.briefingMu.RUnlock()

	// Lazy-init: load briefing + code map on first request if not yet populated
	if text == "" && proj != "" {
		data := s.loadBriefing(proj, extractWorkingDirectory(req))
		if data.Text != "" {
			s.briefingMu.Lock()
			s.briefingText = data.Text
			s.codeMapText = data.CodeMap
			text = data.Text
			s.briefingMu.Unlock()
		}
	}

	if text == "" {
		s.logger.Printf("%s[briefing] WARN: empty briefing for project=%s, injection skipped%s", colorOrange, proj, colorReset)
		return false
	}

	msgs, ok := req["messages"].([]any)
	if !ok || len(msgs) == 0 {
		return false
	}

	// Check if already injected (first message contains our marker)
	if first, ok := msgs[0].(map[string]any); ok {
		if content, ok := first["content"].(string); ok {
			if strings.Contains(content, "<system-reminder>\nYour full session briefing") {
				return false
			}
		}
	}

	// Prepend user/assistant turn pair
	briefingTurns := []any{
		map[string]any{
			"role":    "user",
			"content": "<system-reminder>\n" + text + "\n</system-reminder>",
		},
		map[string]any{
			"role":    "assistant",
			"content": "Understood. I've read the session briefing.",
		},
	}
	req["messages"] = append(briefingTurns, msgs...)
	s.logger.Printf("[briefing] injected user/assistant turn: %db for tid=%s", len(text), threadID)
	return true
}

// injectCodeMapTurn appends the code map as a user/assistant turn after the briefing.
// Same pattern: static per session, cacheable, dedup-protected.
func (s *Server) injectCodeMapTurn(req map[string]any, reqIdx int, proj, threadID string) bool {
	s.briefingMu.RLock()
	cm := s.codeMapText
	s.briefingMu.RUnlock()

	if cm == "" {
		s.logger.Printf("[codemap] skip: empty (%d bytes stored)", len(s.codeMapText))
		return false
	}

	msgs, ok := req["messages"].([]any)
	if !ok || len(msgs) == 0 {
		return false
	}

	// Check if already injected (our specific marker, not generic "## Code Map")
	for _, m := range msgs[:min(4, len(msgs))] {
		if msg, ok := m.(map[string]any); ok {
			if content, ok := msg["content"].(string); ok {
				if strings.Contains(content, "Understood. I've read the code map.") {
					return false
				}
			}
		}
	}

	// Find insertion point: after briefing turn pair (index 2) or at start
	insertAt := 0
	if len(msgs) >= 2 {
		if first, ok := msgs[0].(map[string]any); ok {
			if content, ok := first["content"].(string); ok {
				if strings.Contains(content, "<system-reminder>\nYour full session briefing") {
					insertAt = 2
				}
			}
		}
	}

	codeMapTurns := []any{
		map[string]any{
			"role":    "user",
			"content": "<system-reminder>\n" + cm + "\n</system-reminder>",
		},
		map[string]any{
			"role":    "assistant",
			"content": "Understood. I've read the code map.",
		},
	}

	// Insert at position
	newMsgs := make([]any, 0, len(msgs)+2)
	newMsgs = append(newMsgs, msgs[:insertAt]...)
	newMsgs = append(newMsgs, codeMapTurns...)
	newMsgs = append(newMsgs, msgs[insertAt:]...)
	req["messages"] = newMsgs
	s.logger.Printf("[codemap] injected user/assistant turn: %db for tid=%s", len(cm), threadID)
	return true
}

// Deprecated: popRecentRemember is no longer called from the proxy pipeline.
// Fresh remember injection caused echo-loops: Claude saved a learning, saw it
// again next turn as [yesmem fresh memory], and reacted to its own output.
// Recovery after /clear is handled by recovery.go, subagents get their own briefing.
// The daemon-side handler (pop_recent_remember) still exists but is effectively dead code.
func (s *Server) popRecentRemember() []recentLearningItem {
	result, err := s.queryDaemon("pop_recent_remember", map[string]any{})
	if err != nil {
		return nil
	}
	var resp struct {
		Items []recentLearningItem `json:"items"`
	}
	if json.Unmarshal(result, &resp) != nil {
		return nil
	}
	return resp.Items
}

// getPivotMoments returns cached pivot moment texts (refreshed every 5 minutes).
func (s *Server) getPivotMoments() []string {
	s.pivotMu.RLock()
	if time.Since(s.pivotCached) < 5*time.Minute && s.pivotTexts != nil {
		defer s.pivotMu.RUnlock()
		return s.pivotTexts
	}
	s.pivotMu.RUnlock()

	result, err := s.queryDaemon("get_learnings", map[string]any{
		"category": "pivot_moment",
	})
	if err != nil {
		s.logger.Printf("pivot moments query failed (continuing without): %v", err)
		return nil
	}

	var learnings []struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal(result, &learnings); err != nil {
		s.logger.Printf("pivot moments parse error: %v", err)
		return nil
	}

	texts := make([]string, len(learnings))
	for i, l := range learnings {
		texts[i] = l.Content
	}

	s.pivotMu.Lock()
	s.pivotTexts = texts
	s.pivotCached = time.Now()
	s.pivotMu.Unlock()

	s.logger.Printf("cached %d pivot moments from daemon", len(texts))
	return texts
}
