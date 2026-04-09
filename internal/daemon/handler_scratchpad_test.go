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
