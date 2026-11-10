package daemon

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/carsteneu/yesmem/internal/models"
	"github.com/carsteneu/yesmem/internal/repo"
	"github.com/carsteneu/yesmem/internal/storage"
)

// PlanItem is one step of a structured plan. Status reuses the four values
// todowrite uses, so a display renders both without a translation table; an
// unknown value is stored verbatim and rendered as pending. Depth carries
// nesting (0 top level, 1 sub-step); numbering stays a display concern.
type PlanItem struct {
	Content string `json:"content"`
	Status  string `json:"status,omitempty"`
	Reason  string `json:"reason,omitempty"`
	Depth   int    `json:"depth,omitempty"`
}

// Plan represents an active plan for a thread/session.
type Plan struct {
	Content string `json:"content"`
	// Items is the structured outline. When it is set the items are the source
	// of truth and Content is their derived snapshot (or the caller's full text
	// from the same call, which is never overwritten).
	Items     []PlanItem `json:"items,omitempty"`
	Status    string     `json:"status"` // "active" or "completed"
	Scope     string     `json:"scope"`  // "session" or "permanent"
	Project   string     `json:"project"`
	ThreadID  string     `json:"thread_id"`
	DocsHint  string     `json:"docs_hint"` // formatted reminder for plan checkpoints
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
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
		// Without the log a failed load is indistinguishable from "no plans":
		// the cache stays empty for the rest of the process and every thread
		// silently loses its context-loss anchor.
		log.Printf("[plan] active plans not loaded: %v", err)
		return
	}

	// Build before taking the lock: the docs hint needs a store query per plan.
	// Memoized per project — every plan of the same project yields the same hint,
	// and a restart can restore hundreds of them.
	hints := make(map[string]string)
	restored := make(map[string]*Plan, len(rows))
	for _, r := range rows {
		hint, ok := hints[r.Project]
		if !ok {
			hint = buildDocsHint(store, r.Project)
			hints[r.Project] = hint
		}
		restored[r.ThreadID] = &Plan{
			Content:  r.Content,
			Items:    parseStoredPlanItems(r.Items),
			Status:   r.Status,
			Scope:    r.Scope,
			Project:  r.Project,
			ThreadID: r.ThreadID,
			// DocsHint is derived from the project's doc sources and never
			// persisted, so a reloaded plan has to rebuild it or its
			// checkpoints lose the docs reminder until the next set_plan.
			DocsHint:  hint,
			CreatedAt: r.CreatedAt,
			UpdatedAt: r.UpdatedAt,
		}
	}

	planStore.Lock()
	for threadID, p := range restored {
		planStore.plans[threadID] = p
	}
	planStore.Unlock()
}

// planPersistMu serializes [snapshot, write] pairs of one plan.
//
// Without it two parallel update_plan calls can invert: caller A reads the plan,
// caller B reads the newer one, B writes, A writes — and the DB keeps the older
// content while memory holds the newer one. Under this mutex every writer reads
// the state as it is when it gets its turn, so the last write is the current
// state.
var planPersistMu sync.Mutex

// persistPlan writes the plan of threadID to the DB. The snapshot is taken
// under planStore's read lock: reading the *Plan after Unlock raced against a
// concurrent update_plan that was mutating the same struct.
func (h *Handler) persistPlan(threadID string) {
	if h.store == nil {
		return
	}
	planPersistMu.Lock()
	defer planPersistMu.Unlock()

	planStore.RLock()
	p, ok := planStore.plans[threadID]
	if !ok || p == nil {
		planStore.RUnlock()
		return
	}
	row := storage.PlanRow{
		ThreadID:  p.ThreadID,
		Content:   p.Content,
		Items:     planItemsJSON(p.Items),
		Status:    p.Status,
		Scope:     p.Scope,
		Project:   p.Project,
		CreatedAt: p.CreatedAt,
		UpdatedAt: p.UpdatedAt,
	}
	planStore.RUnlock()

	if err := h.store.UpsertPlan(&row); err != nil {
		// A failed write leaves the cache and the database out of step, and the
		// next restart would load the older plan. Silent is the one thing it must
		// not be.
		log.Printf("[plan] persist failed for thread %q: %v", threadID, err)
	}
}

