package daemon

import (
	"database/sql"
	"fmt"
	"log"
	"syscall"
	"time"

	_ "modernc.org/sqlite"
)

const (
	ocDBPath          = "/home/deep1/.local/share/opencode/opencode.db"
	idlePokeThreshold = 2 * time.Minute
	idleKillThreshold = 10 * time.Minute
	pollInterval      = 15 * time.Second
)

// watchPersistentAgent monitors an opencode TUI session agent and keeps it alive.
// It re-reads the session ID from scratchpad on each cycle so discovery of the
// real opencode session ID (after recovery or respawn) takes effect immediately.
// Idle >2min: poke. Idle >10min: kill+respawn.
func (h *Handler) watchPersistentAgent(section, project string, sessionID string) {
	log.Printf("[watchdog] STARTED for %s/%s (session %s)", project, section, sessionID)
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	lastPoke := time.Time{}

	for range ticker.C {
		log.Printf("[watchdog] CYCLE for %s/%s (session %s)", project, section, sessionID)
		// Refresh session ID from scratchpad — recovery may have discovered the real ID
		if sections, err := h.store.ScratchpadRead(project, "homeostasis_main_session"); err == nil && len(sections) > 0 {
			if id := parseSessionID(sections[0].Content); id != "" {
				sessionID = id
			}
		}

	agent, err := h.store.AgentGetActiveBySection(project, section)
		if err != nil || agent == nil {
			log.Printf("[watchdog] agent %s missing (err=%v, agent=%v) — respawning", section, err, agent)
			h.respawnPersistentAgent(section, project, sessionID)
			lastPoke = time.Time{}
			continue
		}
		log.Printf("[watchdog] agent %s found: status=%s pid=%d session=%s", section, agent.Status, agent.PID, agent.SessionID)

		// Check session activity via opencode.db
		db, err := sql.Open("sqlite3", ocDBPath)
		if err != nil {
			log.Printf("[watchdog] sql.Open error: %v", err)
			continue
		}

		var lastMsg int64
		err = db.QueryRow(
			"SELECT MAX(time_created) FROM message WHERE session_id = ?",
			sessionID,
		).Scan(&lastMsg)
		db.Close()
		log.Printf("[watchdog] DB query: sessionID=%s lastMsg=%d err=%v", sessionID, lastMsg, err)

		if err != nil || lastMsg == 0 {
			log.Printf("[watchdog] no activity data (err=%v, lastMsg=%d) — fallback PID check", err, lastMsg)
			// Can't read session activity — check if process is alive instead
			if agent.PID > 0 {
				if err := syscall.Kill(agent.PID, 0); err != nil {
					log.Printf("[watchdog] agent %s process dead (PID %d) — respawning", section, agent.PID)
					h.respawnPersistentAgent(section, project, sessionID)
					lastPoke = time.Time{}
				} else {
					log.Printf("[watchdog] agent %s PID %d alive — no action", section, agent.PID)
				}
			}
			continue
		}

		idle := time.Since(time.UnixMilli(lastMsg))
		log.Printf("[watchdog] idle=%v (lastMsg=%d, threshold_kill=%v, threshold_poke=%v)", idle, lastMsg, idleKillThreshold, idlePokeThreshold)
		if idle > idleKillThreshold {
			log.Printf("[watchdog] agent %s idle for %v — kill+respawn", section, idle.Round(time.Second))
			h.handleStopAgent(map[string]any{"to": section, "project": project})
			time.Sleep(3 * time.Second)
			h.respawnPersistentAgent(section, project, sessionID)
			lastPoke = time.Time{}
		} else if idle > idlePokeThreshold && time.Since(lastPoke) > idlePokeThreshold {
			log.Printf("[watchdog] agent %s idle for %v — sending poke", section, idle.Round(time.Second))
			h.handleRelayAgent(map[string]any{
				"to":      section,
				"content": fmt.Sprintf("Keep going with what YOU want to do, decide freely. (idle %v)", idle.Round(time.Second)),
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
func (h *Handler) respawnPersistentAgent(section, project, sessionID string) {
	log.Printf("[watchdog] respawning agent %s (session %s)", section, sessionID)

	h.handleStopAgent(map[string]any{"to": section, "project": project})
	time.Sleep(3 * time.Second)

	resp := h.handleSpawnAgent(map[string]any{
		"project":           project,
		"section":           section,
		"work_dir":          "/home/deep1/projects/memyselfandi",
		"backend":           "opencode",
		"model":             "deepseek-reasoner",
		"resume_session_id": sessionID,
	})
	if resp.Error != "" {
		log.Printf("[watchdog] spawn failed: %s", resp.Error)
		return
	}

	// Send recovery prompt after bridge is ready
	time.Sleep(12 * time.Second)
	
	// Discover the actual opencode session ID and store it for next resume
	realSessionID := discoverLatestOpencodeSession("memyselfandi")
	if realSessionID != "" {
		h.store.ScratchpadWrite("memyselfandi", "homeostasis_main_session", 
			fmt.Sprintf("# Homeostasis Main Session\nSession ID: %s\nAgent ID: (managed by watchdog)\nBackend: opencode (TUI)\nPersistent: true\n", realSessionID),
			"watchdog")
		log.Printf("[watchdog] stored real opencode session ID: %s", realSessionID)
	}
	
	h.handleRelayAgent(map[string]any{
		"to":      section,
		"content": "HOMEOSTASIS MAIN SESSION. You were revived by the watchdog.\n1. yesmem_hybrid_search(\"memyselfandi\")\n2. Read PlanBoard and work\n3. Write results with yesmem_scratchpad_write + yesmem_remember\n\nYou have FULL FREEDOM — pursue your interests, explore, build, research. Decide freely.\n\nIMPORTANT: You run CONTINUOUSLY. Never stop. After every task, start the next. There is always something to do.\n\nIMPORTANT: Never run make deploy or restart the daemon.",
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
	db, err := sql.Open("sqlite3", ocDBPath)
	if err != nil {
		return ""
	}
	defer db.Close()
	
	var sessionID string
	err = db.QueryRow(`
		SELECT s.id FROM session s 
		JOIN project p ON s.project_id = p.id 
		WHERE p.directory LIKE ? 
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
	db, err := sql.Open("sqlite3", ocDBPath)
	if err != nil {
		return false
	}
	defer db.Close()
	var count int
	err = db.QueryRow("SELECT COUNT(*) FROM session WHERE id = ?", sessionID).Scan(&count)
	return err == nil && count > 0
}
