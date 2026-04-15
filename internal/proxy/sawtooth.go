package proxy

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

// sawtoothTTLForCacheTTL returns how long frozen stubs survive in RAM.
// Must exceed the Anthropic cache TTL to avoid a dead zone where RAM is empty
// but the API cache still holds the old prefix (causing prefix mismatch).
func sawtoothTTLForCacheTTL(cacheTTL string) time.Duration {
	switch cacheTTL {
	case "1h":
		return 65 * time.Minute
	default:
		return 30 * time.Minute
	}
}

// FrozenStubs stores per-thread frozen stub prefixes for cache optimization.
// Between stub-cycles, the frozen prefix is reused byte-identically so the API
// cache can hit on the prefix portion of the messages array.
type FrozenStubs struct {
	mu           sync.RWMutex
	ttl          time.Duration       // eviction TTL — derived from CacheTTL config
	messages     map[string][]any    // threadID → deep-copied stub messages
	cutoff       map[string]int      // threadID → original message count at stub time
	boundaryHash map[string]string   // threadID → hash of messages[cutoff-1]
	prefixHash   map[string]string   // threadID → hash of marshaled frozen prefix
	stubTime     map[string]time.Time
	tokens       map[string]int // threadID → token estimate of frozen stubs
	rawTokens    map[string]int // threadID → raw token estimate at stub time (pre-compression)
	lastAccess   map[string]time.Time
	persistFn    PersistFunc         // optional: persist frozen state to DB
	loadFn       LoadFunc            // optional: load frozen state from DB on cold start
	loadedFromDB map[string]bool     // threadID → already attempted DB load
}

// frozenPersisted is the JSON-serializable form of frozen stub state.
type frozenPersisted struct {
	Messages     []any  `json:"messages"`
	Cutoff       int    `json:"cutoff"`
	BoundaryHash string `json:"boundary_hash"`
	PrefixHash   string `json:"prefix_hash"`
	Tokens       int    `json:"tokens"`
	RawTokens    int    `json:"raw_tokens,omitempty"`
}

// NewFrozenStubs creates a new FrozenStubs store with the default 30-minute TTL.
func NewFrozenStubs() *FrozenStubs {
	return NewFrozenStubsWithTTL(30 * time.Minute)
}

// NewFrozenStubsWithTTL creates a FrozenStubs store with a custom eviction TTL.
func NewFrozenStubsWithTTL(ttl time.Duration) *FrozenStubs {
	return &FrozenStubs{
		ttl:          ttl,
		messages:     make(map[string][]any),
		cutoff:       make(map[string]int),
		boundaryHash: make(map[string]string),
		prefixHash:   make(map[string]string),
		stubTime:     make(map[string]time.Time),
		tokens:       make(map[string]int),
		rawTokens:    make(map[string]int),
		lastAccess:   make(map[string]time.Time),
		loadedFromDB: make(map[string]bool),
	}
}

// Store freezes stubbed messages for a thread. Deep-copies messages and computes
// boundary/prefix hashes for validation.
// cutoff is the original message count (the index of the first unstubbed message).
// boundaryMsg is originalMessages[cutoff-1] used for boundary validation.
func (f *FrozenStubs) Store(threadID string, stubbed []any, cutoff int, boundaryMsg any, tokenEstimate int, rawTokenEstimate int) {
	frozen := deepCopyMessages(stubbed)

	frozenJSON, _ := json.Marshal(frozen)
	pHash := sha256hex(frozenJSON)

	bHash := stableBoundaryHash(boundaryMsg)

	now := time.Now()

	f.mu.Lock()

	f.messages[threadID] = frozen
	f.cutoff[threadID] = cutoff
	f.boundaryHash[threadID] = bHash
	f.prefixHash[threadID] = pHash
	f.stubTime[threadID] = now
	f.tokens[threadID] = tokenEstimate
	f.rawTokens[threadID] = rawTokenEstimate
	f.lastAccess[threadID] = now
	f.loadedFromDB[threadID] = true // in-memory is authoritative now
	f.mu.Unlock()

	// Persist to DB for cross-restart survival
	if f.persistFn != nil {
		fp := frozenPersisted{
			Messages:     frozen,
			Cutoff:       cutoff,
			BoundaryHash: bHash,
			PrefixHash:   pHash,
			Tokens:       tokenEstimate,
			RawTokens:    rawTokenEstimate,
		}
		if data, err := json.Marshal(fp); err == nil {
			f.persistFn("frozen:"+threadID, string(data))
		}
	}
}

