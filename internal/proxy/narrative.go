package proxy

import (
	"fmt"
	"strings"
	"sync"
)

// Narrative maintains a living summary of the session's red thread.
// It's injected as the first user message after the system prompt.
// ArchivedTopic represents a group of stubbed messages with a common theme.
type ArchivedTopic struct {
	Label      string // e.g. "proxy.go, stubify.go"
	MsgCount   int
	ReqRange   string // e.g. "bis Req 50"
	SearchHint string // e.g. "proxy.go stubify.go"
}

type Narrative struct {
	mu sync.RWMutex

	requestCount   int
	goal           string           // extracted from first user message
	phases         []phase          // chronological phases
	decisions      []string         // key decisions (from isDecision messages)
	pivotMoments   []string         // from yesmem pivot_moment learnings (Phase-A)
	archivedTopics []ArchivedTopic  // stubbed topic summaries
	lastQuestion   string           // last user message ending with ?
}

type phase struct {
	label    string
	startReq int
	endReq   int
	keywords []string
}

// NewNarrative creates a new narrative tracker.
func NewNarrative() *Narrative {
	return &Narrative{}
}

// Update processes a new request's messages and updates the narrative state.
func (n *Narrative) Update(messages []any, requestIdx int) {
	n.mu.Lock()
	defer n.mu.Unlock()

	n.requestCount = requestIdx

	// Extract goal from first user message (only once)
	if n.goal == "" && len(messages) > 0 {
		for _, msg := range messages {
			m, ok := msg.(map[string]any)
			if !ok {
				continue
			}
			if m["role"] == "user" {
				n.goal = extractGoal(m["content"])
				break
			}
		}
	}

	// Scan recent messages for decisions and questions
	for i, msg := range messages {
		m, ok := msg.(map[string]any)
		if !ok {
			continue
		}
		role, _ := m["role"].(string)
		if role != "user" {
			continue
		}
		text := extractText(m["content"])
		if text == "" {
			continue
		}

		// Track decisions
		if isDecision(messages, i) {
			decision := truncateStr(text, 80)
			if !n.hasDecision(decision) {
				n.decisions = append(n.decisions, decision)
				// Keep only last 7 decisions
				if len(n.decisions) > 7 {
					n.decisions = n.decisions[len(n.decisions)-7:]
				}
			}
		}

		// Track last question
		trimmed := strings.TrimSpace(text)
		if len(trimmed) > 0 && trimmed[len(trimmed)-1] == '?' {
			n.lastQuestion = truncateStr(trimmed, 120)
		}
	}

	// Auto-create new phase every 20 requests
	if len(n.phases) == 0 || requestIdx-n.currentPhase().startReq >= 20 {
		keywords := extractKeywords(messages)
		label := strings.Join(keywords, ", ")
		if label == "" {
			label = fmt.Sprintf("Phase ab Request %d", requestIdx)
		}
		n.phases = append(n.phases, phase{
			label:    label,
			startReq: requestIdx,
			endReq:   requestIdx,
			keywords: keywords,
		})
	} else {
		// Update current phase end
		n.phases[len(n.phases)-1].endReq = requestIdx
	}
}

// SetPivotMoments sets pivot moments from yesmem (Phase-A integration).
func (n *Narrative) SetPivotMoments(pivots []string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.pivotMoments = pivots
}

// AddArchivedTopic records a stubbed topic for the narrative. Deduplicates by label.
func (n *Narrative) AddArchivedTopic(topic ArchivedTopic) {
	n.mu.Lock()
	defer n.mu.Unlock()
	for _, existing := range n.archivedTopics {
		if existing.Label == topic.Label {
			return
		}
	}
	n.archivedTopics = append(n.archivedTopics, topic)
	if len(n.archivedTopics) > 10 {
		n.archivedTopics = n.archivedTopics[len(n.archivedTopics)-10:]
	}
}

