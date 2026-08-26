package terminals

import (
	"fmt"
	"path/filepath"
	"time"
)

func snapshotPath(dataDir string) string {
	return filepath.Join(dataDir, SnapshotFile)
}

// haveDisplay checks whether an X11 window manager is reachable.
func haveDisplay() bool {
	_, err := commandOut("wmctrl", "-m")
	return err == nil
}

// Touch records the currently active terminal window for the given session.
// Called on every user interaction (think hook). Also prunes windows the user
// closed since the last touch. Best-effort: never fails headless sessions.
func Touch(dataDir, sessionID string, cliPID int) error {
	if !haveDisplay() {
		return nil
	}
	xid := activeWindowXID()
	if xid == "" {
		return nil
	}
	if ClassifyEmulator(wmClass(xid)) == "" {
		return nil // Nicht-Terminal, z.B. Browser — nichts zu merken
	}
	ws := scanWindows()
	win := windowByXID(ws, xid)
	if win == nil {
		return nil
	}
	w := *win
	w.Kind = procKind(cliPID)
	w.WorkDir = procCwd(cliPID)
	w.SessionID = sessionID
	w.TouchedAt = time.Now().Format(time.RFC3339)

	snap, err := LoadSnapshot(snapshotPath(dataDir))
	if err != nil {
		return err
	}
	UpsertWindow(snap, w)
	PruneMissing(snap, terminalIDs(ws))
	return SaveSnapshot(snapshotPath(dataDir), snap)
}

// Save refreshes the snapshot from the live desktop: it updates geometry of
// known windows, adds previously-unknown terminal windows as plain shells and
// drops windows the user closed. Run manually before a reboot for a precise
// "what was open at reboot" state.
func Save(dataDir string) error {
	if !haveDisplay() {
		return nil
	}
	ws := scanWindows()
	snap, err := LoadSnapshot(snapshotPath(dataDir))
	if err != nil {
		return err
	}

	// Annotate live agent sessions (daemon pidMap) — covers open sessions
	// without recent interaction; their PIDs keep living until closed.
	enrichFromDaemon(dataDir, snap, ws)

	// Known windows: refresh geometry/workspace/title only, keep session info.
	for i := range snap.Windows {
		w := &snap.Windows[i]
		if w.XID == "" {
			continue
		}
		if live := windowByXID(ws, w.XID); live != nil {
			w.updateWith(*live)
		}
	}

	// Windows without a session entry become plain shell windows.
	known := map[string]bool{}
	for _, w := range snap.Windows {
		if w.XID != "" {
			known[w.XID] = true
		}
	}
	for _, w := range ws {
		if ClassifyEmulator(wmClass(w.XID)) != "" && !known[w.XID] {
			w.Kind = "shell"
			UpsertWindow(snap, w)
		}
	}

	PruneMissing(snap, terminalIDs(ws))
	return SaveSnapshot(snapshotPath(dataDir), snap)
}

// Restore reopens all saved terminal windows and resumes their sessions.
func Restore(dataDir string) error {
	snap, err := LoadSnapshot(snapshotPath(dataDir))
	if err != nil {
		return err
	}
	if len(snap.Windows) == 0 {
		fmt.Println("Kein Terminal-Snapshot vorhanden — erst 'yesmem save-terminals' ausführen (bei offenen Fenstern).")
		return nil
	}
	before := windowIDs()
	for _, w := range snap.Windows {
		if (w.Kind == "opencode" || w.Kind == "claude") && w.SessionID != "" {
			// Bevorzugt als verwalteten Agenten starten (relay-fähig).
			if agentID, err := launchAsAgent(dataDir, w); err == nil {
				if xid := waitNewWindowAndPlace(before, w); xid != "" {
					fmt.Printf("  ✓ %s (agent %s)\n", label(w), agentID)
					before = windowIDs()
					continue
				}
			}
			// Fallback: nackter Ghostty-Start ohne Agent-Fähigkeit.
		}
		if err := restoreWindow(w, before); err != nil {
			fmt.Printf("  ✗ %s: %v\n", label(w), err)
		} else {
			fmt.Printf("  ✓ %s\n", label(w))
		}
		before = windowIDs() // für den nächsten Eintrag nur wirklich neue Fenster
	}
	return nil
}

// waitNewWindowAndPlace polls for a new window (diff vs. before) and places it.
func waitNewWindowAndPlace(before []string, w Window) string {
	xid := waitNewWindow(before, 8*time.Second)
	if xid == "" {
		return ""
	}
	time.Sleep(300 * time.Millisecond)
	place(xid, w)
	return xid
}

func label(w Window) string {
	if w.SessionID != "" {
		return fmt.Sprintf("%s (%s, %s)", short(w.SessionID), w.Kind, w.WorkDir)
	}
	return fmt.Sprintf("Shell-Fenster (%s)", w.Emulator)
}

func short(s string) string {
	if len(s) <= 12 {
		return s
	}
	return s[:12] + "…"
}
