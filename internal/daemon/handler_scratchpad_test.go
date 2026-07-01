package daemon

import (
	"testing"
)

func TestHandleScratchpadWrite(t *testing.T) {
	h, _ := mustHandler(t)
	resp := h.handleScratchpadWrite(map[string]any{
		"project": "test-proj",
		"section": "status",
		"content": "implementation in progress",
	})
	if resp.Error != "" {
		t.Fatalf("write error: %s", resp.Error)
	}
	m := resultMap(t, resp)
	if m["project"] != "test-proj" {
		t.Errorf("expected project 'test-proj', got %q", m["project"])
	}
}

func TestHandleScratchpadWrite_RequiresProject(t *testing.T) {
	h, _ := mustHandler(t)
	resp := h.handleScratchpadWrite(map[string]any{"section": "x", "content": "y"})
	if resp.Error == "" {
		t.Fatal("expected error for missing project")
	}
}

func TestHandleScratchpadWrite_RequiresSection(t *testing.T) {
	h, _ := mustHandler(t)
	resp := h.handleScratchpadWrite(map[string]any{"project": "p", "content": "y"})
	if resp.Error == "" {
		t.Fatal("expected error for missing section")
	}
}

func TestHandleScratchpadReadWrite(t *testing.T) {
	h, _ := mustHandler(t)

	h.handleScratchpadWrite(map[string]any{
		"project": "proj-rw", "section": "notes", "content": "hello scratch",
	})

	resp := h.handleScratchpadRead(map[string]any{"project": "proj-rw"})
	if resp.Error != "" {
		t.Fatalf("read error: %s", resp.Error)
	}
	m := resultMap(t, resp)
	sections := m["sections"].([]any)
	if len(sections) != 1 {
		t.Fatalf("expected 1 section, got %d", len(sections))
	}
	sec := sections[0].(map[string]any)
	if sec["content"] != "hello scratch" {
		t.Errorf("expected 'hello scratch', got %q", sec["content"])
	}
}

func TestHandleScratchpadRead_RequiresProject(t *testing.T) {
	h, _ := mustHandler(t)
	resp := h.handleScratchpadRead(map[string]any{})
	if resp.Error == "" {
		t.Fatal("expected error for missing project")
	}
}

func TestHandleScratchpadRead_SpecificSection(t *testing.T) {
	h, _ := mustHandler(t)

	h.handleScratchpadWrite(map[string]any{"project": "proj-sec", "section": "a", "content": "aaa"})
	h.handleScratchpadWrite(map[string]any{"project": "proj-sec", "section": "b", "content": "bbb"})

	resp := h.handleScratchpadRead(map[string]any{"project": "proj-sec", "section": "a"})
	m := resultMap(t, resp)
	sections := m["sections"].([]any)
	if len(sections) != 1 {
		t.Fatalf("expected 1 section when filtering, got %d", len(sections))
	}
}

func TestHandleScratchpadList(t *testing.T) {
	h, _ := mustHandler(t)

	h.handleScratchpadWrite(map[string]any{"project": "proj-list", "section": "s1", "content": "x"})

	resp := h.handleScratchpadList(map[string]any{})
	if resp.Error != "" {
		t.Fatalf("list error: %s", resp.Error)
	}
	m := resultMap(t, resp)
	projects := m["projects"].([]any)
	if len(projects) == 0 {
		t.Fatal("expected at least 1 project in list")
	}
}

func TestHandleScratchpadList_FilterByProject(t *testing.T) {
	h, _ := mustHandler(t)

	h.handleScratchpadWrite(map[string]any{"project": "proj-a", "section": "x", "content": "a"})
	h.handleScratchpadWrite(map[string]any{"project": "proj-b", "section": "x", "content": "b"})

	resp := h.handleScratchpadList(map[string]any{"project": "proj-a"})
	m := resultMap(t, resp)
	projects := m["projects"].([]any)
	if len(projects) != 1 {
		t.Fatalf("expected 1 project when filtering, got %d", len(projects))
	}
}

func TestHandleScratchpadDelete(t *testing.T) {
	h, _ := mustHandler(t)

	h.handleScratchpadWrite(map[string]any{"project": "proj-del", "section": "s1", "content": "x"})
	h.handleScratchpadWrite(map[string]any{"project": "proj-del", "section": "s2", "content": "y"})

	resp := h.handleScratchpadDelete(map[string]any{"project": "proj-del"})
	if resp.Error != "" {
		t.Fatalf("delete error: %s", resp.Error)
	}
	m := resultMap(t, resp)
	if m["deleted_count"] != float64(2) {
		t.Errorf("expected 2 deleted, got %v", m["deleted_count"])
	}
}

