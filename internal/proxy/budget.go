package proxy

// Budget manages token allocation across proxy components.
// Ensures re-expansion + retrieval + narrative don't exceed the threshold.
type Budget struct {
	Narrative     int // fixed 2% for narrative block
	Retrieval     int // max 3% for associative retrieval
	ReExpansion   int // max 25% for re-expanding stubs
	FreshMessages int // 30% for keepRecent messages
	Stubs         int // remaining for stubbed messages

	reExpansionSpent int
}

// NewBudget creates a token budget proportional to the threshold.
func NewBudget(threshold int) *Budget {
	narrative := threshold * 2 / 100    // 2%
	retrieval := threshold * 3 / 100    // 3%
	reExpansion := threshold * 25 / 100 // 25%
	fresh := threshold * 30 / 100       // 30%
	stubs := threshold - narrative - retrieval - reExpansion - fresh // 40%

	return &Budget{
		Narrative:     narrative,
		Retrieval:     retrieval,
		ReExpansion:   reExpansion,
		FreshMessages: fresh,
		Stubs:         stubs,
	}
}

// CanSpendReExpansion checks if there's budget left for re-expansion.
func (b *Budget) CanSpendReExpansion(tokens int) bool {
	return b.reExpansionSpent+tokens <= b.ReExpansion
}

// SpendReExpansion records tokens spent on re-expansion.
func (b *Budget) SpendReExpansion(tokens int) {
	b.reExpansionSpent += tokens
}
