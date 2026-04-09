package proxy

import (
	"encoding/json"
	"testing"
)

func TestDecayTracker_PersistAndRestore(t *testing.T) {
	persisted := make(map[string]string)
	persistFn := func(key, value string) { persisted[key] = value }

	dt1 := NewDecayTracker()
	dt1.SetPersistFunc(persistFn)

	dt1.MarkStubbed(5, 10, 0.8)
	dt1.MarkStubbed(7, 15, 0.3)
	dt1.SetFilePath(5, "main.go")

	dt1.Persist("thread-1")

	val, ok := persisted["decay:thread-1"]
	if !ok {
		t.Fatal("expected persist callback with key 'decay:thread-1'")
	}

	// Verify JSON structure
	var dp decayPersisted
	if err := json.Unmarshal([]byte(val), &dp); err != nil {
		t.Fatalf("persisted value is not valid JSON: %v", err)
	}
	if len(dp.StubbedAt) != 2 {
		t.Errorf("expected 2 stubbedAt entries, got %d", len(dp.StubbedAt))
	}
	if dp.StubbedAt["msg_5"] != 10 {
		t.Errorf("expected msg_5 stubbedAt=10, got %d", dp.StubbedAt["msg_5"])
	}
	if dp.Intensity["msg_5"] != 0.8 {
		t.Errorf("expected msg_5 intensity=0.8, got %f", dp.Intensity["msg_5"])
	}
	if dp.FilePaths["msg_5"] != "main.go" {
		t.Errorf("expected msg_5 filePath=main.go, got %s", dp.FilePaths["msg_5"])
	}

	// Restore into new instance
	loadFn := func(key string) (string, bool) {
		v, ok := persisted[key]
		return v, ok
	}

	dt2 := NewDecayTracker()
	dt2.SetLoadFunc(loadFn)
	dt2.LoadFromDB("thread-1")

	// Verify stages match
	stage1 := dt1.GetStage(5, 14, 100, 2.5)
	stage2 := dt2.GetStage(5, 14, 100, 2.5)
	if stage1 != stage2 {
		t.Errorf("stage mismatch for msg_5: original=%d, restored=%d", stage1, stage2)
	}

	stage1 = dt1.GetStage(7, 20, 100, 2.5)
	stage2 = dt2.GetStage(7, 20, 100, 2.5)
	if stage1 != stage2 {
		t.Errorf("stage mismatch for msg_7: original=%d, restored=%d", stage1, stage2)
	}
}

func TestDecayTracker_RestoreInvalidJSON(t *testing.T) {
	loadFn := func(key string) (string, bool) {
		return "broken{json", true
	}

	dt := NewDecayTracker()
	dt.SetLoadFunc(loadFn)

	// Should not panic
	dt.LoadFromDB("thread-bad")

	// Should return stage 0 (no data)
	stage := dt.GetStage(5, 100, 100, 2.5)
	if stage != DecayStage0 {
		t.Errorf("expected stage 0 for untracked message, got %d", stage)
	}
}

func TestDecayTracker_RestoreNotFound(t *testing.T) {
	loadFn := func(key string) (string, bool) {
		return "", false
	}

	dt := NewDecayTracker()
	dt.SetLoadFunc(loadFn)
	dt.LoadFromDB("thread-missing")

	// No-op, no panic
}

func TestDecayTracker_LoadOnlyOnce(t *testing.T) {
	loadCount := 0
	loadFn := func(key string) (string, bool) {
		loadCount++
		return `{"stubbed_at":{"msg_1":5},"intensity":{"msg_1":0.5},"file_paths":{}}`, true
	}

	dt := NewDecayTracker()
	dt.SetLoadFunc(loadFn)

	dt.LoadFromDB("thread-1")
	if loadCount != 1 {
		t.Errorf("expected 1 load, got %d", loadCount)
	}

	dt.LoadFromDB("thread-1")
	if loadCount != 1 {
		t.Errorf("expected still 1 load, got %d", loadCount)
	}
}

func TestDecayTracker_NoPersistWithoutCallback(t *testing.T) {
	dt := NewDecayTracker()
	dt.MarkStubbed(5, 10, 0.5)

	// Should not panic
	dt.Persist("thread-1")
}
