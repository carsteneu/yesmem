package mcp

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/carsteneu/yesmem/internal/daemon"
)

// mcpErrorLog writes structured error entries to mcp-error.log.
// This captures broken pipes, reconnect failures, and other MCP issues
// that would otherwise be lost (MCP stderr goes to /dev/null).
var mcpErrorLog *log.Logger

func initErrorLog(dataDir string) {
	logDir := filepath.Join(dataDir, "logs")
	os.MkdirAll(logDir, 0755)
	logPath := filepath.Join(logDir, "mcp-error.log")
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return
	}
	mcpErrorLog = log.New(f, "", log.LstdFlags)
}

func logMCPError(event string, method string, err error) {
	if mcpErrorLog == nil {
		return
	}
	// Capture caller location
	_, file, line, _ := runtime.Caller(1)
	file = filepath.Base(file)
	mcpErrorLog.Printf("pid=%d %s:%d event=%s method=%q error=%v",
		os.Getpid(), file, line, event, method, err)
}

// ProxyClient connects to the daemon's Unix socket.
type ProxyClient struct {
	client  *daemon.SocketClient
	dataDir string
}

// NewProxy creates a proxy to the daemon, starting it if necessary.
func NewProxy(dataDir string) (*ProxyClient, error) {
	// Try connecting to existing daemon
	client, err := daemon.Dial(dataDir)
	if err == nil {
		// Verify connection with ping
		result, err := client.Call("ping", nil)
		if err == nil {
			var pong string
			json.Unmarshal(result, &pong)
			if pong == "pong" {
				log.Println("Connected to running daemon")
				return &ProxyClient{client: client, dataDir: dataDir}, nil
			}
		}
		client.Close()
	}

	// Daemon not running — start it
	log.Println("Daemon not running, starting...")
	if err := startDaemon(dataDir); err != nil {
		return nil, fmt.Errorf("start daemon: %w", err)
	}

	// Wait for daemon to be ready (up to 120s — with socket-first this takes ~2s)
	newClient, err := dialWithTimeout(dataDir, 120*time.Second)
	if err != nil {
		return nil, fmt.Errorf("daemon did not become ready: %w", err)
	}
	log.Println("Daemon started and ready")
	return &ProxyClient{client: newClient, dataDir: dataDir}, nil
}

// Call sends a request to the daemon, retrying once on connection error.
// Daemon-level errors (Response.Error) are returned directly without reconnect.
func (p *ProxyClient) Call(method string, params map[string]any) (json.RawMessage, error) {
	result, err := p.callOnce(method, params)
	if err == nil {
		return result, nil
	}

	// Daemon-level errors are application errors, not connection failures — don't reconnect
	if isDaemonError(err) {
		return nil, err
	}

	// Connection error — log and try reconnecting once (5s timeout)
	logMCPError("broken_pipe", method, err)
	if p.client != nil {
		p.client.Close()
	}

	newClient, dialErr := dialWithTimeout(p.dataDir, 5*time.Second)
	if dialErr != nil {
		logMCPError("reconnect_failed", method, fmt.Errorf("%w (original: %v)", dialErr, err))
		return nil, fmt.Errorf("reconnect failed: %w (original: %v)", dialErr, err)
	}
	p.client = newClient
	logMCPError("reconnect_ok", method, nil)

	// Retry the original call
	retryResult, retryErr := p.callOnce(method, params)
	if retryErr != nil && !isDaemonError(retryErr) {
		logMCPError("retry_failed", method, retryErr)
	}
	return retryResult, retryErr
}

// isDaemonError returns true if the error is a daemon-level application error
// (not a connection/transport error). These should not trigger reconnection.
func isDaemonError(err error) bool {
	return err != nil && strings.HasPrefix(err.Error(), "daemon: ")
}

// CallString convenience method — returns result as string.
func (p *ProxyClient) CallString(method string, params map[string]any) (string, error) {
	result, err := p.Call(method, params)
	if err != nil {
		return "", err
	}
	return string(result), nil
}

// Close closes the proxy connection.
func (p *ProxyClient) Close() error {
	return p.client.Close()
}

func (p *ProxyClient) callOnce(method string, params map[string]any) (json.RawMessage, error) {
	client, err := daemon.DialTimeout(p.dataDir, 2*time.Second)
	if err != nil {
		return nil, err
	}
	defer client.Close()

	if isFastMCPMethod(method) {
		_ = client.SetDeadline(time.Now().Add(12 * time.Second))
	}

	return client.Call(method, params)
}

func isFastMCPMethod(method string) bool {
	switch method {
	case "ping", "search", "deep_search", "hybrid_search", "docs_search", "related_to_file", "list_projects", "get_learnings", "get_config":
		return true
	default:
		return false
	}
}

// dialWithTimeout tries to connect to daemon with a short timeout (for reconnects).
func dialWithTimeout(dataDir string, timeout time.Duration) (*daemon.SocketClient, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		client, err := daemon.Dial(dataDir)
		if err == nil {
			result, err := client.Call("ping", nil)
			if err == nil {
				var pong string
				json.Unmarshal(result, &pong)
				if pong == "pong" {
					log.Println("Reconnected to daemon")
					return client, nil
				}
			}
			client.Close()
		}
		time.Sleep(500 * time.Millisecond)
	}
	return nil, fmt.Errorf("daemon not reachable within %v", timeout)
}

func startDaemon(dataDir string) error {
	// Find the yesmem binary
	exe, err := os.Executable()
	if err != nil {
		return err
	}

	home, _ := os.UserHomeDir()
	projectsDir := filepath.Join(home, ".claude", "projects")

	cmd := exec.Command(exe, "daemon", "--replace")
	cmd.Env = append(os.Environ(),
		"YESMEM_DATA_DIR="+dataDir,
		"YESMEM_PROJECTS_DIR="+projectsDir,
	)

	// Detach from parent process
	cmd.Stdout = nil
	cmd.Stderr = nil

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start daemon process: %w", err)
	}

	// Release the process so it survives if MCP exits
	cmd.Process.Release()

	return nil
}
