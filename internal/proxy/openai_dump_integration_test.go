package proxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestOpenAIPath_DumpsRequestBodyWhenDebug verifies that the OpenAI forward path
// (forwardOpenAIWithTracking) writes a request-body dump file when
// YESMEM_PROXY_DEBUG=1, mirroring the Anthropic path (forwardWithAnnotation at
// proxy_forward.go:32). Regression: the OpenAI path was missing the
// maybeDumpRequestBody call, so OpenAI/DeepSeek traffic had no request-body
// observability.
func TestOpenAIPath_DumpsRequestBodyWhenDebug(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		fmt.Fprintf(w, "data: %s\n\n", `{"id":"chatcmpl-1","object":"chat.completion.chunk","model":"gpt-5.4","choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":null}]}`)
		fmt.Fprintf(w, "data: %s\n\n", `{"id":"chatcmpl-1","object":"chat.completion.chunk","model":"gpt-5.4","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`)
		fmt.Fprintf(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer upstream.Close()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	dataDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dataDir, "logs"), 0755); err != nil {
		t.Fatalf("mkdir logs: %v", err)
	}
	t.Setenv("YESMEM_PROXY_DEBUG", "1")

	cfg := Config{
		ListenAddr:            fmt.Sprintf("127.0.0.1:%d", port),
		TargetURL:             upstream.URL,
		OpenAITargetURL:       upstream.URL,
		TokenThreshold:        250000,
		TokenMinimumThreshold: 100000,
		KeepRecent:            10,
		DataDir:               dataDir,
	}

	go Run(cfg)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err == nil {
			conn.Close()
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	oaiReq := OpenAIChatRequest{
		Model: "gpt-5.4",
		Messages: []OpenAIMessage{
			{Role: "user", Content: "dump-me"},
		},
		Stream: true,
	}
	body, _ := json.Marshal(oaiReq)

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Post(fmt.Sprintf("http://127.0.0.1:%d/v1/chat/completions", port), "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}

	matches, err := filepath.Glob(filepath.Join(dataDir, "logs", "req_*_body.json"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(matches) == 0 {
		t.Fatal("OpenAI path did not dump request body: no req_*_body.json in logs/ (expected maybeDumpRequestBody call in forwardOpenAIWithTracking)")
	}
}
