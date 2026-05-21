package indexer

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/carsteneu/yesmem/internal/models"
)

type opencodeDBPart struct {
	Type      string            `json:"type"`
	Text      string            `json:"text,omitempty"`
	Tool      string            `json:"tool,omitempty"`
	CallID    string            `json:"callID,omitempty"`
	ToolState *opencodeToolState `json:"state,omitempty"`
}

type opencodeToolState struct {
	Status string          `json:"status"`
	Input  json.RawMessage `json:"input"`
	Output string          `json:"output"`
}

type opencodeDBMessage struct {
	ID        string        `json:"id"`
	SessionID string        `json:"session_id"`
	Role      string        `json:"role"`
	CreatedMs int64         `json:"time_created"`
	Part      opencodeDBPart `json:"part"`
}

type opencodeDBSession struct {
	ID        string    `json:"id"`
	Directory string    `json:"directory"`
	Title     string    `json:"title"`
	Created   time.Time `json:"time_created"`
	Updated   time.Time `json:"time_updated"`
	ParentID  string    `json:"parent_id"`
	Model     string    `json:"model"`
	Agent     string    `json:"agent"`
}

func mapOpencodeMessages(dbMsgs []opencodeDBMessage) []models.Message {
	out := make([]models.Message, 0, len(dbMsgs)*2)
	seq := 0
	for _, dm := range dbMsgs {
		switch dm.Part.Type {
		case "step-start", "step-finish":
			continue
		case "text":
			out = append(out, models.Message{
				SessionID:   dm.SessionID,
				SourceAgent: models.SourceAgentOpencode,
				Role:        dm.Role,
				MessageType: "text",
				Content:     dm.Part.Text,
				Timestamp:   time.UnixMilli(dm.CreatedMs),
				Sequence:    seq,
			})
			seq++
		case "reasoning":
			out = append(out, models.Message{
				SessionID:   dm.SessionID,
				SourceAgent: models.SourceAgentOpencode,
				Role:        "assistant",
				MessageType: "thinking",
				ContentBlob: []byte(dm.Part.Text),
				Timestamp:   time.UnixMilli(dm.CreatedMs),
				Sequence:    seq,
			})
			seq++
		case "tool":
			if dm.Part.ToolState == nil {
				continue
			}
			toolName := dm.Part.Tool
			toolInput := formatToolInput(dm.Part.ToolState.Input)
			toolOutput := dm.Part.ToolState.Output

			out = append(out, models.Message{
				SessionID:   dm.SessionID,
				SourceAgent: models.SourceAgentOpencode,
				Role:        "assistant",
				MessageType: "tool_use",
				ToolName:    toolName,
				Content:     toolInput,
				Timestamp:   time.UnixMilli(dm.CreatedMs),
				Sequence:    seq,
			})
			seq++

			trMsg := models.Message{
				SessionID:   dm.SessionID,
				SourceAgent: models.SourceAgentOpencode,
				Role:        "user",
				MessageType: "tool_result",
				Timestamp:   time.UnixMilli(dm.CreatedMs),
				Sequence:    seq,
			}
			if len(toolOutput) > 10240 {
				trMsg.Content = toolOutput[:10240]
				trMsg.ContentBlob = []byte(toolOutput)
			} else {
				trMsg.Content = toolOutput
				trMsg.ContentBlob = []byte(toolOutput)
			}
			out = append(out, trMsg)
			seq++
		}
	}
	return out
}

func formatToolInput(input json.RawMessage) string {
	if len(input) == 0 {
		return ""
	}
	var m map[string]any
	if err := json.Unmarshal(input, &m); err != nil {
		return string(input)
	}
	var parts []string
	for k, v := range m {
		parts = append(parts, fmt.Sprintf("%s=%v", k, v))
	}
	return strings.Join(parts, " ")
}

func buildOpencodeSession(dbSess opencodeDBSession, msgs []models.Message) *models.Session {
	projectDir := dbSess.Directory
	projectShort := models.ProjectShortFromPath(projectDir)

	sessID := models.NormalizeSessionID(models.SourceAgentOpencode, dbSess.ID)
	var parentID string
	if dbSess.ParentID != "" {
		parentID = models.NormalizeSessionID(models.SourceAgentOpencode, dbSess.ParentID)
	}

	firstMsg := ""
	if len(msgs) > 0 {
		firstMsg = msgs[0].Content
	}

	return &models.Session{
		ID:              sessID,
		Project:         projectDir,
		ProjectShort:    projectShort,
		FirstMessage:    firstMsg,
		MessageCount:    len(msgs),
		StartedAt:       dbSess.Created,
		EndedAt:         dbSess.Updated,
		JSONLPath:       "",
		JSONLSize:       0,
		IndexedAt:       time.Now(),
		ParentSessionID: parentID,
		SourceAgent:     models.SourceAgentOpencode,
	}
}
