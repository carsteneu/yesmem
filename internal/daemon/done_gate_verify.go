package daemon

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/carsteneu/yesmem/internal/config"
	"github.com/carsteneu/yesmem/internal/storage"
	"github.com/carsteneu/yesmem/internal/verify"
	"github.com/carsteneu/yesmem/internal/verify/engine"
)

// The DONE gate as a consumer of the verification.
//
// The gate keeps no checking logic: it reads the report the agent wrote, looks up
// the verdict a verify_claims run recorded for it, and acts on that verdict.
// "Acting" is the only thing that belongs here, because only the gate knows the
// agent row, its caller and the pause/unpause lever.

// The gate's verdict states for one agent. Pending is the state while a pre-check
// for the report is still running and the verdict is still to come.
const (
	doneVerifyPending  = "pending"
	doneVerifyAccepted = "accepted"
	doneVerifyBlocked  = "blocked"
)

// doneGateVerifyState is the gate's memory of one agent's verification.
//
// It is bound to a report fingerprint, not to the agent alone: an agent that
// corrects its report must be verified again, and the same report must not be
// verified twice.
type doneGateVerifyState struct {
	Fingerprint string
	Status      string
	Kind        string
	Reason      string
	Findings    []verifyClaimFinding
	// Worktree is the state of the inspected tree's uncommitted residue when the
	// verdict was reached. It travels with the head and the branch so a later
	// block can say that residue appeared instead of claiming nothing was checked.
	Worktree string
	// ReportEmpty records that the section beside the pipeline carried no report
	// text when the presence check read it. Like NoClaims it sends the message to
	// the shape: the agent cannot correct a report it never wrote.
	ReportEmpty bool
	// NoClaims records that the section carried text but no claim the verifier
	// reads. The presence check blocks on it before a run starts, and the message
	// prints the shape for the same reason: a report without a claim is corrected
	// by copying a form, not by reading a rule.
	NoClaims bool
	// ProfileInitHint records that the block is the one an init answers: a test
	// claim came back as an environment gap and the repository has no project
	// profile. The message then names the command that creates one. It is a hint,
	// not a verdict: the class stays environment, and nothing runs an init.
	ProfileInitHint bool
	// Bound and Consumed are the limit that applied and what it used: the wait
	// when the run never got its turn, the budget when it ran. They are kept for
	// the limit message, whose whole content is those two numbers.
	Bound    time.Duration
	Consumed time.Duration
	// Notified is the signature of the last verdict the agent and the
	// orchestrator were told about. It travels across a retry of the same report,
	// because a retry is the machine's business: the readers already heard the
	// verdict, and repeating it made the same pending/blocked pair arrive every
	// fifteen minutes (2026-11-02). The map is in memory, so a daemon restart
	// forgets the mark and announces the hold once more; that is the intended
	// re-entry, not a repeat.
	Notified string
	// Deliveries counts the writes this state cost the agent. A socket that does
	// not come up must cost a handful of attempts, not one per tick for the life
	// of the row.
	Deliveries int
	// BlockedRounds counts the consecutive blocked verdicts this report collected,
	// and RoundNotes keeps one short line per round for the escalation message: a
	// number alone would tell the reader that three corrections failed without
	// telling what failed. After doneVerifyEscalateAfter the gate stops asking the
	// agent: the same correction failed three times, and the fourth decision is not
	// the agent's to make.
	BlockedRounds int
	RoundNotes    []string
	Escalated     bool
	// The parts of the key are kept beside the digest, because a block has to
	// name what moved: a bare key mismatch would tell the reader that the verdict
	// no longer applies without telling which fact changed.
	ReportDigest  string
	Repo          string
	Head          string
	Branch        string
	PlanRevision  string
	ReportSession string
	// Mode is the rule set the run was made under, as profile/policy. A verdict
	// computed by a weaker profile judges fewer classes, so it must not release a
	// DONE report.
	Mode string
	// Reused marks a verdict the gate took instead of computing one. The release
	// message says so, so no reader believes a run happened at DONE.
	Reused bool
	// AwaitingVerdict marks a block whose cause is an absent verdict, not a
	// judgement of the report. Such a block is re-evaluated on every tick (the
	// verdict may have arrived in between) and is not counted as a new round.
	AwaitingVerdict bool
	// Precondition is the command the agent has to run before sending DONE. The
	// block message prints it, which is how the reader learns what the gate now
	// requires without reading a rule.
	Precondition string
	// StaleBecause names the part that moved since the verdict was reached.
	StaleBecause string
	// DecisionSig is the decision this state stands for: the signature of the
	// verdict that was used, "presence:<digest>" for a report that states nothing,
	// or "none" while no verdict exists. A tick that reaches the same decision has
	// nothing to say, and one that reaches another re-decides. Comparing this
	// instead of the stored state keeps a verdict that cannot be reused (a wrong
	// rule set, a refusal) from re-counting a round on every tick.
	DecisionSig string
	// PayloadWithheld says the delivery check kept the DONE payload back. It travels
	// with the state so a release can ask for that message again: the verdict is in,
	// but the handoff itself never arrived.
	PayloadWithheld bool
	// RecordedAt orders the verdict registry, which is bounded: the oldest
	// judgement is the one that may be dropped when the store is full.
	RecordedAt int64
}

// doneVerifyMaxDeliveries bounds the writes one verdict may cost the agent, and
// doneVerifyEscalateAfter the blocked verdicts one attempt collects before the
// gate hands the case to the orchestrator instead of asking for another correction.
const (
	doneVerifyMaxDeliveries = 3
	doneVerifyEscalateAfter = 3
)

var (
	doneGateVerifyStates = make(map[string]*doneGateVerifyState)
	// doneGateVerdicts is the shared judgement: one entry per verdict key, written
	// by every verify_claims run (RPC, MCP and CLI are the same operation) and read
	// by the DONE gate instead of a run of its own. It replaces the queue's job
	// store, which was the only other place a pending judgement lived.
	doneGateVerdicts = make(map[string]*doneGateVerifyState)
	// doneVerifyRunsInFlight names the keys whose pre-check is running right now.
	// The window is real: an agent writes its phase blocks before it runs the
	// pre-check, so a guard tick can land between the two. The value is the start,
	// because a mark that never comes back must not freeze a DONE claim forever.
	doneVerifyRunsInFlight = make(map[string]time.Time)
	doneGateVerifyStatesMu sync.Mutex
)

// maxStoredVerdicts bounds the registry. One entry is written per verification,
// not per tick, but a daemon running for weeks would still accumulate them. The
// oldest judgement is dropped first, which costs a re-run of the pre-check and
// never an unverified DONE.
const maxStoredVerdicts = 256

// doneGateRunLocks serialises the gate per agent. One lock per agent rather
// than one global lock: a blocked gate notifies an orchestrator whose own gate
// can run in the same process, and a single lock would then wait on itself.
var (
	doneGateRunLocksMu sync.Mutex
	doneGateRunLocks   = map[string]*sync.Mutex{}
)

// lockDoneGateRun holds the gate slot of one agent and returns its release.
func lockDoneGateRun(id string) func() {
	doneGateRunLocksMu.Lock()
	lock, ok := doneGateRunLocks[id]
	if !ok {
		lock = &sync.Mutex{}
		doneGateRunLocks[id] = lock
	}
	doneGateRunLocksMu.Unlock()
	lock.Lock()
	return lock.Unlock
}

// resetDoneGateVerifyStates clears the gate's whole memory: the per-agent attempt
// states, the verdict registry and the in-flight keys. Used in tests.
func resetDoneGateVerifyStates() {
	doneGateVerifyStatesMu.Lock()
	doneGateVerifyStates = make(map[string]*doneGateVerifyState)
	doneGateVerdicts = make(map[string]*doneGateVerifyState)
	doneVerifyRunsInFlight = make(map[string]time.Time)
	doneGateVerifyStatesMu.Unlock()
}

// recordVerifyVerdict stores one judgement under its key. A newer judgement about
// the same key replaces the older one: the key names one report, and two
// judgements about it cannot both be current.
func (h *Handler) recordVerifyVerdict(state *doneGateVerifyState) {
	if state == nil || state.Fingerprint == "" {
		return
	}
	if state.RecordedAt == 0 {
		state.RecordedAt = time.Now().UnixNano()
	}
	doneGateVerifyStatesMu.Lock()
	defer doneGateVerifyStatesMu.Unlock()
	doneGateVerdicts[state.Fingerprint] = state
	for len(doneGateVerdicts) > maxStoredVerdicts {
		// First within the same repository: a busy project must not push out the
		// verdicts of another one. Only when this repository has nothing older does
		// the oldest entry anywhere go.
		evicted := dropOldestVerdict(state.Repo)
		if !evicted {
			dropOldestVerdict("")
		}
	}
}

