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

// SessionMeta holds metadata extracted from a session JSONL file.
type SessionMeta struct {
	SessionID        string
	Project          string
	GitBranch        string
	StartedAt        time.Time
	EndedAt          time.Time
	FirstUserMessage string
	SourceAgent      string
	// Subagent fields
	AgentID string // non-empty if this is a subagent session
}

// rawLine is the top-level structure of each JSONL line.
type rawLine struct {
	Type        string          `json:"type"`
	SessionID   string          `json:"sessionId"`
	CWD         string          `json:"cwd"`
	GitBranch   string          `json:"gitBranch"`
	Message     json.RawMessage `json:"message"`
	Data        json.RawMessage `json:"data"`
	UUID        string          `json:"uuid"`
	Timestamp   string          `json:"timestamp"`
	AgentID     string          `json:"agentId"`
	IsSidechain bool            `json:"isSidechain"`
	Slug        string          `json:"slug"`
}

// messageEnvelope represents the message field for user/assistant.
type messageEnvelope struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
	Model   string          `json:"model"`
}

// contentBlock represents one block in an assistant message's content array.
type contentBlock struct {
	Type     string          `json:"type"`
	Text     string          `json:"text"`
	Thinking string          `json:"thinking"`
	ID       string          `json:"id"`
	Name     string          `json:"name"`
	Input    json.RawMessage `json:"input"`
	// For tool_result in user messages
	ToolUseID string `json:"tool_use_id"`
	Content   any    `json:"content"`
}

// toolInput holds common input fields we extract from tool_use.
type toolInput struct {
	Command  string `json:"command"`
	FilePath string `json:"file_path"`
	Path     string `json:"path"`
	Pattern  string `json:"pattern"`
}

// progressData represents the data field of a progress message.
type progressData struct {
	Type    string `json:"type"`
	Output  string `json:"output"`
	Content string `json:"content"`
}

