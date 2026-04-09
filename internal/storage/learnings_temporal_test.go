package storage

import (
	"testing"
	"time"

	"github.com/carsteneu/yesmem/internal/models"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open test store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func insertTestLearning(s *Store, content string, category ...string) int64 {
	cat := "gotcha"
	if len(category) > 0 {
		cat = category[0]
	}
	id, err := s.InsertLearning(&models.Learning{
		SessionID: "test-session", Category: cat, Content: content,
		Confidence: 1.0, CreatedAt: time.Now(), ModelUsed: "test",
		Source: "llm_extracted",
	})
	if err != nil {
		panic(err)
	}
	return id
}

func TestSupersedeSetValidUntil(t *testing.T) {
	s := newTestStore(t)
	id1 := insertTestLearning(s, "old truth")
	id2 := insertTestLearning(s, "new truth")

	if err := s.SupersedeLearning(id1, id2, "updated"); err != nil {
		t.Fatalf("supersede: %v", err)
	}

	l1, err := s.GetLearning(id1)
	if err != nil {
		t.Fatalf("get old: %v", err)
	}
	if l1.ValidUntil == nil {
		t.Error("expected valid_until to be set on superseded learning")
	}
	if l1.SupersededBy == nil || *l1.SupersededBy != id2 {
		t.Errorf("expected superseded_by=%d, got %v", id2, l1.SupersededBy)
	}

	// Check backlink
	l2, err := s.GetLearning(id2)
	if err != nil {
		t.Fatalf("get new: %v", err)
	}
	if l2.Supersedes == nil || *l2.Supersedes != id1 {
		t.Errorf("expected supersedes=%d, got %v", id1, l2.Supersedes)
	}
	if l2.ValidUntil != nil {
		t.Error("active learning should not have valid_until")
	}
}

func TestBulkSupersedeReturnsIDs(t *testing.T) {
	s := newTestStore(t)

	// Insert 3 narratives for a session
	for i := 0; i < 3; i++ {
		s.InsertLearning(&models.Learning{
			SessionID: "narr-session", Category: "narrative", Content: "narrative text",
			Confidence: 1.0, CreatedAt: time.Now(), ModelUsed: "test",
			Source: "llm_extracted",
		})
	}

	ids, err := s.SupersedeNarrativesBySession("narr-session", "new narrative")
	if err != nil {
		t.Fatalf("supersede narratives: %v", err)
	}
	if len(ids) != 3 {
		t.Errorf("expected 3 IDs, got %d", len(ids))
	}

	// Verify valid_until is set on all
	for _, id := range ids {
		l, _ := s.GetLearning(id)
		if l.ValidUntil == nil {
			t.Errorf("learning %d missing valid_until", id)
		}
	}
}

func TestSupersededChain(t *testing.T) {
	s := newTestStore(t)
	id1 := insertTestLearning(s, "v1")
	id2 := insertTestLearning(s, "v2")
	id3 := insertTestLearning(s, "v3")

	s.SupersedeLearning(id1, id2, "update 1")
	s.SupersedeLearning(id2, id3, "update 2")

	chain, err := s.GetSupersededChain(id3, 10)
	if err != nil {
		t.Fatalf("chain: %v", err)
	}
	if len(chain) != 3 {
		t.Fatalf("expected chain length 3, got %d", len(chain))
	}
	if chain[0].Content != "v3" {
		t.Errorf("expected chain[0]=v3, got %s", chain[0].Content)
	}
	if chain[2].Content != "v1" {
		t.Errorf("expected chain[2]=v1, got %s", chain[2].Content)
	}
}

func TestVolatility(t *testing.T) {
	s := newTestStore(t)
	id1 := insertTestLearning(s, "v1")
	id2 := insertTestLearning(s, "v2")
	id3 := insertTestLearning(s, "v3")

	s.SupersedeLearning(id1, id2, "update 1")
	s.SupersedeLearning(id2, id3, "update 2")

	vol, err := s.GetVolatility(id3)
	if err != nil {
		t.Fatalf("volatility: %v", err)
	}
	if vol != 2 {
		t.Errorf("expected volatility=2, got %d", vol)
	}

	// Single learning — no volatility
	single := insertTestLearning(s, "standalone")
	vol, _ = s.GetVolatility(single)
	if vol != 0 {
		t.Errorf("expected volatility=0 for standalone, got %d", vol)
	}
}

func TestCleanupJunkReturnsIDs(t *testing.T) {
	s := newTestStore(t)

	// Insert junk (too short)
	s.InsertLearning(&models.Learning{
		SessionID: "junk", Category: "gotcha", Content: "ab",
		Confidence: 1.0, CreatedAt: time.Now(), ModelUsed: "test",
	})
	// Insert valid
	s.InsertLearning(&models.Learning{
		SessionID: "valid", Category: "gotcha", Content: "This is a valid learning with enough content",
		Confidence: 1.0, CreatedAt: time.Now(), ModelUsed: "test",
	})

	ids, err := s.CleanupJunkLearnings()
	if err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if len(ids) != 1 {
		t.Errorf("expected 1 junk ID, got %d", len(ids))
	}

	// Verify valid_until set
	if len(ids) > 0 {
		l, _ := s.GetLearning(ids[0])
		if l.ValidUntil == nil {
			t.Error("junk learning should have valid_until after cleanup")
		}
	}
}

func TestRememberWithSupersedes(t *testing.T) {
	s := newTestStore(t)
	oldID := insertTestLearning(s, "Go binary at /usr/local/go/bin/go")

	// Simulate remember() with supersedes
	supersedes := oldID
	newID, err := s.InsertLearning(&models.Learning{
		SessionID: "test", Category: "gotcha", Content: "Go binary at /home/user/memory/go-sdk/go/bin/go",
		Confidence: 1.0, CreatedAt: time.Now(), ModelUsed: "self",
		Source: "user_stated", Supersedes: &supersedes,
	})
	if err != nil {
		t.Fatalf("insert with supersedes: %v", err)
	}

	// Manually supersede old (handler does this in handleRemember)
	s.SupersedeLearning(oldID, newID, "superseded by remember()")

	// Verify chain
	chain, _ := s.GetSupersededChain(newID, 10)
	if len(chain) != 2 {
		t.Fatalf("expected chain length 2, got %d", len(chain))
	}

	// New learning has supersedes set from INSERT
	newL, _ := s.GetLearning(newID)
	if newL.Supersedes == nil || *newL.Supersedes != oldID {
		t.Errorf("expected supersedes=%d from INSERT, got %v", oldID, newL.Supersedes)
	}
}
