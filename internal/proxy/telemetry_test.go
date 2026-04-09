package proxy

import (
	"testing"
)

func TestParseUsageFromSSE(t *testing.T) {
	tests := []struct {
		name       string
		line       string
		wantInput  int
		wantOutput int
		wantFound  bool
	}{
		{
			name:      "message_start with usage",
			line:      `data: {"type":"message_start","message":{"usage":{"input_tokens":150,"output_tokens":0}}}`,
			wantInput: 150,
			wantOutput: 0,
			wantFound: true,
		},
		{
			name:       "message_delta with output",
			line:       `data: {"type":"message_delta","usage":{"output_tokens":42}}`,
			wantInput:  0,
			wantOutput: 42,
			wantFound:  true,
		},
		{
			name:      "non-usage line",
			line:      `data: {"type":"content_block_delta"}`,
			wantFound: false,
		},
		{
			name:      "message_start zero tokens",
			line:      `data: {"type":"message_start","message":{"usage":{"input_tokens":0,"output_tokens":0}}}`,
			wantFound: false,
		},
		{
			name:      "message_delta zero output",
			line:      `data: {"type":"message_delta","usage":{"output_tokens":0}}`,
			wantFound: false,
		},
		{
			name:      "no data prefix",
			line:      `{"type":"message_start","message":{"usage":{"input_tokens":10}}}`,
			wantFound: false,
		},
		{
			name:      "message_stop",
			line:      `data: {"type":"message_stop"}`,
			wantFound: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in, out, found := parseUsageFromSSE(tt.line)
			if found != tt.wantFound {
				t.Errorf("found=%v want=%v", found, tt.wantFound)
			}
			if found && in != tt.wantInput {
				t.Errorf("input=%d want=%d", in, tt.wantInput)
			}
			if found && out != tt.wantOutput {
				t.Errorf("output=%d want=%d", out, tt.wantOutput)
			}
		})
	}
}
