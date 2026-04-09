package daemon

import (
	"encoding/json"
	"testing"

	"github.com/carsteneu/yesmem/internal/storage"
)

func TestHandleGetSkillContent(t *testing.T) {
	h, s := mustHandler(t)

	content := "# Full Skill\n\nComplete skill text here."
	s.UpsertDocSource(&storage.DocSource{
		Name: "my-skill", Project: "memory", IsSkill: true,
		FullContent: content,
	})

	resp := h.Handle(Request{
		Method: "get_skill_content",
		Params: map[string]any{
			"name":    "my-skill",
			"project": "memory",
		},
	})
	if resp.Error != "" {
		t.Fatalf("get_skill_content error: %s", resp.Error)
	}

	var result struct {
		Name    string `json:"name"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if result.Content != content {
		t.Errorf("content = %q, want %q", result.Content, content)
	}
	if result.Name != "my-skill" {
		t.Errorf("name = %q, want %q", result.Name, "my-skill")
	}
}
