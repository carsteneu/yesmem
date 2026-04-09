package storage

import "time"

// TrackSessionEnd records that a session ended (clear/compact/logout).
func (s *Store) TrackSessionEnd(project, sessionID, reason string) error {
	_, err := s.db.Exec(`INSERT OR REPLACE INTO session_tracking (project, session_id, reason, timestamp)
		VALUES (?, ?, ?, ?)`, project, sessionID, reason, time.Now().UTC().Format(time.RFC3339))
	return err
}

// GetLastEndedSession returns the most recent ended session_id for the project.
func (s *Store) GetLastEndedSession(project string) (string, error) {
	var sid string
	err := s.readerDB().QueryRow(`SELECT session_id FROM session_tracking
		WHERE project = ? AND reason IN ('clear', 'compact')
		ORDER BY timestamp DESC LIMIT 1`, project).Scan(&sid)
	if err != nil {
		return "", err
	}
	return sid, nil
}

// GetRecentEndedSession returns the most recent ended session_id only if it ended
// within maxAge. Returns "" if no recent entry exists. This prevents stale
// session_tracking entries from triggering recovery on normal startups.
func (s *Store) GetRecentEndedSession(project string, maxAge time.Duration) (string, string, error) {
	var sid, reason string
	var ts string
	err := s.readerDB().QueryRow(`SELECT session_id, reason, timestamp FROM session_tracking
		WHERE project = ? AND reason IN ('clear', 'compact')
		ORDER BY timestamp DESC LIMIT 1`, project).Scan(&sid, &reason, &ts)
	if err != nil {
		return "", "", err
	}
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return "", "", err
	}
	if time.Since(t) > maxAge {
		return "", "", nil
	}
	return sid, reason, nil
}
