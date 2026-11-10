package daemon

import (
	"testing"
	"time"

	"github.com/carsteneu/yesmem/internal/models"
	"github.com/carsteneu/yesmem/internal/storage"
)

// The gate resolves Phase 5 evidence over the yesmem session store, and the two
// details that decide whether that works are both easy to get wrong: the store
// keys sessions namespaced ("opencode:ses_x") while the reported id is bare,
// and it does not carry the agent type of an opencode subagent, so a check that
// skipped the enrichment would reject every reviewer. A store that lags the
// index must not reject an id opencode.db still knows.
func TestPhaseEvidenceResolvesThroughSessionStore(t *testing.T) {
	cases := []struct {
		name     string
		storeRow bool
	}{
		{name: "the store row is found and the agent type comes from opencode.db", storeRow: true},
		{name: "an index-lagging store lets opencode.db answer", storeRow: false},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			h, s := mustFileHandler(t)
			h.ocDBPath = evidenceDBPath(t)
			withEvidenceRows(t, evidenceFixtureRows())
			if test.storeRow {
				for id, parent := range map[string]string{"ses_cold": "ses_parent", "ses_cons": "ses_parent", "ses_sec": "ses_parent"} {
					if err := s.UpsertSession(&models.Session{
						ID:              "opencode:" + id,
						Project:         "testproj",
						ParentSessionID: "opencode:" + parent,
						SourceAgent:     models.SourceAgentOpencode,
						StartedAt:       time.Now(),
						IndexedAt:       time.Now(),
					}); err != nil {
						t.Fatalf("UpsertSession: %v", err)
					}
				}
			}

			agent := storage.Agent{ID: "evidence", OpencodeSessionID: "ses_parent"}
			errs := h.verifyPhaseEvidence(agent, evidenceFixtureEvidence())

			if len(errs) != 0 {
				t.Fatalf("findings = %v, want none: the ids resolve to subagents of the report's session", errs)
			}
		})
	}
}
