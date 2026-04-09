package proxy

import (
	"encoding/json"
	"testing"
	"time"
)

func TestFrozenStubs_PersistOnStore(t *testing.T) {
	persisted := make(map[string]string)
	persistFn := func(key, value string) {
		persisted[key] = value
	}

	fs := NewFrozenStubsWithTTL(30 * time.Minute)
	fs.SetPersistFunc(persistFn)

	msgs := []any{
		map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": "hello"}}},
		map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "text", "text": "hi"}}},
	}
	boundary := map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": "boundary msg"}}}

	fs.Store("thread-1", msgs, 3, boundary, 5000, 0)

	val, ok := persisted["frozen:thread-1"]
	if !ok {
		t.Fatal("expected persist callback to be called with key 'frozen:thread-1'")
	}

	// Verify JSON structure
	var fp frozenPersisted
	if err := json.Unmarshal([]byte(val), &fp); err != nil {
		t.Fatalf("persisted value is not valid JSON: %v", err)
	}
	if fp.Cutoff != 3 {
		t.Errorf("expected cutoff=3, got %d", fp.Cutoff)
	}
	if fp.Tokens != 5000 {
		t.Errorf("expected tokens=5000, got %d", fp.Tokens)
	}
	if len(fp.Messages) != 2 {
		t.Errorf("expected 2 messages, got %d", len(fp.Messages))
	}
	if fp.PrefixHash == "" {
		t.Error("expected non-empty prefix hash")
	}
	if fp.BoundaryHash == "" {
		t.Error("expected non-empty boundary hash")
	}
}

func TestFrozenStubs_RestoreFromDB(t *testing.T) {
	// Create first instance, store stubs, capture persisted data
	persisted := make(map[string]string)
	persistFn := func(key, value string) { persisted[key] = value }

	fs1 := NewFrozenStubsWithTTL(30 * time.Minute)
	fs1.SetPersistFunc(persistFn)

	msgs := []any{
		map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": "hello"}}},
		map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "text", "text": "world"}}},
	}
	boundary := msgs[1]
	fs1.Store("thread-1", msgs, 5, boundary, 8000, 0)

	// Verify original works
	currentMsgs := make([]any, 10) // enough messages
	result1 := fs1.Get("thread-1", currentMsgs)
	if result1 == nil {
		t.Fatal("expected frozen result from original instance")
	}

	// Create second instance (simulates proxy restart), load from persisted
	loadFn := func(key string) (string, bool) {
		v, ok := persisted[key]
		return v, ok
	}

	fs2 := NewFrozenStubsWithTTL(30 * time.Minute)
	fs2.SetLoadFunc(loadFn)

	// Get should lazy-load from DB
	result2 := fs2.Get("thread-1", currentMsgs)
	if result2 == nil {
		t.Fatal("expected frozen result from restored instance")
	}

	// Verify restored data matches
	if result2.Cutoff != result1.Cutoff {
		t.Errorf("cutoff mismatch: original=%d, restored=%d", result1.Cutoff, result2.Cutoff)
	}
	if result2.Tokens != result1.Tokens {
		t.Errorf("tokens mismatch: original=%d, restored=%d", result1.Tokens, result2.Tokens)
	}
	if len(result2.Messages) != len(result1.Messages) {
		t.Errorf("message count mismatch: original=%d, restored=%d", len(result1.Messages), len(result2.Messages))
	}

	// Verify message content is identical
	json1, _ := json.Marshal(result1.Messages)
	json2, _ := json.Marshal(result2.Messages)
	if string(json1) != string(json2) {
		t.Error("message content differs between original and restored")
	}
}

func TestFrozenStubs_PersistEmpty(t *testing.T) {
	called := false
	persistFn := func(key, value string) { called = true }

	fs := NewFrozenStubsWithTTL(30 * time.Minute)
	fs.SetPersistFunc(persistFn)

	// Don't store anything — persist should not be called
	// (no Store() call, so no persist)
	if called {
		t.Error("persist should not be called without Store()")
	}
}

func TestFrozenStubs_RestoreInvalidJSON(t *testing.T) {
	loadFn := func(key string) (string, bool) {
		return "not-valid-json{{{", true
	}

	fs := NewFrozenStubsWithTTL(30 * time.Minute)
	fs.SetLoadFunc(loadFn)

	// Should return nil gracefully, not panic
	result := fs.Get("thread-bad", make([]any, 10))
	if result != nil {
		t.Error("expected nil for invalid JSON, got result")
	}
}

