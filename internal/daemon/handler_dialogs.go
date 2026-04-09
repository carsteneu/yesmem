package daemon

import (
	"encoding/json"
	"log"
	"os/exec"
	"time"
)

// resolveSessionID extracts session ID from params, falls back to pidMap lookup, then activeSessionID.
// Note: activeSessionID fallback can be wrong with concurrent sessions — PID lookup is preferred.
func (h *Handler) resolveSessionID(params map[string]any, key string) string {
	if sid, ok := params[key].(string); ok && sid != "" {
		return sid
	}
	// Direct session_id from MCP server (CLAUDE_SESSION_ID or explicit param)
	if sid, ok := params["_session_id"].(string); ok && sid != "" {
		return sid
	}
	// Try reverse-lookup via caller PID (MCP server passes its PPID)
	if pid, ok := params["_caller_pid"].(float64); ok && pid > 0 {
		h.pidMapMu.Lock()
		for sid, p := range h.pidMap {
			if p == int(pid) {
				h.pidMapMu.Unlock()
				return sid
			}
		}
		h.pidMapMu.Unlock()
	}
	h.activeSessionMu.Lock()
	defer h.activeSessionMu.Unlock()
	return h.activeSessionID
}

func (h *Handler) handleStartDialog(params map[string]any) Response {
	initiator := h.resolveSessionID(params, "initiator")
	partner, _ := params["partner"].(string)
	topic := stringOr(params, "topic", "")
	if initiator == "" || partner == "" {
		return errorResponse("initiator and partner required")
	}
	id, err := h.store.StartDialog(initiator, partner, topic)
	if err != nil {
		return errorResponse(err.Error())
	}
	return jsonResponse(map[string]any{"dialog_id": id, "status": "pending"})
}

func (h *Handler) handleSendTo(params map[string]any) Response {
	target, _ := params["target"].(string)
	content, _ := params["content"].(string)
	if target == "" || content == "" {
		return errorResponse("target and content required")
	}

	msgType, _ := params["msg_type"].(string)
	if msgType == "" {
		msgType = "command"
	}

	// Sender: best-effort resolution (cosmetic, not used for delivery)
	sender, _ := params["sender"].(string)
	if sender == "" {
		sender, _ = params["session_id"].(string)
	}
	if sender == "" {
		sender = h.resolveSessionID(params, "_sender")
	}
	if sender == "" {
		sender = "unknown"
	}

	// ACK-on-ACK prevention
	if sender != "unknown" && h.store.IsAckOnAck(target, sender, msgType) {
		sShort, tShort := sender, target
		if len(sShort) > 8 {
			sShort = sShort[:8]
		}
		if len(tShort) > 8 {
			tShort = tShort[:8]
		}
		log.Printf("[send_to] ACK-on-ACK dropped: %s→%s (type=%s)", sShort, tShort, msgType)
		return jsonResponse(map[string]any{"message_id": 0, "target": tShort, "ack_dropped": true})
	}

	// Implicit ack: when sender replies, mark their incoming messages as read.
	// Must happen BEFORE insert — otherwise the new message gets marked too.
	if sender != "unknown" && sender != "" {
		h.store.MarkChannelMessagesRead(sender)
	}

	msgID, err := h.store.SendChannelMessage(target, sender, content, msgType)
	if err != nil {
		return errorResponse(err.Error())
	}

	go h.notifySession(target, "Du hast eine Nachricht erhalten.")

	shortTarget := target
	if len(shortTarget) > 8 {
		shortTarget = shortTarget[:8]
	}
	return jsonResponse(map[string]any{"message_id": msgID, "target": shortTarget})
}

func (h *Handler) handleCheckMessages(params map[string]any) Response {
	sessionID := h.resolveSessionID(params, "session_id")
	if sessionID == "" {
		return errorResponse("session_id required")
	}

	dialog, err := h.store.GetActiveDialogForSession(sessionID)
	if err != nil {
		return errorResponse(err.Error())
	}
	if dialog == nil {
		return jsonResponse(map[string]any{"messages": []any{}, "has_dialog": false})
	}

	msgs, err := h.store.GetUnreadMessages(dialog.ID, sessionID)
	if err != nil {
		return errorResponse(err.Error())
	}

	// DO NOT mark as read here — caller (proxy) marks after successful injection
	// via separate mark_read call. Prevents message loss on injection failure.

	// Serialize
	var result []map[string]any
	for _, m := range msgs {
		result = append(result, map[string]any{
			"id":      m.ID,
			"sender":  m.Sender,
			"content": m.Content,
		})
	}
	if result == nil {
		result = []map[string]any{}
	}

	return jsonResponse(map[string]any{
		"messages":   result,
		"has_dialog": true,
		"dialog_id":  dialog.ID,
		"topic":      dialog.Topic,
	})
}

