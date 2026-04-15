package daemon

import (
	"fmt"
	"time"

	"github.com/carsteneu/yesmem/internal/models"
)

func (h *Handler) handleGetSession(params map[string]any) Response {
	sessionID, _ := params["session_id"].(string)
	mode := stringOr(params, "mode", "summary")
	offset := intOr(params, "offset", 0)
	limit := intOr(params, "limit", 50)

	sess, err := h.store.GetSession(sessionID)
	if err != nil {
		return errorResponse(err.Error())
	}

	msgs, _ := h.store.GetMessagesBySession(sessionID)

	// Look up subagent count for this session (cheap single-ID bulk call)
	subagentCount := 0
	if counts, err := h.store.GetSubagentCounts([]string{sessionID}); err == nil {
		subagentCount = counts[sessionID]
	}

	switch mode {
	case "summary":
		result := buildSessionSummary(sess, msgs)
		result["subagent_count"] = subagentCount
		return jsonResponse(result)

	case "recent":
		// Last N messages
		if limit > len(msgs) {
			limit = len(msgs)
		}
		recent := msgs[len(msgs)-limit:]
		return jsonResponse(map[string]any{
			"session":        sess,
			"messages":       lightMessages(recent),
			"total":          len(msgs),
			"showing":        fmt.Sprintf("last %d of %d", limit, len(msgs)),
			"subagent_count": subagentCount,
		})

	case "paginated":
		if offset >= len(msgs) {
			return jsonResponse(map[string]any{
				"session": sess, "messages": []any{},
				"total": len(msgs), "offset": offset,
				"subagent_count": subagentCount,
			})
		}
		end := offset + limit
		if end > len(msgs) {
			end = len(msgs)
		}
		return jsonResponse(map[string]any{
			"session":        sess,
			"messages":       fullMessages(msgs[offset:end]),
			"total":          len(msgs),
			"offset":         offset,
			"limit":          limit,
			"has_more":       end < len(msgs),
			"subagent_count": subagentCount,
		})

	default:
		// full — returns untruncated content, but cap at 100 messages
		if len(msgs) > 100 {
			return jsonResponse(map[string]any{
				"session":        sess,
				"messages":       fullMessages(msgs[:100]),
				"total":          len(msgs),
				"truncated":      true,
				"hint":           "Session has too many messages. Use mode=paginated with offset/limit.",
				"subagent_count": subagentCount,
			})
		}
		return jsonResponse(map[string]any{"session": sess, "messages": fullMessages(msgs), "subagent_count": subagentCount})
	}
}

// buildSessionSummary creates a compact overview of a session without returning all messages.
func buildSessionSummary(sess *models.Session, msgs []models.Message) map[string]any {
	// Extract topics from user messages
	var userRequests []string
	var toolsUsed = map[string]int{}
	var filesWorked []string
	filesSeen := map[string]bool{}

	for _, m := range msgs {
		if m.Role == "user" && m.MessageType == "text" && m.Content != "" {
			text := m.Content
			if len(userRequests) < 10 {
				userRequests = append(userRequests, text)
			}
		}
		if m.MessageType == "tool_use" && m.ToolName != "" {
			toolsUsed[m.ToolName]++
		}
		if m.FilePath != "" && !filesSeen[m.FilePath] {
			filesSeen[m.FilePath] = true
			if len(filesWorked) < 20 {
				filesWorked = append(filesWorked, m.FilePath)
			}
		}
	}

	// Last few messages for context
	recentCount := 10
	if recentCount > len(msgs) {
		recentCount = len(msgs)
	}
	recent := lightMessages(msgs[len(msgs)-recentCount:])

	return map[string]any{
		"session": map[string]any{
			"id":                sess.ID,
			"project":           sess.Project,
			"project_short":     sess.ProjectShort,
			"git_branch":        sess.GitBranch,
			"first_message":     sess.FirstMessage,
			"message_count":     sess.MessageCount,
			"started_at":        sess.StartedAt,
			"ended_at":          sess.EndedAt,
			"parent_session_id": sess.ParentSessionID,
			"agent_type":        sess.AgentType,
			"source_agent":      sess.SourceAgent,
		},
		"summary": map[string]any{
			"user_requests":   userRequests,
			"tools_used":      toolsUsed,
			"files_worked_on": filesWorked,
		},
		"recent_messages": recent,
		"total_messages":  len(msgs),
		"hint":            "Use mode=recent, mode=paginated, or mode=full for more detail.",
	}
}

type lightMsg struct {
	SourceAgent string `json:"source_agent,omitempty"`
	Role        string `json:"role"`
	MessageType string `json:"message_type"`
	Content     string `json:"content,omitempty"`
	ToolName    string `json:"tool_name,omitempty"`
	FilePath    string `json:"file_path,omitempty"`
	Timestamp   string `json:"timestamp"`
	Sequence    int    `json:"sequence"`
}

func lightMessages(msgs []models.Message) []lightMsg {
	return lightMessagesWithLimit(msgs, 500)
}

func fullMessages(msgs []models.Message) []lightMsg {
	return lightMessagesWithLimit(msgs, 0)
}

func lightMessagesWithLimit(msgs []models.Message, maxLen int) []lightMsg {
	out := make([]lightMsg, len(msgs))
	for i, m := range msgs {
		content := m.Content
		if maxLen > 0 && len(content) > maxLen {
			content = content[:maxLen] + "... (truncated)"
		}
		out[i] = lightMsg{
			SourceAgent: m.SourceAgent,
			Role:        m.Role,
			MessageType: m.MessageType,
			Content:     content,
			ToolName:    m.ToolName,
			FilePath:    m.FilePath,
			Timestamp:   m.Timestamp.Format("2006-01-02T15:04:05Z"),
			Sequence:    m.Sequence,
		}
	}
	return out
}

func (h *Handler) handleListProjects() Response {
	projects, err := h.store.ListProjects()
	if err != nil {
		return errorResponse(err.Error())
	}
	return jsonResponse(projects)
}

func (h *Handler) handleGetSessionStart(params map[string]any) Response {
	sessionID, _ := params["session_id"].(string)
	if sessionID == "" {
		return errorResponse("missing session_id")
	}
	sess, err := h.store.GetSession(sessionID)
	if err != nil {
		return errorResponse(fmt.Sprintf("session not found: %v", err))
	}
	return jsonResponse(map[string]any{
		"started_at": sess.StartedAt.Format(time.RFC3339),
	})
}

func (h *Handler) handleProjectSummary(params map[string]any) Response {
	project, _ := params["project"].(string)
	limit := intOr(params, "limit", 20)
	sessions, err := h.store.ListSessions(project, limit)
	if err != nil {
		return errorResponse(err.Error())
	}
	return jsonResponse(sessions)
}
