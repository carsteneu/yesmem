package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/carsteneu/yesmem/internal/opencode"
	"github.com/carsteneu/yesmem/internal/storage"
)

// --- YM-196: broken/aborted tool calls ---
//
// A loop agent can end a turn with a tool call that never ran: the model writes
// the call as assistant text (finish=stop, the text is a serialized tool_use
// block) or the turn's last tool failed, and the session then goes idle. Live
// 2026-10-16 (agent-20261016-12, 15:14:56): the edit call sat in the text,
// nothing was executed, and the agent kept producing bare "null" turns until
// the 10-minute idle guard fired its generic six-phase relay at 15:23:57.
//
// This layer closes that gap. It reads the agent's last assistant turn through
// the opencode API and, when that turn is broken, delivers a targeted relay —
// kind, tool, and the demand to repeat it as a real call — over the send_to API
// path after a short grace window. A case stays latched until the agent runs a
// tool again: a turn that neither ran a tool nor is the anchored turn itself
// does not refute the defect (the live agent answered with "null" turns, which
// would have cleared the case before the relay went out). Re-fires are bounded,
// and only relays the service actually took count towards the escalation.
// While a case is open the idle guard stays out, so its six-phase text cannot
// overlay the finding.

const (
	brokenToolcallStateIdle = iota
	brokenToolcallStatePending
	brokenToolcallStateEscalated
	brokenToolcallStateGaveUp
)

// The two broken shapes the guard distinguishes, named in the relay so the
// agent knows what it is being asked for.
const (
	brokenToolcallKindTextCall  = "text-tool-call"
	brokenToolcallKindToolError = "tool-error"
)

// brokenToolcallGrace is how long a broken turn is watched before the relay goes
// out. Detection happens on a 30s heartbeat tick, so the delivered delay is
// 30-60s: long enough that a model finishing its turn is not interrupted, short
// enough that the agent is not left hanging for the idle guard's ten minutes.
const brokenToolcallGrace = 20 * time.Second

// brokenToolcallRefireInterval spaces the relay attempts of one case.
const brokenToolcallRefireInterval = 90 * time.Second

// brokenToolcallMaxRefires is how many re-fires follow the first delivery before
// the guard escalates: three deliveries in total, like the idle layer's
// one-plus-two.
const brokenToolcallMaxRefires = 2

// brokenToolcallMaxDeliveryAttempts bounds the attempts when the service never
// confirms a delivery. Every attempt also stores the message for the target's
// own next request, so an unbounded retry loop would fill its inbox with copies
// while the API stays down.
const brokenToolcallMaxDeliveryAttempts = 4

// brokenToolcallReadTimeout bounds one session read. The guard runs inside the
// shared heartbeat goroutine, so a stalled service must not hold that goroutine
// for the client's own five-second timeout once per agent — and it must not
// multiply it by the number of loop agents. A healthy local service answers this
// read in milliseconds; the bound only matters when it does not.
const brokenToolcallReadTimeout = 1 * time.Second

// brokenToolcallSuppressAfter bounds how long an unreadable case may keep the
// idle guard out. A case is re-confirmed on every readable tick, so this only
// takes effect when the session cannot be read at all (an API outage, or a
// subagent holding the session) — and then the idle guard's PTY relay is a path
// the API is not part of, which is a reason to let it back in, not out.
const brokenToolcallSuppressAfter = 3 * time.Minute

// brokenToolcallStaleAfter drops a case entirely after this long without a
// readable turn. Two layers deadlocking against each other is what YM-149 cost
// hours to find.
const brokenToolcallStaleAfter = 10 * time.Minute

// brokenToolcall is one detected defect of an assistant turn.
type brokenToolcall struct {
	Kind string
	Tool string
}