// dropOldestVerdict removes the oldest judgement of one repository (or of the whole
// registry when repo is empty) and reports whether there was one. The caller holds
// the mutex.
func dropOldestVerdict(repo string) bool {
	oldestKey := ""
	var oldestAt int64
	for key, candidate := range doneGateVerdicts {
		if repo != "" && candidate.Repo != repo {
			continue
		}
		if oldestKey == "" || candidate.RecordedAt < oldestAt {
			oldestKey, oldestAt = key, candidate.RecordedAt
		}
	}
	if oldestKey == "" {
		return false
	}
	delete(doneGateVerdicts, oldestKey)
	return true
}

// verifyVerdictFor returns the judgement for one key, or nil. The caller gets a
// copy: the stored verdict is the shared record between two entry points, and a
// reader that mutated it would change what the other one reads.
func (h *Handler) verifyVerdictFor(key string) *doneGateVerifyState {
	if key == "" {
		return nil
	}
	doneGateVerifyStatesMu.Lock()
	defer doneGateVerifyStatesMu.Unlock()
	state, ok := doneGateVerdicts[key]
	if !ok {
		return nil
	}
	return copyVerdictState(state)
}

// beginVerifyRun and endVerifyRun mark the window a verification holds its slot.
func beginVerifyRun(key string) {
	if key == "" {
		return
	}
	doneGateVerifyStatesMu.Lock()
	doneVerifyRunsInFlight[key] = time.Now()
	doneGateVerifyStatesMu.Unlock()
}

func endVerifyRun(key string) {
	if key == "" {
		return
	}
	doneGateVerifyStatesMu.Lock()
	delete(doneVerifyRunsInFlight, key)
	doneGateVerifyStatesMu.Unlock()
}

// doneVerifyRunCeiling is the longest a pre-check can claim to be running: the
// configured budget plus a minute, and never less than the verifier's default.
// A mark older than that belongs to a run that will never report.
func (h *Handler) doneVerifyRunCeiling() time.Duration {
	ceiling := h.doneVerifyBudget() + time.Minute
	if ceiling < 10*time.Minute {
		ceiling = 10 * time.Minute
	}
	return ceiling
}

func (h *Handler) verifyRunInFlight(key string) bool {
	_, inFlight := h.verdictAndRun(key)
	return inFlight
}

// verdictAndRun reads the verdict and the in-flight mark under one lock. A run
// records its verdict before it clears its mark, so two separate reads could see
// "no verdict yet" and "no run any more" and call a checked report unchecked.
func (h *Handler) verdictAndRun(key string) (*doneGateVerifyState, bool) {
	if key == "" {
		return nil, false
	}
	doneGateVerifyStatesMu.Lock()
	defer doneGateVerifyStatesMu.Unlock()
	if state, ok := doneGateVerdicts[key]; ok {
		return copyVerdictState(state), false
	}
	started, ok := doneVerifyRunsInFlight[key]
	if !ok {
		return nil, false
	}
	if time.Since(started) >= h.doneVerifyRunCeiling() {
		delete(doneVerifyRunsInFlight, key)
		return nil, false
	}
	return nil, true
}

// copyVerdictState hands out a copy with its own slices.
func copyVerdictState(state *doneGateVerifyState) *doneGateVerifyState {
	copied := *state
	copied.Findings = append([]verifyClaimFinding(nil), state.Findings...)
	copied.RoundNotes = append([]string(nil), state.RoundNotes...)
	return &copied
}

// verifyVerdictMode names the rule set a run was made under. A verdict is only
// reusable when its mode is the one the DONE gate applies: an empty profile judges
// fewer classes, and an empty policy blocks more than the loop tolerates.
func verifyVerdictMode(profile, policy string) string {
	return profile + "/" + policy
}

// doneVerifyPolicyName is the policy name verify_claims takes for the loop's rule
// set. It is part of the mode a reusable verdict has to carry.
const doneVerifyPolicyName = "yesloop-done"

func doneVerifyRequiredMode() string {
	return verifyVerdictMode(verify.ProfileYesloopDone, doneVerifyPolicyName)
}

// pruneDoneGateVerifyStates drops the state of agents that no longer have a row.
// The map is in-memory and is written once per verified report, so without the
// prune it would keep one entry, with its findings, per agent that ever reached
// DONE until the daemon restarted (the shape YM-285 fixed for the sibling maps).
func (h *Handler) pruneDoneGateVerifyStates(agents []storage.Agent) {
	live := make(map[string]struct{}, len(agents))
	for i := range agents {
		live[agents[i].ID] = struct{}{}
	}
	doneGateVerifyStatesMu.Lock()
	defer doneGateVerifyStatesMu.Unlock()
	for id := range doneGateVerifyStates {
		if _, ok := live[id]; !ok {
			delete(doneGateVerifyStates, id)
		}
	}
}

// doneGateVerifyStateFor returns a copy of the agent's state, or nil. The slices
// are copied too: the state is shared with the stored verdict of the same report,
// and a reader that appended in place would change what the next tick reads.
func (h *Handler) doneGateVerifyStateFor(agentID string) *doneGateVerifyState {
	doneGateVerifyStatesMu.Lock()
	defer doneGateVerifyStatesMu.Unlock()
	state, ok := doneGateVerifyStates[agentID]
	if !ok {
		return nil
	}
	copied := *state
	copied.Findings = append([]verifyClaimFinding(nil), state.Findings...)
	copied.RoundNotes = append([]string(nil), state.RoundNotes...)
	return &copied
}

// replaceDoneGateVerifyState stores a state that was derived from the report as it
// is now, replacing the state of an older report.
//
// Every writer of a per-agent state decides on the current text: the gate reads
// the report, the verdict registry holds what a run concluded about it. None of
// them can lag behind the report the way a verdict from an earlier run could, so
// write for a different fingerprint would only leave a decision unstored and let
// the next tick pause, notify and relay again.
func (h *Handler) replaceDoneGateVerifyState(agentID string, state *doneGateVerifyState) {
	doneGateVerifyStatesMu.Lock()
	doneGateVerifyStates[agentID] = state
	doneGateVerifyStatesMu.Unlock()
	// The map is the fast path; the persisted hold is what a restart reads. The
	// write sits outside the mutex: it is a database call, and no reader of the
	// state map waits for it.
	h.persistDONEHold(agentID, state)
}

// persistDONEHold mirrors one decision into the persisted hold. A blocked or
// pending state is a hold, an accepted one ends it. HeldSince is not passed
// here on purpose: SaveDONEHold keeps the first clock of a report and resets it
// only when the fingerprint changes, which is exactly the attempt boundary.
func (h *Handler) persistDONEHold(agentID string, state *doneGateVerifyState) {
	if h.store == nil || agentID == "" {
		return
	}
	if state == nil || state.Status == doneVerifyAccepted {
		if err := h.store.DeleteDONEHold(agentID); err != nil {
			log.Printf("[done-verify] agent %s: dropping the persisted hold failed: %v",
				safeAgentText(agentID), safeAgentText(err.Error()))
		}
		return
	}
	kind := state.Kind
	if kind == "" {
		kind = state.Status
	}
	hold := storage.DONEHold{
		AgentID:     agentID,
		Fingerprint: state.Fingerprint,
		Status:      state.Status,
		Kind:        kind,
		Reason:      state.Reason,
		HeldSince:   time.Now().Format(time.RFC3339),
	}
	if err := h.store.SaveDONEHold(hold); err != nil {
		// A hold the daemon cannot persist is still a hold in memory; the
		// watchdog loses its clock, not the decision. It is logged because the
		// restart guarantee is the one thing this row exists for.
		log.Printf("[done-verify] agent %s: persisting the hold failed: %v",
			safeAgentText(agentID), safeAgentText(err.Error()))
	}
}

// doneGateVerifySettled reports whether the verification stage has reached a
// verdict for this agent. The idle layer must stay silent for such a row: an
// accepted report releases the agent, and relaying the idle protocol into it
// would contradict the daemon's own verdict (the same contradiction YM-149
// fixed between the done-verify layer and the idle layer).
func (h *Handler) doneGateVerifySettled(agent storage.Agent) bool {
	state := h.doneGateVerifyStateFor(agent.ID)
	return state != nil && (state.Status == doneVerifyAccepted || state.Status == doneVerifyBlocked)
}

