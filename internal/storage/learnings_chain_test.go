package storage

import (
	"testing"
	"time"

	"github.com/carsteneu/yesmem/internal/models"
)

// TestGetLearningChain_SelfCycleBackward verifies that a self-loop in the
// supersedes column (supersedes = id) does not cause infinite recursion.
func TestGetLearningChain_SelfCycleBackward(t *testing.T) {
	s := mustOpen(t)

	id, err := s.InsertLearning(&models.Learning{
		Category: "decision", Content: "self-back", Confidence: 1.0,
		CreatedAt: time.Now(), ModelUsed: "opus",
	})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	// Synthesize the production bug: self-loop in supersedes column.
	if _, err := s.DB().Exec("UPDATE learnings SET supersedes = id WHERE id = ?", id); err != nil {
		t.Fatalf("seed self-cycle: %v", err)
	}

	done := make(chan struct{})
	var chain []models.Learning
	var chainErr error
	go func() {
		defer close(done)
		chain, chainErr = s.GetLearningChain(id)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("GetLearningChain did not terminate on backward self-cycle (infinite loop)")
	}
	if chainErr != nil {
		t.Fatalf("GetLearningChain: %v", chainErr)
	}
	if len(chain) != 1 {
		t.Errorf("expected 1 learning (self only), got %d", len(chain))
	}
}

// TestGetLearningChain_SelfCycleForward verifies that a self-loop in the
// superseded_by column does not cause infinite recursion.
func TestGetLearningChain_SelfCycleForward(t *testing.T) {
	s := mustOpen(t)

	id, err := s.InsertLearning(&models.Learning{
		Category: "decision", Content: "self-fwd", Confidence: 1.0,
		CreatedAt: time.Now(), ModelUsed: "opus",
	})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	if _, err := s.DB().Exec("UPDATE learnings SET superseded_by = id WHERE id = ?", id); err != nil {
		t.Fatalf("seed self-cycle: %v", err)
	}

	done := make(chan struct{})
	var chain []models.Learning
	var chainErr error
	go func() {
		defer close(done)
		chain, chainErr = s.GetLearningChain(id)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("GetLearningChain did not terminate on forward self-cycle (infinite loop)")
	}
	if chainErr != nil {
		t.Fatalf("GetLearningChain: %v", chainErr)
	}
	if len(chain) != 1 {
		t.Errorf("expected 1 learning (self only), got %d", len(chain))
	}
}

// TestGetLearningChain_LongCycle verifies that a non-self cycle (A→B→C→A)
// also terminates with a de-duplicated chain.
func TestGetLearningChain_LongCycle(t *testing.T) {
	s := mustOpen(t)

	idA, _ := s.InsertLearning(&models.Learning{Category: "decision", Content: "A", Confidence: 1.0, CreatedAt: time.Now(), ModelUsed: "opus"})
	idB, _ := s.InsertLearning(&models.Learning{Category: "decision", Content: "B", Confidence: 1.0, CreatedAt: time.Now(), ModelUsed: "opus"})
	idC, _ := s.InsertLearning(&models.Learning{Category: "decision", Content: "C", Confidence: 1.0, CreatedAt: time.Now(), ModelUsed: "opus"})
	// Build a 3-cycle: A.supersedes=B, B.supersedes=C, C.supersedes=A
	// (and matching superseded_by backlinks so both directions see the cycle).
	if _, err := s.DB().Exec("UPDATE learnings SET supersedes = ?, superseded_by = ? WHERE id = ?", idB, idC, idA); err != nil {
		t.Fatalf("seed A: %v", err)
	}
	if _, err := s.DB().Exec("UPDATE learnings SET supersedes = ?, superseded_by = ? WHERE id = ?", idC, idA, idB); err != nil {
		t.Fatalf("seed B: %v", err)
	}
	if _, err := s.DB().Exec("UPDATE learnings SET supersedes = ?, superseded_by = ? WHERE id = ?", idA, idB, idC); err != nil {
		t.Fatalf("seed C: %v", err)
	}

	done := make(chan struct{})
	var chain []models.Learning
	var chainErr error
	go func() {
		defer close(done)
		chain, chainErr = s.GetLearningChain(idA)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("GetLearningChain did not terminate on 3-cycle (infinite loop)")
	}
	if chainErr != nil {
		t.Fatalf("GetLearningChain: %v", chainErr)
	}
	seen := map[int64]bool{}
	for _, l := range chain {
		if seen[l.ID] {
			t.Errorf("id %d appeared twice in chain — not de-duplicated", l.ID)
		}
		seen[l.ID] = true
	}
	if len(chain) != 3 {
		t.Errorf("expected 3 unique learnings in cycle, got %d", len(chain))
	}
}

