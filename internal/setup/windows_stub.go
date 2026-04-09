//go:build !windows

package setup

import "fmt"

func setupWindows(binPath string) error {
	return fmt.Errorf("Windows autostart not supported on this platform")
}
