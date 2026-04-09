package proxy

import (
	"encoding/json"
	"fmt"
	"net/url"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/bbalet/stopwords"
	"github.com/carsteneu/yesmem/internal/briefing"
	"github.com/carsteneu/yesmem/internal/extraction"
	"github.com/carsteneu/yesmem/internal/models"
)

const associativeScoreThreshold = 38     // 0-100 scale: primary threshold for strong matches (was 45)
const associativeScoreFallback = 25      // 0-100 scale: adaptive fallback when no strong matches found (was 28)
const associativeScoreStrong = 65        // strong hybrid match (was 75)
const associativeMaxWeak = 1             // single-source matches that pass threshold
const associativeMaxStrong = 1           // strong hybrid matches (reduced from 2)
const associativeMaxTotal = 1            // absolute cap on injected results (reduced from 2)
const associativeProjectBoost = 2.0   // bonus for matching project (0-100 scale)

// findAssociativeContextFor queries the daemon for learnings relevant to the user query.
// Uses hybrid search (BM25 keyword + vector semantic) for better recall on exact terms.
// Filters stopwords from the query to reduce BM25 noise on common words.
// When the user query is too short (e.g. "ja bitte"), falls back to the last assistant
// response as context source — short confirmations typically refer to the prior suggestion.
// Returns formatted context for injection, or "".
func (s *Server) findAssociativeContextFor(userQuery, currentProject, threadID string, messages []any) string {
	searchQuery := userQuery

	// Resolve project name once (handles renames like "memory" → "yesmem")
	if currentProject != "" {
		if resolved, err := s.queryDaemon("resolve_project", map[string]any{"project_dir": currentProject}); err == nil && resolved != nil {
			var rp struct{ ProjectShort string `json:"project_short"` }
			if json.Unmarshal(resolved, &rp) == nil && rp.ProjectShort != "" {
				currentProject = rp.ProjectShort
			}
		}
	}

	// If user query is too short, use last assistant response as context source
	if !hasMeaningfulQuery(searchQuery) {
		assistantText := lastAssistantText(messages)
		if hasMeaningfulQuery(assistantText) {
			searchQuery = assistantText
		} else {
			return ""
		}
	}

	// Check for recent tool errors — enrich query with error context for better recall
	if errText := lastToolError(messages); errText != "" {
		searchQuery = searchQuery + " " + errText
	}

	// Strip stopwords to reduce BM25 noise on common verbs like "schauen", "checken", "bitte"
	cleanedQuery := cleanQueryForSearch(searchQuery)
	if cleanedQuery == "" {
		return ""
	}

	params := map[string]any{
		"query":       truncateStr(cleanedQuery, 200),
		"limit":       float64(6),
		"skip_recent": float64(3),
	}
	if currentProject != "" {
		params["project"] = currentProject
	}

	result, err := s.queryDaemon("hybrid_search", params)
	if err != nil {
		return ""
	}

	// Parse hybrid_search results — daemon returns {"results": [...], ...}
	type hybridResult struct {
		ID             string  `json:"id"`
		Content        string  `json:"content"`
		Snippet        string  `json:"snippet"`
		Score          float64 `json:"score"`
		Source         string  `json:"source"`
		LearningSource string  `json:"learning_source"`
		Project        string  `json:"project"`
		AgentRole      string  `json:"agent_role"`
	}
	var wrapped struct {
		Results []hybridResult `json:"results"`
	}
	if err := json.Unmarshal(result, &wrapped); err != nil {
		return ""
	}
	results := wrapped.Results

	// Dynamic limit: strong matches (hybrid) get more slots than weak (single-source)
	// Project-matching learnings get a score boost.
	// Score-gap logic: if #1 is 2x better than #2, only keep #1 (low relevance gap).
	type scoredLine struct {
		line  string
		score float64
		hitID float64
	}
	var accepted []scoredLine
	var fallback []scoredLine
	strongCount, weakCount := 0, 0
	for _, r := range results {
		text := r.Content
		if text == "" {
			text = r.Snippet
		}
		score := r.Score

		// Source-boost: user-stated learnings are epistemically more valuable
		switch r.LearningSource {
		case "user_stated":
			score *= 1.25
		case "agreed_upon":
			score *= 1.15
		case "hook_auto_learned":
			score *= 1.10
		}

		// Hard project filter: skip learnings from other projects entirely.
		// Cross-project learnings rarely help and mostly cause noise.
		// Uses suffix/basename matching so "greenWebsite" matches "/var/www/html/.../greenWebsite".
		if currentProject != "" && r.Project != "" && !models.ProjectMatches(r.Project, currentProject) {
			if s.logger != nil {
				s.logger.Printf("  %sassociative: [SKIP-project] project=%s score=%.3f %s%s",
					colorOrange, r.Project, score, truncateStr(text, 80), colorReset)
			}
			continue
		}

		if score < associativeScoreFallback || text == "" {
			continue
		}

		// Project-mismatch penalty: if learning has a specific project that doesn't match current, penalize hard
		if currentProject != "" && r.Project != "" && !strings.Contains(currentProject, r.Project) && !strings.Contains(r.Project, currentProject) {
			score *= 0.5
			if s.logger != nil {
				s.logger.Printf("  %sassociative: [PROJ-PENALTY] score=%.3f→%.3f project=%s %s%s",
					colorOrange, score/0.5, score, r.Project, truncateStr(text, 60), colorReset)
			}
			if score < associativeScoreFallback {
				continue
			}
		}

		// Track if this is a strong match (above primary threshold) or fallback
		isFallback := score < associativeScoreThreshold
		// Session repetition penalty: suppress back-to-back and >2x per session
		if threadID != "" && r.ID != "" {
			if id, err := strconv.ParseInt(r.ID, 10, 64); err == nil {
				if s.shouldSuppressInjection(threadID, id) {
					if s.logger != nil {
						s.logger.Printf("  %sassociative: [REP-SKIP] id=%d score=%.3f%s",
							colorOrange, id, score, colorReset)
					}
					continue
				}
			}
		}

		// Build scored line
		sl := scoredLine{
			score: score,
		}
		if r.ID != "" {
			if id, err := strconv.ParseFloat(r.ID, 64); err == nil {
				sl.hitID = id
				sl.line = fmt.Sprintf("- [ID:%d] %s", int64(id), truncateStr(text, 200))
			} else {
				sl.line = "- " + truncateStr(text, 200)
			}
		} else {
			sl.line = "- " + truncateStr(text, 200)
		}

		if isFallback {
			// In fallback range, block project-less learnings below 35 — above 35 they're likely relevant.
			if currentProject != "" && r.Project == "" && score < 35 {
				if s.logger != nil {
					s.logger.Printf("  %sassociative: [SKIP-noproj-fallback] score=%.3f %s%s",
						colorOrange, score, truncateStr(text, 80), colorReset)
				}
				continue
			}
			// Collect fallback results — used only if no strong results found
			if len(fallback) < 1 {
				fallback = append(fallback, sl)
				if s.logger != nil {
					s.logger.Printf("  %sassociative: [FALLBACK] score=%.3f %s%s",
						colorOrange, score, truncateStr(text, 80), colorReset)
				}
			}
			continue
		}

		// Absolute cap for strong results
		if len(accepted) >= associativeMaxTotal {
			if s.logger != nil {
				s.logger.Printf("  %sassociative: [CAP] score=%.3f %s%s",
					colorOrange, score, truncateStr(text, 80), colorReset)
			}
			continue
		}
		isStrong := score >= associativeScoreStrong
		if isStrong && strongCount >= associativeMaxStrong {
			if s.logger != nil {
				s.logger.Printf("  %sassociative: [CAP-strong] score=%.3f %s%s",
					colorOrange, score, truncateStr(text, 80), colorReset)
			}
			continue
		}
		if !isStrong && weakCount >= associativeMaxWeak {
			if s.logger != nil {
				s.logger.Printf("  %sassociative: [CAP-weak] score=%.3f %s%s",
					colorOrange, score, truncateStr(text, 80), colorReset)
			}
			continue
		}
		accepted = append(accepted, sl)
		if s.logger != nil {
			s.logger.Printf("  %sassociative: [INJECT] score=%.3f %s%s",
				colorBlue, score, truncateStr(text, 80), colorReset)
		}
		if isStrong {
			strongCount++
		} else {
			weakCount++
		}
	}

	// Adaptive threshold: if no strong results found, use fallback results
	if len(accepted) == 0 && len(fallback) > 0 {
		accepted = fallback[:1] // best fallback only
		if s.logger != nil {
			s.logger.Printf("  %sassociative: [ADAPTIVE] no strong matches, using fallback score=%.3f%s",
				colorOrange, fallback[0].score, colorReset)
		}
	}

	// Score-gap pruning: if best result is 2x better than second, drop the rest
	if len(accepted) > 1 && accepted[0].score >= 2.0*accepted[1].score {
		if s.logger != nil {
			s.logger.Printf("  %sassociative: [GAP-PRUNE] dropping %d (gap %.3f vs %.3f)%s",
				colorOrange, len(accepted)-1, accepted[0].score, accepted[1].score, colorReset)
		}
		accepted = accepted[:1]
	}

	// Content dedup: remove near-duplicate accepted learnings
	if len(accepted) > 1 {
		var deduped []scoredLine
		for _, sl := range accepted {
			isDupe := false
			for _, kept := range deduped {
				if extraction.BigramJaccard(sl.line, kept.line) > 0.70 {
					isDupe = true
					if s.logger != nil {
						s.logger.Printf("  %sassociative: [DEDUP] dropping %.3f (similar to %.3f) %s%s",
							colorOrange, sl.score, kept.score, truncateStr(sl.line, 60), colorReset)
					}
					break
				}
			}
			if !isDupe {
				deduped = append(deduped, sl)
			}
		}
		accepted = deduped
	}

	// Record injections for session repetition penalty; also check for contradictions
	var contradictionWarning string
	if threadID != "" {
		thisInjected := make(map[int64]bool)
		for _, sl := range accepted {
			if sl.hitID > 0 {
				thisInjected[int64(sl.hitID)] = true
			}
		}
		s.recordInjections(threadID, thisInjected)

		// Check for contradictions between newly injected and previously injected learnings
		s.lastInjectedIDsMu.Lock()
		prevCounts := s.sessionInjectCounts[threadID]
		s.lastInjectedIDsMu.Unlock()

		if len(prevCounts) > 0 && len(thisInjected) > 0 {
			var newIDs []any
			for id := range thisInjected {
				newIDs = append(newIDs, float64(id))
			}
			var prevIDs []any
			for id := range prevCounts {
				if !thisInjected[id] {
					prevIDs = append(prevIDs, float64(id))
				}
			}
			if len(newIDs) > 0 && len(prevIDs) > 0 {
				if raw, err := s.queryDaemon("get_contradicting_pairs", map[string]any{
					"new_ids": newIDs, "previous_ids": prevIDs,
				}); err == nil && raw != nil {
					var cResult struct {
						Pairs [][]int64 `json:"pairs"`
					}
					if json.Unmarshal(raw, &cResult) == nil && len(cResult.Pairs) > 0 {
						contradictionWarning = formatContradictionWarning(cResult.Pairs[0][0], cResult.Pairs[0][1])
						if s.logger != nil {
							s.logger.Printf("  %sassociative: [CONTRADICTION] %s%s", colorOrange, contradictionWarning, colorReset)
						}
					}
				}
			}
		}
	}

	var lines []string
	var scores []string
	for _, sl := range accepted {
		lines = append(lines, sl.line)
		scores = append(scores, fmt.Sprintf("%.3f", sl.score))
	}

	if len(lines) == 0 {
		// Gap detection: no relevant learnings found — track the gap
		go s.trackGap(userQuery)
		return ""
	}

	// Collect ALL above-threshold results for match_count (including capped/pruned)
	var matchIDs []float64
	for _, r := range results {
		score := r.Score
		if currentProject != "" && r.Project == currentProject {
			score += associativeProjectBoost
		}
		if score >= associativeScoreThreshold && r.ID != "" {
			if id, err := strconv.ParseFloat(r.ID, 64); err == nil {
				matchIDs = append(matchIDs, id)
			}
		}
	}

	// Collect only injected learnings for inject_count
	var injectIDs []float64
	for _, sl := range accepted {
		if sl.hitID > 0 {
			injectIDs = append(injectIDs, sl.hitID)
		}
	}

	// Fire-and-forget: bump differentiated counters
	if len(matchIDs) > 0 {
		go func() {
			idsAny := make([]any, len(matchIDs))
			for i, id := range matchIDs { idsAny[i] = id }
			s.queryDaemon("increment_match", map[string]any{"ids": idsAny})
		}()
	}
	if len(injectIDs) > 0 {
		go func() {
			idsAny := make([]any, len(injectIDs))
			for i, id := range injectIDs { idsAny[i] = id }
			s.queryDaemon("increment_inject", map[string]any{"ids": idsAny})
		}()
	}

	if contradictionWarning != "" {
		return fmt.Sprintf("[yesmem associative context]\n%s\n%s\n%s\n[/yesmem context]",
			strings.Join(lines, "\n"), contradictionWarning, InlineReflectionHint)
	}
	return fmt.Sprintf("[yesmem associative context]\n%s\n%s\n[/yesmem context]",
		strings.Join(lines, "\n"), InlineReflectionHint)
}

