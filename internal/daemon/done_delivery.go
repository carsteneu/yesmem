package daemon

import (
	"fmt"
	"log"
	"strings"

	"github.com/carsteneu/yesmem/internal/storage"
	"github.com/carsteneu/yesmem/internal/verify"
)

// The delivery side of the DONE contract.
//
// The gate (done_gate_verify.go) keeps an agent from keeping a DONE claim it has
// not proven. This file is the counterpart on the wire: a send_to that announces
// DONE is not handed to the orchestrator before the verdict for exactly this
// report is in the registry. The decision is a lookup, never a run: the expensive
// check belongs to verify_claims, whose judgement this path reuses.
//
// Everything else about send_to stays as it was. A message that does not announce
// DONE is stored and delivered untouched, and its whole cost on this path is one
// prefix test on the trimmed start of the body.

// doneDeliveryMarkers are the two shapes a Phase 6 announcement takes: the marker
// line the skill prescribes ("[DONE] [DEPLOY: ...]") and the plain sentence the
// spawn prompt asks for ("DONE: Section '...' is complete."). They are matched
// case-sensitively, at the front of the message and nowhere else: prose that
// mentions the word is not a DONE claim, and a lowercase "done:" is not one
// either. A DONE this test misses is still caught by the gate's next tick, which
// reads the scratchpad; the delivery path is the fast lane, not the only one.
var doneDeliveryMarkers = []string{"[DONE]", "DONE:"}

// doneDeliveryVerdictLine is what a released DONE carries besides the sender's own
// text. The orchestrator reads the DONE and the fact that a verdict backs it in one
// delivery, which is why the release needs no second message.
const doneDeliveryVerdictLine = "verdict: accepted (pre-check)"

// isDONEPayload reports whether a message body announces DONE. The marker has to
// sit at the front of the trimmed text: blank lines in front of it are still a
// DONE, prose that mentions the word is not, and a lowercase "done:" is not one
// either. The trim covers the Unicode spaces a copy-paste or a rich editor
// leaves in front of a line (NBSP, BOM, zero-width space), because those are
// invisible to the sender who thinks the marker is at the front.
func isDONEPayload(content string) bool {
	trimmed := strings.TrimLeft(content, " \t\r\n\u00a0\ufeff\u200b")
	for _, marker := range doneDeliveryMarkers {
		if strings.HasPrefix(trimmed, marker) {
			return true
		}
	}
	return false
}

// doneDelivery is the outcome for one send_to: the payload to deliver, or the
// refusal the sender has to read instead of a delivery.
type doneDelivery struct {
	content  string
	held     bool
	response Response
}

