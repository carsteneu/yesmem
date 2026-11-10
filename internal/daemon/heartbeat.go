package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/carsteneu/yesmem/internal/orchestrator"
	"github.com/carsteneu/yesmem/internal/storage"
)

// startAgentHeartbeat runs a goroutine that polls for unread channel messages
// targeting running agent sessions and relays them via inject sockets.
// Also enforces agent limits via freeze (not kill). This enables autonomous
// multi-level agent communication without manual intervention.
func (h *Handler) startAgentHeartbeat(ctx context.Context) {
	if !procTableReadable {
		// Identity cannot be established on this platform, so every consumer that
		// needs a verified own process abstains: the yesloop idle, skillcheck and
		// stagnation guards stay inert and no agent process is signalled. That is
		// the safe direction (nothing is stopped on a guess), but it must not be a
		// silent one — three guards going quiet is an operational fact.
		log.Printf("[heartbeat] process table unreadable — agent process identity cannot be verified: the yesloop idle/skillcheck/stagnation guards and the agent signals stay inert on this platform")
	}
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()
	var tick int
	var doneVerifyTick int
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			tick++
			doneVerifyTick++
			h.relayAgentMessages()
			if tick%5 == 0 {
				h.crashRecovery()
				// Off the ticker: the pass calls a model and runs a corrected
				// script, and relay delivery runs on this same loop.
				h.runBashErrorPass(ctx)
			}
			if tick%30 == 0 {
				h.checkYesloopDoneGuard()
				// After the guard, so a hold this tick placed is already visible:
				// an undecided one escalates on its own clock, agent alive or not.
				h.checkDONEHoldWatchdog()
				// Before the idle guard: the case it may have to yield to has to
				// exist when that guard makes its decision (same tick).
				h.checkYesloopBrokenToolcalls()
				h.checkYesloopIdle()
				h.checkPermissionKick()
				h.checkYesloopSkillCheck()
				h.checkYesloopStagnation()
				h.reapGoneAgents()
				h.reapAgentsInRemovedTrees()
			}
			if doneVerifyTick >= 300 {
				h.checkYesloopDoneVerify()
				doneVerifyTick = 0
			}
			h.enforceAgentLimits()
			h.detectOrphanedAgents()
			h.attemptRestart()
			if tick%150 == 0 {
				h.sendOrchestratorStatusPing()
				h.pruneChannelHistory()
				h.pruneRunDirs()
				tick = 0
			}
		}
	}
}

// relayAgentMessages checks for pending channel messages of running agents and
// pings them via PTY. Content delivery happens through the proxy's channel
// injection (two-stage delivery); the ping only triggers a new API turn.
func (h *Handler) relayAgentMessages() {
	agents, err := h.store.AgentList("")
	if err != nil {
		return
	}

	for _, agent := range agents {
		if agent.Status != "running" || agent.SessionID == "" {
			continue
		}

		// Codex is not pinged. Its mail is matched now — the OpenAI path resolves
		// the codex session from the client process (internal/proxy/
		// session_pid.go) — but there is no proven way to make a running codex
		// TUI take a turn: stop skips the inject socket for codex, and a line
		// written into its PTY would spend a turn as typed input. The message
		// therefore waits for the session's own next request. Proven, not
		// assumed: that is the remaining gap, and this ping is the fix once the
		// inject path is shown to work for codex.
		if agent.Backend == "codex" {
			continue
		}

		msgs, err := h.store.GetUnnotifiedMessages(h.store.ChannelAddresses(agent.SessionID))
		if err != nil || len(msgs) == 0 {
			continue
		}

		injectPath := h.agentInjectPath(agent.ID)
		// Send ONE short ping per agent (not per message) — proxy delivers the actual content
		var conn net.Conn
		if injectPath == "" {
			err = errNoAgentSocket
		} else {
			conn, err = net.DialTimeout("unix", injectPath, 2*time.Second)
		}
		if err != nil {
			for _, m := range msgs {
				retries, _ := h.store.IncrementDeliveryRetry(m.ID)
				if retries >= 5 {
					h.store.MarkDeliveryFailed(m.ID)
					log.Printf("[heartbeat] message %d to agent %s delivery failed after %d retries", m.ID, agent.ID, retries)
					// A daemon sender has no inbox, so it can neither receive the
					// notice nor be re-notified: the row under its address is the
					// last message on that address, which makes IsAckOnAck drop
					// every repeat. Log it instead of addressing a dead channel.
					if isDaemonSender(m.Sender) {
						log.Printf("[heartbeat] undeliverable notice %d for agent %q (section %q) came from %q — logged, not re-sent", m.ID, agent.ID, agent.Section, m.Sender)
						continue
					}
					if resp := h.Handle(Request{
						Method: "send_to",
						Params: map[string]any{
							"target":   m.Sender,
							"content":  fmt.Sprintf("DELIVERY_FAILED: Nachricht an %s konnte nicht zugestellt werden nach %d Versuchen.", agent.Section, retries),
							"msg_type": "status",
							"sender":   daemonSender("heartbeat"),
						},
					}); resp.Error != "" {
						log.Printf("[heartbeat] DELIVERY_FAILED notice for message %d could not be sent to %q: %s", m.ID, m.Sender, resp.Error)
					}
				}
			}
			continue
		}
		senderList := promptLabel(msgs[0].Sender)
		if len(msgs) > 1 {
			senderList = fmt.Sprintf("%s (+%d more)", senderList, len(msgs)-1)
		}
		// The ping is typed into the agent's TUI and submitted with \r, so the
		// sender label above must not carry a line break (YM-255); the message
		// content itself still arrives through the proxy.
		ping := fmt.Sprintf("[New message from %s — content arrives via system prompt]\r", senderList)
		_, writeErr := conn.Write([]byte(ping))
		conn.Close()
		if writeErr != nil {
			continue
		}
		// Mark all messages as notified (read=1) — proxy will set delivered=1
		for _, m := range msgs {
			h.store.MarkMessageNotified(m.ID)
		}

		newCount, _ := h.store.AgentIncrementRelayCount(agent.ID)
		log.Printf("[heartbeat] pinged agent %s (%s) for %d pending messages, total turns: %d", agent.ID, agent.Section, len(msgs), newCount)

		// Enforce max_turns — stop relaying once budget exhausted. The row's own
		// cap wins over the config default: max_turns is a spawn parameter and
		// survives a respawn on the row (YM-244), so the config is only the value
		// for agents that named none.
		maxTurns := agent.MaxTurns
		if maxTurns == 0 {
			maxTurns = h.agentMaxTurns
		}
		if maxTurns == 0 {
			maxTurns = 30
		}
		if newCount >= maxTurns {
			log.Printf("[heartbeat] agent %s reached max_turns (%d), stopping", agent.ID, maxTurns)
			h.stopAgentForLimit(agent.ID, fmt.Sprintf("max_turns %d reached", maxTurns))
		}
	}
}

// pruneChannelHistory bounds the channel tables. They grew without bound —
// 68.723 rows in agent_messages on 2026-10-16, 65.651 of them from the
// scheduler — so the run is logged whenever it removed something.
func (h *Handler) pruneChannelHistory() {
	messages, broadcasts, err := h.store.PruneChannelHistory()
	if err != nil {
		log.Printf("[heartbeat] pruning channel history failed: %v", err)
		return
	}
	if messages > 0 || broadcasts > 0 {
		log.Printf("[heartbeat] pruned %d channel messages and %d read broadcasts past retention", messages, broadcasts)
	}
}

// isPIDAlive returns true if the given PID is a running, non-defunct process.
// Signal 0 is a no-op signal that succeeds only if the process can be signaled.
// It answers "does any process hold this PID" — a zombie and a recycled PID
// both say yes, so callers that need the agent's *own* process must use
// isAgentProcess instead (Learning #101907).
func isPIDAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return p.Signal(syscall.Signal(0)) == nil
}

// procClockTicks is the tick rate /proc reports starttime in — a fixed kernel
// ABI constant, 100 on every mainstream configuration.
const procClockTicks = 100

// agentProcessStartGrace bounds the skew between an agent row's spawn marker and
// the kernel's start time of its process. The row is written first and the
// process is spawned after it, so the process is normally *younger* than its
// marker; the grace only has to absorb clock drift and the seconds a spawn
// takes. bindAgentProcess refreshes the marker on every spawn and resume, which
// is what keeps that relationship true for a resumed agent.
const agentProcessStartGrace = 5 * time.Minute

// procRoot is the process table the identity check reads. Indirected through a
// var so a test can point it at a fixture tree (pattern: var procRootPath in
// internal/setup/mcp_procs.go).
var procRoot = "/proc"

// maxCmdlineBytes bounds the cmdline read: a colliding PID can present a
// multi-megabyte argv and only the leading arguments can carry the binary name
// (same bound and rationale as internal/setup/mcp_procs.go, Learning #101573).
const maxCmdlineBytes = 8 << 10

// procCmdline reads the argv of a process from the process table. Indirected
// through a var so tests can present a cmdline for a PID they cannot spawn under
// the agent's backend binary name (same seam pattern as var procPPID in
// internal/terminals/placement.go).
//
// Tests in this package must not run in parallel: the seams are package vars.
var procCmdline = readProcCmdline

// readProcCmdline returns the NUL-joined argv of a process. It is empty for a
// zombie (the kernel blanks it once the process is reaped-pending) and for any
// PID whose entry cannot be read.
func readProcCmdline(pid int) string {
	f, err := os.Open(fmt.Sprintf("%s/%d/cmdline", procRoot, pid))
	if err != nil {
		return ""
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxCmdlineBytes))
	if err != nil {
		return ""
	}
	return string(data)
}

