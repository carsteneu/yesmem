package proxy

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// tokenizerDriftFactor compensates for qhenkart BPE tokenizer undercounting
// ~13% vs Claude 4's actual tokenizer, plus structural tokens not captured
// by countMessageTokens (message boundaries, role markers).
// Calibrated: countMessageTokens+overhead=210k vs CC's actual 249k → 1.19.
const tokenizerDriftFactor = 1.19

// CacheStatus is the JSON structure written for the statusline script.
type CacheStatus struct {
	TTL                   string  `json:"ttl"`
	TTLSeconds            int     `json:"ttl_seconds"`
	RemainingS            int     `json:"remaining_s"`
	CacheState            string  `json:"cache_state"`
	CostPerReq            float64 `json:"cost_per_req"`
	LastRequestTS         int64   `json:"last_request_ts"`
	TotalTokens           int     `json:"total_tokens"`
	RawTokenEstimate      int     `json:"raw_token_estimate,omitempty"`
	CacheReadTokens       int     `json:"cache_read_tokens"`
	CacheWriteTokens      int     `json:"cache_write_tokens"`
	ThreadID              string  `json:"thread_id,omitempty"`
	TokenThreshold        int     `json:"token_threshold,omitempty"`
	TokenMinimumThreshold int     `json:"token_minimum_threshold,omitempty"`
	DetectedTTL           string  `json:"detected_ttl"`              // "unknown", "5min", "1h"
	KeepaliveMode         string  `json:"keepalive_mode"`            // "auto", "5m", "1h"
	PingIntervalS         int     `json:"ping_interval_s,omitempty"` // seconds between pings
	PingsRemaining        int     `json:"pings_remaining"`           // pings left in current phase
	ActiveThreads         int     `json:"active_threads,omitempty"`  // tracked keepalive threads
}

// CacheStatusWriter writes cache status to disk every second.
type CacheStatusWriter struct {
	mu                    sync.Mutex
	dataDir               string
	ttlConfig             string
	tokenMinimumThreshold int
	detector              *CacheTTLDetector
	keepalive             *CacheKeepalive
	threads               map[string]*statusThreadState
}

// statusThreadState holds per-session cache status data.
type statusThreadState struct {
	lastRequest      time.Time
	totalTokens      int
	rawTokenEstimate int
	cacheRead        int
	cacheWrite       int
	tokenThreshold   int
}

// NewCacheStatusWriter creates and starts a background writer.
func NewCacheStatusWriter(dataDir string, ttlConfig string, defaultThreshold int, defaultMinThreshold int) *CacheStatusWriter {
	w := &CacheStatusWriter{
		dataDir:               dataDir,
		ttlConfig:             ttlConfig,
		tokenMinimumThreshold: defaultMinThreshold,
		threads:               make(map[string]*statusThreadState),
	}
	go w.tickLoop()
	return w
}

// SetDetector sets the TTL detector for dynamic TTL display.
func (w *CacheStatusWriter) SetDetector(d *CacheTTLDetector) {
	w.mu.Lock()
	w.detector = d
	w.mu.Unlock()
}

// SetKeepalive sets the keepalive for ping status display.
func (w *CacheStatusWriter) SetKeepalive(ka *CacheKeepalive) {
	w.mu.Lock()
	w.keepalive = ka
	w.mu.Unlock()
}

// Update records new values from an API response.
func (w *CacheStatusWriter) Update(lastRequest time.Time, totalTokens, cacheRead, cacheWrite int, threadID string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	ts := w.threads[threadID]
	if ts == nil {
		ts = &statusThreadState{}
		w.threads[threadID] = ts
	}
	ts.lastRequest = lastRequest
	ts.totalTokens = totalTokens
	ts.cacheRead = cacheRead
	ts.cacheWrite = cacheWrite
}

// UpdateThresholdForThread sets the token threshold for a specific thread.
func (w *CacheStatusWriter) UpdateThresholdForThread(threadID string, threshold int) {
	w.mu.Lock()
	if ts := w.threads[threadID]; ts != nil {
		ts.tokenThreshold = threshold
	}
	w.mu.Unlock()
}

// UpdateRawForThread sets the raw token estimate for a specific thread.
func (w *CacheStatusWriter) UpdateRawForThread(threadID string, raw int) {
	w.mu.Lock()
	if ts := w.threads[threadID]; ts != nil {
		ts.rawTokenEstimate = raw
	}
	w.mu.Unlock()
}

