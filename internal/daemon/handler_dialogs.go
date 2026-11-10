package daemon

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/carsteneu/yesmem/internal/channeltext"
	"github.com/carsteneu/yesmem/internal/storage"
)

// resolveSessionID extracts session ID from params, falls back to pidMap lookup, then activeSessionID.
// Note: activeSessionID fallback can be wrong with concurrent sessions — PID lookup is preferred.
// resolveSessionIDFromPID resolves a session ID from a caller PID using only
// PID-proven sources: pidMap, PID file, and agent DB table. Never falls back
// to global active-session state (active_session_opencode, activeSessionID)
// which is Last-Writer-Wins and can silently pick a different same-basename
// session. Returns "" if no PID mapping exists.
func (h *Handler) resolveSessionIDFromPID(pid int) string {
	if sid := h.sessionFromPIDMap(pid); sid != "" {
		return sid
	}
	if sid := h.tryRecoverFromPIDFile(pid); sid != "" {
		h.pidMapMu.Lock()
		h.pidMap[sid] = pid
		h.pidMapMu.Unlock()
		return sid
	}
	if agent, err := h.store.AgentGetByPID(pid); err == nil && agent != nil {
		return agent.SessionID
	}
	return ""
}

// sessionFromPIDMap returns the session a PID is registered for, or "" when the
// map holds no entry — or more than one. Several sessions on one PID is a real
// state (the opencode plugin registers the PID hosting a session, and PID-file
// rebuilds can add more); returning whichever entry the map yields first would
// silently misattribute the call to another session.
func (h *Handler) sessionFromPIDMap(pid int) string {
	h.pidMapMu.Lock()
	defer h.pidMapMu.Unlock()
	match := ""
	for sid, p := range h.pidMap {
		if p != pid {
			continue
		}
		if match != "" {
			return ""
		}
		match = sid
	}
	return match
}

func (h *Handler) resolveSessionID(params map[string]any, key string) string {
	if sid := h.provenSessionID(params, key); sid != "" {
		return sid
	}
	return h.resolveActiveSessionFallback(params)
}

// provenSessionID resolves a session id only from evidence that identifies the
// caller: a name it passed itself, an id the MCP layer attributed, or a PID or
// working directory that maps to an agent row. It returns "" when the caller
// cannot be identified.
//
// The distinction matters because resolveSessionID's remaining fallbacks are
// last-writer-wins across every session on the machine. A caller that reaches
// this point needs an identity it can be *held to* — a delivery that is skipped
// or a session that is woken on a guessed identity is worse than admitting the
// caller is unknown. The implicit ACK in handleSendTo relies on it too.
func (h *Handler) provenSessionID(params map[string]any, key string) string {
	if sid, ok := params[key].(string); ok && sid != "" {
		return sid
	}
	// Direct session_id from MCP server (CLAUDE_SESSION_ID or explicit param)
	if sid, ok := params["_session_id"].(string); ok && sid != "" && !h.envDerivedSessionUntrusted(params) {
		return sid
	}
	// Try reverse-lookup via caller PID (MCP server passes its PPID). Only when
	// that PID identifies the caller — see callerPIDIsAgent.
	if pid, ok := params["_caller_pid"].(float64); ok && pid > 0 && h.callerPIDIsAgent(params) {
		if sid := h.sessionFromPIDMap(int(pid)); sid != "" {
			return sid
		}
		// PID not in memory — try PID file on disk (non-Claude agents persist these)
		if sid := h.tryRecoverFromPIDFile(int(pid)); sid != "" {
			h.pidMapMu.Lock()
			h.pidMap[sid] = int(pid)
			h.pidMapMu.Unlock()
			return sid
		}
		// DB fallback: parallel-safe resolution for non-claude backends.
		// pidMap and PID file can miss (fresh daemon start, in-memory cleared),
		// and the global proxy-state below is Last-Writer-Wins across concurrent
		// agents. The agents table is the authoritative record: bridge stores
		// the backend PID in agents.pid at spawn time, so a direct lookup
		// uniquely identifies the caller's agent.
		if agent, err := h.store.AgentGetByPID(int(pid)); err == nil && agent != nil {
			return agent.SessionID
		}
	}
	if agent := h.agentByCallerCWD(params); agent != nil {
		return agent.SessionID
	}
	return ""
}

// guessedSessionLogInterval bounds how often the same guessed identity is
// reported. A fallback hit repeats on every request; one line per minute is
// enough to find it in the log.
const guessedSessionLogInterval = time.Minute

// logGuessedSession names a session that was guessed rather than identified. The
// fallback stays — refusing it would break every caller that passes no identity
// at all — but a silent guess attributes the write to whichever session
// registered last, so the guess and the missing carrier are reported instead of
// happening invisibly (YM-307).
func (h *Handler) logGuessedSession(params map[string]any, sessionID, source string) {
	key := source + "=" + sessionID
	h.guessedSessionMu.Lock()
	if h.guessedSessionLast == nil {
		h.guessedSessionLast = make(map[string]time.Time)
	}
	if last, ok := h.guessedSessionLast[key]; ok && time.Since(last) < guessedSessionLogInterval {
		h.guessedSessionMu.Unlock()
		return
	}
	h.guessedSessionLast[key] = time.Now()
	h.guessedSessionMu.Unlock()

	callerPID, _ := params["_caller_pid"].(float64)
	log.Printf("[session] guessed identity %q from %s: the caller named no session_id, its _caller_pid %d identified no agent, and _cwd %q matched none",
		sessionID, source, int(callerPID), stringOr(params, "_cwd", ""))
}

