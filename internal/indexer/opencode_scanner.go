package indexer

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/carsteneu/yesmem/internal/models"
	"github.com/carsteneu/yesmem/internal/storage"

	_ "modernc.org/sqlite"
)

const scanOpencodeInterval = 5 * time.Minute

type OpencodeScanner struct {
	dbPath       string
	store        *storage.Store
	lastScanAt   time.Time
	lastCheckAt  time.Time
	logger       *log.Logger
	notifyCh     chan<- struct{}
	excludeProject map[string]bool
}

func NewOpencodeScanner(dbPath string, store *storage.Store, logger *log.Logger, notifyCh chan<- struct{}, excludeProjects []string) *OpencodeScanner {
	exclude := make(map[string]bool, len(excludeProjects))
	for _, p := range excludeProjects {
		exclude[strings.ToLower(p)] = true
	}
	return &OpencodeScanner{
		dbPath:      dbPath,
		store:       store,
		lastScanAt:  time.Time{},
		lastCheckAt: time.Now(),
		logger:      logger,
		notifyCh:    notifyCh,
		excludeProject: exclude,
	}
}

func (s *OpencodeScanner) logf(format string, args ...any) {
	if s.logger != nil {
		s.logger.Printf(format, args...)
	}
}

func (s *OpencodeScanner) MaybeScan() {
	if time.Since(s.lastCheckAt) < scanOpencodeInterval {
		return
	}
	s.lastCheckAt = time.Now()

	db, err := sql.Open("sqlite", s.dbPath)
	if err != nil {
		s.logf("opencode scanner: open db: %v", err)
		return
	}
	defer db.Close()

	db.SetMaxOpenConns(1)

	sessions, err := s.querySessions(db)
	if err != nil {
		s.logf("opencode scanner: query sessions: %v", err)
		return
	}

	if len(sessions) == 0 {
		return
	}

	var newlyIndexed int
	for _, sess := range sessions {
		dbMsgs, err := s.queryMessages(db, sess.ID)
		if err != nil {
			s.logf("opencode scanner: session %s messages: %v", sess.ID, err)
			continue
		}

		msgs := mapOpencodeMessages(dbMsgs)
		if isExtractionPipelineSession(msgs) {
			continue
		}
		yesSess := buildOpencodeSession(sess, msgs)

		// Skip projects on the exclusion list
		if s.excludeProject[strings.ToLower(yesSess.Project)] || s.excludeProject[yesSess.ProjectShort] {
			continue
		}

		if err := s.store.UpsertSession(yesSess); err != nil {
			s.logf("opencode scanner: upsert session %s: %v", yesSess.ID, err)
			continue
		}

		s.normalizeMessages(msgs, yesSess.ID)

		if err := s.store.DeleteMessagesBySession(yesSess.ID); err != nil {
			s.logf("opencode scanner: delete old messages %s: %v", yesSess.ID, err)
		}
		if len(msgs) > 0 {
			if err := s.store.InsertMessages(msgs); err != nil {
				s.logf("opencode scanner: insert messages %s: %v", yesSess.ID, err)
				continue
			}
		}

		newlyIndexed++
		if s.lastScanAt.Before(sess.Updated) {
			s.lastScanAt = sess.Updated
		}
	}

	if newlyIndexed > 0 {
		s.logf("opencode scanner: indexed %d sessions", newlyIndexed)
		if s.notifyCh != nil {
			select {
			case s.notifyCh <- struct{}{}:
			default:
			}
		}
	}
}

func (s *OpencodeScanner) normalizeMessages(msgs []models.Message, sessionID string) {
	for i := range msgs {
		msgs[i].SessionID = sessionID
		msgs[i].SourceAgent = models.SourceAgentOpencode
	}
}

func (s *OpencodeScanner) querySessions(db *sql.DB) ([]opencodeDBSession, error) {
	var rows *sql.Rows
	var err error

	if s.lastScanAt.IsZero() {
		rows, err = db.Query(`
			SELECT id, directory, title,
				time_created, time_updated, parent_id,
				COALESCE(model, ''), COALESCE(agent, '')
			FROM session
			ORDER BY time_created
		`)
	} else {
		rows, err = db.Query(`
			SELECT id, directory, title,
				time_created, time_updated, parent_id,
				COALESCE(model, ''), COALESCE(agent, '')
			FROM session
			WHERE time_updated > ?
			ORDER BY time_created
		`, s.lastScanAt.UnixMilli())
	}
	if err != nil {
		return nil, fmt.Errorf("query sessions: %w", err)
	}
	defer rows.Close()

	var sessions []opencodeDBSession
	for rows.Next() {
		var sess opencodeDBSession
		var createdMs, updatedMs int64
		var parentID sql.NullString
		var model, agent string
		if err := rows.Scan(&sess.ID, &sess.Directory, &sess.Title,
			&createdMs, &updatedMs, &parentID,
			&model, &agent); err != nil {
			return nil, fmt.Errorf("scan session: %w", err)
		}
		if parentID.Valid {
			sess.ParentID = parentID.String
		}
		sess.Created = time.UnixMilli(createdMs)
		sess.Updated = time.UnixMilli(updatedMs)
		sess.Model = model
		sess.Agent = agent
		sessions = append(sessions, sess)
	}
	return sessions, rows.Err()
}

func (s *OpencodeScanner) queryMessages(db *sql.DB, sessionID string) ([]opencodeDBMessage, error) {
	rows, err := db.Query(`
		SELECT m.id, m.session_id,
			json_extract(m.data, '$.role') as role,
			m.time_created,
			p.data as part_data
		FROM message m
		JOIN part p ON p.message_id = m.id
		WHERE m.session_id = ?
		ORDER BY m.time_created, m.id, p.id
	`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("query messages: %w", err)
	}
	defer rows.Close()

	var msgs []opencodeDBMessage
	for rows.Next() {
		var msg opencodeDBMessage
		var createdMs int64
		var partJSON string
		if err := rows.Scan(&msg.ID, &msg.SessionID, &msg.Role, &createdMs, &partJSON); err != nil {
			return nil, fmt.Errorf("scan message: %w", err)
		}
		msg.CreatedMs = createdMs
		if err := json.Unmarshal([]byte(partJSON), &msg.Part); err != nil {
			return nil, fmt.Errorf("parse part: %w", err)
		}
		msgs = append(msgs, msg)
	}
	sort.Slice(msgs, func(i, j int) bool {
		if msgs[i].CreatedMs != msgs[j].CreatedMs {
			return msgs[i].CreatedMs < msgs[j].CreatedMs
		}
		return msgs[i].ID < msgs[j].ID
	})
	return msgs, rows.Err()
}

var extractionPromptSignatures = storage.ExtractionSessionSignatures

func isExtractionPipelineSession(msgs []models.Message) bool {
	for _, m := range msgs {
		if m.Role == "user" {
			for _, sig := range extractionPromptSignatures {
				if strings.Contains(m.Content, sig) {
					return true
				}
			}
		}
	}
	return false
}
