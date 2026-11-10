package daemon

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/carsteneu/yesmem/internal/storage"
)

// Eine yesloop-Agentenzeile trägt die Kennungen, unter denen der Agent selbst
// schreibt: die Backend-Session (opencode/ses_, codex) und die Daemon-UUID. Der
// Plan des Agenten liegt unter einer dieser Kennungen, nicht unter einer x-beliebigen.
func TestPlanThreadsForAgent(t *testing.T) {
	cases := []struct {
		name  string
		agent storage.Agent
		want  []string
	}{
		{
			// The backend session leads; the same address without its namespace,
			// the namespaced daemon uuid the MCP layer produces before the backend
			// id is captured, and the bare uuid (the thread the gate keys on).
			name:  "an opencode agent is asked for its backend session thread first",
			agent: storage.Agent{SessionID: "uuid-1", Backend: "opencode", OpencodeSessionID: "ses_one"},
			want:  []string{"opencode:ses_one", "ses_one", "opencode:uuid-1", "uuid-1"},
		},
		{
			name:  "a recorded proxy thread is not repeated",
			agent: storage.Agent{SessionID: "uuid-1", Backend: "opencode", OpencodeSessionID: "ses_one", ProxyThreadID: "opencode:ses_one"},
			want:  []string{"opencode:ses_one", "ses_one", "opencode:uuid-1", "uuid-1"},
		},
		{
			name:  "a codex agent is asked for its backend session thread",
			agent: storage.Agent{SessionID: "uuid-2", Backend: "codex", CodexSessionID: "cdx_2"},
			want:  []string{"codex:cdx_2", "cdx_2", "codex:uuid-2", "uuid-2"},
		},
		{
			name:  "a claude agent is its daemon session",
			agent: storage.Agent{SessionID: "uuid-3", Backend: "claude"},
			want:  []string{"uuid-3"},
		},
		{
			name:  "an opencode agent without a captured backend id keeps the namespaced uuid",
			agent: storage.Agent{SessionID: "uuid-4", Backend: "opencode"},
			want:  []string{"uuid-4", "opencode:uuid-4"},
		},
		{
			name:  "an agent without a session id names its captured address and its bare form",
			agent: storage.Agent{Backend: "opencode", OpencodeSessionID: "ses_one"},
			want:  []string{"opencode:ses_one", "ses_one"},
		},
		{
			name:  "an agent without an identity names no thread",
			agent: storage.Agent{},
			want:  nil,
		},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			got := planThreadsForAgent(test.agent)
			if strings.Join(got, ",") != strings.Join(test.want, ",") {
				t.Errorf("planThreadsForAgent() = %v, want %v", got, test.want)
			}
		})
	}
}

// planClaimSetup seeds the shape the gate sees: an opencode agent row for a
// report section, and a repository the honest claims can be derived against.
func planClaimSetup(t *testing.T) (h *Handler, store *storage.Store, repo, head, section string) {
	t.Helper()
	h, store = mustFileHandler(t)
	repo, head, _ = claimsRepo(t)
	section = "yesloop-plan-claim"
	agent := storage.Agent{
		ID:                "agent-plan-1",
		Project:           repo,
		Section:           section,
		SessionID:         "daemon-uuid-1",
		Backend:           "opencode",
		OpencodeSessionID: "ses_planclaim",
		Status:            "running",
	}
	if err := store.AgentCreate(agent); err != nil {
		t.Fatalf("AgentCreate: %v", err)
	}
	return h, store, repo, head, section
}

// seedPlan stores an outline for a thread, the way set_plan persists it.
func seedPlan(t *testing.T, store *storage.Store, thread, items string) {
	t.Helper()
	now := time.Now()
	if err := store.UpsertPlan(&storage.PlanRow{
		ThreadID:  thread,
		Content:   items,
		Items:     items,
		Status:    "active",
		Scope:     "session",
		CreatedAt: now,
		UpdatedAt: now,
	}); err != nil {
		t.Fatalf("UpsertPlan: %v", err)
	}
}

// planClaimReport is the report shape the yesloop-done profile requires, with the
// plan marker the claim parser looks for.
func planClaimReport(head, repo string) string {
	return fmt.Sprintf("[COMMIT: %s]\n[BRANCH: main]\nTests run: python3 -m unittest test_ok.py -> exit 0\n[DEPLOY: no]\n[PLAN: %s]\n", head, repo)
}

// writePlanClaimReport stores the report in the section's -verify twin, the
// section the pipeline writes before the pre-check.
func writePlanClaimReport(t *testing.T, store *storage.Store, repo, section, report string) {
	t.Helper()
	if err := store.ScratchpadWrite(repo, section+"-verify", report, ""); err != nil {
		t.Fatalf("ScratchpadWrite: %v", err)
	}
}