// formatContradictionWarning returns a warning string for a contradicting pair.
func formatContradictionWarning(newID, previousID int64) string {
	if newID == 0 || previousID == 0 {
		return ""
	}
	return fmt.Sprintf("Contradiction: [ID:%d] contradicts previously injected [ID:%d] — verify both.", newID, previousID)
}

// trackGap extracts meaningful terms from the query and sends a gap tracking request to the daemon.
func (s *Server) trackGap(query string) {
	words := strings.Fields(query)
	var terms []string
	for _, w := range words {
		w = strings.Trim(w, ".,!?:;()[]{}\"'")
		if len([]rune(w)) >= 4 {
			terms = append(terms, w)
		}
	}
	if len(terms) < 2 {
		return // Too vague to track
	}
	if len(terms) > 4 {
		terms = terms[:4]
	}
	topic := strings.Join(terms, " ")
	s.queryDaemon("track_gap", map[string]any{
		"topic": strings.ToLower(topic),
	})
}

// parseSearchResults extracts content strings from daemon search results.
// Handles both wrapped {results:[...]} and direct [...] formats.
func parseSearchResults(raw json.RawMessage) []string {
	// Try wrapped format first: {results: [{content: "..."}]}
	var wrapped struct {
		Results []struct {
			Content string `json:"content"`
			Snippet string `json:"snippet"`
			Source  string `json:"source"`
		} `json:"results"`
	}
	if err := json.Unmarshal(raw, &wrapped); err == nil && len(wrapped.Results) > 0 {
		var out []string
		for _, r := range wrapped.Results {
			text := r.Content
			if text == "" {
				text = r.Snippet
			}
			if text != "" {
				out = append(out, text)
			}
		}
		return out
	}

	// Try direct array: [{snippet: "...", content: "..."}]
	var direct []struct {
		Content string `json:"content"`
		Snippet string `json:"snippet"`
	}
	if err := json.Unmarshal(raw, &direct); err == nil {
		var out []string
		for _, r := range direct {
			text := r.Content
			if text == "" {
				text = r.Snippet
			}
			if text != "" {
				out = append(out, text)
			}
		}
		return out
	}

	return nil
}

