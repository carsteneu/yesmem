package setup

import (
	"bufio"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

var (
	// Matches: "[3/5] ✓ abc12345 (memory)" or "[2/5] OK abc12345"
	reProgress = regexp.MustCompile(`\[(\d+)/(\d+)\]\s+(✓|OK|⚠|WARN)`)
	// Matches: "━━━ Phase 2: Extraction (5 sessions) ━━━"
	rePhase = regexp.MustCompile(`━━━\s+Phase\s+(\S+):\s+(.+?)\s+━━━`)
	// Matches: "━━━ Quickstart complete (2m30s) ━━━"
	reComplete = regexp.MustCompile(`━━━\s+Quickstart complete`)
)

// quickExtract runs "yesmem quickstart --last N" as a subprocess and shows progress.
// Returns true if quickstart completed successfully.
func quickExtract(binaryPath string, limit int, timeout time.Duration) bool {
	cmd := exec.Command(binaryPath, "quickstart", "--last", fmt.Sprintf("%d", limit))
	cmd.Stdout = nil

	stderr, err := cmd.StderrPipe()
	if err != nil {
		fmt.Printf("  ⚠ Quickstart: %v\n", err)
		return false
	}

	if err := cmd.Start(); err != nil {
		fmt.Printf("  ⚠ Quickstart: %v\n", err)
		return false
	}

	scanner := bufio.NewScanner(stderr)
	var (
		lastLine     int
		currentPhase string
		phaseCount   int
		completed    bool
	)

	// Timeout watchdog
	doneCh := make(chan struct{})
	go func() {
		select {
		case <-doneCh:
		case <-time.After(timeout):
			cmd.Process.Kill()
		}
	}()

	for scanner.Scan() {
		line := scanner.Text()

		// Phase header
		if m := rePhase.FindStringSubmatch(line); m != nil {
			phaseCount++
			currentPhase = m[2]
			// Strip parenthetical details for clean display
			if idx := strings.Index(currentPhase, "("); idx > 0 {
				currentPhase = strings.TrimSpace(currentPhase[:idx])
			}
			clearLine(lastLine)
			display := fmt.Sprintf("  [%d/7] %s...", phaseCount, currentPhase)
			fmt.Print(display)
			lastLine = utf8.RuneCountInString(display)
			continue
		}

		// Per-session progress within a phase
		if m := reProgress.FindStringSubmatch(line); m != nil {
			var done, total int
			fmt.Sscanf(m[1], "%d", &done)
			fmt.Sscanf(m[2], "%d", &total)
			if total > 0 {
				clearLine(lastLine)
				display := renderQuickProgress(currentPhase, done, total, phaseCount)
				fmt.Print(display)
				lastLine = utf8.RuneCountInString(display)
			}
			continue
		}

		// Completion
		if reComplete.MatchString(line) {
			completed = true
		}
	}

	close(doneCh)
	cmd.Wait()

	clearLine(lastLine)
	if completed {
		fmt.Println("  ✓ Initial analysis complete — learnings, narratives, and persona ready")
	} else {
		fmt.Println("  ⚠ Quickstart did not complete fully. Daemon will finish in background.")
	}
	return completed
}

func renderQuickProgress(phase string, done, total, phaseNum int) string {
	if total <= 0 {
		return fmt.Sprintf("  [%d/7] %s...", phaseNum, phase)
	}
	pct := float64(done) / float64(total)
	barWidth := 20
	filled := int(float64(barWidth) * pct)
	if filled > barWidth {
		filled = barWidth
	}
	bar := strings.Repeat("█", filled) + strings.Repeat("░", barWidth-filled)
	return fmt.Sprintf("  [%d/7] %s [%s] %d/%d", phaseNum, phase, bar, done, total)
}
