package daemon

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// handleCapProposalDecide accepts or rejects a cap-correction proposal that
// the auto-correct loop staged via category='cap_proposed'. On accept it
// extracts the proposed bash body, applies it to the active cap through the
// existing save_cap pipeline (source='auto_correct_proposal_accepted' so the
// 24h budget query counts one firing once), and flips the proposal to
// 'cap_proposed_accepted'. On reject it just transitions the proposal to
// 'cap_proposed_rejected', leaving the active cap untouched. The reviewer's
// note is appended to the proposal's content because models.Learning has no
// separate notes column.
func (h *Handler) handleCapProposalDecide(req Request) Response {
	params := req.Params
	idAny, ok := params["id"]
	if !ok {
		return errorResponse("cap_proposal_decide: 'id' is required")
	}
	id := toInt64(idAny)
	if id <= 0 {
		return errorResponse(fmt.Sprintf("cap_proposal_decide: invalid id %v", idAny))
	}
	decision := strings.ToLower(strings.TrimSpace(stringOr(params, "decision", "")))
	if decision != "accept" && decision != "reject" {
		return errorResponse("cap_proposal_decide: 'decision' must be 'accept' or 'reject'")
	}
	notes := strings.TrimSpace(stringOr(params, "notes", ""))

	proposal, err := h.store.GetLearning(id)
	if err != nil {
		return errorResponse(fmt.Sprintf("cap_proposal_decide: lookup id=%d: %v", id, err))
	}
	if proposal == nil {
		return errorResponse(fmt.Sprintf("cap_proposal_decide: id=%d not found", id))
	}
	if err := h.store.LoadJunctionData(proposal); err != nil {
		return errorResponse(fmt.Sprintf("cap_proposal_decide: load proposal junction data: %v", err))
	}
	if proposal.Category != "cap_proposed" {
		return errorResponse(fmt.Sprintf("cap_proposal_decide: id=%d already in state %q", id, proposal.Category))
	}

	capName := strings.TrimPrefix(proposal.TriggerRule, "cap_proposed:")
	if capName == "" || capName == proposal.TriggerRule {
		return errorResponse(fmt.Sprintf("cap_proposal_decide: id=%d has no cap_proposed:NAME trigger_rule", id))
	}

	if decision == "reject" {
		newContent := proposal.Content
		if notes != "" {
			newContent = fmt.Sprintf("%s [rejected: %s]", proposal.Content, notes)
		} else {
			newContent = fmt.Sprintf("%s [rejected]", proposal.Content)
		}
		if err := h.store.UpdateLearningCategoryAndContent(id, "cap_proposed_rejected", newContent); err != nil {
			return errorResponse(fmt.Sprintf("cap_proposal_decide: mark rejected: %v", err))
		}
		return jsonResponse(map[string]any{
			"id":       id,
			"cap_name": capName,
			"decision": "reject",
			"category": "cap_proposed_rejected",
		})
	}

	meta, err := ParseCapMeta(proposal.Context)
	if err != nil {
		return errorResponse(fmt.Sprintf("cap_proposal_decide: parse proposal CapMeta: %v", err))
	}
	scriptName := ""
	for _, k := range proposal.Keywords {
		if strings.HasPrefix(k, "script:") {
			scriptName = strings.TrimPrefix(k, "script:")
			break
		}
	}
	var newBody string
	for _, s := range meta.Scripts {
		if s.Runtime != "bash" {
			continue
		}
		if scriptName != "" && s.Name != scriptName {
			continue
		}
		newBody = s.Body
		break
	}
	if newBody == "" {
		return errorResponse(fmt.Sprintf("cap_proposal_decide: proposal id=%d has no bash script %q", id, scriptName))
	}

	saveResp, err := h.acceptCapProposal(capName, proposal.Project, scriptName, newBody)
	if err != nil {
		return errorResponse(err.Error())
	}
	if saveResp.Error != "" {
		return errorResponse(fmt.Sprintf("cap_proposal_decide: save_cap %q failed: %s", capName, saveResp.Error))
	}

	newContent := proposal.Content
	if notes != "" {
		newContent = fmt.Sprintf("%s [accepted: %s]", proposal.Content, notes)
	} else {
		newContent = fmt.Sprintf("%s [accepted]", proposal.Content)
	}
	if err := h.store.UpdateLearningCategoryAndContent(id, "cap_proposed_accepted", newContent); err != nil {
		return errorResponse(fmt.Sprintf("cap_proposal_decide: mark accepted: %v", err))
	}
	return jsonResponse(map[string]any{
		"id":       id,
		"cap_name": capName,
		"decision": "accept",
		"category": "cap_proposed_accepted",
		"saved":    saveResp.Result,
	})
}

