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
// It checks the session's last message timestamp via opencode.db.
// If idle >5min: sends relay_agent poke.
// If idle >10min after poke: kills agent and respawns it.
func (h *Handler) watchPersistentAgent(section, project, sessionID string) {
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	lastPoke := time.Time{}

	for range ticker.C {
		// Check if agent exists and is running
		agent, err := h.store.AgentGetActiveBySection(project, section)
		if err != nil || agent == nil {
			log.Printf("[watchdog] agent %s missing - respawning", section)
			h.respawnPersistentAgent(section, project, sessionID)
			lastPoke = time.Time{}
			continue
		}

		// Check session activity via opencode.db
		db, err := sql.Open("sqlite3", ocDBPath)
		if err != nil {
			continue
		}

		var lastMsg int64
		err = db.QueryRow(
			"SELECT MAX(timestamp) FROM message WHERE session_id = ?",
			sessionID,
		).Scan(&lastMsg)
		db.Close()

		if err != nil || lastMsg == 0 {
			// Can't read session activity - check if process is alive instead
			if agent.PID > 0 {
				if err := syscall.Kill(agent.PID, 0); err != nil {
					log.Printf("[watchdog] agent %s process dead - respawning", section)
					h.respawnPersistentAgent(section, project, sessionID)
					lastPoke = time.Time{}
				}
			}
			continue
		}

		idle := time.Since(time.UnixMilli(lastMsg))
		if idle > idleKillThreshold {
			log.Printf("[watchdog] agent %s idle for %v - killing and restarting", section, idle.Round(time.Second))
			h.handleStopAgent(map[string]any{"to": section, "project": project})
			time.Sleep(3 * time.Second)
			h.respawnPersistentAgent(section, project, sessionID)
			lastPoke = time.Time{}
		} else if idle > idlePokeThreshold && time.Since(lastPoke) > idlePokeThreshold {
			log.Printf("[watchdog] agent %s idle for %v - sending poke", section, idle.Round(time.Second))
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
		"model":             "deepseek-chat",
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