func (h *Handler) handleMarkRead(params map[string]any) Response {
	dialogID, _ := params["dialog_id"].(float64)
	sessionID, _ := params["session_id"].(string)
	if dialogID == 0 || sessionID == "" {
		return errorResponse("dialog_id and session_id required")
	}
	if err := h.store.MarkMessagesRead(int64(dialogID), sessionID); err != nil {
		return errorResponse(err.Error())
	}
	return jsonResponse(map[string]any{"status": "ok"})
}

// --- Channel-based handlers (no dialog state) ---

func (h *Handler) handleCheckChannel(params map[string]any) Response {
	sessionID, _ := params["session_id"].(string)
	if sessionID == "" {
		return errorResponse("session_id required")
	}
	msgs, err := h.store.GetChannelMessages(sessionID)
	if err != nil {
		return errorResponse(err.Error())
	}
	var result []map[string]any
	for _, m := range msgs {
		result = append(result, map[string]any{
			"id":       m.ID,
			"sender":   m.Sender,
			"content":  m.Content,
			"msg_type": m.MsgType,
		})
	}
	if result == nil {
		result = []map[string]any{}
	}
	return jsonResponse(map[string]any{"messages": result})
}

func (h *Handler) handleMarkChannelRead(params map[string]any) Response {
	sessionID, _ := params["session_id"].(string)
	if sessionID == "" {
		return errorResponse("session_id required")
	}
	if err := h.store.MarkChannelMessagesRead(sessionID); err != nil {
		return errorResponse(err.Error())
	}
	return jsonResponse(map[string]any{"status": "ok"})
}

func (h *Handler) handleCheckInvitations(params map[string]any) Response {
	sessionID := h.resolveSessionID(params, "session_id")
	if sessionID == "" {
		return errorResponse("session_id required")
	}

	dialog, err := h.store.CheckPendingInvitations(sessionID)
	if err != nil {
		return errorResponse(err.Error())
	}
	if dialog == nil {
		return jsonResponse(map[string]any{"has_invitation": false})
	}

	// Also fetch any initial messages
	msgs, _ := h.store.GetUnreadMessages(dialog.ID, sessionID)
	var msgTexts []string
	for _, m := range msgs {
		msgTexts = append(msgTexts, m.Content)
	}
	if len(msgs) > 0 {
		h.store.MarkMessagesRead(dialog.ID, sessionID)
	}

	return jsonResponse(map[string]any{
		"has_invitation": true,
		"dialog_id":     dialog.ID,
		"initiator":     dialog.Initiator,
		"topic":         dialog.Topic,
		"messages":      msgTexts,
	})
}

func (h *Handler) handleEndDialog(params map[string]any) Response {
	sessionID := h.resolveSessionID(params, "session_id")

	// Try by dialog_id first, fallback to session lookup
	var dialogID int64
	if v, ok := params["dialog_id"].(float64); ok && v > 0 {
		dialogID = int64(v)
	} else if sessionID != "" {
		dialog, err := h.store.GetActiveDialogForSession(sessionID)
		if err != nil {
			return errorResponse(err.Error())
		}
		if dialog == nil {
			return errorResponse("no active dialog")
		}
		dialogID = dialog.ID
	} else {
		return errorResponse("dialog_id or session_id required")
	}

	if err := h.store.UpdateDialogStatus(dialogID, "ended"); err != nil {
		return errorResponse(err.Error())
	}
	return jsonResponse(map[string]any{"status": "ended", "dialog_id": dialogID})
}

// handleRegisterPID stores the OS PID of a Claude Code process for stdin injection.
// Also updates activeSessionID so MCP tools can resolve the sender.
func (h *Handler) handleRegisterPID(params map[string]any) Response {
	sessionID, _ := params["session_id"].(string)
	pid, _ := params["pid"].(float64)
	if sessionID == "" || pid == 0 {
		return errorResponse("session_id and pid required")
	}
	h.pidMapMu.Lock()
	h.pidMap[sessionID] = int(pid)
	h.pidMapMu.Unlock()

	// Keep activeSessionID in sync — MCP tools use this as fallback sender
	h.activeSessionMu.Lock()
	h.activeSessionID = sessionID
	h.activeSessionMu.Unlock()

	return jsonResponse(map[string]any{"status": "ok"})
}

// handleRegisterWindow stores the X11 window ID and terminal type for xdotool push.
func (h *Handler) handleRegisterWindow(params map[string]any) Response {
	sessionID, _ := params["session_id"].(string)
	windowID, _ := params["window_id"].(string)
	terminal, _ := params["terminal"].(string)
	if sessionID == "" || windowID == "" {
		return errorResponse("session_id and window_id required")
	}
	h.windowMapMu.Lock()
	h.windowMap[sessionID] = windowID
	if terminal != "" {
		h.terminalMap[sessionID] = terminal
	}
	h.windowMapMu.Unlock()
	return jsonResponse(map[string]any{"status": "ok"})
}

