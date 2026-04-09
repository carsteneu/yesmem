package proxy

import (
	"encoding/json"
	"strings"
)

// parseUsageFromSSE parses token usage from a raw SSE line (including "data: " prefix).
// Returns (inputTokens, outputTokens, found). Only returns found=true for message_start
// (with non-zero tokens) and message_delta (with non-zero output_tokens).
func parseUsageFromSSE(line string) (int, int, bool) {
	if !strings.HasPrefix(line, "data: ") {
		return 0, 0, false
	}
	raw := strings.TrimPrefix(line, "data: ")
	var envelope struct {
		Type    string `json:"type"`
		Message struct {
			Usage struct {
				InputTokens  int `json:"input_tokens"`
				OutputTokens int `json:"output_tokens"`
			} `json:"usage"`
		} `json:"message"`
		Usage struct {
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal([]byte(raw), &envelope); err != nil {
		return 0, 0, false
	}
	switch envelope.Type {
	case "message_start":
		u := envelope.Message.Usage
		if u.InputTokens == 0 && u.OutputTokens == 0 {
			return 0, 0, false
		}
		return u.InputTokens, u.OutputTokens, true
	case "message_delta":
		if envelope.Usage.OutputTokens == 0 {
			return 0, 0, false
		}
		return 0, envelope.Usage.OutputTokens, true
	}
	return 0, 0, false
}
