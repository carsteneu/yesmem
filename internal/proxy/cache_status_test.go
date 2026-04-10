package proxy

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

func TestFormatStatusLines_WarmWithKeepalive(t *testing.T) {
	now := time.Now()
	s := CacheStatus{
		TTLSeconds:            3600,
		RemainingS:            3500,
		CacheState:            "warm",
		LastRequestTS:         now.Unix(),
		TotalTokens:           98000,
		CacheReadTokens:       97000,
		CacheWriteTokens:      0,
		TokenThreshold:        200000,
		TokenMinimumThreshold: 80000,
		DetectedTTL:           "1h",
		KeepaliveMode:         "1h",
		PingIntervalS:         3240,
		PingsRemaining:        4,
	}
	lines := FormatStatusLines(s)

	// Line 1: CacheLifeTime + Keepalive
	if !strings.Contains(lines.CacheLine, "CacheLifeTime (1h)") {
		t.Errorf("CacheLine should contain 'CacheLifeTime (1h)', got: %s", lines.CacheLine)
	}
	if !strings.Contains(lines.CacheLine, "until") {
		t.Errorf("CacheLine should contain 'until', got: %s", lines.CacheLine)
	}
	if !strings.Contains(lines.CacheLine, "Keepalive until") {
		t.Errorf("CacheLine should contain 'Keepalive until', got: %s", lines.CacheLine)
	}
	if !strings.Contains(lines.CacheLine, "4 ping(s) every 54min") {
		t.Errorf("CacheLine should contain '4 ping(s) every 54min', got: %s", lines.CacheLine)
	}

	// Line 2: Collapsing + actual usage
	if !strings.Contains(lines.CollapsingLine, "Lossless collapsing at 200k to 80k Token") {
		t.Errorf("CollapsingLine should contain collapsing info, got: %s", lines.CollapsingLine)
	}
	if !strings.Contains(lines.CollapsingLine, "actual usage: 98k / 200k Token") {
		t.Errorf("CollapsingLine should contain actual usage, got: %s", lines.CollapsingLine)
	}

	// Line 4: expiry cost
	if !strings.Contains(lines.ExpiryCostLine, "after Cache expiry") {
		t.Errorf("ExpiryCostLine should contain 'after Cache expiry', got: %s", lines.ExpiryCostLine)
	}
	if !strings.Contains(lines.ExpiryCostLine, "for refresh") {
		t.Errorf("ExpiryCostLine should contain 'for refresh', got: %s", lines.ExpiryCostLine)
	}
}

func TestFormatStatusLines_WarmWithoutKeepalive(t *testing.T) {
	s := CacheStatus{
		TTLSeconds:            300,
		RemainingS:            250,
		CacheState:            "warm",
		LastRequestTS:         time.Now().Unix(),
		TotalTokens:           50000,
		TokenThreshold:        200000,
		TokenMinimumThreshold: 80000,
		DetectedTTL:           "5min",
		PingIntervalS:         0,
		PingsRemaining:        0,
	}
	lines := FormatStatusLines(s)

	if !strings.Contains(lines.CacheLine, "CacheLifeTime (5min)") {
		t.Errorf("CacheLine should contain '5min', got: %s", lines.CacheLine)
	}
	// No keepalive info when no pings
	if strings.Contains(lines.CacheLine, "Keepalive") {
		t.Errorf("CacheLine should NOT contain Keepalive when no pings, got: %s", lines.CacheLine)
	}
}

func TestFormatStatusLines_Cold(t *testing.T) {
	s := CacheStatus{
		TTLSeconds:            3600,
		RemainingS:            0,
		CacheState:            "cold",
		LastRequestTS:         time.Now().Add(-2 * time.Hour).Unix(),
		TotalTokens:           98000,
		TokenThreshold:        200000,
		TokenMinimumThreshold: 80000,
		DetectedTTL:           "1h",
		PingsRemaining:        0,
	}
	lines := FormatStatusLines(s)

	if !strings.Contains(lines.CacheLine, "COLD") {
		t.Errorf("CacheLine should contain 'COLD', got: %s", lines.CacheLine)
	}
	if !strings.Contains(lines.ExpiryCostLine, "for refresh") {
		t.Errorf("ExpiryCostLine should show refresh cost when cold, got: %s", lines.ExpiryCostLine)
	}
}