// settleDONEDelivery is the hook handleSendTo runs before it stores anything. It
// returns the payload to deliver, or held=true with the answer the sender gets.
func (h *Handler) settleDONEDelivery(resolved, caller, content string) doneDelivery {
	delivered := doneDelivery{content: content}
	agent := h.agentForDONEDelivery(resolved, caller, content)
	if agent == nil {
		return delivered
	}
	// A row that already left the loop is not re-entered: the orchestrator decides
	// it, and asking the agent again is the fourth round the escalation prevents.
	if escalatedHold(*agent) {
		delivered.held = true
		delivered.response = errorResponse(doneVerifyEscalatedPrompt)
		return delivered
	}

	repo := agent.WorkDir
	if repo == "" {
		repo = agent.Project
	}
	report, readErr := h.verifyReport(*agent)
	if readErr != nil {
		// The report is unknown rather than absent. That is the infrastructure gap
		// the gate reports as such, and it is no licence to deliver a DONE that
		// nothing was checked against. The key is the gate's own for the same read
		// error, so its next tick recognises the block instead of placing another.
		context := h.verifyContextFor(*agent, repo, "")
		context.key = verifyFingerprint(repo, readErr.Error(), context.reportSession, "",
			context.head, context.worktree, context.planRevision, context.branch)
		return h.refuseDONE(*agent, context, doneRefusal{
			kind:     "environment",
			reason:   fmt.Sprintf("the report section %s could not be read: %v", verifyReportSection(agent.Section), readErr),
			decision: "environment",
		})
	}

	context := h.verifyContextFor(*agent, repo, report)
	if len(verify.ParseReport(report)) == 0 {
		// A report without a claim has nothing a verdict could be about, so no
		// lookup would ever match it. Answering the shape is the whole help.
		empty := strings.TrimSpace(report) == ""
		reason := "the report states no claim, so there is nothing to verify"
		if empty {
			reason = "the report section carries no text, so there is nothing to verify"
		}
		return h.refuseDONE(*agent, context, doneRefusal{
			kind:        "defect",
			reason:      reason,
			reportEmpty: empty,
			noClaims:    !empty,
			decision:    presenceDecision(context),
		})
	}

	verdict, inFlight := h.verdictAndRun(context.key)
	if verdict == nil && inFlight {
		// The pre-check is running right now. That is not a rejected attempt: the
		// gate holds the agent on the same state without counting a round, and this
		// path answers the same way, so three impatient sends cannot escalate a
		// report nobody has judged yet.
		h.holdOnRunningPrecheck(*agent, context, h.doneGateVerifyStateFor(agent.ID), true)
		return doneDelivery{held: true, response: errorResponse(doneVerifyFrozenPrompt)}
	}
	if verdict == nil {
		return h.refuseDONE(*agent, context, doneRefusal{
			kind:     "defect",
			reason:   "no verdict exists for this report: a DONE is delivered only after the pre-check",
			stale:    h.staleReasonFor(*agent, context),
			decision: verdictDecision(nil),
		})
	}
	switch {
	case verdict.Mode != doneVerifyRequiredMode():
		// A judgement under a weaker rule set judged fewer classes, so its
		// acceptance proves less than the DONE gate promises.
		return h.refuseDONE(*agent, context, doneRefusal{
			kind:     "defect",
			reason:   fmt.Sprintf("the pre-check judged this report as %s, the DONE gate needs %s", verdict.Mode, doneVerifyRequiredMode()),
			decision: verdictDecision(verdict),
			basis:    verdict,
		})
	case verdict.Status != doneVerifyAccepted:
		// The run said no. Its class and findings are what the sender has to act
		// on, so they travel with the refusal instead of a summary of them.
		reason := verdict.Reason
		if reason == "" {
			reason = fmt.Sprintf("the pre-check refused this report as %s", verdict.Kind)
		}
		return h.refuseDONE(*agent, context, doneRefusal{
			kind:     verdict.Kind,
			reason:   reason,
			decision: verdictDecision(verdict),
			basis:    verdict,
		})
	}

	h.recordDONEDelivered(*agent, context, verdict)
	delivered.content = content + "\n" + doneDeliveryVerdictLine
	return delivered
}

// staleReasonFor names the fact that moved since the last judgement, so a block
// can say what to look at instead of claiming nothing was ever checked. The
// agent's own last state is the closest witness; a verdict about this same report
// may exist without one (the plan, the tree or the branch moved before the first
// DONE), and then the registry is.
func (h *Handler) staleReasonFor(agent storage.Agent, context verifyContext) string {
	basis := h.doneGateVerifyStateFor(agent.ID)
	if basis == nil {
		basis = h.verdictForSameReport(context)
	}
	return staleBecause(basis, context)
}

// doneRefusal is one answer of the delivery check: why the DONE is withheld and
// the decision the gate would reach for the same report. Carrying the decision
// keeps the two entry points on one verdict: a tick that lands afterwards finds
// its own decision already stored, so it pauses and announces nothing again.
type doneRefusal struct {
	kind        string
	reason      string
	stale       string
	reportEmpty bool
	noClaims    bool
	decision    string
	basis       *doneGateVerifyState
}

