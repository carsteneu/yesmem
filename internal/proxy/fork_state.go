package proxy

import "sync"

// ForkState tracks per-thread token growth and failure counts for forked agents.
type ForkState struct {
	mu                  sync.Mutex
	tokenGrowthTrigger  int
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
}

// NewForkState creates a ForkState with the given token growth trigger, max failure count, and max forks per session.
func NewForkState(tokenGrowthTrigger, maxFailures, maxForksPerSession int) *ForkState {
	return &ForkState{
		tokenGrowthTrigger: tokenGrowthTrigger,
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

// ShouldFork returns true if the thread has accumulated enough token growth and has cache.
// totalTokens is the absolute input token count of the current request (not a delta).
func (fs *ForkState) ShouldFork(threadID string, totalTokens int, hasCache bool) bool {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if !hasCache {
		return false
	}
	ts := fs.getOrCreate(threadID)
	if ts.disabled {
		return false
	}
	if ts.maxForksReached(fs.maxForksPerSession) {
		return false
	}
	// Compute delta from last known total
	delta := totalTokens - ts.lastTotalTokens
	if delta < 0 {
		delta = 0 // compaction can reduce token count
	}
	ts.lastTotalTokens = totalTokens
	ts.tokensSinceLastFork += delta
	return ts.tokensSinceLastFork >= fs.tokenGrowthTrigger
}

func (ts *threadForkState) maxForksReached(limit int) bool {
	return limit > 0 && ts.forkCount >= limit
}

// RecordFork resets the token growth counter and failure count for a thread.
func (fs *ForkState) RecordFork(threadID string) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	ts := fs.getOrCreate(threadID)
	ts.tokensSinceLastFork = 0
	ts.consecutiveFailures = 0
	ts.forkCount++
}

// RecordFailure increments the failure counter. Disables after maxFailures.
func (fs *ForkState) RecordFailure(threadID string) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	ts := fs.getOrCreate(threadID)
	ts.consecutiveFailures++
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