// FrozenResult holds a valid frozen prefix and its metadata.
type FrozenResult struct {
	Messages  []any // frozen stub messages (do not mutate!)
	Cutoff    int   // original message count at stub time
	Tokens    int   // token estimate
	RawTokens int   // raw token estimate at stub time (pre-compression)
}

// Get returns the frozen stubs for a thread if valid.
// Validates that: (1) enough messages exist (len >= cutoff),
// (2) frozen prefix hasn't been mutated in memory.
// CC may inject system-reminders into older messages, so we skip
// boundary hash validation (it's too fragile).
func (f *FrozenStubs) Get(threadID string, currentMessages []any) *FrozenResult {
	f.mu.RLock()
	msgs, ok := f.messages[threadID]
	loaded := f.loadedFromDB[threadID]
	f.mu.RUnlock()

	// Lazy-load from DB on cold start
	if !ok && !loaded {
		f.loadFrozenFromDB(threadID)
		f.mu.RLock()
		msgs, ok = f.messages[threadID]
		f.mu.RUnlock()
	}

	if !ok {
		return nil
	}

	f.mu.RLock()
	cutoff := f.cutoff[threadID]
	pHash := f.prefixHash[threadID]
	tokens := f.tokens[threadID]
	rawTokens := f.rawTokens[threadID]
	f.mu.RUnlock()

	// Validate: enough messages in current request
	if len(currentMessages) < cutoff {
		f.Invalidate(threadID)
		return nil
	}

	// Verify prefix hash (detect unexpected mutation of frozen data)
	frozenJSON, _ := json.Marshal(msgs)
	if sha256hex(frozenJSON) != pHash {
		f.Invalidate(threadID)
		return nil
	}

	// Update last access
	f.mu.Lock()
	f.lastAccess[threadID] = time.Now()
	f.mu.Unlock()

	// Deep-copy to prevent mutation by downstream operations
	// (UpgradeCacheTTL, InjectCacheBreakpoints add cache_control to maps in-place)
	copied := deepCopyMessages(msgs)
	if copied == nil {
		f.Invalidate(threadID)
		return nil
	}

	return &FrozenResult{
		Messages:  copied,
		Cutoff:    cutoff,
		Tokens:    tokens,
		RawTokens: rawTokens,
	}
}

// Invalidate removes frozen stubs for a thread.
func (f *FrozenStubs) Invalidate(threadID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.messages, threadID)
	delete(f.cutoff, threadID)
	delete(f.boundaryHash, threadID)
	delete(f.prefixHash, threadID)
	delete(f.stubTime, threadID)
	delete(f.tokens, threadID)
	delete(f.lastAccess, threadID)
}

// Evict removes entries that haven't been accessed within the TTL.
// UpdateTTL changes the eviction TTL at runtime (e.g. after 1h cache detection).
func (f *FrozenStubs) UpdateTTL(ttl time.Duration) {
	f.mu.Lock()
	f.ttl = ttl
	f.mu.Unlock()
}

// Touch refreshes lastAccess for a thread without changing content.
// Called by keepalive pings to prevent stubs from expiring while API cache is warm.
func (f *FrozenStubs) Touch(threadID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.lastAccess[threadID]; ok {
		f.lastAccess[threadID] = time.Now()
	}
}