func TestHandleScratchpadDelete_SingleSection(t *testing.T) {
	h, _ := mustHandler(t)

	h.handleScratchpadWrite(map[string]any{"project": "proj-del2", "section": "keep", "content": "k"})
	h.handleScratchpadWrite(map[string]any{"project": "proj-del2", "section": "remove", "content": "r"})

	resp := h.handleScratchpadDelete(map[string]any{"project": "proj-del2", "section": "remove"})
	m := resultMap(t, resp)
	if m["deleted_count"] != float64(1) {
		t.Errorf("expected 1 deleted, got %v", m["deleted_count"])
	}

	// Verify "keep" still exists
	resp = h.handleScratchpadRead(map[string]any{"project": "proj-del2"})
	m = resultMap(t, resp)
	sections := m["sections"].([]any)
	if len(sections) != 1 {
		t.Fatalf("expected 1 remaining section, got %d", len(sections))
	}
}

func TestHandleScratchpadDelete_RequiresProject(t *testing.T) {
	h, _ := mustHandler(t)
	resp := h.handleScratchpadDelete(map[string]any{})
	if resp.Error == "" {
		t.Fatal("expected error for missing project")
	}
}

func TestHandleScratchpadAppend(t *testing.T) {
	h, _ := mustHandler(t)

	// Write initial content (like an orchestrator briefing)
	h.handleScratchpadWrite(map[string]any{
		"project": "proj-append", "section": "task",
		"content": "BRIEFING: do the thing\nConstraints: must be fast",
	})

	// Append status update (like an agent reporting progress)
	resp := h.handleScratchpadAppend(map[string]any{
		"project": "proj-append", "section": "task",
		"content": "Phase 1: ANALYZE complete",
	})
	if resp.Error != "" {
		t.Fatalf("append error: %s", resp.Error)
	}

	// Read back — must contain BOTH the briefing and the appended status
	rresp := h.handleScratchpadRead(map[string]any{"project": "proj-append"})
	sections := resultMap(t, rresp)["sections"].([]any)
	if len(sections) != 1 {
		t.Fatalf("expected 1 section, got %d", len(sections))
	}
	content := sections[0].(map[string]any)["content"].(string)
	if !contains(content, "BRIEFING: do the thing") {
		t.Errorf("briefing preserved after append, got: %s", content)
	}
	if !contains(content, "Phase 1: ANALYZE complete") {
		t.Errorf("appended content not found, got: %s", content)
	}

	// Append a second time
	h.handleScratchpadAppend(map[string]any{
		"project": "proj-append", "section": "task",
		"content": "Phase 2: PLAN complete",
	})
	rresp2 := h.handleScratchpadRead(map[string]any{"project": "proj-append"})
	sections2 := resultMap(t, rresp2)["sections"].([]any)
	content2 := sections2[0].(map[string]any)["content"].(string)
	if !contains(content2, "Phase 2: PLAN complete") {
		t.Errorf("second append not found, got: %s", content2)
	}
	if !contains(content2, "Phase 1: ANALYZE complete") {
		t.Errorf("first append lost after second, got: %s", content2)
	}
}

func TestHandleScratchpadAppend_EmptyCreates(t *testing.T) {
	h, _ := mustHandler(t)

	// Append to non-existent section — should create it (like write)
	resp := h.handleScratchpadAppend(map[string]any{
		"project": "proj-append-new", "section": "new-section",
		"content": "first entry",
	})
	if resp.Error != "" {
		t.Fatalf("append to new section error: %s", resp.Error)
	}

	rresp := h.handleScratchpadRead(map[string]any{"project": "proj-append-new"})
	sections := resultMap(t, rresp)["sections"].([]any)
	content := sections[0].(map[string]any)["content"].(string)
	if content != "first entry" {
		t.Errorf("expected 'first entry', got %q", content)
	}
}

func TestHandleScratchpadAppend_RequiresContent(t *testing.T) {
	h, _ := mustHandler(t)
	resp := h.handleScratchpadAppend(map[string]any{"project": "p", "section": "s"})
	if resp.Error == "" {
		t.Fatal("expected error for missing content")
	}
}

func contains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
