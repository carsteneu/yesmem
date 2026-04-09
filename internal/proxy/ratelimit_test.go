package proxy

import (
	"net/http"
	"testing"
	"time"
)

func TestParseRateLimitHeaders_Standard(t *testing.T) {
	h := http.Header{}
	h.Set("anthropic-ratelimit-requests-limit", "1000")
	h.Set("anthropic-ratelimit-requests-remaining", "847")
	h.Set("anthropic-ratelimit-requests-reset", "2026-04-06T12:00:00Z")
	h.Set("anthropic-ratelimit-tokens-limit", "100000")
	h.Set("anthropic-ratelimit-tokens-remaining", "72000")
	h.Set("anthropic-ratelimit-tokens-reset", "2026-04-06T12:00:00Z")

	rl := ParseRateLimitHeaders(h)

	if rl.RequestsLimit != 1000 {
		t.Errorf("RequestsLimit = %d, want 1000", rl.RequestsLimit)
	}
	if rl.RequestsRemaining != 847 {
		t.Errorf("RequestsRemaining = %d, want 847", rl.RequestsRemaining)
	}
	if rl.TokensLimit != 100000 {
		t.Errorf("TokensLimit = %d, want 100000", rl.TokensLimit)
	}
	if rl.TokensRemaining != 72000 {
		t.Errorf("TokensRemaining = %d, want 72000", rl.TokensRemaining)
	}
	if rl.IsSubscription {
		t.Error("should not be subscription without unified headers")
	}
	if rl.ParsedAt.IsZero() {
		t.Error("ParsedAt should be set")
	}
}

func TestParseRateLimitHeaders_Subscription(t *testing.T) {
	h := http.Header{}
	h.Set("anthropic-ratelimit-unified-5h-utilization", "0.42")
	h.Set("anthropic-ratelimit-unified-5h-reset", "2026-04-06T14:00:00Z")
	h.Set("anthropic-ratelimit-unified-7d-utilization", "0.31")
	h.Set("anthropic-ratelimit-unified-7d-reset", "2026-04-09T00:00:00Z")
	h.Set("anthropic-ratelimit-unified-status", "active")
	h.Set("anthropic-ratelimit-unified-fallback", "available")
	h.Set("anthropic-ratelimit-requests-limit", "500")
	h.Set("anthropic-ratelimit-requests-remaining", "499")

	rl := ParseRateLimitHeaders(h)

	if !rl.IsSubscription {
		t.Error("should be subscription with unified headers")
	}
	if rl.Unified5hUtilization != 0.42 {
		t.Errorf("Unified5hUtilization = %f, want 0.42", rl.Unified5hUtilization)
	}
	if rl.Unified7dUtilization != 0.31 {
		t.Errorf("Unified7dUtilization = %f, want 0.31", rl.Unified7dUtilization)
	}
	if rl.UnifiedStatus != "active" {
		t.Errorf("UnifiedStatus = %q, want active", rl.UnifiedStatus)
	}
	if rl.UnifiedFallback != "available" {
		t.Errorf("UnifiedFallback = %q, want available", rl.UnifiedFallback)
	}
	if rl.RequestsLimit != 500 {
		t.Errorf("RequestsLimit = %d, want 500 (standard headers still parsed)", rl.RequestsLimit)
	}
}

func TestParseRateLimitHeaders_Empty(t *testing.T) {
	h := http.Header{}
	rl := ParseRateLimitHeaders(h)

	if rl.RequestsLimit != 0 {
		t.Errorf("RequestsLimit = %d, want 0 for empty headers", rl.RequestsLimit)
	}
	if rl.IsSubscription {
		t.Error("should not be subscription with empty headers")
	}
	if rl.ParsedAt.IsZero() {
		t.Error("ParsedAt should always be set")
	}
}

func TestParseRateLimitHeaders_MalformedValues(t *testing.T) {
	h := http.Header{}
	h.Set("anthropic-ratelimit-requests-limit", "not-a-number")
	h.Set("anthropic-ratelimit-unified-5h-utilization", "invalid")

	rl := ParseRateLimitHeaders(h)

	if rl.RequestsLimit != 0 {
		t.Errorf("RequestsLimit = %d, want 0 for malformed", rl.RequestsLimit)
	}
	if rl.Unified5hUtilization != 0 {
		t.Errorf("Unified5hUtilization = %f, want 0 for malformed", rl.Unified5hUtilization)
	}
}

func TestShouldThrottle_Subscription(t *testing.T) {
	rl := &RateLimitInfo{
		Unified5hUtilization: 0.60,
		IsSubscription:       true,
		ParsedAt:             time.Now(),
	}
	if !rl.ShouldThrottle(0.5) {
		t.Error("60% utilization should throttle at 50% threshold")
	}
	if rl.ShouldThrottle(0.7) {
		t.Error("60% utilization should not throttle at 70% threshold")
	}
}

func TestShouldThrottle_APIKey(t *testing.T) {
	rl := &RateLimitInfo{
		TokensLimit:     100000,
		TokensRemaining: 30000,
		IsSubscription:  false,
		ParsedAt:        time.Now(),
	}
	if !rl.ShouldThrottle(0.5) {
		t.Error("70% consumed should throttle at 50% threshold")
	}
	if rl.ShouldThrottle(0.8) {
		t.Error("70% consumed should not throttle at 80% threshold")
	}
}

func TestShouldThrottle_APIKey_ZeroLimit(t *testing.T) {
	rl := &RateLimitInfo{
		TokensLimit:     0,
		TokensRemaining: 0,
		IsSubscription:  false,
		ParsedAt:        time.Now(),
	}
	if rl.ShouldThrottle(0.5) {
		t.Error("zero limit should not throttle (unknown state)")
	}
}

func TestShouldThrottle_NoData(t *testing.T) {
	rl := &RateLimitInfo{}
	if rl.ShouldThrottle(0.5) {
		t.Error("no data (zero ParsedAt) should not throttle")
	}
}

func TestUtilization_Subscription(t *testing.T) {
	rl := &RateLimitInfo{
		Unified5hUtilization: 0.42,
		IsSubscription:       true,
		ParsedAt:             time.Now(),
	}
	if u := rl.Utilization(); u != 0.42 {
		t.Errorf("Utilization() = %f, want 0.42", u)
	}
}

func TestUtilization_APIKey(t *testing.T) {
	rl := &RateLimitInfo{
		TokensLimit:     100000,
		TokensRemaining: 40000,
		IsSubscription:  false,
		ParsedAt:        time.Now(),
	}
	u := rl.Utilization()
	if u < 0.59 || u > 0.61 {
		t.Errorf("Utilization() = %f, want ~0.60", u)
	}
}
