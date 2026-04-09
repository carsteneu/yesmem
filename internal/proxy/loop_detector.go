package proxy

import (
	"fmt"
	"hash/fnv"
	"sort"
	"strings"
)

// LoopType identifies which loop pattern was detected.
type LoopType int

const (
	LoopIdenticalCycle LoopType = iota + 1
	LoopEditTestError
	LoopRepeatedError
)

// LoopSignal describes a detected loop.
type LoopSignal struct {
	Type        LoopType
	CycleLen    int
	Repetitions int
	Description string
	Tools       []string
	FilePath    string
}

// ToolCallEntry represents a single tool invocation extracted from the message history.
type ToolCallEntry struct {
	Tool     string
	ArgHash  uint64
	IsError  bool
	FilePath string
	Command  string
	ErrorMsg string
}

const (
	loopWindowSize          = 12
	minIdenticalRepetitions = 2
	minEditTestCycles       = 3
	minRepeatedErrors       = 3
)

// DetectLoop checks the message history for real-time loop patterns.
// Returns the first detected signal, or nil if no loop is found.
func DetectLoop(messages []any) *LoopSignal {
	calls := extractRecentToolCalls(messages, loopWindowSize)
	if len(calls) < 2 {
		return nil
	}

	if sig := detectIdenticalCycle(calls); sig != nil {
		return sig
	}
	if sig := detectEditTestErrorCycle(calls); sig != nil {
		return sig
	}
	if sig := detectRepeatedError(calls); sig != nil {
		return sig
	}
	return nil
}

// extractRecentToolCalls scans the tail of the message array and returns the last maxN tool calls.
func extractRecentToolCalls(messages []any, maxN int) []ToolCallEntry {
	var calls []ToolCallEntry

	for i := 0; i < len(messages); i++ {
		msg, ok := messages[i].(map[string]any)
		if !ok {
			continue
		}

		content, ok := msg["content"].([]any)
		if !ok {
			continue
		}

		for _, block := range content {
			b, ok := block.(map[string]any)
			if !ok {
				continue
			}
			if b["type"] != "tool_use" {
				continue
			}

			name, _ := b["name"].(string)
			input, _ := b["input"].(map[string]any)

			entry := ToolCallEntry{
				Tool:    name,
				ArgHash: hashToolCall(name, input),
			}

			switch name {
			case "Edit", "Write":
				entry.FilePath, _ = input["file_path"].(string)
			case "Bash":
				cmd, _ := input["command"].(string)
				if len(cmd) > 120 {
					cmd = cmd[:120]
				}
				entry.Command = cmd
			}

			// Look ahead for the matching tool_result
			for j := i + 1; j < len(messages) && j <= i+2; j++ {
				next, ok := messages[j].(map[string]any)
				if !ok {
					continue
				}
				if hasErrorInToolResult(next) {
					entry.IsError = true
					entry.ErrorMsg = extractErrorText(next, 100)
					break
				}
				if isToolResultMsg(next) {
					break
				}
			}

			calls = append(calls, entry)
		}
	}

	if len(calls) > maxN {
		calls = calls[len(calls)-maxN:]
	}
	return calls
}

// hashToolCall produces an FNV-64a hash over the tool name and its structurally relevant arguments.
func hashToolCall(name string, input map[string]any) uint64 {
	h := fnv.New64a()
	h.Write([]byte(name))
	h.Write([]byte{0})

	switch name {
	case "Edit", "Write", "Read":
		fp, _ := input["file_path"].(string)
		h.Write([]byte(fp))
	case "Bash":
		cmd, _ := input["command"].(string)
		cmd = strings.TrimSpace(cmd)
		if len(cmd) > 200 {
			cmd = cmd[:200]
		}
		h.Write([]byte(cmd))
	case "Grep", "Glob":
		pattern, _ := input["pattern"].(string)
		h.Write([]byte(pattern))
	default:
		// Sort keys for deterministic hashing (Go map iteration is random)
		keys := make([]string, 0, len(input))
		for k := range input {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if s, ok := input[k].(string); ok {
				h.Write([]byte(s))
				h.Write([]byte{0})
			}
		}
	}

	return h.Sum64()
}

// extractErrorText returns up to maxLen chars from the error content of a tool_result message.
func extractErrorText(msg map[string]any, maxLen int) string {
	content, ok := msg["content"].([]any)
	if !ok {
		return ""
	}
	for _, block := range content {
		b, ok := block.(map[string]any)
		if !ok {
			continue
		}
		if b["type"] == "tool_result" && b["is_error"] == true {
			text, _ := b["content"].(string)
			if len(text) > maxLen {
				text = text[:maxLen]
			}
			return text
		}
	}
	return ""
}

// shortLoopPath returns the last 2 path segments of a file path.
func shortLoopPath(fp string) string {
	parts := strings.Split(fp, "/")
	if len(parts) <= 2 {
		return fp
	}
	return strings.Join(parts[len(parts)-2:], "/")
}

// isBuildTool reports whether a tool call is a build/test command.
func isBuildTool(c ToolCallEntry) bool {
	return c.Tool == "Bash" && isBuildCommand(c.Command)
}

// --- Detection signals ---