// verifyReportSection is the section beside the pipeline section that holds the
// report. The convention is the loop's; the operation reads any text.
func verifyReportSection(section string) string {
	return section + "-verify"
}

// verifyFingerprint binds a verdict to one report about one tree on one branch.
// Everything the verdict depends on is part of it: the same text must be judged
// again when the session it must prove, the plan it names, the branch it is on, the
// work on the tree or the uncommitted residue beside it changes.
func verifyFingerprint(repo, report, reportSession, planThread, head, worktree, planRevision, branch string) string {
	digest := sha256.New()
	for _, part := range []string{repo, reportSession, planThread, head, worktree, planRevision, branch, report} {
		digest.Write([]byte(part))
		digest.Write([]byte{0})
	}
	return hex.EncodeToString(digest.Sum(nil))
}

// verifyBranch is the branch the inspected tree stands on. Same role as the head:
// a checkout that moved to another branch is another question.
func verifyBranch(repo string) string {
	return verify.Branch(repo)
}

// verifyVerdictSignature names a verdict the way its two readers see it: the
// status, the class, the cause, the report it is about, the bound that applied and
// the findings behind it. Two states with the same signature say the same thing
// about the same text, which is what tells a change apart from a repeat. Two parts
// stay out: the reason of a limit verdict and the consumed time, both of which the
// run itself varies by seconds, so a signature built from them would report every
// retry as news. Every other class keeps its cause, because a changed cause is a
// changed verdict for the reader.
func verifyVerdictSignature(state *doneGateVerifyState) string {
	if state == nil {
		return ""
	}
	parts := []string{state.Status, state.Kind, state.Fingerprint, state.Bound.String()}
	if state.Kind != "limit" {
		parts = append(parts, state.Reason)
	}
	for _, finding := range state.Findings {
		parts = append(parts, finding.Kind, finding.Status, finding.Class, finding.Command)
	}
	// Length-prefixed, because a command in a report is free text: a separator
	// inside one would let a single finding look like two.
	var builder strings.Builder
	for _, part := range parts {
		fmt.Fprintf(&builder, "%d:%s", len(part), part)
	}
	return builder.String()
}

// notifiedFor carries the last announced verdict over to the state that replaces
// it, but only while both judge the same report.
func notifiedFor(previous *doneGateVerifyState, fingerprint string) string {
	if previous == nil || previous.Fingerprint != fingerprint {
		return ""
	}
	return previous.Notified
}

// recordVerdictNotice marks the state as told and reports whether it is news. The
// caller stores the state afterwards: the field is written before the state is
// shared, so no reader can see a half-written transition.
func recordVerdictNotice(state *doneGateVerifyState) bool {
	signature := verifyVerdictSignature(state)
	if signature == state.Notified {
		return false
	}
	state.Notified = signature
	return true
}

// markVerifyVerdictNotice records a message that reached the agent. Only the state
// it was written for is marked: a verdict that arrived in between keeps its own
// place and is delivered on its own. The mutex is what makes the field safe to
// write after the state was stored.
func (h *Handler) markVerifyVerdictNotice(agentID, signature string) {
	doneGateVerifyStatesMu.Lock()
	defer doneGateVerifyStatesMu.Unlock()
	state, ok := doneGateVerifyStates[agentID]
	if !ok || verifyVerdictSignature(state) != signature {
		return
	}
	state.Notified = signature
}

// repeatOrAbandonDelivery counts a write that did not leave the daemon and decides
// whether the message is owed again. It only touches its own mark: a verdict that
// arrived in the meantime carries a different signature and stays announced. After
// doneVerifyMaxDeliveries the mark is put back and the agent is not poked again;
// the status line is what a reader falls back to then.
func (h *Handler) repeatOrAbandonDelivery(agentID, signature string) {
	doneGateVerifyStatesMu.Lock()
	defer doneGateVerifyStatesMu.Unlock()
	state, ok := doneGateVerifyStates[agentID]
	if !ok || (state.Notified != "" && state.Notified != signature) {
		return
	}
	state.Deliveries++
	if state.Deliveries < doneVerifyMaxDeliveries {
		state.Notified = ""
		return
	}
	state.Notified = verifyVerdictSignature(state)
}

// deliverVerifyMessage writes one message to the agent and pays for it with the
// state: a write that never left the daemon withdraws the mark, so the next tick
// owes the message again.
func (h *Handler) deliverVerifyMessage(agent storage.Agent, state *doneGateVerifyState, message string) {
	if h.relayVerifyMessage(agent, message) {
		return
	}
	h.repeatOrAbandonDelivery(agent.ID, verifyVerdictSignature(state))
}

// redeliverVerifyHold writes the message this state was never told about. The gate
// does not count a notice as taken before the write left the daemon, so a tick that
// finds the mark withdrawn owes the agent exactly that one message.
//
// It also restores a persisted hold that is gone. The row is the only clock the
// watchdog has, and another guard's pause (or a cleanup that misread one) can
// remove it while the case is still live.
func (h *Handler) redeliverVerifyHold(agent storage.Agent, state *doneGateVerifyState) {
	if state.Notified != "" {
		return
	}
	h.persistDONEHold(agent.ID, state)
	signature := verifyVerdictSignature(state)
	if h.relayVerifyMessage(agent, verifyHoldMessage(agent, state)) {
		h.markVerifyVerdictNotice(agent.ID, signature)
		return
	}
	h.repeatOrAbandonDelivery(agent.ID, signature)
}

// carriedRounds returns the blocked-round count of the state that a new state
// replaces. The count belongs to the attempt, not to the report text, so it travels
// across a correction and across a retry.
func carriedRounds(previous *doneGateVerifyState) (int, []string) {
	if previous == nil {
		return 0, nil
	}
	return previous.BlockedRounds, previous.RoundNotes
}

// countBlockedRound records one more blocked round on a state whose kind and reason
// are already set, and escalates when the attempt has had its third.
func countBlockedRound(state *doneGateVerifyState, previous *doneGateVerifyState) {
	state.BlockedRounds, state.RoundNotes = carriedRounds(previous)
	state.BlockedRounds++
	state.RoundNotes = append(append([]string(nil), state.RoundNotes...), roundNote(state))
	if state.BlockedRounds >= doneVerifyEscalateAfter {
		state.Escalated = true
	}
}

// escalateBlockedReport is the announcement three blocked rounds earn: the agent
// stays held and is told to wait, and the orchestrator gets the rounds, the class and
// the question the gate cannot answer. Nothing is discarded and nothing runs.
func (h *Handler) escalateBlockedReport(agent storage.Agent, state *doneGateVerifyState) {
	log.Printf("[done-verify] agent %s (%s) ESCALATED after %d blocked rounds", safeAgentText(agent.ID), safeAgentText(agent.Section), state.BlockedRounds)
	h.pauseAgent(agent.ID, fmt.Sprintf(
		"DONE-VERIFY-BLOCKED (%s, escalated): %d blocked rounds on the same attempt; the orchestrator decides, the agent waits",
		safeAgentText(state.Kind), state.BlockedRounds))
	h.notifyOrchestrator(agent, doneVerifyEscalatedMessage(agent, state))
	h.deliverVerifyMessage(agent, state, doneVerifyEscalatedPrompt)
}

// escalatedHold reports whether the row still carries the escalation marker. The
// state map is in memory, so after a restart the status line is the only witness of
// a case that already left the loop.
func escalatedHold(agent storage.Agent) bool {
	return strings.HasPrefix(agent.Progress, "paused: DONE-VERIFY-BLOCKED (") &&
		strings.Contains(agent.Progress, ", escalated)")
}

// verifyHoldMessage is what the agent is owed for a state it was never told about:
// the instruction to wait while the pre-check runs, the findings once a verdict
// blocks the report, and the release once it survived. The instruction told the
// agent to wait, so none of the three may rely on the orchestrator to pass it on.
func verifyHoldMessage(agent storage.Agent, state *doneGateVerifyState) string {
	switch state.Status {
	case doneVerifyBlocked:
		if state.Escalated {
			return doneVerifyEscalatedPrompt
		}
		return doneVerifyBlockedMessage(agent, state)
	case doneVerifyAccepted:
		if state.PayloadWithheld {
			// The release that was never told is the release that has to ask for the
			// payload again: "you are finished" here would drop a handoff the verdict
			// just made deliverable.
			return doneVerifyResendPayloadPrompt
		}
		return doneVerifyReleasedPrompt
	}
	return doneVerifyFrozenPrompt
}

