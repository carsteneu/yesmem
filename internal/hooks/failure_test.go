package hooks

import (
	"encoding/json"
	"testing"
)

func TestNormalizeToolOutput(t *testing.T) {
	tests := []struct {
		name         string
		toolOutput   string
		errorField   string
		toolResponse json.RawMessage
		wantOutput   string
	}{
		{
			name:       "keeps existing tool_output",
			toolOutput: "some error",
			wantOutput: "some error",
		},
		{
			name:       "uses error field when tool_output empty (WebFetch format)",
			errorField: "Claude Code is unable to fetch from www.reddit.com",
			wantOutput: "Claude Code is unable to fetch from www.reddit.com",
		},
		{
			name:         "extracts error from tool_response object",
			toolResponse: json.RawMessage(`{"error":"Claude Code is unable to fetch from www.reddit.com"}`),
			wantOutput:   "Claude Code is unable to fetch from www.reddit.com",
		},
		{
			name:         "extracts error from tool_response string",
			toolResponse: json.RawMessage(`"Request failed with status code 403"`),
			wantOutput:   "Request failed with status code 403",
		},
		{
			name:         "no error pattern in tool_response leaves output empty",
			toolResponse: json.RawMessage(`{"content":"<html>normal page</html>"}`),
			wantOutput:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hook := &FailureInput{
				ToolOutput:   tt.toolOutput,
				Error:        tt.errorField,
				ToolResponse: tt.toolResponse,
			}
			normalizeToolOutput(hook)
			if hook.ToolOutput != tt.wantOutput {
				t.Errorf("normalizeToolOutput() ToolOutput = %q, want %q", hook.ToolOutput, tt.wantOutput)
			}
		})
	}
}

func TestExtractToolContext_WebFetch(t *testing.T) {
	tests := []struct {
		name     string
		url      string
		wantKW   string // expected keyword prefix in result
	}{
		{
			name:   "reddit URL yields WebFetch domain",
			url:    "https://www.reddit.com/r/AIMemory/comments/1s62050/what_an_ai_memory_systems_should_look_like_in_2026/",
			wantKW: "WebFetch www.reddit.com",
		},
		{
			name:   "github URL yields WebFetch domain",
			url:    "https://github.com/Haagndaazer/vibe-cognition",
			wantKW: "WebFetch github.com",
		},
		{
			name:   "simple domain URL yields WebFetch domain",
			url:    "https://arxiv.org/abs/2511.20857",
			wantKW: "WebFetch arxiv.org",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input, _ := json.Marshal(map[string]string{"url": tt.url, "prompt": "test"})
			hook := &FailureInput{
				ToolName:  "WebFetch",
				ToolInput: input,
			}
			got := extractToolContext(hook)
			if got != tt.wantKW {
				t.Errorf("extractToolContext WebFetch = %q, want %q", got, tt.wantKW)
			}
		})
	}
}
