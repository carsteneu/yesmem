package proxy

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestKeepaliveWithTTLDetector_Integration(t *testing.T) {
	var pingCount atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pingCount.Add(1)
		fmt.Fprintf(w, `{"type":"message","usage":{"cache_read_input_tokens":100,"cache_creation_input_tokens":0}}`)
	}))
	defer srv.Close()

	detector := NewCacheTTLDetector()
	// Set known 5min state so pings fire (auto mode blocks pings during detection)
	f := false
	detector.supported = &f
	ka := NewCacheKeepalive(CacheKeepaliveConfig{
		Target:           srv.URL,
		Mode:             "auto",
		Pings5m:          1,
		Detector:         detector,
		IntervalOverride: 20 * time.Millisecond,
		OnPing: func(threadID string, cacheRead, cacheWrite int) {
			detector.RecordResponse(cacheRead, cacheWrite, 0)
		},
	})
	defer ka.Stop()

	body := []byte(`{"model":"test","max_tokens":1024,"messages":[{"role":"user","content":"test"}]}`)
	ka.Reset("test-thread", body, "test-key")
	time.Sleep(100 * time.Millisecond)

	if int(pingCount.Load()) < 1 {
		t.Error("expected at least 1 ping")
	}
}

func TestKeepaliveOnPingFeedsDetector(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{
			"type": "message",
			"usage": {
				"cache_read_input_tokens": 50000,
				"cache_creation_input_tokens": 10000,
				"cache_creation": {"ephemeral_1h_input_tokens": 8000}
			}
		}`)
	}))
	defer srv.Close()

	detector := NewCacheTTLDetector()
	// Start with 5m so pings fire
	f := false
	detector.supported = &f

	ka := NewCacheKeepalive(CacheKeepaliveConfig{
		Target:           srv.URL,
		Mode:             "auto",
		Pings5m:          1,
		Detector:         detector,
		IntervalOverride: 10 * time.Millisecond,
		OnPing: func(threadID string, cacheRead, cacheWrite int) {
			// Ping callback doesn't pass ephemeral_1h — that's ok,
			// detection happens on real responses, not pings
			detector.RecordResponse(cacheRead, cacheWrite, 0)
		},
	})
	defer ka.Stop()

	body := []byte(`{"model":"test","max_tokens":1024,"messages":[{"role":"user","content":"test"}]}`)
	ka.Reset("test-thread", body, "test-key")
	time.Sleep(80 * time.Millisecond)

	// Ping results don't carry ephemeral_1h, so detector state shouldn't flip to 1h
	// It stays at "5m" because cacheWrite from ping is 0 (or ephemeral_1h is 0)
	sup := detector.Is1hSupported()
	if sup == nil {
		t.Error("detector should have a determined state")
	}
}
