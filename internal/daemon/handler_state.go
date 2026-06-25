package daemon

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/carsteneu/yesmem/internal/briefing"
	"github.com/carsteneu/yesmem/internal/config"
	"github.com/carsteneu/yesmem/internal/models"
	"github.com/carsteneu/yesmem/internal/storage"
)

func (h *Handler) handleGetCompactedStubs(params map[string]any) Response {
	threadID, _ := params["thread_id"].(string)
	// Fallback to session_id for backwards compat
	if threadID == "" {
		threadID, _ = params["session_id"].(string)
	}
	if threadID == "" {
		return errorResponse("thread_id required")
	}

	// Optional: filter by range
	fromIdx := 0
	toIdx := 0
	if f, ok := params["from_idx"].(float64); ok {
		fromIdx = int(f)
	}
	if t, ok := params["to_idx"].(float64); ok {
		toIdx = int(t)
	}

	// Read frozen stubs from proxy_state (runtime.db), not the dead compacted_blocks table.
	// Frozen stubs are persisted by sawtooth via SetProxyState with key "frozen:<threadID>".
	frozenJSON, err := h.store.GetProxyState("frozen:" + threadID)
	if err != nil {
		return errorResponse(fmt.Sprintf("get frozen stubs: %v", err))
	}
	if frozenJSON == "" {
		// No frozen stubs for this thread — try compacted_blocks as fallback (legacy path)
		var blocks []*storage.CompactedBlock
		if fromIdx > 0 || toIdx > 0 {
			if toIdx == 0 {
				toIdx = 999999
			}
			blocks, err = h.store.GetCompactedBlocksInRange(threadID, fromIdx, toIdx)
		} else {
			blocks, err = h.store.GetCompactedBlocks(threadID)
		}
		if err != nil {
			return errorResponse(fmt.Sprintf("get compacted stubs: %v", err))
		}
		return jsonResponse(blocks)
	}

	// Parse frozen stubs to extract archived message ranges
	blocks, err := parseFrozenStubs(frozenJSON, threadID, fromIdx, toIdx)
	if err != nil {
		return errorResponse(fmt.Sprintf("parse frozen stubs: %v", err))
	}

	// Enrich blocks with actual message content from messages.db (real-time indexed)
	for i, b := range blocks {
		if b.EndIdx-b.StartIdx > 200 {
			continue // too many messages, keep marker as summary
		}
		msgs, err := h.store.GetMessagesBySessionRange(threadID, b.StartIdx, b.EndIdx)
		if err != nil || len(msgs) == 0 {
			continue
		}
		blocks[i].Content = formatMessageRange(msgs, threadID, b.StartIdx, b.EndIdx)
	}

	return jsonResponse(blocks)
}

