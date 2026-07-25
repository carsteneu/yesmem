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

// briefingEntry is a per-thread cache slot that remembers which project the
// snapshot was loaded for. A thread may switch working directory mid-session
// (e.g. when the user moves into a worktree), so we evict on project change.
type briefingEntry struct {
	project string
	text    string
	codeMap string
}

// getCachedBriefing returns the cached briefing + code map for a given thread.
// A miss (ok=false) happens when:
//   - the thread has no entry yet,
//   - the threadID or project is empty (we refuse to cache unattributable data),
//   - the cached entry was loaded for a different project (thread switched CWD).
//
// Keying by threadID is deliberate: frozenStubs and capsCache are both per-thread,
// so the briefing cache MUST follow the same scope. A project-scoped cache would
// let a sawtooth refreeze on thread A invalidate thread B's message-prefix hash.
func (s *Server) getCachedBriefing(threadID, project string) (text, codeMap string, ok bool) {
	if threadID == "" || project == "" {
		return "", "", false
	}
	s.briefingMu.RLock()
	defer s.briefingMu.RUnlock()
	entry, exists := s.briefingCache[threadID]
	if !exists || entry.project != project {
		return "", "", false
	}
	return entry.text, entry.codeMap, true
}

// setCachedBriefing stores the briefing + code map for a specific thread.
// Empty threadID or project is ignored — we refuse to cache briefings we could
// not attribute. Writing a new (threadID, project) pair overwrites any previous
// entry for THAT thread (project switch eviction for the same thread).
func (s *Server) setCachedBriefing(threadID, project, text, codeMap string) {
	if threadID == "" || project == "" {
		return
	}
	s.briefingMu.Lock()
	defer s.briefingMu.Unlock()
	if s.briefingCache == nil {
		s.briefingCache = make(map[string]briefingEntry)
	}
	s.briefingCache[threadID] = briefingEntry{
		project: project,
		text:    text,
		codeMap: codeMap,
	}
}

// invalidateBriefingForThread drops the cached briefing+codemap for one thread
// so the next request on that thread reloads from the daemon. Sawtooth refreeze
// calls this via invalidateThreadCaches; other threads stay untouched.
func (s *Server) invalidateBriefingForThread(threadID string) {
	if threadID == "" {
		return
	}
	s.briefingMu.Lock()
	defer s.briefingMu.Unlock()
	delete(s.briefingCache, threadID)
}

// composeBriefingText returns the base briefing text with the rendered narrative
// appended after a blank-line separator. Nil or empty-Render narrative leaves the
// base text unchanged. This is called inside loadBriefing so the sawtooth refresh
// path captures the current narrative state into the cached briefing; the turn
// pair stays byte-stable until the next sawtooth refresh fires.
func composeBriefingText(base string, n *Narrative) string {
	if n == nil {
		return base
	}
	narrative := n.Render()
	if narrative == "" {
		return base
	}
	if base == "" {
		return narrative
	}
	return base + "\n\n" + narrative
}

// loadBriefing fetches the briefing text and code map from the daemon via generate_briefing RPC.
func (s *Server) loadBriefing(project, projectDir string) briefingData {
	if project == "" {
		s.logger.Printf("[briefing] skipped: no project name")
		return briefingData{}
	}
	result, err := s.queryDaemon("generate_briefing", map[string]any{
		"project":     daemonProject(project, projectDir),
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
		composed := composeBriefingText(text, s.narrative)
		s.logger.Printf("[briefing] loaded (raw): %db (composed %db w/ narrative)", len(text), len(composed))
		return briefingData{Text: composed}
	}
	composedText := composeBriefingText(resp.Text, s.narrative)
	if composedText != "" {
		s.logger.Printf("[briefing] loaded: %db composed (base %db) + %db code_map for project=%s", len(composedText), len(resp.Text), len(resp.CodeMap), project)
	} else {
		s.logger.Printf("[briefing] empty response for project=%s", project)
	}
	return briefingData{Text: composedText, CodeMap: resp.CodeMap}
}

// refreshBriefing forces a briefing reload for one thread only. Called during
// sawtooth stub-cycles via invalidateThreadCaches: when thread X refreezes
// we reload its briefing+codemap so the next request rebuilds its cached
// prefix from scratch. Other threads sharing this project are untouched.
func (s *Server) refreshBriefing(threadID, project, projectDir string) {
	loader := s.briefingLoader
	if loader == nil {
		loader = s.loadBriefing
	}
	if data := loader(project, projectDir); data.Text != "" {
		s.setCachedBriefing(threadID, project, data.Text, data.CodeMap)
		s.logger.Printf("[briefing] refreshed during stub-cycle: tid=%s %db text + %db codemap", threadID, len(data.Text), len(data.CodeMap))
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
	text, _, ok := s.getCachedBriefing(threadID, proj)

	// Lazy-init: load briefing + code map on first request for this thread, or
	// reload when the thread switched project (rare — usually means a new CWD).
	// Per-thread scoping prevents sawtooth refreezes on one thread from
	// invalidating another thread's cached message prefix.
	if !ok && proj != "" && threadID != "" {
		data := s.loadBriefing(proj, extractWorkingDirectory(req))
		s.setCachedBriefing(threadID, proj, data.Text, data.CodeMap)
		text = data.Text
	}

	if text == "" {
		if proj != "" {
			s.logger.Printf("%s[briefing] WARN: empty briefing for project=%s, injection skipped%s", colorOrange, proj, colorReset)
		}
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
	_, cm, _ := s.getCachedBriefing(threadID, proj)

	if cm == "" {
		s.logger.Printf("[codemap] skip: empty for project=%s", proj)
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
