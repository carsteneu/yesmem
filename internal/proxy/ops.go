package proxy

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
)

// === Task #10: Localhost-only Binding ===

// sanitizeListenAddr ensures the proxy only listens on localhost.
// Prevents accidental exposure of API keys over the network.
func sanitizeListenAddr(addr string) string {
	if addr == "" {
		return "127.0.0.1:9099"
	}
	// Port-only like ":9099" → bind to localhost
	if addr[0] == ':' {
		return "127.0.0.1" + addr
	}
	// Explicit 0.0.0.0 → force localhost
	if strings.HasPrefix(addr, "0.0.0.0:") {
		return "127.0.0.1:" + addr[len("0.0.0.0:"):]
	}
	return addr
}

// === Task #9: Bypass-Switch ===

// isBypassed checks if the proxy should be bypassed for this request.
// Two mechanisms: ENV var (global) and request header (per-request).
func isBypassed(header http.Header) bool {
	if os.Getenv("YESMEM_PROXY_BYPASS") != "" {
		return true
	}
	if header != nil && header.Get("X-Yesmem-Bypass") != "" {
		return true
	}
	return false
}

// === Task #6: Hysterese ===

// shouldStub determines if stubbing should be active, using hysteresis
// to prevent flapping around the threshold.
// Activate at TokenThreshold, deactivate at TokenMinimumThreshold.
func (s *Server) shouldStub(totalTokens int, model string) bool {
	threshold := s.effectiveTokenThreshold(model)
	lowWatermark := s.cfg.TokenMinimumThreshold
	if lowWatermark == 0 {
		lowWatermark = threshold * 4 / 5 // fallback: 80%
	}

	active := s.stubActive.Load()
	if !active && totalTokens > threshold {
		s.stubActive.Store(true)
		return true
	}
	if active && totalTokens < lowWatermark {
		s.stubActive.Store(false)
		return false
	}
	return active
}

// === Task #7: Idempotenz (Side-Effect Skip) ===

// requestFingerprint creates a short hash from messages length + last message content.
func requestFingerprint(messages []any) string {
	h := sha256.New()
	fmt.Fprintf(h, "%d:", len(messages))
	if len(messages) > 0 {
		last, _ := json.Marshal(messages[len(messages)-1])
		if len(last) > 200 {
			last = last[:200]
		}
		h.Write(last)
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// isRetry checks if we've already processed a request with this fingerprint.
func (s *Server) isRetry(fp string) bool {
	s.retryMu.RLock()
	defer s.retryMu.RUnlock()
	return s.lastFingerprint == fp
}

// markRequest records the fingerprint of the current request.
func (s *Server) markRequest(fp string) {
	s.retryMu.Lock()
	defer s.retryMu.Unlock()
	s.lastFingerprint = fp
}