func detectIdenticalCycle(calls []ToolCallEntry) *LoopSignal {
	n := len(calls)
	// Try cycle lengths from 2 to 4
	for cycleLen := 2; cycleLen <= 4 && cycleLen*minIdenticalRepetitions <= n; cycleLen++ {
		reps := countCycleRepetitions(calls, cycleLen)
		if reps >= minIdenticalRepetitions {
			tools := make([]string, cycleLen)
			for i := 0; i < cycleLen; i++ {
				tools[i] = calls[n-cycleLen*reps+i].Tool
			}
			return &LoopSignal{
				Type:        LoopIdenticalCycle,
				CycleLen:    cycleLen,
				Repetitions: reps,
				Description: fmt.Sprintf("identical %d-step cycle repeated %d times", cycleLen, reps),
				Tools:       tools,
			}
		}
	}
	return nil
}

// countCycleRepetitions counts how many times the last cycleLen calls repeat backwards.
func countCycleRepetitions(calls []ToolCallEntry, cycleLen int) int {
	n := len(calls)
	if n < cycleLen*2 {
		return 0
	}

	// The candidate cycle is the last cycleLen entries
	cycle := calls[n-cycleLen:]
	reps := 1

	for start := n - cycleLen*2; start >= 0; start -= cycleLen {
		if start+cycleLen > n-cycleLen {
			break
		}
		window := calls[start : start+cycleLen]
		match := true
		for i := 0; i < cycleLen; i++ {
			if window[i].ArgHash != cycle[i].ArgHash {
				match = false
				break
			}
		}
		if match {
			reps++
		} else {
			break
		}
	}

	return reps
}

func detectEditTestErrorCycle(calls []ToolCallEntry) *LoopSignal {
	// State machine: Edit(file) → Bash(test+error) on same file
	// The error status is on the build ToolCallEntry itself (set during extraction),
	// so we check isBuildTool AND IsError in a single transition.
	currentFile := ""
	cycles := 0
	sawEdit := false

	for _, c := range calls {
		if (c.Tool == "Edit" || c.Tool == "Write") && c.FilePath != "" {
			if currentFile == "" || c.FilePath == currentFile {
				currentFile = c.FilePath
				sawEdit = true
			} else {
				// Different file breaks pattern
				cycles = 0
				currentFile = c.FilePath
				sawEdit = true
			}
		} else if sawEdit && isBuildTool(c) {
			if c.IsError {
				cycles++
			} else {
				// Build succeeded — pattern broken
				cycles = 0
				currentFile = ""
			}
			sawEdit = false
		} else if c.Tool != "Read" && c.Tool != "Grep" && c.Tool != "Glob" {
			// Unrelated tool (not a read-only inspection) breaks pattern
			cycles = 0
			currentFile = ""
			sawEdit = false
		}
	}

	if cycles >= minEditTestCycles {
		return &LoopSignal{
			Type:        LoopEditTestError,
			CycleLen:    3,
			Repetitions: cycles,
			Description: fmt.Sprintf("Edit→Test→Error cycle on %s repeated %d times", shortLoopPath(currentFile), cycles),
			FilePath:    currentFile,
		}
	}
	return nil
}

func detectRepeatedError(calls []ToolCallEntry) *LoopSignal {
	errorCounts := make(map[string]int)
	for _, c := range calls {
		if c.IsError && c.ErrorMsg != "" {
			errorCounts[c.ErrorMsg]++
		}
	}
	for msg, count := range errorCounts {
		if count >= minRepeatedErrors {
			return &LoopSignal{
				Type:        LoopRepeatedError,
				CycleLen:    1,
				Repetitions: count,
				Description: fmt.Sprintf("same error repeated %d times: %q", count, msg),
			}
		}
	}
	return nil
}

// --- Warning formatting ---

// FormatLoopWarning generates the warning text injected into the system prompt.
// warningLevel escalates: 1 = first warning, 2 = ignored first, 3+ = persistent.
func FormatLoopWarning(signal *LoopSignal, warningLevel int) string {
	const prefix = "[YesMem Loop Detection]"
	switch {
	case warningLevel <= 1:
		return fmt.Sprintf(
			"%s %s. Step back. The same approach has failed %d times. Consider: a different strategy, reading the error output more carefully, or simplifying the approach.",
			prefix, signal.Description, signal.Repetitions,
		)
	case warningLevel == 2:
		return fmt.Sprintf(
			"%s WARNING: Loop continues despite previous warning. %s. You must change approach or ask the user for guidance.",
			prefix, signal.Description,
		)
	default:
		return fmt.Sprintf(
			"%s ALERT: persistent loop detected (warning #%d). %s. Stop and ask the user what to do next.",
			prefix, warningLevel, signal.Description,
		)
	}
}

// --- Per-thread state + cooldown ---

const loopCooldownRequests = 3

// LoopState tracks per-thread warning state.
type LoopState struct {
	WarningCount int
	cooldownLeft int
}

func (s *LoopState) InCooldown() bool { return s.cooldownLeft > 0 }

func (s *LoopState) Tick() {
	if s.cooldownLeft > 0 {
		s.cooldownLeft--
	}
}

func (s *LoopState) RecordWarning() {
	s.WarningCount++
	s.cooldownLeft = loopCooldownRequests
}

// CheckLoopAndFormat runs detection and returns a formatted warning if a loop is found.
// Returns ("", 0) if no loop or in cooldown. Mutates state.
func CheckLoopAndFormat(messages []any, state *LoopState) (warning string, level int) {
	state.Tick()
	if state.InCooldown() {
		return "", 0
	}
	signal := DetectLoop(messages)
	if signal == nil {
		return "", 0
	}
	state.RecordWarning()
	level = state.WarningCount
	warning = FormatLoopWarning(signal, level)
	return warning, level
}
