package daemon

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"time"
)

const sockName = "daemon.sock"

// SocketPath returns the path to the daemon's Unix socket.
func SocketPath(dataDir string) string {
	return filepath.Join(dataDir, sockName)
}

// IsDaemonRunning checks if the daemon socket is reachable.
func IsDaemonRunning(dataDir string) bool {
	conn, err := net.Dial("unix", SocketPath(dataDir))
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// Request is a JSON-RPC style request to the daemon.
type Request struct {
	Method string         `json:"method"`
	Params map[string]any `json:"params,omitempty"`
}

// Response is a JSON-RPC style response from the daemon.
type Response struct {
	Result json.RawMessage `json:"result,omitempty"`
	Error  string          `json:"error,omitempty"`
}

// SocketServer listens on a Unix socket and dispatches requests to handlers.
type SocketServer struct {
	listener net.Listener
	handler  func(req Request) Response
}

// NewSocketServer creates a Unix socket server.
func NewSocketServer(dataDir string, handler func(req Request) Response) (*SocketServer, error) {
	sockPath := SocketPath(dataDir)

	// Remove stale socket
	os.Remove(sockPath)

	listener, err := net.Listen("unix", sockPath)
	if err != nil {
		return nil, fmt.Errorf("listen %s: %w", sockPath, err)
	}

	log.Printf("Socket server listening on %s", sockPath)

	return &SocketServer{
		listener: listener,
		handler:  handler,
	}, nil
}

// Serve accepts connections and handles requests. Blocks until Close.
func (s *SocketServer) Serve() {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return // listener closed
		}
		go s.handleConn(conn)
	}
}

// Close stops the socket server.
func (s *SocketServer) Close() error {
	return s.listener.Close()
}

func (s *SocketServer) handleConn(conn net.Conn) {
	defer conn.Close()

	decoder := json.NewDecoder(conn)
	encoder := json.NewEncoder(conn)

	for {
		var req Request
		if err := decoder.Decode(&req); err != nil {
			return // client disconnected
		}
		resp := s.handler(req)
		if err := encoder.Encode(resp); err != nil {
			return
		}
	}
}

// SocketClient connects to the daemon's Unix socket.
type SocketClient struct {
	conn    net.Conn
	encoder *json.Encoder
	decoder *json.Decoder
}

// Dial connects to the daemon socket.
func Dial(dataDir string) (*SocketClient, error) {
	conn, err := net.Dial("unix", SocketPath(dataDir))
	if err != nil {
		return nil, fmt.Errorf("dial daemon: %w", err)
	}
	return &SocketClient{
		conn:    conn,
		encoder: json.NewEncoder(conn),
		decoder: json.NewDecoder(conn),
	}, nil
}

// DialTimeout connects to the daemon socket with a timeout.
func DialTimeout(dataDir string, timeout time.Duration) (*SocketClient, error) {
	conn, err := net.DialTimeout("unix", SocketPath(dataDir), timeout)
	if err != nil {
		return nil, fmt.Errorf("dial daemon: %w", err)
	}
	return &SocketClient{
		conn:    conn,
		encoder: json.NewEncoder(conn),
		decoder: json.NewDecoder(conn),
	}, nil
}

// Call sends a request and waits for the response.
func (c *SocketClient) Call(method string, params map[string]any) (json.RawMessage, error) {
	req := Request{Method: method, Params: params}
	if err := c.encoder.Encode(req); err != nil {
		return nil, fmt.Errorf("send: %w", err)
	}

	var resp Response
	if err := c.decoder.Decode(&resp); err != nil {
		return nil, fmt.Errorf("recv: %w", err)
	}

	if resp.Error != "" {
		return nil, fmt.Errorf("daemon: %s", resp.Error)
	}
	return resp.Result, nil
}

// SetDeadline sets the underlying connection deadline.
func (c *SocketClient) SetDeadline(t time.Time) error {
	return c.conn.SetDeadline(t)
}

// Close closes the connection.
func (c *SocketClient) Close() error {
	return c.conn.Close()
}
