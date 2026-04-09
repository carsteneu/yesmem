package proxy

import (
	"testing"
	"time"
)

func TestSawtoothTrigger_PersistOnUpdate(t *testing.T) {
	// Track what gets persisted
	var persisted map[string]string
	persistFn := func(key, value string) {
		if persisted == nil {
			persisted = make(map[string]string)
		}
		persisted[key] = value
	}

	st := NewSawtoothTrigger(5*time.Minute, 200000, 100000)
	st.SetPersistFunc(persistFn)

	st.UpdateAfterResponse("thread-abc", 150000, 42)

	// Should have persisted tokens and message count
	if persisted == nil {
		t.Fatal("expected persist callback to be called")
	}
	val, ok := persisted["sawtooth:thread-abc"]
	if !ok {
		t.Fatal("expected key 'sawtooth:thread-abc' in persisted data")
	}
	// Value should contain both tokens and msg count
	if val == "" {
		t.Fatal("persisted value is empty")
	}
}

func TestSawtoothTrigger_LoadOnColdStart(t *testing.T) {
	// Simulate DB with pre-existing data
	store := map[string]string{
		"sawtooth:thread-xyz": `{"tokens":180000,"msg_count":55}`,
	}
	loadFn := func(key string) (string, bool) {
		v, ok := store[key]
		return v, ok
	}

	st := NewSawtoothTrigger(5*time.Minute, 200000, 100000)
	st.SetLoadFunc(loadFn)

	// First access — no in-memory value, should load from DB
	tokens := st.GetLastTokens("thread-xyz")
	if tokens != 180000 {
		t.Errorf("expected 180000 tokens from DB, got %d", tokens)
	}

	msgCount := st.GetLastMessageCount("thread-xyz")
	if msgCount != 55 {
		t.Errorf("expected 55 msg_count from DB, got %d", msgCount)
	}
}

func TestSawtoothTrigger_NoPersistWithoutCallback(t *testing.T) {
	// Without SetPersistFunc, UpdateAfterResponse should still work
	st := NewSawtoothTrigger(5*time.Minute, 200000, 100000)

	st.UpdateAfterResponse("thread-1", 100000, 20)

	tokens := st.GetLastTokens("thread-1")
	if tokens != 100000 {
		t.Errorf("expected 100000, got %d", tokens)
	}
}

func TestSawtoothTrigger_NoLoadWithoutCallback(t *testing.T) {
	// Without SetLoadFunc, GetLastTokens returns 0 for unknown threads
	st := NewSawtoothTrigger(5*time.Minute, 200000, 100000)

	tokens := st.GetLastTokens("unknown-thread")
	if tokens != 0 {
		t.Errorf("expected 0 for unknown thread, got %d", tokens)
	}
}

func TestSawtoothTrigger_InMemoryOverridesDB(t *testing.T) {
	store := map[string]string{
		"sawtooth:thread-1": `{"tokens":100000,"msg_count":30}`,
	}
	loadFn := func(key string) (string, bool) {
		v, ok := store[key]
		return v, ok
	}

	st := NewSawtoothTrigger(5*time.Minute, 200000, 100000)
	st.SetLoadFunc(loadFn)

	// Set in-memory value via UpdateAfterResponse
	st.UpdateAfterResponse("thread-1", 150000, 40)

	// Should return in-memory value, not DB value
	tokens := st.GetLastTokens("thread-1")
	if tokens != 150000 {
		t.Errorf("expected in-memory 150000, got %d", tokens)
	}

	msgCount := st.GetLastMessageCount("thread-1")
	if msgCount != 40 {
		t.Errorf("expected in-memory 40, got %d", msgCount)
	}
}

func TestSawtoothTrigger_LoadOnlyOnce(t *testing.T) {
	loadCount := 0
	loadFn := func(key string) (string, bool) {
		loadCount++
		return `{"tokens":90000,"msg_count":25}`, true
	}

	st := NewSawtoothTrigger(5*time.Minute, 200000, 100000)
	st.SetLoadFunc(loadFn)

	// First call — loads from DB
	st.GetLastTokens("thread-1")
	if loadCount != 1 {
		t.Errorf("expected 1 load, got %d", loadCount)
	}

	// Second call — should use cached value, not load again
	st.GetLastTokens("thread-1")
	if loadCount != 1 {
		t.Errorf("expected still 1 load after second call, got %d", loadCount)
	}
}

func TestSawtoothTrigger_LoadMalformedJSON(t *testing.T) {
	loadFn := func(key string) (string, bool) {
		return "not-json", true
	}

	st := NewSawtoothTrigger(5*time.Minute, 200000, 100000)
	st.SetLoadFunc(loadFn)

	// Malformed JSON should return 0 (fallback)
	tokens := st.GetLastTokens("thread-bad")
	if tokens != 0 {
		t.Errorf("expected 0 for malformed JSON, got %d", tokens)
	}
}

func TestSawtoothTrigger_LoadNotFound(t *testing.T) {
	loadFn := func(key string) (string, bool) {
		return "", false
	}

	st := NewSawtoothTrigger(5*time.Minute, 200000, 100000)
	st.SetLoadFunc(loadFn)

	tokens := st.GetLastTokens("thread-new")
	if tokens != 0 {
		t.Errorf("expected 0 for not-found key, got %d", tokens)
	}
}
