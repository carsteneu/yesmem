package proxy

import (
	"encoding/json"
	"fmt"
	"time"
)

type sseTranslator struct {
	model        string
	completionID string
	created      int64
	toolIndex    int
}

func newSSETranslator(model, completionID string) *sseTranslator {
	return &sseTranslator{
		model:        model,
		completionID: completionID,
		created:      time.Now().Unix(),
		toolIndex:    -1,
	}
}

func (t *sseTranslator) TranslateEvent(data []byte) [][]byte {
	var event struct {
		Type         string          `json:"type"`
		Index        int             `json:"index"`
		Delta        json.RawMessage `json:"delta"`
		Message      json.RawMessage `json:"message"`
		ContentBlock json.RawMessage `json:"content_block"`
	}
	if err := json.Unmarshal(data, &event); err != nil {
		return nil
	}

	switch event.Type {
	case "message_start":
		return t.handleMessageStart(event.Message)
	case "content_block_start":
		return t.handleContentBlockStart(event.ContentBlock)
	case "content_block_delta":
		return t.handleContentBlockDelta(event.Delta)
	case "content_block_stop":
		return nil
	case "message_delta":
		return t.handleMessageDelta(event.Delta)
	case "message_stop":
		return [][]byte{[]byte("[DONE]")}
	default:
		return nil
	}
}

func (t *sseTranslator) handleMessageStart(msg json.RawMessage) [][]byte {
	var m struct {
		Usage struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	json.Unmarshal(msg, &m)

	chunk := OpenAIStreamChunk{
		ID:      t.completionID,
		Object:  "chat.completion.chunk",
		Created: t.created,
		Model:   t.model,
		Choices: []OpenAIStreamChoice{{
			Index: 0,
			Delta: OpenAIDelta{Role: "assistant"},
		}},
	}
	if m.Usage.InputTokens > 0 {
		chunk.Usage = &OpenAIUsage{
			PromptTokens: m.Usage.InputTokens,
		}
	}
	return t.marshalChunk(chunk)
}

func (t *sseTranslator) handleContentBlockStart(block json.RawMessage) [][]byte {
	var b struct {
		Type string `json:"type"`
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	json.Unmarshal(block, &b)

	if b.Type == "tool_use" {
		t.toolIndex++
		chunk := OpenAIStreamChunk{
			ID:      t.completionID,
			Object:  "chat.completion.chunk",
			Created: t.created,
			Model:   t.model,
			Choices: []OpenAIStreamChoice{{
				Index: 0,
				Delta: OpenAIDelta{
					ToolCalls: []OpenAIToolCall{{
						ID:   b.ID,
						Type: "function",
						Function: OpenAIFunctionCall{Name: b.Name},
					}},
				},
			}},
		}
		return t.marshalChunk(chunk)
	}
	return nil
}

func (t *sseTranslator) handleContentBlockDelta(delta json.RawMessage) [][]byte {
	var d struct {
		Type        string `json:"type"`
		Text        string `json:"text"`
		PartialJSON string `json:"partial_json"`
	}
	json.Unmarshal(delta, &d)

	switch d.Type {
	case "text_delta":
		chunk := OpenAIStreamChunk{
			ID:      t.completionID,
			Object:  "chat.completion.chunk",
			Created: t.created,
			Model:   t.model,
			Choices: []OpenAIStreamChoice{{
				Index: 0,
				Delta: OpenAIDelta{Content: d.Text},
			}},
		}
		return t.marshalChunk(chunk)

	case "input_json_delta":
		chunk := OpenAIStreamChunk{
			ID:      t.completionID,
			Object:  "chat.completion.chunk",
			Created: t.created,
			Model:   t.model,
			Choices: []OpenAIStreamChoice{{
				Index: 0,
				Delta: OpenAIDelta{
					ToolCalls: []OpenAIToolCall{{
						Function: OpenAIFunctionCall{Arguments: d.PartialJSON},
					}},
				},
			}},
		}
		return t.marshalChunk(chunk)

	default:
		return nil
	}
}

func (t *sseTranslator) handleMessageDelta(delta json.RawMessage) [][]byte {
	var d struct {
		StopReason string `json:"stop_reason"`
		Usage      struct {
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	json.Unmarshal(delta, &d)

	finishReason := mapStopReason(d.StopReason)
	chunk := OpenAIStreamChunk{
		ID:      t.completionID,
		Object:  "chat.completion.chunk",
		Created: t.created,
		Model:   t.model,
		Choices: []OpenAIStreamChoice{{
			Index:        0,
			Delta:        OpenAIDelta{},
			FinishReason: &finishReason,
		}},
	}
	if d.Usage.OutputTokens > 0 {
		chunk.Usage = &OpenAIUsage{
			CompletionTokens: d.Usage.OutputTokens,
		}
	}
	return t.marshalChunk(chunk)
}

func (t *sseTranslator) marshalChunk(chunk OpenAIStreamChunk) [][]byte {
	data, err := json.Marshal(chunk)
	if err != nil {
		return nil
	}
	return [][]byte{data}
}

func mapStopReason(reason string) string {
	switch reason {
	case "end_turn":
		return "stop"
	case "tool_use":
		return "tool_calls"
	case "max_tokens":
		return "length"
	case "stop_sequence":
		return "stop"
	default:
		return "stop"
	}
}

func generateCompletionID() string {
	return fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano())
}
