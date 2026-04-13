package proxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestCacheKeepalive_SendsPing(t *testing.T) {
	var pingCount atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pingCount.Add(1)
		fmt.Fprintf(w, `{"type":"message","usage":{"cache_read_input_tokens":100,"cache_creation_input_tokens":0}}`)
	}))
	defer srv.Close()

	ka := NewCacheKeepalive(CacheKeepaliveConfig{
		Target: srv.URL, Mode: "5m", Pings5m: 2, IntervalOverride: 50 * time.Millisecond,
	})
	body := []byte(`{"model":"test","max_tokens":1024,"messages":[{"role":"user","content":"test"}]}`)
	ka.Reset("test-thread", body, "test-key")
	time.Sleep(200 * time.Millisecond)
	ka.Stop()

	if int(pingCount.Load()) < 1 {
		t.Errorf("expected at least 1 ping, got %d", pingCount.Load())
	}
}

func TestCacheKeepalive_StopsAfterMaxPings(t *testing.T) {
	var pingCount atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pingCount.Add(1)
		fmt.Fprintf(w, `{"type":"message","usage":{"cache_read_input_tokens":100}}`)
	}))
	defer srv.Close()

	ka := NewCacheKeepalive(CacheKeepaliveConfig{
		Target: srv.URL, Mode: "5m", Pings5m: 2, IntervalOverride: 30 * time.Millisecond,
	})
	body := []byte(`{"model":"test","max_tokens":1024,"messages":[{"role":"user","content":"test"}]}`)
	ka.Reset("test-thread", body, "test-key")
	time.Sleep(300 * time.Millisecond)
	ka.Stop()

	if int(pingCount.Load()) != 2 {
		t.Errorf("expected exactly 2 pings, got %d", pingCount.Load())
	}
}

func TestCacheKeepalive_ResetCancelsOld(t *testing.T) {
	var pingCount atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pingCount.Add(1)
		fmt.Fprintf(w, `{"type":"message","usage":{"cache_read_input_tokens":100}}`)
	}))
	defer srv.Close()

	ka := NewCacheKeepalive(CacheKeepaliveConfig{
		Target: srv.URL, Mode: "5m", Pings5m: 5, IntervalOverride: 50 * time.Millisecond,
	})
	body := []byte(`{"model":"test","max_tokens":1024,"messages":[{"role":"user","content":"test"}]}`)
	ka.Reset("test-thread", body, "test-key")
	time.Sleep(30 * time.Millisecond)
	ka.Reset("test-thread", body, "test-key") // should cancel first timer
	time.Sleep(200 * time.Millisecond)
	ka.Stop()

	if int(pingCount.Load()) > 5 {
		t.Errorf("pings should not exceed 5 after reset, got %d", pingCount.Load())
	}
}

func TestCacheKeepalive_DisabledWhenZeroPings(t *testing.T) {
	ka := NewCacheKeepalive(CacheKeepaliveConfig{
		Mode: "5m", Pings5m: 0, IntervalOverride: 50 * time.Millisecond,
	})
	body := []byte(`{"model":"test","max_tokens":1024}`)
	ka.Reset("test-thread", body, "test-key")
	time.Sleep(100 * time.Millisecond)
	ka.Stop()
	// No panic, no pings — just verifying it doesn't crash
}

func TestCacheKeepalive_PingModifiesMaxTokens(t *testing.T) {
	var receivedMaxTokens int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]json.RawMessage
		json.NewDecoder(r.Body).Decode(&body)
		var mt int
		json.Unmarshal(body["max_tokens"], &mt)
		receivedMaxTokens = mt
		fmt.Fprintf(w, `{"type":"message","usage":{"cache_read_input_tokens":100}}`)
	}))
	defer srv.Close()

	ka := NewCacheKeepalive(CacheKeepaliveConfig{
		Target: srv.URL, Mode: "5m", Pings5m: 1, IntervalOverride: 10 * time.Millisecond,
	})
	body := []byte(`{"model":"test","max_tokens":4096,"messages":[{"role":"user","content":"test"}]}`)
	ka.Reset("test-thread", body, "test-key")
	time.Sleep(80 * time.Millisecond)
	ka.Stop()

	if receivedMaxTokens != 1 {
		t.Errorf("ping should set max_tokens=1, got %d", receivedMaxTokens)
	}
}

