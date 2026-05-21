package proxy

import "sync"

// ForkState tracks per-thread token growth and failure counts for forked agents.
type ForkState struct {
	mu                  sync.Mutex
	tokenGrowthTrigger  int
	minTotalTokens      int // minimum absolute tokens before any fork is allowed
	maxFailures         int
	maxForksPerSession  int
	threads             map[string]*threadForkState
}

type threadForkState struct {
	lastTotalTokens     int // absolute token count at last check
	tokensSinceLastFork int // accumulated growth since last fork
	consecutiveFailures int
	forkCount           int
	disabled            bool
	forkPending         bool // prevents stacking fork calls before RecordFork completes
	cacheWarmCount      int  // number of warm-cache main requests on this proxy instance
}

// NewForkState creates a ForkState with the given token growth trigger, minimum total tokens,
// max failure count, and max forks per session.
func NewForkState(tokenGrowthTrigger, minTotalTokens, maxFailures, maxForksPerSession int) *ForkState {
	return &ForkState{
		tokenGrowthTrigger: tokenGrowthTrigger,
		minTotalTokens:     minTotalTokens,
		maxFailures:        maxFailures,
		maxForksPerSession: maxForksPerSession,
		threads:            make(map[string]*threadForkState),
	}
}

func (fs *ForkState) getOrCreate(threadID string) *threadForkState {
	if ts, ok := fs.threads[threadID]; ok {
		return ts
	}
	ts := &threadForkState{}
	fs.threads[threadID] = ts
	return ts
}

// ShouldFork returns true if the thread has accumulated enough token growth, meets
// the minimum total token threshold, and has cache.
// totalTokens is the absolute input token count of the current request (not a delta).
func (fs *ForkState) ShouldFork(threadID string, totalTokens int, hasCache bool) bool {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if !hasCache {
		return false
	}
	if totalTokens < fs.minTotalTokens {
		return false
	}
	ts := fs.getOrCreate(threadID)
	if ts.disabled {
		return false
	}
	if ts.maxForksReached(fs.maxForksPerSession) {
		return false
	}
	if ts.forkPending {
		return false
	}
	delta := totalTokens - ts.lastTotalTokens
	if delta < 0 {
		delta = 0
	}
	ts.lastTotalTokens = totalTokens
	ts.tokensSinceLastFork += delta
	if ts.tokensSinceLastFork >= fs.tokenGrowthTrigger {
		ts.forkPending = true
		return true
	}
	return false
}

func (ts *threadForkState) maxForksReached(limit int) bool {
	return limit > 0 && ts.forkCount >= limit
}

// MarkCacheWarm records a warm-cache main request for this session.
// Thread-safe; may be called from multiple goroutines.
func (fs *ForkState) MarkCacheWarm(threadID string) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	ts := fs.getOrCreate(threadID)
	ts.cacheWarmCount++
}

// IsCacheProven returns true if the session has enough warm-cache proofs (>=2).
func (fs *ForkState) IsCacheProven(threadID string) bool {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	ts, ok := fs.threads[threadID]
	return ok && ts.cacheWarmCount >= 2
}

// RecordFork resets the token growth counter and failure count for a thread.
func (fs *ForkState) RecordFork(threadID string) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	ts := fs.getOrCreate(threadID)
	ts.tokensSinceLastFork = 0
	ts.consecutiveFailures = 0
	ts.forkCount++
	ts.forkPending = false
}

// RecordFailure increments the failure counter. Disables after maxFailures.
// Resets tokensSinceLastFork so the next fork requires 20k of NEW token growth
// instead of re-firing immediately on every subsequent request.
func (fs *ForkState) RecordFailure(threadID string) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	ts := fs.getOrCreate(threadID)
	ts.consecutiveFailures++
	ts.forkPending = false
	ts.tokensSinceLastFork = 0
	if ts.consecutiveFailures >= fs.maxFailures {
		ts.disabled = true
	}
}

// IsDisabled returns true if forking is disabled for this thread.
func (fs *ForkState) IsDisabled(threadID string) bool {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	ts, ok := fs.threads[threadID]
	if !ok {
		return false
	}
	return ts.disabled
}

// ForceNextFork sets token growth to trigger threshold, guaranteeing next ShouldFork returns true.
func (fs *ForkState) ForceNextFork(threadID string) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	ts := fs.getOrCreate(threadID)
	ts.tokensSinceLastFork = fs.tokenGrowthTrigger
}
