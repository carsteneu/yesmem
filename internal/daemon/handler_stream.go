package daemon

import (
	"sync"
	"time"
)

// StreamState tracks a single SSE stream in memory.
type StreamState struct {
	Active     bool
	BytesSoFar int64
	StartedAt  time.Time
	IsSubagent bool
	mu         sync.RWMutex
}

// streamStates is the in-memory map of threadID → StreamState.
// No DB writes — ephemeral state that resets on daemon restart.
var (
	streamStates   = make(map[string]*StreamState)
	streamStatesMu sync.RWMutex

	// sessionToThread maps agent session_id → threadID for get_agent enrichment.
	sessionToThread   = make(map[string]string)
	sessionToThreadMu sync.RWMutex

	// parentSubagentCounts tracks active subagent_streams per parent session_id.
	parentSubagentCounts   = make(map[string]int)
	parentSubagentCountsMu sync.RWMutex
)

// handleTrackStreamState is called by the proxy at SSE stream boundaries.
// Internal RPC, not exposed as MCP tool.
func (h *Handler) handleTrackStreamState(params map[string]any) Response {
	threadID, _ := params["thread_id"].(string)
	if threadID == "" {
		return errorResponse("thread_id required")
	}

	streamActive := false
	if v, ok := params["stream_active"].(bool); ok {
		streamActive = v
	}

	bytesSoFar := int64(0)
	if v, ok := params["bytes_so_far"].(float64); ok {
		bytesSoFar = int64(v)
	}

	isSubagent := false
	if v, ok := params["is_subagent"].(bool); ok {
		isSubagent = v
	}

	streamStatesMu.Lock()
	state, exists := streamStates[threadID]
	if !exists {
		state = &StreamState{}
		streamStates[threadID] = state
	}
	streamStatesMu.Unlock()

	state.mu.Lock()
	if streamActive && !state.Active {
		state.StartedAt = time.Now()
		state.IsSubagent = isSubagent
	}
	state.Active = streamActive
	if bytesSoFar > 0 {
		state.BytesSoFar = bytesSoFar
	}
	state.mu.Unlock()

	// Subagent counting: update parent's subagent_streams counter.
	if isSubagent {
		h.updateParentStreamCount(threadID, streamActive)
	}

	// Update session→thread mapping for get_agent enrichment.
	// The proxy passes project for lazy mapping.
	project, _ := params["project"].(string)
	if project != "" {
		h.mapSessionToThread(threadID, project)
	}

	return jsonResponse(map[string]any{"status": "ok", "thread_id": threadID})
}

// mapSessionToThread links an agent's session_id to the proxy threadID
// so get_agent can look up stream state.
func (h *Handler) mapSessionToThread(threadID, project string) {
	// Try to find the agent by its session matching this threadID.
	// For opencode: threadID = "opencode:ses_xxx", agent.session_id = UUID.
	// The optimistic match works when session_id == threadID (Claude Code).
	agent, err := h.store.AgentGetAnyBySession(threadID)
	if err == nil && agent != nil {
		sessionToThreadMu.Lock()
		sessionToThread[agent.SessionID] = threadID
		sessionToThreadMu.Unlock()
		return
	}

	// Fallback: try to find a running agent in this project whose
	// session hasn't been mapped yet. This handles opencode where
	// threadID differs from session_id.
	agents, err := h.store.AgentList(project)
	if err != nil {
		return
	}
	sessionToThreadMu.Lock()
	defer sessionToThreadMu.Unlock()
	for _, a := range agents {
		if a.Status == "running" || a.Status == "pending" || a.Status == "spawning" {
			if _, alreadyMapped := sessionToThread[a.SessionID]; !alreadyMapped {
				sessionToThread[a.SessionID] = threadID
				return
			}
		}
	}
}