// handleExpandContext allows Claude to actively expand archived/compacted context.
// Supports two modes: query-based search or explicit message range.
func (h *Handler) handleExpandContext(params map[string]any) Response {
	threadID, _ := params["thread_id"].(string)
	if threadID == "" {
		threadID, _ = params["session_id"].(string)
	}
	query, _ := params["query"].(string)
	msgRange, _ := params["message_range"].(string)

	if query == "" && msgRange == "" {
		return errorResponse("query or message_range required")
	}

	// Mode 1: Explicit message range (e.g. "200-250")
	if msgRange != "" && threadID != "" {
		var from, to int
		if _, err := fmt.Sscanf(msgRange, "%d-%d", &from, &to); err != nil {
			return errorResponse(fmt.Sprintf("invalid message_range format (expected 'from-to'): %v", err))
		}

		// Try to fetch actual message content from messages.db (real-time indexed by OpencodeScanner)
		msgs, err := h.store.GetMessagesBySessionRange(threadID, from, to)
		if err != nil {
			return errorResponse(fmt.Sprintf("expand range: %v", err))
		}
		if len(msgs) > 0 {
			content := formatMessageRange(msgs, threadID, from, to)
			blocks := []*storage.CompactedBlock{{
				ThreadID: threadID,
				StartIdx: from,
				EndIdx:   to,
				Content:  content,
			}}
			return jsonResponse(map[string]any{"message": fmt.Sprintf("%d messages in range %d-%d", len(msgs), from, to), "blocks": blocks})
		}

		// Fallback: read frozen stubs from proxy_state (sawtooth-persisted markers)
		frozenJSON, err := h.store.GetProxyState("frozen:" + threadID)
		if err != nil {
			return errorResponse(fmt.Sprintf("expand range: %v", err))
		}
		if frozenJSON != "" {
			blocks, err := parseFrozenStubs(frozenJSON, threadID, from, to)
			if err != nil {
				return errorResponse(fmt.Sprintf("parse frozen stubs: %v", err))
			}
			if len(blocks) == 0 {
				return jsonResponse(map[string]any{"message": fmt.Sprintf("No archived content in range %d-%d", from, to), "blocks": []any{}})
			}
			return jsonResponse(map[string]any{"message": fmt.Sprintf("%d blocks in range %d-%d", len(blocks), from, to), "blocks": blocks})
		}

		// Fallback: legacy compacted_blocks table
		blocks, err := h.store.GetCompactedBlocksInRange(threadID, from, to)
		if err != nil {
			return errorResponse(fmt.Sprintf("expand range: %v", err))
		}
		if len(blocks) == 0 {
			return jsonResponse(map[string]any{"message": fmt.Sprintf("No archived content in range %d-%d", from, to), "blocks": []any{}})
		}
		return jsonResponse(map[string]any{"message": fmt.Sprintf("%d blocks in range %d-%d", len(blocks), from, to), "blocks": blocks})
	}

	// Mode 2: Query-based search across archived messages
	if query != "" {
		result := h.handleDeepSearch(map[string]any{"query": query, "limit": float64(5)})
		return result
	}

	return errorResponse("unexpected state")
}

func (h *Handler) handleStoreCompactedBlock(params map[string]any) Response {
	threadID, _ := params["thread_id"].(string)
	if threadID == "" {
		return errorResponse("thread_id required")
	}
	startIdx := 0
	endIdx := 0
	if s, ok := params["start_idx"].(float64); ok {
		startIdx = int(s)
	}
	if e, ok := params["end_idx"].(float64); ok {
		endIdx = int(e)
	}
	content, _ := params["content"].(string)
	if content == "" {
		return errorResponse("content required")
	}

	if err := h.store.SaveCompactedBlock(threadID, startIdx, endIdx, content); err != nil {
		return errorResponse(fmt.Sprintf("save compacted block: %v", err))
	}
	return jsonResponse(map[string]string{"status": "ok"})
}

func (h *Handler) handleGetProxyState(params map[string]any) Response {
	key, _ := params["key"].(string)
	if key == "" {
		return errorResponse("key required")
	}
	value, err := h.store.GetProxyState(key)
	if err != nil {
		return errorResponse(fmt.Sprintf("get proxy state: %v", err))
	}
	return jsonResponse(map[string]string{"key": key, "value": value})
}

func (h *Handler) handleSetProxyState(params map[string]any) Response {
	key, _ := params["key"].(string)
	if key == "" {
		return errorResponse("key required")
	}
	value, _ := params["value"].(string)
	if err := h.store.SetProxyState(key, value); err != nil {
		return errorResponse(fmt.Sprintf("set proxy state: %v", err))
	}
	return jsonResponse(map[string]string{"status": "ok"})
}

func (h *Handler) handleDeleteProxyStatePrefix(params map[string]any) Response {
	prefix, _ := params["prefix"].(string)
	if prefix == "" {
		return errorResponse("prefix required")
	}
	n, err := h.store.DeleteProxyStatePrefix(prefix)
	if err != nil {
		return errorResponse(fmt.Sprintf("delete proxy state prefix: %v", err))
	}
	return jsonResponse(map[string]any{"deleted": n})
}

