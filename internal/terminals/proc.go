package terminals

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// procKind maps a running process to the session kind we know how to resume.
func procKind(pid int) string {
	if pid <= 0 {
		return ""
	}
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "comm"))
	if err != nil {
		return ""
	}
	comm := strings.TrimSpace(string(data))
	switch {
	case strings.Contains(comm, "opencode"):
		return "opencode"
	case strings.Contains(comm, "claude"):
		return "claude"
	}
	return ""
}

// procCwd returns the working directory of a process, or "".
func procCwd(pid int) string {
	if pid <= 0 {
		return ""
	}
	wd, err := os.Readlink(filepath.Join("/proc", strconv.Itoa(pid), "cwd"))
	if err != nil {
		return ""
	}
	return wd
}
