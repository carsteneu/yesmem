//go:build linux

package mcp

import (
	"fmt"
	"os"
)

func processCWD(pid int) (string, bool) {
	cwd, err := os.Readlink(fmt.Sprintf("/proc/%d/cwd", pid))
	if err != nil {
		return "", false
	}
	return cwd, true
}