// refuseDONE records the refusal on the agent's row and renders both answers: the
// sender gets the message with the command that produces a verdict, the
// orchestrator gets the state change and never the DONE.
func (h *Handler) refuseDONE(agent storage.Agent, context verifyContext, refusal doneRefusal) doneDelivery {
	previous := h.doneGateVerifyStateFor(agent.ID)
	state := &doneGateVerifyState{
		Fingerprint:     context.key,
		Status:          doneVerifyBlocked,
		Kind:            refusal.kind,
		Reason:          refusal.reason,
		StaleBecause:    refusal.stale,
		ReportEmpty:     refusal.reportEmpty,
		NoClaims:        refusal.noClaims,
		AwaitingVerdict: refusal.basis == nil,
		DecisionSig:     refusal.decision,
		ReportDigest:    context.reportDigest,
		Head:            context.head,
		Worktree:        context.worktree,
		Branch:          context.branch,
		PlanRevision:    context.planRevision,
		ReportSession:   context.reportSession,
		Precondition:    verifyClaimsCommand(agent, context.repo),
		Notified:        notifiedFor(previous, context.key),
		// The payload never left, so a later release must ask for it again instead
		// of declaring the agent finished.
		PayloadWithheld: true,
	}
	if state.Kind == "" {
		state.Kind = "defect"
	}
	if refusal.basis != nil {
		state.Findings = refusal.basis.Findings
		state.ProfileInitHint = refusal.basis.ProfileInitHint
		state.Bound = refusal.basis.Bound
		state.Consumed = refusal.basis.Consumed
	}
	// The round belongs to the decision, not to the message: the same refusal about
	// the same report is one rejected attempt however often it is sent, and the
	// gate would count it once on its own tick. Without this, one failed correction
	// escalated the case because both entry points counted it.
	if previous != nil && previous.Fingerprint == context.key && previous.DecisionSig == state.DecisionSig {
		state.BlockedRounds = previous.BlockedRounds
		state.RoundNotes = previous.RoundNotes
		state.Escalated = previous.Escalated
		state.Notified = previous.Notified
	} else {
		countBlockedRound(state, attemptRounds(agent, previous))
	}
	news := recordVerdictNotice(state)
	h.replaceDoneGateVerifyState(agent.ID, state)
	h.announceWithheldDONE(agent, state, news)
	return doneDelivery{held: true, response: errorResponse(doneVerifyBlockedMessage(agent, state))}
}

// announceWithheldDONE is the single place a withheld DONE reaches the two rows it
// concerns: the agent's status line always, the orchestrator once per refusal. The
// escalation is the third refusal's answer, and it takes the case out of the loop
// instead of asking for a fourth attempt.
func (h *Handler) announceWithheldDONE(agent storage.Agent, state *doneGateVerifyState, news bool) {
	if state.Escalated {
		h.escalateBlockedReport(agent, state)
		return
	}
	h.pauseAgent(agent.ID, fmt.Sprintf("DONE-VERIFY-BLOCKED (%s): %s", safeAgentText(state.Kind), safeAgentText(state.Reason)))
	if !news {
		return
	}
	log.Printf("[done-delivery] agent %s (%s): DONE withheld (%s)", safeAgentText(agent.ID), safeAgentText(agent.Section), safeAgentText(state.Kind))
	h.notifyOrchestrator(agent, doneWithheldNotice(agent, state))
}

// doneWithheldNotice is the whole of what the orchestrator hears about a withheld
// DONE: the class, the findings, the fact that the message did not reach it, and
// that the agent is held with the command. The DONE itself stays where it was.
// The findings and the command are printed in full, because the reader has to be
// able to act on this notice itself; the agent may be gone by the time it arrives.
func doneWithheldNotice(agent storage.Agent, state *doneGateVerifyState) string {
	notice := fmt.Sprintf(
		"DONE-WITHHELD (%s): agent %s (%s) sent a DONE payload, and the message was withheld because %s. The agent is held and has the command that produces the verdict. Round %d/%d of this report.",
		safeAgentText(state.Kind), safeAgentText(agent.ID), safeAgentText(agent.Section),
		safeAgentText(state.Reason), state.BlockedRounds, doneVerifyEscalateAfter)
	notice += findingLines(state.Findings)
	if state.Precondition != "" {
		notice += "\n  " + state.Precondition
	}
	return notice
}

