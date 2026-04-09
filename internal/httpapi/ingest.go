package httpapi

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/carsteneu/yesmem/internal/parser"
)

// IngestRequest is the input for POST /api/ingest.
type IngestRequest struct {
	SessionID string `json:"session_id"`
	Project   string `json:"project"`
	Source    string `json:"source"` // "user", "subagent"
	Messages  []any  `json:"messages"`
}

// IngestHistoryRequest is the input for POST /api/ingest-history.
type IngestHistoryRequest struct {
	SessionsDir string `json:"sessions_dir"`
}

// handleIngest accepts messages for the extraction pipeline.
func (s *Server) handleIngest(w http.ResponseWriter, r *http.Request) {
	var req IngestRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid JSON"}`, http.StatusBadRequest)
		return
	}
	if len(req.Messages) == 0 {
		http.Error(w, `{"error":"messages must not be empty"}`, http.StatusBadRequest)
		return
	}

	resp := s.handler.Handle(RPCRequest{
		Method: "ingest_messages",
		Params: map[string]any{
			"session_id": req.SessionID,
			"project":    req.Project,
			"source":     req.Source,
			"messages":   req.Messages,
		},
	})

	w.Header().Set("Content-Type", "application/json")
	if resp.Error != "" {
		// Log RPC error but return 200 — daemon may not have ingest_messages yet.
		s.logger.Printf("ingest_messages RPC error (non-fatal): %s", resp.Error)
		json.NewEncoder(w).Encode(map[string]any{"status": "accepted", "warning": resp.Error})
		return
	}
	json.NewEncoder(w).Encode(map[string]any{"status": "accepted"})
}

// handleIngestHistory bulk-imports OpenClaw JSONL files from a directory.
// Returns immediately with {"status":"ingesting"} and processes in background.
func (s *Server) handleIngestHistory(w http.ResponseWriter, r *http.Request) {
	var req IngestHistoryRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid JSON"}`, http.StatusBadRequest)
		return
	}
	if req.SessionsDir == "" {
		http.Error(w, `{"error":"sessions_dir must not be empty"}`, http.StatusBadRequest)
		return
	}

	go s.ingestHistoryDir(req.SessionsDir)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ingesting"})
}

// ingestHistoryDir walks dir, parses .jsonl files with parser.ParseOpenClawSession,
// and sends each session to daemon via ingest_messages RPC.
func (s *Server) ingestHistoryDir(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		s.logger.Printf("ingestHistoryDir: ReadDir %s: %v", dir, err)
		return
	}

	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		messages, meta, err := parser.ParseOpenClawSession(path)
		if err != nil {
			s.logger.Printf("ingestHistoryDir: parse %s: %v", path, err)
			continue
		}

		sessionID := strings.TrimSuffix(e.Name(), ".jsonl")

		// Convert []models.Message to []any for RPC transport.
		msgs := make([]any, len(messages))
		for i, m := range messages {
			msgs[i] = m
		}

		project := ""
		if meta != nil {
			project = meta.Project
		}

		s.handler.Handle(RPCRequest{
			Method: "ingest_messages",
			Params: map[string]any{
				"session_id": sessionID,
				"project":    project,
				"source":     "openclaw_history",
				"messages":   msgs,
			},
		})
	}
}