// brokenToolcallState tracks one agent's case.
type brokenToolcallState struct {
	state int
	// anchor is the message id the case was opened on. A newer turn that ran a
	// tool closes the case; any other newer turn leaves it standing.
	anchor string
	// kind and tool describe the defect the case was opened for, kept so a relay
	// can still name it after the broken turn scrolled out of the list.
	kind string
	tool string
	// relay counts deliveries the service confirmed; attempts counts every send
	// and failed the ones it did not take. A refused push stays stored for the
	// target's next request, so it is retried on the refire cadence — but only
	// the failed ones may exhaust: counting attempts against deliveries left a
	// case with one confirmed delivery and a dead service with no terminal state
	// at all (review round 2).
	relay      int
	attempts   int
	failed     int
	detectedAt time.Time
	lastSeenAt time.Time
	// escalatedAt is when the case was reported. The suppression it buys is
	// bounded: the anchor turn never settles, so lastSeenAt keeps being renewed
	// and without this stamp the case would silence the idle layer for good.
	escalatedAt time.Time
	// lastRelayAt is stamped by the attempt, so a failed push is retried on the
	// refire cadence rather than on every tick.
	lastRelayAt time.Time
	// createdAt identifies the row the case belongs to: agent ids are reused
	// after deletion, and a new agent must not inherit an open case.
	createdAt string
}

var (
	yesloopBrokenToolcallAgents   = make(map[string]*brokenToolcallState)
	yesloopBrokenToolcallAgentsMu sync.Mutex
)

// resetBrokenToolcallState clears the case map. Used in tests.
func resetBrokenToolcallState() {
	yesloopBrokenToolcallAgentsMu.Lock()
	yesloopBrokenToolcallAgents = make(map[string]*brokenToolcallState)
	yesloopBrokenToolcallAgentsMu.Unlock()
}

// brokenToolcallPending reports whether this agent has an open case. The idle
// guard reads it: relaying its generic six-phase text into an agent whose turn
// died on a tool call replaces the specific finding with a message that does not
// mention it (YM-196). The window is short on purpose — it has to be
// re-confirmed by a readable turn, or the suppression lapses.
func brokenToolcallPending(agent storage.Agent) bool {
	yesloopBrokenToolcallAgentsMu.Lock()
	defer yesloopBrokenToolcallAgentsMu.Unlock()
	st, ok := yesloopBrokenToolcallAgents[agent.ID]
	if !ok || st.createdAt != agent.CreatedAt {
		return false
	}
	if st.state == brokenToolcallStateEscalated {
		// The agent keeps running after the nudge, so the idle layer would overlay
		// its generic phase text onto a session whose turn never executed a tool
		// (YM-196). That hold is bounded: the case is reported once, and after the
		// window the idle layer owns the agent again.
		return time.Since(st.escalatedAt) < brokenToolcallSuppressAfter
	}
	if st.state == brokenToolcallStateGaveUp {
		return false
	}
	return time.Since(st.lastSeenAt) < brokenToolcallSuppressAfter
}

