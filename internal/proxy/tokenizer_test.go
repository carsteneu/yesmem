package proxy

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	tokenizer "github.com/qhenkart/anthropic-tokenizer-go"
)

// TestTokenizerAccuracy compares the local tokenizer against our heuristic.
func TestTokenizerAccuracy(t *testing.T) {
	tok, err := tokenizer.New()
	if err != nil {
		t.Fatalf("tokenizer init: %v", err)
	}

	tests := []struct {
		name string
		text string
	}{
		{"short_english", "Hello, how are you?"},
		{"short_german", "Hallo, wie geht es dir?"},
		{"code_go", `func main() { fmt.Println("hello world") }`},
		{"json_schema", `{"type":"object","properties":{"file_path":{"type":"string","description":"The absolute path to the file"}},"required":["file_path"]}`},
		{"tool_stub", `[→] Read /home/user/project/main.go → deep_search('Read /home/user/project/main.go')`},
		{"long_text", "The quick brown fox jumps over the lazy dog. " +
			"This is a longer text that should help us understand how the tokenizer " +
			"handles multi-sentence English text with various punctuation marks, " +
			"including commas, periods, and other special characters like @#$%."},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			libTokens := tok.Tokens(tt.text)
			heuristicTokens := int(float64(len(tt.text)) / 3.6)
			ratio := float64(libTokens) / float64(heuristicTokens)
			t.Logf("lib=%d  heuristic=%d  ratio=%.2f  text=%d bytes",
				libTokens, heuristicTokens, ratio, len(tt.text))
		})
	}
}

// generateRealisticTool creates a tool definition similar to Claude Code's actual tools.
func generateRealisticTool(name, desc string, props map[string]any, required []string) map[string]any {
	return map[string]any{
		"name":        name,
		"description": desc,
		"input_schema": map[string]any{
			"$schema":              "https://json-schema.org/draft/2020-12/schema",
			"type":                 "object",
			"additionalProperties": false,
			"properties":           props,
			"required":             required,
		},
	}
}

