package codescan

import (
	"errors"
	"fmt"
)

// A directory can be unscannable for reasons that are not failures of a scan: it carries
// no index of its own (a worktree that was never indexed), or the scanner refuses it by
// configuration (too large, a system path). Neither becomes true by waiting, so a
// post-failure cooldown is the wrong reaction — it hides the state for ten minutes and
// turns one answer into a repeating log line.
var (
	// ErrNotIndexed reports a directory with no code index of its own.
	ErrNotIndexed = errors.New("codescan: directory has no index of its own")
	// ErrUnscannable reports a directory the scanner refuses by configuration.
	ErrUnscannable = errors.New("codescan: directory is not scannable")
)

// IsState reports whether err describes a state of the directory rather than a failed
// scan. A caller that acts on the difference must not record a failure for a state, and
// may log it far less often.
func IsState(err error) bool {
	return errors.Is(err, ErrNotIndexed) || errors.Is(err, ErrUnscannable)
}

// stateError wraps msg with the sentinel that classifies it.
func stateError(sentinel error, format string, args ...any) error {
	return fmt.Errorf("%w: %s", sentinel, fmt.Sprintf(format, args...))
}