// checkYesloopBrokenToolcalls is the heartbeat-driven detection, called every
// 30s. It reads the last assistant turn of every running loop agent that has a
// bound opencode session and a pipeline still in progress.
//
// The stream state is deliberately not consulted. The finding describes the
// broken session as idle, and the state that proves it is the turn's own: a
// turn that has not settled is never judged (see detectBrokenToolcall), so a
// session still writing its call is left alone. The in-memory stream flag would
// add a second way to miss the case — it is false for a session the tracker has
// never seen, and a stale "active" would delay a detection the finding wants
// within a minute. The relay queues behind running work rather than interrupting
// it (`delivery: queue`), so a relay into a busy agent costs a turn, not the
// agent's current one.
func (h *Handler) checkYesloopBrokenToolcalls() {
	agents, err := h.store.AgentList("")
	if err != nil {
		log.Printf("[broken-toolcall] AgentList error: %v", err)
		return
	}
	h.pruneBrokenToolcallStates(agents)

	// Candidates are collected before the service is looked up: an installation
	// with no loop agents — or none bound to opencode — must not pay a service
	// probe every tick.
	var candidates []storage.Agent
	for _, agent := range agents {
		if agent.Status != "running" {
			continue
		}
		if !strings.HasPrefix(agent.Section, "yesloop-") {
			continue
		}
		if agent.SessionID == "" || agent.Project == "" {
			continue
		}
		// Without a bound opencode session there is nothing to read and no inbox
		// to push to; the case is not observable for this row.
		if agent.OpencodeSessionID == "" {
			continue
		}
		// The guard's action is a push into the agent's session, so it needs the
		// agent's own process: a recycled PID would be nudged on the strength of
		// a row it no longer owns (Learning #101907).
		if agentProcessGone(agent) {
			continue
		}
		// A verdict from the done-verify layer outranks this one (YM-149).
		if doneVerifySettled(agent) {
			continue
		}
		// A finished pipeline belongs to the DONE guards.
		if CountCompletedPhases(h.readAgentScratchpad(agent)) >= donePhaseCount {
			continue
		}
		candidates = append(candidates, agent)
	}
	if len(candidates) == 0 {
		return
	}

	client := opencodeV2Service()
	if client == nil {
		return // v1-only install: no session API to read
	}

	for _, agent := range candidates {
		// An active child session means the agent is blocked inside a task()
		// subagent: the relay would only queue as a pending user message and hit
		// it after the subagent returns (#89428). A case stays open — the child
		// is the agent's own work — but the skip is logged, because the case's
		// suppression lapses only through the unreadable window.
		if opencodeChildActiveFn(agent.OpencodeSessionID) {
			if brokenToolcallTracked(agent) {
				log.Printf("[broken-toolcall] agent %s (%s) not read: a subagent session is active", agent.ID, agent.Section)
			}
			continue
		}
		h.checkOneBrokenToolcall(client, agent)
	}
}

// checkOneBrokenToolcall reads one agent's last assistant turn and drives its
// case.
func (h *Handler) checkOneBrokenToolcall(client *opencode.Client, agent storage.Agent) {
	ctx, cancel := context.WithTimeout(context.Background(), brokenToolcallReadTimeout)
	defer cancel()
	msg, ok, err := client.LastAssistantMessage(ctx, agent.OpencodeSessionID)
	if err != nil {
		// A read failure proves nothing about the turn, so an open case is kept —
		// but it is only worth a line when a case is open, or a stopped service
		// would log once per agent per tick.
		if brokenToolcallTracked(agent) {
			log.Printf("[broken-toolcall] agent %s (%s) session read failed: %v", agent.ID, agent.Section, err)
		}
		return
	}
	if !ok {
		if brokenToolcallTracked(agent) {
			log.Printf("[broken-toolcall] agent %s (%s) session reports no assistant message — case dropped",
				agent.ID, agent.Section)
		}
		h.clearBrokenToolcall(agent)
		return
	}

	action, b := assessBrokenToolcall(agent, msg)
	switch action {
	case brokenToolcallActionClear:
		h.clearBrokenToolcall(agent)
	case brokenToolcallActionRelay:
		h.recordBrokenToolcallAttempt(agent, h.sendBrokenToolcallRelay(agent, b))
	case brokenToolcallActionEscalate:
		h.escalateBrokenToolcall(agent, b)
	case brokenToolcallActionGiveUp:
		h.giveUpBrokenToolcall(agent, b)
	}
}

// brokenToolcallAction is what one reading of an agent's turn asks for.
type brokenToolcallAction int

const (
	brokenToolcallActionNone brokenToolcallAction = iota
	brokenToolcallActionClear
	brokenToolcallActionRelay
	brokenToolcallActionEscalate
	brokenToolcallActionGiveUp
)