// handleSetConfig sets a config value.
// Without session_id: writes to config.yaml (persistent, by dot-path).
// With session_id: writes to proxy_state only (runtime override for that session).
// e.g. set_config("extraction.model", "opus")
//      set_config("token_threshold", "300000", "session_id": "sess-1")
func (h *Handler) handleSetConfig(params map[string]any) Response {
	key, _ := params["key"].(string)
	value, _ := params["value"].(string)
	if key == "" || value == "" {
		return errorResponse("key and value required")
	}

	// Session-specific: proxy_state runtime override (not persistent)
	if sessionID, ok := params["session_id"].(string); ok && sessionID != "" {
		stateKey := "config_override:" + key + ":" + sessionID
		if err := h.store.SetProxyState(stateKey, value); err != nil {
			return errorResponse(fmt.Sprintf("set session config: %v", err))
		}
		return jsonResponse(map[string]string{
			"status": "ok", "key": key, "value": value,
			"scope": "session:" + sessionID, "source": "proxy_state",
		})
	}

	// Global: write to config.yaml (persistent)
	cfgPath := filepath.Join(h.dataDir, "config.yaml")
	if err := config.SetValue(cfgPath, key, config.CoerceValue(value)); err != nil {
		return errorResponse(fmt.Sprintf("set config: %v", err))
	}

	// Dual-write to proxy_state for keys the proxy reads at runtime.
	// refreshConfigOverrides (internal/proxy/proxy.go) reads config_override:<key>
	// directly from proxy_state — it does NOT go through handleGetConfig.
	proxyKey := strings.TrimPrefix(key, "proxy.")
	if proxyKey != key || key == "token_threshold" {
		if err := h.store.SetProxyState("config_override:"+proxyKey, value); err != nil {
			// Non-fatal: config.yaml is the primary store
			log.Printf("set config proxy_state dual-write: %v", err)
		}
	}

	return jsonResponse(map[string]string{
		"status": "ok", "key": key, "value": value,
		"scope": "config.yaml", "source": "config.yaml",
	})
}

// handleGetConfig reads a config value.
// Priority: 1) session-specific proxy_state 2) global proxy_state 3) config.yaml
// e.g. get_config("extraction.model")
// Optional session_id: checks session-specific override first.
func (h *Handler) handleGetConfig(params map[string]any) Response {
	key, _ := params["key"].(string)
	if key == "" {
		return errorResponse("key required")
	}

	// 1. Check session-specific proxy_state override
	if sessionID, ok := params["session_id"].(string); ok && sessionID != "" {
		stateKey := "config_override:" + key + ":" + sessionID
		value, err := h.store.GetProxyState(stateKey)
		if err != nil {
			return errorResponse(fmt.Sprintf("get config: %v", err))
		}
		if value != "" {
			return jsonResponse(map[string]string{
				"key": key, "value": value, "scope": "session:" + sessionID, "source": "proxy_state",
			})
		}
	}

	// 2. Check global proxy_state override
	stateKey := "config_override:" + key
	value, err := h.store.GetProxyState(stateKey)
	if err != nil {
		return errorResponse(fmt.Sprintf("get config: %v", err))
	}
	if value != "" {
		return jsonResponse(map[string]string{
			"key": key, "value": value, "scope": "global", "source": "proxy_state",
		})
	}

	// 3. Read from config.yaml
	cfgPath := filepath.Join(h.dataDir, "config.yaml")
	raw, err := config.GetValue(cfgPath, key)
	if err != nil {
		return errorResponse(fmt.Sprintf("read config: %v", err))
	}
	if raw != nil {
		str := fmt.Sprintf("%v", raw)
		return jsonResponse(map[string]string{
			"key": key, "value": str, "scope": "config.yaml", "source": "config.yaml",
		})
	}

	return jsonResponse(map[string]string{
		"key": key, "value": "", "scope": "", "source": "",
	})
}

