package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLocalhostOnly(t *testing.T) {
	handler := LocalhostOnly(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))

	// Localhost should pass
	req := httptest.NewRequest("GET", "/health", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Errorf("localhost request: got %d, want 200", w.Code)
	}

	// Non-localhost should be rejected
	req = httptest.NewRequest("GET", "/health", nil)
	req.RemoteAddr = "192.168.1.50:12345"
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != 403 {
		t.Errorf("remote request: got %d, want 403", w.Code)
	}
}

func TestBearerAuth(t *testing.T) {
	handler := BearerAuth("test-token-123", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))

	// Valid token
	req := httptest.NewRequest("GET", "/api/search", nil)
	req.Header.Set("Authorization", "Bearer test-token-123")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Errorf("valid token: got %d, want 200", w.Code)
	}

	// Missing token
	req = httptest.NewRequest("GET", "/api/search", nil)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != 401 {
		t.Errorf("missing token: got %d, want 401", w.Code)
	}

	// Wrong token
	req = httptest.NewRequest("GET", "/api/search", nil)
	req.Header.Set("Authorization", "Bearer wrong-token")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != 401 {
		t.Errorf("wrong token: got %d, want 401", w.Code)
	}
}

func TestBearerAuthFunc(t *testing.T) {
	handler := BearerAuthFunc("mytoken", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	})

	req := httptest.NewRequest("GET", "/api/test", nil)
	req.Header.Set("Authorization", "Bearer mytoken")
	w := httptest.NewRecorder()
	handler(w, req)
	if w.Code != 200 {
		t.Errorf("valid token: got %d, want 200", w.Code)
	}
}
