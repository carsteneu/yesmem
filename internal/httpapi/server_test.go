package httpapi

import (
	"encoding/json"
	"log"
	"net/http/httptest"
	"os"
	"testing"
)

func TestHealthEndpoint(t *testing.T) {
	logger := log.New(os.Stderr, "", 0)
	srv := New(nil, "127.0.0.1:0", "test-token", logger)
	// Test health directly (bypasses middleware for unit test)
	req := httptest.NewRequest("GET", "/health", nil)
	w := httptest.NewRecorder()
	srv.handleHealth(w, req)
	if w.Code != 200 {
		t.Errorf("health: got %d, want 200", w.Code)
	}
	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["status"] != "ok" {
		t.Errorf("status = %v, want ok", resp["status"])
	}
}