// buildRealisticRequest builds a request similar to what Claude Code actually sends.
func buildRealisticRequest(numMessages int) map[string]any {
	// System prompt (~8k chars, realistic for Claude Code)
	systemText := strings.Repeat(
		"You are Claude Code, Anthropic's official CLI for Claude. "+
			"You are an interactive agent that helps users with software engineering tasks. "+
			"Use the instructions below and the tools available to you to assist the user. "+
			"IMPORTANT: Assist with authorized security testing, defensive security, CTF challenges. "+
			"When working with tool results, write down any important information. ", 20)

	// ~50 tools with realistic schemas (like Read, Edit, Write, Bash, Grep, Glob, Agent, etc.)
	tools := []any{
		generateRealisticTool("Read", "Reads a file from the local filesystem. You can access any file directly by using this tool. Assume this tool is able to read all files on the machine. If the User provides a path to a file assume that path is valid.", map[string]any{
			"file_path": map[string]any{"type": "string", "description": "The absolute path to the file to read"},
			"offset":    map[string]any{"type": "number", "description": "The line number to start reading from. Only provide if the file is too large to read at once"},
			"limit":     map[string]any{"type": "number", "description": "The number of lines to read. Only provide if the file is too large to read at once."},
		}, []string{"file_path"}),
		generateRealisticTool("Edit", "Performs exact string replacements in files. You must use your Read tool at least once in the conversation before editing.", map[string]any{
			"file_path":   map[string]any{"type": "string", "description": "The absolute path to the file to modify"},
			"old_string":  map[string]any{"type": "string", "description": "The text to replace"},
			"new_string":  map[string]any{"type": "string", "description": "The text to replace it with (must be different from old_string)"},
			"replace_all": map[string]any{"type": "boolean", "default": false, "description": "Replace all occurrences of old_string (default false)"},
		}, []string{"file_path", "old_string", "new_string"}),
		generateRealisticTool("Write", "Writes a file to the local filesystem. This tool will overwrite the existing file if there is one at the provided path.", map[string]any{
			"file_path": map[string]any{"type": "string", "description": "The absolute path to the file to write (must be absolute, not relative)"},
			"content":   map[string]any{"type": "string", "description": "The content to write to the file"},
		}, []string{"file_path", "content"}),
		generateRealisticTool("Bash", "Executes a given bash command and returns its output. The working directory persists between commands, but shell state does not.", map[string]any{
			"command":     map[string]any{"type": "string", "description": "The command to execute"},
			"description": map[string]any{"type": "string", "description": "Clear, concise description of what this command does"},
			"timeout":     map[string]any{"type": "number", "description": "Optional timeout in milliseconds (max 600000)"},
		}, []string{"command"}),
		generateRealisticTool("Grep", "A powerful search tool built on ripgrep. Supports full regex syntax.", map[string]any{
			"pattern":     map[string]any{"type": "string", "description": "The regular expression pattern to search for"},
			"path":        map[string]any{"type": "string", "description": "File or directory to search in"},
			"glob":        map[string]any{"type": "string", "description": "Glob pattern to filter files"},
			"output_mode": map[string]any{"type": "string", "enum": []string{"content", "files_with_matches", "count"}},
			"head_limit":  map[string]any{"type": "number", "description": "Limit output to first N lines/entries"},
			"-n":          map[string]any{"type": "boolean", "description": "Show line numbers in output"},
			"-i":          map[string]any{"type": "boolean", "description": "Case insensitive search"},
			"-A":          map[string]any{"type": "number", "description": "Lines after match"},
			"-B":          map[string]any{"type": "number", "description": "Lines before match"},
			"-C":          map[string]any{"type": "number", "description": "Context lines"},
			"context":     map[string]any{"type": "number", "description": "Context lines (alias)"},
			"multiline":   map[string]any{"type": "boolean", "description": "Enable multiline mode"},
			"type":        map[string]any{"type": "string", "description": "File type filter"},
			"offset":      map[string]any{"type": "number", "description": "Skip first N entries"},
		}, []string{"pattern"}),
		generateRealisticTool("Glob", "Fast file pattern matching tool that works with any codebase size. Returns matching file paths sorted by modification time.", map[string]any{
			"pattern": map[string]any{"type": "string", "description": "The glob pattern to match files against"},
			"path":    map[string]any{"type": "string", "description": "The directory to search in"},
		}, []string{"pattern"}),
		generateRealisticTool("Agent", "Launch a new agent to handle complex, multi-step tasks autonomously. The Agent tool launches specialized agents.", map[string]any{
			"prompt":          map[string]any{"type": "string", "description": "The task for the agent to perform"},
			"description":     map[string]any{"type": "string", "description": "A short (3-5 word) description of the task"},
			"subagent_type":   map[string]any{"type": "string", "description": "The type of specialized agent"},
			"isolation":       map[string]any{"type": "string", "enum": []string{"worktree"}},
			"run_in_background": map[string]any{"type": "boolean", "description": "Run in background"},
			"resume":          map[string]any{"type": "string", "description": "Agent ID to resume from"},
		}, []string{"description", "prompt"}),
		generateRealisticTool("WebSearch", "Allows Claude to search the web and use the results to inform responses.", map[string]any{
			"query":           map[string]any{"type": "string", "description": "The search query to use", "minLength": 2},
			"allowed_domains": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"blocked_domains": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		}, []string{"query"}),
		generateRealisticTool("WebFetch", "Fetches content from a specified URL and processes it using an AI model.", map[string]any{
			"url":    map[string]any{"type": "string", "format": "uri", "description": "The URL to fetch content from"},
			"prompt": map[string]any{"type": "string", "description": "The prompt to run on the fetched content"},
		}, []string{"url", "prompt"}),
		generateRealisticTool("AskUserQuestion", "Use this tool when you need to ask the user questions during execution.", map[string]any{
			"questions": map[string]any{
				"type": "array", "minItems": 1, "maxItems": 4,
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"question":    map[string]any{"type": "string"},
						"header":      map[string]any{"type": "string"},
						"multiSelect": map[string]any{"type": "boolean", "default": false},
						"options": map[string]any{
							"type": "array", "minItems": 2, "maxItems": 4,
							"items": map[string]any{
								"type": "object",
								"properties": map[string]any{
									"label":       map[string]any{"type": "string"},
									"description": map[string]any{"type": "string"},
									"preview":     map[string]any{"type": "string"},
								},
								"required": []string{"label", "description"},
							},
						},
					},
					"required": []string{"question", "header", "options", "multiSelect"},
				},
			},
		}, []string{"questions"}),
	}

	// Duplicate tools to reach ~50 (realistic for Claude Code + MCP)
	baseTool := tools[4] // Grep-like with many params
	for i := len(tools); i < 50; i++ {
		t := map[string]any{}
		b, _ := json.Marshal(baseTool)
		json.Unmarshal(b, &t)
		t["name"] = fmt.Sprintf("Tool_%d", i)
		tools = append(tools, t)
	}

	// Build messages
	messages := make([]any, 0, numMessages)
	for i := 0; i < numMessages; i++ {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		// Mix of short and long messages
		content := fmt.Sprintf("Message %d: ", i)
		if i%5 == 0 {
			content += strings.Repeat("This is a longer message with code examples and explanations. ", 50)
		} else if i%3 == 0 {
			content += `func processData(items []Item) error {
				for _, item := range items {
					if err := item.Validate(); err != nil {
						return fmt.Errorf("validation failed for %s: %w", item.ID, err)
					}
				}
				return nil
			}`
		} else {
			content += "Short response, acknowledged."
		}
		messages = append(messages, map[string]any{"role": role, "content": content})
	}

	return map[string]any{
		"model":      "claude-sonnet-4-20250514",
		"max_tokens": 16000,
		"stream":     true,
		"system": []any{
			map[string]any{"type": "text", "text": systemText},
		},
		"tools":    tools,
		"messages": messages,
	}
}

