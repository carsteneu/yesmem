package claimtypes

import (
	"errors"
	"testing"

	"github.com/carsteneu/yesmem/internal/verify/engine"
)

// fakeSessions is a session store over a literal table. An id mapped to an
// error fails the lookup, which is the infrastructure case the rule has to tell
// apart from a missing session.
type fakeSessions map[string]engine.SessionRecord

func (f fakeSessions) Session(id string) (engine.SessionRecord, bool, error) {
	record, ok := f[id]
	return record, ok, nil
}

type failingSessions struct{ err error }

func (f failingSessions) Session(string) (engine.SessionRecord, bool, error) {
	return engine.SessionRecord{}, false, f.err
}

func evidencePolicy() engine.EvidencePolicy {
	return engine.EvidencePolicy{
		Order: []string{"cold-review", "consequence", "security"},
		Allowed: map[string][]string{
			"cold-review": {"reviewer", "silent-bob"},
			"consequence": {"general", "reviewer"},
			"security":    {"reviewer"},
		},
	}
}

func threeReviewers() fakeSessions {
	return fakeSessions{
		"ses_cold": {ID: "ses_cold", ParentID: "ses_parent", AgentType: "reviewer"},
		"ses_cons": {ID: "ses_cons", ParentID: "ses_parent", AgentType: "general"},
		"ses_sec":  {ID: "ses_sec", ParentID: "ses_parent", AgentType: "reviewer"},
	}
}

func threeRefs() []engine.EvidenceRef {
	return []engine.EvidenceRef{
		{Key: "cold-review", ID: "ses_cold"},
		{Key: "consequence", ID: "ses_cons"},
		{Key: "security", ID: "ses_sec"},
	}
}

// The evidence rule is the one thing a DONE report cannot fake cheaply: a pasted
// session id, a session of another agent, or the same id twice must all be
// findings, and a store that cannot answer must read as an environment gap
// rather than as the agent's fault.
func TestEvidenceClaim(t *testing.T) {
	duplicated := threeRefs()
	duplicated[1].ID = "ses_cold"

	notAllowed := threeRefs()
	notAllowed[2].ID = "ses_alien"

	alien := threeReviewers()
	alien["ses_alien"] = engine.SessionRecord{ID: "ses_alien", ParentID: "ses_parent", AgentType: "explore"}

	foreignParent := threeReviewers()
	foreignParent["ses_cold"] = engine.SessionRecord{ID: "ses_cold", ParentID: "ses_other", AgentType: "reviewer"}

	// The same slot twice: the first id would otherwise never be resolved while
	// the report still reads as complete.
	sameSlotTwice := append(threeRefs(), engine.EvidenceRef{Key: "cold-review", ID: "ses_alien"})

	// A slot the policy pins but the report leaves out: the gap must be a
	// finding, not an omission the checker fills in.
	missingSlot := threeRefs()[:2]

	cases := []struct {
		name      string
		refs      []engine.EvidenceRef
		sessions  engine.SessionStore
		parent    string
		want      engine.Status
		wantClass engine.Class
	}{
		{
			name:     "three distinct subagents of this session are confirmed",
			refs:     threeRefs(),
			sessions: threeReviewers(),
			parent:   "ses_parent",
			want:     engine.StatusConfirmed,
		},
		{
			name:      "a session that does not exist is a defect",
			refs:      threeRefs(),
			sessions:  fakeSessions{"ses_cold": {ID: "ses_cold", ParentID: "ses_parent", AgentType: "reviewer"}},
			parent:    "ses_parent",
			want:      engine.StatusUnverifiable,
			wantClass: engine.ClassDefect,
		},
		{
			name:      "one id under two sub-phases is a defect",
			refs:      duplicated,
			sessions:  threeReviewers(),
			parent:    "ses_parent",
			want:      engine.StatusUnverifiable,
			wantClass: engine.ClassDefect,
		},
		{
			name:      "one slot naming two ids is a defect",
			refs:      sameSlotTwice,
			sessions:  threeReviewers(),
			parent:    "ses_parent",
			want:      engine.StatusUnverifiable,
			wantClass: engine.ClassDefect,
		},
		{
			name:      "a pinned slot the report leaves out is a defect",
			refs:      missingSlot,
			sessions:  threeReviewers(),
			parent:    "ses_parent",
			want:      engine.StatusUnverifiable,
			wantClass: engine.ClassDefect,
		},
		{
			name:      "an agent type outside the slot is a defect",
			refs:      notAllowed,
			sessions:  alien,
			parent:    "ses_parent",
			want:      engine.StatusUnverifiable,
			wantClass: engine.ClassDefect,
		},
		{
			name:      "a subagent of another session is a defect",
			refs:      threeRefs(),
			sessions:  foreignParent,
			parent:    "ses_parent",
			want:      engine.StatusUnverifiable,
			wantClass: engine.ClassDefect,
		},
		{
			name:      "an unreadable store is an environment gap, not a defect",
			refs:      threeRefs(),
			sessions:  failingSessions{err: errors.New("database is locked")},
			parent:    "ses_parent",
			want:      engine.StatusUnverifiable,
			wantClass: engine.ClassEnvironment,
		},
		{
			name:      "no store configured is an environment gap",
			refs:      threeRefs(),
			sessions:  nil,
			parent:    "ses_parent",
			want:      engine.StatusUnverifiable,
			wantClass: engine.ClassEnvironment,
		},
		{
			name:      "no reported session is an environment gap",
			refs:      threeRefs(),
			sessions:  threeReviewers(),
			parent:    "",
			want:      engine.StatusUnverifiable,
			wantClass: engine.ClassEnvironment,
		},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			ctx := &engine.Ctx{
				Sessions:      test.sessions,
				Evidence:      evidencePolicy(),
				ReportSession: test.parent,
			}
			verdict := CheckEvidence(engine.Claim{Kind: "evidence_ids", Evidence: test.refs}, ctx)

			if verdict.Status != test.want {
				t.Fatalf("Status = %s (%s), want %s", verdict.Status, verdict.Reason, test.want)
			}
			if verdict.Class != test.wantClass {
				t.Errorf("Class = %q (%s), want %q", verdict.Class, verdict.Reason, test.wantClass)
			}
			if verdict.Reason == "" {
				t.Error("Reason = empty, want the finding the reader acts on")
			}
		})
	}
}