func (h *Handler) handleSetPlan(params map[string]any) Response {
	plan, _ := params["plan"].(string)
	items, _ := parsePlanItems(params)
	// An outline alone is a complete plan: its steps carry everything the
	// display needs, and the free text is derived from them. An empty outline is
	// not a plan, though: it would replace the active one with nothing and take
	// the context-loss anchor with it.
	if plan == "" && len(items) == 0 {
		return errorResponse("plan is required")
	}
	scope, _ := params["scope"].(string)
	if scope == "" {
		scope = "session"
	}
	threadID, _ := params["thread_id"].(string)
	// Store the resolved project, not the raw param: the MCP schema for
	// set_plan carries only plan/scope, so the param is empty on the normal
	// path while the docs hint is built from the cwd fallback. A plan restored
	// after a daemon restart rebuilds its hint from this value and would
	// otherwise fall back to globals-only.
	project := docsHintProject(params)

	content := plan
	if content == "" {
		content = derivePlanContent(items)
	}

	now := time.Now()
	p := &Plan{
		Content:   content,
		Items:     items,
		Status:    "active",
		Scope:     scope,
		Project:   project,
		ThreadID:  threadID,
		CreatedAt: now,
		UpdatedAt: now,
	}

	// Build docs hint from indexed reference sources, scoped to the session's
	// project. set_plan carries no `project` param (the MCP schema exposes only
	// plan/scope), so fall back to the caller's working directory — otherwise
	// the hint would degrade to globals-only and drop the session's own docs.
	if h.store != nil {
		p.DocsHint = buildDocsHint(h.store, project)
	}

	planStore.Lock()
	planStore.plans[threadID] = p
	planStore.Unlock()

	h.persistPlan(threadID)

	message := fmt.Sprintf("Plan set (%s scope, %d chars)", scope, len(content))
	if len(items) > 0 {
		message = fmt.Sprintf("Plan set (%s scope, %d items)", scope, len(items))
	}
	return jsonResponse(map[string]any{
		"message": message,
		"status":  "active",
		"plan":    content,
	})
}

func (h *Handler) handleUpdatePlan(params map[string]any) Response {
	threadID, _ := params["thread_id"].(string)

	// A plan written before set_plan persisted the resolved project carries an
	// empty one. Resolve it here as well, and outside planStore's lock: the
	// lookup walks the filesystem for a .git root and buildDocsHint runs a store
	// query, while GetActivePlan sits on the request path.
	needsBackfill := false
	planStore.RLock()
	if existing, ok := planStore.plans[threadID]; ok && existing != nil && existing.Project == "" {
		needsBackfill = true
	}
	planStore.RUnlock()

	backfillProject := ""
	backfillHint := ""
	if needsBackfill {
		backfillProject = docsHintProject(params)
		if backfillProject != "" && h.store != nil {
			backfillHint = buildDocsHint(h.store, backfillProject)
		}
	}

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

	if backfillProject != "" && p.Project == "" {
		p.Project = backfillProject
		if h.store != nil {
			p.DocsHint = backfillHint
		}
		log.Printf("[plan] backfilled project %q on thread %q", backfillProject, threadID)
	}

	// Apply updates. An item list makes the items the source of truth: the
	// string operations then act on items and the free text is re-derived from
	// them, so the two cannot drift apart. Without items the old text
	// operations run unchanged.
	items, hasItems := parsePlanItems(params)
	itemsChanged := false
	removeEmptied := false
	if hasItems {
		p.Items = items
		itemsChanged = true
	}
	if completed, ok := params["completed"].([]any); ok {
		for _, item := range completed {
			s, ok := item.(string)
			if !ok || strings.TrimSpace(s) == "" {
				// An empty needle matches everything; it would complete or remove
				// the whole plan.
				continue
			}
			if len(p.Items) > 0 {
				if completePlanItems(p.Items, s) {
					itemsChanged = true
				}
				continue
			}
			p.Content = markCompleted(p.Content, s)
		}
	}
	if add, ok := params["add"].([]any); ok {
		for _, item := range add {
			s, ok := item.(string)
			if !ok {
				continue
			}
			s = strings.TrimSpace(s)
			if s == "" {
				continue
			}
			if len(p.Items) > 0 {
				if len(p.Items) >= maxPlanItems {
					continue
				}
				p.Items = append(p.Items, PlanItem{Content: planClip(s, maxPlanItemRunes), Status: "pending"})
				itemsChanged = true
				continue
			}
			p.Content += "\n" + s
		}
	}
	if remove, ok := params["remove"].([]any); ok {
		for _, item := range remove {
			s, ok := item.(string)
			if !ok || strings.TrimSpace(s) == "" {
				continue
			}
			if len(p.Items) > 0 {
				if removePlanItems(&p.Items, s) {
					itemsChanged = true
					if len(p.Items) == 0 {
						removeEmptied = true
					}
				}
				continue
			}
			p.Content = removeLine(p.Content, s)
		}
	}
	// Allow direct content replacement. A full text supplied in this call wins
	// over the derived snapshot: overwriting caller input is worse than a
	// snapshot that lags behind the items. Otherwise an item-level change
	// re-derives, because the old text describes the old items — except when the
	// outline was emptied on purpose, where the last text is the only
	// representation left and blanking it would delete the plan.
	if newContent, ok := params["plan"].(string); ok && newContent != "" {
		p.Content = newContent
	} else if itemsChanged && len(p.Items) > 0 {
		p.Content = derivePlanContent(p.Items)
	} else if removeEmptied {
		// The text described the steps that were just removed. Keeping it would
		// make the ledger fall back to full text and inject deleted work as open
		// steps, which is worse than a plan with nothing left in it.
		p.Content = ""
	}

	p.UpdatedAt = time.Now()
	content := p.Content
	planStore.Unlock()

	h.persistPlan(threadID)

	return jsonResponse(map[string]any{
		"message": "Plan updated",
		"status":  "active",
		"plan":    content,
	})
}

