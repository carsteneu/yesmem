package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/carsteneu/yesmem/internal/config"
	"github.com/carsteneu/yesmem/internal/proxy"
)

func runProxy() {
	dataDir := yesmemDataDir()
	cfg, _ := config.Load(filepath.Join(dataDir, "config.yaml"))

	fs := flag.NewFlagSet("proxy", flag.ExitOnError)
	port := fs.Int("port", 0, "listen port (overrides config)")
	threshold := fs.Int("threshold", 0, "token threshold for stubbing (overrides config)")
	target := fs.String("target", "", "target API URL (overrides config)")
	openaiTarget := fs.String("openai-target", "", "OpenAI upstream URL (overrides config)")
	keepRecent := fs.Int("keep-recent", 0, "messages to always keep (overrides config)")
	resetCache := fs.Bool("reset-cache", false, "clear persisted frozen stubs and decay state")
	fs.Parse(os.Args[2:])

	pcfg := proxy.Config{
		ListenAddr:            cfg.Proxy.Listen,
		TargetURL:             cfg.Proxy.Target,
		TokenThreshold:        cfg.Proxy.TokenThreshold,
		TokenMinimumThreshold: cfg.Proxy.TokenMinimumThreshold,
		TokenThresholds:       cfg.Proxy.TokenThresholds,
		KeepRecent:            cfg.Proxy.KeepRecent,
		DataDir:               dataDir,
		OpenAITargetURL:      cfg.Proxy.OpenAITarget,
		// Signal reflection
		SignalsEnabled:     cfg.Signals.Enabled,
		SignalsMode:        cfg.Signals.Mode,
		SignalsEveryNTurns: cfg.Signals.EveryNTurns,
		SignalsModel:       cfg.SignalsModelID(),
		APIKey:             cfg.API.APIKey,
		SawtoothEnabled:      cfg.Proxy.SawtoothEnabled,
		CacheTTL:             cfg.Proxy.CacheTTL,
		UsageDeflationFactor: cfg.Proxy.UsageDeflationFactor,
		PromptUngate:         cfg.Proxy.PromptUngate,
		PromptRewrite:        cfg.Proxy.PromptRewrite,
		PromptEnhance:        cfg.Proxy.PromptEnhance,
		CacheKeepaliveEnabled: cfg.Proxy.CacheKeepaliveEnabled,
		CacheKeepaliveMode:    cfg.Proxy.CacheKeepaliveMode,
		CacheKeepalivePings5m: cfg.Proxy.CacheKeepalivePings5m,
		CacheKeepalivePings1h: cfg.Proxy.CacheKeepalivePings1h,
		// Forked agents
		ForkedAgentsEnabled:            cfg.ForkedAgents.Enabled,
		ForkedAgentsModel:              cfg.ForkedAgentsModelID(),
		ForkedAgentsTokenGrowthTrigger: cfg.ForkedAgents.TokenGrowthTrigger,
		ForkedAgentsMaxFailures:        3,
		ForkedAgentsMaxForksPerSession: cfg.ForkedAgents.MaxForksPerSession,
		ForkedAgentsDebug:              cfg.ForkedAgents.Debug,
	}

	// CLI overrides
	if *port > 0 {
		pcfg.ListenAddr = fmt.Sprintf(":%d", *port)
	}
	if *threshold > 0 {
		pcfg.TokenThreshold = *threshold
	}
	if *target != "" {
		pcfg.TargetURL = *target
	}
	if *openaiTarget != "" {
		pcfg.OpenAITargetURL = *openaiTarget
	}
	if *keepRecent > 0 {
		pcfg.KeepRecent = *keepRecent
	}
	if *resetCache {
		pcfg.ResetCache = true
	}

	// Defaults
	if pcfg.ListenAddr == "" {
		pcfg.ListenAddr = ":9099"
	}
	if pcfg.TargetURL == "" {
		pcfg.TargetURL = "https://api.anthropic.com"
	}
	if pcfg.TokenThreshold == 0 {
		pcfg.TokenThreshold = 250000
	}
	if pcfg.TokenMinimumThreshold == 0 {
		pcfg.TokenMinimumThreshold = 100000
	}
	if pcfg.KeepRecent == 0 {
		pcfg.KeepRecent = 10
	}
	if pcfg.OpenAITargetURL == "" {
		pcfg.OpenAITargetURL = "https://api.openai.com"
	}

	if err := proxy.Run(pcfg); err != nil {
		log.Fatalf("proxy: %v", err)
	}
}

