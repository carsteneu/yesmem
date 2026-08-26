package terminals

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// commandOut runs an external helper and returns its trimmed stdout.
// Referenced through a var so tests can stub the X11 layer.
var commandOut = func(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).Output()
	return strings.TrimSpace(string(out)), err
}

// normXID normalizes an X11 window id to canonical lowercase-hex form
// ("0x05600015" and "0x5600015" are the same window).
func normXID(s string) string {
	lower := strings.ToLower(s)
	if !strings.HasPrefix(lower, "0x") {
		return s
	}
	n, err := strconv.ParseUint(s[2:], 16, 64)
	if err != nil {
		return s
	}
	return fmt.Sprintf("0x%x", n)
}

// ParseWmctrlLine parses one `wmctrl -lG` line into a Window.
// Field layout (separated by whitespace):
//
//	<id> <desktop> <x> <y> <w> <h> <host> <title...>
//
// An empty host column (rare) still yields exactly 7 fields.
func ParseWmctrlLine(line string) (Window, bool) {
	f := strings.Fields(line)
	if len(f) < 7 {
		return Window{}, false
	}
	get := func(i int) (int, bool) {
		n, err := strconv.Atoi(f[i])
		return n, err == nil
	}
	desk, ok1 := get(1)
	x, ok2 := get(2)
	y, ok3 := get(3)
	w, ok4 := get(4)
	h, ok5 := get(5)
	if !ok1 || !ok2 || !ok3 || !ok4 || !ok5 {
		return Window{}, false
	}
	return Window{
		XID:       normXID(f[0]),
		Workspace: desk,
		X:         x,
		Y:         y,
		W:         w,
		H:         h,
		Title:     strings.Join(f[7:], " "),
	}, true
}

// scanWindows lists all X11 terminal-emulator windows with their geometry.
func scanWindows() []Window {
	out, err := commandOut("wmctrl", "-lG")
	if err != nil {
		return nil
	}
	var res []Window
	for _, line := range strings.Split(out, "\n") {
		w, ok := ParseWmctrlLine(line)
		if !ok {
			continue
		}
		w.Emulator = ClassifyEmulator(wmClass(w.XID))
		w.Maximized = xpropMaximized(w.XID)
		res = append(res, w)
	}
	return res
}

// windowIDs returns the XIDs of all currently mapped terminal windows.
func windowIDs() []string {
	var ids []string
	for _, w := range scanWindows() {
		ids = append(ids, w.XID)
	}
	return ids
}

// terminalIDs filters a scan to known emulator windows only.
func terminalIDs(ws []Window) []string {
	var ids []string
	for _, w := range ws {
		if cls := ClassifyEmulator(wmClass(w.XID)); cls != "" {
			ids = append(ids, w.XID)
		}
	}
	return ids
}

// activeWindowXID returns the currently focused X11 window id.
func activeWindowXID() string {
	out, err := commandOut("xprop", "-root", "_NET_ACTIVE_WINDOW")
	if err != nil {
		return ""
	}
	f := strings.Fields(out)
	if len(f) == 0 {
		return ""
	}
	return normXID(f[len(f)-1])
}

// wmClass returns the WM_CLASS strings of a window (instance+class).
func wmClass(xid string) []string {
	if xid == "" {
		return nil
	}
	out, err := commandOut("xprop", "-id", xid, "WM_CLASS")
	if err != nil {
		return nil
	}
	rest := out
	if i := strings.Index(out, "="); i >= 0 {
		rest = out[i+1:]
	}
	var cls []string
	for _, part := range strings.Split(rest, ",") {
		v := strings.Trim(strings.TrimSpace(part), `"`)
		if v != "" {
			cls = append(cls, v)
		}
	}
	return cls
}

// xpropMaximized reports whether the window is maximized.
func xpropMaximized(xid string) bool {
	if xid == "" {
		return false
	}
	out, err := commandOut("xprop", "-id", xid, "_NET_WM_STATE")
	if err != nil {
		return false
	}
	return strings.Contains(out, "_NET_WM_STATE_MAXIMIZED")
}

// ownerPid returns the _NET_WM_PID of a window (the process that owns it).
func ownerPid(xid string) int {
	out, err := commandOut("xprop", "-id", xid, "_NET_WM_PID")
	if err != nil {
		return 0
	}
	f := strings.Fields(out)
	if len(f) == 0 {
		return 0
	}
	n, err := strconv.Atoi(f[len(f)-1])
	if err != nil {
		return 0
	}
	return n
}

// windowByXID returns the scanned window matching xid, or nil.
func windowByXID(ws []Window, xid string) *Window {
	for i := range ws {
		if normXID(ws[i].XID) == normXID(xid) {
			return &ws[i]
		}
	}
	return nil
}
