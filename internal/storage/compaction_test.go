package storage

import (
	"testing"
)

func TestSaveAndGetCompactedBlock(t *testing.T) {
	s := mustOpen(t)
	defer s.Close()

	err := s.SaveCompactedBlock("thread-abc", 10, 60, "Compacted: 50 msgs, Read(5), Edit(3)")
	if err != nil {
		t.Fatalf("save: %v", err)
	}

	blocks, err := s.GetCompactedBlocks("thread-abc")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(blocks) != 1 {
		t.Fatalf("expected 1 block, got %d", len(blocks))
	}
	b := blocks[0]
	if b.ThreadID != "thread-abc" {
		t.Errorf("thread_id: expected thread-abc, got %q", b.ThreadID)
	}
	if b.StartIdx != 10 || b.EndIdx != 60 {
		t.Errorf("range: expected 10-60, got %d-%d", b.StartIdx, b.EndIdx)
	}
	if b.Content != "Compacted: 50 msgs, Read(5), Edit(3)" {
		t.Errorf("content mismatch: %q", b.Content)
	}
}

func TestCompactedBlockMultiple(t *testing.T) {
	s := mustOpen(t)
	defer s.Close()

	for _, r := range [][2]int{{0, 50}, {51, 100}, {101, 150}} {
		err := s.SaveCompactedBlock("multi", r[0], r[1], "block")
		if err != nil {
			t.Fatalf("save %d-%d: %v", r[0], r[1], err)
		}
	}

	blocks, err := s.GetCompactedBlocks("multi")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(blocks) != 3 {
		t.Errorf("expected 3 blocks, got %d", len(blocks))
	}
	// Verify ordering by start_idx
	if blocks[0].StartIdx != 0 || blocks[1].StartIdx != 51 || blocks[2].StartIdx != 101 {
		t.Errorf("ordering wrong: %d, %d, %d", blocks[0].StartIdx, blocks[1].StartIdx, blocks[2].StartIdx)
	}
}

func TestCompactedBlockRangeQuery(t *testing.T) {
	s := mustOpen(t)
	defer s.Close()

	s.SaveCompactedBlock("range-test", 10, 60, "block1")
	s.SaveCompactedBlock("range-test", 70, 120, "block2")

	// Overlapping range should find block1
	blocks, err := s.GetCompactedBlocksInRange("range-test", 30, 50)
	if err != nil {
		t.Fatalf("range query: %v", err)
	}
	if len(blocks) != 1 {
		t.Fatalf("expected 1, got %d", len(blocks))
	}
	if blocks[0].StartIdx != 10 {
		t.Errorf("expected start 10, got %d", blocks[0].StartIdx)
	}

	// Non-overlapping range
	blocks2, err := s.GetCompactedBlocksInRange("range-test", 61, 69)
	if err != nil {
		t.Fatalf("non-overlap: %v", err)
	}
	if len(blocks2) != 0 {
		t.Errorf("expected 0, got %d", len(blocks2))
	}
}

func TestCompactedBlockUpsert(t *testing.T) {
	s := mustOpen(t)
	defer s.Close()

	// Same thread+start_idx should upsert
	s.SaveCompactedBlock("upsert-test", 10, 60, "old content")
	s.SaveCompactedBlock("upsert-test", 10, 60, "new content")

	blocks, _ := s.GetCompactedBlocks("upsert-test")
	if len(blocks) != 1 {
		t.Fatalf("expected 1 after upsert, got %d", len(blocks))
	}
	if blocks[0].Content != "new content" {
		t.Errorf("expected new content, got %q", blocks[0].Content)
	}
}

func TestDeleteCompactedBlocks(t *testing.T) {
	s := mustOpen(t)
	defer s.Close()

	s.SaveCompactedBlock("del-test", 0, 50, "to be deleted")

	err := s.DeleteCompactedBlocks("del-test")
	if err != nil {
		t.Fatalf("delete: %v", err)
	}

	blocks, _ := s.GetCompactedBlocks("del-test")
	if len(blocks) != 0 {
		t.Errorf("expected 0 after delete, got %d", len(blocks))
	}
}