// injectAssociativeContext appends context as a text content block to the last
// user message. This preserves the message prefix for cache hits (Sawtooth).
// Falls back to the old insert-before-last approach when SawtoothEnabled is false.
func injectAssociativeContext(messages []any, context string, sawtoothEnabled bool) []any {
	if context == "" || len(messages) < 2 {
		return messages
	}

	if sawtoothEnabled {
		return appendToLastUserMessage(messages, context)
	}

	// Legacy path: insert as separate messages before last user message
	return injectAssociativeContextLegacy(messages, context)
}

// appendToLastUserMessage adds a text content block to the last user message.
// Converts string content to content blocks if needed.
func appendToLastUserMessage(messages []any, text string) []any {
	// Find last user message
	lastUserIdx := -1
	for i := len(messages) - 1; i >= 0; i-- {
		m, ok := messages[i].(map[string]any)
		if !ok {
			continue
		}
		if m["role"] == "user" {
			lastUserIdx = i
			break
		}
	}
	if lastUserIdx < 0 {
		return messages
	}

	msg := messages[lastUserIdx].(map[string]any)

	// Ensure content is in block format ([]any of {type, text} blocks)
	var blocks []any
	switch c := msg["content"].(type) {
	case string:
		// Convert string content to block format
		blocks = []any{
			map[string]any{"type": "text", "text": c},
		}
	case []any:
		// Already blocks — shallow copy
		blocks = make([]any, len(c))
		copy(blocks, c)
	default:
		return messages
	}

	// Append the associative context as a new text block
	blocks = append(blocks, map[string]any{
		"type": "text",
		"text": "\n" + text,
	})

	// Copy the message to avoid mutating the original
	newMsg := make(map[string]any, len(msg))
	for k, v := range msg {
		newMsg[k] = v
	}
	newMsg["content"] = blocks

	// Copy messages slice with the modified last user message
	result := make([]any, len(messages))
	copy(result, messages)
	result[lastUserIdx] = newMsg
	return result
}

