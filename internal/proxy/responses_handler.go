package proxy

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

// isResponsesPath returns true if the request targets the OpenAI Responses API endpoint.
func isResponsesPath(r *http.Request) bool {
	return r.Method == "POST" && strings.HasPrefix(r.URL.Path, "/v1/responses")
}

// handleResponses handles OpenAI Responses API /v1/responses requests.
// Flow: parse Responses → translate to Anthropic internal → run compression pipeline →
// translate back to Responses → forward to OpenAI upstream → passthrough response.
func (s *Server) handleResponses(w http.ResponseWriter, r *http.Request) {
	reqIdx := int(s.requestIdx.Add(1))

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
		return
	}

	var reqMap map[string]any
	if err := json.Unmarshal(body, &reqMap); err != nil {
		http.Error(w, "parse request: "+err.Error(), http.StatusBadRequest)
		return
	}

	model, _ := reqMap["model"].(string)
	wantsStream, _ := reqMap["stream"].(bool)

	inputCount := 0
	switch input := reqMap["input"].(type) {
	case string:
		inputCount = 1
	case []any:
		inputCount = len(input)
	}

	s.logger.Printf("[req %d] %sResponses adapter: model=%s stream=%v input=%d%s",
		reqIdx, colorBlue, model, wantsStream, inputCount, colorReset)

	anthReq, err := translateResponsesToAnthropic(reqMap)
	if err != nil {
		http.Error(w, "translate request: "+err.Error(), http.StatusBadRequest)
		return
	}

	ctx := s.prepareOpenAIRequestContext(anthReq, reqIdx, r.Header.Get("X-Claude-Code-Session-Id"))
	s.runOpenAIParityPipeline(anthReq, &ctx)

	outReq, err := translateAnthropicToResponses(anthReq)
	if err != nil {
		http.Error(w, "translate request: "+err.Error(), http.StatusInternalServerError)
		return
	}
	outReq["stream"] = wantsStream
	copyResponsesPassthroughFields(outReq, reqMap)

	outBody, err := json.Marshal(outReq)
	if err != nil {
		http.Error(w, "marshal: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.forwardOpenAIWithTracking(w, r, outBody, reqIdx, ctx.ToolUseIDs, ctx.Project, ctx.ThreadID, ctx.EstimatedTokens, ctx.MessageCount, responsesParser{})
}

func copyResponsesPassthroughFields(dst, src map[string]any) {
	skip := map[string]bool{
		"model":             true,
		"input":             true,
		"instructions":      true,
		"tools":             true,
		"stream":            true,
		"max_output_tokens": true,
		"temperature":       true,
		"top_p":             true,
		"metadata":          true,
	}
	for key, value := range src {
		if skip[key] {
			continue
		}
		if _, exists := dst[key]; exists {
			continue
		}
		dst[key] = value
	}
}
