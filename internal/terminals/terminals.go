// Package terminals merkt sich die offenen Terminal-Fenster (Position,
// Arbeitsfläche, laufende Session) und stellt sie nach einem Reboot wieder her.
package terminals

import (
	"encoding/json"
	"os"
	"strings"
	"time"
)

// SnapshotFile is the JSON file name inside the yesmem data dir.
const SnapshotFile = "terminals.json"

const snapshotVersion = 1

// Window describes a terminal window (or — for tabs without own geometry —
// a session entry) that should be restored after a reboot.
type Window struct {
	XID       string `json:"xid"`                // X11 window id, e.g. "0x05600004". "" = session without window.
	Emulator  string `json:"emulator,omitempty"` // where it launched from: ghostty | warp
	Title     string `json:"title,omitempty"`
	WorkDir   string `json:"workdir,omitempty"`
	Workspace int    `json:"workspace"` // -1 = unknown
	X         int    `json:"x"`
	Y         int    `json:"y"`
	W         int    `json:"w"`
	H         int    `json:"h"`
	Maximized bool   `json:"maximized,omitempty"`
	Kind      string `json:"kind,omitempty"` // opencode | claude | shell
	SessionID string `json:"session_id,omitempty"`
	TouchedAt string `json:"touched_at,omitempty"` // RFC3339 time of last touch
}

// Snapshot is the persisted terminal state.
type Snapshot struct {
	Version int      `json:"version"`
	SavedAt string   `json:"saved_at"`
	Windows []Window `json:"windows"`
}

// Live tells whether a window still has a known X11 id.
func (w Window) Live() bool { return w.XID != "" }

func (w Window) key() string {
	if w.XID != "" {
		return "x:" + normXID(w.XID)
	}
	return "s:" + w.SessionID
}

// Empty returns a fresh snapshot with the current schema version.
func Empty() *Snapshot {
	return &Snapshot{Version: snapshotVersion}
}

// LoadSnapshot reads the snapshot from path; a missing or corrupt file
// returns an empty snapshot (never an error for missing files).
func LoadSnapshot(path string) (*Snapshot, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Empty(), nil
		}
		return nil, err
	}
	var s Snapshot
	if err := json.Unmarshal(data, &s); err != nil {
		// A corrupt snapshot must not block the workflow.
		return Empty(), nil
	}
	if s.Version == 0 {
		s.Version = snapshotVersion
	}
	if s.Windows == nil {
		s.Windows = []Window{}
	}
	return &s, nil
}

// SaveSnapshot writes the snapshot atomically.
func SaveSnapshot(path string, s *Snapshot) error {
	s.SavedAt = time.Now().Format(time.RFC3339)
	s.Version = snapshotVersion
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// UpsertWindow adds w to the snapshot or updates the entry with the same
// XID/session. Fields that are empty on the incoming window keep their old
// value, so a coarse refresh cannot clobber richer session data.
func UpsertWindow(s *Snapshot, w Window) {
	for i := range s.Windows {
		if s.Windows[i].key() == w.key() {
			merge := s.Windows[i]
			merge.updateWith(w)
			s.Windows[i] = merge
			return
		}
	}
	s.Windows = append(s.Windows, w)
}

func (w *Window) updateWith(n Window) {
	if n.XID != "" {
		w.XID = n.XID
	}
	if n.Emulator != "" {
		w.Emulator = n.Emulator
	}
	if n.Title != "" {
		w.Title = n.Title
	}
	if n.WorkDir != "" {
		w.WorkDir = n.WorkDir
	}
	if n.Workspace >= 0 {
		w.Workspace = n.Workspace
	}
	if n.W != 0 {
		w.X, w.Y, w.W, w.H = n.X, n.Y, n.W, n.H
	}
	if n.Maximized {
		w.Maximized = true
	}
	if n.Kind != "" {
		w.Kind = n.Kind
	}
	if n.SessionID != "" {
		w.SessionID = n.SessionID
	}
	if n.TouchedAt != "" {
		w.TouchedAt = n.TouchedAt
	}
}

// PruneMissing removes windows whose XID is no longer present in liveIDs
// (i.e. the user closed them). Entries without an XID are kept.
func PruneMissing(s *Snapshot, liveIDs []string) {
	live := make(map[string]bool, len(liveIDs))
	for _, id := range liveIDs {
		live[normXID(id)] = true
	}
	out := s.Windows[:0]
	for _, w := range s.Windows {
		if w.XID == "" || live[normXID(w.XID)] {
			out = append(out, w)
		}
	}
	s.Windows = out
}

// ClassifyEmulator maps a WM_CLASS value to a known terminal emulator kind.
func ClassifyEmulator(cls []string) string {
	for _, c := range cls {
		c = strings.ToLower(c)
		switch {
		case strings.Contains(c, "ghostty"):
			return "ghostty"
		case strings.Contains(c, "warp"):
			return "warp"
		}
	}
	return ""
}

// ResumeCommand builds the CLI resume command for a session. An empty result
// means "just open a shell".
func ResumeCommand(kind, sessionID string) string {
	switch kind {
	case "opencode":
		if sessionID == "" {
			return "opencode"
		}
		return "opencode --session " + sessionID
	case "claude":
		if sessionID == "" {
			return "claude"
		}
		return "claude --resume " + sessionID
	}
	return ""
}

// resumeArgs splits the resume command for re-exec inside a terminal.
func resumeArgs(w Window) []string {
	cmd := ResumeCommand(w.Kind, w.SessionID)
	if cmd == "" {
		return nil
	}
	return strings.Fields(cmd)
}