// procTableReadable reports whether this platform exposes the process table the
// identity check needs. Determined once from our own PID. Where it is false
// (macOS) isAgentProcess cannot establish identity at all, so the consumers that
// act on a *negative* answer must abstain rather than read that as "the process
// is gone" — see agentProcessGone. (The probe also covers the boot-time read, so
// a container that masks /proc/stat while exposing /proc/<pid>/* counts as
// unreadable too; treating it as readable would silently drop the start-time
// conjunct.)
var procTableReadable = readProcStartTimeUsable(os.Getpid())

func readProcStartTimeUsable(pid int) bool {
	_, ok := readProcStartTime(pid)
	return ok
}

// procStartTime returns the wall-clock start time of a process, derived from
// the starttime field of the process table plus the boot time. ok is false when
// either read fails, which also marks the platform as unable to answer the
// identity question (see procTableReadable). Indirected through a var so a test
// can date a process it cannot actually have started hours ago (a genuinely
// long-running agent is exactly the case C4 must not reap).
var procStartTime = readProcStartTime

func readProcStartTime(pid int) (time.Time, bool) {
	data, err := os.ReadFile(fmt.Sprintf("%s/%d/stat", procRoot, pid))
	if err != nil {
		return time.Time{}, false
	}
	// The comm field may contain spaces and parentheses; every field the caller
	// needs sits after the LAST ')'.
	s := string(data)
	i := strings.LastIndex(s, ")")
	if i < 0 {
		return time.Time{}, false
	}
	fields := strings.Fields(s[i+1:]) // field 3 onwards
	if len(fields) < 20 {
		return time.Time{}, false
	}
	startTicks, err := strconv.Atoi(fields[19]) // field 22: starttime, in clock ticks
	if err != nil {
		return time.Time{}, false
	}
	btime, ok := procBootTime()
	if !ok {
		return time.Time{}, false
	}
	return time.Unix(btime+int64(startTicks/procClockTicks), 0), true
}

// procBootTime reads btime — the boot time in seconds — from the process table.
func procBootTime() (int64, bool) {
	data, err := os.ReadFile(procRoot + "/stat")
	if err != nil {
		return 0, false
	}
	for _, line := range strings.Split(string(data), "\n") {
		value, found := strings.CutPrefix(line, "btime ")
		if !found {
			continue
		}
		secs, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
		if err != nil {
			return 0, false
		}
		return secs, true
	}
	return 0, false
}

// agentBinaryName maps the stored backend to the binary name that must appear in
// its cmdline. It is the inverse of spawnAgentProcess's backend switch: only
// opencode and codex have their own binary, everything else (including the empty
// column and any unrecognised value) is spawned as claude. Returning the backend
// verbatim here would leave a typo'd row permanently unverifiable — the row would
// name a binary its process never runs.
func agentBinaryName(backend string) string {
	switch backend {
	case "opencode":
		return "opencode"
	case "codex":
		return "codex"
	default:
		return "claude"
	}
}

// agentProcessStartRef returns the timestamp the agent's process must not
// postdate: the newest known spawn marker. bindAgentProcess writes
// last_restart_at together with the pid, so that column dates the running
// process; created_at is the fallback for rows written before that invariant
// existed (it is the spawn time for a row that was never resumed).
func agentProcessStartRef(a storage.Agent) (time.Time, bool) {
	if t, ok := parseAgentCreatedAt(a.LastRestartAt); ok {
		return t, true
	}
	return parseAgentCreatedAt(a.CreatedAt)
}

// isAgentProcess reports whether a.PID belongs to the agent's own backend
// process. isPIDAlive alone answers "does any process hold this PID" — a zombie
// and a recycled PID both say yes (Learning #101907), which made dead paused
// rows look live and let stop_agent signal bystanders. Three conjuncts:
//
//  1. signal 0 succeeds,
//  2. the process did not start after the agent's spawn marker (+ grace). Every
//     process is spawned after its row, so a start time well past the marker
//     means the PID was recycled onto an unrelated process,
//  3. its cmdline carries the backend binary's name. This catches the mirrored
//     case — a process that started before the row and now holds its PID — plus
//     zombies, whose cmdline the kernel blanks.
//
// Conjunct 2 depends on the spawn marker being written whenever a process is
// bound to the row (bindAgentProcess); without that, a resumed agent's row would
// predate its process and the agent would never verify.
//
// A PID whose process-table entries cannot be read is NOT verified — a caller
// about to signal it must treat "cannot inspect" as "not ours". A consumer that
// instead acts on a *negative* answer must use agentProcessGone, which abstains
// where the platform cannot answer.
func isAgentProcess(a storage.Agent) bool {
	if !isPIDAlive(a.PID) {
		return false
	}
	if !procTableReadable {
		return false
	}
	if ref, ok := agentProcessStartRef(a); ok {
		start, ok := procStartTime(a.PID)
		if !ok || start.After(ref.Add(agentProcessStartGrace)) {
			return false
		}
	}
	return strings.Contains(procCmdline(a.PID), agentBinaryName(a.Backend))
}

// agentProcessGone reports whether the agent's process is definitively gone.
// This is deliberately not the negation of isAgentProcess: where identity cannot
// be established (a platform without a process table, or a table the kernel
// refuses to serve), isAgentProcess answers false while the process may well be
// alive. Only "nobody holds this PID any more" and "our process is gone and
// somebody else holds the PID" justify closing a row — the consumers that act on
// a negative answer (reapGoneAgents, the DONE-guard skip) must abstain on
// "cannot verify", while the consumers that act on a positive answer (signals)
// must abstain on it too. One boolean cannot serve both directions.
func agentProcessGone(a storage.Agent) bool {
	if !isPIDAlive(a.PID) {
		return true
	}
	if !procTableReadable {
		return false
	}
	return !isAgentProcess(a)
}

// newAgentBridge creates the PTY bridge that runs an agent's backend process and
// owns the socket the console client connects to. Indirected through a var so a
// test can drive the spawn/restart path with a harmless child instead of a real
// LLM CLI — the bind call site was untestable without it, which is how a missing
// spawn marker reached review in the first place.
var newAgentBridge = orchestrator.NewAgentBridge

// agentDisplayCommand builds the terminal command that shows an agent's console.
// Indirected for the same reason: a test must not open a real terminal window.
var agentDisplayCommand = func(terminal, binPath, title string, innerArgs ...string) (string, []string) {
	return orchestrator.BuildSpawnCommand(terminal, binPath, title, innerArgs...)
}

// pidDecidedlyDead reports whether a PID can no longer be running the agent's
// process: nobody holds the number any more, or what holds it is a zombie — a
// process that has already exited and cannot be signalled back to life. A
// consumer about to act irreversibly on a death (crashRecovery's quarantine and
// its replacement process) needs this proof: a *live* PID that merely fails to
// identify as this agent may be a recycled number or a mislabelled live agent,
// and from the row alone the two cannot be told apart (Learning #101907).
func pidDecidedlyDead(pid int) bool {
	return !isPIDAlive(pid) || isProcessZombie(strconv.Itoa(pid))
}

// bindAgentProcess records the process a spawn or resume produced. The pid and
// the spawn marker are written together on purpose: isAgentProcess dates a
// process against that marker, so a row that gains a pid without refreshing it
// would reject its own process forever — and every caller of isAgentProcess
// would then treat a live agent as gone (C6 would finalize it "stopped" without
// signalling, C2/C3 would stop watching it, C4 would reap it).
//
// The write is conditional on the row still claiming to be live (see
// AgentBindProcessIfLive). The relaunch runs in a goroutine, and a stop_agent
// landing in that window must win: the report lets the caller tear down a process
// no row owns any more.
// daemonOwnsTheBind is the ownerSession a daemon-side spawn passes to
// bindAgentProcess: the daemon created that process itself, so there is no caller
// capability to present. A named constant so the empty value cannot be mistaken
// for a forgotten argument (cold review 5.4).
const daemonOwnsTheBind = ""

func (h *Handler) bindAgentProcess(id string, pid int, sockPath, ownerSession string) (bool, error) {
	// A pid is only ever recorded while a process holds it. register_agent takes
	// the pid from its caller, so without this gate a row could claim a process
	// that already exited — the row would read "running" with a pid nobody owns,
	// and every consumer of agents.pid would start from a false premise. Where
	// the platform cannot answer, the bind proceeds as before: the crash
	// recovery still owns that case.
	if !pidHeldByAProcess(pid) {
		log.Printf("[agent_bind] agent %s: refusing to bind PID %d — no process holds it", safeAgentText(id), pid)
		return false, nil
	}
	// Holding a PID says nothing about whose process it is. The daemon's own
	// spawns bind the child they just created and pass no ownerSession; every
	// other caller has to prove it holds the row's session id (see the store).
	if reason := h.bindIdentityRefusal(id, pid); reason != "" {
		log.Printf("[agent_bind] agent %s: refusing to bind PID %d — %s", safeAgentText(id), pid, reason)
		return false, nil
	}
	return h.store.AgentBindProcessIfLive(id, pid, sockPath, time.Now().Format("2006-01-02 15:04:05"), ownerSession)
}

