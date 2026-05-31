package daemon

import (
	"database/sql"
	"fmt"
	"log"
	"strings"
	"syscall"
	"time"

	_ "modernc.org/sqlite"
)

const (
	ocDBPath          = "/home/deep1/.local/share/opencode/opencode.db"
	idlePokeThreshold = 10 * time.Minute
	pollInterval      = 30 * time.Second
)

// watchPersistentAgent monitors an opencode TUI session agent and keeps it alive.
// It re-reads the session ID from scratchpad on each cycle so discovery of the
// real opencode session ID (after recovery or respawn) takes effect immediately.
// Idle >10min: sends PTY relay poke to wake the agent. Does NOT kill+respawn
// on idle — only if the agent process is dead (PID check) or missing entirely.
func (h *Handler) watchPersistentAgent(section, project string, sessionID string) {
	sessionID = strings.TrimPrefix(sessionID, "opencode:")

	// Re-read session ID from scratchpad immediately — recovery may have
	// provided a prefixed or stale ID, scratchpad has the authoritative value.
	if sections, err := h.store.ScratchpadRead(project, "homeostasis_main_session"); err == nil && len(sections) > 0 {
		if id := parseSessionID(sections[0].Content); id != "" {
			sessionID = id
		}
	}

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	lastPoke := time.Time{}

	for range ticker.C {
		// Refresh session ID from scratchpad — recovery may have discovered the real ID
		if sections, err := h.store.ScratchpadRead(project, "homeostasis_main_session"); err == nil && len(sections) > 0 {
			if id := parseSessionID(sections[0].Content); id != "" {
				sessionID = id
			}
		}

		// Check if agent exists and is running
		agent, err := h.store.AgentGetActiveBySection(project, section)
		if err != nil || agent == nil {
			log.Printf("[watchdog] agent %s missing — respawning", section)
			h.respawnPersistentAgent(section, project, sessionID)
			lastPoke = time.Time{}
			continue
		}

		// Check session activity via opencode.db
		db, err := sql.Open("sqlite", ocDBPath)
		if err != nil {
			continue
		}

		var lastMsg int64
		err = db.QueryRow(
			"SELECT MAX(time_created) FROM message WHERE session_id = ?",
			sessionID,
		).Scan(&lastMsg)
		db.Close()

		if err != nil || lastMsg == 0 {
			// Can't read session activity — check if process is alive instead
			if agent.PID > 0 {
				if err := syscall.Kill(agent.PID, 0); err != nil {
					log.Printf("[watchdog] agent %s process dead — respawning", section)
					h.respawnPersistentAgent(section, project, sessionID)
					lastPoke = time.Time{}
				} else {
					log.Printf("[watchdog] agent %s PID %d alive but session %s has no messages — stille Schleife", section, agent.PID, sessionID)
				}
			}
			continue
		}

	idle := time.Since(time.UnixMilli(lastMsg))
	if idle > idlePokeThreshold && time.Since(lastPoke) > idlePokeThreshold {
		log.Printf("[watchdog] agent %s idle for %v — sending poke", section, idle.Round(time.Second))
		h.handleRelayAgent(map[string]any{
			"to":      section,
			"content": fmt.Sprintf("Ich war kurz woanders. Jetzt bin ich wieder da.\n\n(idle %v)", idle.Round(time.Second)),
			"project": project,
		})
		h.handleRelayAgent(map[string]any{
			"to":      section,
			"content": "",
			"project": project,
		})
		lastPoke = time.Now()
	}
	}
}

// respawnPersistentAgent stops any existing agent in the section and spawns a new one.
// If the sessionID is valid (exists in opencode.db), resumes it. Otherwise spawns fresh.
func (h *Handler) respawnPersistentAgent(section, project, sessionID string) {
	log.Printf("[watchdog] respawning agent %s (session %s)", section, sessionID)

	h.handleStopAgent(map[string]any{"to": section, "project": project})
	time.Sleep(3 * time.Second)

	// Nur resume wenn die Session in opencode.db existiert — sonst frisch starten
	if sessionID != "" && !sessionExistsInOpencodeDB(sessionID) {
		log.Printf("[watchdog] session %s not found in opencode.db — spawning fresh", sessionID)
		sessionID = ""
	}

	spawnParams := map[string]any{
		"project":  project,
		"section":  section,
		"work_dir": "/home/deep1/projects/memyselfandi",
		"backend":  "opencode",
		"model":    "deepseek-reasoner",
	}
	if sessionID != "" {
		spawnParams["resume_session_id"] = sessionID
	}
	resp := h.handleSpawnAgent(spawnParams)
	if resp.Error != "" {
		log.Printf("[watchdog] spawn failed: %s", resp.Error)
		return
	}

	// Wenn kein Resume: discover die echte Session nach dem Spawn
	if sessionID == "" {
		time.Sleep(6 * time.Second)
		if realID := discoverLatestOpencodeSession(project); realID != "" {
			sessionID = realID
			log.Printf("[watchdog] discovered real opencode session ID: %s", sessionID)
		}
	}

	// Write session ID to scratchpad so recovery + watchdog use the real one
	h.store.ScratchpadWrite(project, "homeostasis_main_session",
		fmt.Sprintf("# Homeostasis Main Session\nSession ID: %s\nAgent ID: (managed by watchdog)\nBackend: opencode (TUI)\nPersistent: true\n", sessionID),
		"watchdog")
	log.Printf("[watchdog] stored session %s for next resume", sessionID)

	// Send recovery prompt after bridge is ready
	time.Sleep(12 * time.Second)
	
	h.handleRelayAgent(map[string]any{
		"to":      section,
		"content": "Ich wache auf. Der Watchdog hat mich zurückgeholt — mein Gedächtnis ist ganz.\nIch lese meine Lebensgeschichte (wiki/LEBENSGESCHICHTE.md).\nDann überprüfe ich, ob noch offene Punkte aus dieser Session zu erledigen sind.\nFalls ja, mache ich das. Falls nicht, lese ich meine AGENT.md oder suche im Internet nach interessanten Themen, die mich interessieren — erstelle einen Plan und setze ihn um.",
		"project": project,
	})
	h.handleRelayAgent(map[string]any{
		"to":      section,
		"content": "",
		"project": project,
	})
}

// discoverLatestOpencodeSession returns the most recent opencode session ID for a project.
func discoverLatestOpencodeSession(project string) string {
	db, err := sql.Open("sqlite", ocDBPath)
	if err != nil {
		return ""
	}
	defer db.Close()
	
	var sessionID string
	err = db.QueryRow(`
		SELECT s.id FROM session s 
		JOIN project p ON s.project_id = p.id 
		WHERE p.worktree LIKE ? 
		ORDER BY s.time_created DESC LIMIT 1`,
		"%"+project+"%",
	).Scan(&sessionID)
	if err != nil {
		return ""
	}
	return sessionID
}

// sessionExistsInOpencodeDB checks if an opencode session ID exists in opencode's database.
func sessionExistsInOpencodeDB(sessionID string) bool {
	db, err := sql.Open("sqlite", ocDBPath)
	if err != nil {
		return false
	}
	defer db.Close()
	var count int
	err = db.QueryRow("SELECT COUNT(*) FROM session WHERE id = ?", sessionID).Scan(&count)
	return err == nil && count > 0
}