// heldByVerify reports whether this gate is what keeps the agent paused. The
// prefix, not the bare status, is the witness: another guard may have overwritten
// the line in the meantime, and then the hold has to be placed again.
func heldByVerify(agent storage.Agent) bool {
	return strings.HasPrefix(agent.Progress, "paused: DONE-PENDING-VERIFY:") ||
		strings.HasPrefix(agent.Progress, "paused: DONE-VERIFY-BLOCKED")
}

// attemptRounds is the blocked-round count a new state may carry. It belongs to the
// attempt, not to the report text, and the gate only holds a row it blocked itself:
// a row the orchestrator released starts a fresh attempt, and a release is the
// orchestrator answering, not the agent accumulating rounds.
func attemptRounds(agent storage.Agent, previous *doneGateVerifyState) *doneGateVerifyState {
	if !heldByVerify(agent) {
		return nil
	}
	return previous
}

// agentReportSession and agentPlanThread are the facts the gate's key is built
// from: the session a report's evidence must descend from, and the plan it names.
// One derivation, three callers (the gate, the printed command, the operation).
func agentReportSession(agent storage.Agent) string {
	if agent.OpencodeSessionID != "" {
		return agent.OpencodeSessionID
	}
	return agent.SessionID
}

func agentPlanThread(agent storage.Agent) string {
	if agent.ProxyThreadID != "" {
		return agent.ProxyThreadID
	}
	return agent.SessionID
}

// agentForReportSection finds the agent a report section belongs to. The CLI names
// the report section (the pipeline section plus "-verify"), so both forms are
// tried. A section can have more than one row after a restart or a re-spawn; the
// newest one is the live agent and wins (AgentList orders by creation, newest
// first), which is the row a report being written now belongs to.
func (h *Handler) agentForReportSection(project, section string) *storage.Agent {
	if h.store == nil || project == "" || section == "" {
		return nil
	}
	agents, err := h.store.AgentList(project)
	if err != nil {
		return nil
	}
	plain := strings.TrimSuffix(section, "-verify")
	for i := range agents {
		if agents[i].Section == section || agents[i].Section == plain {
			return &agents[i]
		}
	}
	return nil
}

// verifyContextFor derives the key the pre-check wrote its verdict under. The
// gate and the operation must agree on it without talking to each other, so both
// build it from the same facts: the report text, the tree, the branch, the plan
// and the session that has to prove the evidence.
func (h *Handler) verifyContextFor(agent storage.Agent, repo, report string) verifyContext {
	session := agentReportSession(agent)
	thread := agentPlanThread(agent)
	context := verifyContext{
		repo:          repo,
		head:          verifyHead(repo),
		worktree:      verify.WorktreeState(repo),
		branch:        verifyBranch(repo),
		planRevision:  h.verifyPlanFingerprint(thread),
		reportSession: session,
		reportDigest:  digestText(report),
	}
	context.key = verifyFingerprint(repo, report, session, thread, context.head, context.worktree, context.planRevision, context.branch)
	return context
}

// verifyContext is one verdict key together with the facts it was built from. The
// key decides reuse; the facts decide what the block tells the agent when the key
// no longer matches.
type verifyContext struct {
	key           string
	repo          string
	reportDigest  string
	head          string
	worktree      string
	branch        string
	planRevision  string
	reportSession string
}

// digestText is the report's own fingerprint, kept apart from the key so a stale
// verdict can say that the text changed and not merely that something did.
func digestText(text string) string {
	digest := sha256.Sum256([]byte(text))
	return hex.EncodeToString(digest[:])
}

// gateDoneVerify decides a compliant DONE claim from the verdict the pre-check
// already reached.
//
// It returns false when the claim is not in scope (no DONE claim, fewer than six
// completed phases, nothing to check the report against), so the caller keeps its
// previous behaviour. Everything else is handled here, and none of it runs a
// verification: the expensive run belongs to verify_claims, whose judgement this
// gate reads. A fresh accepted verdict releases the agent, a fresh blocked one
// keeps it with the findings, and an absent or stale one is a defect: the report
// was promoted to DONE before it was checked.
func (h *Handler) gateDoneVerify(agent storage.Agent, content string) bool {
	if !h.doneVerifyEnabled() {
		return false
	}
	if !hasDoneClaim(content) || CountCompletedPhases(content) < donePhaseCount {
		return false
	}
	repo := agent.WorkDir
	if repo == "" {
		repo = agent.Project
	}
	if repo == "" || agent.Project == "" || agent.Section == "" {
		return false
	}

	// One gate per agent at a time. Two ticks can land on the same report, and
	// without this the slower one announces a block after the faster one has
	// already escalated: the agent then reads a fourth request after the case
	// left the loop, which is exactly what the escalation exists to prevent.
	unlock := lockDoneGateRun(agent.ID)
	defer unlock()

	report, readErr := h.verifyReport(agent)
	// A row that still carries the escalation is not re-entered: the state map is in
	// memory, so the status line is the only witness that survives a restart, and a
	// fourth round is what the escalation exists to prevent.
	if escalatedHold(agent) {
		return true
	}
	context := h.verifyContextFor(agent, repo, report)
	if readErr != nil {
		// The store could not answer, so the report is unknown rather than absent.
		// Reading that as the agent's fault would freeze it for an infrastructure
		// problem (plan policy: an environment gap is reported as such). The error is
		// bound into the key so the environment block does not hide the real verdict,
		// and a block this key already earned is not written again: the status line is
		// what a reader takes it from, and a second write would be a second notice.
		key := verifyFingerprint(repo, readErr.Error(), context.reportSession, "", context.head, context.worktree, context.planRevision, context.branch)
		if current := h.doneGateVerifyStateFor(agent.ID); current != nil && current.Fingerprint == key && current.Status == doneVerifyBlocked {
			h.redeliverVerifyHold(agent, current)
			return true
		}
		return h.blockOnEnvironment(agent, key, fmt.Sprintf(
			"the report section %s could not be read: %v", verifyReportSection(agent.Section), readErr))
	}
	verdict, preCheckRunning := h.verdictAndRun(context.key)
	decision := verdictDecision(verdict)
	if preCheckRunning {
		// A run in flight is a decision of its own. Without this, the next tick would
		// read "no verdict" and re-announce the hold it is already in.
		decision = pendingDecision(context)
	}
	if len(verify.ParseReport(report)) == 0 {
		decision = presenceDecision(context)
	}
	previous := h.doneGateVerifyStateFor(agent.ID)
	// Same report, same decision: nothing new happened since the last tick, so the
	// agent is owed only the message it may not have received. A decision that
	// changed (an accepted verdict after a blocked one, because the branch was
	// pushed or the suite turned green) re-opens it.
	if previous != nil && previous.Fingerprint == context.key && previous.DecisionSig == decision {
		h.redeliverVerifyHold(agent, previous)
		return true
	}

	if len(verify.ParseReport(report)) == 0 {
		// A report without a claim has nothing a verdict could be about. The presence
		// check answers before any lookup, exactly as it did before a run was needed.
		return h.blockOnUnstatedReport(agent, context, report)
	}
	if verdict == nil {
		if preCheckRunning {
			// The pre-check is running right now. Blocking here would tell the agent to
			// start the run it is in the middle of.
			return h.holdOnRunningPrecheck(agent, context, previous, false)
		}
		return h.blockOnMissingVerdict(agent, context, previous, decision, "")
	}
	if verdict.Mode != doneVerifyRequiredMode() {
		// A judgement under a weaker rule set cannot release a DONE report: an empty
		// profile judges fewer classes, so its acceptance proves less.
		return h.blockOnMissingVerdict(agent, context, previous, decision, fmt.Sprintf(
			"the pre-check judged this report as %s, the DONE gate needs %s",
			verdict.Mode, doneVerifyRequiredMode()))
	}
	switch verdict.Status {
	case doneVerifyAccepted:
		return h.releaseOnVerifiedReport(agent, context, verdict, previous)
	}
	// The registry only ever holds finished judgements; a run that is still working
	// is known by its in-flight mark, not by an entry.
	return h.blockOnVerifiedReport(agent, context, verdict, previous)
}