func TestFormatStatusLines_RawTokenEstimate(t *testing.T) {
	// When RawTokenEstimate is set, CollapsingLine shows savings
	s := CacheStatus{
		TTLSeconds:            300,
		RemainingS:            250,
		CacheState:            "warm",
		LastRequestTS:         time.Now().Unix(),
		TotalTokens:           98000,
		RawTokenEstimate:      340000,
		TokenThreshold:        200000,
		TokenMinimumThreshold: 80000,
		DetectedTTL:           "5min",
	}
	lines := FormatStatusLines(s)

	// Should show "Lossless collapsing 404k → 98k (76% saved)" (340k × 1.19 drift = 404k)
	if !strings.Contains(lines.CollapsingLine, "Lossless collapsing 404k") {
		t.Errorf("CollapsingLine should contain drift-adjusted raw token count, got: %s", lines.CollapsingLine)
	}
	if !strings.Contains(lines.CollapsingLine, "→ 98k") {
		t.Errorf("CollapsingLine should contain actual token count after arrow, got: %s", lines.CollapsingLine)
	}
	if !strings.Contains(lines.CollapsingLine, "76% saved") {
		t.Errorf("CollapsingLine should show savings percentage, got: %s", lines.CollapsingLine)
	}
	if !strings.Contains(lines.CollapsingLine, "threshold: 200k") {
		t.Errorf("CollapsingLine should contain threshold, got: %s", lines.CollapsingLine)
	}
}

func TestFormatStatusLines_RawTokenEstimateZero(t *testing.T) {
	// When RawTokenEstimate is 0 (not tracked yet), fall back to old format
	s := CacheStatus{
		TTLSeconds:            300,
		RemainingS:            250,
		CacheState:            "warm",
		LastRequestTS:         time.Now().Unix(),
		TotalTokens:           98000,
		RawTokenEstimate:      0,
		TokenThreshold:        200000,
		TokenMinimumThreshold: 80000,
		DetectedTTL:           "5min",
	}
	lines := FormatStatusLines(s)

	// Old format when no raw estimate available
	if !strings.Contains(lines.CollapsingLine, "Lossless collapsing at 200k to 80k Token") {
		t.Errorf("CollapsingLine should fall back to old format, got: %s", lines.CollapsingLine)
	}
}

func TestFormatStatusLines_RawSmallerThanActual(t *testing.T) {
	// When RawTokenEstimate <= TotalTokens (bad data), fall back to old format
	s := CacheStatus{
		TTLSeconds:            300,
		RemainingS:            250,
		CacheState:            "warm",
		LastRequestTS:         time.Now().Unix(),
		TotalTokens:           137000,
		RawTokenEstimate:      86000,
		TokenThreshold:        200000,
		TokenMinimumThreshold: 80000,
		DetectedTTL:           "5min",
	}
	lines := FormatStatusLines(s)

	// Must NOT show negative savings — fall back to old format
	if strings.Contains(lines.CollapsingLine, "saved") {
		t.Errorf("CollapsingLine should not show savings when raw < actual, got: %s", lines.CollapsingLine)
	}
	if !strings.Contains(lines.CollapsingLine, "Lossless collapsing at 200k to 80k Token") {
		t.Errorf("CollapsingLine should fall back to old format, got: %s", lines.CollapsingLine)
	}
}

func TestCacheStatusPath_PerThread(t *testing.T) {
	path := CacheStatusPath("/data", "abc-123")
	if !strings.HasSuffix(path, "cache-status/status-abc-123.json") {
		t.Errorf("CacheStatusPath should include threadID, got: %s", path)
	}
}

func TestCacheStatusWriter_PerThreadIsolation(t *testing.T) {
	dir := t.TempDir()
	w := &CacheStatusWriter{
		dataDir:               dir,
		ttlConfig:             "1h",
		tokenMinimumThreshold: 80000,
		threads:               make(map[string]*statusThreadState),
	}

	now := time.Now()

	// Update thread A
	w.Update(now, 100000, 95000, 5000, "thread-aaa")
	w.writeStatus()

	// Update thread B with different data — older lastRequest, fewer tokens
	w.Update(now.Add(-30*time.Minute), 50000, 45000, 5000, "thread-bbb")
	w.writeStatus()

	// Thread A's file must still have thread A's data
	pathA := CacheStatusPath(dir, "thread-aaa")
	dataA, err := os.ReadFile(pathA)
	if err != nil {
		t.Fatalf("thread A file should exist at %s: %v", pathA, err)
	}
	var statusA CacheStatus
	if err := json.Unmarshal(dataA, &statusA); err != nil {
		t.Fatalf("thread A file should be valid JSON: %v", err)
	}
	if statusA.TotalTokens != 100000 {
		t.Errorf("thread A tokens = %d, want 100000", statusA.TotalTokens)
	}
	if statusA.ThreadID != "thread-aaa" {
		t.Errorf("thread A threadID = %q, want thread-aaa", statusA.ThreadID)
	}

	// Thread B's file must have thread B's data
	pathB := CacheStatusPath(dir, "thread-bbb")
	dataB, err := os.ReadFile(pathB)
	if err != nil {
		t.Fatalf("thread B file should exist at %s: %v", pathB, err)
	}
	var statusB CacheStatus
	if err := json.Unmarshal(dataB, &statusB); err != nil {
		t.Fatalf("thread B file should be valid JSON: %v", err)
	}
	if statusB.TotalTokens != 50000 {
		t.Errorf("thread B tokens = %d, want 50000", statusB.TotalTokens)
	}
	if statusB.ThreadID != "thread-bbb" {
		t.Errorf("thread B threadID = %q, want thread-bbb", statusB.ThreadID)
	}

	// No global status.json should exist
	globalPath := dir + "/cache-status/status.json"
	if _, err := os.Stat(globalPath); err == nil {
		t.Errorf("global status.json should NOT exist, but found at %s", globalPath)
	}
}

