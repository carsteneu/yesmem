package daemon

import (
	"os"
	"path/filepath"
	"time"
)

// handleScratchpadWrite upserts a scratchpad section for a project.
func (h *Handler) handleScratchpadWrite(params map[string]any) Response {
	project, _ := params["project"].(string)
	section, _ := params["section"].(string)
	content, _ := params["content"].(string)

	if project == "" {
		return errorResponse("project required")
	}
	if section == "" {
		return errorResponse("section required")
	}

	owner := h.resolveSessionID(params, "owner")

	if err := h.store.ScratchpadWrite(project, section, content, owner); err != nil {
		return errorResponse(err.Error())
	}

	// Update heartbeat for running agents that write to scratchpad
	if owner != "" {
		h.store.AgentUpdateBySessionID(owner, map[string]any{
			"heartbeat_at": time.Now().Format(time.RFC3339),
		})
	}

	// Ensure assets directory exists for this project
	assetsDir := filepath.Join(h.dataDir, "projects", project, "assets")
	if err := os.MkdirAll(assetsDir, 0755); err != nil {
		// Non-fatal — log but don't fail the write
		_ = err
	}

	return jsonResponse(map[string]any{
		"status":  "ok",
		"project": project,
		"section": section,
	})
}

// handleScratchpadRead returns scratchpad sections for a project.
// If section param is provided, only that section is returned.
func (h *Handler) handleScratchpadRead(params map[string]any) Response {
	project, _ := params["project"].(string)
	if project == "" {
		return errorResponse("project required")
	}
	section, _ := params["section"].(string)

	sections, err := h.store.ScratchpadRead(project, section)
	if err != nil {
		return errorResponse(err.Error())
	}

	type sectionResult struct {
		Section   string `json:"section"`
		Content   string `json:"content"`
		Owner     string `json:"owner"`
		UpdatedAt string `json:"updated_at"`
	}

	result := make([]sectionResult, 0, len(sections))
	for _, s := range sections {
		result = append(result, sectionResult{
			Section:   s.Section,
			Content:   s.Content,
			Owner:     s.Owner,
			UpdatedAt: s.UpdatedAt,
		})
	}

	return jsonResponse(map[string]any{
		"project":  project,
		"sections": result,
	})
}

// handleScratchpadList returns a summary of projects with scratchpad entries.
// If project param is provided, only that project is returned.
func (h *Handler) handleScratchpadList(params map[string]any) Response {
	project, _ := params["project"].(string)

	projects, err := h.store.ScratchpadList(project)
	if err != nil {
		return errorResponse(err.Error())
	}

	type sectionSummary struct {
		Section   string `json:"section"`
		Owner     string `json:"owner"`
		Size      int    `json:"size"`
		UpdatedAt string `json:"updated_at"`
	}
	type projectResult struct {
		Project      string           `json:"project"`
		SectionCount int              `json:"section_count"`
		LastUpdated  string           `json:"last_updated"`
		Sections     []sectionSummary `json:"sections"`
	}

	result := make([]projectResult, 0, len(projects))
	for _, p := range projects {
		secs := make([]sectionSummary, 0, len(p.Sections))
		for _, s := range p.Sections {
			secs = append(secs, sectionSummary{
				Section:   s.Section,
				Owner:     s.Owner,
				Size:      s.Size,
				UpdatedAt: s.UpdatedAt,
			})
		}
		result = append(result, projectResult{
			Project:      p.Project,
			SectionCount: p.SectionCount,
			LastUpdated:  p.LastUpdated,
			Sections:     secs,
		})
	}

	return jsonResponse(map[string]any{
		"projects": result,
	})
}

// handleScratchpadDelete removes scratchpad entries for a project (or a single section).
// If section is omitted the entire project's scratchpad is deleted, including its assets directory.
func (h *Handler) handleScratchpadDelete(params map[string]any) Response {
	project, _ := params["project"].(string)
	if project == "" {
		return errorResponse("project required")
	}
	section, _ := params["section"].(string)

	count, err := h.store.ScratchpadDelete(project, section)
	if err != nil {
		return errorResponse(err.Error())
	}

	// Remove assets directory when deleting the whole project scratchpad
	if section == "" {
		assetsDir := filepath.Join(h.dataDir, "projects", project, "assets")
		_ = os.RemoveAll(assetsDir)
	}

	return jsonResponse(map[string]any{
		"status":        "deleted",
		"deleted_count": count,
	})
}
