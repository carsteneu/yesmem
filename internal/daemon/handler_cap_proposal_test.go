package daemon

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/carsteneu/yesmem/internal/models"
)

func seedCapProposed(t *testing.T, h *Handler, capName, proposedBash string) int64 {
	t.Helper()
	caps, err := h.store.GetActiveLearnings("cap", "", "", "", 0)
	if err != nil {
		t.Fatalf("lookup active cap %q: %v", capName, err)
	}
	var active *models.Learning
	for i := range caps {
		m, err := ParseCapMeta(caps[i].Context)
		if err != nil {
			continue
		}
		if m.Name == capName {
			active = &caps[i]
			break
		}
	}
	if active == nil {
		t.Fatalf("no active cap %q to clone", capName)
	}
	meta, err := ParseCapMeta(active.Context)
	if err != nil {
		t.Fatalf("parse active CapMeta: %v", err)
	}
	for i := range meta.Scripts {
		if meta.Scripts[i].Runtime == "bash" {
			meta.Scripts[i].Body = proposedBash
		}
	}
	ctxJSON, err := meta.ToJSON()
	if err != nil {
		t.Fatalf("render proposal CapMeta: %v", err)
	}
	l := &models.Learning{
		Content:     active.Content + " [auto-correct proposal]",
		Category:    "cap_proposed",
		Source:      "auto_correct_proposal",
		Project:     active.Project,
		Confidence:  1.0,
		Context:     ctxJSON,
		Domain:      active.Domain,
		TriggerRule: "cap_proposed:" + capName,
		Keywords:    append(append([]string{}, active.Keywords...), "pending_approval"),
		CreatedAt:   time.Now(),
	}
	id, err := h.store.InsertLearning(l)
	if err != nil {
		t.Fatalf("insert proposal: %v", err)
	}
	return id
}

func TestCapProposalDecide_AcceptUpdatesActiveCap(t *testing.T) {
	h, _ := mustHandler(t)
	seedCap(t, h, "demo", "yesmem", "echo old\nexit 0\n", []string{"demo"})
	propID := seedCapProposed(t, h, "demo", "echo new\nexit 0\n")

	resp := h.Handle(Request{
		Method: "cap_proposal_decide",
		Params: map[string]any{
			"id":       float64(propID),
			"decision": "accept",
			"notes":    "looks ok",
		},
	})
	if resp.Error != "" {
		t.Fatalf("accept failed: %s", resp.Error)
	}

	caps, _ := h.store.GetActiveLearnings("cap", "", "", "", 0)
	var body string
	for _, c := range caps {
		m, err := ParseCapMeta(c.Context)
		if err != nil || m.Name != "demo" {
			continue
		}
		for _, s := range m.Scripts {
			if s.Runtime == "bash" {
				body = s.Body
				break
			}
		}
		break
	}
	if !strings.Contains(body, "echo new") {
		t.Errorf("active cap not updated: body=%q", body)
	}

	prop, err := h.store.GetLearning(propID)
	if err != nil {
		t.Fatalf("reload proposal: %v", err)
	}
	if prop.Category != "cap_proposed_accepted" {
		t.Errorf("proposal category=%q, want cap_proposed_accepted", prop.Category)
	}
	if !strings.Contains(prop.Content, "[accepted: looks ok]") {
		t.Errorf("note not appended to proposal content: %q", prop.Content)
	}
}