// assessBrokenToolcall folds one reading into the agent's case and reports what
// to do about it. The case lock is held for the decision only; the caller acts —
// relay, pause, notify — outside it.
func assessBrokenToolcall(agent storage.Agent, msg opencode.AssistantMessage) (brokenToolcallAction, brokenToolcall) {
	broken, isBroken := detectBrokenToolcall(msg)
	now := time.Now()

	yesloopBrokenToolcallAgentsMu.Lock()
	defer yesloopBrokenToolcallAgentsMu.Unlock()

	st, exists := yesloopBrokenToolcallAgents[agent.ID]
	if !exists || st.createdAt != agent.CreatedAt {
		st = nil
	}

	// A terminal case is held only to keep the same turn from reopening it: the
	// orchestrator already knows (escalation), or was told the messenger failed
	// (give-up). Holding it also means the unreadable window cannot drop it and
	// start the whole cycle over (review round 2 notes 1 and 3). Any other turn
	// ends it — including another broken one, which is a NEW defect and has to be
	// judged on its own: swallowing it left a new broken turn silent while the
	// idle guard took the agent back with the generic text, the overlay YM-196
	// rules out (consequence review, finding 2).
	if st != nil && (st.state == brokenToolcallStateEscalated || st.state == brokenToolcallStateGaveUp) {
		if isSettled(msg) && msg.ID != st.anchor {
			return brokenToolcallActionClear, brokenToolcall{}
		}
		// The anchored turn is held whatever its stamp says: a terminal case exists
		// to keep the same turn from reopening it, and an implausible stamp must not
		// let the case age out and escalate the same turn a second time.
		if isSettled(msg) || msg.ID == st.anchor {
			st.lastSeenAt = now
		}
		return brokenToolcallActionNone, brokenToolcall{}
	}

	// An unsettled turn says nothing yet: the model may still be writing the
	// call. Waiting on it is also what keeps a long turn free of relays (live
	// 2026-10-16: the running turn is listed with finish absent).
	//
	// A turn whose completion stamp no clock can reach is the exception: it is not
	// being written, the stamp is garbage. Renewing lastSeenAt on it would keep the
	// case — and with it the idle guard's suppression — alive for as long as the
	// stamp stays in the future, so the agent would hear from neither layer. Letting
	// the case age out hands it back to the idle guard, whose relay does not depend
	// on the session read.
	if !isSettled(msg) {
		if st != nil && !stampImplausible(msg) {
			st.lastSeenAt = now
		}
		return brokenToolcallActionNone, brokenToolcall{}
	}

	switch {
	case st == nil:
		if !isBroken {
			return brokenToolcallActionNone, brokenToolcall{}
		}
		st = &brokenToolcallState{
			state:      brokenToolcallStatePending,
			anchor:     msg.ID,
			kind:       broken.Kind,
			tool:       broken.Tool,
			detectedAt: now,
			createdAt:  agent.CreatedAt,
		}
		yesloopBrokenToolcallAgents[agent.ID] = st
		log.Printf("[broken-toolcall] agent %s (%s) %s on tool %q (message %s)",
			agent.ID, agent.Section, broken.Kind, broken.Tool, msg.ID)

	case msg.ID != st.anchor && turnExecutedTool(msg):
		// A newer turn ran a tool: the defect is answered, whatever the tool was.
		return brokenToolcallActionClear, brokenToolcall{}

	case msg.ID == st.anchor && !isBroken:
		// The anchored turn is no longer read as broken — the service can settle a
		// turn after the fact, and then there is nothing to relay.
		return brokenToolcallActionClear, brokenToolcall{}
	}

	// The defect stands: either the turn itself is broken, or a turn that neither
	// ran a tool nor is the anchored one followed it. The second case is the live
	// one this layer exists for — the agent answered with bare "null" turns, and
	// closing the case on them would mean the broken call is never mentioned.
	if isBroken && msg.ID != st.anchor {
		st.anchor = msg.ID
		st.kind, st.tool = broken.Kind, broken.Tool
	}
	st.state = brokenToolcallStatePending
	st.lastSeenAt = now

	if now.Sub(st.detectedAt) < brokenToolcallGrace {
		return brokenToolcallActionNone, brokenToolcall{}
	}
	b := brokenToolcall{Kind: st.kind, Tool: st.tool}
	if st.lastRelayAt.IsZero() {
		return brokenToolcallActionRelay, b
	}
	if now.Sub(st.lastRelayAt) < brokenToolcallRefireInterval {
		return brokenToolcallActionNone, b
	}
	if st.relay > brokenToolcallMaxRefires {
		return brokenToolcallActionEscalate, b
	}
	if st.failed >= brokenToolcallMaxDeliveryAttempts {
		// Every attempt is stored for the target's next request, so more of them
		// would only stack copies. The orchestrator is told instead.
		return brokenToolcallActionGiveUp, b
	}
	return brokenToolcallActionRelay, b
}