// injectAssociativeContextLegacy inserts context as a user message before the last
// user message, with an assistant ack to maintain alternation.
func injectAssociativeContextLegacy(messages []any, context string) []any {

	// Find last user message index
	lastUserIdx := -1
	for i := len(messages) - 1; i >= 0; i-- {
		m, ok := messages[i].(map[string]any)
		if !ok {
			continue
		}
		if m["role"] == "user" {
			lastUserIdx = i
			break
		}
	}
	if lastUserIdx < 1 {
		return messages
	}

	// Verify alternation: previous message must be assistant, not user
	if prev, ok := messages[lastUserIdx-1].(map[string]any); ok {
		if prev["role"] == "user" {
			return messages // would break user/assistant alternation
		}
	}

	// Don't inject between tool_use and tool_result — breaks API pairing
	lastMsg, _ := messages[lastUserIdx].(map[string]any)
	if hasToolResult(lastMsg) {
		return messages
	}

	contextMsg := map[string]any{
		"role":    "user",
		"content": context,
	}
	ack := map[string]any{
		"role":    "assistant",
		"content": "Noted.",
	}

	result := make([]any, 0, len(messages)+2)
	result = append(result, messages[:lastUserIdx]...)
	result = append(result, contextMsg)
	result = append(result, ack)
	result = append(result, messages[lastUserIdx:]...)
	return result
}