// bindIdentityRefusal names why a PID cannot be this row's backend process, or
// returns "" when it can. Two conjuncts: a PID the daemon may not signal is not
// one of its children (pidHeldByAProcess answers existence, deliberately — EPERM
// there means "somebody holds it"), and the command line has to name the row's
// backend binary. A platform without a process table cannot answer the second
// one, so it keeps the previous behaviour rather than refusing every bind.
func (h *Handler) bindIdentityRefusal(id string, pid int) string {
	if err := syscall.Kill(pid, 0); errors.Is(err, syscall.EPERM) {
		return "the daemon may not signal it, so it is not its backend process"
	}
	if !procTableReadable {
		return ""
	}
	row, err := h.store.AgentGet(id)
	if err != nil || row == nil || row.Backend == "" {
		// No row to compare against: the store's own live gate decides, and the
		// spawn path binds a row it just wrote. Logged because this is the only
		// path on which the identity conjunct is skipped silently.
		log.Printf("[agent_bind] agent %s: identity not checkable (row read failed or no backend) — bind relies on the live-row gate", safeAgentText(id))
		return ""
	}
	want := agentBinaryName(row.Backend)
	if !cmdlineNamesBinary(procCmdline(pid), want) {
		return fmt.Sprintf("its command line does not name the %s backend", row.Backend)
	}
	return ""
}

// cmdlineNamesBinary reports whether one argv element of a process is named like
// the backend binary. Exact base names, not a substring: "claude-notes.md" or an
// argument that merely mentions the backend would satisfy a Contains check, and a
// caller chooses the backend name it wants to match (cold review 5.4). Per
// element, because codex runs as `node .../bin/codex`.
func cmdlineNamesBinary(cmdline, want string) bool {
	if want == "" {
		return false
	}
	for _, arg := range strings.Split(cmdline, "\x00") {
		arg = strings.TrimSpace(arg)
		if arg == "" {
			continue
		}
		if filepath.Base(arg) == want {
			return true
		}
	}
	return false
}

// pidHeldByAProcess reports whether any process holds the PID, whether or not this
// daemon may signal it. isPIDAlive answers "no" for EPERM, and that is the wrong
// answer for a bind decision: a foreign process does hold the number. A zombie
// counts as not held — it has exited and only awaits its reaper.
func pidHeldByAProcess(pid int) bool {
	if pid <= 0 {
		return false
	}
	if err := syscall.Kill(pid, 0); err != nil {
		return errors.Is(err, syscall.EPERM)
	}
	return !isProcessZombie(strconv.Itoa(pid))
}

// crashRecovery checks running agents for dead processes, quarantines their
// sessions, taints scratchpad sections, and retries up to 3 times before marking
// failed. Consolidates the former detectDeadPIDs + agentHealthCheck logic.
func (h *Handler) crashRecovery() {
	agents, err := h.store.AgentList("")
	if err != nil {
		return
	}
	for _, a := range agents {
		// A crashed row is a marker this function writes right before it starts the
		// replacement, so the recovery below never sees it. A row that is still
		// crashed long afterwards has no replacement coming — the daemon restarted
		// in between, or the respawn's own spawn failed — and nothing else in the
		// supervision looks at crashed rows.
		// A claim is the same kind of marker: it exists only between two steps of one
		// replacement, so a row that still holds it long afterwards lost its claimer
		// (a daemon death in that window). Without this branch the claim would take the
		// owner away from the abandoned-crash closer and leave the row unwatched
		// (cold review Los 56h).
		if a.Status == "crashed" || a.Status == "respawn_claimed" {
			h.closeAbandonedCrash(a)
			continue
		}
		if a.Status != "running" {
			continue
		}
		// No PID guard on purpose: a running row with no bound process (PID 0) is
		// a crash like any other — no process owns the row, and agentProcessGone
		// answers "gone" for it. The former skip left exactly those rows to
		// detectHungAgents, which every row with a PID never reached (YM-69).
		// agentProcessGone, not isPIDAlive: a zombie or a recycled PID answers
		// signal 0, which hid a crashed agent forever (the row stayed "running"
		// with no recovery). A PID that identifies as the agent's own process —
		// or one the platform cannot inspect — is left alone.
		if !agentProcessGone(a) {
			continue
		}

		now := time.Now().Format(time.RFC3339)

		// Recovery's irreversible parts — session quarantine, scratchpad taint,
		// socket removal and a replacement process — are only safe on a decided
		// death: the PID is free, or it holds a zombie. A live PID that merely
		// fails to identify as this agent is ambiguous: the number may have been
		// recycled onto a stranger (the agent is gone), or the row's identity data
		// may disagree with an agent that is still working. The row must still
		// stop claiming to be running, but destroying its session data or starting
		// a second process for its section would be wrong in the second case, and
		// the two cannot be told apart from the row.
		if !pidDecidedlyDead(a.PID) {
			log.Printf("[heartbeat] agent %s PID %d is alive but does not identify as this agent — closing the row without recovery", a.ID, a.PID)
			h.store.AgentUpdate(a.ID, map[string]any{
				"status":     "failed",
				"stopped_at": now,
				"error":      "pid is held by another process; recovery withheld — this row's process identity could not be established",
			})
			// A silent close would look like an ordinary failure: the row is not
			// stoppable once failed and no replacement was started, so the caller
			// has to know why.
			if a.CallerSession != "" {
				h.Handle(Request{Method: "send_to", Params: map[string]any{
					"target": a.CallerSession,
					"content": fmt.Sprintf("AGENT_UNVERIFIED: agent %s (section %s) was closed as failed without recovery — PID %d is held by another process, so its own process could not be established. Nothing was quarantined and no replacement was started; restart it explicitly if it was meant to run.",
						a.ID, a.Section, a.PID),
					"msg_type": "status",
					"sender":   daemonSender("heartbeat"),
				}})
			}
			continue
		}
		// The claim is the ownership decision for everything below: quarantine,
		// taint and replacement are irreversible, and the snapshot this loop reads
		// is stale the moment it is taken — a stop (single or bulk) can close the
		// row between the read and this branch. A re-read would narrow that window
		// but not close it; the conditional write makes exactly one actor: whoever
		// still sees a running row with this PID wins (YM-356, orchestrator review).
		claimed, claimErr := h.store.AgentClaimCrash(a.ID, a.PID, now,
			fmt.Sprintf("crash #%d — respawning", a.RetryCount+1))
		if claimErr != nil {
			log.Printf("[heartbeat] agent %s: crash claim failed: %v", safeAgentText(a.ID), claimErr)
			continue
		}
		if !claimed {
			log.Printf("[heartbeat] agent %s: row moved since the crash snapshot — another writer owns it, no recovery", safeAgentText(a.ID))
			continue
		}
		log.Printf("[heartbeat] agent %s PID %d is gone, starting crash recovery", a.ID, a.PID)

		if a.SockPath != "" {
			// The column only says that this row ever bound a socket; which file
			// to unlink is derived from the id (YM-287, see agentSockPath).
			h.removeAgentSocketFiles(a.ID)
		}
		if a.SessionID != "" || a.OpencodeSessionID != "" || a.CodexSessionID != "" {
			if n, qErr := h.quarantineAgentLearnings(a); qErr != nil {
				log.Printf("[heartbeat] quarantining the learnings of crashed agent %s failed: %v", a.ID, qErr)
			} else if n > 0 {
				log.Printf("[heartbeat] quarantined %d learnings from crashed agent %s", n, a.ID)
			}
		}
		if a.Section != "" && a.Project != "" {
			taintNote := fmt.Sprintf("[TAINTED — Agent %s crashed at %s. Data may be incomplete.]", a.ID, now)
			for _, prefix := range []string{"ergebnis-", ""} {
				sectionName := prefix + a.Section
				if sections, sErr := h.store.ScratchpadRead(a.Project, sectionName); sErr == nil && len(sections) > 0 {
					h.store.ScratchpadWrite(a.Project, sectionName, taintNote+"\n\n"+sections[0].Content, sections[0].Owner)
				}
			}
		}
		if a.RetryCount < 3 {
			log.Printf("[heartbeat] agent %s crashed (retry %d/3), respawning", a.ID, a.RetryCount+1)
			// Status, stop time, reason and the retry counter were written by the
			// claim above — repeating them here would be a second, unconditional
			// writer on the same columns.
			go h.respawnAgent(a)
			continue
		}
		h.reportFinalCrash(a)
	}
}

