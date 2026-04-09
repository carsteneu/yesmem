package bloom

import (
	"strings"
	"sync"

	bloomlib "github.com/bits-and-blooms/bloom/v3"
)

// Manager maintains per-session bloom filters for fast pre-filtering.
// Each filter uses ~4KB RAM. 16,000 sessions = ~64MB.
type Manager struct {
	mu      sync.RWMutex
	filters map[string]*bloomlib.BloomFilter // sessionID → filter
}

// New creates an empty bloom filter manager.
func New() *Manager {
	return &Manager{
		filters: make(map[string]*bloomlib.BloomFilter),
	}
}

// AddSession creates a bloom filter for a session with the given terms.
// Terms are lowercased and split on whitespace automatically.
func (m *Manager) AddSession(sessionID string, terms []string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// ~4KB per filter: 1000 items, 0.01 false positive rate
	f := bloomlib.NewWithEstimates(1000, 0.01)

	for _, term := range terms {
		for _, word := range tokenize(term) {
			f.AddString(word)
		}
	}

	m.filters[sessionID] = f
}

// MayContain returns session IDs whose bloom filter matches the term.
// False positives possible (~1%), false negatives never.
func (m *Manager) MayContain(term string) []string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	words := tokenize(term)
	var matches []string

	for sid, f := range m.filters {
		allMatch := true
		for _, w := range words {
			if !f.TestString(w) {
				allMatch = false
				break
			}
		}
		if allMatch {
			matches = append(matches, sid)
		}
	}
	return matches
}

// MayContainAll returns session IDs where ALL terms may be present.
func (m *Manager) MayContainAll(terms []string) []string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var allWords []string
	for _, t := range terms {
		allWords = append(allWords, tokenize(t)...)
	}

	var matches []string
	for sid, f := range m.filters {
		allMatch := true
		for _, w := range allWords {
			if !f.TestString(w) {
				allMatch = false
				break
			}
		}
		if allMatch {
			matches = append(matches, sid)
		}
	}
	return matches
}

// SessionCount returns the number of sessions tracked.
func (m *Manager) SessionCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.filters)
}

// tokenize lowercases and splits text on whitespace.
func tokenize(s string) []string {
	s = strings.ToLower(s)
	words := strings.Fields(s)
	var result []string
	for _, w := range words {
		w = strings.TrimFunc(w, func(r rune) bool {
			return r < 'a' || r > 'z'
		})
		if len(w) > 1 { // skip single-char tokens
			result = append(result, w)
		}
	}
	if len(result) == 0 && len(words) > 0 {
		return []string{strings.ToLower(strings.TrimSpace(s))}
	}
	return result
}
