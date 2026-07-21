package daemon

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/carsteneu/yesmem/internal/extraction"
	"github.com/carsteneu/yesmem/internal/storage"
)

const consolidationStateKey = "consolidation:rule_based_state"

type consolidationState = storage.ConsolidationState

type consolidationRunner struct {
	store *storage.Store
	mu    sync.Mutex
	run   func(*storage.Store, *extraction.Extractor, func(int64), extraction.ConsolidateConfig) extraction.ConsolidateResult
}

func newConsolidationRunner(store *storage.Store) *consolidationRunner {
	return &consolidationRunner{store: store, run: extraction.RunConsolidation}
}

// Baseline records the existing dataset once. Automatic consolidation then
// processes only project/category scopes touched by later inserts.
func (r *consolidationRunner) Baseline() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	id, err := r.store.GetMaxLearningID()
	if err != nil {
		return err
	}
	_, exists, err := r.readStateForMax(id)
	if err != nil || exists {
		return err
	}
	return r.writeState(id)
}

// RunIfDirty serializes callers and advances the durable high-watermark only
// after a complete error-free run. Waiting callers observe the new watermark
// and coalesce into a no-op.
func (r *consolidationRunner) RunIfDirty() (extraction.ConsolidateResult, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	throughID, err := r.store.GetMaxLearningID()
	if err != nil {
		return extraction.ConsolidateResult{}, false, err
	}
	state, exists, err := r.readStateForMax(throughID)
	if err != nil {
		return extraction.ConsolidateResult{}, false, err
	}
	if !exists {
		return extraction.ConsolidateResult{HighWatermark: throughID}, false, r.writeState(throughID)
	}
	if throughID <= state.LastLearningID {
		return extraction.ConsolidateResult{HighWatermark: state.LastLearningID}, false, nil
	}
	result := r.run(r.store, nil, nil, extraction.ConsolidateConfig{
		MaxRounds:     2,
		RuleBasedOnly: true,
		AfterID:       state.LastLearningID,
		ThroughID:     throughID,
	})
	if result.Errors > 0 {
		return result, true, fmt.Errorf("incremental consolidation completed with %d errors", result.Errors)
	}
	if err := r.writeState(throughID); err != nil {
		return result, true, err
	}
	return result, true, nil
}

func (r *consolidationRunner) readState() (consolidationState, bool, error) {
	return r.store.GetConsolidationState(consolidationStateKey)
}

func (r *consolidationRunner) readStateForMax(maxLearningID int64) (consolidationState, bool, error) {
	state, exists, err := r.readState()
	if err != nil {
		return consolidationState{}, false, err
	}
	if exists {
		return state, true, validateConsolidationState(state, maxLearningID)
	}

	state, exists, err = r.readLegacyState()
	if err != nil || !exists {
		return state, exists, err
	}
	if err := validateConsolidationState(state, maxLearningID); err != nil {
		return consolidationState{}, false, err
	}
	if err := r.persistState(state); err != nil {
		return consolidationState{}, false, err
	}
	if err := r.store.DeleteProxyState(consolidationStateKey); err != nil {
		return consolidationState{}, false, fmt.Errorf("remove legacy consolidation state: %w", err)
	}
	return state, true, nil
}

func (r *consolidationRunner) readLegacyState() (consolidationState, bool, error) {
	value, err := r.store.GetProxyState(consolidationStateKey)
	if err != nil || value == "" {
		return consolidationState{}, false, err
	}
	var state consolidationState
	if err := json.Unmarshal([]byte(value), &state); err != nil {
		return consolidationState{}, false, fmt.Errorf("decode consolidation state: %w", err)
	}
	return state, true, nil
}

func (r *consolidationRunner) writeState(id int64) error {
	return r.persistState(consolidationState{LastLearningID: id, CompletedAt: time.Now().UTC().Format(time.RFC3339Nano)})
}

func (r *consolidationRunner) persistState(state consolidationState) error {
	return r.store.SetConsolidationState(consolidationStateKey, state)
}

func validateConsolidationState(state consolidationState, maxLearningID int64) error {
	if state.LastLearningID > maxLearningID {
		return fmt.Errorf("consolidation watermark %d exceeds max learning ID %d; learning database may have been restored without matching state", state.LastLearningID, maxLearningID)
	}
	return nil
}