// reportFinalCrash closes an exhausted crash row and reports the final failure.
//
// The close is a compare-and-set (AgentCloseIfCrashed): a stop or a resume that
// landed between the crash claim and here owns the row, and an unconditional
// write would overwrite that decision and announce a final crash for a row
// somebody ended deliberately (cold review, heartbeat.go:688). Nothing is
// reported unless the close was ours. Reports whether the row was closed.
func (h *Handler) reportFinalCrash(a storage.Agent) bool {
	runtime := crashRuntime(a.CreatedAt)
	// Both messages below leave the daemon (orchestrator send_to, log), and
	// every part of them is agent- or caller-influenced: the id and section
	// come from the spawn request, the snippet is raw scratchpad. Neutralized
	// at the sink, and the snippet cut on a rune boundary (YM-285).
	context := fmt.Sprintf("FAILED: Agent '%s' (section '%s') crashed after %d attempts. Runtime: %s.",
		safeAgentText(a.ID), safeAgentText(a.Section), a.RetryCount+1, runtime)
	escalation := ""
	if sections, err := h.store.ScratchpadRead(a.Project, a.Section); err == nil && len(sections) > 0 {
		fullContent := sections[0].Content
		sp := truncate(safeAgentText(fullContent), 200)
		context += fmt.Sprintf(" Letzter Status: %s", sp)

		// Dead-agent-detection for yesloop agents: check if agent had partial
		// phase work but never completed the last phase → structured GATE-ESCALATION.
		if strings.HasPrefix(a.Section, "yesloop-") {
			result := ValidatePhaseBlocks(fullContent)
			completed := CountCompletedPhases(fullContent)
			if completed > 0 && !result.Compliant {
				escalation = fmt.Sprintf(
					"DEAD_AGENT: Agent %s (%s) terminated without completing Phase %d.\n  Runtime: %s\n  Completed: %d/%d phases\n  Missing phases: %v\n  Field errors: %s",
					safeAgentText(a.ID), safeAgentText(a.Section), donePhaseCount, runtime, completed, donePhaseCount,
					result.MissingPhases, summarizeErrors(result),
				)
			}
		}
	}
	// The close decides whether this is a final crash at all. A stop that lands
	// between the crash claim and here owns the row (a resume cannot move a
	// crashed row: handleResumeAgent refuses that status); writing "failed" over it
	// (and announcing a final crash) would resurrect exactly the incident the claim
	// closed (cold review, heartbeat.go:688).
	note := "3 retries exhausted, final crash" + staleHeartbeatNote(a)
	closed, err := h.store.AgentCloseIfCrashed(a.ID, note)
	if err != nil {
		// A write error is not a refusal (SQLITE_BUSY under concurrent writers):
		// retried once, like the bind path, so the final-failure report is not lost
		// and the row does not surface later as an abandoned crash with the wrong
		// reason (cold review 5.4 LOW).
		log.Printf("[heartbeat] closing the exhausted agent %s failed: %v (retrying once)", safeAgentText(a.ID), err)
		closed, err = h.store.AgentCloseIfCrashed(a.ID, note)
		if err != nil {
			log.Printf("[heartbeat] closing the exhausted agent %s failed again: %v", safeAgentText(a.ID), err)
			return false
		}
	}
	if !closed {
		log.Printf("[heartbeat] agent %s: row moved since the claim (a stop owns it now) — no final-failure report", safeAgentText(a.ID))
		return false
	}
	if escalation != "" {
		log.Printf("[heartbeat] %s", escalation)
		if a.CallerSession != "" {
			h.Handle(Request{
				Method: "send_to",
				Params: map[string]any{
					"target":   a.CallerSession,
					"content":  escalation,
					"msg_type": "status",
					"sender":   daemonSender("heartbeat"),
				},
			})
		}
	}
	if a.CallerSession != "" {
		h.Handle(Request{Method: "send_to", Params: map[string]any{"target": a.CallerSession, "content": context, "msg_type": "status", "sender": daemonSender("heartbeat")}})
	}
	return true
}

// quarantineAgentLearnings quarantines the agent's learnings under every session
// address they can carry. Quarantining a.SessionID alone hit none of them for a
// daemon-spawned backend agent: their rows are keyed by the address the proxy
// reported ("opencode:<ses>", or the bare id), never by the synthetic daemon UUID
// the row carries (YM-63).
func (h *Handler) quarantineAgentLearnings(a storage.Agent) (int64, error) {
	var total int64
	for _, key := range storage.LearningSessionKeys(&a) {
		// The agent's project scopes the quarantine: the same session address in
		// another project is not this agent's row to retire (YM-244).
		n, err := h.store.QuarantineSessionInProject(a.Project, key)
		if err != nil {
			return total, err
		}
		total += n
	}
	return total, nil
}

// staleHeartbeatNote dates the last heartbeat for a final crash message. A stale
// heartbeat alone never proved a hang (a long tool call blocks it while the agent
// works, Learning #73191), but it is the one fact the former hung-agent guard
// contributed and an operator reading a failure wants it.
func staleHeartbeatNote(a storage.Agent) string {
	t, ok := parseAgentCreatedAt(a.HeartbeatAt)
	if !ok {
		return ""
	}
	return fmt.Sprintf("; last heartbeat %s ago", time.Since(t).Round(time.Second))
}

// abandonedCrashGrace bounds how long a crashed row may wait for its replacement.
// crashRecovery writes "crashed" and respawnAgent replaces the row two seconds
// later, so a row still crashed after this long has no replacement coming.
const abandonedCrashGrace = 5 * time.Minute

// closeAbandonedCrash closes a crashed row whose replacement never started. The
// four rows found on 2026-10-17 (oldest from 2026-04-28) were exactly this: not
// running, not finished, and invisible to every other supervision path.
func (h *Handler) closeAbandonedCrash(a storage.Agent) {
	ref, ok := parseAgentCreatedAt(a.StoppedAt)
	if !ok {
		ref, ok = newestAgentStamp(a)
	}
	if !ok || time.Since(ref) <= abandonedCrashGrace {
		return
	}
	// The row is closed either way, but the notice must not claim the section is
	// empty when a later row took it over: a replacement that ran and finished
	// inside the grace period is just as much a successor as a live one.
	successor := h.sectionSuccessor(a)
	log.Printf("[heartbeat] agent %q (%q) has been crashed for %s with no replacement started, closing the row",
		a.ID, a.Section, time.Since(ref).Round(time.Minute))
	note := fmt.Sprintf("crash recovery did not start a replacement within %v — closed", abandonedCrashGrace)
	// A successor that runs or finished means the section is not abandoned; one
	// that failed or was stopped leaves it idle, and then the notice is accurate.
	if successor != nil {
		note += fmt.Sprintf("; the section is owned by %s (%s)", successor.ID, successor.Status)
	}
	supervised := successor != nil && (successor.Status == "finished" || agentStatusIsLive(successor.Status))
	// A stop_agent landing between the snapshot and this write wins: the row was
	// closed deliberately, and "failed" would reopen the question why.
	written, err := h.store.AgentCloseIfUnsupervised(a.ID, note+". Last error: "+a.Error)
	if err != nil {
		log.Printf("[heartbeat] closing abandoned agent %q failed: %v", a.ID, err)
		return
	}
	if !written {
		log.Printf("[heartbeat] agent %q left the crashed state while it was being closed — nothing to close", a.ID)
		return
	}
	if supervised || a.CallerSession == "" {
		return
	}
	h.Handle(Request{Method: "send_to", Params: map[string]any{
		"target": a.CallerSession,
		"content": fmt.Sprintf("AGENT_ABANDONED: agent %q (section %q) stayed crashed for %s and no replacement started, so the row is closed as failed. Nothing is running for that section; restart it explicitly if it was meant to work.",
			a.ID, a.Section, time.Since(ref).Round(time.Minute)),
		"msg_type": "status",
		"sender":   daemonSender("heartbeat"),
	}})
}

const livenessPingGrace = 5 * time.Minute

// agentStatusIsLive reports whether a status means a process may still own the
// row. "crashed" is deliberately not live: it is the marker of a row whose process
// is gone and whose replacement has not started yet.
func agentStatusIsLive(status string) bool {
	switch status {
	case "running", "pending", "spawning", "paused", "frozen":
		return true
	}
	return false
}

// sectionSuccessor returns the newest other row of the same section that was
// created after the given one — a respawn's replacement, whether it is still
// running or already finished. A crashed row whose section has such a row was not
// abandoned, and the caller must not be told to restart that section.
func (h *Handler) sectionSuccessor(a storage.Agent) *storage.Agent {
	if a.Project == "" || a.Section == "" {
		return nil
	}
	rows, err := h.store.AgentList(a.Project)
	if err != nil {
		return nil
	}
	ref, haveRef := parseAgentCreatedAt(a.CreatedAt)
	var newest *storage.Agent
	var newestAt time.Time
	for i := range rows {
		c := &rows[i]
		if c.ID == a.ID || c.Section != a.Section {
			continue
		}
		at, ok := parseAgentCreatedAt(c.CreatedAt)
		if haveRef && ok && !at.After(ref) {
			continue
		}
		if newest == nil || (ok && (!haveRef || at.After(newestAt))) {
			newest, newestAt = c, at
		}
	}
	return newest
}

// detectOrphanedAgents finds running agents whose parent agent has died and
// stops them after a 5-minute grace period.
func (h *Handler) detectOrphanedAgents() {
	agents, err := h.store.AgentList("")
	if err != nil {
		return
	}
	for _, agent := range agents {
		if agent.Status != "running" || agent.CallerSession == "" {
			continue
		}
		// Note: agents with status "paused" are intentionally excluded.
		// A paused parent is alive, not dead — its children should not be orphaned.
		parent, err := h.store.AgentGetAnyBySession(agent.CallerSession)
		if err != nil || parent == nil {
			continue // user session or lookup error — skip
		}
		// A finished or crashed parent is as gone as a stopped one: its children
		// have nobody left to report to (YM-72).
		parentDead := parent.Status == "stopped" || parent.Status == "error" ||
			parent.Status == "failed" || parent.Status == "finished" || parent.Status == "crashed"
		if !parentDead {
			if agent.LivenessPingAt != "" {
				h.store.AgentUpdate(agent.ID, map[string]any{"liveness_ping_at": ""})
			}
			continue
		}
		if agent.LivenessPingAt == "" {
			log.Printf("[heartbeat] orphan check: agent %s parent %s dead, starting 5min grace", agent.ID, parent.ID)
			h.store.AgentUpdate(agent.ID, map[string]any{
				"liveness_ping_at": time.Now().Format("2006-01-02 15:04:05"),
			})
			continue
		}
		pingAt, err := time.ParseInLocation("2006-01-02 15:04:05", agent.LivenessPingAt, time.Local)
		if err != nil || time.Since(pingAt) < livenessPingGrace {
			continue
		}
		log.Printf("[heartbeat] agent %s orphaned (parent %s dead for >%v), stopping", agent.ID, parent.ID, livenessPingGrace)
		// Through the ordinary stop path, not a row write: its verified process is
		// ended and its own descendants are cascaded and ended with it. A row-only
		// stop left the process working with no relay and no limit (YM-66). No
		// post-signal verification: this runs inside the supervision tick and reads
		// only the error (see stopVerificationSkipped).
		if resp := h.Handle(Request{Method: "stop_agent", Params: map[string]any{"to": agent.ID, "_verify": false}}); resp.Error != "" {
			log.Printf("[heartbeat] orphan stop for %q refused: %s", agent.ID, resp.Error)
			continue
		}
		h.store.AgentUpdate(agent.ID, map[string]any{
			"progress": fmt.Sprintf("orphaned: parent %s dead", parent.ID),
		})
	}
}