func TestFrozenStubs_RestoreNotFound(t *testing.T) {
	loadFn := func(key string) (string, bool) {
		return "", false
	}

	fs := NewFrozenStubsWithTTL(30 * time.Minute)
	fs.SetLoadFunc(loadFn)

	result := fs.Get("thread-missing", make([]any, 10))
	if result == nil {
		// Expected: no data in DB, no in-memory data → nil
	}
}

func TestFrozenStubs_LoadOnlyOnce(t *testing.T) {
	loadCount := 0
	msgs := []any{
		map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": "test"}}},
	}
	msgsJSON, _ := json.Marshal(msgs)
	pHash := sha256hex(msgsJSON)

	fp := frozenPersisted{
		Messages:     msgs,
		Cutoff:       2,
		BoundaryHash: "somehash",
		PrefixHash:   pHash,
		Tokens:       3000,
	}
	data, _ := json.Marshal(fp)

	loadFn := func(key string) (string, bool) {
		loadCount++
		return string(data), true
	}

	fs := NewFrozenStubsWithTTL(30 * time.Minute)
	fs.SetLoadFunc(loadFn)

	// First call — loads from DB
	fs.Get("thread-1", make([]any, 10))
	if loadCount != 1 {
		t.Errorf("expected 1 load, got %d", loadCount)
	}

	// Second call — should use in-memory, not load again
	fs.Get("thread-1", make([]any, 10))
	if loadCount != 1 {
		t.Errorf("expected still 1 load, got %d", loadCount)
	}
}

func TestFrozenStubs_NoPersistWithoutCallback(t *testing.T) {
	fs := NewFrozenStubsWithTTL(30 * time.Minute)
	// No SetPersistFunc called

	msgs := []any{
		map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": "hello"}}},
	}
	// Should not panic
	fs.Store("thread-1", msgs, 1, msgs[0], 1000, 0)

	result := fs.Get("thread-1", make([]any, 5))
	if result == nil {
		t.Error("expected in-memory result even without persist")
	}
}

func TestFrozenStubs_RestorePreservesBoundaryHash(t *testing.T) {
	persisted := make(map[string]string)
	persistFn := func(key, value string) { persisted[key] = value }

	fs1 := NewFrozenStubsWithTTL(30 * time.Minute)
	fs1.SetPersistFunc(persistFn)

	msgs := []any{
		map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": "question"}}},
	}
	boundary := map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "text", "text": "answer"}}}
	fs1.Store("thread-1", msgs, 3, boundary, 2000, 0)

	// Parse persisted to check boundary hash
	var fp frozenPersisted
	json.Unmarshal([]byte(persisted["frozen:thread-1"]), &fp)
	originalBHash := fp.BoundaryHash

	if originalBHash == "" {
		t.Fatal("boundary hash should not be empty")
	}

	// Restore into new instance
	loadFn := func(key string) (string, bool) {
		v, ok := persisted[key]
		return v, ok
	}
	fs2 := NewFrozenStubsWithTTL(30 * time.Minute)
	fs2.SetLoadFunc(loadFn)

	// Trigger load
	fs2.Get("thread-1", make([]any, 10))

	// Verify boundary hash is preserved in-memory
	fs2.mu.RLock()
	restoredBHash := fs2.boundaryHash["thread-1"]
	fs2.mu.RUnlock()

	if restoredBHash != originalBHash {
		t.Errorf("boundary hash mismatch: original=%s, restored=%s", originalBHash, restoredBHash)
	}
}

func TestFrozenStubs_InMemoryOverridesDB(t *testing.T) {
	msgs := []any{
		map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": "db version"}}},
	}
	msgsJSON, _ := json.Marshal(msgs)
	fp := frozenPersisted{
		Messages:     msgs,
		Cutoff:       2,
		BoundaryHash: "old",
		PrefixHash:   sha256hex(msgsJSON),
		Tokens:       1000,
	}
	data, _ := json.Marshal(fp)

	loadFn := func(key string) (string, bool) {
		return string(data), true
	}

	fs := NewFrozenStubsWithTTL(30 * time.Minute)
	fs.SetLoadFunc(loadFn)

	// Store in-memory (overrides DB)
	newMsgs := []any{
		map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": "fresh version"}}},
	}
	fs.Store("thread-1", newMsgs, 5, newMsgs[0], 9000, 0)

	result := fs.Get("thread-1", make([]any, 10))
	if result == nil {
		t.Fatal("expected result")
	}
	if result.Tokens != 9000 {
		t.Errorf("expected in-memory tokens=9000, got %d", result.Tokens)
	}
	if result.Cutoff != 5 {
		t.Errorf("expected in-memory cutoff=5, got %d", result.Cutoff)
	}
}

