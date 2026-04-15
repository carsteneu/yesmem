package hooks

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/carsteneu/yesmem/internal/daemon"
)

// RunIdleTick reads UserPromptSubmit hook JSON from stdin, calls daemon idle_tick,
// and outputs an escalating reminder if Claude hasn't used yesmem recently.
// Falls back to static micro-reminder if daemon is unreachable.
func RunIdleTick(dataDir string) {
	input, err := io.ReadAll(os.Stdin)
	if err != nil {
		return
	}

	var hook HookInput
	if json.Unmarshal(input, &hook) != nil {
		return
	}

	sessionID := hook.SessionID
	if sessionID == "" {
		sessionID = "unknown"
	}

	client, err := daemon.Dial(dataDir)
	if err != nil {
		outputStaticReminder()
		return
	}
	defer client.Close()

	result, err := client.Call("idle_tick", map[string]any{
		"session_id": sessionID,
	})
	if err != nil {
		outputStaticReminder()
		return
	}

	var tick struct {
		Count    int    `json:"count"`
		Reminder string `json:"reminder"`
	}
	if json.Unmarshal(result, &tick) != nil {
		return
	}

	if tick.Reminder == "" {
		return
	}

	out := map[string]any{
		"hookSpecificOutput": map[string]any{
			"additionalContext": tick.Reminder,
		},
	}
	jsonOut, _ := json.Marshal(out)
	fmt.Print(string(jsonOut))
}

func outputStaticReminder() {
	reminder := `Du hast ein Langzeitgedaechtnis (yesmem). Bei JEDER nicht-trivialen Aufgabe: ZUERST search(thema). Wenn du etwas Wichtiges entdeckst: remember(text, category).`
	out := map[string]any{
		"hookSpecificOutput": map[string]any{
			"additionalContext": reminder,
		},
	}
	jsonOut, _ := json.Marshal(out)
	fmt.Print(string(jsonOut))
}