// findingLines renders the verdict's findings as the numbered list both notices
// carry. Empty findings are no line at all: a header without a finding reads as
// a truncated message.
func findingLines(findings []verifyClaimFinding) string {
	if len(findings) == 0 {
		return ""
	}
	var builder strings.Builder
	for index, finding := range findings {
		if index == maxReportedFindings {
			break
		}
		builder.WriteString(fmt.Sprintf("\n  %d. %s %s: %s",
			index+1, safeAgentText(finding.Kind), safeAgentText(finding.Status), safeAgentText(finding.Reason)))
		if finding.Command != "" {
			builder.WriteString(fmt.Sprintf("\n     check (from the report): %s", safeAgentText(finding.Command)))
		}
	}
	return builder.String()
}

// recordDONEDelivered stores the released verdict on the agent row the same way
// the gate does when it reads an accepted verdict. The tick that follows finds the
// decision taken and its notice paid, so it releases nothing twice: the
// orchestrator has the DONE with its verdict line, and the status line is the
// second half of the state change.
func (h *Handler) recordDONEDelivered(agent storage.Agent, context verifyContext, verdict *doneGateVerifyState) {
	state := *verdict
	state.Fingerprint = context.key
	state.ReportDigest = context.reportDigest
	state.Head = context.head
	state.Worktree = context.worktree
	state.Branch = context.branch
	state.PlanRevision = context.planRevision
	state.ReportSession = context.reportSession
	state.Precondition = ""
	state.StaleBecause = ""
	state.AwaitingVerdict = false
	state.Reused = true
	state.DecisionSig = verdictDecision(verdict)
	state.BlockedRounds = 0
	state.RoundNotes = nil
	state.Escalated = false
	recordVerdictNotice(&state)
	h.replaceDoneGateVerifyState(agent.ID, &state)
	if agent.Status == "paused" && heldByVerify(agent) && !agentProcessGone(agent) {
		// Only this gate's own hold is lifted. Another guard's pause (idle,
		// stagnation, done-guard) has its own reason and its own recovery, and a
		// row whose process is gone belongs to crashRecovery: flipping it to
		// running would announce a live agent that nothing backs.
		h.unpauseAgent(agent.ID, "done-delivery", "the DONE payload was delivered on the pre-check's verdict")
	}
}

// agentForDONEDelivery resolves the row a DONE payload can be judged against, or
// nil when this message is not the loop's DONE.
//
// The identity is the daemon's proven caller, the same one the implicit ACK uses.
// It is asserted by the client: the socket is same-UID and send_to's sender label
// never was a credential, so a caller that knows another agent's session can act on
// that agent's row here exactly as it could elsewhere on this socket. This function
// takes the free `sender` parameter into no decision at all, and a caller that
// proves nothing is not held; the gate's tick remains the backstop.
func (h *Handler) agentForDONEDelivery(resolved, caller, content string) *storage.Agent {
	if h.store == nil || !isDONEPayload(content) {
		return nil
	}
	if !h.doneVerifyEnabled() {
		return nil
	}
	if caller == "" {
		log.Printf("[done-delivery] DONE payload for %q not settled: the caller proved no session", safeAgentText(resolved))
		return nil
	}
	agent, err := h.store.AgentGetAnyBySession(caller)
	if err != nil || agent == nil {
		return nil
	}
	if !isLoopSection(agent.Section) {
		// Every spawned agent announces its handover in this shape, and a report
		// section is what makes a verdict possible. Holding a section the loop never
		// writes would deflect a working handover into a check that does not apply.
		return nil
	}
	if !sameChannelAddress(resolved, agent.CallerSession) {
		// The orchestrator is the reader this holds. A DONE addressed to a peer is
		// not the delivery the contract is about.
		return nil
	}
	return agent
}

// isLoopSection reports whether a section belongs to the loop family whose DONE
// contract this is. yesresearch sections share the verify contract (the skill has
// them write a <section>-verify report) while having no gate tick of their own.
func isLoopSection(section string) bool {
	return strings.HasPrefix(section, loopSectionPrefix) || strings.HasPrefix(section, "yesresearch-")
}

// sameChannelAddress compares two addresses in their stored form.
func sameChannelAddress(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	return storage.CanonicalAddress(a) == storage.CanonicalAddress(b)
}