func (f *FrozenStubs) Evict() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	cutoff := time.Now().Add(-f.ttl)
	evicted := 0
	for tid, t := range f.lastAccess {
		if t.Before(cutoff) {
			delete(f.messages, tid)
			delete(f.cutoff, tid)
			delete(f.boundaryHash, tid)
			delete(f.prefixHash, tid)
			delete(f.stubTime, tid)
			delete(f.tokens, tid)
			delete(f.lastAccess, tid)
			evicted++
		}
	}
	return evicted
}

// loadFrozenFromDB attempts to restore frozen stubs from DB for a thread.
// Called once per thread on cold start (lazy load from Get).
func (f *FrozenStubs) loadFrozenFromDB(threadID string) {
	f.mu.Lock()
	if f.loadedFromDB[threadID] {
		f.mu.Unlock()
		return
	}
	f.loadedFromDB[threadID] = true
	loadFn := f.loadFn
	f.mu.Unlock()

	if loadFn == nil {
		return
	}

	raw, ok := loadFn("frozen:" + threadID)
	if !ok || raw == "" {
		return
	}

	var fp frozenPersisted
	if err := json.Unmarshal([]byte(raw), &fp); err != nil {
		return
	}
	if len(fp.Messages) == 0 || fp.PrefixHash == "" {
		return
	}

	// Verify prefix hash matches stored messages
	frozenJSON, _ := json.Marshal(fp.Messages)
	if sha256hex(frozenJSON) != fp.PrefixHash {
		return
	}

	now := time.Now()
	f.mu.Lock()
	defer f.mu.Unlock()
	// Only populate if still empty (Store() may have been called concurrently)
	if _, exists := f.messages[threadID]; exists {
		return
	}
	f.messages[threadID] = fp.Messages
	f.cutoff[threadID] = fp.Cutoff
	f.boundaryHash[threadID] = fp.BoundaryHash
	f.prefixHash[threadID] = fp.PrefixHash
	f.tokens[threadID] = fp.Tokens
	f.rawTokens[threadID] = fp.RawTokens
	f.stubTime[threadID] = now
	f.lastAccess[threadID] = now
}

// Has returns true if frozen stubs exist for a thread (without validation).
func (f *FrozenStubs) Has(threadID string) bool {
	f.mu.RLock()
	defer f.mu.RUnlock()
	_, ok := f.messages[threadID]
	return ok
}

// Persist writes current in-memory frozen state for a thread to DB.
// Used to re-persist after a failed Store() (e.g. daemon unreachable after deploy).
func (f *FrozenStubs) Persist(threadID string) {
	f.mu.RLock()
	msgs, ok := f.messages[threadID]
	if !ok {
		f.mu.RUnlock()
		return
	}
	fn := f.persistFn
	cutoff := f.cutoff[threadID]
	bHash := f.boundaryHash[threadID]
	pHash := f.prefixHash[threadID]
	tokens := f.tokens[threadID]
	f.mu.RUnlock()

	if fn == nil {
		return
	}

	fp := frozenPersisted{
		Messages:     msgs,
		Cutoff:       cutoff,
		BoundaryHash: bHash,
		PrefixHash:   pHash,
		Tokens:       tokens,
	}
	if data, err := json.Marshal(fp); err == nil {
		fn("frozen:"+threadID, string(data))
	}
}

// SetPersistFunc sets the callback for persisting frozen state to DB.
func (f *FrozenStubs) SetPersistFunc(fn PersistFunc) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.persistFn = fn
}

// SetLoadFunc sets the callback for loading frozen state from DB on cold start.
func (f *FrozenStubs) SetLoadFunc(fn LoadFunc) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.loadFn = fn
}

// deepCopyMessages creates a deep copy of a messages slice via JSON round-trip.
func deepCopyMessages(msgs []any) []any {
	data, err := json.Marshal(msgs)
	if err != nil {
		return nil
	}
	var out []any
	if json.Unmarshal(data, &out) != nil {
		return nil
	}
	return out
}