// detectBrokenToolcall reports the defect of an assistant turn, if it has one.
//
// Two shapes count, both meaning no tool call is running any more:
//   - one of the turn's text parts is a serialized tool call, and the turn
//     stopped (finish=stop) instead of handing the call to the runtime. Every
//     text part is inspected, not just the trailing one: a turn that emits the
//     call and then says something else ("…und dann weiter") would otherwise
//     slip through into the ten-minute idle path (review round 2);
//   - the turn's last part is a tool whose state is "error" — the call was made
//     and failed, and the turn ended on it.
//
// finish is compared against the provider stop reason literally. The live case
// carried "stop"; a provider that reports the same situation under another value
// would be missed, and the value is the one the documented enum names for it.
func detectBrokenToolcall(msg opencode.AssistantMessage) (brokenToolcall, bool) {
	if msg.Type != "assistant" {
		return brokenToolcall{}, false
	}
	if len(msg.Content) == 0 {
		return brokenToolcall{}, false
	}
	if !isSettled(msg) {
		return brokenToolcall{}, false
	}
	last := msg.Content[len(msg.Content)-1]
	if last.Type == "tool" && strings.EqualFold(last.State.Status, "error") {
		return brokenToolcall{Kind: brokenToolcallKindToolError, Tool: sanitizeToolName(last.Name)}, true
	}
	if msg.Finish == "stop" {
		for _, p := range msg.Content {
			if p.Type != "text" {
				continue
			}
			if name, ok := toolCallTextName(p.Text); ok {
				return brokenToolcall{Kind: brokenToolcallKindTextCall, Tool: sanitizeToolName(name)}, true
			}
		}
	}
	return brokenToolcall{}, false
}

// toolNameAllowed is the allowlist a tool name is reduced to before it is used
// anywhere. The name is model output — for a text-shaped call it is a free JSON
// string — so anything the agent read can reach this point (security review,
// finding 1).
func toolNameAllowed(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		return true
	}
	switch r {
	case '_', '.', ':', '/', '-':
		return true
	}
	return false
}

// sanitizeToolName reduces a tool name to a bounded, single-line label.
//
// The name travels into the relay text, into the orchestrator's notice and into
// log lines. Quote-escaping is not enough on either transport: both resolve a
// literal `\n` back into a real line break (send_opencode.go:177 for the API
// push, internal/proxy/think.go for the injection), so a name carrying an escape
// sequence would land as a line of its own in the recipient's context — ahead of
// the directive, reading like a daemon instruction. Only an allowlist that
// cannot produce a line break closes that, and it also bounds the length
// (finding 4: the name was unbounded into logs and the stored message).
// maxToolNameLen bounds the sanitized tool name label.
const maxToolNameLen = 64

func sanitizeToolName(name string) string {
	name = strings.Map(func(r rune) rune {
		if toolNameAllowed(r) {
			return r
		}
		return -1
	}, strings.TrimSpace(name))
	if len(name) > maxToolNameLen {
		name = name[:maxToolNameLen]
	}
	return name
}

// isSettled reports whether a turn ended. finish is empty and time.completed
// unstamped while the model is still writing the turn (verified against a live
// session 2026-10-16). A completion stamp is only taken as evidence when it is
// plausible: the service can report a stamp no clock can reach, and "settled"
// here means "judge this turn now", so trusting it would judge a turn that may
// still be writing. The stop reason is the strong signal and is never overruled.
func isSettled(msg opencode.AssistantMessage) bool {
	if msg.Finish != "" {
		return true
	}
	return msg.Time.Completed != 0 && !stampImplausible(msg)
}

