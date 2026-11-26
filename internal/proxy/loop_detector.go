package proxy

import (
	"fmt"
	"hash/fnv"
	"sort"
	"strings"
	"sync"
)

// LoopType identifies which loop pattern was detected.
type LoopType int

const (
	LoopIdenticalCycle LoopType = iota + 1
	LoopEditTestError
	LoopRepeatedError
	LoopIdleConsumer
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

// loopToolClass groups the tool names of the clients the proxy serves. Claude
// Code sends PascalCase names (Edit, Write, Bash, Read, Grep, Glob); opencode
// sends lowercase ones and names its shell tool "shell" instead of "bash".
// Detection reads the class, never the raw name, so a loop is visible in every
// dialect.
type loopToolClass int

const (
	loopToolOther loopToolClass = iota
	loopToolFileEdit
	loopToolShell
	loopToolReadOnly
)

func classifyLoopTool(name string) loopToolClass {
	switch strings.ToLower(name) {
	case "edit", "write":
		return loopToolFileEdit
	case "bash", "shell":
		return loopToolShell
	case "read", "grep", "glob":
		return loopToolReadOnly
	}
	return loopToolOther
}

func loopToolCommand(input map[string]any) string {
	cmd, _ := input["command"].(string)
	return cmd
}

const (
	loopWindowSize          = 12
	minIdenticalRepetitions = 2
	minEditTestCycles       = 3
	minRepeatedErrors       = 3
	minIdleCycles           = 3 // consecutive assistant turns without productive tools
	// loopWarningResetAfter is how many consecutive signal-free checks clear the
	// warning count. One is not enough: a loop whose pattern sits on the window
	// boundary flickers (signal, no signal, signal) and would never escalate.
	loopWarningResetAfter = 2
)

// productiveTools are tools that produce new output / create artifacts.
// Tools not in this set are considered consumptive (read-only / query / status).
//
// Names must be lower-case: the proxy sees the raw tool_use names exactly as the
// client sends them, and those are lower-case ("write", "edit", "execute"). The
// original fork list used "Write"/"Edit" and therefore never matched anything —
// every turn looked consumptive and the idle loop fired on normal work.
// "execute" is code mode: one call runs arbitrary code, so it counts as productive.
var productiveTools = map[string]bool{
	"write":                   true,
	"edit":                    true,
	"multiedit":               true,
	"patch":                   true,
	"execute":                 true,
	"task":                    true,
	"webfetch":                true,
	"yesmem_remember":         true,
	"yesmem_scratchpad_write": true,
	"yesmem_spawn_agent":      true,
	"yesmem_save_cap":         true,
	"yesmem_register_caps":    true,
	"yesmem_cap_store":        true,
	"yesmem_execute_cap":      true,
	"yesmem_broadcast":        true,
	"yesmem_send_to":          true,
	"yesmem_relay_agent":      true,
	"yesmem_set_plan":         true,
	"yesmem_set_config":       true,
	"yesmem_pin":              true,
	"yesmem_ingest_docs":      true,
	"yesmem_schedule":         true,
}

// DetectLoop checks the message history for real-time loop patterns.
// Returns the first detected signal, or nil if no loop is found.
func DetectLoop(messages []any) *LoopSignal {
	calls := extractRecentToolCalls(messages, loopWindowSize)

	if len(calls) >= 2 {
		if sig := detectIdenticalCycle(calls); sig != nil {
			return sig
		}
		if sig := detectEditTestErrorCycle(calls); sig != nil {
			return sig
		}
		if sig := detectRepeatedError(calls); sig != nil {
			return sig
		}
	}

	// Idle consumer loop (no productive tools across turns) is checked LAST, as a
	// fallback: the specific signals above are more informative, and probing this
	// one first masked them (identical-cycle and repeated-error fixtures were
	// reported as consumer-only loops). It works on raw messages, so it does not
	// depend on the tool-call window being long enough.
	if sig := detectIdleConsumer(messages); sig != nil {
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

			switch classifyLoopTool(name) {
			case loopToolFileEdit:
				entry.FilePath = toolFilePath(input)
			case loopToolShell:
				cmd := loopToolCommand(input)
				if len(cmd) > 120 {
					cmd = cmd[:120]
				}
				entry.Command = cmd
			}

			// Look ahead for the matching tool_result. A message carries the
			// results of every tool call of the previous assistant turn, so the
			// result has to be matched by tool_use_id: reading the message would
			// attribute the failure of one parallel call to all of them.
			callID, _ := b["id"].(string)
			for j := i + 1; j < len(messages) && j <= i+2; j++ {
				next, ok := messages[j].(map[string]any)
				if !ok {
					continue
				}
				if text, found := errorTextForToolUse(next, callID); found {
					entry.IsError = true
					entry.ErrorMsg = text
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

	switch strings.ToLower(name) {
	case "edit", "write", "read":
		// The file is the identity of the step, not the payload: a loop that
		// edits the same file with changing strings is still the same step.
		h.Write([]byte(toolFilePath(input)))
	case "bash", "shell":
		cmd := strings.TrimSpace(loopToolCommand(input))
		if len(cmd) > 200 {
			cmd = cmd[:200]
		}
		h.Write([]byte(cmd))
		// opencode's shell carries a workdir; the same command in another
		// directory is another step, not a repetition of this one.
		if wd, _ := input["workdir"].(string); wd != "" {
			h.Write([]byte{0})
			h.Write([]byte(wd))
		}
	case "grep", "glob":
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

// errorTextForToolUse returns the failure text of the tool_result block that
// answers toolUseID, bounded to 100 chars.
//
// A message that carries exactly ONE result needs no id: there is nothing to
// confuse, so the failure belongs to this call. Several results in one message
// are the output of parallel calls, and there the tool_use_id decides — reading
// the message instead attributed one failed call to its siblings.
func errorTextForToolUse(msg map[string]any, toolUseID string) (string, bool) {
	content, ok := msg["content"].([]any)
	if !ok {
		return "", false
	}
	var results []map[string]any
	for _, block := range content {
		b, ok := block.(map[string]any)
		if !ok || b["type"] != "tool_result" {
			continue
		}
		results = append(results, b)
	}
	if len(results) == 1 {
		if !toolResultIsError(results[0]) {
			return "", false
		}
		return boundedResultText(results[0]), true
	}
	for _, b := range results {
		if !toolResultIsError(b) {
			continue
		}
		if toolUseID != "" {
			if id, _ := b["tool_use_id"].(string); id != toolUseID {
				continue
			}
		}
		return boundedResultText(b), true
	}
	return "", false
}

func boundedResultText(block map[string]any) string {
	text, _ := block["content"].(string)
	if len(text) > 100 {
		text = text[:100]
	}
	return text
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

// maxLoopPathRunes bounds the file path shown in a loop warning.
const maxLoopPathRunes = 120

// shortLoopPath returns the last 2 path segments of a file path. The path comes
// from a tool input and is rendered into an injected reminder line, so control
// characters are flattened and the result is bounded: a newline inside it would
// forge a line of its own next to the metadata blocks.
func shortLoopPath(fp string) string {
	fp = strings.Map(func(r rune) rune {
		if isControlRune(r) {
			return ' '
		}
		return r
	}, fp)

	parts := strings.Split(fp, "/")
	short := fp
	if len(parts) > 2 {
		short = strings.Join(parts[len(parts)-2:], "/")
	}
	if runes := []rune(short); len(runes) > maxLoopPathRunes {
		return string(runes[:maxLoopPathRunes]) + "…"
	}
	return short
}

// isBuildTool reports whether a tool call is a build/test command.
func isBuildTool(c ToolCallEntry) bool {
	return classifyLoopTool(c.Tool) == loopToolShell && isBuildCommand(c.Command)
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
		if classifyLoopTool(c.Tool) == loopToolFileEdit && c.FilePath != "" {
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
		} else if classifyLoopTool(c.Tool) != loopToolReadOnly {
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

// detectIdleConsumer scans the message history for consecutive assistant turns
// that contain no productive tool calls (read-only / status-only responses).
// This detects the "idle consumer loop": agent responds to watchdog pokes with
// hybrid_search + status text but never creates, writes, or spawns anything.
func detectIdleConsumer(messages []any) *LoopSignal {
	idleTurns := 0
	for i := len(messages) - 1; i >= 0; i-- {
		msg, ok := messages[i].(map[string]any)
		if !ok {
			continue
		}
		role, _ := msg["role"].(string)

		if role == "assistant" {
			if !hasProductiveTool(msg) {
				idleTurns++
				if idleTurns >= minIdleCycles {
					return &LoopSignal{
						Type:        LoopIdleConsumer,
						CycleLen:    1,
						Repetitions: idleTurns,
						Description: fmt.Sprintf("consumer-only loop: %d consecutive responses without productive action", idleTurns),
					}
				}
			} else {
				// A productive turn breaks the idle cycle
				return nil
			}
		}
		// Skip user messages (tool results, watchdog pokes)
	}
	return nil
}

// hasProductiveTool checks whether an assistant message contains at least one
// tool_use block whose tool name is in the productiveTools set.
func hasProductiveTool(msg map[string]any) bool {
	content, ok := msg["content"].([]any)
	if !ok {
		return false
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
		if name == "" {
			continue
		}
		lower := strings.ToLower(name)
		if productiveTools[lower] {
			return true
		}
		// Shell is neither inherently productive nor consumptive: `git commit`
		// creates something, `git log` does not. Judge by the command.
		if lower == "shell" || lower == "bash" {
			if input, ok := b["input"].(map[string]any); ok {
				if cmd, _ := input["command"].(string); shellCommandIsProductive(cmd) {
					return true
				}
			}
		}
	}
	return false
}

// mutatingShellCommands are the leading words of shell invocations that create or
// change state. Everything else (git log, ls, grep, cat, systemctl status) is a
// lookup and stays consumptive — that is what the idle detector is meant to catch.
var mutatingShellCommands = []string{
	"git add", "git commit", "git push", "git merge", "git rebase", "git tag",
	"git checkout -b", "git stash pop", "git apply", "git cherry-pick",
	"make", "go build", "go test", "go install", "go generate",
	"cp ", "mv ", "rm ", "mkdir", "touch", "tee ", "chmod", "chown",
	"systemctl --user restart", "systemctl --user start", "systemctl --user stop",
	"npm install", "npm run", "pip install", "pip3 install",
	"sqlite3 " , "python3 -", "bash -c", "sh -c",
}

// shellCommandIsProductive reports whether a shell command line mutates state.
func shellCommandIsProductive(cmd string) bool {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return false
	}
	lower := strings.ToLower(cmd)
	// Output redirection creates or overwrites a file, whatever the command is.
	if strings.Contains(lower, " > ") || strings.Contains(lower, ">>") {
		return true
	}
	for _, prefix := range mutatingShellCommands {
		if strings.Contains(lower, prefix) {
			// "git commit --dry-run" or "git log --grep=commit" would slip through
			// Contains, so require the command to actually start a segment with it.
			for _, segment := range splitShellSegments(lower) {
				if strings.HasPrefix(segment, prefix) {
					return true
				}
			}
		}
	}
	return false
}

// splitShellSegments splits a command line into its individual commands.
func splitShellSegments(cmd string) []string {
	replacer := strings.NewReplacer("&&", "\n", "||", "\n", ";", "\n", "|", "\n")
	parts := strings.Split(replacer.Replace(cmd), "\n")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		out = append(out, strings.TrimSpace(p))
	}
	return out
}

// --- Warning formatting ---

// FormatLoopWarning generates the warning text injected into the system prompt.
// warningLevel escalates: 1 = first warning, 2 = ignored first, 3+ = persistent.
func FormatLoopWarning(signal *LoopSignal, warningLevel int) string {
	const prefix = "[YesMem Loop Detection]"

	// Idle consumer loop gets its own escalation: direct, urgent, blocks on level 3
	if signal.Type == LoopIdleConsumer {
		switch {
		case warningLevel == 1:
			return fmt.Sprintf(
				"%s WARNING: %s. You are responding to pokes with status updates only — no write, edit, spawn, webfetch, or remember calls. This is a consumer loop. Your NEXT response MUST contain at least one productive tool call (Write, Edit, spawn_agent, webfetch, task, yesmem_remember, or yesmem_scratchpad_write).",
				prefix, signal.Description,
			)
		case warningLevel == 2:
			return fmt.Sprintf(
				"%s ALERT: consumer loop persists despite warning #1. %s. WAKE UP. You have been responding without creating anything for %d consecutive turns. This is the second warning. Your next response MUST create something new or the proxy will escalate.",
				prefix, signal.Description, signal.Repetitions,
			)
		default:
			return fmt.Sprintf(
				"%s FORCED MODE: consumer loop persists (warning #%d). %s. You are now in READ-ONLY RESTRICTION. Your responses are blocked until you produce a response with at least one productive tool call (Write, Edit, spawn_agent, webfetch, task, remember, scratchpad_write). DO NOT respond with status text alone.",
				prefix, warningLevel, signal.Description,
			)
		}
	}

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
	// mu serializes the state transitions for this thread only. The loopMu on
	// the server protects the map, not the counter: detection scans the message
	// history, and holding a server-wide lock across that scan would serialize
	// every thread behind the longest one.
	mu           sync.Mutex
	WarningCount int
	cooldownLeft int
	// emptyRuns counts consecutive signal-free checks; it gates the reset so a
	// flickering signal does not restart the escalation ladder.
	emptyRuns int
}

func (s *LoopState) InCooldown() bool { return s.cooldownLeft > 0 }

// escalated reports whether dropping this state would lose a pending warning.
// The fields are written under mu by the detector, so a prune on another
// goroutine reads them under the same lock.
func (s *LoopState) escalated() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.WarningCount > 0 || s.cooldownLeft > 0
}

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
	state.mu.Lock()
	defer state.mu.Unlock()

	state.Tick()
	if state.InCooldown() {
		return "", 0
	}
	signal := DetectLoop(messages)
	if signal == nil {
		state.emptyRuns++
		if state.emptyRuns >= loopWarningResetAfter {
			// The loop is over: the next one is a new loop and starts at level 1.
			state.WarningCount = 0
		}
		return "", 0
	}
	state.emptyRuns = 0
	state.RecordWarning()
	level = state.WarningCount
	warning = FormatLoopWarning(signal, level)
	return warning, level
}
