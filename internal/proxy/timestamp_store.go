package proxy

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
)

// TimestampStore persists timestamp/delta data per user message, keyed by msg:N
// (1-based position among user messages). msg:N is derived from counting, not stored here.
//
// Persistence uses the same SetPersistFunc/SetLoadFunc callback pattern as FrozenStubs.
type TimestampStore struct {
	mu      sync.RWMutex
	threads map[string]*threadTimestamps

	persistFunc PersistFunc
	loadFunc    LoadFunc
}

// TimestampMeta holds the temporal data for a single user message.
type TimestampMeta struct {
	Timestamp string `json:"ts,omitempty"` // "Di 2026-04-14 21:32:03"
	Delta     string `json:"d,omitempty"`  // "36s" or "1m33s"
}

type threadTimestamps struct {
	Entries map[int]*TimestampMeta `json:"entries"` // msg:N → timestamp data
}

func NewTimestampStore() *TimestampStore {
	return &TimestampStore{
		threads: make(map[string]*threadTimestamps),
	}
}

func (ts *TimestampStore) SetPersistFunc(fn PersistFunc) { ts.persistFunc = fn }
func (ts *TimestampStore) SetLoadFunc(fn LoadFunc)       { ts.loadFunc = fn }

// Store saves timestamp data for a msg:N.
func (ts *TimestampStore) Store(threadID string, msgN int, meta *TimestampMeta) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	tt := ts.getOrCreate(threadID)
	tt.Entries[msgN] = meta
}

// Get retrieves stored timestamp data for a msg:N.
func (ts *TimestampStore) Get(threadID string, msgN int) (*TimestampMeta, bool) {
	ts.mu.RLock()
	defer ts.mu.RUnlock()
	tt, ok := ts.threads[threadID]
	if !ok {
		return nil, false
	}
	meta, ok := tt.Entries[msgN]
	return meta, ok
}

// Persist serializes the thread's timestamp data via the persistFunc callback.
func (ts *TimestampStore) Persist(threadID string) {
	if ts.persistFunc == nil {
		return
	}
	ts.mu.RLock()
	tt, ok := ts.threads[threadID]
	ts.mu.RUnlock()
	if !ok {
		return
	}
	data, err := json.Marshal(tt)
	if err != nil {
		return
	}
	ts.persistFunc("timestamps:"+threadID, string(data))
}

// Load deserializes a thread's timestamp data via the loadFunc callback.
func (ts *TimestampStore) Load(threadID string) {
	if ts.loadFunc == nil {
		return
	}
	raw, ok := ts.loadFunc("timestamps:" + threadID)
	if !ok || raw == "" {
		return
	}
	var tt threadTimestamps
	if json.Unmarshal([]byte(raw), &tt) != nil {
		return
	}
	if tt.Entries == nil {
		tt.Entries = make(map[int]*TimestampMeta)
	}
	ts.mu.Lock()
	ts.threads[threadID] = &tt
	ts.mu.Unlock()
}

func (ts *TimestampStore) getOrCreate(threadID string) *threadTimestamps {
	tt, ok := ts.threads[threadID]
	if !ok {
		tt = &threadTimestamps{Entries: make(map[int]*TimestampMeta)}
		ts.threads[threadID] = tt
	}
	return tt
}

// BuildMeta assembles the full annotation string from a msg:N and optional stored data.
func BuildMeta(msgN int, meta *TimestampMeta) string {
	if meta != nil && meta.Timestamp != "" {
		s := fmt.Sprintf("[%s] [msg:%d]", meta.Timestamp, msgN)
		if meta.Delta != "" {
			s += " [+" + meta.Delta + "]"
		}
		return s
	}
	return fmt.Sprintf("[msg:%d]", msgN)
}

// InjectTimestamps injects [msg:N] (+ timestamp/delta if available) on messages.
// msg:N = offset + position among ALL messages (not just user). Returns injection count.
// stubsCount: frozen stubs at the start of the array are skipped (already annotated).
// Fresh tail messages are always re-annotated (strips any existing [msg:N] prefix first).
func InjectTimestamps(store *TimestampStore, threadID string, msgs []any, endIdx, offset, stubsCount int) int {
	if store == nil {
		return 0
	}
	if endIdx > len(msgs) {
		endIdx = len(msgs)
	}
	injected := 0
	for i := 0; i < endIdx; i++ {
		msg, ok := msgs[i].(map[string]any)
		if !ok {
			continue
		}
		if i < stubsCount {
			continue // frozen stubs already have persistent annotations
		}
		stripMetaPrefix(msg) // remove any existing [timestamp] [msg:N] [+delta] prefix
		msgN := offset + i + 1
		meta, _ := store.Get(threadID, msgN)
		prependMeta(msg, BuildMeta(msgN, meta))
		injected++
	}
	return injected
}

// msgHasMetaPrefix checks if a message already has a timestamp/msg annotation.
func msgHasMetaPrefix(msg map[string]any) bool {
	switch content := msg["content"].(type) {
	case string:
		return hasMetaPrefix(content)
	case []any:
		for _, block := range content {
			b, ok := block.(map[string]any)
			if !ok || b["type"] != "text" {
				continue
			}
			text, _ := b["text"].(string)
			return hasMetaPrefix(text)
		}
	}
	return false
}

// msgHasTextBlock returns true if the message has at least one text content block.
func msgHasTextBlock(msg map[string]any) bool {
	switch content := msg["content"].(type) {
	case string:
		return content != ""
	case []any:
		for _, block := range content {
			b, ok := block.(map[string]any)
			if ok && b["type"] == "text" {
				return true
			}
		}
	}
	return false
}

// CountUserMessages counts role="user" messages in msgs[0:endIdx].
func CountUserMessages(msgs []any, endIdx int) int {
	if endIdx > len(msgs) {
		endIdx = len(msgs)
	}
	n := 0
	for i := 0; i < endIdx; i++ {
		msg, ok := msgs[i].(map[string]any)
		if ok && msg["role"] == "user" {
			n++
		}
	}
	return n
}

// hasMetaPrefix is defined in proxy_helpers.go — uses strings package
var _ = strings.Contains // ensure import
