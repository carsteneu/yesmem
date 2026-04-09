package daemon

import (
	"fmt"
)

// handleGetSkillContent returns the full text of a stored skill.
// Parameters: name (required), project (required)
func (h *Handler) handleGetSkillContent(params map[string]any) Response {
	name, _ := params["name"].(string)
	project, _ := params["project"].(string)
	if name == "" {
		return errorResponse("name is required")
	}

	content, err := h.store.GetSkillContent(name, project)
	if err != nil {
		return errorResponse(fmt.Sprintf("get_skill_content: %v", err))
	}

	return jsonResponse(map[string]any{
		"name":    name,
		"content": content,
	})
}
