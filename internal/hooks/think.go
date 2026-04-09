package hooks

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/carsteneu/yesmem/internal/daemon"
)

// RunThink registers the session PID with the daemon for MCP session resolution.
// Dialog injection is handled by the proxy (via metadata.session_id extraction).
func RunThink(dataDir string) {
	var input struct {
		SessionID string `json:"session_id"`
		ToolName  string `json:"tool_name"`
	}
	if err := json.NewDecoder(os.Stdin).Decode(&input); err != nil {
		return
	}

	// Only on UserPromptSubmit (not PreToolUse)
	if input.ToolName != "" || input.SessionID == "" || input.SessionID == "unknown" {
		return
	}

	pid := os.Getppid()
	registerPID(dataDir, input.SessionID, pid)
	writePIDFile(dataDir, input.SessionID, pid)
}

// registerPID tells the daemon which OS PID belongs to this session.
func registerPID(dataDir, sessionID string, pid int) {
	client, err := daemon.Dial(dataDir)
	if err != nil {
		return
	}
	defer client.Close()
	client.Call("register_pid", map[string]any{"session_id": sessionID, "pid": float64(pid)})
}

// writePIDFile persists PID→session_id mapping to disk so MCP servers
// can resolve their session after daemon restarts.
func writePIDFile(dataDir, sessionID string, pid int) {
	dir := filepath.Join(dataDir, "sessions")
	os.MkdirAll(dir, 0700)
	os.WriteFile(filepath.Join(dir, fmt.Sprintf("%d", pid)), []byte(sessionID), 0600)
}
