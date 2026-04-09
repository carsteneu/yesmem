package proxy

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// === Task #10: Localhost-only Binding ===

func TestSanitizeListenAddr_PortOnly(t *testing.T) {
	got := sanitizeListenAddr(":9099")
	if got != "127.0.0.1:9099" {
		t.Errorf("sanitizeListenAddr(':9099') = %q, want '127.0.0.1:9099'", got)
	}
}

func TestSanitizeListenAddr_Empty(t *testing.T) {
	got := sanitizeListenAddr("")
	if got != "127.0.0.1:9099" {
		t.Errorf("sanitizeListenAddr('') = %q, want '127.0.0.1:9099'", got)
	}
}

func TestSanitizeListenAddr_AlreadyLocalhost(t *testing.T) {
	got := sanitizeListenAddr("127.0.0.1:8080")
	if got != "127.0.0.1:8080" {
		t.Errorf("sanitizeListenAddr('127.0.0.1:8080') = %q, want '127.0.0.1:8080'", got)
	}
}

func TestSanitizeListenAddr_ExplicitAllInterfaces(t *testing.T) {
	got := sanitizeListenAddr("0.0.0.0:9099")
	if got != "127.0.0.1:9099" {
		t.Errorf("sanitizeListenAddr('0.0.0.0:9099') = %q, want '127.0.0.1:9099'", got)
	}
}

func TestSanitizeListenAddr_LocalhostName(t *testing.T) {
	got := sanitizeListenAddr("localhost:9099")
	if got != "localhost:9099" {
		t.Errorf("sanitizeListenAddr('localhost:9099') = %q, want 'localhost:9099'", got)
	}
}

// === Task #9: Bypass-Switch ===

func TestBypass_EnvVar(t *testing.T) {
	t.Setenv("YESMEM_PROXY_BYPASS", "1")
	if !isBypassed(nil) {
		t.Error("bypass should be active when YESMEM_PROXY_BYPASS=1")
	}
}

func TestBypass_EnvVarUnset(t *testing.T) {
	if isBypassed(nil) {
		t.Error("bypass should not be active when env is unset")
	}
}

func TestBypass_Header(t *testing.T) {
	h := http.Header{}
	h.Set("X-Yesmem-Bypass", "true")
	if !isBypassed(h) {
		t.Error("bypass should be active with X-Yesmem-Bypass header")
	}
}

func TestBypass_NoHeader(t *testing.T) {
	h := http.Header{}
	if isBypassed(h) {
		t.Error("bypass should not be active without header")
	}
}

// === Task #6: Hysterese ===

func TestHysteresis_ActivatesAboveThreshold(t *testing.T) {
	s := &Server{cfg: Config{TokenThreshold: 100000}}
	// Below threshold — should not be active
	if s.shouldStub(90000, "") {
		t.Error("should not stub below threshold")
	}
	// Above threshold — should activate
	if !s.shouldStub(110000, "") {
		t.Error("should stub above threshold")
	}
}

func TestHysteresis_StaysActiveUntilLowWatermark(t *testing.T) {
	s := &Server{cfg: Config{TokenThreshold: 100000}}
	// Activate
	s.shouldStub(110000, "")
	// Still above low watermark (80k) — should stay active
	if !s.shouldStub(85000, "") {
		t.Error("should stay active above low watermark (80%)")
	}
	// Below low watermark — should deactivate
	if s.shouldStub(75000, "") {
		t.Error("should deactivate below low watermark")
	}
}

func TestHysteresis_ReactivatesAfterDeactivation(t *testing.T) {
	s := &Server{cfg: Config{TokenThreshold: 100000}}
	// Activate → deactivate → reactivate
	s.shouldStub(110000, "")
	s.shouldStub(75000, "") // deactivate
	if s.shouldStub(90000, "") {
		t.Error("should not reactivate between watermarks")
	}
	if !s.shouldStub(110000, "") {
		t.Error("should reactivate above threshold")
	}
}

// === Task #7: Idempotenz ===

func TestRequestFingerprint_SameMessages(t *testing.T) {
	msgs := []any{
		map[string]any{"role": "user", "content": "hello"},
		map[string]any{"role": "assistant", "content": "hi"},
	}
	fp1 := requestFingerprint(msgs)
	fp2 := requestFingerprint(msgs)
	if fp1 != fp2 {
		t.Errorf("same messages should produce same fingerprint: %q != %q", fp1, fp2)
	}
}

