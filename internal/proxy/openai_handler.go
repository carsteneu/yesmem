package proxy

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// fmtReq formats a request identifier for log lines.
// Example: [req 3 v2.0.1-213]
func fmtReq(reqIdx int, version string) string {
	return fmt.Sprintf("[req %d %s]", reqIdx, version)
}

// isOpenAIPath returns true if the request targets OpenAI-format endpoints.
func isOpenAIPath(r *http.Request) bool {
	return r.Method == "POST" && strings.HasPrefix(r.URL.Path, "/v1/chat/completions")
}

// handleOpenAICompletions handles OpenAI-format /v1/chat/completions requests.
// Flow: parse OpenAI → translate to Anthropic internal → run compression pipeline →
// translate back to OpenAI → forward to OpenAI upstream → passthrough response.
func (s *Server) handleOpenAICompletions(w http.ResponseWriter, r *http.Request) {
	reqIdx := int(s.requestIdx.Add(1))

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
		return
	}

	var oaiReq OpenAIChatRequest
	if err := json.Unmarshal(body, &oaiReq); err != nil {
		http.Error(w, "parse request: "+err.Error(), http.StatusBadRequest)
		return
	}

	wantsStream := oaiReq.Stream
	s.logger.Printf("%s %sOpenAI adapter: model=%s stream=%v msgs=%d%s",
		fmtReq(reqIdx, s.version), colorBlue, oaiReq.Model, wantsStream, len(oaiReq.Messages), colorReset)

	anthReq, err := translateOpenAIToAnthropic(oaiReq)
	if err != nil {
		http.Error(w, "translate request: "+err.Error(), http.StatusBadRequest)
		return
	}
	msgsAfterTrans, _ := anthReq["messages"].([]any)
	s.logger.Printf("%s FWD-TRANSLATE: input=%d output=%d", fmtReq(reqIdx, s.version), len(oaiReq.Messages), len(msgsAfterTrans))
	s.logger.Printf("%s OPENAI-PIPE: after-translate msgs=%d", fmtReq(reqIdx, s.version), len(msgsAfterTrans))

	ocSessionID := r.Header.Get("x-opencode-session")
	if ocSessionID == "" {
		ocSessionID = r.Header.Get("x-session-affinity")
	}
	if ocSessionID != "" {
		s.logger.Printf("%s %sopencode session=%s%s", fmtReq(reqIdx, s.version), colorGreen, ocSessionID, colorReset)
	}
	ctx := s.prepareOpenAIRequestContext(anthReq, reqIdx, r.Header.Get("X-Claude-Code-Session-Id"), ocSessionID, r.Header.Get("User-Agent"))
	ctx.Model = oaiReq.Model

	// Persist active opencode session so whoami can resolve it.
	// OpenCode has no hooks — the proxy is the only source of session identity.
	if ocSessionID != "" {
		s.queryDaemon("set_proxy_state", map[string]any{
			"key":   "active_session_opencode",
			"value": "opencode:" + ocSessionID,
		})
	}

	// Non-interactive requests (CLI tools, extraction pipeline) have no session headers.
	// Skip the entire proxy pipeline — no MCP calls, no associative context, no system blocks.
	// Just validate the request and forward upstream.
	// YESMEM_ALLOW_CHILD_MCP header overrides: daemon child with explicit MCP permission.
	allowChildMCP := r.Header.Get("x-yesmem-allow-mcp") == "1"
	headerClaudeSession := r.Header.Get("X-Claude-Code-Session-Id")
	ua := strings.ToLower(r.Header.Get("User-Agent"))
	isCodex := strings.Contains(ua, "codex") || strings.Contains(ua, "codexcli")
	if ocSessionID == "" && headerClaudeSession == "" && !isCodex && !allowChildMCP {
		s.logger.Printf("%s non-interactive request — skipping proxy pipeline", fmtReq(reqIdx, s.version))
	} else {
		// Replace default OpenCode system prompt with custom template (if enabled and loaded)
		if s.cfg.CustomSystemPrompt.EnabledOpenCode && s.customSystemPrompt != nil {
			skillBlock := extractSkillBlock(anthReq)
			workDir := extractWorkingDirectory(anthReq)
			tplCtx := buildSystemContext(buildSystemContextOpts{
				WorkingDir:    workDir,
				ModelID:       ctx.Model,
				ModelDisplayName: modelDisplayName(ctx.Model),
				HostAgentName: "OpenCode",
			})
			tpl := s.resolveSystemTemplate(ctx.Model)
			if tpl == nil {
				tpl = s.customSystemPrompt
			}
			filled := fillSystemTemplate(tpl, tplCtx)
			filled = append(filled, []byte(skillBlock)...)
			replaceFirstSystemBlock(anthReq, string(filled))
			s.logger.Printf("%s %sCUSTOM-SYSTEM: applied (%d bytes, skillBlock=%d)%s",
				fmtReq(reqIdx, s.version), colorGreen, len(filled), len(skillBlock), colorReset)
			if skillBlock == "" {
				s.logger.Printf("%s %sCUSTOM-SYSTEM: no <available_skills> found in original prompt%s",
					fmtReq(reqIdx, s.version), colorOrange, colorReset)
			}
		}
		s.runOpenAIParityPipeline(anthReq, &ctx)
	}

	msgsAfterPipe, _ := anthReq["messages"].([]any)
	s.logger.Printf("%s OPENAI-PIPE: after-pipeline msgs=%d (was %d)", fmtReq(reqIdx, s.version), len(msgsAfterPipe), len(msgsAfterTrans))

	outReq, err := translateAnthropicToOpenAI(anthReq)
	if err != nil {
		http.Error(w, "translate request: "+err.Error(), http.StatusInternalServerError)
		return
	}
	msgsOut, _ := outReq["messages"].([]any)
	s.logger.Printf("%s OPENAI-PIPE: after-reverse msgs=%d", fmtReq(reqIdx, s.version), len(msgsOut))
	for i, m := range msgsOut {
		if msg, ok := m.(map[string]any); ok {
			role, _ := msg["role"].(string)
			_ = i
			_ = role
			// keep json.Marshal for side-effect validation
			if j, _ := json.Marshal(msg); len(j) < 200 {
				// s.logger.Printf("[req %d] OPENAI-OUT msg[%d] role=%s: %s", reqIdx, i, role, string(j))
			} else {
				// s.logger.Printf("[req %d] OPENAI-OUT msg[%d] role=%s len=%d", reqIdx, i, role, len(j))
			}
		}
	}
	outReq["stream"] = wantsStream

	outBody, err := json.Marshal(outReq)
	if err != nil {
		http.Error(w, "marshal: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.forwardOpenAIWithTracking(w, r, outBody, reqIdx, ctx.ToolUseIDs, ctx.Project, ctx.ThreadID, ctx.EstimatedTokens, ctx.MessageCount, chatCompletionsParser{})
}

// passthroughResponse copies the upstream response directly to the client.
// Used when both client and upstream speak the same format (OpenAI↔OpenAI).
func (s *Server) passthroughResponse(w http.ResponseWriter, resp *http.Response, reqIdx int) {
	// Copy all response headers
	for key, vals := range resp.Header {
		for _, v := range vals {
			w.Header().Add(key, v)
		}
	}
	w.WriteHeader(resp.StatusCode)

	written, _ := io.Copy(w, resp.Body)

	s.logger.Printf("%s %sOpenAI passthrough: status=%d bytes=%d%s",
		fmtReq(reqIdx, s.version), colorBlue, resp.StatusCode, written, colorReset)
}