// fakePlans is a plan store over a literal table.
type fakePlans map[string][]engine.PlanItem

func (f fakePlans) Plan(thread string) ([]engine.PlanItem, bool, error) {
	items, ok := f[thread]
	return items, ok, nil
}

// A DONE report that names a plan which still has open steps contradicts
// itself, and that contradiction is a defect: the plan is the loop's own
// record of what is unfinished.
func TestPlanItemsClaim(t *testing.T) {
	cases := []struct {
		name      string
		plans     engine.PlanSource
		thread    string
		want      engine.Status
		wantClass engine.Class
	}{
		{
			name:   "a plan whose steps are all completed is confirmed",
			plans:  fakePlans{"t1": {{Content: "a", Status: "completed"}, {Content: "b", Status: "cancelled"}}},
			thread: "t1",
			want:   engine.StatusConfirmed,
		},
		{
			name:      "a pending step contradicts the DONE claim",
			plans:     fakePlans{"t1": {{Content: "a", Status: "completed"}, {Content: "ship it", Status: "pending"}}},
			thread:    "t1",
			want:      engine.StatusUnverifiable,
			wantClass: engine.ClassDefect,
		},
		{
			name:      "a step in progress contradicts the DONE claim",
			plans:     fakePlans{"t1": {{Content: "still running", Status: "in_progress"}}},
			thread:    "t1",
			want:      engine.StatusUnverifiable,
			wantClass: engine.ClassDefect,
		},
		{
			name:      "a step without a status counts as open",
			plans:     fakePlans{"t1": {{Content: "no status"}}},
			thread:    "t1",
			want:      engine.StatusUnverifiable,
			wantClass: engine.ClassDefect,
		},
		{
			name:      "a thread without a plan is a gap in what the caller supplied",
			plans:     fakePlans{},
			thread:    "t1",
			want:      engine.StatusUnverifiable,
			wantClass: engine.ClassEnvironment,
		},
		{
			name:      "no plan source is an environment gap",
			plans:     nil,
			thread:    "t1",
			want:      engine.StatusUnverifiable,
			wantClass: engine.ClassEnvironment,
		},
		{
			name:      "no thread named by the caller is an environment gap",
			plans:     fakePlans{"t1": {{Content: "a", Status: "completed"}}},
			thread:    "",
			want:      engine.StatusUnverifiable,
			wantClass: engine.ClassEnvironment,
		},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			ctx := &engine.Ctx{Plans: test.plans, PlanThread: test.thread}
			verdict := CheckPlanItems(engine.Claim{Kind: "plan_items", Plan: "yesmem"}, ctx)

			if verdict.Status != test.want {
				t.Fatalf("Status = %s (%s), want %s", verdict.Status, verdict.Reason, test.want)
			}
			if verdict.Class != test.wantClass {
				t.Errorf("Class = %q (%s), want %q", verdict.Class, verdict.Reason, test.wantClass)
			}
		})
	}
}
