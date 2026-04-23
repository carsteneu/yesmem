//go:build !linux && !darwin

package mcp

func processCWD(pid int) (string, bool) {
	return "", false
}
