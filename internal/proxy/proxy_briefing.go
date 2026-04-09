package proxy

import (
	"encoding/json"
	"strings"
	"time"
)

// loadBriefing fetches the briefing text from the daemon via generate_briefing RPC.
func (s *Server) loadBriefing(project string) string {
	if project == "" {
		s.logger.Printf("[briefing] skipped: no project name")
		return ""
	}
	result, err := s.queryDaemon("generate_briefing", map[string]any{
		"project": project,
	})
	if err != nil {
		s.logger.Printf("%s[briefing] daemon error: %v%s", colorRed, err, colorReset)
		return ""
	}
	var resp struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(result, &resp) != nil {
		// Fallback: raw string
		text := strings.Trim(string(result), "\"")
		s.logger.Printf("[briefing] loaded (raw): %db", len(text))
		return text
	}
	if resp.Text != "" {
		s.logger.Printf("[briefing] loaded: %db for project=%s", len(resp.Text), project)
	} else {
		s.logger.Printf("[briefing] empty response for project=%s", project)
	}
	return resp.Text
}

// refreshBriefing forces a briefing reload. Called during sawtooth stub-cycles.
func (s *Server) refreshBriefing(project string) {
	if text := s.loadBriefing(project); text != "" {
		s.briefingMu.Lock()
		s.briefingText = text
		s.briefingMu.Unlock()
		s.logger.Printf("[briefing] refreshed during stub-cycle: %db", len(text))
	}
}

// recentLearningItem represents a recently remembered learning with its ID.
type recentLearningItem struct {
	ID   int64  `json:"id"`
	Text string `json:"text"`
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