func TestCacheKeepalive_Retrigger(t *testing.T) {
	var pingCount atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pingCount.Add(1)
		fmt.Fprintf(w, `{"type":"message","usage":{"cache_read_input_tokens":100}}`)
	}))
	defer srv.Close()

	det := NewCacheTTLDetector()
	ka := NewCacheKeepalive(CacheKeepaliveConfig{
		Target:           srv.URL,
		Mode:             "auto",
		Pings5m:          2,
		Pings1h:          1,
		Detector:         det,
		IntervalOverride: 30 * time.Millisecond,
	})
	defer ka.Stop()

	body := []byte(`{"model":"test","max_tokens":1024,"messages":[{"role":"user","content":"test"}]}`)
	// Reset while detection unknown → 0 pings, timer not started
	ka.Reset("test-thread", body, "test-key")
	time.Sleep(100 * time.Millisecond)
	if pingCount.Load() != 0 {
		t.Errorf("should have 0 pings during unknown detection, got %d", pingCount.Load())
	}

	// Simulate detection → 1h confirmed
	det.mu.Lock()
	det.threadRequests["t1"] = time.Now().Add(-6 * time.Minute)
	det.currentThread = "t1"
	det.mu.Unlock()
	det.RecordResponse(100000, 2000, 0) // >80% read after gap → 1h

	// Retrigger → should now start pinging with pings_1h=1
	ka.Retrigger()
	time.Sleep(100 * time.Millisecond)

	if pingCount.Load() < 1 {
		t.Errorf("after retrigger with 1h confirmed, expected ≥1 ping, got %d", pingCount.Load())
	}
}

func TestCacheKeepalive_DynamicInterval(t *testing.T) {
	// Mode "5m": always 270s
	ka5m := NewCacheKeepalive(CacheKeepaliveConfig{Mode: "5m", Pings5m: 1})
	if got := ka5m.effectiveInterval(); got != 270*time.Second {
		t.Errorf("mode 5m: expected 270s, got %v", got)
	}

	// Mode "1h": always 3240s
	ka1h := NewCacheKeepalive(CacheKeepaliveConfig{Mode: "1h", Pings1h: 1})
	if got := ka1h.effectiveInterval(); got != 3240*time.Second {
		t.Errorf("mode 1h: expected 3240s, got %v", got)
	}

	// Mode "auto", detector unknown → 270s (5min, conservative)
	det := NewCacheTTLDetector()
	kaAuto := NewCacheKeepalive(CacheKeepaliveConfig{Mode: "auto", Pings5m: 1, Pings1h: 1, Detector: det})
	if got := kaAuto.effectiveInterval(); got != 270*time.Second {
		t.Errorf("auto unknown: expected 270s, got %v", got)
	}

	// Mode "auto", detector confirms 1h → 3240s
	det.mu.Lock()
	det.threadRequests["t1"] = time.Now().Add(-6 * time.Minute)
	det.currentThread = "t1"
	det.mu.Unlock()
	det.RecordResponse(50000, 10000, 8000)
	if got := kaAuto.effectiveInterval(); got != 3240*time.Second {
		t.Errorf("auto 1h confirmed: expected 3240s, got %v", got)
	}

	// Mode "auto", detector denies 1h → 270s
	det.RecordResponse(0, 15000, 0)
	if got := kaAuto.effectiveInterval(); got != 270*time.Second {
		t.Errorf("auto 5m: expected 270s, got %v", got)
	}
}

