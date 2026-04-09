package proxy

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestHealthEndpoint_ReturnsJSON(t *testing.T) {
	s := &Server{
		cfg:         Config{},
		logger:      log.New(io.Discard, "", 0),
		annotations: make(map[string]string),
		decay:       NewDecayTracker(),
		narrative:   NewNarrative(),
		stats:       &ProxyStats{startTime: time.Now()},
	}

	req := httptest.NewRequest("GET", "/health", nil)
	w := httptest.NewRecorder()
	s.handleHealth(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}

	var result map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatalf("response not valid JSON: %v", err)
	}

	if result["status"] != "ok" {
		t.Errorf("expected status 'ok', got %v", result["status"])
	}
	if _, ok := result["uptime"]; !ok {
		t.Error("missing 'uptime' field")
	}
	if _, ok := result["requests"]; !ok {
		t.Error("missing 'requests' field")
	}
}

func TestHealthEndpoint_TracksRequestCount(t *testing.T) {
	stats := &ProxyStats{startTime: time.Now()}
	stats.RecordRequest(5, 120000, 95000)
	stats.RecordRequest(3, 85000, 85000)

	if stats.TotalRequests != 2 {
		t.Errorf("expected 2 requests, got %d", stats.TotalRequests)
	}
	if stats.TotalStubs != 8 {
		t.Errorf("expected 8 stubs, got %d", stats.TotalStubs)
	}
}

func TestHealthEndpoint_TracksTokenSavings(t *testing.T) {
	stats := &ProxyStats{startTime: time.Now()}
	stats.RecordRequest(5, 120000, 80000)

	if stats.TokensSaved != 40000 {
		t.Errorf("expected 40000 tokens saved, got %d", stats.TokensSaved)
	}
}
