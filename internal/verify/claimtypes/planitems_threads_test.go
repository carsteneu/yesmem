package claimtypes

import (
	"errors"
	"strings"
	"testing"

	"github.com/carsteneu/yesmem/internal/verify/engine"
)

// Ein yesloop-Agent plant unter seiner Backend-Session, waehrend seine Zeile
// zusaetzlich eine Daemon-UUID traegt. Der Plan-Claim muss den Plan unter beiden
// Kennungen finden, sonst ist er fuer den Agenten strukturell unerfuellbar. Eine
// Kennung, die dem Agenten nicht gehoert, darf dagegen nie den Ausschlag geben:
// sonst unterschiebt ein Bericht einen fremden Plan.
func TestPlanItemsClaimResolvesAcrossAgentThreads(t *testing.T) {
	completed := []engine.PlanItem{{Content: "do the work", Status: "completed"}}
	cases := []struct {
		name       string
		plans      engine.PlanSource
		thread     string
		threads    []string
		want       engine.Status
		wantClass  engine.Class
		wantThread string
	}{
		{
			name:       "a plan under the backend thread is found although the primary thread holds none",
			plans:      fakePlans{"opencode:ses_live": completed},
			thread:     "daemon-uuid",
			threads:    []string{"daemon-uuid", "opencode:ses_live"},
			want:       engine.StatusConfirmed,
			wantThread: "opencode:ses_live",
		},
		{
			name:       "an open step found under a fallback thread still blocks",
			plans:      fakePlans{"opencode:ses_live": {{Content: "ship it", Status: "pending"}}},
			thread:     "daemon-uuid",
			threads:    []string{"daemon-uuid", "opencode:ses_live"},
			want:       engine.StatusUnverifiable,
			wantClass:  engine.ClassDefect,
			wantThread: "ship it",
		},
		{
			name:       "a plan under a foreign thread is not accepted",
			plans:      fakePlans{"opencode:ses_other": completed},
			thread:     "daemon-uuid",
			threads:    []string{"daemon-uuid", "opencode:ses_live"},
			want:       engine.StatusUnverifiable,
			wantClass:  engine.ClassEnvironment,
			wantThread: "wrote no plan",
		},
		{
			name:   "no plan anywhere says the agent wrote none",
			plans:  fakePlans{},
			thread: "daemon-uuid",
			// The message has to name the agent's own threads: "thread X has no
			// plan" without a reference was the sentence that hid the bug.
			threads:    []string{"daemon-uuid", "opencode:ses_live"},
			want:       engine.StatusUnverifiable,
			wantClass:  engine.ClassEnvironment,
			wantThread: "opencode:ses_live",
		},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			ctx := &engine.Ctx{Plans: test.plans, PlanThread: test.thread, PlanThreads: test.threads}
			verdict := CheckPlanItems(engine.Claim{Kind: "plan_items", Plan: "yesmem"}, ctx)

			if verdict.Status != test.want {
				t.Fatalf("Status = %s (%s), want %s", verdict.Status, verdict.Reason, test.want)
			}
			if verdict.Class != test.wantClass {
				t.Errorf("Class = %q (%s), want %q", verdict.Class, verdict.Reason, test.wantClass)
			}
			if !strings.Contains(verdict.Reason, test.wantThread) {
				t.Errorf("Reason = %q, want it to name %q", verdict.Reason, test.wantThread)
			}
		})
	}
}

// errorPlans answers one thread with a store error and every other from a
// literal table.
type errorPlans struct {
	failOn string
	plans  fakePlans
}

func (e errorPlans) Plan(thread string) ([]engine.PlanItem, bool, error) {
	if thread == e.failOn {
		return nil, false, errors.New("database is locked")
	}
	return e.plans.Plan(thread)
}

// Ein Lesefehler auf einer Kennung darf nicht zu einem Urteil ueber eine
// spaetere Kennung fuehren: ein Speicher, der eine Identitaet nicht beantworten
// kann, verbuergt auch nicht fuer die andere, und ein Confirm waere die
// unsichere Richtung.
func TestPlanItemsClaimDoesNotConfirmPastAReadFailure(t *testing.T) {
	plans := errorPlans{
		failOn: "opencode:ses_live",
		plans:  fakePlans{"daemon-uuid": {{Content: "do the work", Status: "completed"}}},
	}

	verdict := CheckPlanItems(engine.Claim{Kind: "plan_items", Plan: "yesmem"},
		&engine.Ctx{Plans: plans, PlanThread: "daemon-uuid", PlanThreads: []string{"opencode:ses_live", "daemon-uuid"}})

	if verdict.Status != engine.StatusUnverifiable || verdict.Class != engine.ClassEnvironment {
		t.Fatalf("Status = %s class = %q (%s), want unverifiable/environment", verdict.Status, verdict.Class, verdict.Reason)
	}
	if !strings.Contains(verdict.Reason, "could not be read") {
		t.Errorf("Reason = %q, want the read failure named instead of a missing plan", verdict.Reason)
	}
}

// Ein Aufrufer ohne Kandidatenliste nennt genau einen Thread; die Meldung spricht
// dann von diesem Thread und nicht von einem Agenten, den es hier nicht gibt.
func TestPlanItemsClaimWithoutACandidateListNamesTheThread(t *testing.T) {
	verdict := CheckPlanItems(engine.Claim{Kind: "plan_items", Plan: "yesmem"},
		&engine.Ctx{Plans: fakePlans{}, PlanThread: "t-inline"})

	if verdict.Status != engine.StatusUnverifiable || verdict.Class != engine.ClassEnvironment {
		t.Fatalf("Status = %s class = %q (%s), want unverifiable/environment", verdict.Status, verdict.Class, verdict.Reason)
	}
	if !strings.Contains(verdict.Reason, "thread t-inline has no plan") {
		t.Errorf("Reason = %q, want the named thread", verdict.Reason)
	}
	if strings.Contains(verdict.Reason, "wrote no plan") {
		t.Errorf("Reason = %q, want no agent claim when the caller named only a thread", verdict.Reason)
	}
}
