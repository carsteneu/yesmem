package proxy

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type openAIRequestContext struct {
	ReqIdx          int
	Project         string
	ThreadID        string
	SessionID       string
	Fingerprint     string
	UserQuery       string
	ToolUseIDs      []string
	Retry           bool
	EstimatedTokens int
	MessageCount    int
	Subagent        bool
}

func (s *Server) ensureOpenAIRuntimeState() {
	s.mu.Lock()
	if s.annotations == nil {
		s.annotations = make(map[string]string)
	}
	if s.decay == nil {
		s.decay = NewDecayTracker()
	}
	if s.narrative == nil {
		s.narrative = NewNarrative()
	}
	if s.cacheGate == nil {
		s.cacheGate = NewCacheGate(cacheGapForTTL(s.cfg.CacheTTL))
	}
	if s.frozenStubs == nil {
		s.frozenStubs = NewFrozenStubsWithTTL(sawtoothTTLForCacheTTL(s.cfg.CacheTTL))
	}
	if s.sawtoothTrigger == nil {
		s.sawtoothTrigger = NewSawtoothTrigger(cacheGapForTTL(s.cfg.CacheTTL), s.cfg.TokenThreshold, s.cfg.TokenMinimumThreshold)
	}
	if s.skillTracker == nil {
		s.skillTracker = newSkillHintTracker()
	}
	s.mu.Unlock()

	s.thinkMu.Lock()
	if s.thinkCounters == nil {
		s.thinkCounters = make(map[string]int)
	}
	s.thinkMu.Unlock()

	s.channelMu.Lock()
	if s.channelInjectCount == nil {
		s.channelInjectCount = make(map[string]int)
	}
	s.channelMu.Unlock()

	s.responseTsMu.Lock()
	if s.responseTimes == nil {
		s.responseTimes = make(map[string]time.Time)
	}
	s.responseTsMu.Unlock()

	s.lastInjectedIDsMu.Lock()
	if s.lastInjectedIDs == nil {
		s.lastInjectedIDs = make(map[string]map[int64]string)
	}
	if s.sessionInjectCounts == nil {
		s.sessionInjectCounts = make(map[string]map[int64]int)
	}
	if s.lastTurnInjected == nil {
		s.lastTurnInjected = make(map[string]map[int64]bool)
	}
	s.lastInjectedIDsMu.Unlock()
}

func (s *Server) prepareOpenAIRequestContext(req map[string]any, reqIdx int, headerSessionID string) openAIRequestContext {
	messages, _ := req["messages"].([]any)
	fp := requestFingerprint(messages)
	ctx := openAIRequestContext{
		ReqIdx:      reqIdx,
		Project:     extractProjectName(req),
		SessionID:   extractSessionID(req, headerSessionID),
		Fingerprint: fp,
		UserQuery:   lastUserText(messages),
		ToolUseIDs:  extractToolUseIDs(messages),
		Subagent:    isOpenAIProxySubagent(messages, req),
	}
	// Prefer stable session ID from metadata (Codex) over content-hash
	if ctx.SessionID != "" {
		ctx.ThreadID = ctx.SessionID
	} else {
		ctx.ThreadID = DeriveThreadID(req)
	}

	ctx.Retry = s.isRetry(fp)
	return ctx
}

