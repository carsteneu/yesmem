package storage

import (
	"database/sql"
	"strings"
)

// ScheduleJobForSection resolves the naming convention this system uses:
// an agent's section for a scheduled run is "sched-" + the job's name
// (agent section "sched-wake-pulse" belongs to job "wake-pulse").
//
// This is the ONLY mapping available: scheduled_jobs has no section column, and
// the section inside a job's prompt is free text. Whoever changes the convention
// has to adjust the orphan branch with it — that is why it is stated here and in
// PR #173 instead of being an implicit assumption.
//
// Returns the job name, whether it still exists, and whether it is enabled.
func (s *Store) ScheduleJobForSection(section string) (job string, exists, enabled bool) {
	job = strings.TrimPrefix(section, "sched-")
	if job == section || job == "" {
		return "", false, false
	}
	var en int
	err := s.db.QueryRow(
		`SELECT COALESCE(enabled,0) FROM scheduled_jobs WHERE name = ?`, job).Scan(&en)
	if err == sql.ErrNoRows {
		return job, false, false
	}
	if err != nil {
		return job, false, false
	}
	return job, true, en != 0
}

// HasYoungerSibling reports whether another agent exists in the same section that
// was created after this row stopped. Such a row is superseded by its successor —
// a safety net for the cases where the sched- convention breaks.
//
// created_at and stopped_at do NOT share a format (created_at is local time
// without a zone in 753 of 771 rows, stopped_at is RFC3339 with an offset), so
// the same two parsers used for last_activity_at are applied here.
func (s *Store) HasYoungerSibling(id string) (bool, error) {
	var project, section, createdAt, stoppedAt string
	err := s.db.QueryRow(`SELECT COALESCE(project,''), COALESCE(section,''),
			COALESCE(created_at,''), COALESCE(stopped_at,'')
		FROM agents WHERE id = ?`, id,
	).Scan(&project, &section, &createdAt, &stoppedAt)
	if err != nil || section == "" {
		return false, err
	}

	created := parseLocalTime(createdAt)
	if created.IsZero() {
		created = parseAnyAgentTime(createdAt)
	}
	stopped := parseZoneBoundTime(stoppedAt)
	if stopped.IsZero() {
		stopped = parseAnyAgentTime(stoppedAt)
	}
	if created.IsZero() || stopped.IsZero() {
		return false, nil
	}

	var youngerCreated string
	err = s.db.QueryRow(`SELECT COALESCE(created_at,'') FROM agents
		WHERE project = ? AND section = ? AND id != ?
		ORDER BY id DESC LIMIT 1`, project, section, id).Scan(&youngerCreated)
	if err == sql.ErrNoRows || youngerCreated == "" {
		return false, nil
	}
	if err != nil {
		return false, err
	}

	sibling := parseLocalTime(youngerCreated)
	if sibling.IsZero() {
		sibling = parseAnyAgentTime(youngerCreated)
	}
	if sibling.IsZero() {
		return false, nil
	}
	return sibling.After(created), nil
}

// supersededReason builds the operator-facing explanation for a schedule-driven
// orphan: whether a successor is still coming decides which sentence is true.
func (s *Store) supersededReason(section string) string {
	job, exists, enabled := s.ScheduleJobForSection(section)
	if !exists {
		return "schedule retired (" + job + "), no successor coming"
	}
	if !enabled {
		return "schedule disabled (" + job + "), no successor coming"
	}
	return "superseded by schedule (" + job + ")"
}