// TestGetLearningChain_MutualTwoCycle verifies that a 2-cycle (A↔B) also
// terminates. This is the one topology where the break happens on the first
// revisit without an intermediate node — distinct from self-loops and longer
// cycles.
func TestGetLearningChain_MutualTwoCycle(t *testing.T) {
	s := mustOpen(t)

	idA, _ := s.InsertLearning(&models.Learning{Category: "decision", Content: "A", Confidence: 1.0, CreatedAt: time.Now(), ModelUsed: "opus"})
	idB, _ := s.InsertLearning(&models.Learning{Category: "decision", Content: "B", Confidence: 1.0, CreatedAt: time.Now(), ModelUsed: "opus"})
	// A.supersedes=B, B.supersedes=A (and matching superseded_by backlinks).
	s.DB().Exec("UPDATE learnings SET supersedes = ?, superseded_by = ? WHERE id = ?", idB, idB, idA)
	s.DB().Exec("UPDATE learnings SET supersedes = ?, superseded_by = ? WHERE id = ?", idA, idA, idB)

	done := make(chan struct{})
	var chain []models.Learning
	var chainErr error
	go func() {
		defer close(done)
		chain, chainErr = s.GetLearningChain(idA)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("GetLearningChain did not terminate on mutual 2-cycle (infinite loop)")
	}
	if chainErr != nil {
		t.Fatalf("GetLearningChain: %v", chainErr)
	}
	seen := map[int64]bool{}
	for _, l := range chain {
		if seen[l.ID] {
			t.Errorf("id %d appeared twice in chain — not de-duplicated", l.ID)
		}
		seen[l.ID] = true
	}
	if len(chain) != 2 {
		t.Errorf("expected 2 unique learnings in 2-cycle, got %d", len(chain))
	}
}

// TestGetLearningChain_NormalChainSanity verifies that the cycle-detection
// change does not break the normal case (linear 3-deep chain, no cycles).
func TestGetLearningChain_NormalChainSanity(t *testing.T) {
	s := mustOpen(t)

	id1, _ := s.InsertLearning(&models.Learning{Category: "decision", Content: "v1", Confidence: 1.0, CreatedAt: time.Now(), ModelUsed: "opus"})
	id2, _ := s.InsertLearning(&models.Learning{Category: "decision", Content: "v2", Confidence: 1.0, CreatedAt: time.Now(), ModelUsed: "opus"})
	id3, _ := s.InsertLearning(&models.Learning{Category: "decision", Content: "v3", Confidence: 1.0, CreatedAt: time.Now(), ModelUsed: "opus"})
	s.SupersedeLearning(id1, id2, "v2")
	s.SupersedeLearning(id2, id3, "v3")

	chain, err := s.GetLearningChain(id2)
	if err != nil {
		t.Fatalf("GetLearningChain: %v", err)
	}
	if len(chain) != 3 {
		t.Fatalf("expected 3 learnings in linear chain, got %d", len(chain))
	}
	if chain[0].ID != id1 || chain[1].ID != id2 || chain[2].ID != id3 {
		t.Errorf("chain order = [%d,%d,%d], want [%d,%d,%d] (oldest-first)",
			chain[0].ID, chain[1].ID, chain[2].ID, id1, id2, id3)
	}
}