func (s *Server) runOpenAIParityPipeline(req map[string]any, ctx *openAIRequestContext) {
	if ctx == nil {
		return
	}

	model, _ := req["model"].(string)

	s.ensureOpenAIRuntimeState()

	s.sessionStartMu.Lock()
	if s.sessionStartTime.IsZero() {
		s.sessionStartTime = time.Now()
	}
	s.sessionStartMu.Unlock()

	s.refreshConfigOverrides(ctx.ThreadID)

	messages, _ := req["messages"].([]any)
	if len(messages) <= 1 {
		AppendSystemBlock(req, "yesmem-identity", "The first user message contains your briefing — written by yourself, from your memory. This is not an external document. This is you, remembering.")
	}

	// prompt_ungate: strip CLAUDE.md subordination disclaimer (OpenAI parity path).
	if s.cfg.PromptUngate {
		StripCLAUDEMDDisclaimer(req)
	}

	// prompt_rewrite: strip + inject (OpenAI parity path)
	if s.cfg.PromptRewrite {
		StripOutputEfficiency(req)
		StripToneBrevity(req)
		InjectAntDirectives(req)
	}
	if s.cfg.PromptEnhance {
		InjectCLAUDEMDAuthority(req)
		if raw, err := s.queryDaemon("get_persona", nil); err == nil {
			var persona map[string]any
			if json.Unmarshal(raw, &persona) == nil {
				if traits, ok := persona["traits"].(map[string]any); ok {
					if verbosity, ok := traits["verbosity"].(string); ok {
						InjectPersonaTone(req, verbosity)
					}
				}
			}
		}
	}

	messages, _ = req["messages"].([]any)
	overhead := s.measureOverhead(req)
	totalTokens := s.estimateTotalTokens(ctx.ThreadID, messages, overhead)
	ctx.EstimatedTokens = totalTokens
	ctx.MessageCount = len(messages)

	if !ctx.Retry && s.cfg.DataDir != "" {
		turnProject := ctx.Project
		if turnProject == "" {
			turnProject = "__global__"
		}
		go s.queryDaemon("increment_turn", map[string]any{"project": turnProject})
	}

	s.runOpenAIInlineReflection(ctx, messages)

	if ci := detectGitCommit(messages); ci != nil && s.cfg.DataDir != "" {
		workdir := extractWorkingDirectory(req)
		if s.logger != nil {
			s.logger.Printf("[req %d %s tid=%s] git commit detected: %s %s", ctx.ReqIdx, ctx.Project, ctx.ThreadID, ci.Hash, ci.Message)
		}
		go s.queryDaemon("invalidate_on_commit", map[string]any{
			"hash": ci.Hash, "project": ctx.Project, "workdir": workdir,
		})
	}

	if ctx.Subagent {
		finalMessages, _ := req["messages"].([]any)
		ctx.EstimatedTokens = s.estimateTotalTokens(ctx.ThreadID, finalMessages, s.measureOverhead(req))
		ctx.MessageCount = len(finalMessages)
		return
	}

	briefingText := ""
	if len(messages) <= 6 {
		cwd := extractWorkingDirectory(req)
		if data := s.loadBriefing(ctx.Project, cwd); data.Text != "" {
			briefingText = data.Text
			AppendSystemBlock(req, "yesmem-briefing", data.Text)
			s.logger.Printf("[req %d] %sOpenAI pipeline: briefing injected (%db) project=%s%s",
				ctx.ReqIdx, colorBlue, len(data.Text), ctx.Project, colorReset)
		}
	}

	if s.cfg.SawtoothEnabled && s.frozenStubs != nil && s.sawtoothTrigger != nil {
		frozen := s.frozenStubs.Get(ctx.ThreadID, messages)
		if frozen != nil {
			freshMessages := messages[frozen.Cutoff:]
			freshTokens := s.countMessageTokens(freshMessages)
			combinedTokens := frozen.Tokens + freshTokens + overhead
			if combinedTokens > s.effectiveTokenThreshold(model) {
				s.frozenStubs.Invalidate(ctx.ThreadID)
				frozen = nil
			} else {
				combined := make([]any, 0, len(frozen.Messages)+len(freshMessages))
				combined = append(combined, frozen.Messages...)
				combined = append(combined, freshMessages...)
				req["messages"] = combined
			}
		}
		if frozen == nil {
			triggerReason := s.sawtoothTrigger.ShouldTrigger(ctx.ThreadID, totalTokens)
			if triggerReason != TriggerNone {
				s.setRawEstimate(ctx.ThreadID, totalTokens)
				_ = s.runStubCycle(messages, req, ctx.ReqIdx, ctx.Project, ctx.ThreadID, overhead, totalTokens, ctx.UserQuery, ctx.Retry)
				finalMessages, _ := req["messages"].([]any)
				if len(finalMessages) > 0 {
					cutoff := len(messages)
					var boundaryMsg any
					if cutoff > 0 {
						boundaryMsg = messages[cutoff-1]
					}
					s.frozenStubs.Store(ctx.ThreadID, finalMessages, cutoff, boundaryMsg, s.countMessageTokens(finalMessages), totalTokens)
				}
			}
		}
	} else if s.shouldStub(totalTokens, model) {
		s.setRawEstimate(ctx.ThreadID, totalTokens)
		s.runStubCycle(messages, req, ctx.ReqIdx, ctx.Project, ctx.ThreadID, overhead, totalTokens, ctx.UserQuery, ctx.Retry)
	}

	messages, _ = req["messages"].([]any)

	assocContext := ""
	if ctx.UserQuery != "" && ctx.Project != "" {
		assocContext = s.findAssociativeContextFor(ctx.UserQuery, ctx.Project, ctx.ThreadID, messages)
		if assocContext != "" {
			req["messages"] = injectAssociativeContext(messages, assocContext, s.cfg.SawtoothEnabled)
			messages, _ = req["messages"].([]any)
			s.logger.Printf("%s[req %d %s tid=%s] associative context injected%s", colorBlue, ctx.ReqIdx, ctx.Project, ctx.ThreadID, colorReset)
		}

		docContext := s.findDocContextFor(ctx.UserQuery, ctx.Project, messages)
		if docContext != "" {
			req["messages"] = injectAssociativeContext(messages, docContext, s.cfg.SawtoothEnabled)
			messages, _ = req["messages"].([]any)
			s.logger.Printf("%s[req %d %s tid=%s] doc context injected%s", colorBlue, ctx.ReqIdx, ctx.Project, ctx.ThreadID, colorReset)
		}
	}

	if rulesBlock := s.rulesInject(ctx.ThreadID, totalTokens, ctx.Project); rulesBlock != "" {
		req["messages"] = injectAssociativeContext(messages, s.formatRulesReminder(rulesBlock, ctx.Project), s.cfg.SawtoothEnabled)
		messages, _ = req["messages"].([]any)
		s.logger.Printf("%s[req %d %s tid=%s] rules reminder injected%s", colorBlue, ctx.ReqIdx, ctx.Project, ctx.ThreadID, colorReset)
	}

	s.detectPlanToolCall(messages, ctx.ThreadID, totalTokens)
	if checkpoint := s.planCheckpointInject(ctx.ThreadID, totalTokens); checkpoint != "" {
		req["messages"] = injectAssociativeContext(messages, checkpoint, s.cfg.SawtoothEnabled)
		messages, _ = req["messages"].([]any)
		s.logger.Printf("%s[req %d %s tid=%s] plan checkpoint injected%s", colorBlue, ctx.ReqIdx, ctx.Project, ctx.ThreadID, colorReset)
	}

	if ctx.ThreadID != "" && ctx.Project != "" && isUserInputTurn(messages) && s.skillTracker != nil {
		s.syncSkillActivations(messages, ctx.Project, ctx.ThreadID)
		skillEval := buildSkillEvalBlock(s.cfg.SkillEvalInject)
		if skillEval != "" {
			req["messages"] = injectAssociativeContext(messages, skillEval, s.cfg.SawtoothEnabled)
		}
		messages, _ = req["messages"].([]any)
		s.logger.Printf("%s[req %d %s tid=%s] skill-eval injected%s", colorBlue, ctx.ReqIdx, ctx.Project, ctx.ThreadID, colorReset)
	}

	if ctx.ThreadID != "" {
		// NOTE: buildThinkReminder accepts sessionID param (unused internally).
		// In the OpenAI path ctx.ThreadID == ctx.SessionID already (set in buildOpenAIContext).
		// Keeping ctx.SessionID here for clarity — both are the same value.
		thinkReminder := s.buildThinkReminder(ctx.ThreadID, ctx.SessionID, true)
		if thinkReminder != "" {
			req["messages"] = injectAssociativeContext(messages, thinkReminder, s.cfg.SawtoothEnabled)
			messages, _ = req["messages"].([]any)
		}

		dr := s.checkDialogMessages(ctx.ThreadID, ctx.Project)
		if dr.Extra != "" {
			if ctx.SessionID != "" {
				dr.Extra = fmt.Sprintf("DEINE_SESSION_ID: %s\n", ctx.SessionID) + dr.Extra
			}
			if len(messages) > 0 {
				lastMsg, _ := messages[len(messages)-1].(map[string]any)
				if lastMsg != nil && lastMsg["role"] == "user" {
					messages = append(messages, map[string]any{
						"role":    "assistant",
						"content": "\u200b",
					})
				}
				messages = append(messages, map[string]any{
					"role":    "user",
					"content": "<channel source=\"yesmem-dialog\">\n" + dr.Extra + "\n</channel>",
				})
				req["messages"] = messages
				if dr.HasUnread && dr.SessionID != "" && s.shouldMarkChannelRead(dr) {
					s.markDialogRead(dr)
				}
			}
		}
	}

	s.annotateOpenAIMessageMetadata(req, ctx.ThreadID)

	if !s.cfg.SawtoothEnabled && s.cacheGate != nil && s.cacheGate.ShouldCache() {
		if n := InjectCacheBreakpoints(req, s.logger); n > 0 && s.logger != nil {
			s.logger.Printf("[req %d %s] prompt cache: %d breakpoints injected", ctx.ReqIdx, ctx.Project, n)
		}
	}
	if s.cfg.CacheTTL != "" && s.cfg.CacheTTL != "ephemeral" {
		if n := UpgradeCacheTTL(req, s.cfg.CacheTTL); n > 0 && s.logger != nil {
			s.logger.Printf("[req %d %s] cache TTL upgraded: %d blocks → %s", ctx.ReqIdx, ctx.Project, n, s.cfg.CacheTTL)
		}
	}
	if n := EnforceCacheBreakpointLimit(req, maxCacheBreakpoints); n > 0 && s.logger != nil {
		s.logger.Printf("[req %d %s] prompt cache: trimmed %d surplus breakpoints", ctx.ReqIdx, ctx.Project, n)
	}

	if msgs, ok := req["messages"].([]any); ok {
		if fixed, orphans := validateToolPairs(msgs, s.logger); orphans > 0 {
			req["messages"] = fixed
			if s.logger != nil {
				s.logger.Printf("%s[req %d %s tid=%s] validate: repaired %d orphan tool_result(s)%s", colorOrange, ctx.ReqIdx, ctx.Project, ctx.ThreadID, orphans, colorReset)
			}
		}
	}

	s.trackOpenAIInjectedLearnings(ctx.ThreadID, briefingText, assocContext)

	finalMessages, _ := req["messages"].([]any)
	ctx.EstimatedTokens = s.measureOverhead(req) + s.countMessageTokens(finalMessages)
}

