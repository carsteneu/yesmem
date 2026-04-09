package proxy

import (
	"net/http"
	"strconv"
	"time"
)

// RateLimitInfo holds parsed Anthropic rate-limit headers from API responses.
type RateLimitInfo struct {
	RequestsLimit     int       `json:"requests_limit"`
	RequestsRemaining int       `json:"requests_remaining"`
	RequestsReset     time.Time `json:"requests_reset,omitempty"`
	TokensLimit       int       `json:"tokens_limit"`
	TokensRemaining   int       `json:"tokens_remaining"`
	TokensReset       time.Time `json:"tokens_reset,omitempty"`

	Unified5hUtilization float64   `json:"unified_5h_utilization"`
	Unified5hReset       time.Time `json:"unified_5h_reset,omitempty"`
	Unified7dUtilization float64   `json:"unified_7d_utilization"`
	Unified7dReset       time.Time `json:"unified_7d_reset,omitempty"`
	UnifiedStatus        string    `json:"unified_status,omitempty"`
	UnifiedFallback      string    `json:"unified_fallback,omitempty"`

	IsSubscription bool      `json:"is_subscription"`
	ParsedAt       time.Time `json:"parsed_at"`
}

// ParseRateLimitHeaders extracts rate-limit info from Anthropic API response headers.
// Missing or malformed headers are silently ignored (fields stay zero-valued).
func ParseRateLimitHeaders(h http.Header) *RateLimitInfo {
	rl := &RateLimitInfo{ParsedAt: time.Now()}

	rl.RequestsLimit = headerInt(h, "Anthropic-Ratelimit-Requests-Limit")
	rl.RequestsRemaining = headerInt(h, "Anthropic-Ratelimit-Requests-Remaining")
	rl.RequestsReset = headerTime(h, "Anthropic-Ratelimit-Requests-Reset")
	rl.TokensLimit = headerInt(h, "Anthropic-Ratelimit-Tokens-Limit")
	rl.TokensRemaining = headerInt(h, "Anthropic-Ratelimit-Tokens-Remaining")
	rl.TokensReset = headerTime(h, "Anthropic-Ratelimit-Tokens-Reset")

	rl.Unified5hUtilization = headerFloat(h, "Anthropic-Ratelimit-Unified-5h-Utilization")
	rl.Unified5hReset = headerTime(h, "Anthropic-Ratelimit-Unified-5h-Reset")
	rl.Unified7dUtilization = headerFloat(h, "Anthropic-Ratelimit-Unified-7d-Utilization")
	rl.Unified7dReset = headerTime(h, "Anthropic-Ratelimit-Unified-7d-Reset")
	rl.UnifiedStatus = h.Get("Anthropic-Ratelimit-Unified-Status")
	rl.UnifiedFallback = h.Get("Anthropic-Ratelimit-Unified-Fallback")

	rl.IsSubscription = h.Get("Anthropic-Ratelimit-Unified-5h-Utilization") != "" ||
		h.Get("Anthropic-Ratelimit-Unified-7d-Utilization") != ""

	return rl
}

func headerInt(h http.Header, key string) int {
	v, _ := strconv.Atoi(h.Get(key))
	return v
}

func headerFloat(h http.Header, key string) float64 {
	v, _ := strconv.ParseFloat(h.Get(key), 64)
	return v
}

func headerTime(h http.Header, key string) time.Time {
	t, _ := time.Parse(time.RFC3339, h.Get(key))
	return t
}

// ShouldThrottle returns true if current utilization exceeds the threshold.
// Fallback chain: subscription unified-5h → API-key tokens → false (unknown).
func (rl *RateLimitInfo) ShouldThrottle(threshold float64) bool {
	if rl.ParsedAt.IsZero() {
		return false
	}
	return rl.Utilization() > threshold
}

// Utilization returns the current utilization as 0.0-1.0.
// Subscription: unified 5h utilization. API-key: 1 - (remaining/limit).
func (rl *RateLimitInfo) Utilization() float64 {
	if rl.IsSubscription {
		return rl.Unified5hUtilization
	}
	if rl.TokensLimit > 0 {
		return 1.0 - float64(rl.TokensRemaining)/float64(rl.TokensLimit)
	}
	return 0
}