func TestCapProposalDecide_RejectKeepsActiveCap(t *testing.T) {
	h, _ := mustHandler(t)
	seedCap(t, h, "demo", "yesmem", "echo old\nexit 0\n", []string{"demo"})
	propID := seedCapProposed(t, h, "demo", "echo rewrite\nexit 0\n")

	resp := h.Handle(Request{
		Method: "cap_proposal_decide",
		Params: map[string]any{
			"id":       float64(propID),
			"decision": "reject",
			"notes":    "too risky",
		},
	})
	if resp.Error != "" {
		t.Fatalf("reject failed: %s", resp.Error)
	}

	caps, _ := h.store.GetActiveLearnings("cap", "", "", "", 0)
	var body string
	for _, c := range caps {
		m, err := ParseCapMeta(c.Context)
		if err != nil || m.Name != "demo" {
			continue
		}
		for _, s := range m.Scripts {
			if s.Runtime == "bash" {
				body = s.Body
				break
			}
		}
		break
	}
	if !strings.Contains(body, "echo old") {
		t.Errorf("active cap wrongly modified: body=%q", body)
	}

	prop, _ := h.store.GetLearning(propID)
	if prop.Category != "cap_proposed_rejected" {
		t.Errorf("proposal category=%q, want cap_proposed_rejected", prop.Category)
	}
	if !strings.Contains(prop.Content, "[rejected: too risky]") {
		t.Errorf("note not appended: %q", prop.Content)
	}
}

func TestCapProposalDecide_RefusesAlreadyDecided(t *testing.T) {
	h, _ := mustHandler(t)
	seedCap(t, h, "demo", "yesmem", "echo old\nexit 0\n", []string{"demo"})
	propID := seedCapProposed(t, h, "demo", "echo rewrite\nexit 0\n")

	first := h.Handle(Request{
		Method: "cap_proposal_decide",
		Params: map[string]any{"id": float64(propID), "decision": "reject"},
	})
	if first.Error != "" {
		t.Fatalf("first reject: %s", first.Error)
	}

	second := h.Handle(Request{
		Method: "cap_proposal_decide",
		Params: map[string]any{"id": float64(propID), "decision": "accept"},
	})
	if second.Error == "" {
		t.Error("second decision on already-decided proposal must fail, got success")
	}
}

// TestCapProposalDecide_AcceptPreservesBundleMetadata seeds a multi-script
// bundle cap (one bash tool + one bash handler) with non-default project,
// tags, and auto_active=false, then accepts a proposal that swaps only the
// tool-script body. The current accept path collapses the cap by calling
// save_cap with handler_bash alone, which drops every sibling script and
// resets metadata. After the fix the active cap must keep all bundle
// metadata (project, tags, auto_active, the setup handler script) and only
// the bash tool body must be swapped to the proposed value.
func TestCapProposalDecide_AcceptPreservesBundleMetadata(t *testing.T) {
	h, _ := mustHandler(t)

	scriptsJSON := `[` +
		`{"name":"main","kind":"tool","runtime":"bash","lang":"bash","body":"echo old\nexit 0\n","schema":"{}"},` +
		`{"name":"setup","kind":"handler","runtime":"bash","lang":"bash","body":"echo setup\n"}` +
		`]`
	saveResp := h.Handle(Request{
		Method: "save_cap",
		Params: map[string]any{
			"name":        "bundle_cap",
			"description": "multi-script bundle",
			"scripts":     scriptsJSON,
			"project":     "yesmem",
			"tags":        []any{"bundle", "demo"},
			"auto_active": false,
			"_from_disk":  true,
		},
	})
	if saveResp.Error != "" {
		t.Fatalf("seed save_cap: %s", saveResp.Error)
	}

	propID := seedCapProposed(t, h, "bundle_cap", "echo new\nexit 0\n")

	resp := h.Handle(Request{
		Method: "cap_proposal_decide",
		Params: map[string]any{
			"id":       float64(propID),
			"decision": "accept",
		},
	})
	if resp.Error != "" {
		t.Fatalf("accept failed: %s", resp.Error)
	}

	caps, err := h.store.GetActiveLearnings("cap", "yesmem", "", "", 0)
	if err != nil {
		t.Fatalf("read caps: %v", err)
	}
	var active *models.Learning
	for i := range caps {
		m, err := ParseCapMeta(caps[i].Context)
		if err == nil && m.Name == "bundle_cap" {
			active = &caps[i]
			break
		}
	}
	if active == nil {
		t.Fatalf("bundle_cap not found among active caps after accept")
	}
	if active.Project != "yesmem" {
		t.Errorf("project lost on accept: %q, want yesmem", active.Project)
	}

	meta, err := ParseCapMeta(active.Context)
	if err != nil {
		t.Fatalf("parse meta: %v", err)
	}
	if len(meta.Scripts) != 2 {
		t.Errorf("scripts collapsed on accept: have %d, want 2; scripts=%+v", len(meta.Scripts), meta.Scripts)
	}
	var setupBody, mainBody string
	for _, sc := range meta.Scripts {
		switch sc.Name {
		case "setup":
			setupBody = sc.Body
		case "main":
			mainBody = sc.Body
		}
	}
	if !strings.Contains(setupBody, "echo setup") {
		t.Errorf("setup handler script lost on accept; setupBody=%q scripts=%+v", setupBody, meta.Scripts)
	}
	if !strings.Contains(mainBody, "echo new") {
		t.Errorf("main bash body not swapped to proposed value: %q", mainBody)
	}
	if !meta.HasTag("bundle") {
		t.Errorf("tag 'bundle' lost on accept; tags=%v", meta.Tags)
	}
	if meta.AutoActive {
		t.Errorf("auto_active flipped to true on accept; want false")
	}
}

