package daemon

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// ensureSingleInstance checks if another daemon is running and handles it.
func ensureSingleInstance(dataDir string, replace bool) error {
	sockPath := SocketPath(dataDir)
	pidPath := pidFilePath(dataDir)

	// Step 1: Try socket probe
	conn, err := net.DialTimeout("unix", sockPath, 2*time.Second)
	if err == nil {
		// Socket reachable — daemon is running
		defer conn.Close()

		// Send ping to verify
		encoder := json.NewEncoder(conn)
		decoder := json.NewDecoder(conn)
		encoder.Encode(Request{Method: "ping"})
		var resp Response
		decoder.Decode(&resp)

		if !replace {
			pid := readPidFile(pidPath)
			return fmt.Errorf("daemon already running (PID %s, socket %s). Use --replace to restart", pid, sockPath)
		}

		// --replace: kill old daemon
		log.Println("Replacing existing daemon...")
		killOldDaemon(pidPath, sockPath)
		killByProcessName()
		time.Sleep(2 * time.Second)
	} else {
		// Socket not reachable — clean up stale files
		if _, err := os.Stat(sockPath); err == nil {
			log.Println("Removing stale socket file")
			os.Remove(sockPath)
		}
		if _, err := os.Stat(pidPath); err == nil {
			pid := readPidFile(pidPath)
			if pid != "" && isProcessZombie(pid) {
				// Zombie: appears alive to signal 0 but is effectively dead
				log.Printf("Removing zombie process PID file (PID %s)", pid)
				os.Remove(pidPath)
			} else if pid != "" && !isProcessAlive(pid) {
				log.Println("Removing stale PID file")
				os.Remove(pidPath)
			} else if pid != "" && isProcessAlive(pid) {
				if replace {
					log.Printf("Killing zombie daemon (PID %s)...", pid)
					killPid(pid)
					killByProcessName()
					time.Sleep(2 * time.Second)
				} else {
					return fmt.Errorf("zombie daemon detected (PID %s, no socket). Use --replace to force restart", pid)
				}
			}
		}

		// Final check: kill any other yesmem daemon processes (no PID file, no socket)
		if replace {
			killByProcessName()
			time.Sleep(1 * time.Second)
		}
	}

	// Write our PID — O_EXCL is atomic on Linux: only one process wins
	f, err := os.OpenFile(pidPath, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		// Another process just wrote the PID file — check if it's alive
		time.Sleep(200 * time.Millisecond)
		pid := readPidFile(pidPath)
		if pid != "" && isProcessAlive(pid) && !isProcessZombie(pid) {
			return fmt.Errorf("daemon already starting (PID %s)", pid)
		}
		// Dead process — remove and retry atomically
		os.Remove(pidPath)
		f2, err2 := os.OpenFile(pidPath, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0644)
		if err2 != nil {
			return fmt.Errorf("daemon already starting (concurrent start): %w", err2)
		}
		f2.WriteString(strconv.Itoa(os.Getpid()))
		f2.Close()
		return nil
	}
	f.WriteString(strconv.Itoa(os.Getpid()))
	f.Close()

	return nil
}

func pidFilePath(dataDir string) string {
	return dataDir + "/daemon.pid"
}

func readPidFile(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(data))
}

func isProcessAlive(pidStr string) bool {
	pid, err := strconv.Atoi(pidStr)
	if err != nil {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	// Signal 0 checks if process exists without actually signaling.
	// Note: zombies also respond to signal 0 — use isProcessZombie() to distinguish.
	return proc.Signal(syscall.Signal(0)) == nil
}

// isProcessZombie checks if a process is in zombie state (State: Z in /proc).
// Zombies respond to signal 0 (appear "alive") but cannot be killed.
func isProcessZombie(pidStr string) bool {
	pid, err := strconv.Atoi(pidStr)
	if err != nil || pid <= 0 {
		return false
	}
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "State:") {
			return strings.Contains(line, "Z (zombie)")
		}
	}
	return false
}

func killOldDaemon(pidPath, sockPath string) {
	pid := readPidFile(pidPath)
	killPid(pid)
	os.Remove(sockPath)
	os.Remove(pidPath)
}

// killByProcessName finds and kills any "yesmem daemon" processes (except ourselves).
func killByProcessName() {
	myPid := os.Getpid()
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return // Not Linux or no /proc
	}
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid == myPid {
			continue
		}
		cmdline, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
		if err != nil {
			continue
		}
		cmd := strings.ReplaceAll(string(cmdline), "\x00", " ")
		if strings.Contains(cmd, "yesmem") && strings.Contains(cmd, "daemon") {
			log.Printf("Killing orphan daemon process (PID %d)", pid)
			proc, _ := os.FindProcess(pid)
			if proc != nil {
				proc.Signal(syscall.SIGKILL)
			}
		}
	}
}

func killPid(pidStr string) {
	pid, err := strconv.Atoi(pidStr)
	if err != nil {
		return
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return
	}
	// Try graceful first
	proc.Signal(syscall.SIGTERM)
	time.Sleep(500 * time.Millisecond)
	// Then force
	if isProcessAlive(pidStr) {
		proc.Signal(syscall.SIGKILL)
	}
}
