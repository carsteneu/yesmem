package daemon

import (
	"database/sql"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/carsteneu/yesmem/internal/extraction"
	"github.com/carsteneu/yesmem/internal/models"
	"github.com/carsteneu/yesmem/internal/storage"
)

func TestConsolidationRunnerPersistsBaselineAndCoalescesConcurrentRuns(t *testing.T) {
	store, err := storage.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	insert := func(content string) {
		if _, err := store.InsertLearning(&models.Learning{Category: "pattern", Content: content, Project: "test", Confidence: 1, CreatedAt: time.Now(), ModelUsed: "test"}); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}
	insert("baseline learning with enough words to remain substantive")
	runner := newConsolidationRunner(store)
	if err := runner.Baseline(); err != nil {
		t.Fatalf("baseline: %v", err)
	}
	insert("new learning with enough words to trigger incremental consolidation")

	var calls atomic.Int32
	runner.run = func(_ *storage.Store, _ *extraction.Extractor, _ func(int64), cfg extraction.ConsolidateConfig) extraction.ConsolidateResult {
		calls.Add(1)
		time.Sleep(10 * time.Millisecond)
		return extraction.ConsolidateResult{HighWatermark: cfg.ThroughID}
	}
	var group sync.WaitGroup
	for range 8 {
		group.Add(1)
		go func() {
			defer group.Done()
			if _, _, err := runner.RunIfDirty(); err != nil {
				t.Errorf("run if dirty: %v", err)
			}
		}()
	}
	group.Wait()

	if calls.Load() != 1 {
		t.Fatalf("expected one serialized run, got %d", calls.Load())
	}
	restarted := newConsolidationRunner(store)
	restarted.run = runner.run
	if _, ran, err := restarted.RunIfDirty(); err != nil || ran {
		t.Fatalf("persisted watermark was not reused after restart: ran=%v err=%v", ran, err)
	}
}

func TestConsolidationRunnerPersistsWatermarkAcrossStoreReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "yesmem.db")
	store, err := storage.Open(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if _, err := store.InsertLearning(&models.Learning{Category: "pattern", Content: "existing baseline learning with enough substantive words", Confidence: 1, CreatedAt: time.Now(), ModelUsed: "test"}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := newConsolidationRunner(store).Baseline(); err != nil {
		t.Fatalf("baseline: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	reopened, err := storage.Open(path)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	t.Cleanup(func() { reopened.Close() })
	if _, ran, err := newConsolidationRunner(reopened).RunIfDirty(); err != nil || ran {
		t.Fatalf("reopened store did not retain watermark: ran=%v err=%v", ran, err)
	}
}

func TestConsolidationRunnerStoresStateWithLearningDatabase(t *testing.T) {
	dir := t.TempDir()
	store, err := storage.Open(filepath.Join(dir, "yesmem.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	if _, err := store.InsertLearning(&models.Learning{Category: "pattern", Content: "existing baseline learning with enough substantive words", Confidence: 1, CreatedAt: time.Now(), ModelUsed: "test"}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := newConsolidationRunner(store).Baseline(); err != nil {
		t.Fatalf("baseline: %v", err)
	}

	var watermark int64
	if err := store.DB().QueryRow(`SELECT last_learning_id FROM consolidation_state WHERE key = ?`, consolidationStateKey).Scan(&watermark); err != nil {
		t.Fatalf("read main-db state: %v", err)
	}
	if watermark != 1 {
		t.Fatalf("main-db watermark = %d, want 1", watermark)
	}

	runtimeDB, err := sql.Open("sqlite", filepath.Join(dir, "runtime.db"))
	if err != nil {
		t.Fatalf("open runtime db: %v", err)
	}
	defer runtimeDB.Close()
	var runtimeRows int
	if err := runtimeDB.QueryRow(`SELECT COUNT(*) FROM proxy_state WHERE key = ?`, consolidationStateKey).Scan(&runtimeRows); err != nil {
		t.Fatalf("read runtime state: %v", err)
	}
	if runtimeRows != 0 {
		t.Fatalf("consolidation state must not remain in runtime.db, got %d rows", runtimeRows)
	}
}

func TestConsolidationRunnerMigratesLegacyRuntimeState(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "yesmem.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	for range 2 {
		if _, err := store.InsertLearning(&models.Learning{Category: "pattern", Content: "existing migration learning with enough substantive words", Confidence: 1, CreatedAt: time.Now(), ModelUsed: "test"}); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}
	if err := store.SetProxyState(consolidationStateKey, `{"last_learning_id":1,"completed_at":"2026-07-21T12:00:00Z"}`); err != nil {
		t.Fatalf("write legacy state: %v", err)
	}

	runner := newConsolidationRunner(store)
	if err := runner.Baseline(); err != nil {
		t.Fatalf("migrate baseline: %v", err)
	}
	state, exists, err := runner.readState()
	if err != nil || !exists {
		t.Fatalf("read migrated state: exists=%v err=%v", exists, err)
	}
	if state.LastLearningID != 1 || state.CompletedAt != "2026-07-21T12:00:00Z" {
		t.Fatalf("legacy state not preserved: %+v", state)
	}
	legacy, err := store.GetProxyState(consolidationStateKey)
	if err != nil {
		t.Fatalf("read legacy state: %v", err)
	}
	if legacy != "" {
		t.Fatalf("legacy runtime state was not removed: %q", legacy)
	}
}

func TestConsolidationRunnerRejectsWatermarkBeyondLearningDatabase(t *testing.T) {
	store, err := storage.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	for range 2 {
		if _, err := store.InsertLearning(&models.Learning{Category: "pattern", Content: "existing regression learning with enough substantive words", Confidence: 1, CreatedAt: time.Now(), ModelUsed: "test"}); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}
	runner := newConsolidationRunner(store)
	if err := runner.Baseline(); err != nil {
		t.Fatalf("baseline: %v", err)
	}
	if _, err := store.DB().Exec(`DELETE FROM learnings WHERE id = 2`); err != nil {
		t.Fatalf("simulate restored database: %v", err)
	}

	_, ran, err := runner.RunIfDirty()
	if err == nil || !strings.Contains(err.Error(), "exceeds max learning ID") {
		t.Fatalf("expected watermark regression error, got ran=%v err=%v", ran, err)
	}
	if ran {
		t.Fatal("regressed database must not run consolidation")
	}
}