// verdictForSameReport finds the most recent judgement about the same report text
// and session, whatever its key. It exists for the diagnosis: when no per-agent
// state is on record, this is the verdict whose facts moved, and the block can say
// whether the text, the tree, the branch or the plan changed instead of claiming
// that nothing was ever checked.
func (h *Handler) verdictForSameReport(context verifyContext) *doneGateVerifyState {
	doneGateVerifyStatesMu.Lock()
	defer doneGateVerifyStatesMu.Unlock()
	var newest *doneGateVerifyState
	for _, candidate := range doneGateVerdicts {
		if candidate.ReportDigest != context.reportDigest || candidate.ReportSession != context.reportSession {
			continue
		}
		if newest == nil || candidate.RecordedAt > newest.RecordedAt {
			newest = candidate
		}
	}
	if newest == nil {
		return nil
	}
	return copyVerdictState(newest)
}

// verdictDecision names what the gate would do with this verdict. It is the tick
// guard's whole criterion: the same decision twice is one message, not two rounds.
// A verdict under a wrong rule set gets a stable marker, because the gate would
// refuse it identically on every tick.
func verdictDecision(verdict *doneGateVerifyState) string {
	if verdict == nil {
		return "none"
	}
	if verdict.Mode != doneVerifyRequiredMode() {
		return "rejected-rule-set:" + verdict.Mode
	}
	return verifyVerdictSignature(verdict)
}

// presenceDecision is the decision for a report that states no claim. It is bound
// to the text like every other decision: the same text reaches it again.
func presenceDecision(context verifyContext) string {
	return "presence:" + context.reportDigest
}

// pendingDecision is the decision while a pre-check for the report is running. It
// is stable across ticks so the hold is announced once, not once per heartbeat.
func pendingDecision(context verifyContext) string {
	return "pending:" + context.key
}

// holdOnRunningPrecheck freezes the agent while its own pre-check runs. The row is
// re-checked every tick while it carries this prefix, so the verdict lands without
// anything else waking the agent.
//
// payloadWithheld says the hold was reached from the delivery side, where a DONE
// message was never stored. It travels with the state so the release asks for that
// message again instead of declaring the agent finished.
func (h *Handler) holdOnRunningPrecheck(agent storage.Agent, context verifyContext, previous *doneGateVerifyState, payloadWithheld bool) bool {
	state := &doneGateVerifyState{
		Fingerprint:     context.key,
		Status:          doneVerifyPending,
		DecisionSig:     pendingDecision(context),
		ReportDigest:    context.reportDigest,
		Repo:            context.repo,
		Head:            context.head,
		Worktree:        context.worktree,
		Branch:          context.branch,
		PlanRevision:    context.planRevision,
		ReportSession:   context.reportSession,
		Precondition:    verifyClaimsCommand(agent, context.repo),
		PayloadWithheld: payloadWithheld,
	}
	// The hold is part of the attempt: a tick that lands while the run works must
	// not reset the rounds the agent already collected, or a correction that starts
	// its pre-check could erase its way to a fourth block without escalating.
	state.BlockedRounds, state.RoundNotes = carriedRounds(attemptRounds(agent, previous))
	news := recordVerdictNotice(state)
	h.replaceDoneGateVerifyState(agent.ID, state)
	h.pauseAgent(agent.ID, "DONE-PENDING-VERIFY: the pre-check for this report is running; the verdict decides")
	if !news {
		return true
	}
	notice := fmt.Sprintf(
		"DONE-PENDING-VERIFY: agent %s (%s) reported DONE while the pre-check for its report was still running; the verdict decides. Report section: %s",
		safeAgentText(agent.ID), safeAgentText(agent.Section), safeAgentText(verifyReportSection(agent.Section)))
	if state.Precondition != "" {
		notice += "\n  " + state.Precondition
	}
	h.notifyOrchestrator(agent, notice)
	h.deliverVerifyMessage(agent, state, doneVerifyFrozenPrompt)
	return true
}

// releaseOnVerifiedReport releases an agent whose report already survived the
// pre-check. No run, no checkout: the verdict is the run.
func (h *Handler) releaseOnVerifiedReport(agent storage.Agent, context verifyContext, verdict *doneGateVerifyState, previous *doneGateVerifyState) bool {
	state := *verdict
	state.Fingerprint = context.key
	state.ReportDigest = context.reportDigest
	state.Head = context.head
	state.Branch = context.branch
	state.PlanRevision = context.planRevision
	state.ReportSession = context.reportSession
	state.Precondition = ""
	state.StaleBecause = ""
	state.AwaitingVerdict = false
	state.Reused = true
	state.DecisionSig = verdictDecision(verdict)
	state.Notified = notifiedFor(previous, context.key)
	// A report whose DONE payload the delivery check withheld still owes that
	// message. Releasing it as "nothing further to do" would tell the agent to stop
	// with the handoff unsent.
	payloadWithheld := previous != nil && previous.PayloadWithheld
	state.PayloadWithheld = payloadWithheld
	// A passed report does not carry the attempt's history into the next one.
	state.BlockedRounds = 0
	state.RoundNotes = nil
	state.Escalated = false
	news := recordVerdictNotice(&state)
	h.replaceDoneGateVerifyState(agent.ID, &state)

	log.Printf("[done-verify] agent %s (%s) verified DONE from the pre-check's verdict", safeAgentText(agent.ID), safeAgentText(agent.Section))
	if agentProcessGone(agent) {
		// The row is the only thing left of this agent. Flipping it to running
		// would announce a live agent that no process backs; crashRecovery owns
		// that transition.
		log.Printf("[done-verify] agent %s: verified DONE, but its process is gone — the row stays paused", safeAgentText(agent.ID))
		return true
	}
	if agent.Status == "paused" {
		h.unpauseAgent(agent.ID, "DONE-VERIFY", "the DONE report's verdict came from the pre-check")
	}
	releasePrompt := doneVerifyReusedReleasePrompt
	if payloadWithheld {
		releasePrompt = doneVerifyResendPayloadPrompt
	}
	h.deliverVerifyMessage(agent, &state, releasePrompt)
	if news {
		notice := fmt.Sprintf(
			"DONE-VERIFIED: agent %s (%s) reported DONE; the report was already verified by the pre-check, so no run happened at DONE (reused verdict from the pre-check)",
			safeAgentText(agent.ID), safeAgentText(agent.Section))
		if payloadWithheld {
			notice += " Its DONE payload was withheld by the delivery check and never arrived: the agent has been asked to send it again."
		}
		h.notifyOrchestrator(agent, notice)
	}
	return true
}

// blockOnMissingVerdict holds a report that was promoted to DONE without a usable
// verdict. It is a defect of the report's handling, not of its content: the gate
// names the command that produces the verdict and never runs one itself.
func (h *Handler) blockOnMissingVerdict(agent storage.Agent, context verifyContext, previous *doneGateVerifyState, decision, because string) bool {
	reason := "no verdict exists for this report: the DONE gate does not run the check itself"
	if because == "" {
		// The agent's own last state is the closest witness, but a verdict about this
		// same report may exist without one: the plan, the tree or the branch moved
		// before the first DONE tick. Then the verdict registry is the witness, and
		// the block names what changed instead of claiming nothing was checked.
		basis := previous
		if basis == nil {
			basis = h.verdictForSameReport(context)
		}
		because = staleBecause(basis, context)
	}
	if because != "" {
		reason = "the stored verdict does not apply: " + because
	}
	state := &doneGateVerifyState{
		Fingerprint:     context.key,
		Status:          doneVerifyBlocked,
		Kind:            "defect",
		Reason:          reason,
		StaleBecause:    because,
		AwaitingVerdict: true,
		DecisionSig:     decision,
		ReportDigest:    context.reportDigest,
		Head:            context.head,
		Worktree:        context.worktree,
		Branch:          context.branch,
		PlanRevision:    context.planRevision,
		ReportSession:   context.reportSession,
		Precondition:    verifyClaimsCommand(agent, context.repo),
		Notified:        notifiedFor(previous, context.key),
	}
	countBlockedRound(state, attemptRounds(agent, previous))
	news := recordVerdictNotice(state)
	h.replaceDoneGateVerifyState(agent.ID, state)
	return h.announceBlockedReport(agent, state, news)
}

