package setup

import "testing"

func TestImportDone(t *testing.T) {
	cases := []struct {
		name     string
		started  bool
		status   indexStatus
		idleZero int
		done     bool
		procs    int
		noSess   bool
	}{
		{
			name:    "normal finish after Running observed",
			started: true,
			status:  indexStatus{Running: false, Done: 5, Skipped: 2, Total: 7},
			done:    true, procs: 3,
		},
		{
			name:   "finished before Running observed, sessions present",
			status: indexStatus{Running: false, Done: 4, Total: 4},
			done:   true, procs: 4,
		},
		{
			// Empty-session race: indexing with 0 sessions completes before any
			// poll observes Running=true; Done stays 0. Must exit, not spin to timeout.
			name:     "empty machine idle after grace polls",
			status:   indexStatus{Running: false, Done: 0, Total: 0},
			idleZero: 3,
			done:     true, procs: 0, noSess: true,
		},
		{
			name:     "empty machine still inside grace",
			status:   indexStatus{Running: false, Done: 0, Total: 0},
			idleZero: 2,
			done:     false,
		},
		{
			name:   "still running",
			status: indexStatus{Running: true, Done: 1, Total: 9},
			done:   false,
		},
		{
			// Queued work not yet started: Total set, not running, nothing done.
			// Keep waiting — daemon may flip to Running on the next poll.
			name:   "queued not started",
			status: indexStatus{Running: false, Done: 0, Total: 12},
			done:   false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			done, procs, noSess := importDone(c.started, &c.status, c.idleZero)
			if done != c.done || procs != c.procs || noSess != c.noSess {
				t.Fatalf("importDone(%v, %+v, %d) = (%v, %d, %v), want (%v, %d, %v)",
					c.started, c.status, c.idleZero, done, procs, noSess, c.done, c.procs, c.noSess)
			}
		})
	}
}
