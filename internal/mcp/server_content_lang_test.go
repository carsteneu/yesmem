package mcp

import (
	"strings"
	"testing"

	mcpserver "github.com/mark3labs/mcp-go/server"
)

// Language directive that must appear in descriptions of content-writing tools.
// Kept as a local const for the test — the production const lives in server.go.
const wantLangPhrase = "write content in English"

// contentToolNames lists the MCP tools that write user-facing content to the
// database. Each must include the LanguageDirective in its description.
var contentToolNames = []string{
	"remember",
	"pin",
	"save_cap",
	"set_persona",
	"scratchpad_write",
	"scratchpad_append",
}

func TestContentToolDescriptions_IncludeLanguageDirective(t *testing.T) {
	srv := &Server{}
	srv.srv = mcpserver.NewMCPServer("test", "0.0.0")
	srv.registerTools()

	tools := srv.srv.ListTools()
	toolByName := make(map[string]string, len(tools))
	for _, st := range tools {
		toolByName[st.Tool.Name] = st.Tool.Description
	}

	for _, name := range contentToolNames {
		desc, ok := toolByName[name]
		if !ok {
			t.Errorf("tool %q not registered", name)
			continue
		}
		if !strings.Contains(desc, wantLangPhrase) {
			t.Errorf("tool %q description missing language directive %q", name, wantLangPhrase)
		}
	}
}
