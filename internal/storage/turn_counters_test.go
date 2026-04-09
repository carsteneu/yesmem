package storage

import (
	"sync"
	"testing"
)

func TestIncrementTurnCount_CreatesNew(t *testing.T) {
	s := newTestStore(t)
	defer s.Close()

	count, err := s.IncrementTurnCount("testproject")
	if err != nil {
		t.Fatalf("IncrementTurnCount: %v", err)
	}
	if count != 1 {
		t.Errorf("expected count=1, got %d", count)
	}
}

func TestIncrementTurnCount_Increments(t *testing.T) {
	s := newTestStore(t)
	defer s.Close()

	s.IncrementTurnCount("proj")
	s.IncrementTurnCount("proj")
	count, err := s.IncrementTurnCount("proj")
	if err != nil {
		t.Fatalf("IncrementTurnCount: %v", err)
	}
	if count != 3 {
		t.Errorf("expected count=3, got %d", count)
	}
}

func TestGetTurnCount_NotFound(t *testing.T) {
	s := newTestStore(t)
	defer s.Close()

	count, err := s.GetTurnCount("nonexistent")
	if err != nil {
		t.Fatalf("GetTurnCount: %v", err)
	}
	if count != 0 {
		t.Errorf("expected count=0 for unknown project, got %d", count)
	}
}

func TestGetTurnCount_AfterIncrement(t *testing.T) {
	s := newTestStore(t)
	defer s.Close()

	s.IncrementTurnCount("proj")
	s.IncrementTurnCount("proj")

	count, err := s.GetTurnCount("proj")
	if err != nil {
		t.Fatalf("GetTurnCount: %v", err)
	}
	if count != 2 {
		t.Errorf("expected count=2, got %d", count)
	}
}

func TestGetTurnCountsBulk(t *testing.T) {
	s := newTestStore(t)
	defer s.Close()

	s.IncrementTurnCount("a")
	s.IncrementTurnCount("a")
	s.IncrementTurnCount("b")

	counts, err := s.GetTurnCountsBulk([]string{"a", "b", "c"})
	if err != nil {
		t.Fatalf("GetTurnCountsBulk: %v", err)
	}
	if counts["a"] != 2 {
		t.Errorf("expected a=2, got %d", counts["a"])
	}
	if counts["b"] != 1 {
		t.Errorf("expected b=1, got %d", counts["b"])
	}
	if counts["c"] != 0 {
		t.Errorf("expected c=0, got %d", counts["c"])
	}
}

func TestGetTurnCountsBulk_Empty(t *testing.T) {
	s := newTestStore(t)
	defer s.Close()

	counts, err := s.GetTurnCountsBulk(nil)
	if err != nil {
		t.Fatalf("GetTurnCountsBulk: %v", err)
	}
	if len(counts) != 0 {
		t.Errorf("expected empty map, got %d entries", len(counts))
	}
}

func TestIncrementTurnCount_Concurrent(t *testing.T) {
	s := newTestStore(t)
	defer s.Close()

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.IncrementTurnCount("proj")
		}()
	}
	wg.Wait()

	count, err := s.GetTurnCount("proj")
	if err != nil {
		t.Fatalf("GetTurnCount: %v", err)
	}
	if count != 20 {
		t.Errorf("expected count=20 after concurrent increments, got %d", count)
	}
}

func TestIncrementTurnCount_MultipleProjects(t *testing.T) {
	s := newTestStore(t)
	defer s.Close()

	s.IncrementTurnCount("proj-a")
	s.IncrementTurnCount("proj-b")
	s.IncrementTurnCount("proj-a")

	a, _ := s.GetTurnCount("proj-a")
	b, _ := s.GetTurnCount("proj-b")
	if a != 2 {
		t.Errorf("expected proj-a=2, got %d", a)
	}
	if b != 1 {
		t.Errorf("expected proj-b=1, got %d", b)
	}
}
