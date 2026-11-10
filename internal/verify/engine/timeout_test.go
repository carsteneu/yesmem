package engine

import (
	"testing"
	"time"
)

// A project may declare a profile that runs its whole Go suite (yesmem: 44
// packages). In the bwrap sandbox such a run measured over five minutes on
// 2026-11-10 while the same command finished in about three on the host, so a
// five minute cap turns a green project into an unverifiable DONE. The cap is
// the verifier's default, not the project's promise: the run's own budget can
// still shorten it.
func TestDefaultTestTimeoutCoversAFullProjectSuite(t *testing.T) {
	if DefaultTestTimeout < 15*time.Minute {
		t.Fatalf("DefaultTestTimeout = %v, want at least 15m for a full project suite", DefaultTestTimeout)
	}
}