// Der Plan des Agenten liegt unter seiner Backend-Session, nicht unter der
// Daemon-UUID, aus der der Verifier ihn heute ableitet. Ohne die Auflösung über
// die Agentenzeile ist ein korrekter Bericht dort unverifiable.
func TestVerifyClaimsJudgesThePlanOfItsAgent(t *testing.T) {
	h, store, repo, head, section := planClaimSetup(t)
	skipWithoutBubblewrap(t)
	seedPlan(t, store, "opencode:ses_planclaim", `[{"content":"do the work","status":"completed"}]`)
	writePlanClaimReport(t, store, repo, section, planClaimReport(head, repo))

	out := verifyClaimsCall(t, h, map[string]any{"project": repo, "section": section + "-verify", "repo": repo})

	if out["status"] != "accepted" {
		t.Fatalf("status = %v (%v: %v), want accepted", out["status"], out["kind"], out["reason"])
	}
}

// Ein offener Schritt widerspricht dem DONE-Bericht, auch wenn der Plan unter der
// Backend-Session liegt.
func TestVerifyClaimsBlocksOnAnOpenPlanStep(t *testing.T) {
	h, store, repo, head, section := planClaimSetup(t)
	seedPlan(t, store, "opencode:ses_planclaim", `[{"content":"do the work","status":"completed"},{"content":"ship it","status":"pending"}]`)
	writePlanClaimReport(t, store, repo, section, planClaimReport(head, repo))

	out := verifyClaimsCall(t, h, map[string]any{"project": repo, "section": section + "-verify", "repo": repo})

	if out["status"] != "blocked" || out["kind"] != "defect" {
		t.Fatalf("status = %v kind = %v (%v), want blocked/defect", out["status"], out["kind"], out["reason"])
	}
	if reason, _ := out["reason"].(string); !strings.Contains(reason, "ship it") {
		t.Errorf("reason = %q, want the open step named", reason)
	}
}

// Ein Bericht darf sich nicht hinter dem Plan eines anderen Threads verstecken:
// der genannte Thread gehört dem Agenten nicht und wird nicht befragt.
func TestVerifyClaimsDoesNotAcceptAForeignPlanThread(t *testing.T) {
	h, store, repo, head, section := planClaimSetup(t)
	seedPlan(t, store, "opencode:ses_other", `[{"content":"foreign work","status":"completed"}]`)
	writePlanClaimReport(t, store, repo, section, planClaimReport(head, repo))

	out := verifyClaimsCall(t, h, map[string]any{
		"project":     repo,
		"section":     section + "-verify",
		"repo":        repo,
		"plan_thread": "opencode:ses_other",
	})

	if out["status"] == "accepted" {
		t.Fatalf("status = accepted, want blocked: a foreign plan thread must not confirm the claim")
	}
	if out["kind"] != "environment" {
		t.Errorf("kind = %v (%v), want environment", out["kind"], out["reason"])
	}
}

// Existieren beide Plaene, entscheidet der eigene: ein fremder, vollstaendig
// erledigter Plan darf den offenen Schritt des Agenten nicht verdecken.
func TestVerifyClaimsPrefersTheAgentsPlanOverAForeignOne(t *testing.T) {
	h, store, repo, head, section := planClaimSetup(t)
	seedPlan(t, store, "opencode:ses_other", `[{"content":"foreign work","status":"completed"}]`)
	seedPlan(t, store, "opencode:ses_planclaim", `[{"content":"my own work","status":"pending"}]`)
	writePlanClaimReport(t, store, repo, section, planClaimReport(head, repo))

	out := verifyClaimsCall(t, h, map[string]any{
		"project":     repo,
		"section":     section + "-verify",
		"repo":        repo,
		"plan_thread": "opencode:ses_other",
	})

	if out["status"] != "blocked" || out["kind"] != "defect" {
		t.Fatalf("status = %v kind = %v (%v), want blocked/defect from the agent's own plan",
			out["status"], out["kind"], out["reason"])
	}
	if reason, _ := out["reason"].(string); !strings.Contains(reason, "my own work") {
		t.Errorf("reason = %q, want the agent's own open step named", reason)
	}
}

// Es gibt wirklich keinen Plan: das bleibt environment, aber mit einem Satz, der
// vom Agenten spricht und nicht von einem Thread ohne Plan.
func TestVerifyClaimsReportsAnAgentWithoutAPlan(t *testing.T) {
	h, store, repo, head, section := planClaimSetup(t)
	writePlanClaimReport(t, store, repo, section, planClaimReport(head, repo))

	out := verifyClaimsCall(t, h, map[string]any{"project": repo, "section": section + "-verify", "repo": repo})

	if out["status"] != "blocked" || out["kind"] != "environment" {
		t.Fatalf("status = %v kind = %v (%v), want blocked/environment", out["status"], out["kind"], out["reason"])
	}
	reason, _ := out["reason"].(string)
	if !strings.Contains(reason, "wrote no plan") {
		t.Errorf("reason = %q, want the sentence that the agent wrote no plan", reason)
	}
	if !strings.Contains(reason, "opencode:ses_planclaim") {
		t.Errorf("reason = %q, want the thread that was consulted named", reason)
	}
}
