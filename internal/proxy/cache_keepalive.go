package proxy

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"sync"
	"time"
)

// CacheKeepaliveConfig holds configuration for the keepalive timer.
type CacheKeepaliveConfig struct {
	Target           string            // API endpoint URL
	Mode             string            // "auto", "5m", "1h"
	Pings5m          int               // max pings per phase when TTL=5min
	Pings1h          int               // max pings per phase when TTL=1h
	Detector         *CacheTTLDetector // for auto mode
	IntervalOverride time.Duration     // testing only: override computed interval
	Logger           *log.Logger
	OnPing           func(threadID string, cacheRead, cacheWrite int) // optional callback
}

// threadState stores per-thread request body, timer, and ping state.
type threadState struct {
	body           []byte
	apiKey         string
	lastUsed       time.Time
	timer          *time.Timer
	pingsRemaining int
	generation     uint64
}

// CacheKeepalive sends bounded ping requests to keep the prompt cache warm.
// Each thread gets its own independent timer so active sessions don't starve quiet ones.
type CacheKeepalive struct {
	cfg     CacheKeepaliveConfig
	mu      sync.Mutex
	threads map[string]*threadState // threadID → state
	client  *http.Client
}

const threadEvictionAge = 2 * time.Hour

// NewCacheKeepalive creates a new keepalive timer (initially idle).
func NewCacheKeepalive(cfg CacheKeepaliveConfig) *CacheKeepalive {
	return &CacheKeepalive{
		cfg:     cfg,
		threads: make(map[string]*threadState),
		client:  &http.Client{Timeout: 20 * time.Second},
	}
}

// effectiveInterval returns the ping interval based on mode and detected TTL.
func (ka *CacheKeepalive) effectiveInterval() time.Duration {
	if ka.cfg.IntervalOverride > 0 {
		return ka.cfg.IntervalOverride
	}
	switch ka.cfg.Mode {
	case "5m":
		return time.Duration(float64(300)*0.9) * time.Second // 270s
	case "1h":
		return time.Duration(float64(3600)*0.9) * time.Second // 3240s
	default: // "auto"
		if ka.cfg.Detector != nil {
			sup := ka.cfg.Detector.Is1hSupported()
			if sup != nil && *sup {
				return time.Duration(float64(3600)*0.9) * time.Second
			}
		}
		return time.Duration(float64(300)*0.9) * time.Second
	}
}

// effectivePings returns the ping count based on mode and detected TTL.
func (ka *CacheKeepalive) effectivePings() int {
	switch ka.cfg.Mode {
	case "5m":
		return ka.cfg.Pings5m
	case "1h":
		return ka.cfg.Pings1h
	default: // "auto"
		if ka.cfg.Detector != nil {
			sup := ka.cfg.Detector.Is1hSupported()
			if sup == nil {
				return 0 // detection pending: don't interfere
			}
			if *sup {
				return ka.cfg.Pings1h
			}
		}
		return ka.cfg.Pings5m
	}
}

// Reset stores the request body for a thread and starts/resets only that thread's timer.
func (ka *CacheKeepalive) Reset(threadID string, requestBody []byte, apiKey string) {
	ka.mu.Lock()
	defer ka.mu.Unlock()

	ts := ka.threads[threadID]
	if ts != nil && ts.timer != nil {
		ts.timer.Stop()
	}

	if ts == nil {
		ts = &threadState{}
		ka.threads[threadID] = ts
	}

	ts.body = make([]byte, len(requestBody))
	copy(ts.body, requestBody)
	ts.apiKey = apiKey
	ts.lastUsed = time.Now()

	ka.evictStaleLocked()

	pings := ka.effectivePings()
	if pings <= 0 {
		return
	}
	ts.pingsRemaining = pings
	ts.generation++
	gen := ts.generation
	ts.timer = time.AfterFunc(ka.effectiveInterval(), func() {
		ka.sendPingForThread(threadID, gen)
	})
}

// Retrigger restarts keepalive timers for all threads with recalculated interval and ping count.
func (ka *CacheKeepalive) Retrigger() {
	ka.mu.Lock()
	defer ka.mu.Unlock()
	if len(ka.threads) == 0 {
		return
	}
	pings := ka.effectivePings()
	if pings <= 0 {
		return
	}
	for tid, ts := range ka.threads {
		if ts.timer != nil {
			ts.timer.Stop()
		}
		ts.pingsRemaining = pings
		ts.generation++
		gen := ts.generation
		capturedTID := tid
		ts.timer = time.AfterFunc(ka.effectiveInterval(), func() {
			ka.sendPingForThread(capturedTID, gen)
		})
	}
}

// Stop cancels all pending timers.
func (ka *CacheKeepalive) Stop() {
	ka.mu.Lock()
	defer ka.mu.Unlock()
	for _, ts := range ka.threads {
		if ts.timer != nil {
			ts.timer.Stop()
		}
	}
}