// notifySession injects a prompt into a running Claude session via xdotool.
// Returns true if push was successful, false if polling fallback is needed.
// Skips xdotool for: Ghostty (unreliable), shared windows (tabs), missing xdotool.
func (h *Handler) notifySession(sessionID, text string) bool {
	h.windowMapMu.Lock()
	wid, ok := h.windowMap[sessionID]
	if !ok || wid == "" {
		h.windowMapMu.Unlock()
		return false
	}
	// Skip Ghostty — xdotool type is unreliable in Ghostty terminals
	if h.terminalMap[sessionID] == "ghostty" {
		h.windowMapMu.Unlock()
		return false
	}
	// Check if window is shared by multiple sessions (tabs) → unsafe for xdotool
	shareCount := 0
	for _, w := range h.windowMap {
		if w == wid {
			shareCount++
		}
	}
	h.windowMapMu.Unlock()
	if shareCount > 1 {
		return false
	}

	// Check if xdotool is available
	xdotool, err := exec.LookPath("xdotool")
	if err != nil {
		return false
	}

	// windowactivate --sync, then type, then Enter
	activate := exec.Command(xdotool, "windowactivate", "--sync", wid)
	if err := activate.Run(); err != nil {
		return false
	}
	time.Sleep(300 * time.Millisecond)

	typeCmd := exec.Command(xdotool, "type", "--clearmodifiers", "--delay", "50", text)
	if err := typeCmd.Run(); err != nil {
		return false
	}
	time.Sleep(200 * time.Millisecond)

	exec.Command(xdotool, "key", "Return").Run()
	return true
}

func (h *Handler) handleBroadcast(params map[string]any) Response {
	sender := h.resolveSessionID(params, "sender")
	project, _ := params["project"].(string)
	content, _ := params["content"].(string)
	if sender == "" || content == "" {
		return errorResponse("sender and content required")
	}
	if project == "" {
		return errorResponse("project required")
	}
	msgID, err := h.store.SendBroadcast(sender, project, content)
	if err != nil {
		return errorResponse(err.Error())
	}

	// Notify all registered sessions except sender
	go func() {
		h.pidMapMu.Lock()
		sessions := make(map[string]int, len(h.pidMap))
		for sid, pid := range h.pidMap {
			sessions[sid] = pid
		}
		h.pidMapMu.Unlock()
		for sid := range sessions {
			if sid != sender {
				h.notifySession(sid, "You received a broadcast message. Check broadcasts.")
			}
		}
	}()

	return jsonResponse(map[string]any{"message_id": msgID, "status": "broadcast_sent"})
}

func (h *Handler) handleCheckBroadcasts(params map[string]any) Response {
	sessionID := h.resolveSessionID(params, "session_id")
	project, _ := params["project"].(string)
	if sessionID == "" || project == "" {
		return jsonResponse(map[string]any{"messages": []any{}})
	}
	msgs, err := h.store.GetUnreadBroadcasts(sessionID, project)
	if err != nil {
		return errorResponse(err.Error())
	}
	var ids []int64
	var result []map[string]any
	for _, m := range msgs {
		result = append(result, map[string]any{
			"sender":     m.Sender,
			"sender_short": m.Sender[:min(8, len(m.Sender))],
			"content":    m.Content,
		})
		ids = append(ids, m.ID)
	}
	if len(ids) > 0 {
		h.store.MarkBroadcastsRead(ids, sessionID)
	}
	if result == nil {
		result = []map[string]any{}
	}
	return jsonResponse(map[string]any{"messages": result})
}

// formatCheckMessagesResult formats check_messages output for injection.
func formatCheckMessagesResult(raw json.RawMessage) string {
	var r struct {
		Messages  []struct {
			Sender  string `json:"sender"`
			Content string `json:"content"`
		} `json:"messages"`
		HasDialog bool   `json:"has_dialog"`
		Topic     string `json:"topic"`
	}
	if json.Unmarshal(raw, &r) != nil {
		return ""
	}
	if !r.HasDialog || len(r.Messages) == 0 {
		return ""
	}
	var out string
	for _, m := range r.Messages {
		out += "📨 DIALOG [" + r.Topic + "] " + m.Sender[:8] + ": " + m.Content + "\n"
	}
	return out
}

// handleWhoami returns the caller's session ID and optional agent metadata.
// Agents can call this to discover their own session ID without DB queries.
func (h *Handler) handleWhoami(params map[string]any) Response {
	sessionID := h.resolveSessionID(params, "session_id")
	project, _ := params["project"].(string)

	result := map[string]any{
		"session_id": sessionID,
		"project":    project,
		"is_agent":   false,
	}

	if sessionID != "" {
		if agent, err := h.store.AgentGetAnyBySession(sessionID); err == nil && agent != nil {
			result["agent_id"] = agent.ID
			result["section"]  = agent.Section
			result["status"]   = agent.Status
			result["is_agent"] = true
		}
	}

	return jsonResponse(result)
}
