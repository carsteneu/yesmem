package httpapi

import (
	"bytes"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestIngestRequestParsing(t *testing.T) {
	var req IngestRequest
	input := `{"session_id":"s1","project":"/tmp/proj","source":"user","messages":[{"role":"user","content":"hi"}]}`
	if err := json.Unmarshal([]byte(input), &req); err != nil {
		t.Fatal(err)
	}
	if req.SessionID != "s1" {
		t.Errorf("session_id = %q, want %q", req.SessionID, "s1")
	}
	if req.Project != "/tmp/proj" {
		t.Errorf("project = %q, want %q", req.Project, "/tmp/proj")
	}
	if req.Source != "user" {
		t.Errorf("source = %q, want %q", req.Source, "user")
	}
	if len(req.Messages) != 1 {
		t.Errorf("messages count = %d, want 1", len(req.Messages))
	}
}

func TestIngestRequestParsing_Subagent(t *testing.T) {
	var req IngestRequest
	input := `{"session_id":"s2","project":"/home/user/proj","source":"subagent","messages":[]}`
	if err := json.Unmarshal([]byte(input), &req); err != nil {
		t.Fatal(err)
	}
	if req.Source != "subagent" {
		t.Errorf("source = %q, want %q", req.Source, "subagent")
	}
	if len(req.Messages) != 0 {
		t.Errorf("messages count = %d, want 0", len(req.Messages))
	}
}

func TestIngestHistoryRequestParsing(t *testing.T) {
	var req IngestHistoryRequest
	input := `{"sessions_dir":"/home/user/.claude/sessions"}`
	if err := json.Unmarshal([]byte(input), &req); err != nil {
		t.Fatal(err)
	}
	if req.SessionsDir != "/home/user/.claude/sessions" {
		t.Errorf("sessions_dir = %q, want %q", req.SessionsDir, "/home/user/.claude/sessions")
	}
}

func TestHandleIngest_InvalidJSON(t *testing.T) {
	logger := log.New(os.Stderr, "", 0)
	srv := &Server{handler: &ingestCaptureHandler{}, logger: logger}
	body := bytes.NewBufferString(`not-json`)
	req := httptest.NewRequest(http.MethodPost, "/api/ingest", body)
	w := httptest.NewRecorder()
	srv.handleIngest(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestHandleIngest_EmptyMessages(t *testing.T) {
	logger := log.New(os.Stderr, "", 0)
	srv := &Server{handler: &ingestCaptureHandler{}, logger: logger}
	body := bytes.NewBufferString(`{"session_id":"s1","messages":[]}`)
	req := httptest.NewRequest(http.MethodPost, "/api/ingest", body)
	w := httptest.NewRecorder()
	srv.handleIngest(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestHandleIngest_Success(t *testing.T) {
	captured := &ingestCaptureHandler{}
	logger := log.New(os.Stderr, "", 0)
	srv := &Server{handler: captured, logger: logger}

	body := bytes.NewBufferString(`{"session_id":"s1","project":"/tmp","source":"user","messages":[{"role":"user","content":"hello"}]}`)
	req := httptest.NewRequest(http.MethodPost, "/api/ingest", body)
	w := httptest.NewRecorder()
	srv.handleIngest(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
	}
	if captured.lastMethod != "ingest_messages" {
		t.Errorf("method = %q, want %q", captured.lastMethod, "ingest_messages")
	}
	if captured.lastParams["session_id"] != "s1" {
		t.Errorf("session_id param = %v", captured.lastParams["session_id"])
	}
	if captured.lastParams["source"] != "user" {
		t.Errorf("source param = %v", captured.lastParams["source"])
	}
}

func TestHandleIngest_RPCErrorStillOK(t *testing.T) {
	// If RPC returns unknown method error, endpoint still responds 200
	// (daemon may not have ingest_messages yet)
	errH := &ingestErrorHandler{errMsg: "unknown method: ingest_messages"}
	logger := log.New(os.Stderr, "", 0)
	srv := &Server{handler: errH, logger: logger}

	body := bytes.NewBufferString(`{"session_id":"s1","messages":[{"role":"user","content":"hi"}]}`)
	req := httptest.NewRequest(http.MethodPost, "/api/ingest", body)
	w := httptest.NewRecorder()
	srv.handleIngest(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d (RPC error should not cause HTTP error)", w.Code, http.StatusOK)
	}
}

func TestHandleIngestHistory_InvalidJSON(t *testing.T) {
	logger := log.New(os.Stderr, "", 0)
	srv := &Server{handler: &ingestCaptureHandler{}, logger: logger}
	body := bytes.NewBufferString(`{bad`)
	req := httptest.NewRequest(http.MethodPost, "/api/ingest-history", body)
	w := httptest.NewRecorder()
	srv.handleIngestHistory(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestHandleIngestHistory_MissingDir(t *testing.T) {
	logger := log.New(os.Stderr, "", 0)
	srv := &Server{handler: &ingestCaptureHandler{}, logger: logger}
	body := bytes.NewBufferString(`{"sessions_dir":""}`)
	req := httptest.NewRequest(http.MethodPost, "/api/ingest-history", body)
	w := httptest.NewRecorder()
	srv.handleIngestHistory(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestHandleIngestHistory_ReturnsImmediately(t *testing.T) {
	captured := &ingestCaptureHandler{}
	logger := log.New(os.Stderr, "", 0)
	srv := &Server{handler: captured, logger: logger}

	// Use /tmp as dir — exists, but no .jsonl files; goroutine completes fast
	body := bytes.NewBufferString(`{"sessions_dir":"/tmp"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/ingest-history", body)
	w := httptest.NewRecorder()
	srv.handleIngestHistory(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
	}

	var resp map[string]string
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp["status"] != "ingesting" {
		t.Errorf("status = %q, want %q", resp["status"], "ingesting")
	}
}

func TestIngestHistoryDir_ParsesJSONL(t *testing.T) {
	// Create a temp dir with a minimal OpenClaw-style JSONL file
	dir := t.TempDir()
	jsonlContent := `{"type":"message","timestamp":"2026-01-01T00:00:00Z","message":{"role":"user","content":"hello"}}` + "\n"
	path := filepath.Join(dir, "session-abc123.jsonl")
	if err := os.WriteFile(path, []byte(jsonlContent), 0644); err != nil {
		t.Fatal(err)
	}

	captured := &ingestCaptureHandler{}
	logger := log.New(os.Stderr, "", 0)
	srv := &Server{handler: captured, logger: logger}

	// Run synchronously to verify behavior
	srv.ingestHistoryDir(dir)

	if captured.callCount == 0 {
		t.Error("expected at least one RPC call for the .jsonl file")
	}
	if captured.lastMethod != "ingest_messages" {
		t.Errorf("method = %q, want %q", captured.lastMethod, "ingest_messages")
	}
	if captured.lastParams["session_id"] != "session-abc123" {
		t.Errorf("session_id = %v, want %q", captured.lastParams["session_id"], "session-abc123")
	}
	if captured.lastParams["source"] != "openclaw_history" {
		t.Errorf("source = %v, want %q", captured.lastParams["source"], "openclaw_history")
	}
}

func TestIngestHistoryDir_SkipsNonJSONL(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("ignore me"), 0644); err != nil {
		t.Fatal(err)
	}

	captured := &ingestCaptureHandler{}
	logger := log.New(os.Stderr, "", 0)
	srv := &Server{handler: captured, logger: logger}
	srv.ingestHistoryDir(dir)

	if captured.callCount != 0 {
		t.Errorf("expected 0 RPC calls for non-.jsonl files, got %d", captured.callCount)
	}
}

func TestIngestHistoryDir_SkipsDirectories(t *testing.T) {
	dir := t.TempDir()
	// Edge case: a directory named with .jsonl suffix
	subdir := filepath.Join(dir, "fake.jsonl")
	if err := os.Mkdir(subdir, 0755); err != nil {
		t.Fatal(err)
	}

	captured := &ingestCaptureHandler{}
	logger := log.New(os.Stderr, "", 0)
	srv := &Server{handler: captured, logger: logger}
	srv.ingestHistoryDir(dir)

	if captured.callCount != 0 {
		t.Errorf("expected 0 RPC calls for directory entries, got %d", captured.callCount)
	}
}

// --- test helpers (ingest_test.go-local) ---

// ingestCaptureHandler records the last RPC call made.
type ingestCaptureHandler struct {
	lastMethod string
	lastParams map[string]any
	callCount  int
}

func (h *ingestCaptureHandler) Handle(req RPCRequest) RPCResponse {
	h.lastMethod = req.Method
	h.lastParams = req.Params
	h.callCount++
	return RPCResponse{Result: json.RawMessage(`{}`)}
}

// ingestErrorHandler always returns an RPC error.
type ingestErrorHandler struct {
	errMsg string
}

func (h *ingestErrorHandler) Handle(_ RPCRequest) RPCResponse {
	return RPCResponse{Error: h.errMsg}
}
