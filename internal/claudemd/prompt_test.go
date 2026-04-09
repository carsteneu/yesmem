package claudemd

import (
	"strings"
	"testing"

	"github.com/carsteneu/yesmem/internal/models"
)

func TestBuildPromptContainsProject(t *testing.T) {
	learnings := []models.Learning{
		{Category: "gotcha", Content: "some gotcha"},
	}
	prompt, err := buildPrompt("myproject", learnings)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(prompt, "myproject") {
		t.Error("prompt missing project name")
	}
	if !strings.Contains(prompt, "some gotcha") {
		t.Error("prompt missing learning content")
	}
}

func TestBuildPromptEmptyLearnings(t *testing.T) {
	_, err := buildPrompt("empty", []models.Learning{})
	if err != nil {
		t.Fatal("should not error on empty learnings")
	}
}

func TestBuildPromptUserSourceMarker(t *testing.T) {
	learnings := []models.Learning{
		{Category: "gotcha", Content: "user gotcha", Source: "user_stated"},
		{Category: "gotcha", Content: "llm gotcha", Source: "llm_extracted"},
	}
	prompt, err := buildPrompt("proj", learnings)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(prompt, "[User]") {
		t.Error("prompt missing [User] marker for user_stated learning")
	}
	if !strings.Contains(prompt, "### gotcha") {
		t.Error("prompt missing category header")
	}
	if !strings.Contains(prompt, "Max 60") {
		t.Error("prompt missing token budget constraint")
	}
}
