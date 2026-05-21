package daemon

import (
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/carsteneu/yesmem/internal/models"
)

func (h *Handler) handleSearch(params map[string]any) Response {
	query, _ := params["query"].(string)
	project, _ := params["project"].(string)
	excludeSession, _ := params["exclude_session"].(string)
	since, _ := params["since"].(string)
	before, _ := params["before"].(string)
	limit := intOr(params, "limit", 10)

	hits, err := h.store.SearchMessagesCtx(query, since, before, limit*3) // over-fetch for project filter
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
	since, _ := params["since"].(string)
	before, _ := params["before"].(string)
	limit := intOr(params, "limit", 5)
	includeThinking, _ := params["include_thinking"].(bool)
	includeCommands, _ := params["include_commands"].(bool)

	t0 := time.Now()
	ftsStart := time.Now()

	// FTS5 now indexes thinking blocks (content_blob copied to content),
	// so we search all types directly — no separate enrichment queries needed
	hits, err := h.store.SearchMessagesDeepCtx(query, includeThinking, includeCommands, since, before, limit*3)
	if err != nil {
		return errorResponse(err.Error())
	}
	ftsMs := time.Since(ftsStart).Milliseconds()

	ctxStart := time.Now()

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

	// Batch-load ±3 context windows: one query per session (not per hit)
	type ctxMsg struct{ seq int; content, ts string }
	ctxBySession := make(map[string][]ctxMsg)
	if msgDB != nil {
		type sessionRange struct{ min, max int }
		ranges := make(map[string]*sessionRange)
		for _, hit := range hits {
			r := ranges[hit.SessionID]
			if r == nil {
				r = &sessionRange{min: hit.Sequence - 3, max: hit.Sequence + 3}
				ranges[hit.SessionID] = r
			} else {
				if n := hit.Sequence - 3; n < r.min {
					r.min = n
				}
				if n := hit.Sequence + 3; n > r.max {
					r.max = n
				}
			}
		}
		for sid, r := range ranges {
			rows, err := msgDB.Query(
				`SELECT sequence, content, timestamp FROM messages WHERE session_id = ? AND sequence BETWEEN ? AND ? ORDER BY sequence`,
				sid, r.min, r.max)
			if err != nil {
				continue
			}
			for rows.Next() {
				var m ctxMsg
				rows.Scan(&m.seq, &m.content, &m.ts)
				ctxBySession[sid] = append(ctxBySession[sid], m)
			}
			rows.Close()
		}
	}

	var out []enriched
	const maxCtxSize = 10000
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
		snippet := ""
		contextWindow := ""
		if msgs, ok := ctxBySession[hit.SessionID]; ok {
			var sb strings.Builder
			for _, m := range msgs {
				if m.seq < hit.Sequence-3 || m.seq > hit.Sequence+3 {
					continue
				}
				content := m.content
				if m.seq == hit.Sequence {
					snippet = content
				}
				if m.seq != hit.Sequence && len(content) > 1000 {
					content = content[:1000] + "..."
				}
				date := ""
				if len(m.ts) >= 10 {
					date = "[" + m.ts[:10] + "] "
				}
				if m.seq == hit.Sequence {
					fmt.Fprintf(&sb, "%s>>> %s\n", date, content)
				} else {
					fmt.Fprintf(&sb, "%s%s\n", date, content)
				}
				if sb.Len() > maxCtxSize {
					break
				}
			}
			contextWindow = sb.String()
		}
		out = append(out, enriched{
			SessionID: hit.SessionID, Project: proj, Timestamp: hit.Timestamp,
			Snippet: snippet, Context: contextWindow, Score: -hit.Rank, MessageType: hit.MessageType,
			AgentType: agentType, ParentSessionID: parentSID, SourceAgent: sourceAgent,
		})
	}
	ctxMs := time.Since(ctxStart).Milliseconds()
	totalMs := time.Since(t0).Milliseconds()
	log.Printf("deep_search: q='%s' fts=%dms ctx=%dms n=%d total=%dms", query, ftsMs, ctxMs, len(out), totalMs)
	return jsonResponse(out)
}
