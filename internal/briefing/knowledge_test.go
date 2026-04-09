package briefing

import (
	"strings"
	"testing"
	"time"

	"github.com/carsteneu/yesmem/internal/models"
)

func TestRenderKnowledge_IncludesPivotMoments(t *testing.T) {
	g := &Generator{maxPerCategory: 3, strings: DefaultStrings()}
	learnings := []models.Learning{
		{Category: "pivot_moment", Content: "User sagte 'das ist Murks' — Redesign gestartet", CreatedAt: time.Now()},
	}
	result := g.renderKnowledge(g.strings, learnings)
	if !strings.Contains(result, "Murks") {
		t.Error("renderKnowledge should include pivot_moments content")
	}
	if !strings.Contains(result, "Moments that shaped me") {
		t.Errorf("renderKnowledge should contain pivot moments header, got: %s", result)
	}
}

func TestRenderKnowledge_PivotMomentsOverflow(t *testing.T) {
	g := &Generator{maxPerCategory: 5, strings: DefaultStrings()}
	learnings := []models.Learning{
		{Category: "pivot_moment", Content: "Pivot 1", CreatedAt: time.Now()},
		{Category: "pivot_moment", Content: "Pivot 2", CreatedAt: time.Now()},
		{Category: "pivot_moment", Content: "Pivot 3", CreatedAt: time.Now()},
		{Category: "pivot_moment", Content: "Pivot 4", CreatedAt: time.Now()},
		{Category: "pivot_moment", Content: "Pivot 5", CreatedAt: time.Now()},
		{Category: "pivot_moment", Content: "Pivot 6", CreatedAt: time.Now()},
	}
	result := g.renderKnowledge(g.strings, learnings)
	if !strings.Contains(result, "Pivot 1") {
		t.Error("should contain first pivot")
	}
	if !strings.Contains(result, "Pivot 5") {
		t.Error("should contain fifth pivot")
	}
	if strings.Contains(result, "Pivot 6") {
		t.Error("should NOT contain sixth pivot (overflow)")
	}
	if !strings.Contains(result, "1") && !strings.Contains(result, "pivot_moment") {
		t.Error("should show overflow count with get_learnings hint")
	}
}
