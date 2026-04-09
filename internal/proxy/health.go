package proxy

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync/atomic"
	"time"
)

// ProxyStats tracks aggregate proxy metrics.
type ProxyStats struct {
	startTime     time.Time
	TotalRequests int64
	TotalStubs    int64
	TokensSaved   int64
}

// RecordRequest records metrics for a completed request.
func (s *ProxyStats) RecordRequest(stubCount int, tokensBefore, tokensAfter int) {
	if s == nil {
		return
	}
	atomic.AddInt64(&s.TotalRequests, 1)
	atomic.AddInt64(&s.TotalStubs, int64(stubCount))
	saved := tokensBefore - tokensAfter
	if saved > 0 {
		atomic.AddInt64(&s.TokensSaved, int64(saved))
	}
}

// handleHealth serves the /health endpoint with JSON status.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	uptime := time.Since(s.stats.startTime).Round(time.Second)

	// C1 fix: read annotations count under lock
	s.mu.RLock()
	annCount := len(s.annotations)
	s.mu.RUnlock()

	resp := map[string]any{
		"status":       "ok",
		"uptime":       fmt.Sprintf("%s", uptime),
		"requests":     atomic.LoadInt64(&s.stats.TotalRequests),
		"stubs":        atomic.LoadInt64(&s.stats.TotalStubs),
		"tokens_saved": atomic.LoadInt64(&s.stats.TokensSaved),
		"annotations":  annCount,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}
