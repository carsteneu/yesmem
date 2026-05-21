package proxy

import (
	"strings"
)

const ccSystemMarker = "Claude Code, Anthropic's official CLI"

// findCCSystemBlockIndex finds the system block index containing the CC marker.
func findCCSystemBlockIndex(req map[string]any) int {
	sys, ok := req["system"].([]any)
	if !ok {
		return -1
	}
	for i, b := range sys {
		blk, ok := b.(map[string]any)
		if !ok {
			continue
		}
		text, _ := blk["text"].(string)
		if strings.Contains(text, ccSystemMarker) {
			return i
		}
	}
	return -1
}

// extractCCWorkingDir extracts the working directory from a CC system prompt.
// Delegates to the existing extractWorkingDirectoryFromText helper.
func extractCCWorkingDir(orig string) string {
	return extractWorkingDirectoryFromText(orig)
}

// replaceCCSystemBlock replaces the CC system block with the given content.
func replaceCCSystemBlock(req map[string]any, replacement []byte) {
	idx := findCCSystemBlockIndex(req)
	if idx < 0 {
		return
	}
	sys := req["system"].([]any)
	blk := sys[idx].(map[string]any)
	blk["text"] = string(replacement)
}

// applyCCSystemPrompt replaces the CC system block with the filled template.
// Returns true if replacement was performed.
func (s *Server) applyCCSystemPrompt(req map[string]any) bool {
	if !s.cfg.CustomSystemPrompt.EnabledClaudeCode || len(s.customSystemPrompt) == 0 {
		return false
	}
	idx := findCCSystemBlockIndex(req)
	if idx < 0 {
		return false
	}
	orig := req["system"].([]any)[idx].(map[string]any)["text"].(string)
	modelID, _ := req["model"].(string)
	ctx := buildSystemContext(buildSystemContextOpts{
		WorkingDir:       extractCCWorkingDir(orig),
		ModelID:          modelID,
		ModelDisplayName: modelDisplayName(modelID),
		HostAgentName:    "Claude Code",
	})
	replaceCCSystemBlock(req, fillSystemTemplate(s.customSystemPrompt, ctx))
	return true
}