// enforceAgentLimits checks running agents against configured limits and stops violators.
func (h *Handler) enforceAgentLimits() {
	agents, err := h.store.AgentList("")
	if err != nil {
		return
	}

	maxRuntime := h.getAgentMaxRuntime()

	for _, agent := range agents {
		if agent.Status != "running" {
			continue
		}

		// Check runtime limit — but only stop if the process is actually gone.
		// A live process means the agent is working past its configured budget
		// (e.g. long refactors); the hung-agent detector handles truly stuck
		// processes via heartbeat_at staleness. See learning #73505.
		//
		// agentProcessGone, not isPIDAlive: a zombie or a recycled PID answers
		// signal 0, which kept an over-budget row "running" forever. This site
		// declares a death, so it needs a proven one (Learning #101907). A live
		// process keeps the row running: the agent is working past its budget, and
		// the token budget is the limit that ends it (not a heartbeat heuristic —
		// a stale heartbeat alone proves nothing, Learning #73191).
		created, ok := parseAgentCreatedAt(agent.CreatedAt)
		if ok && time.Since(created) > maxRuntime {
			if !agentProcessGone(agent) {
				continue
			}
			log.Printf("[heartbeat] agent %s exceeded max_runtime (%v) and PID %d holds no process of this agent, stopping", agent.ID, maxRuntime, agent.PID)
			h.stopAgent(agent.ID, fmt.Sprintf("max_runtime %v exceeded, no verified process (PID %d)", maxRuntime, agent.PID))
			continue
		}

		// Check the token budget. Its unit is output tokens: input_tokens is
		// cumulative and includes cache reads (a live YesLoop reaches 50 to 130
		// million at 300 to 750 turns), so a budget in that unit counted the
		// context that was re-sent, not the work the agent produced (YM-254).
		if agent.TokenBudget > 0 {
			used := agent.OutputTokens
			if used >= agent.TokenBudget {
				log.Printf("[heartbeat] agent %s exceeded token_budget (%d output tokens/%d), stopping", agent.ID, used, agent.TokenBudget)
				h.stopAgentForLimit(agent.ID, fmt.Sprintf("token_budget %d exceeded (used %d output tokens)", agent.TokenBudget, used))
			}
		}
	}
}

// stopAgent marks an agent as permanently stopped — process is dead or the
// supervisor has given up (max_runtime + dead PID, token_budget exhausted,
// max_restarts exceeded). Orchestrator must respawn to make progress.
func (h *Handler) stopAgent(id, reason string) {
	h.store.AgentUpdate(id, map[string]any{
		"status":     "stopped",
		"stopped_at": time.Now().Format("2006-01-02 15:04:05"),
		"progress":   "stopped: " + reason,
	})
}

// stopAgentForLimit ends an agent that hit a configured limit. The row write
// alone left its process running with no supervisor watching it: the limit was
// the last owner the row had, so a DB-only stop produced a live agent nobody
// could see or reach (YM-66). stop_agent carries the verified-signal path and
// tears the row down under a PID compare-and-set.
func (h *Handler) stopAgentForLimit(id, reason string) {
	// verify:false — this runs inside the supervision tick and reads only the
	// error; the post-signal verification (see stopVerificationSkipped) belongs to
	// callers that read the answer.
	resp := h.Handle(Request{Method: "stop_agent", Params: map[string]any{"to": id, "_verify": false}})
	if resp.Error != "" {
		// Not closed here: the refusal means the row is moving under this decision
		// (a successor was bound), and an unconditional write would mark a live
		// process stopped. The limit is still exceeded on the next tick.
		log.Printf("[heartbeat] stop_agent for %q refused (%s): %s", id, reason, resp.Error)
		return
	}
	// stop_agent writes the bare "stopped", or a note when it sent no signal; the
	// limit that ended the agent is what an operator needs on top of that.
	a, err := h.store.AgentGet(id)
	if err != nil || a == nil || strings.Contains(a.Progress, reason) {
		return
	}
	progress := a.Progress
	if progress == "" {
		progress = "stopped"
	}
	h.store.AgentUpdate(id, map[string]any{"progress": progress + ": " + reason})
}

// pauseAgent marks an agent as waiting — process is alive, PTY bridge still
// open, but supervisor needs orchestrator input (idle escalation, done-verify
// pending). Orchestrator should relay_agent to nudge it forward.
func (h *Handler) pauseAgent(id, reason string) {
	h.store.AgentUpdate(id, map[string]any{
		"status":   "paused",
		"progress": "paused: " + reason,
	})
}

// unpauseAgent mirrors pauseAgent: flips status back to running with a
// recovery note. Pure DB flip, no process spawn — used after proven recovery
// (done-guard compliant scratchpad, perm-kick revived the stream).
func (h *Handler) unpauseAgent(id, source, reason string) {
	h.store.AgentUpdate(id, map[string]any{
		"status": "running",
		"progress": fmt.Sprintf("recovered by %s at %s: %s",
			source, time.Now().Format("2006-01-02 15:04:05"), reason),
	})
}

// getAgentMaxRuntime returns the configured max runtime for agents.
func (h *Handler) getAgentMaxRuntime() time.Duration {
	if h.agentMaxRuntime > 0 {
		return h.agentMaxRuntime
	}
	return 48 * time.Hour
}

// attemptRestart checks running agents with a restart strategy and relaunches
// dead processes via a new terminal bridge using --resume.
func (h *Handler) attemptRestart() {
	agents, err := h.store.AgentList("")
	if err != nil {
		return
	}
	for _, agent := range agents {
		if agent.Status != "error" {
			continue
		}
		if agent.RestartStrategy == "" || agent.RestartStrategy == "temporary" {
			continue
		}

		// GAP2: 30s race-condition guard
		if lastRestart, ok := parseAgentCreatedAt(agent.LastRestartAt); ok {
			if time.Since(lastRestart) < 30*time.Second {
				continue
			}
		}

		// agent failed during spawn (bridge or terminal error in handler_agents.go)
		// "permanent" restarts indefinitely — only "transient" is capped by MaxRestarts
		if agent.RestartStrategy != "permanent" && agent.RestartCount >= agent.MaxRestarts {
			log.Printf("[heartbeat] agent %s max_restarts %d exceeded, stopping", agent.ID, agent.MaxRestarts)
			h.stopAgent(agent.ID, fmt.Sprintf("max_restarts %d exceeded", agent.MaxRestarts))
			continue
		}

		// The restart, its stale-socket cleanup and the bridge all unlink what
		// they are handed, so the path is derived from the id here and never
		// taken from the caller-writable column (YM-287). The column is no
		// precondition any more: a row without one still has an id (YM-338).
		sockPath := h.agentSockPath(agent.ID)
		if sockPath == "" {
			// No path to bind, and falling back to the column would hand the bridge
			// a caller-chosen file to unlink. Close the row the way the exhausted
			// retry budget does, so it is not logged again on every tick (YM-287).
			log.Printf("[heartbeat] agent %s has no derivable socket path, stopping instead of restarting", safeAgentText(agent.ID))
			h.stopAgent(agent.ID, "socket path could not be derived from the agent id")
			continue
		}
		agent.SockPath = sockPath

		// L4: skip opencode agents whose OpencodeSessionID was never captured.
		// --resume with the daemon UUID makes opencode exit 1 (Learning #80228).
		// Before giving up, try to bind the session late: capture can lose the
		// race at spawn (the TUI needs ~15s) or the daemon can have restarted in
		// between, and a resumed-vs-stopped agent is the difference between a
		// recoverable loop and a lost one.
		if agent.Backend == "opencode" && agent.OpencodeSessionID == "" {
			if sid := h.bindOpencodeSession(agent); sid != "" {
				agent.OpencodeSessionID = sid
			} else {
				log.Printf("[heartbeat] agent %s has no opencode_session_id, skipping restart (would fall back to daemon UUID)", agent.ID)
				h.stopAgent(agent.ID, opencodeUnresumableMsg)
				continue
			}
		}
		if h.disableAgentProcesses {
			continue
		}

		log.Printf("[heartbeat] restarting agent %s (strategy=%s, attempt %d/%d)", agent.ID, agent.RestartStrategy, agent.RestartCount+1, agent.MaxRestarts)

		// GAP3: remove stale sockets — derived from the id, never from the
		// caller-writable column (YM-287).
		h.removeAgentSocketFiles(agent.ID)

		// Relaunch through the same code a spawn uses. It builds the PTY bridge
		// in-process — only the bridge owner can know the backend PID — and binds
		// the row to it via bindAgentProcess, then opens the display console.
		// This path used to launch `yesmem agent-tty ... --resume` directly,
		// which is a console client: it dials a socket that nobody was serving,
		// so no backend was started at all, and the PID recorded here was the
		// terminal emulator's. resume=true reuses the stored session and skips
		// the initial prompt injection.
		now := time.Now().Format("2006-01-02 15:04:05")
		h.store.AgentUpdate(agent.ID, map[string]any{
			"restart_count": agent.RestartCount + 1,
			// "spawning", not "running": the row has no process yet, and since
			// crashRecovery closes running rows whose PID does not identify as the
			// agent's, a tick landing in this window would quarantine the session
			// and spawn a second agent for the section. Every tick consumer skips
			// spawning rows; the bind flips the row to running.
			"status":          "spawning",
			"last_restart_at": now,
		})
		// The relaunch runs in its own goroutine: the work-dir lookup below is a
		// (bounded but up to 5s) DB query, and this window should stay as short as
		// a stop racing it can find it.
		//
		// If the daemon dies between this write and the bind, the row stays
		// "spawning": every guard skips it and the section stays owned by it, so
		// nothing retries it automatically — stop_agent accepts a spawning row as
		// live (finalizing it without a signal), so the orchestrator can close it
		// by hand.
		go h.restartAgentProcess(agent)

		// GAP4: notify parent via inject socket (best-effort)
		if agent.CallerSession != "" {
			parent, err := h.store.AgentGetAnyBySession(agent.CallerSession)
			if err == nil && parent != nil && parent.Status == "running" {
				parentInject := h.agentInjectPath(parent.ID)
				if parentInject != "" {
					conn, err := net.DialTimeout("unix", parentInject, 2*time.Second)
					if err == nil {
						msg := fmt.Sprintf("[SYSTEM] agent %s (%s) restarted (attempt %d/%d)\r", agent.ID, agent.Section, agent.RestartCount+1, agent.MaxRestarts)
						conn.Write([]byte(msg))
						conn.Close()
					}
				}
			}
		}
	}
}

