package briefing

import (
	"fmt"
	"strings"

	"github.com/carsteneu/yesmem/internal/models"
	"github.com/carsteneu/yesmem/internal/storage"
)

// RecoveryConfig holds parameters for session recovery context generation.
type RecoveryConfig struct {
	SessionID string
	Source    string // "clear" or "compact"
}

// SetRecovery configures session recovery for the next Generate() call.
func (g *Generator) SetRecovery(sessionID, source string) {
	g.recovery = &RecoveryConfig{
		SessionID: sessionID,
		Source:    source,
	}
}

// GenerateRecovery builds a session recovery block from stored session data.
// Returns empty string if no recovery configured, session not found, or no messages.
// Called separately from Generate() so recovery survives the refine pass.
func (g *Generator) GenerateRecovery() string {
	if g.recovery == nil || g.recovery.SessionID == "" {
		return ""
	}

	sess, err := g.store.GetSession(g.recovery.SessionID)
	if err != nil || sess == nil {
		return ""
	}

	msgs, err := g.store.GetMessagesBySession(g.recovery.SessionID)
	if err != nil || len(msgs) == 0 {
		return ""
	}

	isCompact := g.recovery.Source == "compact"
	return formatRecovery(sess, msgs, isCompact)
}

// formatRecovery formats session data into a recovery text block.
// compact=true produces a shorter version (~200-300 tokens) for post-compact.
// compact=false produces a fuller version (~500-1000 tokens) for post-clear.
func formatRecovery(sess *models.Session, msgs []models.Message, compact bool) string {
	var b strings.Builder

	if compact {
		b.WriteString("## Session-Kontext (vor Compact)\n")
	} else {
		b.WriteString("## Session-Kontext (vor Clear)\n")
	}

	// Current task: extract from recent user messages
	userRequests := extractUserRequests(msgs)
	filesWorked := extractFiles(msgs)

	// Recent conversation: last N user+assistant text messages
	var recentLimit int
	if compact {
		recentLimit = 5
	} else {
		recentLimit = 15
	}
	recentMsgs := extractRecentConversation(msgs, recentLimit)

	// Task summary
	if len(userRequests) > 0 {
		b.WriteString("Aufgaben in dieser Session:\n")
		limit := len(userRequests)
		if compact && limit > 3 {
			limit = 3
		}
		for i := 0; i < limit; i++ {
			b.WriteString(fmt.Sprintf("- %s\n", userRequests[i]))
		}
		b.WriteString("\n")
	}

	// Files worked on
	if len(filesWorked) > 0 {
		b.WriteString("Files worked on:\n")
		limit := len(filesWorked)
		if compact && limit > 5 {
			limit = 5
		} else if limit > 15 {
			limit = 15
		}
		for i := 0; i < limit; i++ {
			b.WriteString(fmt.Sprintf("- %s\n", filesWorked[i]))
		}
		if len(filesWorked) > limit {
			b.WriteString(fmt.Sprintf("  (+%d weitere)\n", len(filesWorked)-limit))
		}
		b.WriteString("\n")
	}

	// Recent conversation thread
	if len(recentMsgs) > 0 {
		b.WriteString("Recent conversation thread:\n")
		for _, m := range recentMsgs {
			prefix := "User"
			if m.Role == "assistant" {
				prefix = "Claude"
			}
			content := m.Content
			maxLen := 200
			if compact {
				maxLen = 100
			}
			if len(content) > maxLen {
				content = content[:maxLen] + "..."
			}
			b.WriteString(fmt.Sprintf("  %s: %s\n", prefix, content))
		}
		b.WriteString("\n")
	}

	// Message count for context
	b.WriteString(fmt.Sprintf("(Session: %d messages total, get_session(\"%s\") for more detail)\n",
		len(msgs), sess.ID))

	return b.String()
}

// extractUserRequests returns user text messages as task descriptions.
func extractUserRequests(msgs []models.Message) []string {
	var requests []string
	for _, m := range msgs {
		if m.Role == "user" && m.MessageType == "text" && m.Content != "" {
			text := m.Content
			// Skip very short messages and system-like content
			if len(text) < 5 || strings.HasPrefix(text, "<") {
				continue
			}
			if len(text) > 120 {
				text = text[:120] + "..."
			}
			if len(requests) < 10 {
				requests = append(requests, text)
			}
		}
	}
	return requests
}

// extractFiles returns unique file paths from tool_use messages.
func extractFiles(msgs []models.Message) []string {
	seen := map[string]bool{}
	var files []string
	for _, m := range msgs {
		if m.FilePath != "" && !seen[m.FilePath] {
			seen[m.FilePath] = true
			files = append(files, m.FilePath)
		}
	}
	return files
}

// extractRecentConversation returns the last N user/assistant text messages.
func extractRecentConversation(msgs []models.Message, limit int) []models.Message {
	var textMsgs []models.Message
	for _, m := range msgs {
		if m.MessageType == "text" && m.Content != "" &&
			(m.Role == "user" || m.Role == "assistant") {
			// Skip system-injected content
			if m.Role == "user" && strings.HasPrefix(m.Content, "<") {
				continue
			}
			textMsgs = append(textMsgs, m)
		}
	}
	if len(textMsgs) > limit {
		textMsgs = textMsgs[len(textMsgs)-limit:]
	}
	return textMsgs
}

// Ensure Store has the methods we need (compile-time check).
var _ interface {
	GetSession(string) (*models.Session, error)
	GetMessagesBySession(string) ([]models.Message, error)
} = (*storage.Store)(nil)
