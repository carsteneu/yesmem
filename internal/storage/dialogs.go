package storage

import (
	"database/sql"
	"path/filepath"
	"time"

	"github.com/carsteneu/yesmem/internal/models"
)

const tableAgentDialogs = `CREATE TABLE IF NOT EXISTS agent_dialogs (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	initiator   TEXT NOT NULL,
	partner     TEXT NOT NULL,
	topic       TEXT DEFAULT '',
	status      TEXT DEFAULT 'pending',
	created_at  DATETIME DEFAULT CURRENT_TIMESTAMP
)`

const tableAgentMessages = `CREATE TABLE IF NOT EXISTS agent_messages (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	dialog_id   INTEGER NOT NULL REFERENCES agent_dialogs(id),
	target      TEXT DEFAULT '',
	sender      TEXT NOT NULL,
	content     TEXT NOT NULL,
	msg_type    TEXT DEFAULT 'command',
	read        INTEGER DEFAULT 0,
	delivered   INTEGER DEFAULT 0,
	delivered_at TEXT,
	delivery_retries INTEGER DEFAULT 0,
	delivery_failed  INTEGER DEFAULT 0,
	created_at  DATETIME DEFAULT CURRENT_TIMESTAMP
)`

// StartDialog creates a new agent dialog. Returns dialog ID.
func (s *Store) StartDialog(initiator, partner, topic string) (int64, error) {
	result, err := s.db.Exec(`INSERT INTO agent_dialogs (initiator, partner, topic, status) VALUES (?, ?, ?, 'pending')`,
		initiator, partner, topic)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

// GetDialog returns a dialog by ID.
func (s *Store) GetDialog(id int64) (*models.Dialog, error) {
	row := s.readerDB().QueryRow(`SELECT id, initiator, partner, topic, status, created_at FROM agent_dialogs WHERE id = ?`, id)
	d := &models.Dialog{}
	var ts string
	err := row.Scan(&d.ID, &d.Initiator, &d.Partner, &d.Topic, &d.Status, &ts)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	d.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", ts)
	return d, nil
}

// GetActiveDialogForSession returns the active or pending dialog for a session (as initiator or partner).
func (s *Store) GetActiveDialogForSession(sessionID string) (*models.Dialog, error) {
	row := s.readerDB().QueryRow(`SELECT id, initiator, partner, topic, status, created_at FROM agent_dialogs WHERE (initiator = ? OR partner = ?) AND status IN ('pending', 'active') ORDER BY created_at DESC LIMIT 1`,
		sessionID, sessionID)
	d := &models.Dialog{}
	var ts string
	err := row.Scan(&d.ID, &d.Initiator, &d.Partner, &d.Topic, &d.Status, &ts)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	d.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", ts)
	return d, nil
}

// UpdateDialogStatus sets the status of a dialog (pending → active → ended).
func (s *Store) UpdateDialogStatus(id int64, status string) error {
	_, err := s.db.Exec(`UPDATE agent_dialogs SET status = ? WHERE id = ?`, status, id)
	return err
}

// SendDialogMessage writes a message to a dialog.
func (s *Store) SendDialogMessage(dialogID int64, sender, content string) (int64, error) {
	result, err := s.db.Exec(`INSERT INTO agent_messages (dialog_id, sender, content, created_at) VALUES (?, ?, ?, datetime('now', 'localtime'))`,
		dialogID, sender, content)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

// GetUnreadMessages returns unread messages for a session in a dialog (messages NOT sent by this session).
func (s *Store) GetUnreadMessages(dialogID int64, forSession string) ([]models.DialogMessage, error) {
	rows, err := s.readerDB().Query(`SELECT id, dialog_id, sender, content, read, created_at FROM agent_messages WHERE dialog_id = ? AND sender != ? AND read = 0 ORDER BY created_at ASC`,
		dialogID, forSession)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var msgs []models.DialogMessage
	for rows.Next() {
		m := models.DialogMessage{}
		var ts string
		var readInt int
		if err := rows.Scan(&m.ID, &m.DialogID, &m.Sender, &m.Content, &readInt, &ts); err != nil {
			continue
		}
		m.Read = readInt != 0
		m.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", ts)
		msgs = append(msgs, m)
	}
	return msgs, rows.Err()
}

// MarkMessagesRead marks all messages in a dialog as read for a session.
func (s *Store) MarkMessagesRead(dialogID int64, forSession string) error {
	_, err := s.db.Exec(`UPDATE agent_messages SET read = 1 WHERE dialog_id = ? AND sender != ? AND read = 0`,
		dialogID, forSession)
	return err
}

// CheckPendingInvitations returns a pending dialog where this session is the partner.
func (s *Store) CheckPendingInvitations(sessionID string) (*models.Dialog, error) {
	row := s.readerDB().QueryRow(`SELECT id, initiator, partner, topic, status, created_at FROM agent_dialogs WHERE partner = ? AND status = 'pending' ORDER BY created_at DESC LIMIT 1`,
		sessionID)
	d := &models.Dialog{}
	var ts string
	err := row.Scan(&d.ID, &d.Initiator, &d.Partner, &d.Topic, &d.Status, &ts)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	d.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", ts)
	return d, nil
}

const tableAgentBroadcasts = `CREATE TABLE IF NOT EXISTS agent_broadcasts (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	sender      TEXT NOT NULL,
	project     TEXT NOT NULL,
	content     TEXT NOT NULL,
	read_by     TEXT DEFAULT '',
	created_at  DATETIME DEFAULT CURRENT_TIMESTAMP
)`

// SendBroadcast writes a broadcast message visible to all sessions on the same project.
func (s *Store) SendBroadcast(sender, project, content string) (int64, error) {
	result, err := s.db.Exec(`INSERT INTO agent_broadcasts (sender, project, content) VALUES (?, ?, ?)`,
		sender, project, content)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

// GetUnreadBroadcasts returns broadcast messages not sent by this session, created in the last 24h.
func (s *Store) GetUnreadBroadcasts(forSession, project string) ([]models.DialogMessage, error) {
	short := filepath.Base(project)
	rows, err := s.readerDB().Query(`SELECT id, sender, content, created_at FROM agent_broadcasts WHERE sender != ? AND (project = ? OR project = ?) AND created_at > datetime('now', '-24 hours') AND (read_by NOT LIKE ? ) ORDER BY created_at ASC`,
		forSession, project, short, "%"+forSession+"%")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var msgs []models.DialogMessage
	for rows.Next() {
		m := models.DialogMessage{}
		var ts string
		if err := rows.Scan(&m.ID, &m.Sender, &m.Content, &ts); err != nil {
			continue
		}
		m.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", ts)
		msgs = append(msgs, m)
	}
	return msgs, rows.Err()
}

// MarkBroadcastsRead marks broadcast messages as read for a session.
func (s *Store) MarkBroadcastsRead(ids []int64, sessionID string) error {
	for _, id := range ids {
		s.db.Exec(`UPDATE agent_broadcasts SET read_by = CASE WHEN read_by = '' THEN ? ELSE read_by || ',' || ? END WHERE id = ?`,
			sessionID, sessionID, id)
	}
	return nil
}

// --- Channel-based messaging (target-routed, no dialog state) ---

// SendChannelMessage inserts a message targeted at a specific session.
// msgType is one of: command, response, ack, status. Empty string defaults to "command".
func (s *Store) SendChannelMessage(target, sender, content, msgType string) (int64, error) {
	if msgType == "" {
		msgType = "command"
	}
	result, err := s.db.Exec(`INSERT INTO agent_messages (dialog_id, target, sender, content, msg_type, created_at) VALUES (0, ?, ?, ?, ?, datetime('now', 'localtime'))`,
		target, sender, content, msgType)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

// GetChannelMessages returns undelivered messages targeted at this session.
// Uses prefix match (LIKE) as safety net for truncated session IDs.
func (s *Store) GetChannelMessages(targetSession string) ([]models.DialogMessage, error) {
	prefix := targetSession
	if len(prefix) > 8 {
		prefix = prefix[:8]
	}
	rows, err := s.readerDB().Query(`SELECT id, sender, content, COALESCE(msg_type, 'command'), created_at FROM agent_messages WHERE (target = ? OR target LIKE ? || '%') AND delivered = 0 AND delivery_failed = 0 ORDER BY created_at ASC`,
		targetSession, prefix)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var msgs []models.DialogMessage
	for rows.Next() {
		m := models.DialogMessage{}
		var ts string
		if err := rows.Scan(&m.ID, &m.Sender, &m.Content, &m.MsgType, &ts); err != nil {
			continue
		}
		m.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", ts)
		msgs = append(msgs, m)
	}
	return msgs, rows.Err()
}

// MarkMessageDelivered marks a single message as successfully delivered via socket inject.
func (s *Store) MarkMessageDelivered(messageID int64) error {
	_, err := s.db.Exec(`UPDATE agent_messages SET delivered = 1, delivered_at = datetime('now', 'localtime'), read = 1 WHERE id = ?`, messageID)
	return err
}

// MarkMessageNotified sets read=1 (heartbeat pinged) but NOT delivered.
// The proxy will handle actual content delivery.
func (s *Store) MarkMessageNotified(messageID int64) error {
	_, err := s.db.Exec(`UPDATE agent_messages SET read = 1 WHERE id = ? AND delivered = 0`, messageID)
	return err
}

// GetUnnotifiedMessages returns messages that haven't been pinged yet (read=0, delivered=0).
// Used by heartbeat to avoid repeated pings.
func (s *Store) GetUnnotifiedMessages(targetSession string) ([]models.DialogMessage, error) {
	prefix := targetSession
	if len(prefix) > 8 {
		prefix = prefix[:8]
	}
	rows, err := s.readerDB().Query(`SELECT id, sender, content, COALESCE(msg_type, 'command'), created_at FROM agent_messages WHERE (target = ? OR target LIKE ? || '%') AND delivered = 0 AND read = 0 AND delivery_failed = 0 ORDER BY created_at ASC`,
		targetSession, prefix)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var msgs []models.DialogMessage
	for rows.Next() {
		m := models.DialogMessage{}
		var ts string
		if err := rows.Scan(&m.ID, &m.Sender, &m.Content, &m.MsgType, &ts); err != nil {
			continue
		}
		m.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", ts)
		msgs = append(msgs, m)
	}
	return msgs, nil
}

// IncrementDeliveryRetry increments the retry counter for a message.
// Returns the new retry count.
func (s *Store) IncrementDeliveryRetry(messageID int64) (int, error) {
	_, err := s.db.Exec(`UPDATE agent_messages SET delivery_retries = delivery_retries + 1 WHERE id = ?`, messageID)
	if err != nil {
		return 0, err
	}
	var count int
	err = s.readerDB().QueryRow(`SELECT delivery_retries FROM agent_messages WHERE id = ?`, messageID).Scan(&count)
	return count, err
}

// MarkDeliveryFailed marks a message as permanently undeliverable.
func (s *Store) MarkDeliveryFailed(messageID int64) error {
	_, err := s.db.Exec(`UPDATE agent_messages SET delivery_failed = 1 WHERE id = ?`, messageID)
	return err
}

// GetMessageDeliveryStatus returns delivery status for a message.
func (s *Store) GetMessageDeliveryStatus(messageID int64) (delivered bool, retries int, failed bool, err error) {
	err = s.readerDB().QueryRow(`SELECT delivered, delivery_retries, delivery_failed FROM agent_messages WHERE id = ?`, messageID).Scan(&delivered, &retries, &failed)
	return
}

// MarkChannelMessagesRead marks all unread messages targeted at this session as read AND delivered.
// Sets both read=1 and delivered=1 to prevent re-injection by both proxy (read filter) and
// heartbeat (delivered filter). Without delivered=1, messages loop: proxy injects → session
// echoes → new message → proxy re-injects old (still delivered=0) → infinite echo loop.
// Uses prefix match (same as GetChannelMessages) to handle truncated session IDs.
func (s *Store) MarkChannelMessagesRead(targetSession string) error {
	prefix := targetSession
	if len(prefix) > 8 {
		prefix = prefix[:8]
	}
	_, err := s.db.Exec(`UPDATE agent_messages SET read = 1, delivered = 1, delivered_at = datetime('now', 'localtime') WHERE (target = ? OR target LIKE ? || '%') AND read = 0`,
		targetSession, prefix)
	return err
}

// IsAckOnAck returns true if sending msgType from target to sender would create an ACK loop.
// An ACK loop occurs when the last message target→sender was already an ack/status,
// and the new outgoing message is also an ack/status — both sides just echoing back.
func (s *Store) IsAckOnAck(target, sender, msgType string) bool {
	if msgType != "ack" && msgType != "status" {
		return false
	}
	var lastType string
	err := s.readerDB().QueryRow(`SELECT COALESCE(msg_type, 'command') FROM agent_messages WHERE target = ? AND sender = ? ORDER BY id DESC LIMIT 1`,
		sender, target).Scan(&lastType)
	if err != nil {
		return false
	}
	return lastType == "ack" || lastType == "status"
}