// handleTrackGap records a knowledge gap via daemon.
func (h *Handler) handleTrackGap(params map[string]any) Response {
	topic, _ := params["topic"].(string)
	project, _ := params["project"].(string)
	if topic == "" {
		return errorResponse("topic required")
	}
	if err := h.store.TrackGap(topic, project); err != nil {
		return errorResponse(fmt.Sprintf("track gap: %v", err))
	}
	return jsonResponse(map[string]string{"status": "ok"})
}

func (h *Handler) handleResolveGap(params map[string]any) Response {
	topic, _ := params["topic"].(string)
	project, _ := params["project"].(string)
	if topic == "" {
		return errorResponse("topic required")
	}
	if err := h.store.ResolveGap(topic, project, 0); err != nil {
		return errorResponse(fmt.Sprintf("resolve gap: %v", err))
	}
	return jsonResponse(map[string]string{"status": "ok"})
}

func (h *Handler) handleGetActiveGaps(params map[string]any) Response {
	project, _ := params["project"].(string)
	limit := intOr(params, "limit", 10)
	gaps, err := h.store.GetActiveGaps(project, limit)
	if err != nil {
		return errorResponse(fmt.Sprintf("get active gaps: %v", err))
	}
	type gapEntry struct {
		ID       int64  `json:"id"`
		Topic    string `json:"topic"`
		HitCount int    `json:"hit_count"`
	}
	entries := make([]gapEntry, len(gaps))
	for i, g := range gaps {
		entries[i] = gapEntry{ID: g.ID, Topic: g.Topic, HitCount: g.HitCount}
	}
	return jsonResponse(map[string]any{"gaps": entries})
}

func (h *Handler) handleGenerateBriefing(params map[string]any) Response {
	project, _ := params["project"].(string)
	if project == "" {
		return errorResponse("project required")
	}

	// Use full CWD path for Code Map scanner, fall back to short name
	projectDir, _ := params["project_dir"].(string)
	if projectDir != "" {
		project = projectDir
	}

	sessionID, _ := params["session_id"].(string)

	// Persist source_agent if passed — belt-and-suspenders with register_pid
	// so briefings can identify non-Claude agents even on first request.
	if sa, _ := params["source_agent"].(string); sa != "" && sessionID != "" {
		_ = h.store.SetProxyState("source_agent:"+sessionID, sa)
	}

	result := briefing.GenerateFullBriefing(h.store, h.dataDir, project, sessionID)

	return jsonResponse(map[string]any{"text": result.Text, "code_map": result.CodeMap})
}

// handlePopRecentRemember returns and clears recently remembered learnings.
// The proxy calls this to inject fresh remember() content into the current session.
func (h *Handler) handlePopRecentRemember() Response {
	h.recentRememberMu.Lock()
	items := h.recentRemembered
	h.recentRemembered = nil
	h.recentRememberMu.Unlock()

	if len(items) == 0 {
		return jsonResponse(map[string]any{"items": []any{}})
	}
	return jsonResponse(map[string]any{"items": items})
}

// handlePin creates a new pinned learning.
func (h *Handler) handlePin(params map[string]any) Response {
	content, _ := params["content"].(string)
	if content == "" {
		return errorResponse("content required")
	}
	scope, _ := params["scope"].(string)
	if scope == "" {
		scope = "session"
	}
	if scope != "session" && scope != "permanent" {
		return errorResponse("scope must be 'session' or 'permanent'")
	}
	project, _ := params["project"].(string)
	source, _ := params["source"].(string)

	id, err := h.store.PinLearning(scope, project, content, source)
	if err != nil {
		return errorResponse(fmt.Sprintf("pin failed: %v", err))
	}
	return jsonResponse(map[string]any{
		"id":      id,
		"scope":   scope,
		"project": project,
		"content": content,
	})
}

// handleUnpin removes a pinned learning by ID.
func (h *Handler) handleUnpin(params map[string]any) Response {
	id, ok := params["id"].(float64)
	if !ok || id <= 0 {
		return errorResponse("id required (positive number)")
	}
	scope, _ := params["scope"].(string)
	if scope == "" {
		scope = "session"
	}
	if scope != "session" && scope != "permanent" {
		return errorResponse("scope must be 'session' or 'permanent'")
	}
	if err := h.store.UnpinLearning(scope, int64(id)); err != nil {
		return errorResponse(fmt.Sprintf("unpin failed: %v", err))
	}
	return jsonResponse(map[string]any{"unpinned_id": int64(id), "scope": scope})
}

