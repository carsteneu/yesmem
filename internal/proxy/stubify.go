package proxy

import (
	"encoding/json"
	"fmt"
	"strings"
)

// decisionMarkers are signal words in user messages that indicate a decision.
// Messages containing these are NEVER stubbed or decayed.
var decisionMarkers = []string{
	"nimm", "mach", "ja bitte", "nein", "bitte nicht",
	"so lassen", "lass uns", "entscheidung", "option", "variante",
	"ansatz", "take", "use", "go with", "let's do", "ja -", "ok -",
}

// StubResult holds the outcome of a stubify pass.
type StubResult struct {
	Messages      []any // modified messages array
	Archived      []any // original messages that were stubbed
	TokensBefore  int
	TokensAfter   int
	StubCount     int
}

// Stubify inspects the messages array and replaces older messages with reference stubs
// when the estimated token count exceeds the threshold.
// It preserves: first message (system), last keepRecent messages, decision messages.
// If decay is non-nil, already-stubbed messages are further compressed based on age.
func Stubify(messages []any, threshold, keepRecent, requestIdx int, annotations map[string]string, pivotTexts []string, estimateTokens TokenEstimateFunc, decay ...*DecayTracker) StubResult {
	return stubifyInternal(messages, threshold, keepRecent, requestIdx, annotations, pivotTexts, estimateTokens, 0, decay...)
}

// StubifyWithTotal is like Stubify but uses a known token total for the threshold check
// instead of re-counting all messages. The known total comes from actual API response data.
func StubifyWithTotal(messages []any, threshold, keepRecent, requestIdx int, annotations map[string]string, pivotTexts []string, estimateTokens TokenEstimateFunc, knownTotal int, decay ...*DecayTracker) StubResult {
	return stubifyInternal(messages, threshold, keepRecent, requestIdx, annotations, pivotTexts, estimateTokens, knownTotal, decay...)
}

func stubifyInternal(messages []any, threshold, keepRecent, requestIdx int, annotations map[string]string, pivotTexts []string, estimateTokens TokenEstimateFunc, knownTotal int, decay ...*DecayTracker) StubResult {
	result := StubResult{
		Messages: messages,
	}

	var totalTokens int
	if knownTotal > 0 {
		totalTokens = knownTotal
	} else {
		totalTokens = estimateTokensFromMessages(messages, estimateTokens)
	}
	result.TokensBefore = totalTokens
	runningTokens := totalTokens

	if totalTokens <= threshold {
		result.TokensAfter = totalTokens
		return result
	}

	// Determine protected zones
	// Zone 1: First message (system prompt or compact summary)
	// Zone 2: Last keepRecent messages
	protectedTail := len(messages) - keepRecent
	if protectedTail < 1 {
		protectedTail = 1
	}

	// Extract optional decay tracker
	var dt *DecayTracker
	if len(decay) > 0 && decay[0] != nil {
		dt = decay[0]
	}

	// Build tool_use_id → keywords map for deep_search hints
	toolUseInfo := buildToolUseInfo(messages)

	// Compute emotional intensity for decay
	intensity := estimateIntensity(messages)

	// Compute token pressure for adaptive decay boundaries
	pressure := float64(totalTokens) / float64(threshold)

	// Work on a copy
	modified := make([]any, len(messages))
	copy(modified, messages)
	var archived []any

	// Walk from oldest to newest (skip first, stop before tail)
	for i := 1; i < protectedTail; i++ {
		msg, ok := modified[i].(map[string]any)
		if !ok {
			continue
		}

		role, _ := msg["role"].(string)
		content := msg["content"]

		// Check if this message is a decision or pivot — these decay slower, not never
		isProtectedMsg := false
		if role == "user" && isDecision(modified, i) {
			isProtectedMsg = true
		}
		if isProtectedExtended(modified, i, pivotTexts) {
			isProtectedMsg = true
		}

		// Protected messages get high emotional intensity (decay 3-4x slower)
		// but they DO decay — they're not exempt forever
		msgIntensity := intensity
		if isProtectedMsg {
			msgIntensity = 0.95 // +19 request boost before decay starts
		}

		// Estimate tokens for this message before stubbing (content-based)
		origTokens := estimateMessageContentTokens(msg, func(text string) int {
			return estimateTokens(text)
		})

		// Hard floor: never stub messages ≤80 tokens — nothing to gain.
		// Exception: tool_use blocks are small but their paired tool_result
		// (force-stubbed below) can be huge, so always allow those through.
		if origTokens <= 80 && !hasToolUseContent(content) {
			continue
		}

		// Stub the content blocks
		stubbed, wasStubbable := stubContent(content, role, requestIdx, annotations, toolUseInfo)
		if !wasStubbable {
			continue
		}

		// Apply progressive decay if tracker available
		if dt != nil {
			dt.MarkStubbed(i, requestIdx, msgIntensity)
			dt.SetFilePath(i, extractFilePathFromContent(content))
			stage := dt.GetStage(i, requestIdx, len(messages), pressure)
			stubbed = applyDecayToContent(stubbed, stage, role)
		}

		// Archive original
		archived = append(archived, messages[i])

		// Replace with stubbed version
		newMsg := make(map[string]any)
		for k, v := range msg {
			newMsg[k] = v
		}
		newMsg["content"] = stubbed
		modified[i] = newMsg
		result.StubCount++

		// Incremental token update (avoid O(n) remarshal per stub)
		stubbedTokens := estimateMessageContentTokens(newMsg, func(text string) int {
			return estimateTokens(text)
		})
		runningTokens -= (origTokens - stubbedTokens)

		// If we stubbed a tool_use, force-stub the next tool_result too
		// (API requires tool_result to follow its matching tool_use)
		// Note: must reach into protected tail — orphaned tool_result causes API 400
		if role == "assistant" && hasToolUseContent(content) && i+1 < len(modified) {
			if nextMsg, ok := modified[i+1].(map[string]any); ok {
				if nextMsg["role"] == "user" && hasToolResultContent(nextMsg["content"]) {
					nextStubbed, nextWasStubbable := stubContent(nextMsg["content"], "user", requestIdx, annotations, toolUseInfo)
					if nextWasStubbable {
						nextOrigTokens := estimateMessageContentTokens(nextMsg, func(text string) int {
							return estimateTokens(text)
						})
						nextNew := make(map[string]any)
						for k, v := range nextMsg {
							nextNew[k] = v
						}
						nextNew["content"] = nextStubbed
						modified[i+1] = nextNew
						archived = append(archived, messages[i+1])
						result.StubCount++
						nextNewTokens := estimateMessageContentTokens(nextNew, func(text string) int {
							return estimateTokens(text)
						})
						runningTokens -= (nextOrigTokens - nextNewTokens)
						i++ // skip next message in loop
					}
				}
			}
		}

		// Stubify stubs all eligible messages (no floor).
		// The actual token floor is controlled by Collapse downstream.
	}

	result.Messages = modified
	result.Archived = archived
	result.TokensAfter = runningTokens
	return result
}