func (h *Handler) handleGetPlan(params map[string]any) Response {
	threadID, _ := params["thread_id"].(string)

	// Read every field under the lock: another goroutine mutates this plan in
	// place under the write lock (update_plan), so a field read after RUnlock
	// raced against that write.
	planStore.RLock()
	defer planStore.RUnlock()
	p, ok := planStore.plans[threadID]
	if !ok || p == nil {
		return jsonResponse(map[string]any{"exists": false, "plan": "", "status": ""})
	}
	out := map[string]any{
		"exists":    true,
		"plan":      p.Content,
		"status":    p.Status,
		"scope":     p.Scope,
		"project":   p.Project,
		"docs_hint": p.DocsHint,
	}
	// Only present when the plan has an outline: a content-only plan answers
	// exactly as it did before items existed.
	if len(p.Items) > 0 {
		out["items"] = p.Items
	}
	return jsonResponse(out)
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

	h.persistPlan(threadID)

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

// The item array is the single validation boundary of a plan: every renderer
// caps what it draws, so the stored outline has to be bounded here as well or
// the status file and the get_plan payload grow with the caller's input. The
// rune caps exclude the "…" that marks a cut field.
const (
	maxPlanItems       = 64
	maxPlanItemRunes   = 500
	maxPlanReasonRunes = 300
	maxPlanStatusRunes = 32
)

// parsePlanItems reads the optional items array. present is false when the
// request carries no items at all, which is the difference between "this plan
// has no outline" and "clear the outline". Entries without a content string are
// dropped rather than stored as empty steps.
func parsePlanItems(params map[string]any) (items []PlanItem, present bool) {
	raw, ok := params["items"]
	if !ok {
		return nil, false
	}
	list, ok := raw.([]any)
	if !ok {
		return nil, false
	}
	if len(list) > maxPlanItems {
		list = list[:maxPlanItems]
	}
	items = make([]PlanItem, 0, len(list))
	for _, entry := range list {
		m, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		content := strings.TrimSpace(stringOr(m, "content", ""))
		if content == "" {
			continue
		}
		items = append(items, PlanItem{
			Content: planClip(content, maxPlanItemRunes),
			Status:  planClip(strings.TrimSpace(stringOr(m, "status", "")), maxPlanStatusRunes),
			Reason:  planClip(strings.TrimSpace(stringOr(m, "reason", "")), maxPlanReasonRunes),
			Depth:   intOr(m, "depth", 0),
		})
	}
	return items, true
}

// planClip bounds a single field so one step cannot dominate the stored plan.
func planClip(text string, max int) string {
	runes := []rune(text)
	if len(runes) <= max {
		return text
	}
	return string(runes[:max]) + "…"
}

// planItemMarker renders a status as a checklist marker. An unknown status
// renders as an open step: a display must survive a value it does not know.
func planItemMarker(status string) string {
	switch status {
	case "completed":
		return "[x]"
	case "in_progress":
		return "[>]"
	case "cancelled":
		return "[-]"
	default:
		return "[ ]"
	}
}

// derivePlanContent renders items into the free-text twin. It exists for
// readers that only understand a string (the MCP plan formatter, an older
// client); the items stay the source of truth.
func derivePlanContent(items []PlanItem) string {
	var b strings.Builder
	for _, it := range items {
		b.WriteString(strings.Repeat("  ", clampPlanDepth(it.Depth)))
		b.WriteString("- " + planItemMarker(it.Status) + " " + planOneLine(it.Content, 200))
		if reason := planOneLine(it.Reason, 120); reason != "" {
			b.WriteString(" (" + reason + ")")
		}
		b.WriteString("\n")
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// clampPlanDepth keeps the derived snapshot inside the three levels the model
// defines. The stored value stays as given; only the rendering is clamped.
func clampPlanDepth(depth int) int {
	if depth < 0 {
		return 0
	}
	if depth > 2 {
		return 2
	}
	return depth
}

// planOneLine flattens text that may carry line breaks into one line of at
// most max runes, so a single step cannot break the list it is rendered into.
// Control characters become spaces: this text is the plan a reader gets back,
// and it ends up in a terminal.
func planOneLine(text string, max int) string {
	stripped := strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) || r == 0x2028 || r == 0x2029 {
			return ' '
		}
		return r
	}, text)
	flat := strings.Join(strings.Fields(stripped), " ")
	runes := []rune(flat)
	if len(runes) <= max {
		return flat
	}
	return string(runes[:max]) + "…"
}