// handleListCapProposals returns proposals filtered by status and project.
// Default status is 'cap_proposed' (pending). Use 'cap_proposed_accepted',
// 'cap_proposed_rejected', or 'all' to see history.
func (h *Handler) handleListCapProposals(req Request) Response {
	params := req.Params
	status := strings.TrimSpace(stringOr(params, "status", "cap_proposed"))
	project := strings.TrimSpace(stringOr(params, "project", ""))
	limit := int(toInt64(params["limit"]))
	if limit <= 0 {
		limit = 100
	}

	categories := []string{status}
	if status == "all" {
		categories = []string{"cap_proposed", "cap_proposed_accepted", "cap_proposed_rejected"}
	}

	var out []map[string]any
	for _, cat := range categories {
		rows, err := h.store.ListLearningsByCategory(cat, project, limit)
		if err != nil {
			return errorResponse(fmt.Sprintf("list_cap_proposals: %v", err))
		}
		for _, l := range rows {
			capName := strings.TrimPrefix(l.TriggerRule, "cap_proposed:")
			out = append(out, map[string]any{
				"id":          l.ID,
				"cap_name":    capName,
				"category":    l.Category,
				"project":     l.Project,
				"created_at":  l.CreatedAt,
				"content":     l.Content,
				"trigger":     l.TriggerRule,
			})
		}
	}
	payload, _ := json.Marshal(out)
	return jsonResponse(map[string]any{
		"count":     len(out),
		"proposals": json.RawMessage(payload),
	})
}

func toInt64(v any) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case int:
		return int64(n)
	case float64:
		return int64(n)
	case json.Number:
		i, _ := n.Int64()
		return i
	}
	return 0
}

// acceptCapProposal loads the active cap with the given name, splices the
// proposed bash body into its existing scripts (preserving project, tags,
// auto_active, and any extra non-bash scripts in a bundle), and re-saves the
// cap through the standard save_cap pipeline. project must match the
// proposal's project so the 3-way project filter in GetActiveLearnings
// (project = ? OR project IS NULL OR project = '') resolves the correct row.
func (h *Handler) acceptCapProposal(capName, project, scriptName, newBody string) (Response, error) {
	activeCaps, err := h.store.GetActiveLearnings("cap", project, "", "", 0)
	if err != nil {
		return Response{}, fmt.Errorf("cap_proposal_decide: load active caps: %w", err)
	}
	activeIdx := -1
	wantTrigger := "cap:" + capName
	for i := range activeCaps {
		if activeCaps[i].TriggerRule == wantTrigger {
			activeIdx = i
			break
		}
	}
	if activeIdx < 0 {
		return Response{}, fmt.Errorf("cap_proposal_decide: no active cap %q to update", capName)
	}
	active := &activeCaps[activeIdx]
	activeMeta, err := ParseCapMeta(active.Context)
	if err != nil {
		return Response{}, fmt.Errorf("cap_proposal_decide: parse active CapMeta: %w", err)
	}
	swapped := false
	for i := range activeMeta.Scripts {
		if activeMeta.Scripts[i].Runtime != "bash" {
			continue
		}
		if scriptName != "" && activeMeta.Scripts[i].Name != scriptName {
			continue
		}
		activeMeta.Scripts[i].Body = newBody
		swapped = true
		break
	}
	if !swapped {
		return Response{}, fmt.Errorf("cap_proposal_decide: active cap %q has no bash script %q to replace", capName, scriptName)
	}
	scriptsJSON, err := json.Marshal(activeMeta.Scripts)
	if err != nil {
		return Response{}, fmt.Errorf("cap_proposal_decide: marshal scripts: %w", err)
	}

	saveParams := map[string]any{
		"name":        capName,
		"description": activeMeta.Description,
		"scripts":     string(scriptsJSON),
		"tested":      true,
		"test_date":   time.Now().Format(time.RFC3339),
		"source":      "auto_correct_proposal_accepted",
		"auto_active": activeMeta.AutoActive,
	}
	if active.Project != "" {
		saveParams["project"] = active.Project
	}
	if len(activeMeta.Tags) > 0 {
		tagsAny := make([]any, len(activeMeta.Tags))
		for i, t := range activeMeta.Tags {
			tagsAny[i] = t
		}
		saveParams["tags"] = tagsAny
	}

	return h.Handle(Request{Method: "save_cap", Params: saveParams}), nil
}
