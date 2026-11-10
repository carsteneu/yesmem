package daemon

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/carsteneu/yesmem/internal/models"
	"github.com/carsteneu/yesmem/internal/storage"
	"github.com/carsteneu/yesmem/internal/verify/claimtypes"
	"github.com/carsteneu/yesmem/internal/verify/engine"
)

// evidenceLookupTimeout bounds one session lookup for the DONE-guard's Phase 5
// evidence check. The check runs inside the daemon's heartbeat goroutine once
// per evidence key, so the whole lookup is bounded here rather than per table.
const evidenceLookupTimeout = 5 * time.Second

// allowedReviewAgents maps each Phase 5 evidence key to the opencode agent
// names permitted to fill that slot. The task() dispatches behind the ids are
// agent-typed in opencode's session table; the sets below pin which agent type
// may serve which review sub-phase.
var allowedReviewAgents = map[string][]string{
	"cold-review": {"reviewer", "silent-bob"},
	"consequence": {"general", "reviewer"},
	"security":    {"reviewer"},
}

// phase5EvidenceOrder pins the evidence key order so validation errors are
// deterministic despite map iteration.
var phase5EvidenceOrder = []string{"cold-review", "consequence", "security"}

// queryOpencodeSession looks up a session row in the opencode session database.
// found=false means the row does not exist (agent fault). An error means the
// lookup itself failed — locked DB, missing sqlite3 binary, schema drift — an
// infrastructure problem, not an agent fault. Variable so tests can stub the
// lookup hermetically.
//
// Both session tables are consulted (v1 `session`, v2 `session_v2`): v2 agents
// have a captured session id in `session_v2` only, so without the fallback the
// evidence check would reject every v2 agent's Phase 5 block (Learning #101594).
var queryOpencodeSession = func(ocDBPath, sessionID string) (found bool, parentID, agentType string, err error) {
	escaped := strings.ReplaceAll(sessionID, "'", "''")
	// A total budget for the whole lookup: this runs inside the shared heartbeat
	// goroutine once per evidence key, so it must not multiply by the number of
	// session tables.
	ctx, cancel := context.WithTimeout(context.Background(), evidenceLookupTimeout)
	defer cancel()
	row, err := queryFirstSessionRow(ctx, ocDBPath, func(table string) string {
		return fmt.Sprintf("SELECT parent_id, agent FROM %s WHERE id = '%s' LIMIT 1", table, escaped)
	})
	if err != nil {
		return false, "", "", err
	}
	if row == "" {
		return false, "", "", nil
	}
	parts := strings.SplitN(row, "|", 2)
	if len(parts) != 2 {
		return false, "", "", fmt.Errorf("unexpected sqlite3 output %q", row)
	}
	return true, parts[0], parts[1], nil
}

// VerifySubagentEvidence checks Phase 5 evidence session ids. evidence maps the
// sub-phase key (cold-review, consequence, security) to the reported session id.
//
// The rule itself lives in the verifier (claimtypes.EvidenceFindings): per key
// the row exists, its parent is the report's session, the agent type is in the
// allowed set, and no id serves two keys. This function is the adapter the
// verification operation uses when it has only opencode's database; the gate
// goes through the yesmem session store instead (Handler.verifyPhaseEvidence).
// Both feed the same rule, so neither can drift.
//
// An empty ocDBPath or parentSessionID skips verification — that is a
// configuration, not a failure: the check cannot run at all. A named database
// that cannot be read, and a lookup that fails, are findings: an unverifiable id
// is not a verified one, and the report is the only thing the DONE claim rests
// on (YM-304). The guard pauses the agent on a finding and unpauses it once the
// scratchpad is compliant, so the closed direction is the one that heals itself.
func VerifySubagentEvidence(ocDBPath, parentSessionID string, evidence map[string]string) []FieldError {
	if ocDBPath == "" || parentSessionID == "" || len(evidence) == 0 {
		return nil
	}
	return evidenceFindings(evidence, parentSessionID, opencodeEvidenceStore{dbPath: ocDBPath})
}

// verifyPhaseEvidence is the DONE gate's entry into the same rule, over the
// yesmem session store: the store is the basis because it is harness-neutral,
// opencode's database only enriches what the store cannot answer (the subagent's
// agent type, and an id whose session row has not been indexed yet).
func (h *Handler) verifyPhaseEvidence(agent storage.Agent, evidence map[string]string) []FieldError {
	if len(evidence) == 0 || agent.OpencodeSessionID == "" {
		// No reported session to attribute the ids to. The verification operation
		// still checks the same evidence once the report is verified, and it does so
		// for every harness.
		return nil
	}
	if h.store == nil {
		return evidenceFindings(evidence, agent.OpencodeSessionID, opencodeEvidenceStore{dbPath: h.ocDBPath})
	}
	store := sessionEvidenceStore{store: h.store, ocDBPath: h.ocDBPath}
	return evidenceFindings(evidence, agent.OpencodeSessionID, store)
}