// stampImplausible reports whether the turn carries a completion stamp beyond the
// tolerated clock difference. Such a turn is not a turn in progress: there is
// nothing to wait for, so a case must not be kept alive on it — the suppression
// window would be renewed on every tick and neither this guard nor the idle
// guard would ever speak.
func stampImplausible(msg opencode.AssistantMessage) bool {
	return msg.Time.Completed != 0 && opencode.StampInFuture(msg.Time.Completed, time.Now())
}

// turnExecutedTool reports whether a turn actually ran a tool: any part whose
// state is running or completed. That is the evidence a relay was answered with
// work; prose — however long — is not.
func turnExecutedTool(msg opencode.AssistantMessage) bool {
	for _, p := range msg.Content {
		if p.Type != "tool" {
			continue
		}
		switch strings.ToLower(p.State.Status) {
		case "running", "completed":
			return true
		}
	}
	return false
}

// toolCallTextName reports the tool name when the whole text is a serialized
// tool call.
//
// The bar is deliberately high, because a false positive spends one of the
// agent's turns: the text from its first JSON bracket to the end has to parse,
// every block in it has to be a tool call, each has to carry a name and its
// arguments, and nothing may follow the JSON. A findings line that quotes a
// config snippet, a bare {"name":…} without arguments, or prose after the call
// therefore does not qualify.
func toolCallTextName(text string) (string, bool) {
	s := strings.TrimSpace(text)
	start := strings.IndexAny(s, "[{")
	if start < 0 {
		return "", false
	}
	// Leading narration is allowed ("Ich mache das jetzt.\n[{…}]"): the model
	// emits its call after a sentence often enough.
	s = s[start:]
	var raw any
	if err := json.Unmarshal([]byte(s), &raw); err != nil {
		return "", false
	}
	var blocks []any
	switch v := raw.(type) {
	case []any:
		blocks = v
	case map[string]any:
		blocks = []any{v}
	default:
		return "", false
	}
	if len(blocks) == 0 {
		return "", false
	}
	name := ""
	for _, b := range blocks {
		m, ok := b.(map[string]any)
		if !ok {
			return "", false
		}
		switch strings.ToLower(fmt.Sprint(m["type"])) {
		case "tool_use", "tool_call", "function_call":
		default:
			return "", false
		}
		n := strings.TrimSpace(fmt.Sprint(m["name"]))
		if n == "" || n == "<nil>" {
			return "", false
		}
		if !hasToolArguments(m) {
			return "", false
		}
		if name == "" {
			name = n
		}
	}
	return name, true
}

// hasToolArguments reports whether a block carries the arguments a real call
// always has. The three names cover the providers' block shapes.
func hasToolArguments(m map[string]any) bool {
	for _, key := range []string{"input", "arguments", "parameters"} {
		if v, ok := m[key]; ok && v != nil {
			return true
		}
	}
	return false
}

// buildBrokenToolcallRelay renders the targeted relay: which failure, which
// tool, and what to do instead of nothing. Plain single-line text (no markdown,
// no backticks) — it is delivered as a user turn into the agent's session. The
// closing sentence exists because the command directive invites a reply to the
// sender, and a daemon address has no inbox to receive one.
func buildBrokenToolcallRelay(b brokenToolcall) string {
	tool := b.Tool
	if tool == "" {
		tool = "unknown"
	}
	switch b.Kind {
	case brokenToolcallKindTextCall:
		return fmt.Sprintf("BROKEN TOOL CALL: your last turn ended with finish=stop and carried a serialized tool call (%s) as text, so nothing was executed. The work did not happen. Repeat it now as a REAL tool call. No reply needed.", tool)
	default:
		return fmt.Sprintf("BROKEN TOOL CALL: your last tool call (%s) ended with status=error and the turn stopped. Call the tool again as a REAL tool call, or write the reason into the scratchpad and continue. No reply needed.", tool)
	}
}

