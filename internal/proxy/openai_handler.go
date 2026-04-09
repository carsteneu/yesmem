package proxy

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

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
	s.logger.Printf("[req %d] %sOpenAI adapter: model=%s stream=%v msgs=%d%s",
		reqIdx, colorBlue, oaiReq.Model, wantsStream, len(oaiReq.Messages), colorReset)

	anthReq, err := translateOpenAIToAnthropic(oaiReq)
	if err != nil {
		http.Error(w, "translate request: "+err.Error(), http.StatusBadRequest)
		return
	}
	ctx := s.prepareOpenAIRequestContext(anthReq, reqIdx, r.Header.Get("X-Claude-Code-Session-Id"))
	s.runOpenAIParityPipeline(anthReq, &ctx)

	outReq, err := translateAnthropicToOpenAI(anthReq)
	if err != nil {
		http.Error(w, "translate request: "+err.Error(), http.StatusInternalServerError)
		return
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

	s.logger.Printf("[req %d] %sOpenAI passthrough: status=%d bytes=%d%s",
		reqIdx, colorBlue, resp.StatusCode, written, colorReset)
}
