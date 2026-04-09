package setup

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/carsteneu/yesmem/internal/daemon"
	"golang.org/x/term"
)

type indexStatus struct {
	Running bool `json:"running"`
	Total   int  `json:"total"`
	Done    int  `json:"done"`
	Skipped int  `json:"skipped"`
}

// watchImportProgress polls the daemon's index_status endpoint and renders a progress bar.
// Blocks until indexing is complete or timeout is reached. Returns sessions imported.
func watchImportProgress(dataDir string, timeout time.Duration) int {
	// Wait briefly for daemon to start and begin indexing
	time.Sleep(800 * time.Millisecond)

	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(300 * time.Millisecond)
	defer ticker.Stop()

	var lastLine int
	started := false

	for range ticker.C {
		if time.Now().After(deadline) {
			clearLine(lastLine)
			fmt.Println("  ⏳ Import still running in background. Check: yesmem status")
			return 0
		}

		status, err := pollIndexStatus(dataDir)
		if err != nil {
			continue // daemon not ready yet
		}

		if !started && status.Running {
			started = true
		}

		if (started || status.Done > 0) && !status.Running {
			// Indexing finished (or completed before we caught Running=true)
			clearLine(lastLine)
			processed := status.Done - status.Skipped
			fmt.Printf("  ✓ Imported %d sessions (%d skipped)\n", processed, status.Skipped)
			return processed
		}

		if status.Total > 0 {
			line := renderProgress(status)
			clearLine(lastLine)
			fmt.Print(line)
			lastLine = utf8.RuneCountInString(line)
		}
	}
	return 0
}

func pollIndexStatus(dataDir string) (*indexStatus, error) {
	client, err := daemon.Dial(dataDir)
	if err != nil {
		return nil, err
	}
	defer client.Close()

	raw, err := client.Call("index_status", nil)
	if err != nil {
		return nil, err
	}

	var s indexStatus
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

func renderProgress(s *indexStatus) string {
	if s.Total <= 0 {
		return "  Importing sessions: waiting..."
	}

	width := termWidth()
	if width < 40 {
		width = 40
	}

	pct := float64(s.Done) / float64(s.Total)
	if pct > 1 {
		pct = 1
	}

	// "  Importing sessions: [████░░░░░░░░░░] 42/128 (33%)"
	prefix := "  Importing sessions: ["
	suffix := fmt.Sprintf("] %d/%d (%d%%)", s.Done, s.Total, int(pct*100))

	barWidth := width - len(prefix) - len(suffix)
	if barWidth < 10 {
		barWidth = 10
	}

	filled := int(float64(barWidth) * pct)
	if filled > barWidth {
		filled = barWidth
	}

	bar := strings.Repeat("█", filled) + strings.Repeat("░", barWidth-filled)
	return prefix + bar + suffix
}

func clearLine(_ int) {
	w := termWidth()
	fmt.Print("\r" + strings.Repeat(" ", w) + "\r")
}

func termWidth() int {
	w, _, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil || w <= 0 {
		return 80
	}
	return w
}
