package extraction

import (
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Sentinel errors for gate checks.
var (
	ErrAPIDown        = errors.New("API health gate: too many consecutive errors")
	ErrBudgetExceeded = errors.New("API budget gate: daily budget exceeded")
)

// APIGate is a global circuit breaker + budget gate for all LLM API calls.
// Shared across all goroutines via a single instance created at daemon startup.
type APIGate struct {
	mu                sync.Mutex
	consecutiveErrors int32 // also read atomically for fast-path
	cooldownUntil     time.Time
	cooldownLevel     int // 0=30s, 1=60s, 2=120s, 3+=300s
	lastErrorMsg      string

	threshold int // consecutive errors before Down (default: 5)

	extractTracker *BudgetTracker
	qualityTracker *BudgetTracker

	reserveUSD float64 // budget reserve per check (default: 0.50)
}

// NewAPIGate creates a global API health + budget gate.
func NewAPIGate(extractTracker, qualityTracker *BudgetTracker, threshold int, reserveUSD float64) *APIGate {
	if threshold <= 0 {
		threshold = 5
	}
	if reserveUSD <= 0 {
		reserveUSD = 0.50
	}
	return &APIGate{
		extractTracker: extractTracker,
		qualityTracker: qualityTracker,
		threshold:      threshold,
		reserveUSD:     reserveUSD,
	}
}

// Check verifies API health and budget before making a call.
// Returns nil if OK, ErrAPIDown if health gate tripped, ErrBudgetExceeded if over budget.
func (g *APIGate) Check(bucket string) error {
	// Fast path: healthy
	if atomic.LoadInt32(&g.consecutiveErrors) == 0 {
		return g.checkBudget(bucket)
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	errs := int(g.consecutiveErrors)

	// Down state: check cooldown
	if errs >= g.threshold {
		if time.Now().Before(g.cooldownUntil) {
			return fmt.Errorf("%w (cooldown until %s, last: %s)",
				ErrAPIDown, g.cooldownUntil.Format("15:04:05"), g.lastErrorMsg)
		}
		// Cooldown expired — allow ONE probe call through
		log.Printf("[api-gate] Cooldown expired, allowing probe call (errors=%d)", errs)
	}

	return g.checkBudget(bucket)
}

// RecordSuccess resets the error counter on a successful API call.
func (g *APIGate) RecordSuccess() {
	prev := atomic.SwapInt32(&g.consecutiveErrors, 0)
	if prev >= int32(g.threshold) {
		g.mu.Lock()
		g.cooldownLevel = 0
		g.mu.Unlock()
		log.Printf("[api-gate] Recovered from Down state (was %d consecutive errors)", prev)
	}
}

// RecordError increments the error counter and may trip the gate.
func (g *APIGate) RecordError(err error) {
	errMsg := ""
	if err != nil {
		errMsg = err.Error()
	}

	// Don't count budget errors as API health issues
	if errors.Is(err, ErrBudgetExceeded) {
		return
	}

	newCount := atomic.AddInt32(&g.consecutiveErrors, 1)

	g.mu.Lock()
	g.lastErrorMsg = errMsg

	if int(newCount) == g.threshold {
		// Just tripped — set cooldown
		cooldownSecs := g.cooldownDuration()
		g.cooldownUntil = time.Now().Add(cooldownSecs)
		g.cooldownLevel++
		g.mu.Unlock()

		log.Printf("[api-gate] DOWN: %d consecutive errors, cooldown %v (last: %s)",
			newCount, cooldownSecs, truncStr(errMsg, 120))
	} else {
		g.mu.Unlock()
	}
}

// IsDown returns true if the gate is in Down state.
func (g *APIGate) IsDown() bool {
	return int(atomic.LoadInt32(&g.consecutiveErrors)) >= g.threshold
}

// Status returns a human-readable status string.
func (g *APIGate) Status() string {
	errs := int(atomic.LoadInt32(&g.consecutiveErrors))
	if errs == 0 {
		return "healthy"
	}
	if errs < g.threshold {
		return fmt.Sprintf("degraded (%d errors)", errs)
	}
	g.mu.Lock()
	until := g.cooldownUntil
	g.mu.Unlock()
	return fmt.Sprintf("down (cooldown until %s)", until.Format("15:04:05"))
}

func (g *APIGate) checkBudget(bucket string) error {
	tracker := g.trackerForBucket(bucket)
	if tracker == nil {
		return nil
	}
	remaining := tracker.Remaining()
	if remaining < g.reserveUSD {
		return fmt.Errorf("%w: %s has $%.2f remaining (reserve: $%.2f)",
			ErrBudgetExceeded, bucket, remaining, g.reserveUSD)
	}
	return nil
}

func (g *APIGate) trackerForBucket(bucket string) *BudgetTracker {
	switch bucket {
	case "extract":
		return g.extractTracker
	case "quality":
		return g.qualityTracker
	default:
		return g.extractTracker // fallback
	}
}

func (g *APIGate) cooldownDuration() time.Duration {
	switch g.cooldownLevel {
	case 0:
		return 30 * time.Second
	case 1:
		return 60 * time.Second
	case 2:
		return 120 * time.Second
	default:
		return 300 * time.Second // cap at 5 minutes
	}
}

func truncStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// ---------------------------------------------------------------------------
// GatedClient — wraps any LLMClient with global health + budget gate
// ---------------------------------------------------------------------------

// GatedClient wraps an LLMClient (typically BudgetClient) with the global APIGate.
// Layering: GatedClient → BudgetClient → Client (API)
type GatedClient struct {
	inner  LLMClient
	gate   *APIGate
	bucket string // "extract" or "quality"
}

// NewGatedClient wraps a client with global health + budget gating.
func NewGatedClient(inner LLMClient, gate *APIGate, bucket string) *GatedClient {
	return &GatedClient{inner: inner, gate: gate, bucket: bucket}
}

func (g *GatedClient) Name() string  { return g.inner.Name() }
func (g *GatedClient) Model() string { return g.inner.Model() }

func (g *GatedClient) Complete(system, userMsg string, opts ...CallOption) (string, error) {
	if err := g.gate.Check(g.bucket); err != nil {
		return "", err
	}
	result, err := g.inner.Complete(system, userMsg, opts...)
	g.recordResult(err)
	return result, err
}

func (g *GatedClient) CompleteJSON(system, userMsg string, schema map[string]any, opts ...CallOption) (string, error) {
	if err := g.gate.Check(g.bucket); err != nil {
		return "", err
	}
	result, err := g.inner.CompleteJSON(system, userMsg, schema, opts...)
	g.recordResult(err)
	return result, err
}

func (g *GatedClient) recordResult(err error) {
	if err == nil {
		g.gate.RecordSuccess()
		return
	}
	// Only count actual API errors, not budget/gate errors
	errStr := err.Error()
	if strings.Contains(errStr, "api error:") || strings.Contains(errStr, "api call:") {
		g.gate.RecordError(err)
	}
}

// Unwrap returns the inner client (for type assertions like HasBudget).
func (g *GatedClient) Unwrap() LLMClient { return g.inner }