// sendBrokenToolcallRelay delivers the relay through the send_to API path, the
// same way an orchestrator reaches a session: it stores the message under the
// agent's canonical address and pushes it into the session's inbox, which also
// wakes an idle session. The PTY inject socket the other guard layers use would
// not reach an agent whose turn died — the terminal is not waiting for input.
//
// The result is true only for a delivery the service confirmed into the inbox. A
// refused or unconfirmed push leaves the message stored for the target's own
// next request, which an agent that stopped making requests will never make:
// counting that as an answer would pause an agent that never heard from the
// guard.
func (h *Handler) sendBrokenToolcallRelay(agent storage.Agent, b brokenToolcall) bool {
	target := storage.AgentCanonicalAddress(&agent)
	if target == "" {
		log.Printf("[broken-toolcall] relay to agent %s skipped: no session address", agent.ID)
		return false
	}
	resp := h.Handle(Request{
		Method: "send_to",
		Params: map[string]any{
			"target":   target,
			"content":  buildBrokenToolcallRelay(b),
			"msg_type": "command",
			"sender":   daemonSender("yesloop"),
		},
	})
	if resp.Error != "" {
		log.Printf("[broken-toolcall] relay to agent %s failed: %s", agent.ID, resp.Error)
		return false
	}
	var out struct {
		DeliveredVia string `json:"delivered_via"`
	}
	if err := json.Unmarshal(resp.Result, &out); err != nil {
		return false
	}
	if out.DeliveredVia != string(deliveryAPI) {
		log.Printf("[broken-toolcall] relay to agent %s stayed on the stored path (%s) — the target must ask for it",
			agent.ID, out.DeliveredVia)
		return false
	}
	return true
}

// recordBrokenToolcallAttempt counts one relay attempt and, when the service
// confirmed it, one delivery. Only deliveries drive the escalation: an agent
// that never received a relay must not be paused for not acting on it.
func (h *Handler) recordBrokenToolcallAttempt(agent storage.Agent, delivered bool) {
	yesloopBrokenToolcallAgentsMu.Lock()
	defer yesloopBrokenToolcallAgentsMu.Unlock()
	st, ok := yesloopBrokenToolcallAgents[agent.ID]
	if !ok || st.createdAt != agent.CreatedAt {
		return
	}
	st.attempts++
	st.lastRelayAt = time.Now()
	if delivered {
		st.relay++
	} else {
		st.failed++
	}
	log.Printf("[broken-toolcall] agent %s (%s) relay attempt %d (delivered=%v, confirmed=%d/%d, failed=%d) for %s on tool %q",
		agent.ID, agent.Section, st.attempts, delivered, st.relay, brokenToolcallMaxRefires+1, st.failed, st.kind, st.tool)
}

// escalateBrokenToolcall nudges the agent once and reports it, after the
// deliveries went unanswered.
func (h *Handler) escalateBrokenToolcall(agent storage.Agent, b brokenToolcall) {
	yesloopBrokenToolcallAgentsMu.Lock()
	st, ok := yesloopBrokenToolcallAgents[agent.ID]
	if !ok || st.createdAt != agent.CreatedAt || st.state != brokenToolcallStatePending {
		yesloopBrokenToolcallAgentsMu.Unlock()
		return
	}
	st.state = brokenToolcallStateEscalated
	st.escalatedAt = time.Now()
	delivered := st.relay
	yesloopBrokenToolcallAgentsMu.Unlock()

	reason := fmt.Sprintf("yesloop-broken-toolcall escalation: %s on tool %q unanswered after %d delivered relays",
		b.Kind, b.Tool, delivered)
	log.Printf("[broken-toolcall] agent %s (%s) ESCALATION: %s", agent.ID, agent.Section, reason)
	if !h.nudgeAgent(agent.ID, reason) {
		skipNudgeNote("broken-toolcall", agent.ID, agent.Section)
		return
	}
	h.notifyOrchestrator(agent, nudgeNotice(fmt.Sprintf(
		"Agent %s (%s) has a broken tool call: %s on tool %q stayed unanswered after %d delivered relays",
		safeAgentText(agent.ID), safeAgentText(agent.Section), b.Kind, b.Tool, delivered)))
}

