package hooks

import (
	"encoding/json"
	"net"
	"path/filepath"
	"testing"

	"github.com/carsteneu/yesmem/internal/daemon"
)

func TestDaemonCall_Success(t *testing.T) {
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "daemon.sock")
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	go func() {
		conn, _ := ln.Accept()
		defer conn.Close()
		var req map[string]any
		json.NewDecoder(conn).Decode(&req)
		resp := map[string]any{"result": map[string]any{"count": 5, "reminder": ""}}
		json.NewEncoder(conn).Encode(resp)
	}()

	client, err := daemon.Dial(dir)
	if err != nil {
		t.Fatalf("Dial failed: %v", err)
	}
	defer client.Close()

	result, err := client.Call("idle_tick", map[string]any{"session_id": "test-123"})
	if err != nil {
		t.Fatalf("Call failed: %v", err)
	}
	var parsed map[string]any
	json.Unmarshal(result, &parsed)
	if parsed["count"] != float64(5) {
		t.Errorf("expected count=5, got %v", parsed["count"])
	}
}

func TestDaemonCall_Unreachable(t *testing.T) {
	_, err := daemon.Dial("/tmp/nonexistent_dir_for_test")
	if err == nil {
		t.Error("expected error for unreachable socket")
	}
}