// blockOnVerifiedReport holds an agent whose report the pre-check refused. The
// findings and the class are the verdict's, not the gate's: nothing is recomputed
// here, and the agent is told what the run said.
func (h *Handler) blockOnVerifiedReport(agent storage.Agent, context verifyContext, verdict *doneGateVerifyState, previous *doneGateVerifyState) bool {
	state := *verdict
	state.Fingerprint = context.key
	state.ReportDigest = context.reportDigest
	state.Head = context.head
	state.Branch = context.branch
	state.Worktree = context.worktree
	state.PlanRevision = context.planRevision
	state.ReportSession = context.reportSession
	state.Precondition = verifyClaimsCommand(agent, context.repo)
	state.StaleBecause = ""
	state.AwaitingVerdict = false
	state.Reused = true
	state.DecisionSig = verdictDecision(verdict)
	state.Notified = notifiedFor(previous, context.key)
	countBlockedRound(&state, attemptRounds(agent, previous))
	news := recordVerdictNotice(&state)
	h.replaceDoneGateVerifyState(agent.ID, &state)
	return h.announceBlockedReport(agent, &state, news)
}

// announceBlockedReport is the one place a block reaches the agent and the
// orchestrator: the status line always, the two messages once per verdict.
func (h *Handler) announceBlockedReport(agent storage.Agent, state *doneGateVerifyState, news bool) bool {
	if state.Escalated {
		// The agent stays held, but the case leaves the loop: the same correction
		// failed three times, so a fourth request would be an answer the gate already
		// knows. The orchestrator decides, and until it does, nothing runs.
		h.escalateBlockedReport(agent, state)
		return true
	}
	h.pauseAgent(agent.ID, fmt.Sprintf("DONE-VERIFY-BLOCKED (%s): %s", safeAgentText(state.Kind), safeAgentText(state.Reason)))
	if !news {
		return true
	}
	message := doneVerifyBlockedMessage(agent, state)
	h.notifyOrchestrator(agent, message)
	h.deliverVerifyMessage(agent, state, message)
	return true
}

// staleBecause names the fact that moved since the agent's last verdict, if one is
// on record. Without one there is nothing to compare, and the block is the simple
// case: the report was never checked.
func staleBecause(previous *doneGateVerifyState, context verifyContext) string {
	if previous == nil {
		return ""
	}
	switch {
	case previous.ReportDigest != "" && previous.ReportDigest != context.reportDigest:
		return "the report text changed since the last verdict"
	case previous.Head != "" && previous.Head != context.head:
		return fmt.Sprintf("HEAD moved from %s to %s", shortHash(previous.Head), shortHash(context.head))
	case previous.Worktree != "" && context.worktree != "" && previous.Worktree != context.worktree:
		// The tree, not the commits: work that is in no commit. Naming it is the
		// difference between "run the check again" and "something changed, I do not
		// know what". An unreadable tree has no digest, and claiming residue the
		// verifier did not see would send the agent after a file that may not exist.
		return "the working tree changed since the verdict: uncommitted work appeared beside the commits"
	case previous.Branch != "" && previous.Branch != context.branch:
		return fmt.Sprintf("the branch changed from %s to %s", previous.Branch, context.branch)
	case previous.PlanRevision != "" && previous.PlanRevision != context.planRevision:
		return "the plan changed since the last verdict"
	case previous.ReportSession != "" && previous.ReportSession != context.reportSession:
		return "the report session changed since the last verdict"
	}
	return ""
}

func shortHash(hash string) string {
	if len(hash) > 12 {
		return hash[:12]
	}
	return hash
}

// verifyClaimsCommand is the one command that produces the verdict the gate now
// requires. It names the report section, the profile and the policy; the session
// and the plan thread are not flags here, because a section call derives them from
// the agent that owns it. Every value is quoted: the command is read and copied by
// another process, so a path with a space stays one argument and a value that
// carries a shell metacharacter must not become a second command in the reader's
// shell.
func verifyClaimsCommand(agent storage.Agent, repo string) string {
	return fmt.Sprintf(
		"yesmem verify-claims --repo %s --project %s --section %s --profile %s --policy %s --strict",
		shellQuote(repo), shellQuote(agent.Project), shellQuote(verifyReportSection(agent.Section)),
		verify.ProfileYesloopDone, doneVerifyPolicyName)
}

// shellQuote renders one value as a single-quoted shell word.
func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

// verifyReport reads the report section beside the pipeline section. A missing
// section is not an error: it reads as an empty report. The gate's presence check
// answers that case before a run, with the shape the report has to be written in;
// a caller that reads the report directly gets the empty text and its own profile
// names the claims nobody stated.
func (h *Handler) verifyReport(agent storage.Agent) (string, error) {
	sections, err := h.store.ScratchpadRead(agent.Project, verifyReportSection(agent.Section))
	if err != nil {
		return "", err
	}
	if len(sections) == 0 {
		return "", nil
	}
	return sections[0].Content, nil
}

// blockOnEnvironment records a verdict that is about the machine, not the report,
// and holds the agent for it.
//
// A pending or already released report is not blocked here: a read error while a
// pre-check runs must not discard the verdict that run is about to record, and a
// released agent is running. Only the blocked verdict of an older report is
// replaced, and it is replaced rather than refused: the decision comes from the
// text as it is now, so a refused write would leave the block unstored and every
// tick would pause and notify again.
func (h *Handler) blockOnEnvironment(agent storage.Agent, fingerprint, reason string) bool {
	current := h.doneGateVerifyStateFor(agent.ID)
	if current != nil && (current.Status == doneVerifyPending || current.Status == doneVerifyAccepted) {
		return true
	}
	state := &doneGateVerifyState{
		Fingerprint: fingerprint,
		Status:      doneVerifyBlocked,
		Kind:        "environment",
		Reason:      reason,
		DecisionSig: "environment",
		Notified:    notifiedFor(current, fingerprint),
	}
	countBlockedRound(state, attemptRounds(agent, current))
	news := recordVerdictNotice(state)
	h.replaceDoneGateVerifyState(agent.ID, state)
	return h.announceBlockedReport(agent, state, news)
}

// blockOnUnstatedReport records a report that carries no claim and holds the
// agent for it, without starting a run.
//
// It is a defect, not an environment gap, and it does not retry: the report is
// the agent's own text, and nothing but a correction of it changes the answer.
// The relay is the lever that lets the agent correct itself, and the command in
// the message is the one that produces the verdict the gate will read next.
//
// The state replaces whatever the agent carried before, including the verdict of
// an older report: it was derived from the text as it is now, and a refused write
// would leave the block unstored, so every guard tick would pause, notify and
// relay the same message again.
func (h *Handler) blockOnUnstatedReport(agent storage.Agent, context verifyContext, report string) bool {
	// The two fields stay apart: an absent section carried no text, a present one
	// carried text without a claim. The message and the status line need the
	// difference to say what is missing, and both send the reader to the shape.
	empty := strings.TrimSpace(report) == ""
	reason := "the report states no claim, so there is nothing to verify"
	if empty {
		reason = "the report section carries no text, so there is nothing to verify"
	}
	current := h.doneGateVerifyStateFor(agent.ID)
	state := &doneGateVerifyState{
		Fingerprint:   context.key,
		Status:        doneVerifyBlocked,
		Kind:          "defect",
		Reason:        reason,
		ReportEmpty:   empty,
		NoClaims:      !empty,
		DecisionSig:   presenceDecision(context),
		ReportDigest:  context.reportDigest,
		Head:          context.head,
		Worktree:      context.worktree,
		Branch:        context.branch,
		PlanRevision:  context.planRevision,
		ReportSession: context.reportSession,
		Precondition:  verifyClaimsCommand(agent, context.repo),
		Notified:      notifiedFor(current, context.key),
	}
	countBlockedRound(state, attemptRounds(agent, current))
	news := recordVerdictNotice(state)
	h.replaceDoneGateVerifyState(agent.ID, state)
	return h.announceBlockedReport(agent, state, news)
}

// gateVerifyCompliantDone is the DONE-guard's entry: it reads the scratchpad and
// hands it to gateDoneVerify.
func (h *Handler) gateVerifyCompliantDone(agent storage.Agent) bool {
	content := h.readAgentScratchpad(agent)
	if content == "" {
		return false
	}
	return h.gateDoneVerify(agent, content)
}

// reportTemplate is the seven-line shape the verifier reads, printed verbatim:
// the agent copies it instead of recalling seven line formats from the skill.
const reportTemplate = `[COMMIT: <full hash>]
[BRANCH: <pushed branch>]
[DEPLOY: yes|no]
Tests run: <command> -> exit <n>
Files in scope: <path>, <path>
[EVIDENCE: cold-review=ses_..., consequence=ses_..., security=ses_...]
[PLAN: <project>]`

// maxCommandChars bounds the command line the message keeps. A report may name a
// megabyte of command text, and the line is an instruction, not a transcript.
const maxCommandChars = 200