// evidenceStore is a session store that also knows the shape of the parent id it
// compares against: the yesmem store keys opencode sessions namespaced, so the
// bare id an agent reports has to be normalized before it is matched.
type evidenceStore interface {
	engine.SessionStore
	Parent(sessionID string) string
}

// evidenceFindings runs the shared evidence rule and maps its findings onto the
// gate's field errors. Phase is always 5: the rule exists for that block.
func evidenceFindings(evidence map[string]string, parentSessionID string, store evidenceStore) []FieldError {
	var refs []engine.EvidenceRef
	for _, key := range phase5EvidenceOrder {
		if id, ok := evidence[key]; ok && id != "" {
			refs = append(refs, engine.EvidenceRef{Key: key, ID: id})
		}
	}
	findings := claimtypes.EvidenceFindings(refs, store.Parent(parentSessionID), store, engine.EvidencePolicy{
		Order:   phase5EvidenceOrder,
		Allowed: allowedReviewAgents,
	})
	errs := make([]FieldError, 0, len(findings))
	for _, finding := range findings {
		// The detail carries agent-influenced values (the reported session id, the
		// stored parent) and reaches relay bodies and orchestrator messages, so it
		// is neutralized where it enters the message (YM-285).
		errs = append(errs, FieldError{Phase: 5, Field: finding.Field, Detail: safeAgentText(finding.Detail)})
	}
	return errs
}

// opencodeEvidenceStore answers over opencode's own session database. The ids
// are bare there, so the parent is compared as reported.
type opencodeEvidenceStore struct {
	dbPath string
}

func (s opencodeEvidenceStore) Parent(sessionID string) string { return sessionID }

func (s opencodeEvidenceStore) Session(id string) (engine.SessionRecord, bool, error) {
	found, parentID, agentType, err := queryOpencodeSession(s.dbPath, id)
	if err != nil {
		return engine.SessionRecord{}, false, err
	}
	if !found {
		return engine.SessionRecord{}, false, nil
	}
	return engine.SessionRecord{ID: id, ParentID: parentID, AgentType: agentType}, true, nil
}

// sessionEvidenceStore answers over the yesmem session store and enriches what
// that store does not hold for an opencode session: its agent type, and a
// session row the indexer has not caught up with yet.
//
// The store keys every non-Claude session namespaced ("opencode:ses_x"), while a
// report cites the bare id an agent was handed. Both sides are normalized to the
// bare form here, so the rule compares ids and never a namespace: the same check
// must work for a harness whose ids carry no prefix at all.
type sessionEvidenceStore struct {
	store    *storage.Store
	ocDBPath string
}

func (s sessionEvidenceStore) Parent(sessionID string) string {
	return bareSessionID(sessionID)
}

func (s sessionEvidenceStore) Session(id string) (engine.SessionRecord, bool, error) {
	bare := bareSessionID(id)
	session, err := s.store.GetSession(bare)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		// A bare key without a row is the normal case for opencode; try the
		// namespaced key the indexer writes before calling it missing.
		session, err = s.store.GetSession(models.NormalizeSessionID(models.SourceAgentOpencode, bare))
	}
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return s.enrichFromOpencode(bare)
		}
		return engine.SessionRecord{}, false, err
	}
	record := engine.SessionRecord{
		ID:        bare,
		ParentID:  bareSessionID(session.ParentSessionID),
		AgentType: session.AgentType,
	}
	if record.AgentType != "" {
		return record, true, nil
	}
	enriched, found, _ := s.enrichFromOpencode(bare)
	if found {
		record.AgentType = enriched.AgentType
	}
	return record, true, nil
}

// enrichFromOpencode asks opencode's database for a session the store could not
// answer for. A failed query returns not-found: the store already had nothing,
// so the enrichment is best effort and must not turn into a second error.
func (s sessionEvidenceStore) enrichFromOpencode(id string) (engine.SessionRecord, bool, error) {
	if s.ocDBPath == "" {
		return engine.SessionRecord{}, false, nil
	}
	found, parentID, agentType, err := queryOpencodeSession(s.ocDBPath, id)
	if err != nil || !found {
		return engine.SessionRecord{}, false, nil
	}
	return engine.SessionRecord{
		ID:        id,
		ParentID:  bareSessionID(parentID),
		AgentType: agentType,
	}, true, nil
}

// bareSessionID drops the source namespace the session store writes in front of
// a non-Claude id. Only the three known namespaces are stripped: an id that
// happens to carry a colon of its own must survive unchanged.
func bareSessionID(id string) string {
	for _, source := range []string{models.SourceAgentOpencode, models.SourceAgentCodex} {
		prefix := source + ":"
		if strings.HasPrefix(id, prefix) {
			return strings.TrimPrefix(id, prefix)
		}
	}
	return id
}
