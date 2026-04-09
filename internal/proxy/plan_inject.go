package proxy

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
)

const planCheckpointInterval = 10000 // tokens between plan checkpoints

// planCheckpoint tracks plan-related state per thread.
type planCheckpoint struct {
	mu             sync.Mutex
	lastTokenCount map[string]int // threadID → totalTokens at last checkpoint/update
}

var planCp = &planCheckpoint{
	lastTokenCount: make(map[string]int),
}

// planCheckpointInject checks if a plan checkpoint should be injected.
// Returns the checkpoint hint, or "" if not yet time or no active plan.
func (s *Server) planCheckpointInject(threadID string, totalTokens int) string {
	// Check if there's an active plan via daemon
	plan, docsHint := s.getActivePlan(threadID)
	if plan == "" {
		return ""
	}

	planCp.mu.Lock()
	lastTokens := planCp.lastTokenCount[threadID]
	if lastTokens == 0 {
		// First check — set baseline
		planCp.lastTokenCount[threadID] = totalTokens
		planCp.mu.Unlock()
		return ""
	}
	if totalTokens-lastTokens < planCheckpointInterval {
		planCp.mu.Unlock()
		return ""
	}
	planCp.lastTokenCount[threadID] = totalTokens
	planCp.mu.Unlock()

	return formatPlanCheckpoint(plan, docsHint)
}

// planCheckpointReset resets the plan checkpoint counter (called after set_plan/update_plan).
func planCheckpointReset(threadID string, totalTokens int) {
	planCp.mu.Lock()
	planCp.lastTokenCount[threadID] = totalTokens
	planCp.mu.Unlock()
}

// getActivePlan fetches the active plan and docs hint from daemon.
func (s *Server) getActivePlan(threadID string) (string, string) {
	result, err := s.queryDaemon("get_plan", map[string]any{"thread_id": threadID})
	if err != nil {
		return "", ""
	}
	var resp struct {
		Exists   bool   `json:"exists"`
		Plan     string `json:"plan"`
		Status   string `json:"status"`
		DocsHint string `json:"docs_hint"`
	}
	if json.Unmarshal(result, &resp) != nil || !resp.Exists || resp.Status != "active" {
		return "", ""
	}
	return resp.Plan, resp.DocsHint
}

func formatPlanCheckpoint(plan, docsHint string) string {
	checkpoint := fmt.Sprintf("[Plan Checkpoint] You have an active plan. Please update it with update_plan() — mark completed items and set current status. If everything is done, call complete_plan().\n\n[Active Plan]\n%s\n[/Active Plan]", plan)
	if docsHint != "" {
		checkpoint += "\n\n" + docsHint
	}
	checkpoint += "\n[/Plan Checkpoint]"
	return checkpoint
}

// detectPlanFileRead scans the last 4 messages for a Read tool_use on a file
// whose path contains "plan" and ends with ".md". Returns the file path or "".
func detectPlanFileRead(messages []any) string {
	start := len(messages) - 4
	if start < 0 {
		start = 0
	}
	for i := start; i < len(messages); i++ {
		msg, ok := messages[i].(map[string]any)
		if !ok || msg["role"] != "assistant" {
			continue
		}
		blocks, ok := msg["content"].([]any)
		if !ok {
			continue
		}
		for _, b := range blocks {
			block, ok := b.(map[string]any)
			if !ok {
				continue
			}
			if block["type"] != "tool_use" || block["name"] != "Read" {
				continue
			}
			input, ok := block["input"].(map[string]any)
			if !ok {
				continue
			}
			fp, _ := input["file_path"].(string)
			if fp != "" && strings.Contains(strings.ToLower(fp), "plan") && strings.HasSuffix(fp, ".md") {
				return fp
			}
		}
	}
	return ""
}

// shouldNudgePlan returns true if we should inject a system-reminder nudging
// Claude to call set_plan(). Conditions: a plan file was read AND no plan is active.
func shouldNudgePlan(planFile, activePlan string) bool {
	return planFile != "" && activePlan == ""
}

// detectPlanToolCall checks if the current request contains a set_plan or update_plan tool response.
// If so, resets the checkpoint counter. Called during request processing.
func (s *Server) detectPlanToolCall(messages []any, threadID string, totalTokens int) {
	if len(messages) == 0 {
		return
	}
	// Check last few messages for plan tool calls
	start := len(messages) - 4
	if start < 0 {
		start = 0
	}
	for _, msg := range messages[start:] {
		m, ok := msg.(map[string]any)
		if !ok {
			continue
		}
		role, _ := m["role"].(string)
		if role != "assistant" {
			continue
		}
		content, ok := m["content"].([]any)
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
			if name == "mcp__yesmem__set_plan" || name == "mcp__yesmem__update_plan" || name == "mcp__yesmem__complete_plan" {
				planCheckpointReset(threadID, totalTokens)
				if name == "mcp__yesmem__complete_plan" {
					// Clear the counter entirely
					planCp.mu.Lock()
					delete(planCp.lastTokenCount, threadID)
					planCp.mu.Unlock()
				}
				return
			}
		}
	}
}