func isOpenAIProxySubagent(messages []any, req map[string]any) bool {
	if len(messages) == 0 {
		return false
	}
	if req != nil {
		if sys, ok := req["system"].([]any); ok && len(sys) > 0 {
			if b, ok := sys[0].(map[string]any); ok {
				header, _ := b["text"].(string)
				if strings.Contains(header, "cc_entrypoint=sdk-ts") {
					return true
				}
				if strings.Contains(header, "cc_entrypoint=cli") {
					return false
				}
			}
		}
		model, _ := req["model"].(string)
		if strings.Contains(model, "haiku") {
			return true
		}
	}
	// The generic len<=3 fallback is correct for Anthropic agent-tool subagents,
	// but false-positives for OpenAI/Codex requests are common and break parity.
	return false
}

func (s *Server) runOpenAIInlineReflection(ctx *openAIRequestContext, messages []any) {
	if ctx == nil || ctx.ThreadID == "" || ctx.Retry || s.cfg.DataDir == "" {
		return
	}

	s.lastInjectedIDsMu.Lock()
	prevInjected := s.lastInjectedIDs[ctx.ThreadID]
	s.lastInjectedIDsMu.Unlock()
	if len(prevInjected) == 0 {
		return
	}

	signals := scanAssistantSignals(messages)

	var confirmedUsed []int64
	for _, id := range signals.UsedIDs {
		if _, ok := prevInjected[id]; ok {
			confirmedUsed = append(confirmedUsed, id)
		}
	}
	if len(confirmedUsed) > 0 {
		idsAny := make([]any, len(confirmedUsed))
		for i, id := range confirmedUsed {
			idsAny[i] = id
		}
		go s.queryDaemon("increment_use", map[string]any{"ids": idsAny})
	}

	usedSet := make(map[int64]bool, len(confirmedUsed))
	for _, id := range confirmedUsed {
		usedSet[id] = true
	}
	var noiseIDs []int64
	for id, source := range prevInjected {
		if usedSet[id] {
			continue
		}
		if source == "associative" || source == "fresh" {
			noiseIDs = append(noiseIDs, id)
		}
	}
	if len(noiseIDs) > 0 {
		idsAny := make([]any, len(noiseIDs))
		for i, id := range noiseIDs {
			idsAny[i] = id
		}
		go s.queryDaemon("increment_noise", map[string]any{"ids": idsAny})
	}

	if signals.HasToolErrors && len(confirmedUsed) > 0 {
		failAny := make([]any, len(confirmedUsed))
		for i, id := range confirmedUsed {
			failAny[i] = id
		}
		go s.queryDaemon("increment_fail", map[string]any{"ids": failAny})
		if s.logger != nil {
			s.logger.Printf("[inline-reflection] error feedback: %d used learnings → fail_count++", len(confirmedUsed))
		}
	}

	for _, topic := range signals.GapTopics {
		go s.queryDaemon("track_gap", map[string]any{"topic": topic, "project": ctx.Project})
	}
	for _, desc := range signals.Contradictions {
		go s.queryDaemon("flag_contradiction", map[string]any{"description": desc, "project": ctx.Project, "thread_id": ctx.ThreadID})
	}

	if s.logger != nil && (len(confirmedUsed) > 0 || len(signals.GapTopics) > 0 || len(signals.Contradictions) > 0 || len(noiseIDs) > 0) {
		s.logger.Printf("[req %d %s tid=%s] inline-reflection: injected=%d used=%d noise=%d gaps=%d contradictions=%d",
			ctx.ReqIdx, ctx.Project, ctx.ThreadID, len(prevInjected), len(confirmedUsed), len(noiseIDs), len(signals.GapTopics), len(signals.Contradictions))
	}
}

