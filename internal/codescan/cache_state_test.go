package codescan

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

// A directory without an index of its own is a state, not a failure: there is nothing
// transient to retry, and a cooldown hides the state behind ten minutes of silence while
// the log fills with "cooldown active". On 2026-11-13 four yesloop worktrees and one
// blacklisted checkout produced ~223 such lines in a day.
func TestScanStateIsNotAFailure(t *testing.T) {
	dir := gitWorktreeDir(t)
	fs := &memFailureStore{failures: map[string]time.Time{}}
	inner := &countingScanner{err: fmt.Errorf("%w: no CBM database", ErrNotIndexed)}
	cs := NewCachedScanner(inner).WithStore(fs)

	if _, err := cs.Scan(dir); !errors.Is(err, ErrNotIndexed) {
		t.Fatalf("first scan error = %v, want ErrNotIndexed", err)
	}
	if _, recorded := fs.failures[scanCooldownKey(dir)]; recorded {
		t.Error("a state was recorded as a scan failure, so it buys a cooldown")
	}

	// The next call reaches the scanner again instead of dying in a cooldown.
	if _, err := cs.Scan(dir); errors.Is(err, ErrScanCooldown) {
		t.Fatal("a state bought a cooldown: the next call never reaches the scanner")
	}
	if inner.calls != 2 {
		t.Fatalf("inner scan calls = %d, want 2", inner.calls)
	}
}

// A state is worth one line a day, not one per request.
func TestScanStateIsLoggedOncePerDay(t *testing.T) {
	dir := gitWorktreeDir(t)
	inner := &countingScanner{err: fmt.Errorf("%w: blacklisted", ErrUnscannable)}
	cs := NewCachedScanner(inner).WithStore(&memFailureStore{failures: map[string]time.Time{}})

	day := time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC)
	previous := stateNow
	stateNow = func() time.Time { return day }
	t.Cleanup(func() { stateNow = previous })

	if !cs.noteState(dir, inner.err) {
		t.Error("the first state of a day is not logged")
	}
	if cs.noteState(dir, inner.err) {
		t.Error("the second state of the same day is logged again")
	}

	stateNow = func() time.Time { return day.Add(24 * time.Hour) }
	if !cs.noteState(dir, inner.err) {
		t.Error("the first state of the next day is not logged")
	}
}