// stubContent replaces content blocks with stubs based on their type.
// Returns the stubbed content and whether any stubbing was done.
func stubContent(content any, role string, requestIdx int, annotations map[string]string, toolUseInfo map[string]string) (any, bool) {
	// Content can be a string or an array of blocks
	switch c := content.(type) {
	case string:
		return stubText(c, role, requestIdx)
	case []any:
		return stubBlocks(c, role, requestIdx, annotations, toolUseInfo)
	default:
		return content, false
	}
}

// stubText handles plain text content (string form).
func stubText(text, role string, requestIdx int) (string, bool) {
	if role == "user" {
		if len(text) < 800 {
			return text, false
		}
		return truncateText(text, 500), true
	}
	// assistant
	if len(text) < 400 {
		return text, false
	}
	limit := 300
	if hasStructuredContent(text) {
		limit = 500
	}
	return truncateText(text, limit), true
}

// stubBlocks handles array-of-blocks content.
func stubBlocks(blocks []any, role string, requestIdx int, annotations map[string]string, toolUseInfo map[string]string) ([]any, bool) {
	anyStubbed := false
	result := make([]any, 0, len(blocks))

	for _, block := range blocks {
		b, ok := block.(map[string]any)
		if !ok {
			result = append(result, block)
			continue
		}

		blockType, _ := b["type"].(string)

		switch blockType {
		case "thinking":
			// Remove completely — zero information value for context
			anyStubbed = true
			continue

		case "tool_use":
			stub := stubToolUse(b, annotations)
			result = append(result, map[string]any{
				"type": "text",
				"text": stub,
			})
			anyStubbed = true

		case "tool_result":
			toolUseID, _ := b["tool_use_id"].(string)
			hint := ""
			if kw, ok := toolUseInfo[toolUseID]; ok && kw != "" {
				hint = fmt.Sprintf(" → deep_search('%s')", kw)
			}
			stub := fmt.Sprintf("[tool result archived%s]", hint)
			result = append(result, map[string]any{
				"type": "text",
				"text": stub,
			})
			anyStubbed = true

		case "text":
			text, _ := b["text"].(string)
			stubbed, wasStubbable := stubText(text, role, requestIdx)
			if wasStubbable {
				result = append(result, map[string]any{
					"type": "text",
					"text": stubbed,
				})
				anyStubbed = true
			} else {
				result = append(result, block)
			}

		default:
			result = append(result, block)
		}
	}

	return result, anyStubbed
}