func TestCacheStatusWriter_UpdateThresholdPerThread(t *testing.T) {
	dir := t.TempDir()
	w := &CacheStatusWriter{
		dataDir:               dir,
		ttlConfig:             "ephemeral",
		tokenMinimumThreshold: 80000,
		threads:               make(map[string]*statusThreadState),
	}

	now := time.Now()

	// Thread A uses Opus (200k threshold)
	w.Update(now, 100000, 95000, 5000, "thread-aaa")
	w.UpdateThresholdForThread("thread-aaa", 200000)

	// Thread B uses Sonnet (150k threshold)
	w.Update(now, 80000, 75000, 5000, "thread-bbb")
	w.UpdateThresholdForThread("thread-bbb", 150000)

	w.writeStatus()

	pathA := CacheStatusPath(dir, "thread-aaa")
	dataA, _ := os.ReadFile(pathA)
	var statusA CacheStatus
	json.Unmarshal(dataA, &statusA)
	if statusA.TokenThreshold != 200000 {
		t.Errorf("thread A threshold = %d, want 200000", statusA.TokenThreshold)
	}

	pathB := CacheStatusPath(dir, "thread-bbb")
	dataB, _ := os.ReadFile(pathB)
	var statusB CacheStatus
	json.Unmarshal(dataB, &statusB)
	if statusB.TokenThreshold != 150000 {
		t.Errorf("thread B threshold = %d, want 150000", statusB.TokenThreshold)
	}
}

func TestCacheStatusWriter_UpdateRawPerThread(t *testing.T) {
	dir := t.TempDir()
	w := &CacheStatusWriter{
		dataDir:               dir,
		ttlConfig:             "ephemeral",
		tokenMinimumThreshold: 80000,
		threads:               make(map[string]*statusThreadState),
	}

	now := time.Now()

	w.Update(now, 100000, 95000, 5000, "thread-aaa")
	w.UpdateRawForThread("thread-aaa", 250000)

	w.Update(now, 80000, 75000, 5000, "thread-bbb")
	// Thread B has no raw estimate

	w.writeStatus()

	pathA := CacheStatusPath(dir, "thread-aaa")
	dataA, _ := os.ReadFile(pathA)
	var statusA CacheStatus
	json.Unmarshal(dataA, &statusA)
	if statusA.RawTokenEstimate != 250000 {
		t.Errorf("thread A raw = %d, want 250000", statusA.RawTokenEstimate)
	}

	pathB := CacheStatusPath(dir, "thread-bbb")
	dataB, _ := os.ReadFile(pathB)
	var statusB CacheStatus
	json.Unmarshal(dataB, &statusB)
	if statusB.RawTokenEstimate != 0 {
		t.Errorf("thread B raw = %d, want 0", statusB.RawTokenEstimate)
	}
}

func TestFormatStatusLines_KeepaliveUntilCalculation(t *testing.T) {
	// Cache expires at lastRequest + TTL = now + 3600
	// Keepalive extends by 4 * 3240s = 12960s from cache expiry
	// Total = now + 3600 + 12960 = now + 16560
	now := time.Now()
	s := CacheStatus{
		TTLSeconds:    3600,
		RemainingS:    3600,
		CacheState:    "warm",
		LastRequestTS: now.Unix(),
		TotalTokens:   100000,
		DetectedTTL:   "1h",
		PingIntervalS: 3240,
		PingsRemaining: 4,
	}
	lines := FormatStatusLines(s)

	cacheExpiry := now.Add(3600 * time.Second)
	keepaliveEnd := cacheExpiry.Add(4 * 3240 * time.Second)
	expectedKATime := keepaliveEnd.Format("15:04")
	expectedCacheTime := cacheExpiry.Format("15:04")

	if !strings.Contains(lines.CacheLine, "until "+expectedCacheTime) {
		t.Errorf("CacheLine should show cache expiry time %s, got: %s", expectedCacheTime, lines.CacheLine)
	}
	if !strings.Contains(lines.CacheLine, "Keepalive until "+expectedKATime) {
		t.Errorf("CacheLine should show keepalive end time %s, got: %s", expectedKATime, lines.CacheLine)
	}
}
