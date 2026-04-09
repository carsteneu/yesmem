package daemon

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/carsteneu/yesmem/internal/storage"
)

// Plan represents an active plan for a thread/session.
type Plan struct {
	Content   string    `json:"content"`
	Status    string    `json:"status"` // "active" or "completed"
	Scope     string    `json:"scope"`  // "session" or "persistent"
	Project   string    `json:"project"`
	ThreadID  string    `json:"thread_id"`
	DocsHint  string    `json:"docs_hint"` // formatted reminder for plan checkpoints
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// planStore holds plans in memory (cache), backed by SQLite for persistence.
var planStore = struct {
	sync.RWMutex
	plans map[string]*Plan // threadID → plan
}{plans: make(map[string]*Plan)}

// LoadPlansFromDB restores active plans from DB into memory cache.
// Called once at daemon startup.
func LoadPlansFromDB(store *storage.Store) {
	rows, err := store.GetActivePlans()
	if err != nil {
		return
	}
	planStore.Lock()
	for _, r := range rows {
		planStore.plans[r.ThreadID] = &Plan{
			Content:   r.Content,
			Status:    r.Status,
			Scope:     r.Scope,
			Project:   r.Project,
			ThreadID:  r.ThreadID,
			CreatedAt: r.CreatedAt,
			UpdatedAt: r.UpdatedAt,
		}
	}
	planStore.Unlock()
}

// persistPlan writes the in-memory plan to DB.
func (h *Handler) persistPlan(p *Plan) {
	if h.store == nil {
		return
	}
	h.store.UpsertPlan(&storage.PlanRow{
		ThreadID:  p.ThreadID,
		Content:   p.Content,
		Status:    p.Status,
		Scope:     p.Scope,
		Project:   p.Project,
		CreatedAt: p.CreatedAt,
		UpdatedAt: p.UpdatedAt,
	})
}

func (h *Handler) handleSetPlan(params map[string]any) Response {
	plan, _ := params["plan"].(string)
	if plan == "" {
		return errorResponse("plan is required")
	}
	scope, _ := params["scope"].(string)
	if scope == "" {
		scope = "session"
	}
	threadID, _ := params["thread_id"].(string)
	project, _ := params["project"].(string)

	now := time.Now()
	p := &Plan{
		Content:   plan,
		Status:    "active",
		Scope:     scope,
		Project:   project,
		ThreadID:  threadID,
		CreatedAt: now,
		UpdatedAt: now,
	}

	// Build docs hint from indexed reference sources
	if h.store != nil {
		p.DocsHint = buildDocsHint(h.store)
	}

	planStore.Lock()
	planStore.plans[threadID] = p
	planStore.Unlock()

	h.persistPlan(p)

	return jsonResponse(map[string]any{
		"message": fmt.Sprintf("Plan set (%s scope, %d chars)", scope, len(plan)),
		"status":  "active",
		"plan":    plan,
	})
}

func (h *Handler) handleUpdatePlan(params map[string]any) Response {
	threadID, _ := params["thread_id"].(string)

	planStore.Lock()
	p, ok := planStore.plans[threadID]
	if !ok || p == nil {
		planStore.Unlock()
		return errorResponse("no active plan — use set_plan() first")
	}
	if p.Status == "completed" {
		planStore.Unlock()
		return errorResponse("plan already completed — use set_plan() for a new plan")
	}

	// Apply updates
	if completed, ok := params["completed"].([]any); ok {
		for _, item := range completed {
			if s, ok := item.(string); ok {
				p.Content = markCompleted(p.Content, s)
			}
		}
	}
	if add, ok := params["add"].([]any); ok {
		for _, item := range add {
			if s, ok := item.(string); ok {
				p.Content += "\n" + s
			}
		}
	}
	if remove, ok := params["remove"].([]any); ok {
		for _, item := range remove {
			if s, ok := item.(string); ok {
				p.Content = removeLine(p.Content, s)
			}
		}
	}
	// Allow direct content replacement
	if newContent, ok := params["plan"].(string); ok && newContent != "" {
		p.Content = newContent
	}

	p.UpdatedAt = time.Now()
	content := p.Content
	planStore.Unlock()

	h.persistPlan(p)

	return jsonResponse(map[string]any{
		"message": "Plan updated",
		"status":  "active",
		"plan":    content,
	})
}

func (h *Handler) handleGetPlan(params map[string]any) Response {
	threadID, _ := params["thread_id"].(string)

	planStore.RLock()
	p, ok := planStore.plans[threadID]
	planStore.RUnlock()

	if !ok || p == nil {
		return jsonResponse(map[string]any{"exists": false, "plan": "", "status": ""})
	}
	return jsonResponse(map[string]any{
		"exists":    true,
		"plan":      p.Content,
		"status":    p.Status,
		"scope":     p.Scope,
		"project":   p.Project,
		"docs_hint": p.DocsHint,
	})
}

func (h *Handler) handleCompletePlan(params map[string]any) Response {
	threadID, _ := params["thread_id"].(string)

	planStore.Lock()
	p, ok := planStore.plans[threadID]
	if !ok || p == nil {
		planStore.Unlock()
		return errorResponse("no active plan to complete")
	}
	p.Status = "completed"
	p.UpdatedAt = time.Now()
	content := p.Content
	planStore.Unlock()

	h.persistPlan(p)

	return jsonResponse(map[string]any{
		"message": "Plan completed — checkpoints stopped",
		"plan":    content,
		"status":  "completed",
	})
}

// GetActivePlan returns the active plan for a thread (used by proxy for injection).
func GetActivePlan(threadID string) (string, bool) {
	planStore.RLock()
	defer planStore.RUnlock()
	p, ok := planStore.plans[threadID]
	if !ok || p == nil || p.Status != "active" {
		return "", false
	}
	return p.Content, true
}

// HasActivePlan returns true if there is an active plan for a thread.
func HasActivePlan(threadID string) bool {
	planStore.RLock()
	defer planStore.RUnlock()
	p, ok := planStore.plans[threadID]
	return ok && p != nil && p.Status == "active"
}

// buildDocsHint queries reference doc sources and builds a formatted reminder
// for plan checkpoint injection. Returns "" if no reference sources exist.
func buildDocsHint(store *storage.Store) string {
	sources, err := store.GetReferenceSources()
	if err != nil || len(sources) == 0 {
		return ""
	}

	var sourceList []string
	var examples []string
	for _, s := range sources {
		label := s.Name
		if s.Version != "" {
			label += " " + s.Version
		}
		sourceList = append(sourceList, label)
		if s.ExampleQuery != "" {
			examples = append(examples, fmt.Sprintf("- %s → docs_search(\"%s\")", label, s.ExampleQuery))
		}
	}

	var b strings.Builder
	b.WriteString("[Docs available] Reference docs indexed:\n")
	b.WriteString("  " + strings.Join(sourceList, ", ") + "\n\n")
	b.WriteString("BEFORE writing code that uses framework APIs, external libraries,\n")
	b.WriteString("or language stdlib beyond basic control flow:\n")
	b.WriteString("1. Identify which functions/methods/tags you will use\n")
	b.WriteString("2. Call docs_search(query=\"<function or concept>\", exact=true)\n")
	b.WriteString("3. Read the result BEFORE writing the Edit/Write\n")
	if len(examples) > 0 {
		b.WriteString("\nExamples:\n")
		for _, ex := range examples {
			b.WriteString(ex + "\n")
		}
	}
	b.WriteString("\nSkip for: variable assignments, if/else, loops, string operations,\n")
	b.WriteString("error handling patterns you already know.\n")
	b.WriteString("[/Docs available]")
	return b.String()
}

// docsHintCache caches the formatted docs hint with a TTL.
var docsHintCache = struct {
	sync.RWMutex
	hint    string
	builtAt time.Time
}{} // 5min TTL

// handleGetDocsHint returns the cached docs hint, rebuilding if stale (>5min).
// Used by proxy for subagent injection — project-independent since reference docs are global.
func (h *Handler) handleGetDocsHint(params map[string]any) Response {
	docsHintCache.RLock()
	hint := docsHintCache.hint
	builtAt := docsHintCache.builtAt
	docsHintCache.RUnlock()

	if time.Since(builtAt) < 5*time.Minute && hint != "" {
		return jsonResponse(map[string]any{"docs_hint": hint})
	}

	// Rebuild
	if h.store == nil {
		return jsonResponse(map[string]any{"docs_hint": ""})
	}
	hint = buildDocsHint(h.store)

	docsHintCache.Lock()
	docsHintCache.hint = hint
	docsHintCache.builtAt = time.Now()
	docsHintCache.Unlock()

	return jsonResponse(map[string]any{"docs_hint": hint})
}

// markCompleted marks a line as completed (prepends ✅ or replaces ⬜/🔄 with ✅).
func markCompleted(content, item string) string {
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		if strings.Contains(line, item) {
			line = strings.Replace(line, "⬜", "✅", 1)
			line = strings.Replace(line, "🔄", "✅", 1)
			if !strings.Contains(line, "✅") {
				line = "✅ " + line
			}
			lines[i] = line
		}
	}
	return strings.Join(lines, "\n")
}

// removeLine removes lines containing the given text.
func removeLine(content, item string) string {
	lines := strings.Split(content, "\n")
	var result []string
	for _, line := range lines {
		if !strings.Contains(line, item) {
			result = append(result, line)
		}
	}
	return strings.Join(result, "\n")
}