// stubToolUse creates a reference stub for a tool_use block.
func stubToolUse(b map[string]any, annotations map[string]string) string {
	name, _ := b["name"].(string)
	id, _ := b["id"].(string)
	input, _ := b["input"].(map[string]any)

	var stub string
	switch name {
	case "Read":
		path, _ := input["file_path"].(string)
		stub = fmt.Sprintf("[→] Read %s", path)
	case "Bash":
		cmd, _ := input["command"].(string)
		stub = fmt.Sprintf("[→] Bash: %s", truncateStr(cmd, 80))
	case "Edit":
		path, _ := input["file_path"].(string)
		stub = fmt.Sprintf("[→] Edit %s — file on disk is current", path)
	case "Write":
		path, _ := input["file_path"].(string)
		stub = fmt.Sprintf("[→] Write %s — file on disk is current", path)
	case "Grep":
		pattern, _ := input["pattern"].(string)
		path, _ := input["path"].(string)
		stub = fmt.Sprintf("[→] Grep '%s' in %s", truncateStr(pattern, 40), path)
	case "Glob":
		pattern, _ := input["pattern"].(string)
		stub = fmt.Sprintf("[→] Glob '%s'", pattern)
	case "Agent":
		desc, _ := input["description"].(string)
		stub = fmt.Sprintf("[→] Agent: %s", truncateStr(desc, 60))
	case "WebFetch":
		url, _ := input["url"].(string)
		stub = fmt.Sprintf("[→] WebFetch %s", truncateStr(url, 60))
	case "WebSearch":
		query, _ := input["query"].(string)
		stub = fmt.Sprintf("[→] WebSearch '%s'", truncateStr(query, 60))
	default:
		stub = fmt.Sprintf("[→] %s", name)
	}

	// Attach annotation if available
	if annotations != nil {
		if ann, ok := annotations[id]; ok && ann != "" {
			stub += " — " + ann
		}
	}

	// Append deep_search hint for recovery
	kw := extractToolKeywords(name, input)
	stub += fmt.Sprintf(" → deep_search('%s')", kw)

	return stub
}

// extractTextFromContent extracts text from any content type (string or blocks).
func extractTextFromContent(content any) string {
	switch c := content.(type) {
	case string:
		return c
	case []any:
		var sb strings.Builder
		for _, block := range c {
			if b, ok := block.(map[string]any); ok {
				if t, ok := b["text"].(string); ok {
					sb.WriteString(t)
					sb.WriteByte(' ')
				}
			}
		}
		return strings.TrimSpace(sb.String())
	default:
		return ""
	}
}