// doneVerifyFrozenPrompt is the instruction a held agent reads when the gate
// takes its DONE claim. The text is fixed and lives here: no model writes it, no
// template engine renders it, so a repeat run cannot produce a different order.
// It is written once per transition, because a second copy would wake the agent
// out of the hold it was just put into.
const doneVerifyFrozenPrompt = "DONE-PENDING-VERIFY: der Daemon hält dich an und prüft deinen Bericht jetzt. " +
	"Warte und tu nichts weiter: kein sleep, kein Polling, keine Session-Aktivität, keine eigene Verifikation, und schreibe den Bericht nicht erneut. " +
	"Auf DONE-VERIFIED bist du fertig. " +
	"Auf DONE-VERIFY-BLOCKED schickt der Daemon dir eine Meldung: bei einer Report-Klasse, zum Beispiel refuted oder defect, korrigierst du, schreibst den Abschnitt neu und setzt das DONE erneut ab. " +
	"Ist die Klasse environment, limit oder error, ist dein Bericht nicht schuld: melde dich beim Orchestrator und warte."

// doneVerifyReleasedPrompt is what a held agent reads when its report survived.
// The instruction told it that waiting is all it may do, so the release has to
// arrive on the same channel as the hold; a flipped row is not something the agent
// reads.
const doneVerifyReleasedPrompt = "DONE-VERIFIED: dein Bericht hat die Prüfung bestanden. Du bist fertig, es ist nichts weiter zu tun."

// doneVerifyReusedReleasePrompt is the release when the verdict came from the
// pre-check. It names that origin, so no reader believes a run happened at DONE.
const doneVerifyReusedReleasePrompt = "DONE-VERIFIED: dein Bericht hat die Prüfung bestanden. Das Urteil stammt aus der Vorab-Prüfung (verify-claims) und wurde wiederverwendet; beim DONE wurde nichts neu gerechnet. Du bist fertig, es ist nichts weiter zu tun."

// doneVerifyResendPayloadPrompt is the release when the DONE payload itself was
// withheld. The verdict is in, so the message the agent wanted to send is now
// deliverable; telling it "nothing further to do" would drop the handoff. The
// missing delivery is the first thing the reader sees, because the rest of the
// message reads exactly like an ordinary release.
const doneVerifyResendPayloadPrompt = "DONE-VERIFIED, aber die Zustellung fehlt: dein Bericht hat die Pruefung bestanden (Urteil aus der Vorab-Pruefung). Deine DONE-Nachricht wurde von der Zustellpruefung zurueckgehalten und ist nie beim Orchestrator angekommen. Setze genau diese DONE-Nachricht jetzt erneut ab."

// doneVerifyEscalatedPrompt is what the held agent reads once the gate has handed
// its case to the orchestrator. Fixed, like the hold: it asks for nothing but
// waiting, so a third failed correction cannot become a fourth attempt.
const doneVerifyEscalatedPrompt = "Eskaliert an den Orchestrator. Warte. Kein sleep, kein Polling."

// roundNote is one line per blocked round: the class and the reason, bounded by the
// same limit the finding commands take.
func roundNote(state *doneGateVerifyState) string {
	return state.Kind + ": " + truncateCommand(state.Reason)
}

// doneVerifyEscalatedMessage is what a report earns that was blocked three times.
// It carries what the orchestrator needs to decide: the number of rounds, what each
// round said, the class of the last one and the question the gate cannot answer.
// Nothing is discarded here, and the agent is not released by it.
func doneVerifyEscalatedMessage(agent storage.Agent, state *doneGateVerifyState) string {
	var builder strings.Builder
	builder.WriteString(fmt.Sprintf(
		"DONE-VERIFY-ESCALATED: agent %s (%s) was blocked %d times on the report %s and is no longer asked to correct it. Last class: %s. The agent stays paused and waits.",
		safeAgentText(agent.ID), safeAgentText(agent.Section), state.BlockedRounds,
		safeAgentText(verifyReportSection(agent.Section)), safeAgentText(state.Kind)))
	for index, note := range state.RoundNotes {
		builder.WriteString(fmt.Sprintf("\n  %d. %s", index+1, safeAgentText(note)))
	}
	builder.WriteString("\n  Open question: is this an agent fault, an environment gap, or a task the report cannot satisfy? Nothing was discarded; the next decision is yours.")
	if state.Precondition != "" {
		builder.WriteString("\n  The command that produces a verdict:\n  ")
		builder.WriteString(state.Precondition)
	}
	return builder.String()
}

func truncateCommand(command string) string {
	runes := []rune(command)
	if len(runes) <= maxCommandChars {
		return command
	}
	return string(runes[:maxCommandChars]) + "..."
}

// wantsReportTemplate reports whether the message should print the report shape:
// the section carried no text, the report stated no claim, or the findings say the
// report misses a class it has to state. All three are the report's own gap, and
// all three are answered by copying a shape rather than by reading a rule.
func wantsReportTemplate(state *doneGateVerifyState) bool {
	if state.ReportEmpty || state.NoClaims {
		return true
	}
	for _, finding := range state.Findings {
		if finding.Kind == "profile" {
			return true
		}
	}
	return false
}

// wantsProfileInitHint reports whether this block is the one a project profile
// answers: a test claim came back as an environment gap, and the repository has
// no .yesmem/verify.yaml.
//
// The class is not touched by it and nothing runs an init: the sentence is the
// same kind of deterministic help the report template gives, one command the
// reader can act on. Without the file, a test claim can only ever say that the
// environment does not know how this project runs its suite.
func wantsProfileInitHint(findings []verifyClaimFinding, repo string) bool {
	if repo == "" {
		return false
	}
	testsGap := false
	for _, finding := range findings {
		if finding.Kind != "tests_green" && finding.Kind != "tests_exit" {
			continue
		}
		if finding.Status == string(engine.StatusUnverifiable) &&
			finding.Class == string(engine.ClassEnvironment) {
			testsGap = true
			break
		}
	}
	if !testsGap {
		return false
	}
	if _, err := os.Stat(filepath.Join(repo, verify.ProfilePath)); !errors.Is(err, os.ErrNotExist) {
		// Only a missing file gets the hint. A file the daemon cannot read is a
		// different finding, and "no profile in this repository" would be false.
		return false
	}
	return true
}