func (w *CacheStatusWriter) tickLoop() {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		w.writeStatus()
	}
}

func (w *CacheStatusWriter) writeStatus() {
	w.mu.Lock()
	if len(w.threads) == 0 {
		w.mu.Unlock()
		return
	}
	ttlConfig := w.ttlConfig
	minThreshold := w.tokenMinimumThreshold
	det := w.detector
	ka := w.keepalive

	// Snapshot all thread states under lock
	type snapshot struct {
		id  string
		ts  statusThreadState
	}
	snaps := make([]snapshot, 0, len(w.threads))
	for id, ts := range w.threads {
		snaps = append(snaps, snapshot{id: id, ts: *ts})
	}
	w.mu.Unlock()

	// Resolve TTL (shared across threads)
	ttlSec := 300
	detectedTTL := "unknown"
	if ttlConfig == "" || ttlConfig == "ephemeral" {
		ttlSec = 300
		detectedTTL = "5min"
	} else if ttlConfig == "1h" {
		if det != nil {
			ttlSec = det.EffectiveTTLSeconds()
			sup := det.Is1hSupported()
			if sup != nil {
				if *sup {
					detectedTTL = "1h"
				} else {
					detectedTTL = "5min"
				}
			}
		} else {
			ttlSec = 3600
			detectedTTL = "1h"
		}
	}

	// Keepalive status (shared across threads)
	var kaMode string
	var pingIntervalS, pingsRemaining, totalPings, activeThreads int
	if ka != nil {
		ks := ka.Status()
		kaMode = ks.Mode
		pingIntervalS = ks.IntervalS
		totalPings = ks.TotalPings
		pingsRemaining = ks.PingsRemaining
		activeThreads = ks.ActiveThreads
	}

	dir := filepath.Join(w.dataDir, "cache-status")
	os.MkdirAll(dir, 0755)

	for _, snap := range snaps {
		ts := snap.ts

		remaining := ttlSec - int(time.Since(ts.lastRequest).Seconds())
		if remaining < 0 {
			remaining = 0
		}

		state := "warm"
		if remaining == 0 {
			if totalPings > 0 && pingIntervalS > 0 {
				elapsed := int(time.Since(ts.lastRequest).Seconds())
				totalCoverage := totalPings*pingIntervalS + ttlSec
				remaining = totalCoverage - elapsed
				if remaining < 0 {
					remaining = 0
				}
			}
			if remaining > 0 {
				state = "warm"
			} else {
				state = "cold"
			}
		} else if remaining < 60 {
			state = "cooling"
		}

		tokensK := float64(ts.totalTokens) / 1000.0
		var costPerReq float64
		switch state {
		case "warm", "cooling":
			costPerReq = (tokensK-5)*0.0005 + 5*0.005
		case "cold":
			writeRate := 0.00625
			if ttlConfig == "1h" {
				writeRate = 0.01
			}
			costPerReq = tokensK * writeRate
		}

		threshold := ts.tokenThreshold

		status := CacheStatus{
			TTL:                   ttlConfig,
			TTLSeconds:            ttlSec,
			RemainingS:            remaining,
			CacheState:            state,
			CostPerReq:            costPerReq,
			LastRequestTS:         ts.lastRequest.Unix(),
			TotalTokens:           ts.totalTokens,
			RawTokenEstimate:      ts.rawTokenEstimate,
			CacheReadTokens:       ts.cacheRead,
			CacheWriteTokens:      ts.cacheWrite,
			ThreadID:              snap.id,
			TokenThreshold:        threshold,
			TokenMinimumThreshold: minThreshold,
			DetectedTTL:           detectedTTL,
			KeepaliveMode:         kaMode,
			PingIntervalS:         pingIntervalS,
			PingsRemaining:        pingsRemaining,
			ActiveThreads:         activeThreads,
		}

		data, err := json.Marshal(status)
		if err != nil {
			continue
		}

		tmp := filepath.Join(dir, fmt.Sprintf("status-%s.json.tmp", snap.id))
		target := filepath.Join(dir, fmt.Sprintf("status-%s.json", snap.id))
		if err := os.WriteFile(tmp, data, 0644); err != nil {
			continue
		}
		os.Rename(tmp, target)
	}
}

// CacheStatusPath returns the path to the per-thread cache status JSON file.
func CacheStatusPath(dataDir string, threadID string) string {
	return filepath.Join(dataDir, "cache-status", fmt.Sprintf("status-%s.json", threadID))
}

