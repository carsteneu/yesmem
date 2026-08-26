package terminals

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/carsteneu/yesmem/internal/daemon"
)

// stripAgentPrefix converts daemon session ids like "opencode:ses_abc" to the
// bare id expected by the agent CLI resume flag.
func stripAgentPrefix(sid string) string {
	for _, p := range []string{"opencode:", "claude:", "codex:"} {
		if len(sid) >= len(p) && sid[:len(p)] == p {
			return sid[len(p):]
		}
	}
	return sid
}

// ancestors returns the PID chain of a process, oldest last (including pid).
func ancestors(pid int) []int {
	var chain []int
	seen := map[int]bool{}
	for pid > 1 && !seen[pid] {
		seen[pid] = true
		chain = append(chain, pid)
		data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
		if err != nil {
			break
		}
		// Comm can contain spaces/parens; fields after the last ')' are
		// "<state> <ppid> ...".
		rest := string(data)
		if i := indexAfterParen(rest); i >= 0 {
			f := strings.Fields(rest[i:])
			if len(f) >= 2 {
				if ppid, err := strconv.Atoi(f[1]); err == nil {
					pid = ppid
					continue
				}
			}
		}
		break
	}
	return chain
}

// indexAfterParen returns the byte offset after the closing ')' of comm.
func indexAfterParen(s string) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == ')' {
			return i + 1
		}
	}
	return -1
}

func pidSet(ids []int) map[int]bool {
	m := make(map[int]bool, len(ids))
	for _, p := range ids {
		m[p] = true
	}
	return m
}

// matchUniqueWindow returns the single window whose owner PID is in ancestors.
// nil if none or ambiguous (e.g. several Ghostty windows share one process).
func matchUniqueWindow(ws []Window, owners map[string]int, anc map[int]bool) *Window {
	var found *Window
	for i := range ws {
		o, ok := owners[ws[i].XID]
		if ok && anc[o] {
			if found != nil {
				return nil
			}
			found = &ws[i]
		}
	}
	return found
}

// enrichFromDaemon annotates open agent sessions (daemon pidMap) onto the
// snapshot: kind/workdir come from /proc of the live PID, the window from the
// daemon windowMap or a unique owner-PID match. This covers sessions that were
// left open without interaction (their PIDs keep living until closed).
func enrichFromDaemon(dataDir string, snap *Snapshot, ws []Window) {
	client, err := daemon.Dial(dataDir)
	if err != nil {
		return
	}
	defer client.Close()
	raw, err := client.Call("list_registrations", map[string]any{})
	if err != nil {
		return
	}
	var data map[string]any
	if err := json.Unmarshal(raw, &data); err != nil {
		return
	}
	sessions, _ := data["sessions"].(map[string]any)
	if len(sessions) == 0 {
		return
	}
	daemonWindows, _ := data["windows"].(map[string]any)

	owners := ownerMap(ws)
	for sid, pv := range sessions {
		pid := int(pv.(float64))
		kind := procKind(pid)
		if kind == "" {
			continue // keine Agent-CLI (Shell, Hilfsprozess)
		}
		sess := stripAgentPrefix(sid)
		if sess == "" {
			sess = sid
		}
		w := Window{
			Kind:      kind,
			WorkDir:   procCwd(pid),
			SessionID: sess,
		}
		if wx, ok := daemonWindows[sid].(string); ok && wx != "" {
			w.XID = normXID(wx)
		} else if win := matchUniqueWindow(ws, owners, pidSet(ancestors(pid))); win != nil {
			w.XID = win.XID
		}
		// Fenster-Geometrie für bekannte XIDs anreichern
		if w.XID != "" {
			if live := windowByXID(ws, w.XID); live != nil {
				w.Emulator = live.Emulator
				w.Workspace = live.Workspace
				w.X, w.Y, w.W, w.H = live.X, live.Y, live.W, live.H
				w.Title = live.Title
				w.Maximized = live.Maximized
			}
		}
		UpsertWindow(snap, w)
	}
}

// launchAsAgent asks the daemon to open a session window as a managed agent
// (PTY bridge + relay/stop/resume). Returns the new agent id.
func launchAsAgent(dataDir string, w Window) (string, error) {
	client, err := daemon.Dial(dataDir)
	if err != nil {
		return "", err
	}
	defer client.Close()
	raw, err := client.Call("open_agent_terminal", map[string]any{
		"session_id": w.SessionID,
		"work_dir":   w.WorkDir,
		"backend":    w.Kind,
		"project":    w.WorkDir, // immer Absolutpfad — Short-Names sind mehrdeutig
	})
	if err != nil {
		return "", err
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", err
	}
	id, _ := out["agent_id"].(string)
	if id == "" {
		return "", fmt.Errorf("daemon returned no agent_id")
	}
	return id, nil
}

// SpawnNewTerminal starts a brand-new terminal session (open) in workDir as a
// managed agent via the daemon. Returns the agent id.
func SpawnNewTerminal(dataDir, workDir, backend string) (string, error) {
	if backend == "" {
		backend = "opencode"
	}
	client, err := daemon.Dial(dataDir)
	if err != nil {
		return "", err
	}
	defer client.Close()
	raw, err := client.Call("open_agent_terminal", map[string]any{
		"work_dir": workDir,
		"backend":  backend,
		"project":  workDir, // immer Absolutpfad — Short-Names sind mehrdeutig
	})
	if err != nil {
		return "", err
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", err
	}
	id, _ := out["agent_id"].(string)
	if id == "" {
		return "", fmt.Errorf("daemon returned no agent_id")
	}
	return id, nil
}

// ownerMap caches each window's _NET_WM_PID.
func ownerMap(ws []Window) map[string]int {
	m := make(map[string]int, len(ws))
	for _, w := range ws {
		m[w.XID] = ownerPid(w.XID)
	}
	return m
}