// resolveActiveSessionFallback is the last-writer-wins fallback of
// resolveSessionID: the session the proxy or idle tick saw last. It is a guess,
// not an identification, and it says so in the log.
func (h *Handler) resolveActiveSessionFallback(params map[string]any) string {
	h.activeSessionMu.Lock()
	defer h.activeSessionMu.Unlock()
	// OpenCode has no hooks — the proxy persists the active session on each request.
	// Check this BEFORE activeSessionID to avoid returning a stale Claude Code session
	// that was set by a proxy-health-check or other non-OpenCode caller.
	if sa, _ := params["_source_agent"].(string); sa == "opencode" {
		// The key is shared with codex, and _source_agent is caller-supplied, so
		// the label alone does not make the value an opencode session (audit
		// 16.10.2026, YM-328). The value is returned, not cached: h.activeSessionID
		// is the global last-writer state, and writing it here would hand this
		// session to every later caller without an identity of its own (YM-338).
		if sid, err := h.store.GetProxyState("active_session_opencode"); err == nil && strings.HasPrefix(sid, "opencode:") {
			h.logGuessedSession(params, sid, "active_session_opencode")
			return sid
		}
	}
	if h.activeSessionID != "" {
		h.logGuessedSession(params, h.activeSessionID, "activeSessionID")
		return h.activeSessionID
	}
	// No last resort from persisted state here: the branch above already reads
	// active_session_opencode for the only caller it belongs to. The key can still
	// hold a codex channel written by a version before YM-338 (register_pid no
	// longer writes it for codex), and handing that to an unidentified caller would
	// attribute its writes to a foreign session (audit 16.10.2026, YM-328).
	return ""
}

// callerPIDIsAgent reports whether the caller PID can stand for the caller,
// i.e. whether it belongs to a running agent the daemon spawned.
//
// In the v1 layout the `yesmem mcp` child of an agent's TUI carries that TUI's
// PID as its parent, so _caller_pid identifies the caller. Under opencode v2 the
// child belongs to the shared `opencode serve --service` process instead, and
// the plugin registers that one PID once per session — every registration
// displaces the previous one, so the PID ends up standing for whichever session
// registered last. Resolving through it yields exactly the last-writer-wins
// misattribution the caller-cwd path exists to avoid.
//
// Non-opencode callers report their own PID and keep the PID path. A store that
// cannot be read reports "agent", so a working path is never discarded because
// of an unreadable database.
func (h *Handler) callerPIDIsAgent(params map[string]any) bool {
	pid, _ := params["_caller_pid"].(float64)
	if pid <= 0 {
		return false
	}
	if sa, _ := params["_source_agent"].(string); sa != "opencode" {
		return true
	}
	agent, err := h.store.AgentGetByPID(int(pid))
	if errors.Is(err, sql.ErrNoRows) {
		return false
	}
	if err != nil {
		return true
	}
	return agent != nil
}

// envDerivedSessionUntrusted reports whether an _session_id that the MCP layer
// read from its own environment must not be trusted for this call.
//
// opencode v2 hosts every session in one `opencode serve --service` process, so
// the `yesmem mcp` children inherit that process's environment and report the
// id of whichever session was created last — never the caller's. The MCP layer
// marks such an id with _session_id_source="env" (see internal/mcp).
//
// The env id stays authoritative in the v1 layout, where the MCP child is the
// direct child of the agent the daemon spawned. Claude and Codex sessions rely
// on the env id as the only source and are never discounted here.
func (h *Handler) envDerivedSessionUntrusted(params map[string]any) bool {
	if src, _ := params["_session_id_source"].(string); src != "env" {
		return false
	}
	if sa, _ := params["_source_agent"].(string); sa != "opencode" {
		return false
	}
	pid, _ := params["_caller_pid"].(float64)
	if pid <= 0 {
		return false
	}
	return !h.callerPIDIsAgent(params)
}

