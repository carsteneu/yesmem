package proxy

import (
	"testing"
)

// === Task #14: Re-Expansion Budget Manager ===

func TestBudget_DefaultAllocation(t *testing.T) {
	b := NewBudget(100000)

	if b.Narrative != 2000 {
		t.Errorf("narrative budget: expected 2000, got %d", b.Narrative)
	}
	if b.Retrieval != 3000 {
		t.Errorf("retrieval budget: expected 3000, got %d", b.Retrieval)
	}
	if b.ReExpansion != 25000 {
		t.Errorf("re-expansion budget: expected 25000, got %d", b.ReExpansion)
	}
	if b.FreshMessages != 30000 {
		t.Errorf("fresh messages budget: expected 30000, got %d", b.FreshMessages)
	}
	if b.Stubs != 40000 {
		t.Errorf("stubs budget: expected 40000, got %d", b.Stubs)
	}
}

func TestBudget_SumsToThreshold(t *testing.T) {
	b := NewBudget(100000)
	total := b.Narrative + b.Retrieval + b.ReExpansion + b.FreshMessages + b.Stubs
	if total != 100000 {
		t.Errorf("budget components should sum to threshold: got %d", total)
	}
}

func TestBudget_ScalesWithThreshold(t *testing.T) {
	b := NewBudget(200000)
	if b.ReExpansion != 50000 {
		t.Errorf("re-expansion should scale: expected 50000, got %d", b.ReExpansion)
	}
	total := b.Narrative + b.Retrieval + b.ReExpansion + b.FreshMessages + b.Stubs
	if total != 200000 {
		t.Errorf("budget should sum to 200000, got %d", total)
	}
}

func TestBudget_CanSpend(t *testing.T) {
	b := NewBudget(100000)

	if !b.CanSpendReExpansion(12000) {
		t.Error("should be able to spend 12000 from 25000 re-expansion budget")
	}
	b.SpendReExpansion(12000)

	if !b.CanSpendReExpansion(13000) {
		t.Error("should be able to spend remaining 13000")
	}
	b.SpendReExpansion(13000)

	if b.CanSpendReExpansion(1) {
		t.Error("re-expansion budget should be exhausted")
	}
}

func TestBudget_SmallThreshold(t *testing.T) {
	b := NewBudget(10000)
	// Should not panic or produce negative values
	if b.Narrative < 0 || b.Stubs < 0 {
		t.Error("no budget component should be negative")
	}
	total := b.Narrative + b.Retrieval + b.ReExpansion + b.FreshMessages + b.Stubs
	if total != 10000 {
		t.Errorf("budget should sum to threshold even when small: got %d", total)
	}
}
