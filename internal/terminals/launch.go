package terminals

import (
	"fmt"
	"math"
	"os/exec"
	"strings"
	"sync"
	"time"
)

var (
	scaleOnce   sync.Once
	cachedScale float64
)

// displayScale returns how much window-manager logic coordinates are scaled to
// physical pixels on this display (Cinnamon multiplies absolute x/y values
// passed to wmctrl -e by this factor). Measured once per process by opening a
// throwaway window and reading back the actually applied position.
func displayScale() float64 {
	scaleOnce.Do(func() { cachedScale = measureScale() })
	return cachedScale
}

func measureScale() float64 {
	const want = 100
	cmd := exec.Command("ghostty", "+new-window", "--title=YMSSCALE")
	if err := cmd.Start(); err != nil {
		return 1
	}
	xid := waitForTitle("YMSSCALE", 4*time.Second)
	if xid == "" {
		return 1
	}
	defer commandOut("wmctrl", "-ic", xid)

	commandOut("wmctrl", "-i", "-r", xid, "-e", fmt.Sprintf("0,%d,%d,400,300", want, want))
	time.Sleep(600 * time.Millisecond)
	for _, w := range scanWindows() {
		if w.XID == xid && w.X > 0 {
			s := float64(w.X) / float64(want)
			if s >= 0.5 {
				return s
			}
			return 1
		}
	}
	return 1
}

// restoreWindow opens one saved window via Ghostty and moves it to its
// saved position/workspace. `before` is the set of window ids that already
// existed, so the newly created window can be identified by diff.
func restoreWindow(w Window, before []string) error {
	args := launchArgs(w)
	cmd := exec.Command("ghostty", args...)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("ghostty start: %w", err)
	}

	xid := waitNewWindow(before, 6*time.Second)
	if xid == "" {
		return fmt.Errorf("kein neues Ghostty-Fenster erkannt")
	}
	time.Sleep(300 * time.Millisecond) // Fenster zuerst aufbauen lassen, sonst verpufft wmctrl -e
	place(xid, w)
	return nil
}

// launchArgs builds the ghostty invocation for a window. Sessions (opencode/
// claude) are started directly with -e; plain shell windows use +new-window.
func launchArgs(w Window) []string {
	if ra := resumeArgs(w); len(ra) > 0 {
		return append([]string{"-e"}, ra...)
	}
	if w.WorkDir != "" {
		return []string{"+new-window", "--working-directory=" + w.WorkDir}
	}
	return []string{"+new-window"}
}

// waitNewWindow polls the window list until a window id appears that was not
// present in `before`.
func waitNewWindow(before []string, timeout time.Duration) string {
	old := make(map[string]bool, len(before))
	for _, id := range before {
		old[normXID(id)] = true
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		for _, id := range windowIDs() {
			if !old[normXID(id)] {
				return normXID(id)
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	return ""
}

// waitForTitle polls until a window with the given title appears.
func waitForTitle(title string, timeout time.Duration) string {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		for _, w := range scanWindows() {
			if strings.Contains(w.Title, title) {
				return w.XID
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	return ""
}

// place moves the window to its saved geometry and workspace.
func place(xid string, w Window) {
	if w.X != 0 || w.Y != 0 || w.W != 0 || w.H != 0 {
		scale := displayScale()
		gx := int(math.Round(float64(w.X) / scale))
		gy := int(math.Round(float64(w.Y) / scale))
		commandOut("wmctrl", "-i", "-r", xid, "-e", fmt.Sprintf("0,%d,%d,%d,%d", gx, gy, w.W, w.H))
	}
	if w.Workspace >= 0 {
		commandOut("wmctrl", "-i", "-r", xid, "-t", fmt.Sprintf("%d", w.Workspace))
	}
	if w.Maximized {
		commandOut("wmctrl", "-i", "-r", xid, "-b", "add,maximized_vert,maximized_horz")
	}
}
