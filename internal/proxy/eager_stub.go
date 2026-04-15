package proxy

import (
	"fmt"
	"regexp"
	"strings"
)

const eagerStubTokenThreshold = 500

// EagerStubToolResults walks the fresh tail (messages after frozenBoundary)
// and replaces large tool_result content with rule-based summaries.
// Only stubs tool_results that (a) exceed the token threshold and (b) have a
// following assistant turn (meaning Claude already processed them).
// Operates in the uncached zone — zero prompt cache cost.
func EagerStubToolResults(messages []any, frozenBoundary int, estimateTokens TokenEstimateFunc) []any {
	result := make([]any, len(messages))
	copy(result, messages)

	for i := frozenBoundary; i < len(result); i++ {
		msg, ok := result[i].(map[string]any)
		if !ok || msg["role"] != "user" {
			continue
		}

		blocks, ok := msg["content"].([]any)
		if !ok {
			continue
		}

		hasFollowingAssistant := false
		for j := i + 1; j < len(result); j++ {
			if m, ok := result[j].(map[string]any); ok && m["role"] == "assistant" {
				hasFollowingAssistant = true
				break
			}
		}
		if !hasFollowingAssistant {
			continue
		}

		// Find the matching tool_use in the previous assistant message
		var toolName string
		var toolInput map[string]any
		if i > 0 {
			if prev, ok := result[i-1].(map[string]any); ok && prev["role"] == "assistant" {
				toolName, toolInput = eagerExtractToolInfo(prev["content"])
			}
		}

		anyChanged := false
		newBlocks := make([]any, 0, len(blocks))
		for _, block := range blocks {
			b, ok := block.(map[string]any)
			if !ok || b["type"] != "tool_result" {
				newBlocks = append(newBlocks, block)
				continue
			}

			content := extractToolResultText(b)
			if estimateTokens(content) <= eagerStubTokenThreshold {
				newBlocks = append(newBlocks, block)
				continue
			}

			stub := buildEagerStub(toolName, toolInput, content)
			newBlock := make(map[string]any)
			for k, v := range b {
				newBlock[k] = v
			}
			newBlock["content"] = stub
			newBlocks = append(newBlocks, newBlock)
			anyChanged = true
		}

		if anyChanged {
			newMsg := make(map[string]any)
			for k, v := range msg {
				newMsg[k] = v
			}
			newMsg["content"] = newBlocks
			result[i] = newMsg
		}
	}

	return result
}

func eagerExtractToolInfo(content any) (string, map[string]any) {
	blocks, ok := content.([]any)
	if !ok {
		return "", nil
	}
	for _, block := range blocks {
		b, ok := block.(map[string]any)
		if !ok {
			continue
		}
		if b["type"] == "tool_use" {
			name, _ := b["name"].(string)
			input, _ := b["input"].(map[string]any)
			return name, input
		}
	}
	return "", nil
}

func buildEagerStub(toolName string, input map[string]any, content string) string {
	lines := strings.Split(content, "\n")
	lineCount := len(lines)

	switch toolName {
	case "Read":
		path, _ := input["file_path"].(string)
		funcs := eagerExtractFuncSignatures(content)
		funcStr := ""
		if len(funcs) > 0 {
			funcStr = " | " + strings.Join(funcs, ", ")
		}
		return fmt.Sprintf("[Read %s — %d lines%s]", path, lineCount, funcStr)

	case "Grep":
		pattern, _ := input["pattern"].(string)
		path, _ := input["path"].(string)
		return fmt.Sprintf("[Grep '%s' in %s — %d matches]", pattern, path, lineCount)

	case "Bash":
		cmd, _ := input["command"].(string)
		head, tail := eagerHeadTail(lines, 3, 3)
		if tail != "" {
			return fmt.Sprintf("[Bash: %s — %d lines]\n%s\n[...]\n%s", truncateStr(cmd, 80), lineCount, head, tail)
		}
		return fmt.Sprintf("[Bash: %s — %d lines]\n%s", truncateStr(cmd, 80), lineCount, head)

	case "Glob":
		pattern, _ := input["pattern"].(string)
		first := eagerFirstN(lines, 10)
		return fmt.Sprintf("[Glob '%s' — %d results]\n%s", pattern, lineCount, first)

	case "Agent":
		desc, _ := input["description"].(string)
		return fmt.Sprintf("[Agent: %s — %s]", desc, truncateStr(content, 200))

	default:
		return fmt.Sprintf("[%s result — %d lines archived]", toolName, lineCount)
	}
}

var eagerGoFuncRe = regexp.MustCompile(`(?m)^func\s+(\([^)]+\)\s+)?(\w+)\s*\(`)

func eagerExtractFuncSignatures(code string) []string {
	matches := eagerGoFuncRe.FindAllStringSubmatch(code, 20)
	var names []string
	for _, m := range matches {
		if len(m) > 2 {
			names = append(names, m[2]+"()")
		}
	}
	return names
}

func eagerHeadTail(lines []string, h, t int) (string, string) {
	if len(lines) <= h+t {
		return strings.Join(lines, "\n"), ""
	}
	return strings.Join(lines[:h], "\n"), strings.Join(lines[len(lines)-t:], "\n")
}

func eagerFirstN(lines []string, n int) string {
	if len(lines) <= n {
		return strings.Join(lines, "\n")
	}
	return strings.Join(lines[:n], "\n") + fmt.Sprintf("\n[... +%d more]", len(lines)-n)
}