// agentByCallerCWD resolves the calling opencode v2 agent from the working
// directory the MCP layer reports in _cwd.
// It exists because the PID path cannot work for opencode v2: the TUI no longer
// spawns the `yesmem mcp` child, the shared `opencode serve --service` process
// does. So the child's PPID — and with it _caller_pid — is the same service PID
// for every session that service hosts, while agents.pid holds the TUI PID
// (verified 2026-10-15 via ps --ppid and /proc/<mcp-pid>/environ). The child's
// cwd, by contrast, is the session's workdir, and callerCWD in internal/mcp
// sends exactly that for opencode.
//
// The workdir is matched against the *session* store, not against
// agents.project: a loop agent is spawned under its repository root while its
// session lives in the worktree, so the agent row cannot answer where it runs.
//
// Returns nil unless exactly one running opencode agent runs in that directory:
// two agents sharing a workdir cannot be told apart, and a wrong answer would
// misattribute another loop's telemetry. The same reasoning applies to a
// non-agent opencode session that shares the directory with an agent — the
// caller is then attributed to that agent. That is accepted: refusing whenever
// two root sessions share a directory would disable the repair for the ordinary
// worktree layout, and the effect stays inside one user's own sessions
// (telemetry, dialog initiator, scratchpad owner).
func (h *Handler) agentByCallerCWD(params map[string]any) *storage.Agent {
	if sa, _ := params["_source_agent"].(string); sa != "opencode" {
		return nil
	}
	cwd, _ := params["_cwd"].(string)
	if cwd == "" {
		return nil
	}
	agents, err := h.store.AgentList("")
	if err != nil {
		return nil
	}
	// Collect the candidates first: this runs on every request path, so the
	// session lookup is skipped entirely when no agent can match anyway.
	var candidates []*storage.Agent
	for i := range agents {
		a := &agents[i]
		if a.Status == "running" && a.Backend == "opencode" && a.OpencodeSessionID != "" {
			candidates = append(candidates, a)
		}
	}
	if len(candidates) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), sessionTableQueryTimeout)
	defer cancel()
	sessionIDs := opencodeRootSessionIDsInDirFn(ctx, cwd)
	if len(sessionIDs) == 0 {
		return nil
	}
	var match *storage.Agent
	for _, a := range candidates {
		if !containsString(sessionIDs, a.OpencodeSessionID) {
			continue
		}
		if match != nil {
			return nil
		}
		match = a
	}
	return match
}

func containsString(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
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

	// Sender: the label the target sees. Cosmetic — it does not decide whether
	// the message is delivered; that is the caller's proven identity, resolved
	// separately in deliveryCaller. The caller is resolved once, from proven
	// sources only; the implicit ack below reuses it.
	caller := h.provenSessionID(params, "")
	sender, _ := params["sender"].(string)
	if sender == "" {
		sender, _ = params["session_id"].(string)
	}
	if sender == "" {
		sender, _ = params["_sender"].(string)
	}
	if sender == "" {
		sender = caller
	}
	// No further fallback. The remaining source would be the last-writer-wins
	// active session, which is whichever session touched the proxy last: replies
	// to that label reached a foreign session (YM-60). A caller that proved
	// nothing is labelled "unknown" instead.
	if sender == "" {
		sender = "unknown"
	}

	// Resolve the target once, at send time: a named channel stays literal, an
	// id-like target is resolved against the known agents. Ambiguous or
	// unknown short forms are refused instead of being black-holed. An opencode
	// agent whose session is still unknown keeps the synthetic address its proxy
	// never polls, so the caller gets that as a warning rather than silence.
	warning := h.bindSendTarget(target)
	resolved, err := h.store.ResolveChannelTarget(target)
	if err != nil {
		return errorResponse(err.Error())
	}
	if resolved != target {
		log.Printf("[send_to] target %q resolved to %q", target, resolved)
	}

	// The delivery side of the DONE contract runs before anything is stored: a
	// DONE payload leaves the daemon only when the verdict for exactly this report
	// is already in the registry. Every other message pays one prefix test here and
	// continues unchanged, because send_to is infrastructure first.
	delivery := h.settleDONEDelivery(resolved, caller, content)
	if delivery.held {
		return delivery.response
	}
	content = delivery.content

	// ACK-on-ACK prevention, and the implicit ack below, run on the identity the
	// caller can be held to (implicitAckIdentity): both pair an inbox with the
	// sender, so a guessed sender must not take part in either. With the global
	// active-session fallback as the sender, a self-addressed status row
	// (target == sender) made the check drop every following message before it
	// was stored — the daemon's status reports to the orchestrator among them
	// (YM-194).
	ackIdentity := h.implicitAckIdentity(params, sender, caller)
	if ackIdentity != "" && h.store.IsAckOnAck(resolved, ackIdentity, msgType) {
		sShort, tShort := ackIdentity, resolved
		if len(sShort) > 8 {
			sShort = sShort[:8]
		}
		if len(tShort) > 8 {
			tShort = tShort[:8]
		}
		log.Printf("[send_to] ACK-on-ACK dropped: %s→%s (type=%s)", sShort, tShort, msgType)
		return jsonResponse(addSendWarning(map[string]any{"message_id": 0, "target": resolved, "ack_dropped": true}, warning))
	}

	// Implicit ack: when sender replies, mark their incoming messages as read.
	// Must happen BEFORE insert — otherwise the new message gets marked too.
	// Only an identity proven to be the caller is acknowledged (see
	// implicitAckIdentity): otherwise a free sender parameter or the global
	// active-session guess would mark another session's inbox read.
	if ackIdentity != "" {
		h.store.MarkChannelInboxRead(h.store.ChannelAddresses(ackIdentity))
	}

	msgID, err := h.store.SendChannelMessage(resolved, sender, content+"\n", msgType)
	if err != nil {
		return errorResponse(err.Error())
	}

	// Active delivery for opencode sessions: an idle session only ever sees
	// what its own proxy request pulls in, so a message it should act on has to
	// be pushed into its inbox. Every other target — and every failure — keeps
	// the stored path, which the target's proxy picks up on its next request.
	outcome, deliveryErr := h.sendViaOpencodeAPI(channelDelivery{
		Address:   resolved,
		Sender:    sender,
		Caller:    h.deliveryCaller(params),
		Content:   content + "\n",
		MsgType:   msgType,
		MessageID: msgID,
	})
	if deliveryErr != nil {
		// The outcome decides which sentence is true: a released claim means the
		// message is waiting for the target, an unconfirmed one means it is
		// claimed and may or may not have arrived.
		if outcome == deliveryStored {
			log.Printf("[send_to] API delivery to %q failed, message %d stays stored: %v", resolved, msgID, deliveryErr)
		} else {
			log.Printf("[send_to] API delivery to %q failed, message %d is unconfirmed: %v", resolved, msgID, deliveryErr)
		}
	}

	return jsonResponse(addSendWarning(map[string]any{
		"message_id":    msgID,
		"target":        resolved,
		"delivered_via": string(outcome),
	}, warning))
}

