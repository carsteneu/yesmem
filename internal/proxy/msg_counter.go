package proxy

import "sync"

// msgCounters tracks global per-thread message indices that persist across collapses.
// Unlike len(msgs)-1, which resets after a sawtooth collapse, this counter increments
// monotonically so [msg:N] in timestamps always increases throughout the session.
type msgCounters struct {
	mu     sync.Mutex
	counts map[string]int
}

func newMsgCounters() *msgCounters {
	return &msgCounters{counts: make(map[string]int)}
}

// nextFor returns the global message number for threadID.
// On the first call for a thread, localIdx (len(msgs)-1) is used as the starting value
// so new sessions start at their current position. On subsequent calls, the counter
// increments by 1 regardless of any collapse that reduced the message array.
func (mc *msgCounters) nextFor(threadID string, localIdx int) int {
	if mc == nil {
		return localIdx // fallback for test servers without constructor
	}
	mc.mu.Lock()
	defer mc.mu.Unlock()
	n, ok := mc.counts[threadID]
	if !ok {
		mc.counts[threadID] = localIdx
		return localIdx
	}
	next := n + 1
	mc.counts[threadID] = next
	return next
}