// ParseSessionFile reads a Claude Code session JSONL file and extracts messages and metadata.
func ParseSessionFile(path string) ([]models.Message, *SessionMeta, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	var messages []models.Message
	meta := &SessionMeta{}
	seq := 0

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 4*1024*1024), 64*1024*1024)

	for scanner.Scan() {
		var line rawLine
		if err := json.Unmarshal(scanner.Bytes(), &line); err != nil {
			continue
		}

		// Extract metadata
		if meta.SessionID == "" && line.SessionID != "" {
			meta.SessionID = line.SessionID
		}
		if meta.Project == "" && line.CWD != "" {
			meta.Project = line.CWD
		}
		if meta.GitBranch == "" && line.GitBranch != "" {
			meta.GitBranch = line.GitBranch
		}
		if meta.AgentID == "" && line.AgentID != "" {
			meta.AgentID = line.AgentID
		}

		ts := parseTimestamp(line.Timestamp)
		if meta.StartedAt.IsZero() && !ts.IsZero() {
			meta.StartedAt = ts
		}
		if !ts.IsZero() {
			meta.EndedAt = ts
		}

		switch line.Type {
		case "user":
			msgs := parseUserMessage(line, meta, seq, ts)
			messages = append(messages, msgs...)
			seq += len(msgs)

		case "assistant":
			msgs := parseAssistantMessage(line, seq, ts)
			messages = append(messages, msgs...)
			seq += len(msgs)

		case "progress":
			if msg, ok := parseProgressMessage(line, seq, ts); ok {
				messages = append(messages, msg)
				seq++
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return messages, meta, fmt.Errorf("scan %s: %w", path, err)
	}

	return messages, meta, nil
}

// ParseAuto detects the session source from path/content and dispatches to the
// corresponding parser, returning messages normalized into yesmem models.
func ParseAuto(path string) ([]models.Message, *SessionMeta, error) {
	if isCodexPath(path) {
		return ParseCodexSession(path)
	}
	return ParseSessionFile(path)
}

func isCodexPath(path string) bool {
	clean := filepath.Clean(path)
	return strings.Contains(clean, string(os.PathSeparator)+".codex"+string(os.PathSeparator)+"sessions"+string(os.PathSeparator))
}

func parseUserMessage(line rawLine, meta *SessionMeta, seq int, ts time.Time) []models.Message {
	if line.Message == nil {
		return nil
	}

	var env messageEnvelope
	if err := json.Unmarshal(line.Message, &env); err != nil {
		return nil
	}

	if env.Role != "user" {
		return nil
	}

	var messages []models.Message

	// Content can be a string or an array
	// Try string first
	var contentStr string
	if err := json.Unmarshal(env.Content, &contentStr); err == nil {
		if meta.FirstUserMessage == "" && contentStr != "" {
			meta.FirstUserMessage = contentStr
		}
		messages = append(messages, models.Message{
			SessionID:   line.SessionID,
			Role:        "user",
			MessageType: "text",
			Content:     contentStr,
			Timestamp:   ts,
			Sequence:    seq,
		})
		return messages
	}

	// Try array of content blocks
	var blocks []json.RawMessage
	if err := json.Unmarshal(env.Content, &blocks); err != nil {
		return nil
	}

	for _, blockRaw := range blocks {
		var block struct {
			Type      string `json:"type"`
			Text      string `json:"text"`
			ToolUseID string `json:"tool_use_id"`
			Content   any    `json:"content"`
		}
		if err := json.Unmarshal(blockRaw, &block); err != nil {
			continue
		}

		switch block.Type {
		case "text":
			if meta.FirstUserMessage == "" && block.Text != "" {
				meta.FirstUserMessage = block.Text
			}
			messages = append(messages, models.Message{
				SessionID:   line.SessionID,
				Role:        "user",
				MessageType: "text",
				Content:     block.Text,
				Timestamp:   ts,
				Sequence:    seq + len(messages),
			})
		case "tool_result":
			content := fmt.Sprintf("%v", block.Content)
			messages = append(messages, models.Message{
				SessionID:   line.SessionID,
				Role:        "user",
				MessageType: "tool_result",
				Content:     truncate(content, 10240),
				ContentBlob: []byte(content),
				Timestamp:   ts,
				Sequence:    seq + len(messages),
			})
		}
	}

	return messages
}

func parseAssistantMessage(line rawLine, seq int, ts time.Time) []models.Message {
	if line.Message == nil {
		return nil
	}

	var env messageEnvelope
	if err := json.Unmarshal(line.Message, &env); err != nil {
		return nil
	}

	if env.Role != "assistant" {
		return nil
	}

	// Content is always an array for assistant messages
	var blocks []json.RawMessage
	if err := json.Unmarshal(env.Content, &blocks); err != nil {
		return nil
	}

	var messages []models.Message
	for _, blockRaw := range blocks {
		var block contentBlock
		if err := json.Unmarshal(blockRaw, &block); err != nil {
			continue
		}

		switch block.Type {
		case "text":
			messages = append(messages, models.Message{
				SessionID:   line.SessionID,
				Role:        "assistant",
				MessageType: "text",
				Content:     block.Text,
				Timestamp:   ts,
				Sequence:    seq + len(messages),
			})
		case "thinking":
			messages = append(messages, models.Message{
				SessionID:   line.SessionID,
				Role:        "assistant",
				MessageType: "thinking",
				ContentBlob: []byte(block.Thinking),
				Timestamp:   ts,
				Sequence:    seq + len(messages),
			})
		case "tool_use":
			msg := models.Message{
				SessionID:   line.SessionID,
				Role:        "assistant",
				MessageType: "tool_use",
				ToolName:    block.Name,
				Timestamp:   ts,
				Sequence:    seq + len(messages),
			}

			// Extract file_path and command from input
			if block.Input != nil {
				var ti toolInput
				if err := json.Unmarshal(block.Input, &ti); err == nil {
					if ti.FilePath != "" {
						msg.FilePath = ti.FilePath
					} else if ti.Path != "" {
						msg.FilePath = ti.Path
					}
					if ti.Command != "" {
						msg.Content = ti.Command
					} else {
						msg.Content = string(block.Input)
					}
				}
			}

			messages = append(messages, msg)
		}
	}

	return messages
}

func parseProgressMessage(line rawLine, seq int, ts time.Time) (models.Message, bool) {
	if line.Data == nil {
		return models.Message{}, false
	}

	var data progressData
	if err := json.Unmarshal(line.Data, &data); err != nil {
		return models.Message{}, false
	}

	if data.Type != "bash_progress" {
		return models.Message{}, false
	}

	output := data.Output
	if output == "" {
		output = data.Content
	}
	if output == "" {
		return models.Message{}, false
	}

	return models.Message{
		SessionID:   line.SessionID,
		Role:        "assistant",
		MessageType: "bash_output",
		Content:     truncate(output, 10240),
		ContentBlob: []byte(output),
		Timestamp:   ts,
		Sequence:    seq,
	}, true
}

func parseTimestamp(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		t, err = time.Parse("2006-01-02T15:04:05.000Z", s)
		if err != nil {
			return time.Time{}
		}
	}
	return t
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen]
}