// TestTokenizerRealisticScale tests with realistic Claude Code request sizes.
func TestTokenizerRealisticScale(t *testing.T) {
	tok, err := tokenizer.New()
	if err != nil {
		t.Fatalf("tokenizer init: %v", err)
	}

	for _, numMsgs := range []int{10, 30, 64, 100} {
		t.Run(fmt.Sprintf("%d_messages", numMsgs), func(t *testing.T) {
			req := buildRealisticRequest(numMsgs)

			systemJSON, _ := json.Marshal(req["system"])
			toolsJSON, _ := json.Marshal(req["tools"])
			messagesJSON, _ := json.Marshal(req["messages"])
			fullJSON, _ := json.Marshal(req)

			parts := []struct {
				name string
				data []byte
			}{
				{"system", systemJSON},
				{"tools", toolsJSON},
				{"messages", messagesJSON},
				{"full_body", fullJSON},
			}

			totalLib := 0
			for _, p := range parts[:3] {
				libTokens := tok.Tokens(string(p.data))
				heuristicTokens := int(float64(len(p.data)) / 3.6)
				totalLib += libTokens
				t.Logf("%-12s  bytes=%7d  lib=%6d  heuristic=%6d  heur/lib=%.2f",
					p.name, len(p.data), libTokens, heuristicTokens,
					float64(heuristicTokens)/float64(libTokens))
			}

			fullLib := tok.Tokens(string(fullJSON))
			fullHeur := int(float64(len(fullJSON)) / 3.6)
			overhead := fullLib - tok.Tokens(string(messagesJSON))
			t.Logf("%-12s  bytes=%7d  lib=%6d  heuristic=%6d  heur/lib=%.2f",
				"full_body", len(fullJSON), fullLib, fullHeur, float64(fullHeur)/float64(fullLib))
			t.Logf("OVERHEAD (lib): %d tokens  |  sum-of-parts: %d  full: %d",
				overhead, totalLib, fullLib)
		})
	}
}

// TestTokenizerPerformance benchmarks tokenizer init and counting speed.
func TestTokenizerPerformance(t *testing.T) {
	// Init time
	start := time.Now()
	tok, err := tokenizer.New()
	initTime := time.Since(start)
	if err != nil {
		t.Fatalf("tokenizer init: %v", err)
	}
	t.Logf("Init time: %v", initTime)

	req := buildRealisticRequest(64)
	fullJSON, _ := json.Marshal(req)
	text := string(fullJSON)
	t.Logf("Request size: %d bytes", len(fullJSON))

	// Count time (single)
	start = time.Now()
	tokens := tok.Tokens(text)
	countTime := time.Since(start)
	t.Logf("Count time (full body): %v → %d tokens", countTime, tokens)

	// Count time (just messages)
	messagesJSON, _ := json.Marshal(req["messages"])
	start = time.Now()
	msgTokens := tok.Tokens(string(messagesJSON))
	msgTime := time.Since(start)
	t.Logf("Count time (messages only): %v → %d tokens", msgTime, msgTokens)

	// Count time (just tools+system = overhead)
	systemJSON, _ := json.Marshal(req["system"])
	toolsJSON, _ := json.Marshal(req["tools"])
	start = time.Now()
	sysTokens := tok.Tokens(string(systemJSON))
	toolTokens := tok.Tokens(string(toolsJSON))
	overheadTime := time.Since(start)
	t.Logf("Count time (system+tools): %v → %d tokens (sys=%d, tools=%d)",
		overheadTime, sysTokens+toolTokens, sysTokens, toolTokens)

	if initTime > 500*time.Millisecond {
		t.Logf("WARNING: Init time >500ms — must init once at startup, not per-request")
	}
	if countTime > 50*time.Millisecond {
		t.Logf("WARNING: Count time >50ms — may add latency to proxy")
	}
}

// TestTokenizerOnCapturedRequest tests with a real captured proxy request if available.
func TestTokenizerOnCapturedRequest(t *testing.T) {
	data, err := os.ReadFile("testdata/sample_request.json")
	if err != nil {
		t.Skip("no testdata/sample_request.json — skipping real request test")
	}

	tok, err := tokenizer.New()
	if err != nil {
		t.Fatalf("tokenizer init: %v", err)
	}

	var req map[string]any
	if err := json.Unmarshal(data, &req); err != nil {
		t.Fatalf("parse request: %v", err)
	}

	systemJSON, _ := json.Marshal(req["system"])
	toolsJSON, _ := json.Marshal(req["tools"])
	messagesJSON, _ := json.Marshal(req["messages"])

	t.Logf("system:   %6d bytes → %5d tokens", len(systemJSON), tok.Tokens(string(systemJSON)))
	t.Logf("tools:    %6d bytes → %5d tokens", len(toolsJSON), tok.Tokens(string(toolsJSON)))
	t.Logf("messages: %6d bytes → %5d tokens", len(messagesJSON), tok.Tokens(string(messagesJSON)))
	t.Logf("TOTAL:    %6d bytes → %5d tokens", len(data), tok.Tokens(string(data)))
}