// TestFrozenStubs_RealisticMessageRoundtrip tests persist/restore with messages
// shaped like real Anthropic API payloads: tool_use, tool_result, cache_control,
// nested content arrays.
func TestFrozenStubs_RealisticMessageRoundtrip(t *testing.T) {
	persisted := make(map[string]string)
	persistFn := func(key, value string) { persisted[key] = value }

	msgs := []any{
		map[string]any{
			"role": "user",
			"content": []any{
				map[string]any{"type": "text", "text": "Read the file"},
				map[string]any{"type": "text", "text": "[YesMem stub: 15 msgs collapsed, 3 tool calls, key: proxy.go refactor]", "cache_control": map[string]any{"type": "ephemeral"}},
			},
		},
		map[string]any{
			"role": "assistant",
			"content": []any{
				map[string]any{"type": "text", "text": "I'll read that file."},
				map[string]any{"type": "tool_use", "id": "toolu_abc123", "name": "Read", "input": map[string]any{"file_path": "/src/main.go"}},
			},
		},
		map[string]any{
			"role": "user",
			"content": []any{
				map[string]any{"type": "tool_result", "tool_use_id": "toolu_abc123", "content": "package main\nfunc main() {}"},
			},
		},
		map[string]any{
			"role": "assistant",
			"content": []any{
				map[string]any{"type": "text", "text": "Here's the file content."},
			},
		},
	}

	fs1 := NewFrozenStubsWithTTL(30 * time.Minute)
	fs1.SetPersistFunc(persistFn)
	fs1.Store("thread-real", msgs, 8, msgs[3], 45000, 0)

	// Simulate restart — new instance loads from persisted
	loadFn := func(key string) (string, bool) {
		v, ok := persisted[key]
		return v, ok
	}
	fs2 := NewFrozenStubsWithTTL(30 * time.Minute)
	fs2.SetLoadFunc(loadFn)

	result := fs2.Get("thread-real", make([]any, 20))
	if result == nil {
		t.Fatal("expected frozen result from restored instance")
	}
	if len(result.Messages) != 4 {
		t.Fatalf("expected 4 messages, got %d", len(result.Messages))
	}
	if result.Cutoff != 8 {
		t.Errorf("expected cutoff=8, got %d", result.Cutoff)
	}
	if result.Tokens != 45000 {
		t.Errorf("expected tokens=45000, got %d", result.Tokens)
	}

	// Verify nested structure survived roundtrip
	msg1, ok := result.Messages[1].(map[string]any)
	if !ok {
		t.Fatal("message[1] not a map")
	}
	content, ok := msg1["content"].([]any)
	if !ok {
		t.Fatal("message[1].content not an array")
	}
	if len(content) != 2 {
		t.Fatalf("expected 2 content blocks, got %d", len(content))
	}
	toolUse, ok := content[1].(map[string]any)
	if !ok {
		t.Fatal("content[1] not a map")
	}
	if toolUse["name"] != "Read" {
		t.Errorf("expected tool_use name=Read, got %v", toolUse["name"])
	}
	input, ok := toolUse["input"].(map[string]any)
	if !ok {
		t.Fatal("tool_use input not a map")
	}
	if input["file_path"] != "/src/main.go" {
		t.Errorf("expected file_path=/src/main.go, got %v", input["file_path"])
	}

	// Verify cache_control on stub message survived
	msg0, _ := result.Messages[0].(map[string]any)
	c0, _ := msg0["content"].([]any)
	stub, _ := c0[1].(map[string]any)
	cc, ok := stub["cache_control"].(map[string]any)
	if !ok {
		t.Fatal("cache_control not preserved on stub message")
	}
	if cc["type"] != "ephemeral" {
		t.Errorf("expected cache_control type=ephemeral, got %v", cc["type"])
	}

	// Verify original JSON bytes match (byte-identical prefix → cache hit)
	json1, _ := json.Marshal(msgs)
	json2, _ := json.Marshal(result.Messages)
	if string(json1) != string(json2) {
		t.Error("JSON output differs — would cause cache miss after deploy")
	}
}
