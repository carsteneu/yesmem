package extraction

import (
	"context"
	"sync"
	"time"
)

// RateLimiter limits concurrent LLM calls and enforces a minimum interval between calls.
// Uses a semaphore for concurrency and a last-start timestamp for interval pacing.
type RateLimiter struct {
	sem         chan struct{}
	mu          sync.Mutex
	lastStart   time.Time
	minInterval time.Duration
}

// NewRateLimiter creates a new rate limiter with max concurrent calls and min interval.
func NewRateLimiter(maxConcurrent int, minInterval time.Duration) *RateLimiter {
	if maxConcurrent < 1 {
		maxConcurrent = 1
	}
	return &RateLimiter{
		sem:         make(chan struct{}, maxConcurrent),
		minInterval: minInterval,
	}
}

// Acquire blocks until a call slot is available and the minimum interval has elapsed.
// Returns an error if the context is cancelled before a slot is available.
func (rl *RateLimiter) Acquire(ctx context.Context) error {
	// Wait for semaphore slot
	select {
	case rl.sem <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}

	// Enforce minimum interval between call starts
	if rl.minInterval > 0 {
		rl.mu.Lock()
		if !rl.lastStart.IsZero() {
			since := time.Since(rl.lastStart)
			if since < rl.minInterval {
				wait := rl.minInterval - since

				// Non-blocking semaphore release + reacquire would be complex.
				// Sleeping while holding the slot is fine — it just delays the
				// actual LLM call start, which is the intended behavior.
				rl.mu.Unlock()

				select {
				case <-time.After(wait):
				case <-ctx.Done():
					<-rl.sem // release slot
					return ctx.Err()
				}

				rl.mu.Lock()
			}
		}
		rl.lastStart = time.Now()
		rl.mu.Unlock()
	}

	return nil
}

// Release frees a call slot so another caller can proceed.
func (rl *RateLimiter) Release() {
	<-rl.sem
}

// DefaultLLMRateLimiter limits concurrent LLM API calls daemon-wide.
// Initialized by the daemon at startup. Components that make LLM calls
// should check this field and use Acquire/Release if non-nil.
var DefaultLLMRateLimiter *RateLimiter = NewRateLimiter(2, 5*time.Second)