// TestSupersedeLearningBatch_SelfLoopGuard verifies that calling
// SupersedeLearningBatch with id == supersededByID is rejected (no self-loop
// is written to the DB).
func TestSupersedeLearningBatch_SelfLoopGuard(t *testing.T) {
	s := mustOpen(t)

	id, err := s.InsertLearning(&models.Learning{
		Category: "decision", Content: "candidate self-supersede",
		Confidence: 1.0, CreatedAt: time.Now(), ModelUsed: "opus",
	})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	// Attempt to self-supersede — must be rejected silently (no-op) or sanitized.
	if err := s.SupersedeLearningBatch([]int64{id}, id, "self-loop attempt"); err != nil {
		t.Fatalf("SupersedeLearningBatch self-loop: unexpected err %v", err)
	}

	var supersedes, supersededBy *int64
	row := s.DB().QueryRow("SELECT supersedes, superseded_by FROM learnings WHERE id = ?", id)
	if err := row.Scan(&supersedes, &supersededBy); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if supersedes != nil && *supersedes == id {
		t.Errorf("supersedes == id: self-loop was written (supersedes column)")
	}
	if supersededBy != nil && *supersededBy == id {
		t.Errorf("superseded_by == id: self-loop was written (superseded_by column)")
	}
}

// TestMigration_CleansExistingSelfCycles verifies that createSchema's
// migration list nulls out any pre-existing self-cycles in both supersedes
// and superseded_by columns. This mirrors the production scenario where
// rows with supersedes=id / superseded_by=id already exist.
func TestMigration_CleansExistingSelfCycles(t *testing.T) {
	s := mustOpen(t)

	// Insert two clean learnings, then poison them with self-cycles
	// exactly as observed in production (Learning IDs 22780, 33084, ...).
	id1, _ := s.InsertLearning(&models.Learning{Category: "decision", Content: "poisoned-1", Confidence: 1.0, CreatedAt: time.Now(), ModelUsed: "opus"})
	id2, _ := s.InsertLearning(&models.Learning{Category: "decision", Content: "poisoned-2", Confidence: 1.0, CreatedAt: time.Now(), ModelUsed: "opus"})
	if _, err := s.DB().Exec("UPDATE learnings SET supersedes = id WHERE id = ?", id1); err != nil {
		t.Fatalf("poison 1 (supersedes): %v", err)
	}
	if _, err := s.DB().Exec("UPDATE learnings SET superseded_by = id WHERE id = ?", id2); err != nil {
		t.Fatalf("poison 2 (superseded_by): %v", err)
	}

	// Re-run createSchema — the migration should clean up the self-cycles.
	if err := s.createSchema(); err != nil {
		t.Fatalf("re-run createSchema: %v", err)
	}

	var sup1, supby1, sup2, supby2 *int64
	s.DB().QueryRow("SELECT supersedes, superseded_by FROM learnings WHERE id = ?", id1).Scan(&sup1, &supby1)
	s.DB().QueryRow("SELECT supersedes, superseded_by FROM learnings WHERE id = ?", id2).Scan(&sup2, &supby2)
	if sup1 != nil && *sup1 == id1 {
		t.Errorf("id1 supersedes == id1: migration did not clean self-cycle (supersedes column)")
	}
	if sup2 != nil && *sup2 == id2 {
		t.Errorf("id2 supersedes == id2: migration did not clean self-cycle (supersedes column)")
	}
	if supby1 != nil && *supby1 == id1 {
		t.Errorf("id1 superseded_by == id1: migration did not clean self-cycle (superseded_by column)")
	}
	if supby2 != nil && *supby2 == id2 {
		t.Errorf("id2 superseded_by == id2: migration did not clean self-cycle (superseded_by column)")
	}

	// Upgrade-path idempotency: re-running createSchema on an already-cleaned DB
	// (as happens on every daemon restart) must be a no-op — rows that no longer
	// carry self-cycles must stay clean, and the migration must not error.
	if err := s.createSchema(); err != nil {
		t.Fatalf("second createSchema (upgrade-path idempotency): %v", err)
	}
	s.DB().QueryRow("SELECT supersedes, superseded_by FROM learnings WHERE id = ?", id1).Scan(&sup1, &supby1)
	s.DB().QueryRow("SELECT supersedes, superseded_by FROM learnings WHERE id = ?", id2).Scan(&sup2, &supby2)
	if (sup1 != nil && *sup1 == id1) || (sup2 != nil && *sup2 == id2) ||
		(supby1 != nil && *supby1 == id1) || (supby2 != nil && *supby2 == id2) {
		t.Errorf("self-cycle reappeared after idempotent re-run — migration not stable")
	}
}

