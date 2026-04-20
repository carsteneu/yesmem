package setup

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const DefaultExtractionModel = "sonnet"

// detectUserTypeDefault picks a sensible default for the user-type prompt.
// An explicit ANTHROPIC_API_KEY env beats everything else; otherwise CLI
// (which also covers Claude Code subscription users with oauthAccount).
func detectUserTypeDefault(home, envKey string) string {
	if envKey != "" {
		return "api"
	}
	return "cli"
}

// permanentDirs lists directories that are considered permanent (not Downloads, tmp, etc.)
var permanentDirs = []string{
	"/usr/local/bin",
	"/usr/bin",
	".local/bin",
	"bin",
	"go/bin",
}

// ensurePermanentLocation checks if the binary is in a permanent location.
// If not, automatically copies it to ~/.local/bin (the standard user binary path).
func ensurePermanentLocation(home, binaryPath string) string {
	absPath, err := filepath.Abs(binaryPath)
	if err != nil {
		return binaryPath
	}

	// Resolve symlinks
	resolved, err := filepath.EvalSymlinks(absPath)
	if err == nil {
		absPath = resolved
	}

	if isPermanentLocation(home, absPath) {
		fmt.Printf("  ✓ Binary location: %s\n", absPath)
		ensureInPath(home, filepath.Dir(absPath))
		return absPath
	}

	// Not in a permanent location — copy to ~/.local/bin automatically
	dest := filepath.Join(home, ".local", "bin", "yesmem")
	os.MkdirAll(filepath.Dir(dest), 0755)
	var copyErr error
	withSpinner("Installing to "+dest, func() (string, error) {
		if err := copyBinary(absPath, dest); err != nil {
			if strings.Contains(err.Error(), "text file busy") {
				exec.Command("systemctl", "--user", "stop", "yesmem-proxy").Run()
				exec.Command("systemctl", "--user", "stop", "yesmem").Run()
				selfPid := fmt.Sprintf("%d", os.Getpid())
				exec.Command("bash", "-c", fmt.Sprintf("pgrep -x yesmem | grep -v %s | xargs -r kill", selfPid)).Run()
				time.Sleep(1 * time.Second)
				if err := copyBinary(absPath, dest); err != nil {
					copyErr = err
					return "", fmt.Errorf("%v — keeping at %s", err, absPath)
				}
			} else {
				copyErr = err
				return "", fmt.Errorf("%v — keeping at %s", err, absPath)
			}
		}
		return "", nil
	})
	if copyErr != nil {
		ensureInPath(home, filepath.Dir(absPath))
		return absPath
	}
	ensureInPath(home, filepath.Dir(dest))
	return dest
}

func isPermanentLocation(home, path string) bool {
	lowerPath := strings.ToLower(path)

	// Definitely not permanent
	notPermanent := []string{"download", "tmp", "temp", "desktop", "/tmp/"}
	for _, np := range notPermanent {
		if strings.Contains(lowerPath, np) {
			return false
		}
	}

	// Known permanent locations
	for _, dir := range permanentDirs {
		var fullDir string
		if filepath.IsAbs(dir) {
			fullDir = dir
		} else {
			fullDir = filepath.Join(home, dir)
		}
		if strings.HasPrefix(path, fullDir) {
			return true
		}
	}

	// If it's somewhere in home but not Downloads/tmp, it's probably fine
	if strings.HasPrefix(path, home) {
		return true
	}

	return true // Anywhere else, assume the user knows what they're doing
}

func copyBinary(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0755)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}

func ensureInPath(home, dir string) {
	path := os.Getenv("PATH")
	if strings.Contains(path, dir) {
		return
	}

	envLine := fmt.Sprintf("export PATH=\"%s:$PATH\"", dir)
	marker := dir

	for _, rcFile := range []string{".bashrc", ".zshrc", ".profile"} {
		rcPath := filepath.Join(home, rcFile)
		data, err := os.ReadFile(rcPath)
		if err != nil {
			continue
		}
		if strings.Contains(string(data), marker) {
			continue
		}
		f, err := os.OpenFile(rcPath, os.O_APPEND|os.O_WRONLY, 0644)
		if err != nil {
			continue
		}
		fmt.Fprintf(f, "\n# YesMem binary\n%s\n", envLine)
		f.Close()
	}
	fmt.Printf("  Added %s to PATH in shell config ✓\n", dir)
}