// Render produces the narrative block text (~2k tokens).
func (n *Narrative) Render() string {
	n.mu.RLock()
	defer n.mu.RUnlock()

	if n.requestCount == 0 {
		return ""
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Session-Kontext (auto-generiert, Request %d):\n", n.requestCount)

	// Goal
	if n.goal != "" {
		fmt.Fprintf(&b, "Ziel: %s\n", n.goal)
	}

	// Phases
	if len(n.phases) > 0 {
		b.WriteString("Verlauf:\n")
		for _, p := range n.phases {
			fmt.Fprintf(&b, "- %s (Req %d-%d)\n", p.label, p.startReq, p.endReq)
		}
	}

	// Current phase
	if len(n.phases) > 0 {
		current := n.phases[len(n.phases)-1]
		fmt.Fprintf(&b, "Aktuelle Phase: %s\n", current.label)
	}

	// Key decisions — filter out system-reminder noise
	if len(n.decisions) > 0 {
		var filtered []string
		for _, d := range n.decisions {
			if !strings.Contains(d, "<system-reminder>") && !strings.Contains(d, "system-reminder") {
				filtered = append(filtered, d)
			}
		}
		if len(filtered) > 0 {
			b.WriteString("Key Decisions: ")
			b.WriteString(strings.Join(filtered, "; "))
			b.WriteString("\n")
		}
	}

	// Pivot moments — only every 10 requests or in first 3 (initial context)
	if len(n.pivotMoments) > 0 && (n.requestCount <= 3 || n.requestCount%10 == 0) {
		b.WriteString("Pivot-Moments:\n")
		for _, p := range n.pivotMoments {
			fmt.Fprintf(&b, "- %s\n", p)
		}
	}

	// Archived topics
	if len(n.archivedTopics) > 0 {
		b.WriteString("Archived topics (not in active context):\n")
		for _, topic := range n.archivedTopics {
			fmt.Fprintf(&b, "- %s (%d Messages, %s) → deep_search('%s')\n",
				topic.Label, topic.MsgCount, topic.ReqRange, topic.SearchHint)
		}
	}

	// Open question
	if n.lastQuestion != "" {
		fmt.Fprintf(&b, "Offene Frage: %s\n", n.lastQuestion)
	}

	return b.String()
}

// InjectNarrative inserts the narrative block at a safe position in the messages array.
// Finds the first assistant→user boundary that doesn't break tool_use/tool_result pairing.
func InjectNarrative(messages []any, narrativeText string) []any {
	if narrativeText == "" || len(messages) < 2 {
		return messages
	}

	narrativeMsg := map[string]any{
		"role": "user",
		"content": []any{
			map[string]any{
				"type": "text",
				"text": narrativeText,
			},
		},
	}
	assistantAck := map[string]any{
		"role":    "assistant",
		"content": "Understood. Continuing with this context.",
	}

	// Find safe insertion point: after an assistant message that is NOT a tool_use,
	// and before a user message that is NOT a tool_result.
	// Start from index 1 (skip first message which is system/compact summary).
	insertIdx := -1
	for i := 1; i < len(messages)-1; i++ {
		curr, ok := messages[i].(map[string]any)
		if !ok {
			continue
		}
		next, ok := messages[i+1].(map[string]any)
		if !ok {
			continue
		}
		// Need: current = assistant (without tool_use), next = user (without tool_result)
		if curr["role"] == "assistant" && next["role"] == "user" {
			if !hasToolUse(curr) && !hasToolResult(next) {
				insertIdx = i + 1
				break
			}
		}
	}

	if insertIdx < 0 {
		return messages // no safe spot found, skip narrative
	}

	result := make([]any, 0, len(messages)+2)
	result = append(result, messages[:insertIdx]...)
	result = append(result, narrativeMsg)
	result = append(result, assistantAck)
	result = append(result, messages[insertIdx:]...)
	return result
}

// hasToolUse checks if a message contains tool_use content blocks.
func hasToolUse(msg map[string]any) bool {
	blocks, ok := msg["content"].([]any)
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

func (n *Narrative) currentPhase() *phase {
	if len(n.phases) == 0 {
		return nil
	}
	return &n.phases[len(n.phases)-1]
}

func (n *Narrative) hasDecision(d string) bool {
	for _, existing := range n.decisions {
		if existing == d {
			return true
		}
	}
	return false
}

// extractGoal gets a short goal description from the first user message.
func extractGoal(content any) string {
	text := extractText(content)
	if text == "" {
		return ""
	}
	// Take first sentence or first 150 runes
	runes := []rune(text)
	for i, ch := range runes {
		if (ch == '.' || ch == '!' || ch == '?') && i > 10 {
			return string(runes[:i+1])
		}
		if i >= 150 {
			return string(runes[:150]) + "..."
		}
	}
	return text
}

// extractText gets plain text from any content format.
func extractText(content any) string {
	switch c := content.(type) {
	case string:
		return c
	case []any:
		var parts []string
		for _, block := range c {
			if b, ok := block.(map[string]any); ok {
				if t, ok := b["text"].(string); ok {
					parts = append(parts, t)
				}
			}
		}
		return strings.Join(parts, " ")
	}
	return ""
}

// extractKeywords pulls tool names and file paths from messages.
func extractKeywords(messages []any) []string {
	seen := make(map[string]bool)
	var keywords []string

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
			if b["type"] == "tool_use" {
				name, _ := b["name"].(string)
				if name != "" && !seen[name] {
					seen[name] = true
					keywords = append(keywords, name)
				}
				if input, ok := b["input"].(map[string]any); ok {
					if fp, ok := input["file_path"].(string); ok {
						short := shortPath(fp)
						if !seen[short] {
							seen[short] = true
							keywords = append(keywords, short)
						}
					}
				}
			}
		}
	}

	// Limit to 5 keywords
	if len(keywords) > 5 {
		keywords = keywords[:5]
	}
	return keywords
}