// hasToolResult checks if a message contains tool_result content blocks.
func hasToolResult(msg map[string]any) bool {
	blocks, ok := msg["content"].([]any)
	if !ok {
		return false
	}
	for _, block := range blocks {
		b, ok := block.(map[string]any)
		if !ok {
			continue
		}
		if b["type"] == "tool_result" {
			return true
		}
	}
	return false
}

// lastUserText returns the text of the last user message, with system-reminder
// blocks stripped so hook-injected boilerplate doesn't pollute the search query.
func lastUserText(messages []any) string {
	for i := len(messages) - 1; i >= 0; i-- {
		m, ok := messages[i].(map[string]any)
		if !ok {
			continue
		}
		if m["role"] == "user" {
			text := extractTextFromContent(m["content"])
			text = reminderPattern.ReplaceAllString(text, "")
			return strings.TrimSpace(text)
		}
	}
	return ""
}

// hasMeaningfulQuery returns true if the query has at least 3 words with >= 4 runes.
// Filters out short conversational queries ("ist das deployed?", "ok", "ja") that
// would produce irrelevant associative results via BM25 stop-word matching.
func hasMeaningfulQuery(query string) bool {
	meaningful := 0
	for _, word := range strings.Fields(query) {
		word = strings.Trim(word, ".,!?;:-'\"()")
		if len([]rune(word)) >= 4 {
			meaningful++
		}
	}
	return meaningful >= 3
}

// lastAssistantText returns the text of the last assistant message.
// Used as fallback context when the user query is too short (e.g. "ja bitte").
func lastAssistantText(messages []any) string {
	for i := len(messages) - 1; i >= 0; i-- {
		m, ok := messages[i].(map[string]any)
		if !ok {
			continue
		}
		if m["role"] == "assistant" {
			return truncateStr(extractTextFromContent(m["content"]), 500)
		}
	}
	return ""
}

// lastToolError extracts error text from the most recent tool_result with is_error=true.
// Scans backwards through recent messages (max 5) to find error context.
func lastToolError(messages []any) string {
	checked := 0
	for i := len(messages) - 1; i >= 0 && checked < 5; i-- {
		m, ok := messages[i].(map[string]any)
		if !ok {
			continue
		}
		if m["role"] != "user" {
			checked++
			continue
		}
		content, ok := m["content"].([]any)
		if !ok {
			checked++
			continue
		}
		for _, block := range content {
			b, ok := block.(map[string]any)
			if !ok {
				continue
			}
			if b["type"] == "tool_result" && b["is_error"] == true {
				if text, ok := b["content"].(string); ok && text != "" {
					// Extract first meaningful line, cap at 150 chars
					lines := strings.SplitN(text, "\n", 2)
					return truncateStr(lines[0], 150)
				}
			}
		}
		checked++
	}
	return ""
}

// cleanQueryForSearch strips DE+EN stopwords and punctuation from the user query,
// keeping only content words that carry semantic meaning. This reduces BM25 noise
// on common verbs ("schauen", "checken", "machen") while preserving technical terms.
// URLs in the query are parsed and their domains appended as additional search terms,
// so a query like "schau https://reddit.com/r/…" matches the word "reddit.com" in learnings.
func cleanQueryForSearch(query string) string {
	lower := strings.ToLower(query)
	// Remove stopwords for configured languages (from briefing.SetLanguages)
	cleaned := lower
	for _, lang := range briefing.GetLanguages() {
		cleaned = stopwords.CleanString(cleaned, lang, false)
	}

	// Strip punctuation but keep technical chars
	cleaned = strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-' || r == '.' {
			return r
		}
		return ' '
	}, cleaned)

	var words []string
	for _, w := range strings.Fields(cleaned) {
		if len([]rune(w)) >= 2 {
			words = append(words, w)
		}
	}

	// Append URL domains extracted from the original query as additional search terms.
	// This ensures "reddit.com" is a searchable token even when the full URL would be
	// mangled by stopword removal and punctuation stripping.
	for _, domain := range extractURLDomains(query) {
		words = append(words, strings.ToLower(domain))
	}

	return strings.Join(words, " ")
}

// extractURLDomains finds all https:// and http:// URLs in text and returns their hosts.
// Strips the "www." prefix so "www.reddit.com" becomes "reddit.com" for cleaner matching.
func extractURLDomains(text string) []string {
	var domains []string
	seen := make(map[string]bool)
	fields := strings.Fields(text)
	for _, f := range fields {
		if !strings.HasPrefix(f, "http://") && !strings.HasPrefix(f, "https://") {
			continue
		}
		// Trim trailing punctuation that may have been attached to the URL
		f = strings.TrimRight(f, ".,;:!?\"')")
		u, err := url.Parse(f)
		if err != nil || u.Host == "" {
			continue
		}
		host := strings.TrimPrefix(u.Host, "www.")
		if host != "" && !seen[host] {
			seen[host] = true
			domains = append(domains, host)
		}
	}
	return domains
}