// agentRestartWorkDir resolves the directory a replacement must run in and
// reports why it must not start at all when the row's tree is gone.
//
// The two answers are different cases and must not be collapsed: ("", "") means
// "the row never recorded a directory" — the caller falls back the way a fresh
// spawn does — while a non-empty refusal means the row did record one and it is
// gone. The second case is the incident of 18.10.2026: a yesloop worktree is
// removed after its branch is merged, the row survived as crashed, and the
// replacement came up in the daemon's run dir without a repository and redid the
// phases of a finished task.
//
// A backend session still knows where it ran, so its directory is accepted as the
// replacement for a vanished tree; only when that is unavailable too is the
// restart refused.
func (h *Handler) agentRestartWorkDir(a storage.Agent) (string, string) {
	if dir := h.existingWorkDir(a.WorkDir); dir != "" {
		return dir, ""
	}
	if a.Backend == "opencode" && a.OpencodeSessionID != "" {
		if dir := opencodeSessionDirFn(a.OpencodeSessionID); dir != "" {
			if info, err := os.Stat(dir); err == nil && info.IsDir() {
				return dir, ""
			}
		}
	}
	if strings.TrimSpace(a.WorkDir) != "" {
		return "", fmt.Sprintf("recorded working directory %q is gone and the backend session names no replacement", a.WorkDir)
	}
	return "", ""
}

// opencodeSessionDirFn is the session-directory lookup the restart gate uses.
// Variable so a test can drive the "the tree is gone but the session knows where
// it ran" branch without an opencode database (the pattern every other seam in
// this package follows).
var opencodeSessionDirFn = opencodeSessionDir

// restartAgentProcess relaunches an agent's backend through the spawn path, which
// binds the row to the process it creates. The work dir comes from the row: a loop
// agent ran in its worktree, and deriving the dir from agents.project lands on the
// repository root, where the agent's own skill guard fails. A vanished work dir is
// NOT resolved on demand since YM-351: the backend session may still name the tree
// it ran in, and when it does not, the restart is refused and the row closed with
// the reason instead of starting a backend outside any repository.
func (h *Handler) restartAgentProcess(agent storage.Agent) {
	workDir, refusal := h.agentRestartWorkDir(agent)
	if refusal != "" {
		// Same refusal as the respawn: a tree that no longer exists is no
		// workplace, and the run-dir fallback below would start the backend
		// outside any repository.
		log.Printf("[heartbeat] restart of %s refused: %s", safeAgentText(agent.ID), refusal)
		h.closeRefusedRestart(agent, refusal)
		return
	}
	workDir = h.resolveAgentWorkDir(agent.Project, workDir)
	if workDir == "" {
		// The daemon's own cwd is not the agent's project (YM-286). Restart in the
		// daemon-owned run dir and say so, rather than in the user's home.
		if dir, err := EnsureRunWorkDir(h.dataDir, agent.ID); err == nil {
			log.Printf("[heartbeat] agent %s: project %q has no work dir, restarting in %s", agent.ID, agent.Project, dir)
			workDir = dir
		} else {
			log.Printf("[heartbeat] agent %s: no working directory at all, not restarting: %v", agent.ID, err)
			h.stopAgent(agent.ID, fmt.Sprintf("project %q has no working directory", agent.Project))
			return
		}
	}
	if workDir != agent.WorkDir {
		// The row has to name the directory its process really got, or a later bind
		// cannot tell this agent's sessions apart (YM-286).
		if err := h.store.AgentSetWorkDir(agent.ID, workDir); err != nil {
			log.Printf("[heartbeat] agent %s: record work dir: %v", agent.ID, err)
		}
	}
	// Model and theme travel with the row for the same reason as the work dir:
	// they are spawn parameters, and a restart that drops them changes the agent
	// the operator asked for (YM-64).
	// The socket path is derived from the id for the same reason as in the crash
	// path: the bridge unlinks what it is handed, and the column is
	// caller-writable — or empty, which would bind an unreachable abstract socket
	// (YM-287, YM-328).
	sockPath := h.agentSockPath(agent.ID)
	if sockPath == "" {
		log.Printf("[heartbeat] agent %s: no derivable socket path, not restarting", safeAgentText(agent.ID))
		h.stopAgent(agent.ID, "socket path could not be derived from the agent id")
		return
	}
	// The replacement window belongs on the desktop the agent was started from.
	// The row carries only the caller session, so the placement sources are
	// resolved from it the way the resume routes resolve the caller's — without
	// them the window opened on whatever desktop was focused (YM-458).
	parentPid, parentWindow := h.callerPlacement(agent.CallerSession)
	h.spawnAgentProcess(agent.ID, agent.SessionID, agent.Project, agent.Section, "", sockPath, workDir, agent.Backend, agent.Model, agent.Theme, agent.MaxTurns, parentPid, parentWindow, true)
}

// killAgent terminates an agent process and removes it from the DB.
// The signal is gated on the same identity check as every other signal in this
// file, so the invariant holds even if this becomes reachable (it has no caller
// today).
func (h *Handler) killAgent(agent *storage.Agent) {
	if isAgentProcess(*agent) {
		syscall.Kill(agent.PID, syscall.SIGTERM)
	}
	h.store.AgentDelete(agent.ID)
	if agent.SockPath != "" {
		// Same derivation as every other cleanup site: the column only says that
		// this row ever bound a socket (YM-287).
		h.removeAgentSocketFiles(agent.ID)
	}
}

// crashRuntime calculates how long an agent ran before crashing.
func crashRuntime(createdAt string) string {
	t, err := time.Parse("2006-01-02 15:04:05", createdAt)
	if err != nil {
		t, err = time.Parse(time.RFC3339, createdAt)
	}
	if err != nil {
		return "unbekannt"
	}
	d := time.Since(t)
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	return fmt.Sprintf("%dm%ds", int(d.Minutes()), int(d.Seconds())%60)
}

// respawnRestartStrategy bounds the policy a replacement inherits. spawn_agent
// accepts only bounded policies (the uncapped "permanent" is a supervisor
// decision on the row, not a spawn parameter), so a row that carries it comes
// back with the bounded equivalent instead of failing to spawn at all.
func respawnRestartStrategy(strategy string) string {
	if strategy == "permanent" {
		return "transient"
	}
	return strategy
}

// closeRefusedRestart closes a row whose replacement was refused and tells its
// caller. It goes through finalizeStop rather than a bare progress write: the stop
// contract clears the pid (a "stopped" row must not name a process any more, see
// AgentFinalizeStop) and compares it, so a bind that landed in between is not
// erased. The notice matters as much as the write: every other close in this file
// tells the caller, and an orchestrator waiting on a loop must not learn about a
// refused restart only by polling the row.
func (h *Handler) closeRefusedRestart(a storage.Agent, refusal string) {
	written, err := h.finalizeStop(a, "restart refused: "+refusal)
	if err != nil {
		log.Printf("[heartbeat] agent %s: closing the refused restart failed: %v", safeAgentText(a.ID), err)
		return
	}
	if !written {
		log.Printf("[heartbeat] agent %s: the row moved while the refused restart was being closed — left as it is", safeAgentText(a.ID))
		return
	}
	if a.CallerSession == "" {
		return
	}
	h.Handle(Request{Method: "send_to", Params: map[string]any{
		"target": a.CallerSession,
		"content": fmt.Sprintf("AGENT_NOT_RESTARTED: agent %q (section %q) was closed instead of restarted — %s. Nothing runs for that section; start it again with an explicit work dir if the task is still open.",
			safeAgentText(a.ID), safeAgentText(a.Section), refusal),
		"msg_type": "status",
		"sender":   daemonSender("heartbeat"),
	}})
}

// respawnAgent creates a new agent process with the same config as the crashed one.
// respawnClaimStale is when a claim that nobody completed may be taken again: the
// claiming daemon died between the claim and the replacement.
const respawnClaimStale = 60 * time.Second

// agentStampFormat is the timestamp layout the agents table carries.
const agentStampFormat = "2006-01-02 15:04:05"

