package orchestrator

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"sync"

	pty "github.com/creack/pty/v2"
)

// AgentBridge holds a PTY master and bridges it to a Unix socket.
// The terminal-side program (agent-tty) connects to the socket.
// The Go tool can inject messages via Inject() or the inject socket.
type AgentBridge struct {
	Ptmx       *os.File     // PTY master — Go holds this
	SockPath   string       // Unix socket path for agent-tty
	InjectPath string       // Unix socket path for inject/relay
	Cmd        *exec.Cmd    // Claude process
	listener   net.Listener // Socket listener (agent-tty)
	injectLn   net.Listener // Socket listener (inject)
	mu         sync.Mutex   // Protects Ptmx writes
}

// NewAgentBridge creates a PTY, starts the command on it, and prepares a Unix socket.
// If workDir is non-empty, the command runs in that directory.
func NewAgentBridge(name string, args []string, sockPath string, workDir string) (*AgentBridge, error) {
	cmd := exec.Command(name, args...)
	if workDir != "" {
		cmd.Dir = workDir
	}
	// Inherit full environment and ensure terminal colors work
	cmd.Env = append(os.Environ(), "TERM=xterm-256color", "COLORTERM=truecolor", "FORCE_COLOR=1")
	ptmx, err := pty.Start(cmd)
	if err != nil {
		return nil, fmt.Errorf("pty.Start: %w", err)
	}

	_ = pty.Setsize(ptmx, &pty.Winsize{Rows: 50, Cols: 100})

	os.Remove(sockPath)
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		ptmx.Close()
		cmd.Process.Kill()
		return nil, fmt.Errorf("listen %s: %w", sockPath, err)
	}

	// Inject socket stays open for relay commands
	injectPath := sockPath + ".inject"
	os.Remove(injectPath)
	injectLn, err := net.Listen("unix", injectPath)
	if err != nil {
		ln.Close()
		ptmx.Close()
		cmd.Process.Kill()
		return nil, fmt.Errorf("listen %s: %w", injectPath, err)
	}

	return &AgentBridge{Ptmx: ptmx, SockPath: sockPath, InjectPath: injectPath, Cmd: cmd, listener: ln, injectLn: injectLn}, nil
}

// Serve blocks and bridges the first socket connection to the PTY.
// Also starts the inject listener for relay commands.
// Call in a goroutine.
func (b *AgentBridge) Serve() {
	defer b.listener.Close()
	defer os.Remove(b.SockPath)
	defer b.injectLn.Close()
	defer os.Remove(b.InjectPath)

	// Start inject listener in background
	go b.serveInject()

	conn, err := b.listener.Accept()
	if err != nil {
		return
	}
	defer conn.Close()

	done := make(chan struct{}, 2)

	go func() {
		b.copyWithWinsize(conn)
		done <- struct{}{}
	}()

	go func() {
		io.Copy(conn, b.Ptmx)
		done <- struct{}{}
	}()

	<-done
}

// serveInject accepts short-lived connections on the inject socket.
// Each connection's content is written to the PTY master as user input.
func (b *AgentBridge) serveInject() {
	for {
		conn, err := b.injectLn.Accept()
		if err != nil {
			return // listener closed
		}
		go func(c net.Conn) {
			defer c.Close()
			data, err := io.ReadAll(c)
			if err == nil && len(data) > 0 {
				b.mu.Lock()
				b.Ptmx.Write(data)
				b.mu.Unlock()
			}
		}(conn)
	}
}

// copyWithWinsize reads from the socket, intercepts winsize markers (0x01),
// and forwards everything else to the PTY master.
func (b *AgentBridge) copyWithWinsize(conn net.Conn) {
	buf := make([]byte, 4096)
	for {
		n, err := conn.Read(buf)
		if n > 0 {
			data := buf[:n]
			for len(data) > 0 {
				if data[0] == 0x01 && len(data) >= 5 {
					rows := binary.BigEndian.Uint16(data[1:3])
					cols := binary.BigEndian.Uint16(data[3:5])
					pty.Setsize(b.Ptmx, &pty.Winsize{Rows: rows, Cols: cols})
					data = data[5:]
				} else {
					// Find next potential marker or write all
					end := 1
					for end < len(data) && data[end] != 0x01 {
						end++
					}
					b.mu.Lock()
					b.Ptmx.Write(data[:end])
					b.mu.Unlock()
					data = data[end:]
				}
			}
		}
		if err != nil {
			return
		}
	}
}

// Inject writes a message directly into the PTY master.
// Claude sees it as keyboard input. Thread-safe.
func (b *AgentBridge) Inject(msg string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	_, err := b.Ptmx.Write([]byte(msg))
	return err
}

// Close cleans up the bridge.
func (b *AgentBridge) Close() {
	b.listener.Close()
	b.injectLn.Close()
	b.Ptmx.Close()
	os.Remove(b.SockPath)
	os.Remove(b.InjectPath)
}
