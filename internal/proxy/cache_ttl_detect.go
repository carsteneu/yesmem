package proxy

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// CacheTTLDetector determines whether the API honors 1h cache TTL.
//
// Two signals:
//   - Negative: substantial write with ephemeral_1h=0 → immediate 5min
//   - Positive: after >5:01 gap, cache_read/(cache_read+cache_write) > 0.8
//     means the cache survived past 5min → confirmed 1h
type CacheTTLDetector struct {
	mu             sync.Mutex
	supported      *bool                // nil = unknown, true/false = detected
	testedAt       time.Time            // when supported was last determined
	threadRequests map[string]time.Time // per-thread last request time for gap measurement
	currentThread  string               // thread whose gap should be checked in RecordResponse
	dataDir        string               // for persisting state across restarts
}

func NewCacheTTLDetector() *CacheTTLDetector {
	return &CacheTTLDetector{
		threadRequests: make(map[string]time.Time),
	}
}

// NewCacheTTLDetectorWithPersist creates a detector that persists state to disk.
func NewCacheTTLDetectorWithPersist(dataDir string) *CacheTTLDetector {
	d := &CacheTTLDetector{
		threadRequests: make(map[string]time.Time),
		dataDir:        dataDir,
	}
	d.loadState()
	return d
}

// RecordRequest tracks per-thread request timing for gap measurement.
func (d *CacheTTLDetector) RecordRequest(threadID string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.currentThread = threadID
	// Don't update threadRequests yet — the gap is measured from the
	// PREVIOUS request time for THIS thread to now.
}

// RecordResponse checks response fields to determine 1h TTL support.
func (d *CacheTTLDetector) RecordResponse(cacheRead, cacheWrite, ephemeral1h int) {
	d.mu.Lock()
	defer d.mu.Unlock()

	total := cacheRead + cacheWrite
	if total == 0 {
		return
	}

	prevSupported := d.supported

	// Gap measurement: time since THIS thread's previous request
	now := time.Now()
	prevTime, hasPrev := d.threadRequests[d.currentThread]
	gap := now.Sub(prevTime)
	hasGap := hasPrev && gap > 301*time.Second

	// Update this thread's timestamp after measuring gap
	d.threadRequests[d.currentThread] = now

	// Positive: after >5:01 gap, cache still warm (>80% read) → 1h confirmed
	if hasGap && total > 0 && cacheRead*100/total > 80 {
		t := true
		d.supported = &t
		d.testedAt = time.Now()
		d.saveStateIfChanged(prevSupported)
		return
	}

	// Negative: substantial write (not delta) with ephemeral_1h=0 → 5min
	isSubstantialWrite := cacheWrite > 0 && (cacheRead == 0 || cacheWrite*5 >= cacheRead)
	if isSubstantialWrite && ephemeral1h == 0 {
		f := false
		d.supported = &f
		d.testedAt = time.Now()
		d.saveStateIfChanged(prevSupported)
	}
}

// Is1hSupported returns nil (unknown), true (confirmed), or false (denied).
func (d *CacheTTLDetector) Is1hSupported() *bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.supported
}

// EffectiveTTLSeconds returns 3600 if 1h is confirmed, 300 otherwise.
func (d *CacheTTLDetector) EffectiveTTLSeconds() int {
	sup := d.Is1hSupported()
	if sup != nil && *sup {
		return 3600
	}
	return 300
}

type ttlDetectState struct {
	Supported bool      `json:"supported"`
	TestedAt  time.Time `json:"tested_at"`
}

func (d *CacheTTLDetector) statePath() string {
	if d.dataDir == "" {
		return ""
	}
	return filepath.Join(d.dataDir, "cache_ttl_state.json")
}

// saveStateIfChanged writes state to disk when detection result changes. Caller holds mu.
func (d *CacheTTLDetector) saveStateIfChanged(prev *bool) {
	if d.supported == nil || d.statePath() == "" {
		return
	}
	if prev != nil && *prev == *d.supported {
		return
	}
	data, _ := json.Marshal(ttlDetectState{Supported: *d.supported, TestedAt: d.testedAt})
	os.WriteFile(d.statePath(), data, 0644)
}

// loadState reads persisted detection state. Only used at startup.
func (d *CacheTTLDetector) loadState() {
	p := d.statePath()
	if p == "" {
		return
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return
	}
	var st ttlDetectState
	if json.Unmarshal(data, &st) != nil {
		return
	}
	// Only use if tested recently (within 24h)
	if time.Since(st.TestedAt) > 24*time.Hour {
		return
	}
	d.supported = &st.Supported
	d.testedAt = st.TestedAt
}