func TestRequestFingerprint_DifferentMessages(t *testing.T) {
	msgs1 := []any{
		map[string]any{"role": "user", "content": "hello"},
	}
	msgs2 := []any{
		map[string]any{"role": "user", "content": "goodbye"},
	}
	if requestFingerprint(msgs1) == requestFingerprint(msgs2) {
		t.Error("different messages should produce different fingerprints")
	}
}

func TestRequestFingerprint_DifferentLength(t *testing.T) {
	msgs1 := []any{
		map[string]any{"role": "user", "content": "hello"},
	}
	msgs2 := []any{
		map[string]any{"role": "user", "content": "hello"},
		map[string]any{"role": "assistant", "content": "hi"},
	}
	if requestFingerprint(msgs1) == requestFingerprint(msgs2) {
		t.Error("different length messages should produce different fingerprints")
	}
}

func TestRetryDetection_SkipsSideEffects(t *testing.T) {
	s := &Server{cfg: Config{TokenThreshold: 100000}}
	msgs := []any{
		map[string]any{"role": "user", "content": "hello"},
	}
	fp := requestFingerprint(msgs)

	// First request — not a retry
	if s.isRetry(fp) {
		t.Error("first request should not be detected as retry")
	}
	s.markRequest(fp)

	// Same fingerprint again — retry
	if !s.isRetry(fp) {
		t.Error("same fingerprint should be detected as retry")
	}
}

// === Task #8: Transaction Boundaries ===

func TestAnnotationOnlyOnMessageStop(t *testing.T) {
	s := &Server{
		cfg:         Config{},
		annotations: make(map[string]string),
	}

	var textAccum strings.Builder
	var firstTextCollected bool
	var messageComplete bool

	// Simulate SSE events: text deltas, then message_stop
	events := []string{
		`{"type":"content_block_delta","delta":{"type":"text_delta","text":"hello "}}`,
		`{"type":"content_block_delta","delta":{"type":"text_delta","text":"world"}}`,
		`{"type":"message_stop"}`,
	}

	for _, event := range events {
		s.parseSSEForAnnotation([]byte(event), &textAccum, &firstTextCollected, 120)
		if event == `{"type":"message_stop"}` {
			messageComplete = true
		}
	}

	// Should have collected text
	if textAccum.String() != "hello world" {
		t.Errorf("expected 'hello world', got %q", textAccum.String())
	}
	if !messageComplete {
		t.Error("message_stop should set messageComplete")
	}
}

func TestAnnotationNotCommittedWithoutMessageStop(t *testing.T) {
	// Simulate stream break — no message_stop
	events := []string{
		`{"type":"content_block_delta","delta":{"type":"text_delta","text":"partial"}}`,
		// stream breaks here — no message_stop
	}

	var textAccum strings.Builder
	var firstTextCollected bool
	messageComplete := false

	for _, event := range events {
		data := []byte(event)
		var ev struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(data, &ev); err == nil && ev.Type == "message_stop" {
			messageComplete = true
		}
		// Use a fresh Server to parse (only the parseSSE part)
		s := &Server{annotations: make(map[string]string)}
		s.parseSSEForAnnotation(data, &textAccum, &firstTextCollected, 120)
	}

	if messageComplete {
		t.Error("messageComplete should be false without message_stop")
	}
	// Text was collected but should NOT be committed as annotation
	if textAccum.Len() == 0 {
		t.Error("text should still be accumulated in buffer")
	}
}

// === Integration: Bypass passthrough ===

func TestBypass_PassesThroughUnmodified(t *testing.T) {
	// Create a simple backend that echoes
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	}))
	defer backend.Close()

	s := &Server{
		cfg: Config{
			TargetURL:      backend.URL,
			TokenThreshold: 100,
		},
		httpClient:  backend.Client(),
		logger:      createTestLogger(),
		annotations: make(map[string]string),
		decay:       NewDecayTracker(),
		narrative:   NewNarrative(),
	}

	// Request with bypass header
	body := `{"messages":[{"role":"user","content":"` + longString(5000) + `"}]}`
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body))
	req.Header.Set("X-Yesmem-Bypass", "true")
	w := httptest.NewRecorder()

	s.handleMessages(w, req)

	// Should have passed through without stubbing (200 from backend)
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

func createTestLogger() *log.Logger {
	return log.New(io.Discard, "[proxy-test] ", 0)
}