// handleGetPins returns all pinned learnings (session + permanent merged).
func (h *Handler) handleGetPins(params map[string]any) Response {
	project, _ := params["project"].(string)

	sessionPins, err := h.store.GetPinnedLearnings("session", project)
	if err != nil {
		sessionPins = nil
	}
	permanentPins, err := h.store.GetPinnedLearnings("permanent", project)
	if err != nil {
		permanentPins = nil
	}

	type pinItem struct {
		ID      int64  `json:"id"`
		Scope   string `json:"scope"`
		Content string `json:"content"`
		Project string `json:"project,omitempty"`
	}
	var items []pinItem
	for _, p := range sessionPins {
		items = append(items, pinItem{ID: p.ID, Scope: "session", Content: p.Content, Project: p.Project})
	}
	for _, p := range permanentPins {
		items = append(items, pinItem{ID: p.ID, Scope: "permanent", Content: p.Content, Project: p.Project})
	}
	return jsonResponse(map[string]any{"pins": items})
}

func (h *Handler) handleRelatedToFile(params map[string]any) Response {
	path, _ := params["path"].(string)
	assocs, err := h.store.GetAssociationsTo("file", path)
	if err != nil {
		return errorResponse(err.Error())
	}
	return jsonResponse(assocs)
}

func (h *Handler) handleGetCoverage(params map[string]any) Response {
	project, _ := params["project"].(string)
	cov, err := h.store.GetCoverageByProject(project)
	if err != nil {
		return errorResponse(err.Error())
	}
	return jsonResponse(cov)
}

func (h *Handler) handleGetProjectProfile(params map[string]any) Response {
	project, _ := params["project"].(string)
	profile, err := h.store.GetProjectProfile(project)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return jsonResponse(map[string]string{"message": fmt.Sprintf("No profile for %q available", project)})
		}
		return errorResponse(err.Error())
	}
	return jsonResponse(profile)
}

func (h *Handler) handleGetSelfFeedback(params map[string]any) Response {
	days := intOr(params, "days", 30)
	fbs, err := h.store.GetSelfFeedback(days)
	if err != nil {
		return errorResponse(err.Error())
	}
	return jsonResponse(fbs)
}

func (h *Handler) handleIndexStatus() Response {
	if h.IndexProgress == nil {
		return jsonResponse(map[string]any{
			"running": false,
			"total":   0,
			"done":    0,
			"skipped": 0,
		})
	}
	total, done, skipped, running := h.IndexProgress()
	return jsonResponse(map[string]any{
		"running": running,
		"total":   total,
		"done":    done,
		"skipped": skipped,
	})
}

// handleTrackSessionEnd records a session end (clear/compact) in the DB.
// Called by the session-end hook via daemon socket instead of direct DB access
// to avoid write-lock contention with the daemon's own transactions.
func (h *Handler) handleTrackSessionEnd(params map[string]any) Response {
	project, _ := params["project"].(string)
	sessionID, _ := params["session_id"].(string)
	reason, _ := params["reason"].(string)

	if project == "" || sessionID == "" || reason == "" {
		return errorResponse("project, session_id, and reason required")
	}

	if err := h.store.TrackSessionEnd(project, sessionID, reason); err != nil {
		return errorResponse(fmt.Sprintf("track session end: %v", err))
	}

	if reason == "clear" {
		projectShort := h.store.ResolveProjectShort(project)
		h.store.ClearSessionPins(projectShort)
	}

	return jsonResponse(map[string]any{"status": "tracked", "session_id": sessionID})
}