// addSendWarning attaches the undeliverable-target warning to a send_to
// response. An empty warning leaves the response untouched.
func addSendWarning(m map[string]any, warning string) map[string]any {
	if warning != "" {
		m["warning"] = warning
	}
	return m
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

// channelInboxSession returns the inbox a channel read or mark is about.
//
// A named session_id is not an ownership proof: any client of the same-UID
// daemon socket can open a connection and name an id. The one legitimate consumer
// of these endpoints is the proxy — it reads and marks for the session whose
// request it is serving and has no session of its own — so a caller that arrives
// on a connection has to be it, judged by the peer process the kernel reports
// (SO_PEERCRED, see peerPID). Nothing the caller sends counts as its identity
// here: _session_id is a value it chose, and trusting it made this guard
// bypassable with a single extra key (review finding on YM-204).
//
// Where no peer can be established — a call from the daemon's own code, or a
// platform without peer credentials, which the identity checks in this package
// already abstain from — the named session stands as before. The exposure this
// closes is the socket, not the daemon's internal calls.
func (h *Handler) channelInboxSession(params map[string]any) (string, error) {
	named, _ := params["session_id"].(string)
	if strings.TrimSpace(named) == "" {
		return "", errors.New("session_id required")
	}
	if pid := peerPIDParam(params); pid > 0 && procTableReadable && !peerIsProxy(pid) {
		// Logged, not silent: the only caller of these endpoints is the proxy, so
		// a refusal means something else tried to read an inbox — or that the
		// proxy's own argv no longer matches (a wrapper or a renamed binary),
		// which would stop channel delivery until it is noticed.
		log.Printf("[channel] refused a read of %q by peer pid %d, which is not the proxy", named, pid)
		return "", errors.New("a channel inbox may only be read by the proxy")
	}
	return named, nil
}

// peerIsProxy reports whether a pid belongs to the installation's proxy: the
// argv conjunct this package already uses for agent processes (isAgentProcess)
// and for finding a proxy to stop (cmd_process.go), anchored on a pid the kernel
// reported rather than one the caller sent.
//
// argv is a routing label, not a credential — the same reading as the daemon
// sender prefix (daemon_sender.go): a same-UID process that makes itself look
// like the proxy is indistinguishable here, and the socket is the boundary.
// What this does close is the ordinary case: any other client of the socket,
// an agent's own process among them, is refused.
func peerIsProxy(pid int) bool {
	argv := strings.Split(procCmdline(pid), "\x00")
	if len(argv) < 2 || !strings.Contains(argv[0], "yesmem") {
		return false
	}
	return containsString(argv[1:], "proxy")
}

// peerPIDParam reads the peer pid the socket layer attached to a request. Only
// the in-process int form counts: the socket layer writes the kernel's answer as
// an int, while anything a client sends arrives through JSON decoding as a
// float64 — so a caller that names a pid of its own choosing is ignored on every
// transport. On a transport that attaches no peer at all (the HTTP API), the key
// is simply absent, and those methods are not served there at all
// (httpDeniedMethods).
func peerPIDParam(params map[string]any) int {
	if v, ok := params["_peer_pid"].(int); ok {
		return v
	}
	return 0
}

func (h *Handler) handleCheckChannel(params map[string]any) Response {
	sessionID, err := h.channelInboxSession(params)
	if err != nil {
		return errorResponse(err.Error())
	}
	msgs, err := h.store.GetChannelMessages(h.store.ChannelAddresses(sessionID))
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
	sessionID, err := h.channelInboxSession(params)
	if err != nil {
		return errorResponse(err.Error())
	}
	if err := h.store.MarkChannelMessagesInjected(h.store.ChannelAddresses(sessionID)); err != nil {
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
		"dialog_id":      dialog.ID,
		"initiator":      dialog.Initiator,
		"topic":          dialog.Topic,
		"messages":       msgTexts,
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

// handleRegisterPID stores the OS PID of a Claude Code / OpenCode process for
// MCP tool session resolution. Also updates activeSessionID so MCP tools can
// resolve the sender. Non-Claude agents can pass source_agent to persist their
// identity for briefing generation.
func (h *Handler) handleRegisterPID(params map[string]any) Response {
	sessionID, _ := params["session_id"].(string)
	pid, _ := params["pid"].(float64)
	if sessionID == "" || pid == 0 {
		return errorResponse("session_id and pid required")
	}
	h.pidMapMu.Lock()
	// Remove ALL old session→PID entries for this PID. A TUI server may
	// handle multiple sessions over its lifetime and can accumulate stale
	// mappings; re-registration must displace all of them so resolveSessionID
	// returns only the current session.
	for sid, p := range h.pidMap {
		if p == int(pid) {
			delete(h.pidMap, sid)
		}
	}
	h.pidMap[sessionID] = int(pid)
	h.pidMapMu.Unlock()

	// Keep activeSessionID in sync — MCP tools use this as fallback sender
	h.activeSessionMu.Lock()
	h.activeSessionID = sessionID
	h.activeSessionMu.Unlock()

	// Persist source_agent for non-Claude agents so briefings identify correctly
	if sa, _ := params["source_agent"].(string); sa != "" {
		_ = h.store.SetProxyState("source_agent:"+sessionID, sa)
	}

	// Write PID file for recovery after daemon restart (non-Claude agents only)
	if sa, _ := params["source_agent"].(string); sa == "opencode" || sa == "codex" {
		pidDir := filepath.Join(h.dataDir, "sessions")
		os.MkdirAll(pidDir, 0755)
		pidFile := filepath.Join(pidDir, fmt.Sprintf("%d", int(pid)))
		os.WriteFile(pidFile, []byte(sessionID+"\n"), 0644)
	}
	// Persist the active opencode session so resolveSessionID's fallback works
	// after a restart. Only opencode: the key names the opencode channel, and a
	// codex registration used to displace it (audit 16.10.2026, YM-338).
	if sa, _ := params["source_agent"].(string); sa == "opencode" {
		_ = h.store.SetProxyState("active_session_opencode", sessionID)
	}

	return jsonResponse(map[string]any{"status": "ok"})
}

// RebuildFromPIDFiles scans dataDir/sessions/ for PID→sessionID mappings
// and restores pidMap after a daemon restart. Only loads entries for
// currently running PIDs (stale entries from dead processes are skipped).
// Non-Claude agents (opencode, codex) write PID files in handleRegisterPID.
func (h *Handler) RebuildFromPIDFiles() {
	pidDir := filepath.Join(h.dataDir, "sessions")
	entries, err := os.ReadDir(pidDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		pid, convErr := strconv.Atoi(e.Name())
		if convErr != nil {
			continue
		}
		// Check if process is still alive; delete stale PID file if not
		if err := syscall.Kill(pid, 0); err != nil {
			os.Remove(filepath.Join(pidDir, e.Name()))
			continue
		}
		data, readErr := os.ReadFile(filepath.Join(pidDir, e.Name()))
		if readErr != nil {
			continue
		}
		sid := strings.TrimSpace(string(data))
		if sid == "" {
			continue
		}
		h.pidMapMu.Lock()
		h.pidMap[sid] = pid
		h.pidMapMu.Unlock()
	}
}

// tryRecoverFromPIDFile reads a single PID file to recover sessionID.
// Used as a lazy fallback in resolveSessionID when pidMap has no entry.
func (h *Handler) tryRecoverFromPIDFile(pid int) string {
	pidFile := filepath.Join(h.dataDir, "sessions", strconv.Itoa(pid))
	data, err := os.ReadFile(pidFile)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// handleListRegistrations returns the daemon's live session→PID, session→window
// and session→terminal maps. Used by `save-terminals` to annotate terminal
// windows with their running agent sessions.
func (h *Handler) handleListRegistrations(params map[string]any) Response {
	h.pidMapMu.Lock()
	sessions := make(map[string]int, len(h.pidMap))
	for sid, p := range h.pidMap {
		sessions[sid] = p
	}
	h.pidMapMu.Unlock()

	h.windowMapMu.Lock()
	windows := make(map[string]string, len(h.windowMap))
	pids := make(map[string]int, len(h.windowMap))
	for sid, xid := range h.windowMap {
		windows[sid] = xid
		pids[sid] = h.windowPid[sid]
	}
	terminals := make(map[string]string, len(h.terminalMap))
	for sid, t := range h.terminalMap {
		terminals[sid] = t
	}
	h.windowMapMu.Unlock()

	// Never hand save-terminals a window that is closed or whose id now
	// belongs to another client (one window-manager call, outside the lock).
	if live, known := h.liveSnapshot(); known {
		for sid, xid := range windows {
			if _, ok := windowAlive(xid, pids[sid], live); !ok {
				delete(windows, sid)
			}
		}
	}

	return jsonResponse(map[string]any{
		"sessions":  sessions,
		"windows":   windows,
		"terminals": terminals,
	})
}

// maxRegisterLen bounds session_id and terminal in register_window; real
// values are well below (opencode:ses_… ≈ 40 chars).
const maxRegisterLen = 256

// sameX11Window reports whether two well-formed X11 ids name the same window
// ("0x06800004" and "0x6800004" do).
func sameX11Window(a, b string) bool {
	x, errA := strconv.ParseUint(a[2:], 16, 64)
	y, errB := strconv.ParseUint(b[2:], 16, 64)
	return errA == nil && errB == nil && x == y
}

// handleRegisterWindow stores the X11 terminal window a session is shown in
// (fed by the opencode TUI plugin) so consumers can find that window, e.g. to
// open a spawned agent on the caller's desktop. Read-only use: the daemon never
// drives a registered window (no input injection). Input is validated on write
// (any same-uid socket client can call this), and a window shows exactly one
// session: registering a window removes every other session claiming it, so
// save-terminals sees a deterministic session per window. window_pid (the
// window's owner pid, optional) lets later readers detect a window id the X
// server handed to another client. No window-manager call happens here.
func (h *Handler) handleRegisterWindow(params map[string]any) Response {
	sessionID, _ := params["session_id"].(string)
	windowID, _ := params["window_id"].(string)
	terminal, _ := params["terminal"].(string)
	if sessionID == "" || windowID == "" {
		return errorResponse("session_id and window_id required")
	}
	if !x11WindowID.MatchString(windowID) {
		return errorResponse("window_id must be an X11 id like 0x04c00004")
	}
	if len(sessionID) > maxRegisterLen || len(terminal) > maxRegisterLen {
		return errorResponse("session_id or terminal too long")
	}
	pid := 0
	if raw, present := params["window_pid"]; present {
		f, ok := raw.(float64)
		if !ok || f <= 0 || f > maxPid || f != float64(int(f)) {
			return errorResponse("window_pid must be a positive process id")
		}
		pid = int(f)
	}
	h.windowMapMu.Lock()
	for sid, wid := range h.windowMap {
		if sid != sessionID && x11WindowID.MatchString(wid) && sameX11Window(wid, windowID) {
			h.deleteWindowLocked(sid)
		}
	}
	if _, known := h.windowMap[sessionID]; !known && len(h.windowMap) >= maxWindowEntries {
		h.evictOldestLocked()
	}
	h.windowNextSeq++
	h.setWindowLocked(sessionID, windowRecord{Window: windowID, Terminal: terminal, Pid: pid, Seq: h.windowNextSeq})
	h.windowMapMu.Unlock()
	h.saveWindowMap()
	return jsonResponse(map[string]any{"status": "ok"})
}

// broadcastSender labels a broadcast with who sent it.
//
// The label is not chosen by the caller: a broadcast is read by every agent in
// the project, so a forged one speaks with someone else's voice — the same
// ownership class the channel reads were closed against (YM-204, YM-235). The one
// caller that may name itself is the sandboxed job socket, which rewrites the
// value before dispatch and marks the request with a type no JSON payload decodes
// into (jobCallerMark).
//
// Everything else is resolved from evidence the daemon can check, in four steps:
// the sandboxed job socket (a private marker type, and the label every cap run
// carries), the process the kernel reports on the connection (sessionFromPeerChain),
// the working directory for an opencode v2 session hosted by the shared service
// (agentByVerifiedCallerCWD — the peer must really run there), and finally the MCP
// client's own report — accepted because the peer is an MCP-shaped process (argv[1]
// is "mcp" and /proc/<pid>/exe carries yesmem), which proves the form, not the
// identity: a same-UID process can copy the binary under that name. A
// session id that arrives from any other peer is not evidence: any client of the
// same-UID socket can put any value under any key, an empty key included. With no
// step resolving there is no label, and the broadcast is refused (YM-271).
func (h *Handler) broadcastSender(params map[string]any) (string, error) {
	if _, internalCall := params["_job_caller"].(jobCallerMark); internalCall {
		if sender, _ := params["sender"].(string); sender != "" {
			return sender, nil
		}
	}
	if sender := h.sessionFromPeerChain(params); sender != "" {
		return sender, nil
	}
	if agent := h.agentByVerifiedCallerCWD(params); agent != nil && agent.SessionID != "" {
		return agent.SessionID, nil
	}
	// The MCP client reports the session it serves, and that report is the one
	// self-declaration accepted — because the peer is the yesmem MCP process itself.
	// Be honest about what that proves: argv shows that an MCP server sits on this
	// socket, not that the value belongs to its client. Same reading as peerIsProxy
	// (argv is a routing label, not a credential); a same-UID process can still
	// claim a session id it knows. Without this step a session the daemon has no pid
	// mapping for (an interactive Claude Code session) could not broadcast at all.
	if peer := peerPIDParam(params); peer > 0 && peerIsMCPClient(peer) {
		// The env-derived id under opencode v2 names the session the shared service
		// created last, not the caller (provenSessionID refuses it for the same
		// reason): a label built on it is a wrong attribution, not a weak one.
		if sid, _ := params["_session_id"].(string); sid != "" && !h.envDerivedSessionUntrusted(params) {
			return sid, nil
		}
	}
	// A transport that cannot name a peer at all (a platform without peer
	// credentials) abstains here, exactly as the channel reads do
	// (channelInboxSession): the named session stands, unverified. Refusing instead
	// would take the feature away on those platforms, not make the label safer.
	if peerPIDParam(params) < 0 {
		if sid, _ := params["_session_id"].(string); sid != "" {
			return sid, nil
		}
	}
	return "", errors.New("sender could not be established: the broadcast has no proven session, and only the job socket may name one")
}

// peerIsMCPClient reports whether a pid belongs to a yesmem MCP server process —
// the child the MCP tool calls go through. The executable is read from
// /proc/<pid>/exe, not from argv: argv[0] is free text, so a process could call
// itself "yesmem mcp" without being it (security review, YM-271). The "mcp" argument
// is still required, anchored on argv[1] like setup.isMCPHelperArgs.
func peerIsMCPClient(pid int) bool {
	exe, err := readProcExe(pid)
	if err != nil || !strings.Contains(filepath.Base(exe), "yesmem") {
		return false
	}
	argv := strings.Split(procCmdline(pid), "\x00")
	if len(argv) < 2 {
		return false
	}
	return argv[1] == "mcp"
}

// agentByVerifiedCallerCWD is agentByCallerCWD behind the check that the claim is
// true: the peer — or its parent, the process the MCP child reports — actually runs
// in the directory it names. The kernel resolves /proc/<pid>/cwd, so a symlinked
// worktree still matches. A directory is not a credential: a same-UID process that
// knows the target worktree can `cd` there and pass the check. What it closes is the
// blind claim, which is the shape a model-written parameter takes (security review,
// YM-271).
func (h *Handler) agentByVerifiedCallerCWD(params map[string]any) *storage.Agent {
	peer := peerPIDParam(params)
	claimed, _ := params["_cwd"].(string)
	if peer <= 0 || claimed == "" {
		return nil
	}
	if !processRunsIn(peer, claimed) && !processRunsIn(parentPID(peer), claimed) {
		return nil
	}
	return h.agentByCallerCWD(params)
}

// readProcExe reads the executable a pid really runs. A var so a test can declare
// its own process an MCP client without a yesmem binary on disk.
var readProcExe = func(pid int) (string, error) {
	return os.Readlink(fmt.Sprintf("%s/%d/exe", procRoot, pid))
}

// processRunsIn reports whether pid's working directory is dir.
func processRunsIn(pid int, dir string) bool {
	if pid <= 0 || dir == "" {
		return false
	}
	got, err := os.Readlink(fmt.Sprintf("%s/%d/cwd", procRoot, pid))
	if err != nil {
		return false
	}
	if got == dir {
		return true
	}
	if resolved, err := filepath.EvalSymlinks(dir); err == nil && got == resolved {
		return true
	}
	return false
}

// sessionFromPeerChain resolves the caller from the process the kernel reports,
// never from a value the caller sent.
//
// The socket layer overwrites _peer_pid with SO_PEERCRED's answer on every
// request, so it is the only identity on this path a client cannot choose. The
// one claim accepted next to it is the caller's own parent: the MCP child reports
// its parent as the caller pid, and that is checked against /proc instead of
// believed — a pid that is not the peer and not the peer's parent is ignored.
func (h *Handler) sessionFromPeerChain(params map[string]any) string {
	peer := peerPIDParam(params)
	if peer <= 0 {
		return ""
	}
	candidates := []int{peer}
	if claimed, _ := params["_caller_pid"].(float64); claimed > 0 && int(claimed) != peer {
		if parentPID(peer) == int(claimed) {
			candidates = append(candidates, int(claimed))
		}
	}
	for _, pid := range candidates {
		if sid := h.resolveSessionIDFromPID(pid); sid != "" {
			return sid
		}
	}
	return ""
}

// parentPID returns the parent process id /proc reports for pid, or 0 when the
// process is gone. The comm field may contain spaces and parentheses, so the
// fixed fields are counted from the LAST ')' (field 4 is ppid).
func parentPID(pid int) int {
	if pid <= 0 {
		return 0
	}
	data, err := os.ReadFile(fmt.Sprintf("%s/%d/stat", procRoot, pid))
	if err != nil {
		return 0
	}
	s := string(data)
	i := strings.LastIndex(s, ")")
	if i < 0 {
		return 0
	}
	fields := strings.Fields(s[i+1:])
	if len(fields) < 2 {
		return 0
	}
	ppid, err := strconv.Atoi(fields[1])
	if err != nil {
		return 0
	}
	return ppid
}

func (h *Handler) handleBroadcast(params map[string]any) Response {
	sender, senderErr := h.broadcastSender(params)
	project, _ := params["project"].(string)
	content, _ := params["content"].(string)
	if content == "" {
		return errorResponse("sender and content required")
	}
	if senderErr != nil {
		return errorResponse(senderErr.Error())
	}
	if project == "" {
		return errorResponse("project required")
	}
	// A broadcast is stored under the project its recipients check. A name nothing
	// resolves leaves a row no project ever reads, while the sender is told
	// "broadcast_sent" (YM-300).
	if unresolved, _ := params["_project_unresolved"].(string); unresolved != "" {
		return unresolvedProjectResponse(unresolved)
	}
	// A DONE is a handover to one orchestrator and is held until its report has a
	// verdict. A broadcast has no single reader to hold and no report to check, so
	// publishing the same payload would walk around the hold instead of through
	// it. The channel is refused by name, and the refusal names the one it needs.
	if isDONEPayload(content) {
		return errorResponse("broadcast refuses a DONE payload: a DONE is a handover to the orchestrator and stays held until its report has a verdict; use send_to")
	}
	msgID, err := h.store.SendBroadcast(sender, project, content)
	if err != nil {
		return errorResponse(err.Error())
	}

	// Recipients pick the broadcast up via check_broadcasts; the daemon never
	// types into their windows.
	return jsonResponse(map[string]any{"message_id": msgID, "status": "broadcast_sent"})
}

func (h *Handler) handleCheckBroadcasts(params map[string]any) Response {
	// Same ownership form as check_channel/mark_channel_read (YM-204): the
	// session id is caller-chosen and the call consumes what it shows, so it is
	// proven the same way — a named session, and a peer that is the proxy.
	sessionID, err := h.channelInboxSession(params)
	if err != nil {
		return errorResponse(err.Error())
	}
	project, _ := params["project"].(string)
	if project == "" {
		return jsonResponse(map[string]any{"messages": []any{}})
	}
	// The mirror of handleBroadcast: a reader under a name nothing resolves reads an
	// inbox that no sender can fill (YM-300).
	if unresolved, _ := params["_project_unresolved"].(string); unresolved != "" {
		return unresolvedProjectResponse(unresolved)
	}
	msgs, err := h.store.GetUnreadBroadcasts(sessionID, project)
	if err != nil {
		return errorResponse(err.Error())
	}
	var ids []int64
	var result []map[string]any
	for _, m := range msgs {
		result = append(result, map[string]any{
			"sender":       m.Sender,
			"sender_short": shortSender(m.Sender),
			"content":      m.Content,
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

// shortSender truncates a sender label for display. The value comes from the
// caller and lands in a line the receiving agent reads as structure, so it goes
// through channeltext.Field first: that flattens line breaks and drops the
// invisible characters, and only then is it cut to a display length. Cutting
// first left the control characters in place and could open a second header line.
func shortSender(sender string) string {
	// SenderLabel is the rule both transports render a sender with: it goes
	// through Field and falls back to "unknown" for a value that carries nothing
	// visible, so an unusable label cannot leave an empty slot in the header line.
	s := channeltext.SenderLabel(sender)
	// SenderLabel already bounds the value; keep the short display form on runes,
	// not bytes, so a multi-byte label is not cut mid-character.
	if runes := []rune(s); len(runes) > 8 {
		return string(runes[:8])
	}
	return s
}

// formatCheckMessagesResult formats check_messages output for injection.
//
// The line it opens is a message header to the reading agent (chunker.go treats a
// line starting with "📨" as an injected dialog), so topic and sender go through
// channeltext.Field and the body through channeltext.Neutralise: a caller-supplied
// newline must not append a second, differently attributed message to the turn.
func formatCheckMessagesResult(raw json.RawMessage) string {
	var r struct {
		Messages []struct {
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
	// The topic sits inside the header's brackets, so a bracket of its own would
	// close that section early.
	topic := strings.NewReplacer("[", "(", "]", ")").Replace(channeltext.Field(r.Topic))
	for _, m := range r.Messages {
		out += "📨 DIALOG [" + topic + "] " + shortSender(m.Sender) + ": " + channeltext.Neutralise(m.Content) + "\n"
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
		// AgentGetAnyBySession tolerates "opencode:"/"codex:" prefixes on the
		// sessionID (storage-layer stripAgentPrefix); the response preserves
		// the prefixed value for caller-contract compatibility.
		agent, err := h.store.AgentGetAnyBySession(sessionID)
		if err != nil || agent == nil {
			agent = h.bindAgentForOpencodeThread(sessionID)
		}
		if agent != nil {
			result["agent_id"] = agent.ID
			result["section"] = agent.Section
			result["status"] = agent.Status
			result["is_agent"] = true
			// Surface the project string the agent was spawned under so agents
			// can use it verbatim for scratchpad_write/read calls — prevents
			// scope drift between orchestrator spawn (project=path) and agent
			// guess (project=basename). See yesloop scratchpad-scope fix.
			if agent.Project != "" {
				result["project"] = agent.Project
			}
			if agent.CodexSessionID != "" {
				result["codex_session_id"] = agent.CodexSessionID
			}
			if agent.OpencodeSessionID != "" {
				result["opencode_session_id"] = agent.OpencodeSessionID
			}
		}
		if model, err := h.store.GetProxyState("session_model:" + sessionID); err == nil && model != "" {
			result["model"] = model
		}
		// The terminal window this session is shown in (registered by the
		// opencode TUI plugin) and the desktop that window sits on right now.
		// Only for an explicitly named session: the active-session fallback of
		// resolveSessionID is last-writer-wins and would report another
		// session's window.
		if explicitSessionParam(params) == sessionID {
			if window, desktop, ok := h.windowInfo(sessionID); window != "" {
				result["window"] = window
				if ok {
					result["desktop"] = desktop
				}
			}
		}
	}

	return jsonResponse(result)
}

// explicitSessionParam returns the session id the caller named itself
// (session_id or the per-call _session_id), or "".
func explicitSessionParam(params map[string]any) string {
	for _, key := range []string{"session_id", "_session_id"} {
		if s, ok := params[key].(string); ok && s != "" {
			return s
		}
	}
	return ""
}

// opencodeRootSessionIDsInDirFn is opencodeRootSessionIDsInDir behind a var so a
// test can drive the v2 session-table lookup for a broadcast label without an
// opencode.db on the host.
var opencodeRootSessionIDsInDirFn = opencodeRootSessionIDsInDir