// completePlanItems marks every item whose content contains needle. The legacy
// text path marks every matching line, so the item path does the same: the same
// call must not depend on whether the plan happens to have an outline.
func completePlanItems(items []PlanItem, needle string) bool {
	changed := false
	for i := range items {
		if !strings.Contains(items[i].Content, needle) {
			continue
		}
		if items[i].Status != "completed" {
			changed = true
		}
		items[i].Status = "completed"
	}
	return changed
}

func removePlanItems(items *[]PlanItem, needle string) bool {
	kept := (*items)[:0]
	removed := false
	for _, it := range *items {
		if strings.Contains(it.Content, needle) {
			removed = true
			continue
		}
		kept = append(kept, it)
	}
	*items = kept
	return removed
}

func planItemsJSON(items []PlanItem) string {
	if len(items) == 0 {
		return ""
	}
	raw, err := json.Marshal(items)
	if err != nil {
		return ""
	}
	return string(raw)
}

func parseStoredPlanItems(raw string) []PlanItem {
	if raw == "" {
		return nil
	}
	var items []PlanItem
	if json.Unmarshal([]byte(raw), &items) != nil {
		return nil
	}
	return items
}

// HasActivePlan returns true if there is an active plan for a thread.
func HasActivePlan(threadID string) bool {
	planStore.RLock()
	defer planStore.RUnlock()
	p, ok := planStore.plans[threadID]
	return ok && p != nil && p.Status == "active"
}

// buildDocsHint queries reference doc sources and builds a formatted reminder
// for plan checkpoint injection. The hint is scoped to `project` (plus global
// sources); a foreign project's reference docs must never appear here.
// Returns "" if no reference sources exist for the project.
func buildDocsHint(store *storage.Store, project string) string {
	sources, err := store.GetReferenceSources(project)
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

// docsHintTTL bounds how long a cached docs hint is served.
const docsHintTTL = 5 * time.Minute

// docsHintProject resolves the project to scope a docs hint to: the explicit
// `project` param, else the caller's working directory. resolveProjectParam
// only canonicalizes an already-present `project`, so without this fallback
// callers that never send one (the MCP set_plan path) would scope to "" and
// lose their own reference docs.
func docsHintProject(params map[string]any) string {
	if p := stringOr(params, "project", ""); p != "" {
		return repo.RootOrSelf(p)
	}
	if cwd, _ := params["_cwd"].(string); cwd != "" {
		return repo.RootOrSelf(models.ProjectShortFromPath(cwd))
	}
	return ""
}

// docsHintEntry is one cached docs hint for a project.
type docsHintEntry struct {
	hint    string
	builtAt time.Time
}

// docsHintCache caches the formatted docs hint per project with a TTL.
// Keying by project matters: a single global cache served one project's hint
// to every other project for the whole TTL window.
var docsHintCache = struct {
	sync.RWMutex
	entries map[string]docsHintEntry
}{entries: make(map[string]docsHintEntry)}

// handleGetDocsHint returns the cached docs hint for the requested project,
// rebuilding if stale (>docsHintTTL). Used by proxy for subagent injection.
func (h *Handler) handleGetDocsHint(params map[string]any) Response {
	project := docsHintProject(params)

	docsHintCache.RLock()
	entry, ok := docsHintCache.entries[project]
	docsHintCache.RUnlock()

	if ok && time.Since(entry.builtAt) < docsHintTTL && entry.hint != "" {
		return jsonResponse(map[string]any{"docs_hint": entry.hint})
	}

	// Rebuild
	if h.store == nil {
		return jsonResponse(map[string]any{"docs_hint": ""})
	}
	hint := buildDocsHint(h.store, project)

	docsHintCache.Lock()
	if docsHintCache.entries == nil {
		docsHintCache.entries = make(map[string]docsHintEntry)
	}
	// Drop expired entries so the map stays bounded by the projects seen within
	// one TTL window rather than growing with every project string ever sent.
	for k, e := range docsHintCache.entries {
		if time.Since(e.builtAt) >= docsHintTTL {
			delete(docsHintCache.entries, k)
		}
	}
	docsHintCache.entries[project] = docsHintEntry{hint: hint, builtAt: time.Now()}
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