// giveUpBrokenToolcall stops relaying a finding the API never confirmed, and
// reports that instead of pausing: the agent is not at fault for a messenger
// whose confirmation is missing, and a refused push is still stored, for the
// agent's own next request and for the PTY ping. Calling that "undeliverable"
// was wrong — the confirmation is what is missing, not necessarily the message
// (consequence review, finding 4).
func (h *Handler) giveUpBrokenToolcall(agent storage.Agent, b brokenToolcall) {
	yesloopBrokenToolcallAgentsMu.Lock()
	st, ok := yesloopBrokenToolcallAgents[agent.ID]
	if !ok || st.createdAt != agent.CreatedAt || st.state != brokenToolcallStatePending {
		yesloopBrokenToolcallAgentsMu.Unlock()
		return
	}
	st.state = brokenToolcallStateGaveUp
	attempts := st.attempts
	yesloopBrokenToolcallAgentsMu.Unlock()

	log.Printf("[broken-toolcall] agent %s (%s) GIVE UP: %d relay attempts unconfirmed by the API (%s on tool %q)",
		agent.ID, agent.Section, attempts, b.Kind, b.Tool)
	h.notifyOrchestrator(agent, fmt.Sprintf(
		"BROKEN_TOOLCALL_UNCONFIRMED: Agent %s (%s) — the API did not confirm %d relays for its session; %s on tool %q is still unanswered. The messages wait on the stored path for the session's next request.",
		agent.ID, agent.Section, attempts, b.Kind, b.Tool))
}

// brokenToolcallTracked reports whether an open case exists for this row.
func brokenToolcallTracked(agent storage.Agent) bool {
	yesloopBrokenToolcallAgentsMu.Lock()
	defer yesloopBrokenToolcallAgentsMu.Unlock()
	st, ok := yesloopBrokenToolcallAgents[agent.ID]
	return ok && st.createdAt == agent.CreatedAt && st.state == brokenToolcallStatePending
}

// clearBrokenToolcall drops an agent's case: the turn is no longer broken, so
// the idle guard owns the agent again.
func (h *Handler) clearBrokenToolcall(agent storage.Agent) {
	yesloopBrokenToolcallAgentsMu.Lock()
	defer yesloopBrokenToolcallAgentsMu.Unlock()
	st, ok := yesloopBrokenToolcallAgents[agent.ID]
	if !ok || st.createdAt != agent.CreatedAt {
		return
	}
	delete(yesloopBrokenToolcallAgents, agent.ID)
	log.Printf("[broken-toolcall] agent %s (%s) case closed after %d relay attempts", agent.ID, agent.Section, st.attempts)
}

// pruneBrokenToolcallStates drops cases of vanished rows and cases whose turn
// could not be read for brokenToolcallStaleAfter. Without the second bound a
// permanently unreadable session would keep the idle guard suppressed.
func (h *Handler) pruneBrokenToolcallStates(agents []storage.Agent) {
	live := make(map[string]string, len(agents))
	for i := range agents {
		live[agents[i].ID] = agents[i].CreatedAt
	}
	now := time.Now()
	yesloopBrokenToolcallAgentsMu.Lock()
	defer yesloopBrokenToolcallAgentsMu.Unlock()
	for id, st := range yesloopBrokenToolcallAgents {
		createdAt, ok := live[id]
		if !ok || createdAt != st.createdAt || now.Sub(st.lastSeenAt) >= brokenToolcallStaleAfter {
			delete(yesloopBrokenToolcallAgents, id)
		}
	}
}