// Regression: Reset("B") must NOT cancel thread A's timer.
// Previously, all threads shared one timer — any Reset killed everyone's countdown.
func TestCacheKeepalive_PerThreadTimerIndependence(t *testing.T) {
	pingsPerThread := make(map[string]*atomic.Int32)
	pingsPerThread["thread-A"] = &atomic.Int32{}
	pingsPerThread["thread-B"] = &atomic.Int32{}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"type":"message","usage":{"cache_read_input_tokens":100}}`)
	}))
	defer srv.Close()

	ka := NewCacheKeepalive(CacheKeepaliveConfig{
		Target:           srv.URL,
		Mode:             "5m",
		Pings5m:          3,
		IntervalOverride: 80 * time.Millisecond,
		OnPing: func(threadID string, cacheRead, cacheWrite int) {
			if counter, ok := pingsPerThread[threadID]; ok {
				counter.Add(1)
			}
		},
	})
	defer ka.Stop()

	body := []byte(`{"model":"test","max_tokens":1024,"messages":[{"role":"user","content":"test"}]}`)

	// Thread A sends one request, then goes quiet (user thinking)
	ka.Reset("thread-A", body, "key-a")

	// Thread B resets every 20ms for 300ms — keeps the shared timer from ever firing
	// (interval=80ms, reset every 20ms → timer never reaches 80ms)
	done := make(chan struct{})
	go func() {
		for i := 0; i < 15; i++ {
			time.Sleep(20 * time.Millisecond)
			ka.Reset("thread-B", body, "key-b")
		}
		close(done)
	}()
	<-done

	aPings := pingsPerThread["thread-A"].Load()
	if aPings == 0 {
		t.Errorf("thread-A should have received pings (independent timer), got 0 — shared timer was starved by thread-B resets")
	}
	t.Logf("thread-A pings: %d, thread-B pings: %d", aPings, pingsPerThread["thread-B"].Load())
}

func TestBuildPingBody_StripsThinkingAndTools(t *testing.T) {
	original := []byte(`{"model":"claude-opus-4-6","max_tokens":64000,"thinking":{"type":"adaptive"},"tools":[{"name":"test"}],"context_management":{"pruning":true},"messages":[{"role":"user","content":"test"}],"system":[{"type":"text","text":"hello","cache_control":{"type":"ephemeral"}}],"metadata":{"user_id":"u1"}}`)

	result := buildPingBody(original)

	var m map[string]json.RawMessage
	if err := json.Unmarshal(result, &m); err != nil {
		t.Fatalf("failed to unmarshal ping body: %v", err)
	}

	if _, ok := m["thinking"]; !ok {
		t.Error("ping body must preserve 'thinking' — adaptive works with max_tokens=1")
	}
	if _, ok := m["context_management"]; ok {
		t.Error("ping body must not contain 'context_management' — rejected by API")
	}
	if _, ok := m["metadata"]; ok {
		t.Error("ping body must not contain 'metadata'")
	}

	// tools MUST be preserved — they are part of the cache prefix
	if _, ok := m["tools"]; !ok {
		t.Error("ping body must preserve 'tools' (part of cache prefix)")
	}

	var mt int
	json.Unmarshal(m["max_tokens"], &mt)
	if mt != 1 {
		t.Errorf("expected max_tokens=1, got %d", mt)
	}

	if _, ok := m["system"]; !ok {
		t.Error("ping body must preserve 'system' (cache breakpoints)")
	}
	if _, ok := m["messages"]; !ok {
		t.Error("ping body must preserve 'messages'")
	}
	if _, ok := m["model"]; !ok {
		t.Error("ping body must preserve 'model'")
	}
}

func TestCacheKeepalive_PingLogsErrorResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		fmt.Fprintf(w, `{"type":"error","error":{"type":"invalid_request_error","message":"max_tokens: 1 is less than minimum"}}`)
	}))
	defer srv.Close()

	var logBuf bytes.Buffer
	logger := log.New(&logBuf, "", 0)

	ka := NewCacheKeepalive(CacheKeepaliveConfig{
		Target: srv.URL, Mode: "5m", Pings5m: 1, IntervalOverride: 10 * time.Millisecond,
		Logger: logger,
	})
	body := []byte(`{"model":"test","max_tokens":1024,"messages":[{"role":"user","content":"test"}]}`)
	ka.Reset("test-thread", body, "test-key")
	time.Sleep(80 * time.Millisecond)
	ka.Stop()

	logOutput := logBuf.String()
	if !strings.Contains(logOutput, "400") {
		t.Errorf("expected log to contain HTTP status 400, got: %s", logOutput)
	}
}

func TestCacheKeepalive_PingStripsThinkingEndToEnd(t *testing.T) {
	var receivedBody map[string]json.RawMessage
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&receivedBody)
		fmt.Fprintf(w, `{"type":"message","usage":{"cache_read_input_tokens":100}}`)
	}))
	defer srv.Close()

	ka := NewCacheKeepalive(CacheKeepaliveConfig{
		Target: srv.URL, Mode: "5m", Pings5m: 1, IntervalOverride: 10 * time.Millisecond,
	})
	body := []byte(`{"model":"test","max_tokens":64000,"thinking":{"type":"adaptive"},"tools":[{"name":"bash"}],"messages":[{"role":"user","content":"test"}]}`)
	ka.Reset("test-thread", body, "test-key")
	time.Sleep(80 * time.Millisecond)
	ka.Stop()

	if receivedBody == nil {
		t.Fatal("server received no ping request")
	}
	if _, ok := receivedBody["thinking"]; !ok {
		t.Error("ping request must preserve 'thinking' field (cache prefix)")
	}
	// tools MUST be present — part of cache prefix
	if _, ok := receivedBody["tools"]; !ok {
		t.Error("ping request must preserve 'tools' field (cache prefix)")
	}
	if _, ok := receivedBody["context_management"]; ok {
		t.Error("ping request must not contain 'context_management'")
	}
}

func TestCacheKeepalive_EffectivePings(t *testing.T) {
	// Mode "5m" → Pings5m
	ka5m := NewCacheKeepalive(CacheKeepaliveConfig{Mode: "5m", Pings5m: 3, Pings1h: 1})
	if got := ka5m.effectivePings(); got != 3 {
		t.Errorf("mode 5m: expected 3, got %d", got)
	}

	// Mode "1h" → Pings1h
	ka1h := NewCacheKeepalive(CacheKeepaliveConfig{Mode: "1h", Pings5m: 3, Pings1h: 1})
	if got := ka1h.effectivePings(); got != 1 {
		t.Errorf("mode 1h: expected 1, got %d", got)
	}

	// Mode "auto", unknown → 0 (detection pending)
	det := NewCacheTTLDetector()
	kaAuto := NewCacheKeepalive(CacheKeepaliveConfig{Mode: "auto", Pings5m: 3, Pings1h: 1, Detector: det})
	if got := kaAuto.effectivePings(); got != 0 {
		t.Errorf("auto unknown: expected 0 (detection), got %d", got)
	}

	// Mode "auto", 1h confirmed → Pings1h
	det.mu.Lock()
	det.threadRequests["t1"] = time.Now().Add(-6 * time.Minute)
	det.currentThread = "t1"
	det.mu.Unlock()
	det.RecordResponse(50000, 10000, 8000)
	if got := kaAuto.effectivePings(); got != 1 {
		t.Errorf("auto 1h: expected 1, got %d", got)
	}

	// Mode "auto", 5m → Pings5m
	det.RecordResponse(0, 15000, 0)
	if got := kaAuto.effectivePings(); got != 3 {
		t.Errorf("auto 5m: expected 3, got %d", got)
	}
}