// doneVerifyBlockedMessage is the findings table the orchestrator reads.
//
// Each class carries what a reader needs to act on it and nothing else. An
// environment gap and a limit are about the machine or the run, not about the
// report, so they never send the agent back to it; the findings still follow,
// because both classes can come from a run that happened, one claim at a time.
// A refutation names the claim, the finding and the command that reproduces it,
// and a report that says too little brings the shape it has to be written in.
func doneVerifyBlockedMessage(agent storage.Agent, state *doneGateVerifyState) string {
	var builder strings.Builder
	// machineVerdict says the report is not at fault, so no correction of it is
	// asked for. It does not say that nothing was judged: a run that hit an
	// environment gap or a budget judges claim by claim, and those findings are
	// the only account of what happened.
	machineVerdict := false
	switch state.Kind {
	case "environment":
		machineVerdict = true
		builder.WriteString(fmt.Sprintf(
			"DONE-VERIFY-BLOCKED (environment, not the report): agent %s (%s) stays paused, which is no fault of the report but of the environment: %s",
			safeAgentText(agent.ID), safeAgentText(agent.Section), safeAgentText(state.Reason)))
	case "limit":
		machineVerdict = true
		builder.WriteString(fmt.Sprintf(
			"DONE-VERIFY-BLOCKED (limit, not the report): agent %s (%s) stays paused: limit %s, consumed %s",
			safeAgentText(agent.ID), safeAgentText(agent.Section),
			state.Bound.Round(time.Second), state.Consumed.Round(time.Second)))
	case "error":
		// A failed run judged nothing, so there is no finding below to report. The
		// command still follows: it is the one lever the agent has.
		machineVerdict = true
		builder.WriteString(fmt.Sprintf(
			"DONE-VERIFY-BLOCKED (verification failed): agent %s (%s) stays paused: %s",
			safeAgentText(agent.ID), safeAgentText(agent.Section), safeAgentText(state.Reason)))
	default:
		if state.AwaitingVerdict {
			if state.StaleBecause != "" {
				// Something moved. The report is the one thing that did not, so the
				// sentence must not say that it was never checked.
				builder.WriteString(fmt.Sprintf(
					"DONE-VERIFY-BLOCKED (%s): agent %s (%s) reported DONE, but the verdict for this report no longer applies: %s",
					safeAgentText(state.Kind), safeAgentText(agent.ID), safeAgentText(agent.Section), safeAgentText(state.StaleBecause)))
			} else {
				builder.WriteString(fmt.Sprintf(
					"DONE-VERIFY-BLOCKED (%s): agent %s (%s) reported DONE, and the report has not been verified: %s",
					safeAgentText(state.Kind), safeAgentText(agent.ID), safeAgentText(agent.Section), safeAgentText(state.Reason)))
			}
		} else if state.NoClaims || state.ReportEmpty {
			// No run happened, so the sentence must not claim that one judged the
			// report: it was refused for what it states.
			builder.WriteString(fmt.Sprintf(
				"DONE-VERIFY-BLOCKED (%s): agent %s (%s) reported DONE, and the report states no claim, so no verification was started",
				safeAgentText(state.Kind), safeAgentText(agent.ID), safeAgentText(agent.Section)))
		} else {
			builder.WriteString(fmt.Sprintf(
				"DONE-VERIFY-BLOCKED (%s): agent %s (%s) reported DONE, the report did not survive verification: %s",
				safeAgentText(state.Kind), safeAgentText(agent.ID), safeAgentText(agent.Section), safeAgentText(state.Reason)))
		}
	}
	builder.WriteString(findingLines(state.Findings))
	if state.ProfileInitHint {
		// The repository cannot tell the verifier what its suite is. Naming the
		// command that writes the profile keeps the fix in the deterministic
		// output instead of in a skill the reader may not have loaded.
		builder.WriteString(fmt.Sprintf(
			"\n  No verify profile in this repository (%s): `yesmem verify-claims --init` creates one, and only after a test run of its own proved it.",
			verify.ProfilePath))
	}
	if machineVerdict {
		// The report is not at fault, so it is not sent back to it. The command is
		// still printed: without the gate's own retry, it is the one lever the agent
		// or the operator has to reach a fresh verdict once the cause is gone.
		if state.Precondition != "" {
			builder.WriteString("\n  When the cause is gone, this produces a fresh verdict:\n  ")
			builder.WriteString(state.Precondition)
		}
		return builder.String()
	}
	if wantsReportTemplate(state) {
		builder.WriteString(fmt.Sprintf("\n  The report section %s ", safeAgentText(verifyReportSection(agent.Section))))
		switch {
		case state.ReportEmpty:
			builder.WriteString("carries no text.")
		case state.NoClaims:
			builder.WriteString("states no claim the verifier reads.")
		default:
			builder.WriteString("does not state every class the report profile requires.")
		}
		builder.WriteString(" Write it in this shape:\n")
		builder.WriteString(reportTemplate)
	} else if state.StaleBecause == "" {
		// A verdict that no longer applies is not a broken report. Telling the agent
		// to "fix" an unchanged section sends it to the one thing that is right.
		builder.WriteString(fmt.Sprintf(
			"\n  Fix the report section %s.", safeAgentText(verifyReportSection(agent.Section))))
	}
	if state.Precondition != "" {
		// The DONE gate runs no check of its own, so the verdict has to be produced
		// first. Naming the exact command is the deterministic part of the answer:
		// the agent copies it instead of reconstructing the flags from a rule.
		builder.WriteString("\n  Then produce the verdict and send DONE again:\n  ")
		builder.WriteString(state.Precondition)
	}
	return builder.String()
}

// relayVerifyMessage hands one message to the agent on the channel a paused
// agent reads when it continues. The agent is paused, so this is the one lever
// that lets it act without the orchestrator relaying by hand: the findings of a
// blocked verdict, and the fixed prompt of a hold. It reports whether the write
// left the daemon, because the hold's instruction is not one to lose silently.
func (h *Handler) relayVerifyMessage(agent storage.Agent, message string) bool {
	// Newlines in the body would be Enter keypresses in the PTY and split the
	// message into fragments, so they are escaped the way the relay handler does
	// it. The prefix names the sender: every guard relay carries one.
	body := strings.ReplaceAll(strings.ReplaceAll(message, "\r", ""), "\n", "\\n")
	wrapped := "[RELAY from=done-verify] " + safeAgentText(body)

	// Content and submit go on two connections with a settle delay between them:
	// a carriage return inside the same write is swallowed by the TUI's bracketed
	// paste, and the message would sit in the input field instead of arriving as a
	// turn (handler_agents.go relaySubmitDelay). Only the content write stays on
	// this path, but deliveries to one socket are serialised, so this call can wait
	// out a previous delivery's settle delay before it writes.
	injectPath := h.agentInjectPath(agent.ID)
	if injectPath == "" {
		log.Printf("[done-verify] relay to agent %s skipped: no derivable socket path", safeAgentText(agent.ID))
		return false
	}
	written := make(chan error, 1)
	go deliverRelay(injectPath, wrapped, written)
	if err := <-written; err != nil {
		log.Printf("[done-verify] relay to agent %s failed: %v", safeAgentText(agent.ID), safeAgentText(err.Error()))
		return false
	}
	return true
}

// doneVerifyEnabled reports the gate switch. An unconfigured handler runs the
// documented default, which is on: a daemon that never wired the section must
// not silently verify nothing.
func (h *Handler) doneVerifyEnabled() bool {
	enabled, _ := h.doneVerifySettings()
	return enabled
}

// doneVerifySettings reports the gate switch and the run budget. The budget is the
// default of a direct verify_claims call: that operation is the only place a
// verification runs, so its budget is the gate's budget.
func (h *Handler) doneVerifySettings() (enabled bool, budget time.Duration) {
	h.doneVerifyMu.Lock()
	cfg := h.doneVerify
	h.doneVerifyMu.Unlock()
	if cfg == nil {
		return true, rpcVerifyBudget
	}
	return cfg.Enabled, parseVerifyDuration(cfg.Budget, rpcVerifyBudget)
}

func (h *Handler) doneVerifyBudget() time.Duration {
	_, budget := h.doneVerifySettings()
	return budget
}

// doneVerifyMaxWait is how long a direct call waits for the single verification
// slot before it reports that another run is still working. The queue had this
// ceiling; with the queue gone, the caller's own call carries it.
func (h *Handler) doneVerifyMaxWait() time.Duration {
	h.doneVerifyMu.Lock()
	cfg := h.doneVerify
	h.doneVerifyMu.Unlock()
	if cfg == nil {
		return 10 * time.Minute
	}
	return parseVerifyDuration(cfg.MaxWait, 10*time.Minute)
}

// SetDoneVerifyConfig wires the DONE gate's verification configuration.
func (h *Handler) SetDoneVerifyConfig(cfg *config.DoneVerifyConfig) {
	h.doneVerifyMu.Lock()
	defer h.doneVerifyMu.Unlock()
	h.doneVerify = cfg
}

func parseVerifyDuration(value string, fallback time.Duration) time.Duration {
	if value == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(value)
	if err != nil || parsed <= 0 {
		log.Printf("[done-verify] ignoring an invalid duration %q, using %s", value, fallback)
		return fallback
	}
	return parsed
}

// verifyMergeBase is the base a diff-scope claim in a DONE report is judged
// from: the point where the work left its upstream. The upstream name differs
// between repositories, so every usual one is tried before the base is reported
// as unavailable; a repository without an upstream leaves the claim
// unverifiable rather than guessed.
func verifyMergeBase(repo string) string {
	for _, ref := range []string{"origin/HEAD", "origin/main", "origin/master", "main", "master"} {
		if base := verify.MergeBase(repo, ref); base != "" {
			return base
		}
	}
	return ""
}

// verifyHead is the commit the inspected tree stands on. It is part of the
// fingerprint, so work that lands after a verdict is judged again.
func verifyHead(repo string) string {
	return verify.Head(repo)
}

// verifyPlanFingerprint binds a verdict to the plan it was judged against. A
// blocked agent that closes its open plan steps must be verified again, and the
// plan text is the only thing that changes in that case.
func (h *Handler) verifyPlanFingerprint(thread string) string {
	if h.store == nil || thread == "" {
		return ""
	}
	source := storagePlanSource{store: h.store}
	items, found, err := source.Plan(thread)
	if err != nil || !found {
		return ""
	}
	digest := sha256.New()
	for _, item := range items {
		digest.Write([]byte(item.Status))
		digest.Write([]byte{0})
		digest.Write([]byte(item.Content))
		digest.Write([]byte{0})
	}
	return hex.EncodeToString(digest.Sum(nil))
}