// --- Doc-Chunk search for associative context ---

// docSearchResult holds a parsed doc_chunks search result.
type docSearchResult struct {
	ID           int64   `json:"id"`
	Source       string  `json:"source"`
	Version      string  `json:"version"`
	HeadingPath  string  `json:"heading_path"`
	Content      string  `json:"content"`
	Score        float64 `json:"score"`
	SourceFile   string  `json:"source_file"`
	TokensApprox int     `json:"tokens_approx"`
	IsSkill      bool    `json:"is_skill"`
}

// parseDocSearchResults parses daemon docs_search response, filtering out skills.
func parseDocSearchResults(raw json.RawMessage) []docSearchResult {
	var wrapped struct {
		Results []docSearchResult `json:"results"`
	}
	if err := json.Unmarshal(raw, &wrapped); err != nil {
		return nil
	}
	var filtered []docSearchResult
	for _, r := range wrapped.Results {
		if !r.IsSkill {
			filtered = append(filtered, r)
		}
	}
	return filtered
}

// formatDocResult creates a formatted doc context block for injection.
func formatDocResult(source, version, heading, content string) string {
	if content == "" {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("[yesmem doc context]\n")
	label := source
	if version != "" {
		label += " " + version
	}
	if heading != "" {
		label += " > " + heading
	}
	fmt.Fprintf(&sb, "Quelle: %s\n%s\n", label, content)
	sb.WriteString("[/yesmem doc context]")
	return sb.String()
}

// fileToolNames maps tool names to their file path input key.
var fileToolNames = map[string]string{
	"Read":         "file_path",
	"Edit":         "file_path",
	"Write":        "file_path",
	"NotebookEdit": "notebook_path",
}

// extractFileExtensionsFromMessages scans the last 10 messages for file-related
// tool_use blocks and matches file paths against registered trigger_extensions.
// registeredExts sorted longest-first for greedy compound matching.
func extractFileExtensionsFromMessages(messages []any, registeredExts []string) []string {
	const scanWindow = 10
	start := 0
	if len(messages) > scanWindow {
		start = len(messages) - scanWindow
	}

	sorted := make([]string, len(registeredExts))
	copy(sorted, registeredExts)
	sort.Slice(sorted, func(i, j int) bool { return len(sorted[i]) > len(sorted[j]) })

	seen := make(map[string]bool)
	for _, raw := range messages[start:] {
		msg, ok := raw.(map[string]any)
		if !ok || msg["role"] != "assistant" {
			continue
		}
		blocks, ok := msg["content"].([]any)
		if !ok {
			continue
		}
		for _, block := range blocks {
			b, ok := block.(map[string]any)
			if !ok || b["type"] != "tool_use" {
				continue
			}
			name, _ := b["name"].(string)
			pathKey, ok := fileToolNames[name]
			if !ok {
				continue
			}
			input, ok := b["input"].(map[string]any)
			if !ok {
				continue
			}
			filePath, _ := input[pathKey].(string)
			if filePath == "" {
				continue
			}
			lower := strings.ToLower(filePath)
			for _, ext := range sorted {
				if strings.HasSuffix(lower, ext) {
					seen[ext] = true
					break
				}
			}
		}
	}

	var exts []string
	for ext := range seen {
		exts = append(exts, ext)
	}
	return exts
}

// findDocContextFor queries daemon docs_search for relevant doc chunks.
// Uses OR matching with minimum term overlap filter for balanced recall/precision.
// Returns formatted context for injection, or "".
// NOTE: Auto-injection disabled — docs_search should be called explicitly during planning.
// Decision: "correct != useful" — only Claude knows when docs are needed.
// The improved docs_search infrastructure (AND-matching, sourceIDs, doc_type) remains as MCP tool.
func (s *Server) findDocContextFor(userQuery, project string, messages []any) string {
	return "" // disabled: auto-injection produces noise (correct != useful). See decision #45621.
	/* preserved for future use — re-enable when plan-based docs_search reminder is implemented
	cleanedQuery := cleanQueryForSearch(userQuery)
	if cleanedQuery == "" {
		// No user text — try extracting query from coding context (Edit/Write)
		codingQuery, codingExt := extractCodingQuery(messages)
		if codingQuery == "" {
			if s.logger != nil {
				s.logger.Printf("  %sdoc-context: empty query, no coding context, skipping%s", colorOrange, colorReset)
			}
			return ""
		}
		if s.logger != nil {
			s.logger.Printf("  %sdoc-context: coding-query=%q ext=%s%s", colorBlue, truncateStr(codingQuery, 60), codingExt, colorReset)
		}
		return s.findDocsByCodingQuery(codingQuery, codingExt, project)
	}

	queryTerms := strings.Fields(cleanedQuery)
	if len(queryTerms) < 2 {
		if s.logger != nil {
			s.logger.Printf("  %sdoc-context: too few terms (%d), skipping%s", colorOrange, len(queryTerms), colorReset)
		}
		// return s.findDocsByFileExtensions(messages, project) // disabled: extension fallback produces noise
		return ""
	}

	// Send raw cleaned query — daemon handles tiered AND search (5→4→3→2 terms)
	params := map[string]any{
		"query": cleanedQuery,
		"limit": float64(3),
	}
	if project != "" {
		params["project"] = project
	}
	result, err := s.queryDaemon("docs_search", params)
	if err != nil {
		if s.logger != nil {
			s.logger.Printf("  %sdoc-context: daemon error: %v%s", colorOrange, err, colorReset)
		}
		// return s.findDocsByFileExtensions(messages, project) // disabled: extension fallback produces noise
		return ""
	}

	results := parseDocSearchResults(result)
	if len(results) == 0 {
		if s.logger != nil {
			s.logger.Printf("  %sdoc-context: 0 text results, skipping%s", colorOrange, colorReset)
		}
		// return s.findDocsByFileExtensions(messages, project) // disabled: extension fallback produces noise
		return ""
	}

	// Take best result from daemon (already tier-ranked)
	r := results[0]
	content := truncateStr(r.Content, 500)

	formatted := formatDocResult(r.Source, r.Version, r.HeadingPath, content)
	if formatted != "" && s.logger != nil {
		s.logger.Printf("  %sdoc-context: [INJECT] source=%s score=%.3f %s%s",
			colorBlue, r.Source, r.Score, truncateStr(content, 80), colorReset)
	}
	return formatted
	*/
}

// findDocsByFileExtensions extracts file extensions from recent tool_use blocks
// and queries the daemon for docs matching those extensions.
func (s *Server) findDocsByFileExtensions(messages []any, project string) string {
	listParams := map[string]any{}
	if project != "" {
		listParams["project"] = project
	}
	listResult, err := s.queryDaemon("list_trigger_extensions", listParams)
	if err != nil {
		if s.logger != nil {
			s.logger.Printf("  %sdoc-ext-fallback: list_trigger_extensions error: %v%s", colorOrange, err, colorReset)
		}
		return ""
	}
	var listResp struct {
		Extensions []string `json:"extensions"`
	}
	if err := json.Unmarshal(listResult, &listResp); err != nil {
		if s.logger != nil {
			s.logger.Printf("  %sdoc-ext-fallback: unmarshal error: %v%s", colorOrange, err, colorReset)
		}
		return ""
	}
	if len(listResp.Extensions) == 0 {
		return ""
	}

	exts := extractFileExtensionsFromMessages(messages, listResp.Extensions)
	if len(exts) == 0 {
		return ""
	}

	extArgs := make([]any, len(exts))
	for i, e := range exts {
		extArgs[i] = e
	}
	params := map[string]any{"extensions": extArgs, "limit": float64(1)}
	if project != "" {
		params["project"] = project
	}

	result, err := s.queryDaemon("contextual_docs", params)
	if err != nil {
		if s.logger != nil {
			s.logger.Printf("  %sdoc-ext-fallback: contextual_docs error: %v%s", colorOrange, err, colorReset)
		}
		return ""
	}

	// Dedicated parsing — contextual_docs returns {source, version, heading_path, content, tokens_approx}
	// which differs from docs_search (no id, score, is_skill fields).
	var resp struct {
		Results []struct {
			Source      string `json:"source"`
			Version     string `json:"version"`
			HeadingPath string `json:"heading_path"`
			Content     string `json:"content"`
		} `json:"results"`
	}
	if err := json.Unmarshal(result, &resp); err != nil {
		if s.logger != nil {
			s.logger.Printf("  %sdoc-ext-fallback: contextual_docs unmarshal error: %v%s", colorOrange, err, colorReset)
		}
		return ""
	}
	if len(resp.Results) == 0 {
		return ""
	}

	r := resp.Results[0]
	content := truncateStr(r.Content, 500)

	formatted := formatDocResult(r.Source, r.Version, r.HeadingPath, content)
	if formatted != "" && s.logger != nil {
		s.logger.Printf("  %sdoc-context: [INJECT via extension] source=%s exts=%v%s",
			colorBlue, r.Source, exts, colorReset)
	}
	return formatted
}

// findDocsByCodingQuery searches docs using a query extracted from written code.
// Uses docs_search (BM25 + vector fallback) filtered to sources matching the file extension.
// Only searches "reference" docs (not "style" guides).
func (s *Server) findDocsByCodingQuery(query, ext, project string) string {
	params := map[string]any{
		"query":    truncateStr(query, 200),
		"limit":    float64(1),
		"doc_type": "reference",
	}
	if project != "" {
		params["project"] = project
	}
	if ext != "" {
		params["extensions"] = []any{ext}
	}

	result, err := s.queryDaemon("docs_search", params)
	if err != nil {
		if s.logger != nil {
			s.logger.Printf("  %sdoc-context: coding-query search error: %v%s", colorOrange, err, colorReset)
		}
		return ""
	}

	results := parseDocSearchResults(result)
	if len(results) == 0 {
		return ""
	}

	r := results[0]
	content := truncateStr(r.Content, 500)
	formatted := formatDocResult(r.Source, r.Version, r.HeadingPath, content)
	if formatted != "" && s.logger != nil {
		s.logger.Printf("  %sdoc-context: [CODING-INJECT] source=%s score=%.3f %s%s",
			colorBlue, r.Source, r.Score, truncateStr(content, 80), colorReset)
	}
	return formatted
}

// shouldSuppressInjection returns true if this learning should be skipped
// due to session repetition limits (max 2x per session, no back-to-back).
func (s *Server) shouldSuppressInjection(threadID string, learningID int64) bool {
	s.lastInjectedIDsMu.Lock()
	defer s.lastInjectedIDsMu.Unlock()

	// Back-to-back: was this ID injected in the previous turn?
	if prev := s.lastTurnInjected[threadID]; prev != nil && prev[learningID] {
		return true
	}
	// Session max: injected >= 2 times already?
	if counts := s.sessionInjectCounts[threadID]; counts != nil && counts[learningID] >= 2 {
		return true
	}
	return false
}

// recordInjections updates session injection tracking after a turn completes.
// Sets lastTurnInjected to the current turn's IDs and increments session counts.
func (s *Server) recordInjections(threadID string, injectedIDs map[int64]bool) {
	s.lastInjectedIDsMu.Lock()
	defer s.lastInjectedIDsMu.Unlock()

	s.lastTurnInjected[threadID] = injectedIDs
	if s.sessionInjectCounts[threadID] == nil {
		s.sessionInjectCounts[threadID] = make(map[int64]int)
	}
	for id := range injectedIDs {
		s.sessionInjectCounts[threadID][id]++
	}
}

// extractCodingQuery scans the last 5 assistant messages backwards for Edit/Write
// tool_use blocks and extracts a meaningful search query from the tool input.
// Cleans via cleanQueryForSearch, caps at 120 chars.
// Returns (query, extension) where extension may be compound (e.g. ".html.twig").
func extractCodingQuery(messages []any) (string, string) {
	const scanWindow = 5
	start := 0
	if len(messages) > scanWindow {
		start = len(messages) - scanWindow
	}

	// Walk backwards to find the most recent matching tool_use
	for i := len(messages) - 1; i >= start; i-- {
		msg, ok := messages[i].(map[string]any)
		if !ok || msg["role"] != "assistant" {
			continue
		}
		blocks, ok := msg["content"].([]any)
		if !ok {
			continue
		}
		for _, block := range blocks {
			b, ok := block.(map[string]any)
			if !ok || b["type"] != "tool_use" {
				continue
			}
			name, _ := b["name"].(string)
			input, ok := b["input"].(map[string]any)
			if !ok {
				continue
			}

			switch name {
			case "Edit":
				filePath, _ := input["file_path"].(string)
				content, _ := input["new_string"].(string)
				if content == "" {
					continue
				}
				query := cleanQueryForSearch(content)
				if len(query) < 30 {
					continue // too short for meaningful doc search
				}
				if len(query) > 120 {
					query = query[:120]
				}
				return query, extractExtension(filePath)

			case "Write":
				filePath, _ := input["file_path"].(string)
				content, _ := input["content"].(string)
				if content == "" {
					continue
				}
				query := cleanQueryForSearch(content)
				if len(query) < 30 {
					continue
				}
				if len(query) > 120 {
					query = query[:120]
				}
				return query, extractExtension(filePath)
			}
		}
	}
	return "", ""
}

// extractExtension returns the file extension from a path, handling compound extensions
// like ".html.twig", ".xml.twig", ".txt.twig". Falls back to the simple last extension.
// Returns "" if no extension is found.
func extractExtension(path string) string {
	if path == "" {
		return ""
	}
	base := filepath.Base(path)
	lower := strings.ToLower(base)

	// Compound extension support: check for known double-extension patterns ending in .twig
	twigCompounds := []string{".html.twig", ".xml.twig", ".txt.twig", ".js.twig", ".css.twig", ".json.twig"}
	for _, compound := range twigCompounds {
		if strings.HasSuffix(lower, compound) {
			return compound
		}
	}

	// Simple extension: last dot onwards
	if idx := strings.LastIndex(base, "."); idx > 0 {
		return strings.ToLower(base[idx:])
	}
	return ""
}