func TestListCapProposals_DefaultsToPending(t *testing.T) {
	h, _ := mustHandler(t)
	seedCap(t, h, "alpha", "yesmem", "echo a\n", []string{"alpha"})
	seedCap(t, h, "beta", "yesmem", "echo b\n", []string{"beta"})
	pendingID := seedCapProposed(t, h, "alpha", "echo a2\n")
	rejectedID := seedCapProposed(t, h, "beta", "echo b2\n")

	if err := h.store.UpdateLearningCategoryAndContent(rejectedID, "cap_proposed_rejected", "rejected for test"); err != nil {
		t.Fatalf("set up rejected proposal: %v", err)
	}

	resp := h.Handle(Request{Method: "list_cap_proposals", Params: map[string]any{}})
	if resp.Error != "" {
		t.Fatalf("list failed: %s", resp.Error)
	}
	var result struct {
		Count int `json:"count"`
	}
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("unmarshal result: %v (raw=%s)", err, string(resp.Result))
	}
	if result.Count != 1 {
		t.Errorf("count=%d, want 1 pending; rejectedID=%d, pendingID=%d", result.Count, rejectedID, pendingID)
	}
}

// TestCapProposalDecide_AcceptDoesNotDoubleCountInBudget pins the firings-per-cap
// semantic of CountAutoCorrectGenerations: one auto-correct firing must consume
// exactly one budget slot, regardless of which path it took.
//
// Bug pre-fix: the substantial-diff path persists the original proposal row
// (source=auto_correct_proposal, trigger=cap_proposed:NAME) AND on accept writes
// a NEW save_cap row with source=auto_correct_accepted, trigger=cap:NAME. Both
// match the count query, so a single accepted proposal counted as TWO firings,
// halving the effective per-cap daily budget.
//
// Fix: the new save_cap row written by acceptCapProposal must use a
// disambiguating source ("auto_correct_proposal_accepted") so it does not match
// the count query. The original proposal row keeps its source and trigger and
// remains the canonical record of the firing.
func TestCapProposalDecide_AcceptDoesNotDoubleCountInBudget(t *testing.T) {
	h, _ := mustHandler(t)
	seedCap(t, h, "demo", "yesmem", "echo old\nexit 0\n", []string{"demo"})
	propID := seedCapProposed(t, h, "demo", "echo new\nexit 0\n")

	resp := h.Handle(Request{
		Method: "cap_proposal_decide",
		Params: map[string]any{
			"id":       float64(propID),
			"decision": "accept",
		},
	})
	if resp.Error != "" {
		t.Fatalf("accept failed: %s", resp.Error)
	}

	got, err := h.store.CountAutoCorrectGenerations("demo", time.Now().Add(-1*time.Hour))
	if err != nil {
		t.Fatalf("CountAutoCorrectGenerations: %v", err)
	}
	if got != 1 {
		t.Errorf("accept consumed %d budget slots, want exactly 1 (one firing = one slot)", got)
	}
}