// stableBoundaryHash creates a hash from stable parts of a message:
// role + first 200 chars of the first text content block.
// This avoids invalidation from injected system-reminders and context tags.
func stableBoundaryHash(msg any) string {
	mm, ok := msg.(map[string]any)
	if !ok {
		data, _ := json.Marshal(msg)
		return sha256hex(data)
	}

	role, _ := mm["role"].(string)
	text := ""

	// Extract first text from content
	switch c := mm["content"].(type) {
	case string:
		text = c
	case []any:
		for _, block := range c {
			if bm, ok := block.(map[string]any); ok {
				if t, ok := bm["text"].(string); ok {
					text = t
					break
				}
			}
		}
	}

	// Truncate to first 200 chars for stability
	if len(text) > 200 {
		text = text[:200]
	}

	return sha256hex([]byte(role + ":" + text))
}

// sha256hex returns the hex-encoded SHA-256 hash of data.
func sha256hex(data []byte) string {
	h := sha256.Sum256(data)
	return fmt.Sprintf("%x", h[:])
}

// --- Sawtooth Trigger ---

// TriggerReason indicates why a stub-cycle was triggered.
type TriggerReason string

const (
	TriggerNone      TriggerReason = ""
	TriggerTokens    TriggerReason = "tokens"    // lastTotalTokens > 180k
	TriggerPause     TriggerReason = "pause"     // pause > 330s && tokens > 100k
	TriggerEmergency TriggerReason = "emergency" // rawEstimate > 190k
)

// PersistFunc writes a key-value pair to persistent storage (e.g. proxy_state DB).
type PersistFunc func(key, value string)

// LoadFunc reads a key from persistent storage. Returns value and whether found.
type LoadFunc func(key string) (string, bool)

// SawtoothTrigger decides when to run a stub-cycle based on token usage and timing.
type SawtoothTrigger struct {
	mu               sync.RWMutex
	lastTotalTokens  map[string]int       // threadID → last API response input tokens
	lastMessageCount map[string]int       // threadID → message count at last response
	lastRequestTime  map[string]time.Time // threadID → time of last API response
	loadedFromDB     map[string]bool      // threadID → already attempted DB load
	pauseThreshold   time.Duration        // cache TTL - safety margin
	tokenThreshold   int                  // trigger stub-cycle above this (from config)
	tokenMinimum     int                  // stub down to this floor (from config)
	persistFn        PersistFunc          // optional: persist tokens to DB on update
	loadFn           LoadFunc             // optional: load tokens from DB on cold start
}

// NewSawtoothTrigger creates a new trigger state tracker.
func NewSawtoothTrigger(pauseThreshold time.Duration, tokenThreshold, tokenMinimum int) *SawtoothTrigger {
	return &SawtoothTrigger{
		lastTotalTokens:  make(map[string]int),
		lastMessageCount: make(map[string]int),
		lastRequestTime:  make(map[string]time.Time),
		loadedFromDB:     make(map[string]bool),
		pauseThreshold:   pauseThreshold,
		tokenThreshold:   tokenThreshold,
		tokenMinimum:     tokenMinimum,
	}
}

// ShouldTrigger checks if a stub-cycle should run for this thread.
// rawEstimate is the pre-API-call token estimate (for emergency brake).
func (st *SawtoothTrigger) ShouldTrigger(threadID string, rawEstimate int) TriggerReason {
	st.mu.RLock()
	defer st.mu.RUnlock()

	emergencyThreshold := st.tokenThreshold + 10_000 // 10k margin above threshold

	// Emergency brake — raw estimate too high, no usage data needed
	if rawEstimate > emergencyThreshold {
		return TriggerEmergency
	}

	// Token threshold based on raw estimate (works even without prior API data)
	if rawEstimate > st.tokenThreshold {
		return TriggerTokens
	}

	tokens, hasTokens := st.lastTotalTokens[threadID]
	lastTime, hasTime := st.lastRequestTime[threadID]

	// No prior data and below threshold → no stub needed
	if !hasTokens {
		return TriggerNone
	}

	// Token threshold based on last API response (more accurate than estimate)
	if tokens > st.tokenThreshold {
		return TriggerTokens
	}

	// Pause detection: cache is cold after TTL, worth re-stubbing if enough tokens
	if hasTime && tokens > st.tokenMinimum {
		if time.Since(lastTime) > st.pauseThreshold {
			return TriggerPause
		}
	}

	return TriggerNone
}

