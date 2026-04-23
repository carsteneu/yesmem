//go:build darwin

package mcp

import (
	"os/exec"
	"strconv"
	"strings"
)

func processCWD(pid int) (string, bool) {
	out, err := exec.Command("lsof", "-a", "-p", strconv.Itoa(pid), "-d", "cwd", "-Fn").Output()
	if err != nil {
		return "", false
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "n") {
			if path := strings.TrimPrefix(line, "n"); path != "" {
				return path, true
			}
		}
	}
	return "", false
}