// KeepaliveStatus holds current keepalive state for status display.
type KeepaliveStatus struct {
	Mode           string // "auto", "5m", "1h"
	IntervalS      int    // current ping interval in seconds
	TotalPings     int    // configured total pings (constant)
	PingsRemaining int    // max pings remaining across any single thread
	ActiveThreads  int    // number of tracked threads
}

// Status returns the current keepalive state for status display.
func (ka *CacheKeepalive) Status() KeepaliveStatus {
	ka.mu.Lock()
	defer ka.mu.Unlock()
	maxRemaining := 0
	for _, ts := range ka.threads {
		if ts.pingsRemaining > maxRemaining {
			maxRemaining = ts.pingsRemaining
		}
	}
	return KeepaliveStatus{
		Mode:           ka.cfg.Mode,
		IntervalS:      int(ka.effectiveInterval().Seconds()),
		TotalPings:     ka.effectivePings(),
		PingsRemaining: maxRemaining,
		ActiveThreads:  len(ka.threads),
	}
}

func (ka *CacheKeepalive) sendPingForThread(threadID string, expectedGen uint64) {
	ka.mu.Lock()
	ts := ka.threads[threadID]
	if ts == nil || ts.generation != expectedGen || ts.pingsRemaining <= 0 {
		ka.mu.Unlock()
		return
	}
	ts.pingsRemaining--
	remaining := ts.pingsRemaining
	body := make([]byte, len(ts.body))
	copy(body, ts.body)
	apiKey := ts.apiKey
	ka.mu.Unlock()

	pingBody := buildPingBody(body)
	cacheRead, cacheWrite, outputTokens := ka.doHTTPPing(pingBody, apiKey)

	if ka.cfg.OnPing != nil {
		ka.cfg.OnPing(threadID, cacheRead, cacheWrite)
	}

	if ka.cfg.Logger != nil {
		ka.cfg.Logger.Printf("[keepalive] ping tid=%s cache_read=%d cache_write=%d out=%d remaining=%d mode=%s",
			threadID, cacheRead, cacheWrite, outputTokens, remaining, ka.cfg.Mode)
	}

	ka.mu.Lock()
	if ts.generation == expectedGen && ts.pingsRemaining > 0 {
		gen := ts.generation
		ts.timer = time.AfterFunc(ka.effectiveInterval(), func() {
			ka.sendPingForThread(threadID, gen)
		})
	}
	ka.mu.Unlock()
}

func (ka *CacheKeepalive) evictStaleLocked() {
	cutoff := time.Now().Add(-threadEvictionAge)
	for tid, ts := range ka.threads {
		if ts.lastUsed.Before(cutoff) {
			if ts.timer != nil {
				ts.timer.Stop()
			}
			delete(ka.threads, tid)
		}
	}
}

func (ka *CacheKeepalive) doHTTPPing(body []byte, apiKey string) (cacheRead, cacheWrite, outputTokens int) {
	req, err := http.NewRequest("POST", ka.cfg.Target+"/v1/messages", nil)
	if err != nil {
		return 0, 0, 0
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Body = io.NopCloser(bytesReader(body))
	req.ContentLength = int64(len(body))

	resp, err := ka.client.Do(req)
	if err != nil {
		return 0, 0, 0
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK && ka.cfg.Logger != nil {
		ka.cfg.Logger.Printf("[keepalive] ping error: HTTP %d body=%s", resp.StatusCode, truncateBytes(respBody, 200))
	}

	var usage struct {
		Usage struct {
			CacheReadInputTokens     int `json:"cache_read_input_tokens"`
			CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
			OutputTokens             int `json:"output_tokens"`
		} `json:"usage"`
	}
	json.Unmarshal(respBody, &usage)
	return usage.Usage.CacheReadInputTokens, usage.Usage.CacheCreationInputTokens, usage.Usage.OutputTokens
}

// buildPingBody sets max_tokens=8000 and stream=false on the request body.
// Keeps thinking intact (it's part of the cache key / request hash).
// max_tokens=8000 is above the implicit budget for adaptive thinking
// (avoids "max_tokens must be greater than thinking.budget_tokens").
// Cost: each ping can produce up to 8000 thinking tokens if the model
// decides to think — in practice these are near-zero because the input
// asks nothing new.
func buildPingBody(original []byte) []byte {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(original, &m); err != nil {
		return original
	}
	m["max_tokens"] = json.RawMessage("8000")
	m["stream"] = json.RawMessage("false")
	delete(m, "metadata")
	delete(m, "context_management")
	out, err := json.Marshal(m)
	if err != nil {
		return original
	}
	return out
}

// bytesReader is a helper that wraps a byte slice as an io.Reader.
func bytesReader(b []byte) io.Reader {
	return &bytesReaderImpl{data: b}
}

type bytesReaderImpl struct {
	data []byte
	pos  int
}

func (r *bytesReaderImpl) Read(p []byte) (int, error) {
	if r.pos >= len(r.data) {
		return 0, io.EOF
	}
	n := copy(p, r.data[r.pos:])
	r.pos += n
	return n, nil
}

func truncateBytes(b []byte, max int) string {
	if len(b) <= max {
		return string(b)
	}
	return string(b[:max]) + "…"
}
