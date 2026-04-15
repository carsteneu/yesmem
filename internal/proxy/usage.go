package proxy

import (
	"encoding/json"
	"fmt"
	"math"
)

// UsageTracker extracts real token counts from the SSE response stream.
type UsageTracker struct {
	InputTokens              int
	CacheCreationInputTokens int
	CacheReadInputTokens     int
	Ephemeral1hInputTokens   int // from cache_creation.ephemeral_1h_input_tokens
	OutputTokens             int
	Complete                 bool // true after message_stop
}

// ParseSSEEvent processes a single SSE data payload for usage information.
func (u *UsageTracker) ParseSSEEvent(data []byte) {
	if len(data) == 0 || data[0] != '{' {
		return
	}

	var event struct {
		Type    string `json:"type"`
		Message struct {
			Usage struct {
				InputTokens              int `json:"input_tokens"`
				CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
				CacheReadInputTokens     int `json:"cache_read_input_tokens"`
				CacheCreation            struct {
					Ephemeral1hInputTokens int `json:"ephemeral_1h_input_tokens"`
				} `json:"cache_creation"`
			} `json:"usage"`
		} `json:"message"`
		Usage struct {
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}

	if err := json.Unmarshal(data, &event); err != nil {
		return
	}

	switch event.Type {
	case "message_start":
		u.InputTokens = event.Message.Usage.InputTokens
		u.CacheCreationInputTokens = event.Message.Usage.CacheCreationInputTokens
		u.CacheReadInputTokens = event.Message.Usage.CacheReadInputTokens
		u.Ephemeral1hInputTokens = event.Message.Usage.CacheCreation.Ephemeral1hInputTokens
	case "message_delta":
		u.OutputTokens = event.Usage.OutputTokens
	case "message_stop":
		u.Complete = true
	}
}

// TotalInputTokens returns the sum of all input token types.
func (u *UsageTracker) TotalInputTokens() int {
	return u.InputTokens + u.CacheCreationInputTokens + u.CacheReadInputTokens
}

// CacheHitRate returns the percentage of input tokens served from cache.
func (u *UsageTracker) CacheHitRate() float64 {
	total := u.TotalInputTokens()
	if total == 0 {
		return 0
	}
	return float64(u.CacheReadInputTokens) / float64(total) * 100
}

// LogLine formats a usage log line for a completed request.
func (u *UsageTracker) LogLine(reqIdx, stubCount, estimatedTokens int) string {
	total := u.TotalInputTokens()
	if total == 0 && u.OutputTokens == 0 {
		return ""
	}

	line := fmt.Sprintf("[req %d] in=%d out=%d", reqIdx, total, u.OutputTokens)

	// Cache breakdown
	if u.CacheReadInputTokens > 0 || u.CacheCreationInputTokens > 0 {
		line += fmt.Sprintf(" | cache: %dk read, %dk write, %dk uncached (%.0f%% hit)",
			u.CacheReadInputTokens/1000,
			u.CacheCreationInputTokens/1000,
			u.InputTokens/1000,
			u.CacheHitRate())
	}

	if stubCount > 0 {
		line += fmt.Sprintf(" | stubbed: %d msgs", stubCount)
	}

	if estimatedTokens > 0 && total > 0 {
		diff := float64(total-estimatedTokens) / float64(estimatedTokens) * 100
		line += fmt.Sprintf(" | estimate: %dk, actual: %dk (diff: %+.0f%%)",
			estimatedTokens/1000, total/1000, diff)
	}

	return line
}

// deflateUsage scales down input_tokens in a message_start SSE event.
// Returns modified JSON bytes, or nil if parsing fails.
func deflateUsage(data []byte, factor float64) []byte {
	var event map[string]any
	if json.Unmarshal(data, &event) != nil {
		return nil
	}

	msg, ok := event["message"].(map[string]any)
	if !ok {
		return nil
	}
	usage, ok := msg["usage"].(map[string]any)
	if !ok {
		return nil
	}

	// Scale down each token field
	for _, key := range []string{"input_tokens", "cache_creation_input_tokens", "cache_read_input_tokens"} {
		if v, ok := usage[key].(float64); ok && v > 0 {
			usage[key] = math.Round(v * factor)
		}
	}

	out, err := json.Marshal(event)
	if err != nil {
		return nil
	}
	return out
}