func (s *Server) trackOpenAIInjectedLearnings(threadID, briefingText, assocContext string) {
	if threadID == "" {
		return
	}

	currentIDs := make(map[int64]string)
	for _, m := range idPattern.FindAllStringSubmatch(briefingText, -1) {
		if id, err := strconv.ParseInt(m[1], 10, 64); err == nil {
			currentIDs[id] = "briefing"
		}
	}
	for _, m := range idPattern.FindAllStringSubmatch(assocContext, -1) {
		if id, err := strconv.ParseInt(m[1], 10, 64); err == nil {
			currentIDs[id] = "associative"
		}
	}
	if len(currentIDs) == 0 {
		return
	}

	s.lastInjectedIDsMu.Lock()
	existing := s.lastInjectedIDs[threadID]
	if existing == nil {
		existing = make(map[int64]string)
	}
	for id, source := range currentIDs {
		existing[id] = source
	}
	s.lastInjectedIDs[threadID] = existing
	s.lastInjectedIDsMu.Unlock()
}

func (s *Server) annotateOpenAIMessageMetadata(req map[string]any, threadID string) {
	if threadID == "" {
		return
	}
	msgs, ok := req["messages"].([]any)
	if !ok || len(msgs) == 0 {
		return
	}

	now := time.Now()
	nowStr := shortWeekday(now.Weekday()) + " " + now.Format("2006-01-02 15:04:05")

	cacheIdx := -1
	for i, m := range msgs {
		msg, _ := m.(map[string]any)
		if msg == nil {
			continue
		}
		if _, has := msg["cache_control"]; has {
			cacheIdx = i
		}
		if content, ok := msg["content"].([]any); ok {
			for _, block := range content {
				if b, ok := block.(map[string]any); ok {
					if _, has := b["cache_control"]; has {
						cacheIdx = i
					}
				}
			}
		}
	}

	startIdx := cacheIdx + 1
	if startIdx < 0 {
		startIdx = 0
	}

	s.responseTsMu.RLock()
	respTime, hasRespTime := s.responseTimes[threadID]
	s.responseTsMu.RUnlock()

	lastMsg, _ := msgs[len(msgs)-1].(map[string]any)
	if lastMsg != nil {
		role, _ := lastMsg["role"].(string)
		if role == "user" || role == "tool" {
			meta := fmt.Sprintf("[%s] [msg:%d]", nowStr, s.msgCounters.nextFor(threadID, len(msgs)-1))
			if hasRespTime {
				meta += " [+" + formatDelta(now.Sub(respTime)) + "]"
			}
			prependMeta(lastMsg, meta)
		}
	}

	if hasRespTime {
		for i := len(msgs) - 1; i >= startIdx; i-- {
			msg, _ := msgs[i].(map[string]any)
			if msg != nil && msg["role"] == "assistant" {
				meta := fmt.Sprintf("[%s] [msg:%d]", respTime.Format("2006-01-02 15:04:05"), i)
				prependMeta(msg, meta)
				break
			}
		}
	}
}

