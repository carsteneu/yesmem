package daemon

import (
	"database/sql"
	"fmt"
	"strings"

	"github.com/carsteneu/yesmem/internal/models"
)

func (h *Handler) handleSearch(params map[string]any) Response {
	query, _ := params["query"].(string)
	project, _ := params["project"].(string)
	excludeSession, _ := params["exclude_session"].(string)
	limit := intOr(params, "limit", 10)

	hits, err := h.store.SearchMessages(query, limit*3) // over-fetch for project filter
	if err != nil {
		return errorResponse(err.Error())
	}

	// Batch-load session metadata for all hits at once (single query)
	sessionIDs := make([]string, 0, len(hits))
	seen := make(map[string]bool)
	for _, hit := range hits {
		if !seen[hit.SessionID] {
			seen[hit.SessionID] = true
			sessionIDs = append(sessionIDs, hit.SessionID)
		}
	}
	sessionMeta, _ := h.store.GetSessionMetaBulk(sessionIDs)

	var results []models.SearchResult
	for _, hit := range hits {
		if len(results) >= limit {
			break
		}
		if excludeSession != "" && hit.SessionID == excludeSession {
			continue
		}
		proj := ""
		if m, ok := sessionMeta[hit.SessionID]; ok {
			proj = m.Project
		}
		if project != "" && !models.ProjectMatches(proj, project) {
			continue
		}
		snippet := hit.Content
		if len(snippet) > 300 {
			snippet = snippet[:300] + "..."
		}
		results = append(results, models.SearchResult{
			SessionID:   hit.SessionID,
			Project:     proj,
			Timestamp:   hit.Timestamp,
			Snippet:     snippet,
			Score:       -hit.Rank,
			MessageType: hit.MessageType,
			SourceAgent: hit.SourceAgent,
		})
	}
	h.annotateSubagentInfo(results)
	return jsonResponse(results)
}

// annotateSubagentInfo enriches search results with subagent metadata from the DB.
func (h *Handler) annotateSubagentInfo(results []models.SearchResult) {
	if len(results) == 0 {
		return
	}
	seen := make(map[string]bool, len(results))
	var ids []string
	for _, r := range results {
		if r.SessionID != "" && !seen[r.SessionID] {
			seen[r.SessionID] = true
			ids = append(ids, r.SessionID)
		}
	}
	meta, err := h.store.GetSessionMetaBulk(ids)
	if err != nil || len(meta) == 0 {
		return
	}
	for i := range results {
		if m, ok := meta[results[i].SessionID]; ok {
			results[i].AgentType = m.AgentType
			results[i].ParentSessionID = m.ParentSessionID
			results[i].SourceAgent = m.SourceAgent
		}
	}
}

func (h *Handler) handleDeepSearch(params map[string]any) Response {
	query, _ := params["query"].(string)
	project, _ := params["project"].(string)
	excludeSession, _ := params["exclude_session"].(string)
	limit := intOr(params, "limit", 5)
	includeThinking, _ := params["include_thinking"].(bool)
	includeCommands, _ := params["include_commands"].(bool)

	// FTS5 now indexes thinking blocks (content_blob copied to content),
	// so we search all types directly — no separate enrichment queries needed
	hits, err := h.store.SearchMessagesDeep(query, includeThinking, includeCommands, limit*3)
	if err != nil {
		return errorResponse(err.Error())
	}

	// Batch-load session metadata (single query instead of N+1)
	sessionIDs := make([]string, 0, len(hits))
	seen := make(map[string]bool)
	for _, hit := range hits {
		if !seen[hit.SessionID] {
			seen[hit.SessionID] = true
			sessionIDs = append(sessionIDs, hit.SessionID)
		}
	}
	sessionMeta, _ := h.store.GetSessionMetaBulk(sessionIDs)

	type enriched struct {
		SessionID       string  `json:"session_id"`
		Project         string  `json:"project"`
		Timestamp       string  `json:"timestamp"`
		Snippet         string  `json:"snippet"`
		Context         string  `json:"context,omitempty"`
		Score           float64 `json:"score"`
		MessageType     string  `json:"message_type"`
		AgentType       string  `json:"agent_type,omitempty"`
		ParentSessionID string  `json:"parent_session_id,omitempty"`
		SourceAgent     string  `json:"source_agent,omitempty"`
	}

	msgDB := h.store.MessagesDB()
	var out []enriched
	for _, hit := range hits {
		if len(out) >= limit {
			break
		}
		if excludeSession != "" && hit.SessionID == excludeSession {
			continue
		}
		proj := ""
		agentType := ""
		parentSID := ""
		sourceAgent := hit.SourceAgent
		if m, ok := sessionMeta[hit.SessionID]; ok {
			proj = m.Project
			agentType = m.AgentType
			parentSID = m.ParentSessionID
			if sourceAgent == "" {
				sourceAgent = m.SourceAgent
			}
		}
		if project != "" && !models.ProjectMatches(proj, project) {
			continue
		}
		snippet := hit.Content
		// deep_search returns full content — that's the whole point.
		// Regular search() truncates to 300 chars; deep_search does not.
		// Build ±3 message context window
		contextWindow := buildMessageContext(msgDB, hit.SessionID, hit.Sequence)
		out = append(out, enriched{
			SessionID: hit.SessionID, Project: proj, Timestamp: hit.Timestamp,
			Snippet: snippet, Context: contextWindow, Score: -hit.Rank, MessageType: hit.MessageType,
			AgentType: agentType, ParentSessionID: parentSID, SourceAgent: sourceAgent,
		})
	}
	return jsonResponse(out)
}

// buildMessageContext returns ±3 surrounding messages for context around a search hit.
func buildMessageContext(db *sql.DB, sessionID string, sequence int) string {
	if db == nil || sequence <= 0 {
		return ""
	}
	rows, err := db.Query(`SELECT sequence, content, timestamp FROM messages WHERE session_id = ? AND sequence BETWEEN ? AND ? ORDER BY sequence`, sessionID, sequence-3, sequence+3)
	if err != nil {
		return ""
	}
	defer rows.Close()

	var sb strings.Builder
	const maxContextSize = 10000 // deep_search context window — generous for actual inspection
	for rows.Next() {
		var seq int
		var content, ts string
		if err := rows.Scan(&seq, &content, &ts); err != nil {
			continue
		}
		// Truncate individual context messages (not the hit itself)
		if seq != sequence && len(content) > 1000 {
			content = content[:1000] + "..."
		}
		date := ""
		if len(ts) >= 10 {
			date = "[" + ts[:10] + "] "
		}
		if seq == sequence {
			fmt.Fprintf(&sb, "%s>>> %s\n", date, content)
		} else {
			fmt.Fprintf(&sb, "%s%s\n", date, content)
		}
		if sb.Len() > maxContextSize {
			break
		}
	}
	return sb.String()
}