// archivePattern matches the archive marker written by collapse.go:buildArchiveBlock.
// Format: [Archiv: Messages 1-725 (725 msgs) — get_compacted_stubs('...', 1, 725) zum Reinzoomen]
// Keep in sync with the writer in internal/proxy/collapse.go line 145.
var archivePattern = regexp.MustCompile(`\[Archiv: Messages (\d+)-(\d+) \((\d+) msgs\)`)

// parseFrozenStubs extracts CompactedBlock entries from a frozen stubs JSON blob.
// Frozen stubs are stored in proxy_state with key "frozen:<threadID>".
// The JSON schema matches sawtooth.frozenPersisted:
//
//	{"messages": [...], "cutoff": 725, "boundary_hash": "...", "prefix_hash": "...", "tokens": 45678, "raw_tokens": 67890}
//
// The frozen messages contain archive blocks like:
//
//	"[Archiv: Messages 1-725 (725 msgs) — get_compacted_stubs(...)]"
//
// from which we extract the message range.
func parseFrozenStubs(frozenJSON, threadID string, fromIdx, toIdx int) ([]*storage.CompactedBlock, error) {
	var fp struct {
		Messages     []any  `json:"messages"`
		Cutoff       int    `json:"cutoff"`
		BoundaryHash string `json:"boundary_hash"`
		PrefixHash   string `json:"prefix_hash"`
		Tokens       int    `json:"tokens"`
		RawTokens    int    `json:"raw_tokens"`
	}
	if err := json.Unmarshal([]byte(frozenJSON), &fp); err != nil {
		return nil, fmt.Errorf("unmarshal frozen stubs: %w", err)
	}

	var blocks []*storage.CompactedBlock
	for _, msg := range fp.Messages {
		m, ok := msg.(map[string]any)
		if !ok {
			continue
		}

		// Collect text content from string or block-based message content
		var texts []string
		if text, ok := m["content"].(string); ok {
			texts = append(texts, text)
		} else if contentBlocks, ok := m["content"].([]any); ok {
			for _, cb := range contentBlocks {
				if bm, ok := cb.(map[string]any); ok {
					if text, ok := bm["text"].(string); ok {
						texts = append(texts, text)
					}
				}
			}
		}

		for _, text := range texts {
			matches := archivePattern.FindStringSubmatch(text)
			if len(matches) < 4 {
				continue
			}
			start, _ := strconv.Atoi(matches[1])
			end, _ := strconv.Atoi(matches[2])
			msgCount, _ := strconv.Atoi(matches[3])

			if toIdx > 0 && (start > toIdx || end < fromIdx) {
				continue
			}
			if fromIdx > 0 && end < fromIdx {
				continue
			}

			blocks = append(blocks, &storage.CompactedBlock{
				ThreadID: threadID,
				StartIdx: start,
				EndIdx:   end,
				Content: fmt.Sprintf("[Archiv: Messages %d-%d (%d msgs) — cutoff=%d, frozen_tokens=%d — use expand_context() or deep_search() for full content]",
					start, end, msgCount, fp.Cutoff, fp.Tokens),
			})
		}
	}

	return blocks, nil
}

// formatMessageRange converts messages into a readable text block.
// Includes role, type, tool name, and timestamp for each message.
// Content is truncated at 5000 chars per message.
func formatMessageRange(msgs []models.Message, threadID string, from, to int) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "[Messages %d-%d (%d msgs) for %s]\n", from, to, len(msgs), threadID)

	for _, m := range msgs {
		ts := m.Sequence
		content := m.Content
		if len(content) > 5000 {
			content = content[:5000] + "..."
		}
		if content == "" && len(m.ContentBlob) > 0 {
			content = string(m.ContentBlob)
			if len(content) > 5000 {
				content = content[:5000] + "..."
			}
		}

		typeStr := m.MessageType
		if m.ToolName != "" {
			typeStr += ":" + m.ToolName
		}
		fmt.Fprintf(&sb, "\n--- %s (%s, seq=%d, ts=%s) ---\n%s\n",
			m.Role, typeStr, ts, m.Timestamp.Format("15:04:05"), content)
	}
	return sb.String()
}