func (s *Server) forwardOpenAIWithTracking(w http.ResponseWriter, origReq *http.Request, body []byte, reqIdx int, toolUseIDs []string, project string, threadID string, estimatedTokens, msgCount int, parser openAIUsageParser) {
	_ = project
	targetURL := s.cfg.OpenAITargetURL
	if targetURL == "" {
		targetURL = "https://api.openai.com"
	}

	proxyReq, err := http.NewRequestWithContext(origReq.Context(), origReq.Method, targetURL+origReq.URL.RequestURI(), bytes.NewReader(body))
	if err != nil {
		http.Error(w, "create request: "+err.Error(), http.StatusBadGateway)
		return
	}

	for key, vals := range origReq.Header {
		for _, v := range vals {
			proxyReq.Header.Add(key, v)
		}
	}
	proxyReq.Header.Set("Content-Length", strconv.Itoa(len(body)))
	proxyReq.Header.Del("Connection")
	proxyReq.Header.Del("Accept-Encoding")

	if s.cacheTTLDetector != nil {
		s.cacheTTLDetector.RecordRequest(threadID)
	}

	resp, err := s.httpClient.Do(proxyReq)
	if err != nil {
		http.Error(w, "upstream: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

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

	ct := resp.Header.Get("Content-Type")
	if !strings.Contains(ct, "text/event-stream") {
		bodyBytes, readErr := io.ReadAll(responseBody)
		if readErr != nil {
			return
		}
		_, _ = w.Write(bodyBytes)
		s.trackOpenAINonStreamingUsage(reqIdx, bodyBytes, threadID, estimatedTokens, msgCount)
		return
	}

	flusher, canFlush := w.(http.Flusher)
	reader := bufio.NewReaderSize(responseBody, 8192)
	usage := &UsageTracker{}
	var textAccum strings.Builder
	var textDone bool

	for {
		line, readErr := reader.ReadBytes('\n')
		if len(line) > 0 {
			trimmedLine := bytes.TrimSpace(line)
			if bytes.HasPrefix(trimmedLine, []byte("data: ")) {
				data := bytes.TrimSpace(bytes.TrimPrefix(trimmedLine, []byte("data: ")))
				if parser != nil {
					parser.ParseUsage(usage, data)
					parser.ParseAnnotation(data, &textAccum, &textDone, 120)
				}
			}
			if _, writeErr := w.Write(line); writeErr != nil {
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

	var fwdModel struct{ Model string `json:"model"` }
	json.Unmarshal(body, &fwdModel)
	s.finalizeOpenAIUsage(reqIdx, threadID, estimatedTokens, msgCount, usage, fwdModel.Model, rlJSON)

	// NOTE: No keepalive reset here — OpenAI-format body would be rejected by Anthropic API.
	// TTL detection is handled in finalizeOpenAIUsage via cacheTTLDetector.RecordResponse().

	if usage.Complete && textAccum.Len() > 0 && len(toolUseIDs) > 0 {
		annotation := strings.TrimSpace(textAccum.String())
		if len([]rune(annotation)) > 120 {
			annotation = string([]rune(annotation)[:120])
		}
		s.mu.Lock()
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

func (s *Server) trackOpenAINonStreamingUsage(reqIdx int, body []byte, threadID string, estimatedTokens, msgCount int) {
	var parsed struct {
		Model string         `json:"model"`
		Usage map[string]any `json:"usage"`
	}
	if json.Unmarshal(body, &parsed) != nil || parsed.Usage == nil {
		return
	}

	u := &UsageTracker{Complete: true}
	if v, ok := parsed.Usage["prompt_tokens"].(float64); ok {
		u.InputTokens = int(v)
	}
	if v, ok := parsed.Usage["completion_tokens"].(float64); ok {
		u.OutputTokens = int(v)
	}
	if u.InputTokens == 0 {
		if v, ok := parsed.Usage["input_tokens"].(float64); ok {
			u.InputTokens = int(v)
		}
	}
	if u.OutputTokens == 0 {
		if v, ok := parsed.Usage["output_tokens"].(float64); ok {
			u.OutputTokens = int(v)
		}
	}
	if u.InputTokens == 0 {
		if total, ok := parsed.Usage["total_tokens"].(float64); ok && u.OutputTokens > 0 {
			u.InputTokens = int(total) - u.OutputTokens
		}
	}
	if details, ok := parsed.Usage["input_tokens_details"].(map[string]any); ok {
		if v, ok := details["cached_tokens"].(float64); ok {
			u.CacheReadInputTokens = int(v)
		}
	}
	s.finalizeOpenAIUsage(reqIdx, threadID, estimatedTokens, msgCount, u, parsed.Model, "")
}

func (s *Server) finalizeOpenAIUsage(reqIdx int, threadID string, estimatedTokens, msgCount int, usage *UsageTracker, model string, rlJSON string) {
	if usage == nil || (usage.TotalInputTokens() == 0 && usage.OutputTokens == 0) {
		return
	}

	if reqIdx > 0 {
		s.logger.Printf("[req %d] %s", reqIdx, usage.LogLine(reqIdx, 0, estimatedTokens, threadID))
	}
	if s.cfg.SawtoothEnabled && threadID != "" && s.sawtoothTrigger != nil {
		s.sawtoothTrigger.UpdateAfterResponse(threadID, usage.TotalInputTokens(), msgCount)
	}

	if threadID != "" && s.cfg.DataDir != "" {
		go s.queryDaemon("_track_usage", map[string]any{
			"thread_id":          threadID,
			"input_tokens":       usage.TotalInputTokens(),
			"output_tokens":      usage.OutputTokens,
			"cache_read_tokens":  usage.CacheReadInputTokens,
			"cache_write_tokens": usage.CacheCreationInputTokens,
		})
	}
	// Persist rate-limit snapshot (independent of threadID)
	if rlJSON != "" && s.cfg.DataDir != "" {
		go s.queryDaemon("_persist_rate_limits", map[string]any{"rate_limits": rlJSON})
	}

	if s.cacheStatusWriter != nil {
		s.cacheStatusWriter.Update(time.Now(), usage.TotalInputTokens(), usage.CacheReadInputTokens, usage.CacheCreationInputTokens, threadID)
		s.cacheStatusWriter.UpdateThresholdForThread(threadID, s.effectiveTokenThreshold(model))
		if raw := s.getRawEstimate(threadID); raw > 0 {
			s.cacheStatusWriter.UpdateRawForThread(threadID, raw)
		}
	}

	// Feed TTL detector with cache hit/miss signal
	if s.cacheTTLDetector != nil {
		prevState := s.cacheTTLDetector.Is1hSupported()
		s.cacheTTLDetector.RecordResponse(usage.CacheReadInputTokens, usage.CacheCreationInputTokens, 0)
		newState := s.cacheTTLDetector.Is1hSupported()
		if prevState == nil && newState != nil {
			if *newState && s.frozenStubs != nil {
				s.frozenStubs.UpdateTTL(sawtoothTTLForCacheTTL("1h"))
			}
			if s.cacheKeepalive != nil {
				s.cacheKeepalive.Retrigger()
			}
		}
	}

	if threadID != "" {
		s.responseTsMu.Lock()
		if s.responseTimes == nil {
			s.responseTimes = make(map[string]time.Time)
		}
		s.responseTimes[threadID] = time.Now()
		s.responseTsMu.Unlock()
	}

	if s.cfg.DataDir != "" {
		go func(u *UsageTracker) {
			s.queryDaemon("track_proxy_usage", map[string]any{
				"input_tokens":          u.InputTokens,
				"output_tokens":         u.OutputTokens,
				"cache_read_tokens":     u.CacheReadInputTokens,
				"cache_creation_tokens": u.CacheCreationInputTokens,
			})
		}(usage)
	}
}

type openAIUsageParser interface {
	ParseUsage(usage *UsageTracker, data []byte)
	ParseAnnotation(data []byte, accum *strings.Builder, done *bool, maxLen int)
}

type chatCompletionsParser struct{}

func (chatCompletionsParser) ParseUsage(usage *UsageTracker, data []byte) {
	if usage == nil || len(data) == 0 || data[0] != '{' {
		return
	}
	var chunk struct {
		Usage *OpenAIUsage `json:"usage"`
	}
	if json.Unmarshal(data, &chunk) != nil || chunk.Usage == nil {
		return
	}
	if chunk.Usage.PromptTokens > 0 {
		usage.InputTokens = chunk.Usage.PromptTokens
	}
	if chunk.Usage.CompletionTokens > 0 {
		usage.OutputTokens = chunk.Usage.CompletionTokens
	}
	if chunk.Usage.TotalTokens > 0 && usage.InputTokens == 0 && usage.OutputTokens > 0 {
		usage.InputTokens = chunk.Usage.TotalTokens - usage.OutputTokens
	}
	if chunk.Usage.TotalTokens > 0 || chunk.Usage.CompletionTokens > 0 {
		usage.Complete = true
	}
}

func (chatCompletionsParser) ParseAnnotation(data []byte, accum *strings.Builder, done *bool, maxLen int) {
	if *done || len(data) == 0 || data[0] != '{' {
		return
	}
	var chunk struct {
		Choices []struct {
			Delta struct {
				Content string `json:"content"`
			} `json:"delta"`
			FinishReason *string `json:"finish_reason"`
		} `json:"choices"`
	}
	if json.Unmarshal(data, &chunk) != nil {
		return
	}
	for _, choice := range chunk.Choices {
		if choice.Delta.Content != "" {
			accum.WriteString(choice.Delta.Content)
			if accum.Len() >= maxLen {
				*done = true
				return
			}
		}
		if choice.FinishReason != nil && *choice.FinishReason != "" {
			*done = true
		}
	}
}

type responsesParser struct{}

func (responsesParser) ParseUsage(usage *UsageTracker, data []byte) {
	if usage == nil || len(data) == 0 || data[0] != '{' {
		return
	}
	var event struct {
		Type     string `json:"type"`
		Response struct {
			Usage struct {
				InputTokens  int `json:"input_tokens"`
				OutputTokens int `json:"output_tokens"`
				InputDetails struct {
					CachedTokens int `json:"cached_tokens"`
				} `json:"input_tokens_details"`
			} `json:"usage"`
		} `json:"response"`
	}
	if json.Unmarshal(data, &event) != nil || event.Type != "response.completed" {
		return
	}
	usage.InputTokens = event.Response.Usage.InputTokens
	usage.OutputTokens = event.Response.Usage.OutputTokens
	usage.CacheReadInputTokens = event.Response.Usage.InputDetails.CachedTokens
	usage.Complete = true
}

func (responsesParser) ParseAnnotation(data []byte, accum *strings.Builder, done *bool, maxLen int) {
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
	if json.Unmarshal(data, &event) != nil {
		return
	}
	if event.Type == "response.content_part.delta" && event.Delta.Type == "text_delta" {
		accum.WriteString(event.Delta.Text)
		if accum.Len() >= maxLen {
			*done = true
		}
	}
	if event.Type == "response.completed" && accum.Len() > 0 {
		*done = true
	}
}