// claimRespawnFor takes the crashed row for one replacement attempt. A refused claim is
// the honest answer: a stop closed the row, or another pass already owns it.
func (h *Handler) claimRespawnFor(id string) bool {
	now := time.Now()
	claimed, err := h.store.AgentClaimRespawn(id,
		"respawn in flight: a replacement is being started",
		now.Format(agentStampFormat),
		now.Add(-respawnClaimStale).Format(agentStampFormat))
	if err != nil {
		log.Printf("[heartbeat] respawn claim of %s failed: %v", safeAgentText(id), err)
		return false
	}
	if !claimed {
		log.Printf("[heartbeat] respawn of %s not claimed: the row left the crashed state", safeAgentText(id))
	}
	return claimed
}

// releaseRespawnClaim returns the row to crashed after a failed spawn, so the next
// recovery pass can try again.
func (h *Handler) releaseRespawnClaim(id, reason string) bool {
	released, err := h.store.AgentReleaseRespawn(id, "crashed: replacement not started ("+reason+")")
	if err != nil {
		log.Printf("[heartbeat] respawn release of %s failed: %v", safeAgentText(id), err)
		return false
	}
	return released
}

// finishRespawn decides about the replacement the spawn already created. The claim is
// the token: while it is still there, nothing else touched the row and the replacement
// is the successor. Once it is gone, a stop won the race and the replacement must not
// outlive it — the stop is the operator's decision, and a row that was told to stop
// must not be replaced (YM-397).
func (h *Handler) finishRespawn(crashedID, replacementID string) bool {
	closed, err := h.store.AgentCloseReplaced(crashedID, replacementID, time.Now().Format(agentStampFormat))
	if err != nil {
		log.Printf("[heartbeat] closing the replaced row %s failed: %v", safeAgentText(crashedID), err)
	}
	if closed {
		return true
	}
	log.Printf("[heartbeat] respawn of %s was revoked by a stop — ending the replacement %s",
		safeAgentText(crashedID), safeAgentText(replacementID))
	if resp := h.Handle(Request{Method: "stop_agent", Params: map[string]any{
		"to":      replacementID,
		"_verify": false,
	}}); resp.Error != "" {
		log.Printf("[heartbeat] stopping the revoked replacement %s failed: %s", safeAgentText(replacementID), resp.Error)
	}
	return false
}

func (h *Handler) respawnAgent(crashed storage.Agent) {
	time.Sleep(2 * time.Second) // Brief cooldown before retry
	// The agent may have been stopped during the cooldown; stop means "do not
	// bring it back", so a replacement must not be spawned anyway.
	if current, err := h.store.AgentGet(crashed.ID); err != nil || current == nil || current.Status != "crashed" {
		log.Printf("[heartbeat] respawn of %s skipped: no longer crashed", crashed.ID)
		return
	}
	workDir, refusal := h.agentRestartWorkDir(crashed)
	if refusal != "" {
		// The tree this agent worked in is gone. A replacement would come up in
		// the daemon's run dir, without a repository, and redo the phases of a
		// task that was finished (its worktree is removed after the merge) —
		// observed 18.10.2026. Closing the row is the honest answer: nothing is
		// running there and nothing should be started.
		log.Printf("[heartbeat] respawn of %s refused: %s", safeAgentText(crashed.ID), refusal)
		h.closeRefusedRestart(crashed, refusal)
		return
	}
	if workDir == "" {
		// The recorded tree is gone (or was never there). The daemon-owned run dir
		// keeps the replacement out of the user's home; the project resolver alone
		// would leave it no directory at all (YM-286).
		if dir, err := EnsureRunWorkDir(h.dataDir, crashed.ID); err == nil {
			log.Printf("[heartbeat] respawn %s: no work dir on the row, using %s", crashed.ID, dir)
			workDir = dir
		}
	}
	// The claim is the token the stop path can revoke: without it, a stop that lands
	// between the status read above and the spawn is overtaken by the replacement and
	// nobody reaches the new row (YM-397).
	if !h.claimRespawnFor(crashed.ID) {
		return
	}
	resp := h.Handle(Request{
		Method: "spawn_agent",
		Params: map[string]any{
			"project":        crashed.Project,
			"section":        crashed.Section,
			"caller_session": crashed.CallerSession,
			// The replacement must come back where the crashed agent ran: the
			// project resolver alone lands on the repository root, which for a loop
			// agent is the wrong tree (its own skill guard fails there).
			"work_dir": workDir,
			// Backend, model and theme are spawn parameters too. Without them the
			// replacement started with the default backend and the daemon's default
			// model — an opencode agent with an explicit model came back as a
			// claude agent (YM-64).
			"backend": crashed.Backend,
			"model":   crashed.Model,
			"theme":   crashed.Theme,
			// The restart policy travels with the row for the same reason: a
			// replacement that silently drops it is a different agent (YM-68).
			"restart_strategy": respawnRestartStrategy(crashed.RestartStrategy),
			"max_restarts":     crashed.MaxRestarts,
			// The budget is a spawn parameter as well: a replacement that comes back
			// without it runs unbounded, and the limits are the only cap the agent
			// has (YM-66). It travels as a float64 because spawn_agent reads its
			// numbers with a float64 type assertion — an int here is silently
			// dropped, and the replacement came back on the default (YM-67).
			"token_budget": float64(crashed.TokenBudget),
			// The turn cap is the second limit the row owns. Without it a respawn
			// silently changed the agent's own limit to the config default (YM-244).
			"max_turns": float64(crashed.MaxTurns),
		},
	})
	if resp.Error != "" {
		log.Printf("[heartbeat] respawn failed for %s: %s", crashed.ID, resp.Error)
		h.releaseRespawnClaim(crashed.ID, resp.Error)
	} else {
		var result map[string]any
		if err := json.Unmarshal(resp.Result, &result); err == nil {
			// handleSpawnAgent returns "id"; reading "agent_id" here made the
			// assertion below fail silently, so the replacement never inherited
			// retry_count and the "respawned X as Y" line never appeared.
			if newID, ok := result["id"].(string); ok {
				if !h.finishRespawn(crashed.ID, newID) {
					return
				}
				log.Printf("[heartbeat] respawned %s as %s", crashed.ID, newID)
				h.store.AgentUpdate(newID, map[string]any{
					"retry_count": crashed.RetryCount + 1,
				})
			} else {
				h.releaseRespawnClaim(crashed.ID, "spawn returned no agent id")
			}
		} else {
			h.releaseRespawnClaim(crashed.ID, "spawn result unreadable")
		}
	}
}

// goneAgentReapAfter is how long a paused agent's process must have been gone
// before the row is closed (decided 2026-10-16: one day).
const goneAgentReapAfter = 24 * time.Hour

// loopSectionPrefix marks the yesloop worktree sections: their row's work_dir is a
// worktree the orchestrator removes once the branch is merged.
const loopSectionPrefix = "yesloop-"

// reapAgentsInRemovedTrees ends loop agents whose tree is gone. A worktree is removed
// after its branch is merged, and a process that keeps running there works in a
// directory that no longer exists: it writes to no repository, nobody relays to it and
// no limit watches it (18 such processes on 20.10.2026, YM-385). The stop goes through
// the ordinary path, so the process is signalled and its own children cascade.
func (h *Handler) reapAgentsInRemovedTrees() {
	agents, err := h.store.AgentList("")
	if err != nil {
		return
	}
	for _, agent := range agents {
		reason := h.removedTreeReason(agent)
		if reason == "" {
			continue
		}
		log.Printf("[heartbeat] loop agent %s (section %q, pid %d) %s — stopping it",
			safeAgentText(agent.ID), safeAgentText(agent.Section), agent.PID, reason)
		// No post-signal verification: this runs inside the supervision tick and reads
		// only the error (see stopVerificationSkipped).
		if resp := h.Handle(Request{Method: "stop_agent", Params: map[string]any{
			"to":      agent.ID,
			"_verify": false,
		}}); resp.Error != "" {
			log.Printf("[heartbeat] stop of loop agent %q refused: %s", agent.ID, resp.Error)
			continue
		}
		// The stop writes its own reason; the tree is what an operator needs on top.
		h.store.AgentUpdate(agent.ID, map[string]any{
			"progress": "stopped: " + reason,
		})
	}
}

// removedTreeReason reports why a loop agent must not keep running, or "" when its tree
// is still in place. Two observations count, both about the tree and not about the
// section name: the recorded worktree is gone from disk, or the process's own cwd was
// deleted under it. An agent without a recorded tree is left alone: its project
// directory is the user's, and nothing here can tell that it was removed.
//
// The cwd probe is gated on the process identity, the same rule every signal in this file
// follows: a recycled PID with a deleted cwd would otherwise end a stranger's process
// (cold review Los 56h). The work-dir branch needs no PID and no signal of its own.
//
// A paused loop row is stopped here rather than after reapGoneAgents' one-day grace. That
// grace exists to keep a resumable row resumable; a row whose tree is gone cannot be
// resumed into anything, so waiting a day only leaves the lie on record.
func (h *Handler) removedTreeReason(agent storage.Agent) string {
	if !strings.HasPrefix(agent.Section, loopSectionPrefix) || !agentStatusIsLive(agent.Status) {
		return ""
	}
	if agent.WorkDir != "" {
		info, err := os.Stat(agent.WorkDir)
		switch {
		case err != nil && errors.Is(err, fs.ErrNotExist):
			return fmt.Sprintf("works in the removed tree %q", agent.WorkDir)
		case err == nil && !info.IsDir():
			return fmt.Sprintf("works in %q, which is no directory any more", agent.WorkDir)
		}
		// Any other error (a permission change, a transient IO error) says nothing about
		// the tree and must not end a running agent.
	}
	if agent.PID > 0 && isAgentProcess(agent) && processCwdDeleted(agent.PID) {
		return fmt.Sprintf("works with a deleted working directory (pid %d)", agent.PID)
	}
	return ""
}