// FormatCacheStatus returns a short status string for the terminal.
func FormatCacheStatus(s CacheStatus) string {
	switch s.CacheState {
	case "warm":
		min := s.RemainingS / 60
		sec := s.RemainingS % 60
		return fmt.Sprintf("Cache: %d:%02d %s $%.2f/req", min, sec, s.TTL, s.CostPerReq)
	case "cooling":
		return fmt.Sprintf("Cache: %ds! %s $%.2f/req", s.RemainingS, s.TTL, s.CostPerReq)
	default:
		return fmt.Sprintf("Cache: COLD $%.2f/req", s.CostPerReq)
	}
}

// StatusLines holds the pre-formatted lines for the terminal display.
type StatusLines struct {
	CacheLine      string // "CacheLifeTime (1h) until 13:20 | Keepalive until 17:20 with 4 ping(s) every 54min"
	CollapsingLine string // "Lossless collapsing at 200k to 80k Token | actual usage: 98k / 200k Token"
	ExpiryCostLine string // "after Cache expiry $1.84 for refresh"
}

// FormatStatusLines builds structured terminal display lines from cache status.
func FormatStatusLines(s CacheStatus) StatusLines {
	var lines StatusLines

	ttlLabel := ttlDisplayLabel(s.DetectedTTL)
	tokK := s.TotalTokens / 1000
	coldCost := calcColdCost(s)

	if s.CacheState == "cold" {
		tokDisplay := formatTokenDisplay(tokK, s.TokenThreshold)
		lines.CacheLine = fmt.Sprintf("COLD (%s) | %s Token", ttlLabel, tokDisplay)
		lines.ExpiryCostLine = fmt.Sprintf("$%.2f for refresh", coldCost)
	} else {
		cacheExpiry := time.Unix(s.LastRequestTS+int64(s.TTLSeconds), 0)
		cacheExpiryStr := cacheExpiry.Format("15:04")

		cachePart := fmt.Sprintf("CacheLifeTime (%s) until %s", ttlLabel, cacheExpiryStr)

		if s.PingsRemaining > 0 && s.PingIntervalS > 0 {
			keepaliveEnd := cacheExpiry.Add(time.Duration(s.PingsRemaining) * time.Duration(s.PingIntervalS) * time.Second)
			keepaliveEndStr := keepaliveEnd.Format("15:04")
			intervalMin := s.PingIntervalS / 60
			lines.CacheLine = fmt.Sprintf("%s | Keepalive until %s with %d ping(s) every %dmin",
				cachePart, keepaliveEndStr, s.PingsRemaining, intervalMin)
		} else {
			lines.CacheLine = cachePart
		}

		lines.ExpiryCostLine = fmt.Sprintf("after Cache expiry $%.2f for refresh", coldCost)
	}

	if s.TokenThreshold > 0 && s.TokenMinimumThreshold > 0 {
		if s.RawTokenEstimate > 0 && s.RawTokenEstimate > s.TotalTokens {
			rawK := int(float64(s.RawTokenEstimate) * tokenizerDriftFactor) / 1000
			saved := 100 - tokK*100/rawK
			lines.CollapsingLine = fmt.Sprintf("Lossless collapsing %dk → %dk (%d%% saved) | threshold: %dk", rawK, tokK, saved, s.TokenThreshold/1000)
		} else {
			colPart := fmt.Sprintf("Lossless collapsing at %dk to %dk Token", s.TokenThreshold/1000, s.TokenMinimumThreshold/1000)
			usagePart := fmt.Sprintf("actual usage: %dk / %dk Token", tokK, s.TokenThreshold/1000)
			lines.CollapsingLine = fmt.Sprintf("%s | %s", colPart, usagePart)
		}
	}

	return lines
}

func ttlDisplayLabel(detected string) string {
	switch detected {
	case "1h":
		return "1h"
	case "5min":
		return "5min"
	default:
		return "detecting…"
	}
}

func formatTokenDisplay(tokK int, threshold int) string {
	if threshold > 0 {
		return fmt.Sprintf("%dk/%dk", tokK, threshold/1000)
	}
	return fmt.Sprintf("%dk", tokK)
}

func calcColdCost(s CacheStatus) float64 {
	tokK := float64(s.TotalTokens) / 1000.0
	// Opus 4.6: 5min cache write $6.25/MTok, 1h cache write $10.00/MTok
	writeRate := 0.00625
	if s.DetectedTTL == "1h" || s.TTL == "1h" {
		writeRate = 0.01
	}
	return tokK * writeRate
}
