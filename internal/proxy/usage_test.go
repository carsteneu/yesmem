package proxy

import (
	"strings"
	"testing"
)

func TestParseSSEUsage_MessageStart(t *testing.T) {
	data := []byte(`{"type":"message_start","message":{"usage":{"input_tokens":85234}}}`)
	u := &UsageTracker{}
	u.ParseSSEEvent(data)

	if u.InputTokens != 85234 {
		t.Errorf("expected input_tokens=85234, got %d", u.InputTokens)
	}
	if u.TotalInputTokens() != 85234 {
		t.Errorf("expected total=85234, got %d", u.TotalInputTokens())
	}
}

func TestParseSSEUsage_MessageDelta(t *testing.T) {
	data := []byte(`{"type":"message_delta","usage":{"output_tokens":1523}}`)
	u := &UsageTracker{}
	u.ParseSSEEvent(data)

	if u.OutputTokens != 1523 {
		t.Errorf("expected output_tokens=1523, got %d", u.OutputTokens)
	}
}

func TestParseSSEUsage_WithCacheTokens(t *testing.T) {
	data := []byte(`{"type":"message_start","message":{"usage":{"input_tokens":3,"cache_creation_input_tokens":15000,"cache_read_input_tokens":20000}}}`)
	u := &UsageTracker{}
	u.ParseSSEEvent(data)

	if u.InputTokens != 3 {
		t.Errorf("expected input_tokens=3 (uncached only), got %d", u.InputTokens)
	}
	if u.CacheCreationInputTokens != 15000 {
		t.Errorf("expected cache_creation=15000, got %d", u.CacheCreationInputTokens)
	}
	if u.CacheReadInputTokens != 20000 {
		t.Errorf("expected cache_read=20000, got %d", u.CacheReadInputTokens)
	}
	if u.TotalInputTokens() != 35003 {
		t.Errorf("expected total=35003, got %d", u.TotalInputTokens())
	}
}

func TestParseSSEUsage_FullSequence(t *testing.T) {
	u := &UsageTracker{}
	u.ParseSSEEvent([]byte(`{"type":"message_start","message":{"usage":{"input_tokens":5000,"cache_read_input_tokens":45000}}}`))
	u.ParseSSEEvent([]byte(`{"type":"content_block_delta","delta":{"type":"text_delta","text":"hi"}}`))
	u.ParseSSEEvent([]byte(`{"type":"message_delta","usage":{"output_tokens":800}}`))
	u.ParseSSEEvent([]byte(`{"type":"message_stop"}`))

	if u.InputTokens != 5000 {
		t.Errorf("input_tokens: expected 5000, got %d", u.InputTokens)
	}
	if u.CacheReadInputTokens != 45000 {
		t.Errorf("cache_read: expected 45000, got %d", u.CacheReadInputTokens)
	}
	if u.TotalInputTokens() != 50000 {
		t.Errorf("total: expected 50000, got %d", u.TotalInputTokens())
	}
	if u.OutputTokens != 800 {
		t.Errorf("output_tokens: expected 800, got %d", u.OutputTokens)
	}
	if !u.Complete {
		t.Error("expected Complete=true after message_stop")
	}
	if u.CacheHitRate() != 90.0 {
		t.Errorf("expected 90%% cache hit rate, got %.1f%%", u.CacheHitRate())
	}
}

func TestParseSSEUsage_IgnoresGarbage(t *testing.T) {
	u := &UsageTracker{}
	u.ParseSSEEvent([]byte(`not json`))
	u.ParseSSEEvent([]byte(``))
	u.ParseSSEEvent([]byte(`{"type":"ping"}`))

	if u.InputTokens != 0 || u.OutputTokens != 0 {
		t.Error("garbage should not affect token counts")
	}
}

func TestUsageLogLine_WithCache(t *testing.T) {
	u := &UsageTracker{
		InputTokens:              5000,
		CacheCreationInputTokens: 15000,
		CacheReadInputTokens:     65000,
		OutputTokens:             1523,
		Complete:                 true,
	}
	line := u.LogLine(5, 23, 65000, "test-thread-123")
	if line == "" {
		t.Error("log line should not be empty")
	}
	if !strings.Contains(line, "in=85000") {
		t.Errorf("log line missing total input: %q", line)
	}
	if !strings.Contains(line, "cache:") {
		t.Errorf("log line missing cache breakdown: %q", line)
	}
	if !strings.Contains(line, "hit") {
		t.Errorf("log line missing hit rate: %q", line)
	}
	if !strings.Contains(line, "stubbed: 23") {
		t.Errorf("log line missing stub count: %q", line)
	}
}

func TestUsageLogLine_NoCache(t *testing.T) {
	u := &UsageTracker{
		InputTokens:  85234,
		OutputTokens: 1523,
		Complete:     true,
	}
	line := u.LogLine(5, 0, 0, "")
	if strings.Contains(line, "cache:") {
		t.Errorf("log line should not show cache when no caching: %q", line)
	}
}

func TestCacheHitRate(t *testing.T) {
	u := &UsageTracker{InputTokens: 0, CacheReadInputTokens: 0}
	if u.CacheHitRate() != 0 {
		t.Errorf("expected 0%% for empty, got %.1f%%", u.CacheHitRate())
	}

	u2 := &UsageTracker{InputTokens: 10000, CacheReadInputTokens: 90000}
	if u2.CacheHitRate() != 90.0 {
		t.Errorf("expected 90%%, got %.1f%%", u2.CacheHitRate())
	}
}

func TestDeflateUsage_ScalesTokens(t *testing.T) {
	data := []byte(`{"type":"message_start","message":{"model":"claude-opus-4-6","usage":{"input_tokens":100000,"cache_creation_input_tokens":20000,"cache_read_input_tokens":50000}}}`)
	result := deflateUsage(data, 0.7)
	if result == nil {
		t.Fatal("deflateUsage returned nil")
	}

	// Parse and verify scaled values
	u := &UsageTracker{}
	u.ParseSSEEvent(result)
	if u.InputTokens != 70000 {
		t.Errorf("expected input_tokens=70000, got %d", u.InputTokens)
	}
	if u.CacheCreationInputTokens != 14000 {
		t.Errorf("expected cache_creation=14000, got %d", u.CacheCreationInputTokens)
	}
	if u.CacheReadInputTokens != 35000 {
		t.Errorf("expected cache_read=35000, got %d", u.CacheReadInputTokens)
	}
}

func TestDeflateUsage_DisabledAtZero(t *testing.T) {
	data := []byte(`{"type":"message_start","message":{"usage":{"input_tokens":100000}}}`)
	// Factor 0 should not be called (checked in proxy.go), but deflateUsage should still work
	result := deflateUsage(data, 0)
	if result == nil {
		t.Fatal("deflateUsage returned nil")
	}
	u := &UsageTracker{}
	u.ParseSSEEvent(result)
	if u.InputTokens != 0 {
		t.Errorf("factor 0 should zero out tokens, got %d", u.InputTokens)
	}
}

func TestDeflateUsage_InvalidJSON(t *testing.T) {
	result := deflateUsage([]byte(`not json`), 0.7)
	if result != nil {
		t.Error("expected nil for invalid JSON")
	}
}

func TestDeflateUsage_NoUsageField(t *testing.T) {
	result := deflateUsage([]byte(`{"type":"message_start","message":{}}`), 0.7)
	if result != nil {
		t.Error("expected nil when no usage field")
	}
}
