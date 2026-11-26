package storage

import "time"

const capUsageErrorMax = 200

// tableCapUsage records every execute_cap call (see handler_cap_exec.go).
const tableCapUsage = `CREATE TABLE IF NOT EXISTS cap_usage (
	id           INTEGER PRIMARY KEY AUTOINCREMENT,
	cap_name     TEXT,
	fn           TEXT,
	session_id   TEXT,
	started_at   TEXT,
	duration_ms  INTEGER,
	ok           INTEGER,
	error        TEXT,
	args_hash    TEXT,
	args_len     INTEGER
)`

const indexCapUsageName = `CREATE INDEX IF NOT EXISTS idx_cap_usage_name_started
	ON cap_usage(cap_name, started_at)`

// CapUsageEntry holds one call. Arguments are stored as hash + length only,
// because cap arguments routinely carry secrets.
type CapUsageEntry struct {
	CapName   string
	Fn        string
	SessionID string
	StartedAt time.Time
	Duration  time.Duration
	OK        bool
	Error     string
	ArgsHash  string
	ArgsLen   int
}

// LogCapUsage appends one row. An error here must never fail the cap call.
func (s *Store) LogCapUsage(e CapUsageEntry) error {
	errText := e.Error
	if len(errText) > capUsageErrorMax {
		errText = errText[:capUsageErrorMax]
	}
	ok := 0
	if e.OK {
		ok = 1
	}
	_, err := s.db.Exec(`INSERT INTO cap_usage
		(cap_name, fn, session_id, started_at, duration_ms, ok, error, args_hash, args_len)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.CapName, e.Fn, e.SessionID,
		e.StartedAt.Format("2006-01-02T15:04:05"),
		e.Duration.Milliseconds(), ok, errText, e.ArgsHash, e.ArgsLen)
	return err
}

// CapUsageCounts returns how many calls succeeded and how many failed.
func (s *Store) CapUsageCounts() (ok, failed int, err error) {	rows, err := s.db.Query("SELECT ok, COUNT(*) FROM cap_usage GROUP BY ok")
	if err != nil {
		return 0, 0, err
	}
	defer rows.Close()
	for rows.Next() {
		var flag, n int
		if err := rows.Scan(&flag, &n); err != nil {
			return 0, 0, err
		}
		if flag == 1 {
			ok = n
		} else {
			failed = n
		}
	}
	return ok, failed, rows.Err()
}

// CapUsageLast returns the cap name, argument hash and argument length of the
// most recent call.
func (s *Store) CapUsageLast() (capName, argsHash string, argsLen int, err error) {
	err = s.db.QueryRow(`SELECT cap_name, args_hash, args_len FROM cap_usage
		ORDER BY id DESC LIMIT 1`).Scan(&capName, &argsHash, &argsLen)
	if err != nil {
		return "", "", 0, nil
	}
	return capName, argsHash, argsLen, nil
}