func runStop() {
	target := "all"
	if len(os.Args) > 2 {
		target = os.Args[2] // "daemon", "proxy", or "all"
	}

	self := os.Getpid()
	stopped := 0

	switch target {
	case "daemon":
		stopped = stopProcesses("yesmem daemon", self)
	case "proxy":
		stopped = stopProcesses("yesmem proxy", self)
	default: // "all"
		stopped += stopProcesses("yesmem daemon", self)
		stopped += stopProcesses("yesmem proxy", self)
	}

	if stopped == 0 {
		fmt.Fprintf(os.Stderr, "No yesmem %s processes found.\n", target)
	} else {
		fmt.Fprintf(os.Stderr, "Stopped %d process(es).\n", stopped)
	}
}

func stopProcesses(pattern string, selfPID int) int {
	cmd := exec.Command("pgrep", "-f", pattern)
	out, err := cmd.Output()
	if err != nil {
		return 0
	}

	count := 0
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		pid := 0
		fmt.Sscanf(line, "%d", &pid)
		if pid == 0 || pid == selfPID {
			continue
		}
		proc, err := os.FindProcess(pid)
		if err != nil {
			continue
		}
		// Try SIGTERM first
		if err := proc.Signal(syscall.SIGTERM); err == nil {
			fmt.Fprintf(os.Stderr, "  stopped PID %d (%s)\n", pid, pattern)
			count++
		}
		// Wait briefly, then SIGKILL zombies
		time.Sleep(500 * time.Millisecond)
		if err := proc.Signal(syscall.Signal(0)); err == nil {
			// Still alive → force kill
			proc.Signal(syscall.SIGKILL)
			fmt.Fprintf(os.Stderr, "  force-killed PID %d (%s)\n", pid, pattern)
		}
	}
	return count
}

func runRestart() {
	dataDir := os.Getenv("YESMEM_DATA_DIR")
	if dataDir == "" {
		dataDir = filepath.Join(os.Getenv("HOME"), ".claude", "yesmem")
	}

	fmt.Fprintf(os.Stderr, "Stopping all yesmem processes...\n")
	self := os.Getpid()
	stopProcesses("yesmem daemon", self)
	stopProcesses("yesmem proxy", self)

	// Wait for processes to die
	time.Sleep(1 * time.Second)

	// Clean up stale socket and PID file
	sockPath := filepath.Join(dataDir, "daemon.sock")
	pidPath := filepath.Join(dataDir, "daemon.pid")
	os.Remove(sockPath)
	os.Remove(pidPath)

	fmt.Fprintf(os.Stderr, "Starting daemon...\n")
	exe, _ := os.Executable()
	cmd := exec.Command(exe, "daemon")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		log.Fatalf("restart: %v", err)
	}
	// Detach — don't wait for daemon
	cmd.Process.Release()

	// Wait for socket to appear
	for i := 0; i < 10; i++ {
		time.Sleep(500 * time.Millisecond)
		if _, err := os.Stat(sockPath); err == nil {
			fmt.Fprintf(os.Stderr, "Daemon ready (PID %d).\n", cmd.Process.Pid)
			return
		}
	}
	fmt.Fprintf(os.Stderr, "Daemon started (PID %d) but socket not yet ready. Check logs.\n", cmd.Process.Pid)
}

// ensureProxyRunning starts the proxy if ANTHROPIC_BASE_URL points to
// localhost:9099 and no proxy is listening. Prefers systemd, falls back to direct start.
func ensureProxyRunning() {
	baseURL := os.Getenv("ANTHROPIC_BASE_URL")
	if baseURL == "" || !strings.Contains(baseURL, "localhost:9099") {
		return // proxy not configured
	}

	// Quick check: is something listening on :9099?
	conn, err := net.DialTimeout("tcp", "localhost:9099", 500*time.Millisecond)
	if err == nil {
		conn.Close()
		return // already running
	}

	// Try systemd first (Linux)
	if runtime.GOOS == "linux" {
		if err := exec.Command("systemctl", "--user", "start", "yesmem-proxy").Run(); err == nil {
			time.Sleep(300 * time.Millisecond)
			return
		}
	}

	// Try launchd (macOS)
	if runtime.GOOS == "darwin" {
		home, _ := os.UserHomeDir()
		plist := filepath.Join(home, "Library", "LaunchAgents", "com.yesmem.proxy.plist")
		if err := exec.Command("launchctl", "load", plist).Run(); err == nil {
			time.Sleep(300 * time.Millisecond)
			return
		}
	}

	// Fallback: direct start
	binaryPath, _ := os.Executable()
	cmd := exec.Command(binaryPath, "proxy")
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		return // best-effort, don't fail the hook
	}
	cmd.Process.Release()
	time.Sleep(200 * time.Millisecond)
}