func TestMigration_CleansReactivatedSupersedeMetadata(t *testing.T) {
	s := mustOpen(t)
	id, _ := s.InsertLearning(&models.Learning{Category: "pattern", Content: "reactivated", Confidence: 1, CreatedAt: time.Now(), ModelUsed: "test"})
	if _, err := s.DB().Exec(`UPDATE learnings SET superseded_by = id, supersede_reason = 'rule-based: cross-chunk near-duplicate', valid_until = datetime('now') WHERE id = ?`, id); err != nil {
		t.Fatalf("seed self-cycle: %v", err)
	}

	if err := s.createSchema(); err != nil {
		t.Fatalf("run migrations: %v", err)
	}

	var supersededBy *int64
	var reason, validUntil *string
	if err := s.DB().QueryRow("SELECT superseded_by, supersede_reason, valid_until FROM learnings WHERE id = ?", id).Scan(&supersededBy, &reason, &validUntil); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if supersededBy != nil || reason != nil || validUntil != nil {
		t.Fatalf("reactivated learning kept stale metadata: superseded_by=%v reason=%v valid_until=%v", supersededBy, reason, validUntil)
	}
}

func TestMigration_ReactivatesCrossProjectRuleBasedSupersede(t *testing.T) {
	s := mustOpen(t)
	loser, _ := s.InsertLearning(&models.Learning{Category: "pattern", Content: "same content in alpha", Project: "/work/alpha", CanonicalProject: "alpha", Confidence: 1, CreatedAt: time.Now(), ModelUsed: "test"})
	winner, _ := s.InsertLearning(&models.Learning{Category: "pattern", Content: "same content in beta", Project: "/work/beta", CanonicalProject: "beta", Confidence: 1, CreatedAt: time.Now(), ModelUsed: "test"})
	if err := s.SupersedeLearning(loser, winner, "rule-based: near-duplicate"); err != nil {
		t.Fatalf("seed cross-project supersede: %v", err)
	}

	if err := s.createSchema(); err != nil {
		t.Fatalf("run migrations: %v", err)
	}

	var supersededBy *int64
	var reason, validUntil *string
	if err := s.DB().QueryRow("SELECT superseded_by, supersede_reason, valid_until FROM learnings WHERE id = ?", loser).Scan(&supersededBy, &reason, &validUntil); err != nil {
		t.Fatalf("scan loser: %v", err)
	}
	if supersededBy != nil || reason != nil || validUntil != nil {
		t.Fatalf("cross-project learning was not restored: superseded_by=%v reason=%v valid_until=%v", supersededBy, reason, validUntil)
	}
	var supersedes *int64
	if err := s.DB().QueryRow("SELECT supersedes FROM learnings WHERE id = ?", winner).Scan(&supersedes); err != nil {
		t.Fatalf("scan winner: %v", err)
	}
	if supersedes != nil {
		t.Fatalf("winner kept stale backlink to restored learning: %d", *supersedes)
	}
}