// TestCapProposalDecide_AcceptTargetsScriptByName seeds a multi-bash bundle cap
// (two bash handlers: poll + reply) plus a proposal that patches only the
// "reply" script and carries a "script:reply" Keyword. After accept the active
// cap must have reply patched and poll preserved. Without script-name plumbing
// the accept path patches the first bash script (poll) and overwrites the
// wrong one.
func TestCapProposalDecide_AcceptTargetsScriptByName(t *testing.T) {
	h, _ := mustHandler(t)

	scriptsJSON := `[` +
		`{"name":"poll","kind":"handler","runtime":"bash","lang":"bash","body":"echo poll-original\nexit 0\n"},` +
		`{"name":"reply","kind":"handler","runtime":"bash","lang":"bash","body":"echo reply-original\nexit 0\n"}` +
		`]`
	saveResp := h.Handle(Request{
		Method: "save_cap",
		Params: map[string]any{
			"name":        "tg_bundle",
			"description": "two bash handlers",
			"scripts":     scriptsJSON,
			"project":     "yesmem",
		},
	})
	if saveResp.Error != "" {
		t.Fatalf("seed save_cap: %s", saveResp.Error)
	}

	caps, err := h.store.GetActiveLearnings("cap", "yesmem", "", "", 0)
	if err != nil {
		t.Fatalf("get active: %v", err)
	}
	var active *models.Learning
	for i := range caps {
		if caps[i].TriggerRule == "cap:tg_bundle" {
			active = &caps[i]
			break
		}
	}
	if active == nil {
		t.Fatalf("seeded bundle cap not found")
	}
	propMeta, err := ParseCapMeta(active.Context)
	if err != nil {
		t.Fatalf("parse active CapMeta: %v", err)
	}
	for i := range propMeta.Scripts {
		if propMeta.Scripts[i].Name == "reply" && propMeta.Scripts[i].Runtime == "bash" {
			propMeta.Scripts[i].Body = "echo reply-patched\nexit 0\n"
		}
	}
	propCtx, err := propMeta.ToJSON()
	if err != nil {
		t.Fatalf("render proposal CapMeta: %v", err)
	}
	prop := &models.Learning{
		Content:     "tg_bundle proposal",
		Category:    "cap_proposed",
		Source:      "auto_correct_proposal",
		Project:     "yesmem",
		Confidence:  1.0,
		Context:     propCtx,
		TriggerRule: "cap_proposed:tg_bundle",
		Keywords:    []string{"pending_approval", "script:reply"},
		CreatedAt:   time.Now(),
	}
	propID, err := h.store.InsertLearning(prop)
	if err != nil {
		t.Fatalf("InsertLearning: %v", err)
	}

	resp := h.Handle(Request{
		Method: "cap_proposal_decide",
		Params: map[string]any{
			"id":       float64(propID),
			"decision": "accept",
		},
	})
	if resp.Error != "" {
		t.Fatalf("accept: %s", resp.Error)
	}

	caps, err = h.store.GetActiveLearnings("cap", "yesmem", "", "", 0)
	if err != nil {
		t.Fatalf("re-read active caps: %v", err)
	}
	var got *models.Learning
	for i := range caps {
		if caps[i].TriggerRule == "cap:tg_bundle" {
			got = &caps[i]
			break
		}
	}
	if got == nil {
		t.Fatalf("active bundle cap missing after accept")
	}
	gotMeta, err := ParseCapMeta(got.Context)
	if err != nil {
		t.Fatalf("parse post-accept CapMeta: %v", err)
	}
	var pollBody, replyBody string
	for _, sc := range gotMeta.Scripts {
		switch sc.Name {
		case "poll":
			pollBody = sc.Body
		case "reply":
			replyBody = sc.Body
		}
	}
	if !strings.Contains(replyBody, "echo reply-patched") {
		t.Errorf("reply not patched after accept, got %q", replyBody)
	}
	if !strings.Contains(pollBody, "echo poll-original") {
		t.Errorf("poll wrongly mutated after accept, got %q", pollBody)
	}
}
