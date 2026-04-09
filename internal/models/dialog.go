package models

import "time"

// Dialog represents an active agent-to-agent conversation.
type Dialog struct {
	ID        int64     `json:"id"`
	Initiator string    `json:"initiator"` // session_id of starter
	Partner   string    `json:"partner"`   // session_id of partner
	Topic     string    `json:"topic"`
	Status    string    `json:"status"` // pending, active, ended
	CreatedAt time.Time `json:"created_at"`
}

// DialogMessage is a single message within a dialog.
type DialogMessage struct {
	ID        int64     `json:"id"`
	DialogID  int64     `json:"dialog_id"`
	Sender    string    `json:"sender"`  // session_id
	Content   string    `json:"content"`
	MsgType   string    `json:"msg_type"` // command, response, ack, status
	Read      bool      `json:"read"`
	CreatedAt time.Time `json:"created_at"`
}