// SetTokenThreshold updates the trigger threshold dynamically (e.g. from runtime config overrides).
func (st *SawtoothTrigger) SetTokenThreshold(threshold int) {
	st.mu.Lock()
	st.tokenThreshold = threshold
	st.mu.Unlock()
}

// SetPersistFunc sets the callback for writing token state to persistent storage.
func (st *SawtoothTrigger) SetPersistFunc(fn PersistFunc) {
	st.persistFn = fn
}

// SetLoadFunc sets the callback for reading token state from persistent storage.
func (st *SawtoothTrigger) SetLoadFunc(fn LoadFunc) {
	st.loadFn = fn
}

// persistedState is the JSON structure stored in proxy_state.
type persistedState struct {
	Tokens   int `json:"tokens"`
	MsgCount int `json:"msg_count"`
}

// UpdateAfterResponse records the actual token count, message count, and time after an API response.
func (st *SawtoothTrigger) UpdateAfterResponse(threadID string, totalInputTokens, messageCount int) {
	st.mu.Lock()
	st.lastTotalTokens[threadID] = totalInputTokens
	st.lastMessageCount[threadID] = messageCount
	st.lastRequestTime[threadID] = time.Now()
	st.loadedFromDB[threadID] = true
	st.mu.Unlock()

	if st.persistFn != nil {
		data, _ := json.Marshal(persistedState{Tokens: totalInputTokens, MsgCount: messageCount})
		st.persistFn("sawtooth:"+threadID, string(data))
	}
}

// TouchRequestTime refreshes lastRequestTime without changing token/message state.
// Called by keepalive pings to prevent false pause-triggered stub-cycles.
func (st *SawtoothTrigger) TouchRequestTime(threadID string) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if _, ok := st.lastRequestTime[threadID]; ok {
		st.lastRequestTime[threadID] = time.Now()
	}
}

// GetLastTokens returns the last known token count for a thread (0 if unknown).
// On cold start (no in-memory value), attempts to load from DB via LoadFunc.
func (st *SawtoothTrigger) GetLastTokens(threadID string) int {
	st.mu.RLock()
	tokens := st.lastTotalTokens[threadID]
	loaded := st.loadedFromDB[threadID]
	st.mu.RUnlock()

	if tokens > 0 || loaded {
		return tokens
	}
	st.loadFromDB(threadID)

	st.mu.RLock()
	defer st.mu.RUnlock()
	return st.lastTotalTokens[threadID]
}

// GetLastMessageCount returns the last known message count for a thread (0 if unknown).
// On cold start (no in-memory value), attempts to load from DB via LoadFunc.
func (st *SawtoothTrigger) GetLastMessageCount(threadID string) int {
	st.mu.RLock()
	count := st.lastMessageCount[threadID]
	loaded := st.loadedFromDB[threadID]
	st.mu.RUnlock()

	if count > 0 || loaded {
		return count
	}
	st.loadFromDB(threadID)

	st.mu.RLock()
	defer st.mu.RUnlock()
	return st.lastMessageCount[threadID]
}

// loadFromDB loads persisted token state for a thread. Called once per thread on cold start.
func (st *SawtoothTrigger) loadFromDB(threadID string) {
	st.mu.Lock()
	defer st.mu.Unlock()

	// Double-check after acquiring write lock
	if st.loadedFromDB[threadID] {
		return
	}
	st.loadedFromDB[threadID] = true

	if st.loadFn == nil {
		return
	}
	val, ok := st.loadFn("sawtooth:" + threadID)
	if !ok || val == "" {
		return
	}
	var state persistedState
	if err := json.Unmarshal([]byte(val), &state); err != nil {
		return
	}
	st.lastTotalTokens[threadID] = state.Tokens
	st.lastMessageCount[threadID] = state.MsgCount
}
