package proxy

import (
	"context"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// === Task #1: Graceful Proxy-Restart ===

func TestGracefulShutdown_DrainsInFlight(t *testing.T) {
	// Slow backend — takes 200ms per request
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("done"))
	}))
	defer backend.Close()

	s := &Server{
		cfg: Config{
			TargetURL:      backend.URL,
			TokenThreshold: 999999,
		},
		httpClient:            backend.Client(),
		logger:                log.New(io.Discard, "", 0),
		annotations:           make(map[string]string),
		decay:                 NewDecayTracker(),
		narrative:             NewNarrative(),
		stats:                 &ProxyStats{startTime: time.Now()},
		selfPrimes:            make(map[string]string),
		responseTimes:         make(map[string]time.Time),
		thinkCounters:         make(map[string]int),
		loopStates:            make(map[string]*LoopState),
		lastInjectedIDs:       make(map[string]map[int64]string),
		sessionInjectCounts:   make(map[string]map[int64]int),
		lastTurnInjected:      make(map[string]map[int64]bool),
		channelInjectCount:    make(map[string]int),
		rulesTokenCount:       make(map[string]int),
		rulesCollapseInjected: make(map[string]bool),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleRequest)
	srv := &http.Server{Handler: mux}

	// Start on random port
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve(ln)

	addr := ln.Addr().String()

	// Start an in-flight request
	var wg sync.WaitGroup
	var respCode int
	wg.Add(1)
	go func() {
		defer wg.Done()
		resp, err := http.Post("http://"+addr+"/v1/messages",
			"application/json",
			strings.NewReader(`{"messages":[{"role":"user","content":"test"}]}`))
		if err != nil {
			t.Logf("request error: %v", err)
			return
		}
		respCode = resp.StatusCode
		resp.Body.Close()
	}()

	// Give the request time to start
	time.Sleep(50 * time.Millisecond)

	// Graceful shutdown with 5s timeout
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown error: %v", err)
	}

	// Wait for in-flight request to complete
	wg.Wait()

	if respCode != http.StatusOK {
		t.Errorf("in-flight request should complete: got status %d", respCode)
	}
}
