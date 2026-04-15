package proxy

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// forwardWithAnnotation forwards the request and extracts annotations from the SSE response.
func (s *Server) forwardWithAnnotation(w http.ResponseWriter, origReq *http.Request, body []byte, reqIdx int, toolUseIDs []string, proj string, threadID string, msgCount int, estimatedTokens ...int) {
	// Debug: dump request body to file for inspection
	if s.cfg.DataDir != "" {
		debugPath := filepath.Join(s.cfg.DataDir, "logs", fmt.Sprintf("req_%d_body.json", reqIdx))
		os.WriteFile(debugPath, body, 0644)
	}

	targetURL := s.cfg.TargetURL + origReq.URL.RequestURI()

	proxyReq, err := http.NewRequestWithContext(origReq.Context(), origReq.Method, targetURL, bytes.NewReader(body))
	if err != nil {
		s.logger.Printf("create request error: %v", err)
		http.Error(w, "failed to create proxy request", http.StatusBadGateway)
		return
	}

	for key, vals := range origReq.Header {
		for _, v := range vals {
			proxyReq.Header.Add(key, v)
		}
	}
	proxyReq.Header.Set("Content-Length", strconv.Itoa(len(body)))
	proxyReq.Header.Del("Connection")
	proxyReq.Header.Del("Accept-Encoding") // Force uncompressed for SSE parsing

	if s.cacheTTLDetector != nil {
		s.cacheTTLDetector.RecordRequest(threadID)
	}

	resp, err := s.httpClient.Do(proxyReq)
	if err != nil {
		s.logger.Printf("upstream error: %v", err)
		http.Error(w, "upstream error: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	// Decompress gzip response if needed (API may compress regardless of Accept-Encoding)
	responseBody := resp.Body
	if strings.EqualFold(resp.Header.Get("Content-Encoding"), "gzip") {
		gzReader, gzErr := gzip.NewReader(resp.Body)
		if gzErr == nil {
			responseBody = gzReader
			defer gzReader.Close()
			resp.Header.Del("Content-Encoding")
			resp.Header.Del("Content-Length")
		}
	}

	// Parse rate-limit headers before forwarding to client
	rlInfo := ParseRateLimitHeaders(resp.Header)
	var rlJSON string
	if rlInfo != nil {
		if b, err := json.Marshal(rlInfo); err == nil {
			rlJSON = string(b)
		}
	}

	for key, vals := range resp.Header {
		for _, v := range vals {
			w.Header().Add(key, v)
		}
	}
	w.WriteHeader(resp.StatusCode)

	// Check if this is an SSE stream
	ct := resp.Header.Get("Content-Type")
	isSSE := strings.Contains(ct, "text/event-stream")
	if !isSSE {
		// Read body to extract usage, then write to client
		bodyBytes, readErr := io.ReadAll(responseBody)
		if readErr != nil {
			s.logger.Printf("[req %d] read error: %v", reqIdx, readErr)
			return
		}
		w.Write(bodyBytes)

		// Parse usage from JSON response
		var jsonResp struct {
			Usage struct {
				InputTokens              int `json:"input_tokens"`
				OutputTokens             int `json:"output_tokens"`
				CacheReadInputTokens     int `json:"cache_read_input_tokens"`
				CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
				CacheCreation            struct {
					Ephemeral1hInputTokens int `json:"ephemeral_1h_input_tokens"`
				} `json:"cache_creation"`
			} `json:"usage"`
		}
		if err := json.Unmarshal(bodyBytes, &jsonResp); err == nil && jsonResp.Usage.InputTokens > 0 {
			// Sawtooth: track actual tokens for trigger decisions (non-streaming path)
			if s.cfg.SawtoothEnabled && threadID != "" && s.sawtoothTrigger != nil {
				s.sawtoothTrigger.UpdateAfterResponse(threadID, jsonResp.Usage.InputTokens, msgCount)
			}
			// Report token usage to daemon for agent budget tracking
			if threadID != "" {
				go s.queryDaemon("_track_usage", map[string]any{
					"thread_id":          threadID,
					"input_tokens":       jsonResp.Usage.InputTokens,
					"output_tokens":      jsonResp.Usage.OutputTokens,
					"cache_read_tokens":  jsonResp.Usage.CacheReadInputTokens,
					"cache_write_tokens": jsonResp.Usage.CacheCreationInputTokens,
				})
			}
			// Persist rate-limit snapshot (independent of threadID)
			if rlJSON != "" {
				go s.queryDaemon("_persist_rate_limits", map[string]any{"rate_limits": rlJSON})
			}
			// Update cache status writer
			s.cacheStatusWriter.Update(time.Now(),
				jsonResp.Usage.InputTokens, jsonResp.Usage.CacheReadInputTokens, jsonResp.Usage.CacheCreationInputTokens, threadID)
			var fwdModel struct{ Model string `json:"model"` }
			json.Unmarshal(body, &fwdModel)
			s.cacheStatusWriter.UpdateThresholdForThread(threadID, s.effectiveTokenThreshold(fwdModel.Model))
			// Feed TTL detector and reset keepalive (non-streaming path)
			if s.cacheTTLDetector != nil {
				prevState := s.cacheTTLDetector.Is1hSupported()
				s.cacheTTLDetector.RecordResponse(jsonResp.Usage.CacheReadInputTokens, jsonResp.Usage.CacheCreationInputTokens, jsonResp.Usage.CacheCreation.Ephemeral1hInputTokens)
				newState := s.cacheTTLDetector.Is1hSupported()
				if prevState == nil && newState != nil {
					s.logger.Printf("TTL detection: ephemeral_1h=%d → 1h_supported=%v", jsonResp.Usage.CacheCreation.Ephemeral1hInputTokens, *newState)
					if *newState && s.frozenStubs != nil {
						s.frozenStubs.UpdateTTL(sawtoothTTLForCacheTTL("1h"))
					}
					if s.cacheKeepalive != nil {
						s.cacheKeepalive.Retrigger()
					}
				}
			}
			if s.cacheKeepalive != nil {
				apiKey := origReq.Header.Get("x-api-key")
				if apiKey == "" {
					apiKey = origReq.Header.Get("Authorization")
				}
				s.cacheKeepalive.Reset(threadID, body, apiKey)
			}
			// Store response timestamp (non-streaming path)
			if threadID != "" {
				s.responseTsMu.Lock()
				s.responseTimes[threadID] = time.Now()
				s.responseTsMu.Unlock()
			}
		}
		return
	}

	// SSE streaming with annotation extraction
	flusher, canFlush := w.(http.Flusher)
	reader := bufio.NewReaderSize(responseBody, 8192)

	var firstTextCollected bool
	var textAccum strings.Builder
	const maxAnnotationLen = 120

	// Task #3: Usage tracking from SSE stream
	usage := &UsageTracker{}

	// Accumulate full response text for async reflection call
	var fullResponseText strings.Builder

	for {
		line, readErr := reader.ReadBytes('\n')
		if len(line) > 0 {
			trimmedLine := bytes.TrimSpace(line)

			if bytes.HasPrefix(trimmedLine, []byte("data: ")) {
				data := bytes.TrimPrefix(trimmedLine, []byte("data: "))
				data = bytes.TrimSpace(data)

				if !firstTextCollected {
					s.parseSSEForAnnotation(data, &textAccum, &firstTextCollected, maxAnnotationLen)
				}

				// Accumulate response text for reflection call
				if bytes.Contains(data, []byte(`"text_delta"`)) {
					var delta struct {
						Delta struct {
							Text string `json:"text"`
						} `json:"delta"`
					}
					if json.Unmarshal(data, &delta) == nil && delta.Delta.Text != "" {
						fullResponseText.WriteString(delta.Delta.Text)
					}
				}

				// Task #3: Extract real token counts + Task #8: message_stop detection
				usage.ParseSSEEvent(data)

				// Deflate input_tokens in message_start so CC sees lower context usage.
				// This prevents the "Context low" warning since our proxy manages context.
				// Real values are already captured in usage tracker above.
				if s.cfg.UsageDeflationFactor > 0 && bytes.Contains(data, []byte(`"message_start"`)) {
					if deflated := deflateUsage(data, s.cfg.UsageDeflationFactor); deflated != nil {
						deflatedLine := append([]byte("data: "), deflated...)
						deflatedLine = append(deflatedLine, '\n')
						if _, writeErr := w.Write(deflatedLine); writeErr != nil {
							s.logger.Printf("[req %d] write error: %v", reqIdx, writeErr)
							return
						}
						if canFlush {
							flusher.Flush()
						}
						// Skip normal write for this line
						goto skipWrite
					}
				}
			}

			if _, writeErr := w.Write(line); writeErr != nil {
				s.logger.Printf("[req %d] write error: %v", reqIdx, writeErr)
				return
			}
			if canFlush {
				flusher.Flush()
			}
		skipWrite:
		}
		if readErr != nil {
			break
		}
	}

	// Task #3: Log real usage
	if usage.TotalInputTokens() > 0 || usage.OutputTokens > 0 {
		est := 0
		if len(estimatedTokens) > 0 {
			est = estimatedTokens[0]
		}
		s.logger.Printf("[req %d] %s", reqIdx, usage.LogLine(reqIdx, 0, est, threadID))

		// Sawtooth: track actual tokens for trigger decisions
		if s.cfg.SawtoothEnabled && threadID != "" {
			s.sawtoothTrigger.UpdateAfterResponse(threadID, usage.TotalInputTokens(), msgCount)
		}

		// Report token usage to daemon for agent budget tracking
		if threadID != "" {
			go s.queryDaemon("_track_usage", map[string]any{
				"thread_id":          threadID,
				"input_tokens":       usage.TotalInputTokens(),
				"output_tokens":      usage.OutputTokens,
				"cache_read_tokens":  usage.CacheReadInputTokens,
				"cache_write_tokens": usage.CacheCreationInputTokens,
			})
		}
		// Persist rate-limit snapshot (independent of threadID)
		if rlJSON != "" {
			go s.queryDaemon("_persist_rate_limits", map[string]any{"rate_limits": rlJSON})
		}

		// Update cache status writer (background goroutine writes to disk every second)
		s.cacheStatusWriter.Update(time.Now(),
			usage.TotalInputTokens(), usage.CacheReadInputTokens, usage.CacheCreationInputTokens, threadID)
		var fwdModel struct{ Model string `json:"model"` }
		json.Unmarshal(body, &fwdModel)
		s.cacheStatusWriter.UpdateThresholdForThread(threadID, s.effectiveTokenThreshold(fwdModel.Model))

		// Feed TTL detector and reset keepalive timer
		if s.cacheTTLDetector != nil {
			prevState := s.cacheTTLDetector.Is1hSupported()
			s.cacheTTLDetector.RecordResponse(usage.CacheReadInputTokens, usage.CacheCreationInputTokens, usage.Ephemeral1hInputTokens)
			newState := s.cacheTTLDetector.Is1hSupported()
			if prevState == nil && newState != nil {
				s.logger.Printf("TTL detection: ephemeral_1h=%d → 1h_supported=%v", usage.Ephemeral1hInputTokens, *newState)
				if *newState && s.frozenStubs != nil {
					s.frozenStubs.UpdateTTL(sawtoothTTLForCacheTTL("1h"))
					s.logger.Printf("TTL detection: frozen stubs TTL → %v", sawtoothTTLForCacheTTL("1h"))
				}
				if s.cacheKeepalive != nil {
					s.cacheKeepalive.Retrigger()
					s.logger.Printf("TTL detection: keepalive retriggered (interval=%ds)", s.cacheKeepalive.Status().IntervalS)
				}
			}
		}
		if s.cacheKeepalive != nil {
			apiKey := origReq.Header.Get("x-api-key")
			if apiKey == "" {
				apiKey = origReq.Header.Get("Authorization")
			}
			s.cacheKeepalive.Reset(threadID, body, apiKey)
		}

		// Store response timestamp for next-request annotation
		if threadID != "" {
			s.responseTsMu.Lock()
			s.responseTimes[threadID] = time.Now()
			s.responseTsMu.Unlock()
		}

		// Persist proxy usage tokens (fire-and-forget)
		go func(u *UsageTracker) {
			s.queryDaemon("track_proxy_usage", map[string]any{
				"input_tokens":           u.InputTokens,
				"output_tokens":          u.OutputTokens,
				"cache_read_tokens":      u.CacheReadInputTokens,
				"cache_creation_tokens":  u.CacheCreationInputTokens,
			})
		}(usage)

		// Fire forked agents (async, fire-and-forget)
		if usage.Complete && usage.CacheReadInputTokens > 0 && !s.forkState.IsDisabled(threadID) {
			totalTokens := usage.TotalInputTokens()
			hasCache := usage.CacheReadInputTokens > 0
			if s.forkState.ShouldFork(threadID, totalTokens, hasCache) {
				// Snapshot injected IDs for fork context
				s.lastInjectedIDsMu.Lock()
				injectedCopy := make(map[int64]string)
				for k, v := range s.lastInjectedIDs[threadID] {
					injectedCopy[k] = v
				}
				s.lastInjectedIDsMu.Unlock()

				go s.fireForkedAgents(ForkContext{
					OriginalBody:      body,
					AssistantResponse: fullResponseText.String(),
					ThreadID:          threadID,
					Project:           proj,
					ReqIdx:            reqIdx,
					TokensUsed:        totalTokens,
					CacheReadTokens:   usage.CacheReadInputTokens,
					MessageCount:      msgCount,
					InjectedIDs:       injectedCopy,
					LastExtractedIdx:  reqIdx - 2, // previous turn
					SessionID:         threadID,
					AuthHeader:        origReq.Header,
				}, s.forkConfigs)
			}
		}
	}

	// Task #8: Only commit annotation when message completed successfully
	if usage.Complete && textAccum.Len() > 0 && len(toolUseIDs) > 0 {
		annotation := strings.TrimSpace(textAccum.String())
		if len([]rune(annotation)) > maxAnnotationLen {
			annotation = string([]rune(annotation)[:maxAnnotationLen])
		}
		s.mu.Lock()
		// Evict old entries if map is too large
		if len(s.annotations) > maxAnnotations {
			s.evictOldAnnotations()
		}
		for _, id := range toolUseIDs {
			if _, exists := s.annotations[id]; !exists {
				s.annotations[id] = annotation
			}
		}
		s.mu.Unlock()
	}
}

// forwardRaw forwards a request to the upstream API without any annotation extraction.
func (s *Server) forwardRaw(w http.ResponseWriter, origReq *http.Request, body []byte) {
	targetURL := s.cfg.TargetURL + origReq.URL.RequestURI()

	proxyReq, err := http.NewRequestWithContext(origReq.Context(), origReq.Method, targetURL, bytes.NewReader(body))
	if err != nil {
		s.logger.Printf("create request error: %v", err)
		http.Error(w, "failed to create proxy request", http.StatusBadGateway)
		return
	}

	for key, vals := range origReq.Header {
		for _, v := range vals {
			proxyReq.Header.Add(key, v)
		}
	}
	proxyReq.Header.Set("Content-Length", strconv.Itoa(len(body)))
	proxyReq.Header.Del("Connection")
	proxyReq.Header.Del("Accept-Encoding") // Force uncompressed for SSE parsing

	resp, err := s.httpClient.Do(proxyReq)
	if err != nil {
		s.logger.Printf("upstream error: %v", err)
		http.Error(w, "upstream error: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	for key, vals := range resp.Header {
		for _, v := range vals {
			w.Header().Add(key, v)
		}
	}
	w.WriteHeader(resp.StatusCode)

	flusher, canFlush := w.(http.Flusher)
	buf := make([]byte, 4096)
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if _, writeErr := w.Write(buf[:n]); writeErr != nil {
				s.logger.Printf("write error: %v", writeErr)
				return
			}
			if canFlush {
				flusher.Flush()
			}
		}
		if readErr != nil {
			break
		}
	}
}

func (s *Server) passthrough(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	r.Body.Close()
	if err != nil {
		http.Error(w, "failed to read request body", http.StatusBadGateway)
		return
	}
	s.forwardRaw(w, r, body)
}

// parseSSEForAnnotation extracts text from SSE content_block_delta events.
func (s *Server) parseSSEForAnnotation(data []byte, accum *strings.Builder, done *bool, maxLen int) {
	if *done || len(data) == 0 || data[0] != '{' {
		return
	}

	var event struct {
		Type  string `json:"type"`
		Delta struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"delta"`
	}

	if err := json.Unmarshal(data, &event); err != nil {
		return
	}

	if event.Type == "content_block_delta" && event.Delta.Type == "text_delta" {
		accum.WriteString(event.Delta.Text)
		if accum.Len() >= maxLen {
			*done = true
		}
	}

	if event.Type == "content_block_stop" && accum.Len() > 0 {
		*done = true
	}
}

// evictOldAnnotations removes half the entries when the map exceeds maxAnnotations.
// Must be called with s.mu held.
func (s *Server) evictOldAnnotations() {
	target := maxAnnotations / 2
	count := 0
	for id := range s.annotations {
		if count >= target {
			break
		}
		delete(s.annotations, id)
		count++
	}
	s.logger.Printf("evicted %d annotations (was %d, now %d)", count, maxAnnotations, len(s.annotations))
}