// matchesDecisionKeywords checks for explicit decision signal words (fallback).
func matchesDecisionKeywords(text string) bool {
	lower := strings.ToLower(text)
	for _, marker := range decisionMarkers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// isDecision checks if a user message is a decision using structural signals.
// Language-agnostic: decisions are short, come after analysis, and aren't questions.
func isDecision(messages []any, currentIdx int) bool {
	msg, ok := messages[currentIdx].(map[string]any)
	if !ok {
		return false
	}

	text := extractTextFromContent(msg["content"])
	trimmed := strings.TrimSpace(text)
	isQuestion := strings.HasSuffix(trimmed, "?")

	// 1. Very short confirmation (< 30 chars, no ?) → almost always a decision
	if len([]rune(trimmed)) < 30 && !isQuestion {
		return true
	}

	// 2. Short answer (< 100 chars) to long analysis (> 400 chars)
	if len([]rune(trimmed)) < 100 && !isQuestion && currentIdx > 0 {
		if prev, ok := messages[currentIdx-1].(map[string]any); ok {
			if prev["role"] == "assistant" {
				prevText := extractTextFromContent(prev["content"])
				if len([]rune(prevText)) > 400 {
					return true
				}
			}
		}
	}

	// 3. Fallback: keyword match for edge cases
	return matchesDecisionKeywords(text)
}

// buildToolUseInfo extracts tool_use_id → keywords mapping from all messages.
func buildToolUseInfo(messages []any) map[string]string {
	info := make(map[string]string)
	for _, msg := range messages {
		m, ok := msg.(map[string]any)
		if !ok {
			continue
		}
		blocks, ok := m["content"].([]any)
		if !ok {
			continue
		}
		for _, block := range blocks {
			b, ok := block.(map[string]any)
			if !ok {
				continue
			}
			if b["type"] != "tool_use" {
				continue
			}
			id, _ := b["id"].(string)
			name, _ := b["name"].(string)
			input, _ := b["input"].(map[string]any)
			info[id] = extractToolKeywords(name, input)
		}
	}
	return info
}

// extractToolKeywords returns concise search keywords for a tool_use.
func extractToolKeywords(name string, input map[string]any) string {
	switch name {
	case "Read", "Edit", "Write":
		path, _ := input["file_path"].(string)
		return name + " " + path
	case "Bash":
		cmd, _ := input["command"].(string)
		return "Bash " + truncateStr(cmd, 40)
	case "Grep":
		pattern, _ := input["pattern"].(string)
		path, _ := input["path"].(string)
		return "Grep " + pattern + " " + path
	case "Glob":
		pattern, _ := input["pattern"].(string)
		return "Glob " + pattern
	case "Agent":
		desc, _ := input["description"].(string)
		return "Agent " + desc
	case "WebSearch":
		query, _ := input["query"].(string)
		return query
	case "WebFetch":
		url, _ := input["url"].(string)
		return url
	default:
		return name
	}
}

// extractFilePathFromContent extracts the first file_path from tool_use blocks in content.
func extractFilePathFromContent(content any) string {
	blocks, ok := content.([]any)
	if !ok {
		return ""
	}
	for _, block := range blocks {
		b, ok := block.(map[string]any)
		if !ok {
			continue
		}
		if b["type"] != "tool_use" {
			continue
		}
		if input, ok := b["input"].(map[string]any); ok {
			if fp, ok := input["file_path"].(string); ok && fp != "" {
				return fp
			}
		}
	}
	return ""
}

// isProtected checks if a message text overlaps significantly with any pivot moment.
// Uses word overlap: if >= 3 significant words (len > 3) match, the message is protected.
func isProtected(text string, pivotTexts []string) bool {
	if len(pivotTexts) == 0 || text == "" {
		return false
	}
	textWords := significantWords(text)
	for _, pivot := range pivotTexts {
		pivotWords := significantWords(pivot)
		if countOverlap(textWords, pivotWords) >= 3 {
			return true
		}
	}
	return false
}

func significantWords(text string) map[string]bool {
	words := make(map[string]bool)
	for _, w := range strings.Fields(strings.ToLower(text)) {
		if len(w) > 3 {
			words[w] = true
		}
	}
	return words
}

func countOverlap(a, b map[string]bool) int {
	count := 0
	for w := range a {
		if b[w] {
			count++
		}
	}
	return count
}

// hasToolUseContent checks if content contains tool_use blocks.
func hasToolUseContent(content any) bool {
	blocks, ok := content.([]any)
	if !ok {
		return false
	}
	for _, block := range blocks {
		b, ok := block.(map[string]any)
		if !ok {
			continue
		}
		if b["type"] == "tool_use" {
			return true
		}
	}
	return false
}

// hasToolResultContent checks if content contains tool_result blocks.
func hasToolResultContent(content any) bool {
	blocks, ok := content.([]any)
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

// hasStructuredContent checks for tables or code blocks.
func hasStructuredContent(text string) bool {
	return strings.Contains(text, "|---|") || strings.Contains(text, "```")
}

// truncateText truncates at a char limit and adds "..."
func truncateText(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit]) + "..."
}

// truncateStr truncates a string for use in stubs (rune-safe).
func truncateStr(s string, maxLen int) string {
	r := []rune(s)
	if len(r) <= maxLen {
		return s
	}
	return string(r[:maxLen]) + "..."
}

// estimateTokensFromMessages estimates total tokens for a messages array.
// Uses content extraction for accurate API-aligned estimates.
func estimateTokensFromMessages(messages []any, estimateFn TokenEstimateFunc) int {
	return countContentTokens(messages, func(text string) int {
		return estimateFn(text)
	})
}

// estimateBlockSize estimates the character count of a content block.
func estimateBlockSize(block map[string]any) int {
	data, _ := json.Marshal(block)
	return len(data)
}

// applyDecayToContent applies progressive decay to already-stubbed content.
func applyDecayToContent(content any, stage int, role string) any {
	if stage == DecayStage0 {
		return content
	}

	switch c := content.(type) {
	case string:
		d := ApplyDecay(c, stage, role)
		if d == "" {
			return "[…]" // API rejects empty text content
		}
		return d
	case []any:
		result := make([]any, 0, len(c))
		for _, block := range c {
			b, ok := block.(map[string]any)
			if !ok {
				result = append(result, block)
				continue
			}
			text, isText := b["text"].(string)
			if !isText || b["type"] != "text" {
				result = append(result, block)
				continue
			}
			var decayed string
			if strings.HasPrefix(text, "[→]") {
				decayed = ApplyDecayToToolStub(text, stage)
			} else {
				decayed = ApplyDecay(text, stage, role)
			}
			// Skip empty blocks — Claude API rejects {"type":"text","text":""}
			if decayed == "" {
				continue
			}
			result = append(result, map[string]any{"type": "text", "text": decayed})
		}
		// Never return empty content array — keep at least a minimal marker
		if len(result) == 0 {
			result = append(result, map[string]any{"type": "text", "text": "[…]"})
		}
		return result
	default:
		return content
	}
}