// updateParentStreamCount adjusts the parent agent's subagent_streams count
// when a subagent stream starts or ends.
func (h *Handler) updateParentStreamCount(threadID string, active bool) {
	// Find this thread's agent to get its caller_session (the parent).
	agent, err := h.store.AgentGetAnyBySession(threadID)
	if err != nil || agent == nil {
		return
	}
	if agent.CallerSession == "" {
		return
	}
	parentSubagentCountsMu.Lock()
	defer parentSubagentCountsMu.Unlock()
	if active {
		parentSubagentCounts[agent.CallerSession]++
	} else {
		parentSubagentCounts[agent.CallerSession]--
		if parentSubagentCounts[agent.CallerSession] <= 0 {
			delete(parentSubagentCounts, agent.CallerSession)
		}
	}
}

// getStreamFields returns stream state fields for an agent response.
func (h *Handler) getStreamFields(sessionID string) map[string]any {
	fields := map[string]any{
		"stream_active":    false,
		"stream_bytes":     int64(0),
		"stream_started":   "",
		"subagent_streams": 0,
	}

	// Always populate subagent count — independent of StreamState.
	parentSubagentCountsMu.RLock()
	subCount := parentSubagentCounts[sessionID]
	parentSubagentCountsMu.RUnlock()
	fields["subagent_streams"] = subCount

	sessionToThreadMu.RLock()
	threadID, ok := sessionToThread[sessionID]
	sessionToThreadMu.RUnlock()
	if !ok {
		return fields
	}

	streamStatesMu.RLock()
	state, exists := streamStates[threadID]
	streamStatesMu.RUnlock()
	if !exists {
		return fields
	}

	state.mu.RLock()
	defer state.mu.RUnlock()

	fields["stream_active"] = state.Active
	fields["stream_bytes"] = state.BytesSoFar
	if !state.StartedAt.IsZero() {
		fields["stream_started"] = state.StartedAt.Format(time.RFC3339)
	}

	return fields
}

// enrichAgentWithStreamFields merges stream state into an agent response map.
func (h *Handler) enrichAgentWithStreamFields(agent map[string]any) map[string]any {
	sessionID, _ := agent["session_id"].(string)
	if sessionID == "" {
		return agent
	}
	streamFields := h.getStreamFields(sessionID)
	for k, v := range streamFields {
		agent[k] = v
	}
	return agent
}

// cleanupStaleStreams removes stream states that have been inactive
// for longer than the given timeout. Called periodically.
func (h *Handler) cleanupStaleStreams(timeout time.Duration) {
	streamStatesMu.Lock()
	defer streamStatesMu.Unlock()

	cutoff := time.Now().Add(-timeout)
	for threadID, state := range streamStates {
		state.mu.RLock()
		stale := !state.Active && !state.StartedAt.IsZero() && state.StartedAt.Before(cutoff)
		state.mu.RUnlock()
		if stale {
			delete(streamStates, threadID)
		}
	}
}

// Stream state size guard — prevent unbounded memory growth.
const maxStreamStates = 10000

// ensureStreamStateCapacity removes oldest inactive entries if the map
// exceeds maxStreamStates.
func ensureStreamStateCapacity() {
	streamStatesMu.Lock()
	defer streamStatesMu.Unlock()
	if len(streamStates) < maxStreamStates {
		return
	}

	// Remove entries that have been inactive longest.
	var oldestThreadID string
	var oldestTime time.Time
	for threadID, state := range streamStates {
		state.mu.RLock()
		if !state.Active && !state.StartedAt.IsZero() {
			if oldestThreadID == "" || state.StartedAt.Before(oldestTime) {
				oldestThreadID = threadID
				oldestTime = state.StartedAt
			}
		}
		state.mu.RUnlock()
	}
	if oldestThreadID != "" {
		delete(streamStates, oldestThreadID)
	}
}

// RegisterSessionThread maps an agent's session_id to a proxy threadID.
// Called from _track_usage when a thread is matched to an agent.
func RegisterSessionThread(sessionID, threadID string) {
	sessionToThreadMu.Lock()
	sessionToThread[sessionID] = threadID
	sessionToThreadMu.Unlock()
}
