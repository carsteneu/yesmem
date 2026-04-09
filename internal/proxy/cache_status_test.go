package proxy

import (
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
