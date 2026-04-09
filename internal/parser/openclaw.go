package parser

import (
	"bufio"
	"encoding/json"
	"os"
	"time"

	"github.com/carsteneu/yesmem/internal/models"
)

// openClawLine represents a single line in OpenClaw's JSONL format.
type openClawLine struct {
	Type      string    `json:"type"`
	Timestamp time.Time `json:"timestamp"`
	Message   struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
		Usage   struct {
			Cost struct {
				Total float64 `json:"total"`
			} `json:"cost"`
		} `json:"usage"`
	} `json:"message"`
}

// ParseOpenClawSession parses an OpenClaw JSONL session file.
// Returns messages in yesmem's internal format + session metadata.
func ParseOpenClawSession(path string) ([]models.Message, *SessionMeta, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()

	var messages []models.Message
	meta := &SessionMeta{}
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 4*1024*1024), 64*1024*1024)

	seq := 0
	for scanner.Scan() {
		var line openClawLine
		if err := json.Unmarshal(scanner.Bytes(), &line); err != nil {
			continue // skip unparseable lines
		}
		if line.Type != "message" {
			continue
		}

		// Set metadata from first message
		if seq == 0 {
			meta.StartedAt = line.Timestamp
		}
		meta.EndedAt = line.Timestamp

		// Extract text content
		msg := models.Message{
			Sequence: seq,
			Role:     line.Message.Role,
		}

		// Parse content blocks (same structure as Anthropic format)
		var blocks []contentBlock
		if err := json.Unmarshal(line.Message.Content, &blocks); err != nil {
			// Try string content
			var text string
			if json.Unmarshal(line.Message.Content, &text) == nil {
				msg.MessageType = "text"
				msg.Content = text
			}
		} else {
			for _, block := range blocks {
				switch block.Type {
				case "text":
					msg.MessageType = "text"
					msg.Content = block.Text
				case "thinking":
					msg.MessageType = "thinking"
					msg.Content = block.Thinking
				case "tool_use":
					msg.MessageType = "tool_use"
					msg.Content = block.Name
				case "tool_result":
					msg.MessageType = "tool_result"
				}
			}
		}

		// Capture first user message for metadata
		if meta.FirstUserMessage == "" && line.Message.Role == "user" && msg.Content != "" {
			meta.FirstUserMessage = msg.Content
		}

		messages = append(messages, msg)
		seq++
	}

	return messages, meta, scanner.Err()
}