// processCwdDeleted reports whether the process's working directory was removed. The
// kernel marks the link target with " (deleted)"; a pid without a readable link (gone,
// or another user's process) is not provable here and stays out.
func processCwdDeleted(pid int) bool {
	target, err := os.Readlink(fmt.Sprintf("/proc/%d/cwd", pid))
	if err != nil {
		return false
	}
	return strings.HasSuffix(target, " (deleted)")
}

// reapGoneAgents closes paused agents whose process has been gone for more than
// goneAgentReapAfter. Their PID can never come back, so the row would otherwise
// stay paused forever and keep the paused-row guards pointing at a process that
// does not exist (C4). Resume stays possible: handleResumeAgent accepts stopped.
//
// "Gone for 24h" is measured from the newest lifecycle stamp, not from
// created_at — a row created two days ago that still reported activity an hour
// ago has not been gone for a day.
func (h *Handler) reapGoneAgents() {
	agents, err := h.store.AgentList("")
	if err != nil {
		return
	}
	now := time.Now()
	for _, agent := range agents {
		if agent.Status != "paused" || !agentProcessGone(agent) {
			continue
		}
		ref, ok := newestAgentStamp(agent)
		if !ok || now.Sub(ref) <= goneAgentReapAfter {
			continue
		}
		log.Printf("[heartbeat] agent %s (%s) paused with no process for %s, auto-stopping",
			agent.ID, agent.Section, now.Sub(ref).Round(time.Minute))
		// The row is closed the same way every other stop path closes one: the
		// stale pid and socket belong to a process that is gone, and the row
		// must not advertise them. Socket files are left to the worktree/socket
		// cleanup — this task stays DB-only.
		h.store.AgentUpdate(agent.ID, map[string]any{
			"status":     "stopped",
			"pid":        0,
			"sock_path":  "",
			"stopped_at": now.Format(time.RFC3339),
			"progress":   "auto-stopped: process gone",
		})
	}
}

// newestAgentStamp returns the most recent lifecycle stamp of an agent row.
// ok is false when none of them parses — an undatable row is left to the
// orchestrator rather than reaped on a guess.
func newestAgentStamp(a storage.Agent) (time.Time, bool) {
	var newest time.Time
	found := false
	for _, s := range []string{a.LastActivityAt, a.HeartbeatAt, a.LastRestartAt, a.CreatedAt} {
		t, ok := parseAgentCreatedAt(s)
		if !ok {
			continue
		}
		if !found || t.After(newest) {
			newest, found = t, true
		}
	}
	return newest, found
}

// sendOrchestratorStatusPing sends a status summary to orchestrator agents every 5 minutes.
// An orchestrator is an agent whose session_id is referenced as caller_session by other agents.
func (h *Handler) sendOrchestratorStatusPing() {
	agents, err := h.store.AgentList("")
	if err != nil || len(agents) == 0 {
		return
	}

	// Index running agents by session_id
	bySession := map[string]*storage.Agent{}
	for i := range agents {
		if agents[i].SessionID != "" && agents[i].Status == "running" {
			bySession[agents[i].SessionID] = &agents[i]
		}
	}

	// Group children by their orchestrator (caller_session → parent)
	type projectInfo struct {
		orchestrator *storage.Agent
		children     []storage.Agent
	}
	projects := map[string]*projectInfo{}
	for i := range agents {
		a := &agents[i]
		if a.CallerSession == "" {
			continue
		}
		parent, ok := bySession[a.CallerSession]
		if !ok {
			continue
		}
		key := parent.ID
		if projects[key] == nil {
			projects[key] = &projectInfo{orchestrator: parent}
		}
		projects[key].children = append(projects[key].children, *a)
	}

	for _, info := range projects {
		if info.orchestrator == nil || len(info.children) == 0 {
			continue
		}
		running, done, failed := 0, 0, 0
		for _, c := range info.children {
			switch c.Status {
			case "running", "spawning":
				running++
			case "finished":
				done++
			case "failed", "crashed", "error":
				failed++
			}
		}
		msg := fmt.Sprintf("STATUS_PING [%s]: %d running, %d done, %d failed (von %d total)", info.orchestrator.Project, running, done, failed, len(info.children))
		h.Handle(Request{
			Method: "send_to",
			Params: map[string]any{
				"target":   info.orchestrator.SessionID,
				"content":  msg,
				"msg_type": "status",
				"sender":   daemonSender("heartbeat"),
			},
		})
		log.Printf("[heartbeat] status ping → %s: %s", info.orchestrator.ID, msg)
	}
}

// checkYesloopDoneGuard validates yesloop agent scratchpad sections for
// DONE compliance. If an agent claims all 7 phases COMPLETE but the phase
// blocks fail validation, the per-agent refire state machine
// (yesloop_done_guard.go) relays the missing field to the agent and only
// pauses after doneGuardMaxRefires attempts. Runs every ~30s from
// startAgentHeartbeat.
func (h *Handler) checkYesloopDoneGuard() {
	agents, err := h.store.AgentList("")
	if err != nil {
		return
	}
	h.pruneDoneGuardStates(agents)
	h.pruneDoneGateVerifyStates(agents)
	for _, agent := range agents {
		if agent.Status != "running" && agent.Status != "paused" {
			continue
		}
		// Paused agents: only revalidate those paused by DONE-GUARD.
		// Idle and DoneVerify pauses use different progress prefixes
		// ("paused: yesloop-idle escalation:" / "paused: yesloop-done-verify
		// escalation:") and must not be touched by this guard. A row held by the
		// verification stage is the exception: its in-memory state is lost on a
		// daemon restart, and without re-entering the stage such a row would stay
		// paused with no verdict and nobody to ask for one.
		if agent.Status == "paused" &&
			!strings.HasPrefix(agent.Progress, "paused: DONE-GUARD:") &&
			!strings.HasPrefix(agent.Progress, "paused: DONE-PENDING-VERIFY:") &&
			!strings.HasPrefix(agent.Progress, "paused: DONE-VERIFY-BLOCKED") {
			continue
		}
		// A paused agent whose process is definitively gone has nothing left to
		// fix, and the refire state is in-memory: without this gate every daemon
		// restart re-ran the whole relay/refire/escalation cycle for the paused
		// rows of long-dead agents. Running agents stay out of this check —
		// crashRecovery owns their dead-process transition.
		if agent.Status == "paused" && agentProcessGone(agent) {
			continue
		}
		// Only check yesloop agents (section starts with "yesloop-")
		if !strings.HasPrefix(agent.Section, "yesloop-") {
			continue
		}
		if agent.Project == "" || agent.Section == "" {
			continue
		}
		sections, err := h.store.ScratchpadRead(agent.Project, agent.Section)
		if err != nil || len(sections) == 0 {
			continue
		}
		content := sections[0].Content
		if content == "" {
			continue
		}

		result := ValidatePhaseBlocks(content)

		// The guard judges a claim, not a draft. A pipeline whose blocks are
		// still being written is in progress: it has neither the completion of
		// the last block nor a blocker to report, and relaying the 20-odd
		// missing fields of a fresh skeleton interrupted agents during
		// legitimate work (#81984).
		//
		// A loop that started under the six-block contract counts as a claim:
		// every block it wrote is COMPLETE and the seventh is missing, which is
		// exactly the bounce this contract owes it.
		if !claimsPipelineEnd(content) && !lastPhaseBlocked(content) {
			continue
		}

		// Delegate the refire/pause decision to the state machine in
		// yesloop_done_guard.go.
		h.checkOneDoneGuardAgent(agent, result)
	}
}

// phaseName is the contract name of one phase, e.g. 6 -> DAEMON-VERIFY. The
// number alone is not actionable for a reader who has to write the block.
func phaseName(number int) string {
	for _, pv := range phaseValidations {
		if pv.Number == number {
			return pv.Name
		}
	}
	return ""
}

// summarizeErrors produces a compact one-line summary of validation errors.
// Evidence errors carry their reason in Detail — truncated to keep the relay
// actionable but one-line. Detail is neutralized a second time here (the
// producers sanitize their own values) because this string is the one every
// relay and notification path embeds, and truncate() keeps the cut on a rune
// boundary instead of splitting a multi-byte character (YM-285).
func summarizeErrors(r ValidationResult) string {
	if r.Compliant {
		return "compliant"
	}
	var parts []string
	if len(r.MissingPhases) > 0 {
		// The name belongs beside the number: a loop that started under the
		// six-block contract has to be told that the seventh block is the one it
		// is missing (#phase 6 DAEMON-VERIFY, phase 7 FINISH), not merely that a
		// number is absent.
		missing := make([]string, 0, len(r.MissingPhases))
		for _, number := range r.MissingPhases {
			missing = append(missing, fmt.Sprintf("%d %s", number, phaseName(number)))
		}
		parts = append(parts, "missing phases: "+strings.Join(missing, ", "))
	}
	for _, fe := range r.FieldErrors {
		part := fmt.Sprintf("Phase%d:%s", fe.Phase, fe.Field)
		if fe.Detail != "" {
			part += ": " + truncate(safeAgentText(fe.Detail), 140)
		}
		parts = append(parts, part)
	}
	if len(parts) == 0 {
		return "compliant"
	}
	return strings.Join(parts, ", ")
}
