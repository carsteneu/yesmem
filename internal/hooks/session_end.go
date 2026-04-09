package hooks

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/carsteneu/yesmem/internal/daemon"
)

// SessionEndInput represents the JSON Claude Code sends for SessionEnd.
type SessionEndInput struct {
	SessionID string `json:"session_id"`
	CWD       string `json:"cwd"`
	Reason    string `json:"reason"` // "clear", "compact", "logout", etc.
}

// RunSessionEnd reads SessionEnd JSON from stdin and tracks the session end
// via the daemon socket. This avoids DB write-lock contention that caused
// the hook to hang when the daemon held long write transactions.
func RunSessionEnd(dataDir string) {
	var input SessionEndInput
	if err := json.NewDecoder(os.Stdin).Decode(&input); err != nil {
		fmt.Print("{}")
		return
	}

	if input.Reason != "clear" && input.Reason != "compact" {
		fmt.Print("{}")
		return
	}

	project := input.CWD
	if project == "" {
		project = os.Getenv("CLAUDE_PROJECT_DIR")
		if project == "" {
			project = os.Getenv("PWD")
		}
	}

	if input.SessionID == "" || project == "" {
		fmt.Print("{}")
		return
	}

	client, err := daemon.DialTimeout(dataDir, 3*time.Second)
	if err != nil {
		fmt.Print("{}")
		return
	}
	defer client.Close()
	client.SetDeadline(time.Now().Add(3 * time.Second))

	client.Call("track_session_end", map[string]any{
		"project":    project,
		"session_id": input.SessionID,
		"reason":     input.Reason,
	})

	fmt.Print("{}")
}
