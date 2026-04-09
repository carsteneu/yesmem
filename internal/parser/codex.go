package parser

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/carsteneu/yesmem/internal/models"
)

type codexLine struct {
	Timestamp string          `json:"timestamp"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
}

type codexSessionMetaPayload struct {
	ID        string `json:"id"`
	Timestamp string `json:"timestamp"`
	CWD       string `json:"cwd"`
}

type codexResponseItem struct {
	Type    string          `json:"type"`
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type codexFunctionCall struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
	CallID    string          `json:"call_id"`
}

type codexFunctionCallOutput struct {
	CallID string          `json:"call_id"`
	Output json.RawMessage `json:"output"`
}

type codexReasoningPayload struct {
	Summary []struct {
		Text string `json:"text"`
	} `json:"summary"`
	Content any `json:"content"`
}

type codexEventMsg struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

type codexContentItem struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// ParseCodexSession parses Codex JSONL event streams from ~/.codex/sessions.
// It normalizes user/assistant messages, function calls, outputs, and reasoning
// into yesmem's generic session/message model.
func ParseCodexSession(path string) ([]models.Message, *SessionMeta, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	meta := &SessionMeta{SourceAgent: "codex"}
	var messages []models.Message
	seq := 0

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 4*1024*1024), 64*1024*1024)

	for scanner.Scan() {
		var line codexLine
		if err := json.Unmarshal(scanner.Bytes(), &line); err != nil {
			continue
		}

		ts := parseTimestamp(line.Timestamp)
		if meta.StartedAt.IsZero() && !ts.IsZero() {
			meta.StartedAt = ts
		}
		if !ts.IsZero() {
			meta.EndedAt = ts
		}

		switch line.Type {
		case "session_meta":
			var payload codexSessionMetaPayload
			if json.Unmarshal(line.Payload, &payload) != nil {
				continue
			}
			if payload.ID != "" && meta.SessionID == "" {
				meta.SessionID = normalizeCodexSessionID(payload.ID)
			}
			if payload.CWD != "" && meta.Project == "" {
				meta.Project = payload.CWD
			}
			if meta.StartedAt.IsZero() {
				if parsed := parseTimestamp(payload.Timestamp); !parsed.IsZero() {
					meta.StartedAt = parsed
				}
			}
		case "response_item":
			added := parseCodexResponseItem(line.Payload, meta, ts, seq)
			messages = append(messages, added...)
			seq += len(added)
		case "function_call":
			if msg, ok := parseCodexFunctionCall(line.Payload, meta.SessionID, ts, seq); ok {
				messages = append(messages, msg)
				seq++
			}
		case "function_call_output":
			if msg, ok := parseCodexFunctionCallOutput(line.Payload, meta.SessionID, ts, seq); ok {
				messages = append(messages, msg)
				seq++
			}
		case "reasoning":
			if msg, ok := parseCodexReasoning(line.Payload, meta.SessionID, ts, seq); ok {
				messages = append(messages, msg)
				seq++
			}
		case "event_msg":
			if msg, ok := parseCodexEventMsg(line.Payload, meta.SessionID, ts, seq); ok {
				messages = append(messages, msg)
				seq++
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return messages, meta, fmt.Errorf("scan %s: %w", path, err)
	}

	if meta.Project == "" {
		meta.Project = projectFromCodexPath(path)
	}

	return messages, meta, nil
}

func parseCodexResponseItem(raw json.RawMessage, meta *SessionMeta, ts time.Time, seq int) []models.Message {
	var item codexResponseItem
	if json.Unmarshal(raw, &item) != nil {
		return nil
	}
	if item.Type != "message" {
		return nil
	}

	text := extractCodexText(item.Content)
	if strings.TrimSpace(text) == "" {
		return nil
	}

	role := item.Role
	if role == "" {
		role = "assistant"
	}
	if role == "system" || role == "developer" {
		// These are instruction payloads, not conversational history for extraction.
		return nil
	}
	if meta.FirstUserMessage == "" && role == "user" {
		meta.FirstUserMessage = text
	}

	return []models.Message{{
		SessionID:   meta.SessionID,
		Role:        role,
		MessageType: "text",
		Content:     text,
		Timestamp:   ts,
		Sequence:    seq,
	}}
}

func parseCodexFunctionCall(raw json.RawMessage, sessionID string, ts time.Time, seq int) (models.Message, bool) {
	var payload codexFunctionCall
	if json.Unmarshal(raw, &payload) != nil || payload.Name == "" {
		return models.Message{}, false
	}

	content := strings.TrimSpace(string(payload.Arguments))
	filePath := extractCodexFilePath(payload.Arguments)

	return models.Message{
		SessionID:   sessionID,
		Role:        "assistant",
		MessageType: "tool_use",
		Content:     truncate(content, 10240),
		ContentBlob: []byte(content),
		ToolName:    payload.Name,
		FilePath:    filePath,
		Timestamp:   ts,
		Sequence:    seq,
	}, true
}

func parseCodexFunctionCallOutput(raw json.RawMessage, sessionID string, ts time.Time, seq int) (models.Message, bool) {
	var payload codexFunctionCallOutput
	if json.Unmarshal(raw, &payload) != nil {
		return models.Message{}, false
	}

	content := normalizeCodexRawContent(payload.Output)
	if strings.TrimSpace(content) == "" {
		return models.Message{}, false
	}

	msgType := "tool_result"
	if looksLikeBashOutput(content) {
		msgType = "bash_output"
	}

	return models.Message{
		SessionID:   sessionID,
		Role:        "user",
		MessageType: msgType,
		Content:     truncate(content, 10240),
		ContentBlob: []byte(content),
		Timestamp:   ts,
		Sequence:    seq,
	}, true
}

func parseCodexReasoning(raw json.RawMessage, sessionID string, ts time.Time, seq int) (models.Message, bool) {
	var payload codexReasoningPayload
	if json.Unmarshal(raw, &payload) != nil {
		return models.Message{}, false
	}

	var parts []string
	for _, item := range payload.Summary {
		if strings.TrimSpace(item.Text) != "" {
			parts = append(parts, item.Text)
		}
	}
	if len(parts) == 0 && payload.Content != nil {
		parts = append(parts, fmt.Sprintf("%v", payload.Content))
	}
	text := strings.TrimSpace(strings.Join(parts, "\n"))
	if text == "" {
		return models.Message{}, false
	}

	return models.Message{
		SessionID:   sessionID,
		Role:        "assistant",
		MessageType: "thinking",
		Content:     truncate(text, 10240),
		ContentBlob: []byte(text),
		Timestamp:   ts,
		Sequence:    seq,
	}, true
}

func parseCodexEventMsg(raw json.RawMessage, sessionID string, ts time.Time, seq int) (models.Message, bool) {
	var payload codexEventMsg
	if json.Unmarshal(raw, &payload) != nil {
		return models.Message{}, false
	}

	switch payload.Type {
	case "agent_message":
		if strings.TrimSpace(payload.Message) == "" {
			return models.Message{}, false
		}
		return models.Message{
			SessionID:   sessionID,
			Role:        "assistant",
			MessageType: "text",
			Content:     payload.Message,
			Timestamp:   ts,
			Sequence:    seq,
		}, true
	case "user_message":
		if strings.TrimSpace(payload.Message) == "" {
			return models.Message{}, false
		}
		return models.Message{
			SessionID:   sessionID,
			Role:        "user",
			MessageType: "text",
			Content:     payload.Message,
			Timestamp:   ts,
			Sequence:    seq,
		}, true
	default:
		return models.Message{}, false
	}
}

func extractCodexText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}

	var direct string
	if json.Unmarshal(raw, &direct) == nil {
		return direct
	}

	var blocks []codexContentItem
	if json.Unmarshal(raw, &blocks) == nil {
		var parts []string
		for _, block := range blocks {
			if strings.TrimSpace(block.Text) != "" {
				parts = append(parts, block.Text)
			}
		}
		return strings.Join(parts, "\n")
	}

	var generic []map[string]any
	if json.Unmarshal(raw, &generic) == nil {
		var parts []string
		for _, block := range generic {
			if text, ok := block["text"].(string); ok && strings.TrimSpace(text) != "" {
				parts = append(parts, text)
			}
			if inputText, ok := block["input_text"].(string); ok && strings.TrimSpace(inputText) != "" {
				parts = append(parts, inputText)
			}
		}
		return strings.Join(parts, "\n")
	}

	return ""
}

func extractCodexFilePath(raw json.RawMessage) string {
	var payload map[string]any
	if json.Unmarshal(raw, &payload) != nil {
		return ""
	}
	for _, key := range []string{"path", "file_path", "cwd"} {
		if v, ok := payload[key].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

func normalizeCodexRawContent(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var direct string
	if json.Unmarshal(raw, &direct) == nil {
		return direct
	}
	return strings.TrimSpace(string(raw))
}

func normalizeCodexSessionID(id string) string {
	if id == "" {
		return ""
	}
	if strings.HasPrefix(id, "codex:") {
		return id
	}
	return "codex:" + id
}

func projectFromCodexPath(path string) string {
	parts := strings.Split(filepath.Clean(path), string(os.PathSeparator))
	for i, part := range parts {
		if part == "sessions" && i+4 < len(parts) {
			// ~/.codex/sessions/YYYY/MM/DD/<file>.jsonl -> project comes from file content,
			// but as a fallback return ~/.codex.
			break
		}
	}
	return filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(path))))
}

func looksLikeBashOutput(content string) bool {
	content = strings.TrimSpace(content)
	if content == "" {
		return false
	}
	if strings.Contains(content, "Command: ") || strings.Contains(content, "Process exited with code") {
		return true
	}
	if strings.Contains(content, "Chunk ID:") || strings.Contains(content, "Wall time:") {
		return true
	}
	return false
}