// ActivePaths returns file paths referenced in the last 2 phases.
// These paths get decay immunity — stubs referencing them stay fresh longer.
func (n *Narrative) ActivePaths() []string {
	n.mu.RLock()
	defer n.mu.RUnlock()

	seen := make(map[string]bool)
	var paths []string

	// Collect from last 2 phases
	start := len(n.phases) - 2
	if start < 0 {
		start = 0
	}
	for _, p := range n.phases[start:] {
		for _, kw := range p.keywords {
			if (strings.Contains(kw, "/") || strings.Contains(kw, ".")) && !seen[kw] {
				seen[kw] = true
				paths = append(paths, kw)
			}
		}
	}
	return paths
}

// shortPath returns the last 2 path components.
func shortPath(p string) string {
	parts := strings.Split(p, "/")
	if len(parts) > 2 {
		return strings.Join(parts[len(parts)-2:], "/")
	}
	return p
}

// StripOldNarratives removes old narrative message pairs from the stream.
// Narrative messages are identified by "Session-Kontext (auto-generiert" prefix.
// Each narrative is a user message followed by an assistant ack.
func StripOldNarratives(messages []any) []any {
	result := make([]any, 0, len(messages))
	skipNext := false
	for _, msg := range messages {
		if skipNext {
			skipNext = false
			// Skip the assistant ack that follows a narrative
			m, ok := msg.(map[string]any)
			if ok {
				if role, _ := m["role"].(string); role == "assistant" {
					content, _ := m["content"].(string)
					if strings.Contains(content, "Understood") || strings.Contains(content, "Continuing with this context") {
						continue
					}
				}
			}
			// Not an ack — keep it
			result = append(result, msg)
			continue
		}

		m, ok := msg.(map[string]any)
		if !ok {
			result = append(result, msg)
			continue
		}

		if isNarrativeMessage(m) {
			skipNext = true
			continue
		}

		result = append(result, msg)
	}
	return result
}

// isNarrativeMessage checks if a message contains "Session-Kontext (auto-generiert".
func isNarrativeMessage(m map[string]any) bool {
	role, _ := m["role"].(string)
	if role != "user" {
		return false
	}
	text := extractMessageText(m)
	return strings.Contains(text, "Session-Kontext (auto-generiert")
}

// extractMessageText gets text from a message with string or array content.
func extractMessageText(m map[string]any) string {
	switch c := m["content"].(type) {
	case string:
		return c
	case []any:
		var parts []string
		for _, block := range c {
			bm, ok := block.(map[string]any)
			if !ok {
				continue
			}
			if text, ok := bm["text"].(string); ok && text != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, "\n")
	default:
		return ""
	}
}
