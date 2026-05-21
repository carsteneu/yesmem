package daemon

import (
	"encoding/json"
	"fmt"
	"log"
	"path/filepath"
	"strings"

	"github.com/carsteneu/yesmem/internal/capfile"
	"github.com/carsteneu/yesmem/internal/models"
	"github.com/carsteneu/yesmem/internal/storage"
	"github.com/carsteneu/yesmem/internal/textutil"
)

type capResult struct {
	ID      int64   `json:"id"`
	Project string  `json:"project,omitempty"`
	Source  string  `json:"source"`
	Meta    CapMeta `json:"meta"`
}

func (h *Handler) handleGetCaps(params map[string]any) Response {
	project := stringOr(params, "project", "")
	name := stringOr(params, "name", "")
	tag := stringOr(params, "tag", "")
	limit := intOr(params, "limit", 50)

	learnings, err := h.store.GetActiveLearnings("cap", project, "", "", 0)
	if err != nil {
		return errorResponse(fmt.Sprintf("get_caps: %v", err))
	}

	var results []capResult
	for _, l := range learnings {
		meta, err := ParseCapMeta(l.Context)
		if err != nil {
			continue
		}
		if name != "" && meta.Name != name {
			continue
		}
		if tag != "" && !meta.HasTag(tag) {
			continue
		}
		results = append(results, capResult{
			ID:      l.ID,
			Project: l.Project,
			Source:  l.Source,
			Meta:    h.filterSetupActions(meta),
		})
		if len(results) >= limit {
			break
		}
	}
	if results == nil {
		results = []capResult{}
	}
	return jsonResponse(results)
}

func (h *Handler) filterSetupActions(meta CapMeta) CapMeta {
	if len(meta.Actions) == 0 || meta.Actions["setup"] == "" {
		return meta
	}
	rows, err := h.store.CapsQuery(meta.Name, "config", "key = ?", []any{"_setup_complete"}, 1)
	if err == nil && len(rows) > 0 {
		filtered := make(map[string]string, len(meta.Actions))
		for k, v := range meta.Actions {
			if k != "setup" {
				filtered[k] = v
			}
		}
		meta.Actions = filtered
		if len(meta.Actions) == 0 {
			meta.Actions = nil
		}
	}
	return meta
}

func (h *Handler) handleSaveCap(params map[string]any) Response {
	name := stringOr(params, "name", "")
	if name == "" {
		return errorResponse("save_cap: 'name' is required")
	}
	if err := storage.ValidateCapName(name); err != nil {
		return errorResponse(fmt.Sprintf("save_cap: %v", err))
	}
	description := stringOr(params, "description", "")
	project := stringOr(params, "project", "")
	testDate := stringOr(params, "test_date", "")
	source := stringOr(params, "source", "user_stated")

	tags := parseTagsParam(params)
	tested, _ := params["tested"].(bool)
	autoActive := true
	if v, ok := params["auto_active"].(bool); ok {
		autoActive = v
	}

	// Auto-supersede: find existing cap with same name.
	// Skip if version unchanged (avoids churn on daemon restart).
	incomingVersion := 0
	if v, ok := params["version"].(float64); ok {
		incomingVersion = int(v)
	}
	var supersededID *int64
	var oldMeta *CapMeta
	version := 1
	existing, err := h.store.GetActiveLearnings("cap", project, "", "", 0)
	if err != nil {
		return errorResponse(fmt.Sprintf("save_cap supersede lookup: %v", err))
	}
	for _, l := range existing {
		meta, err := ParseCapMeta(l.Context)
		if err != nil {
			continue
		}
		if meta.Name == name {
			if incomingVersion > 0 && incomingVersion == meta.Version {
				return jsonResponse(map[string]any{"id": l.ID, "status": "unchanged"})
			}
			id := l.ID
			supersededID = &id
			version = meta.Version + 1
			metaCopy := meta
			oldMeta = &metaCopy
			break
		}
	}

	content := name + " — " + description
	hash := textutil.ContentHash(content)

	hasScriptsParam := stringOr(params, "scripts", "") != ""
	hasLegacyHandlers := stringOr(params, "handler_bash", "") != "" || stringOr(params, "handler_repl", "") != ""

	var scripts []ScriptMeta
	if hasScriptsParam || hasLegacyHandlers {
		scripts, err = scriptsFromSaveCapParams(name, params)
		if err != nil {
			return errorResponse(fmt.Sprintf("save_cap: %v", err))
		}
		// Merge is only needed for the scripts-array path — legacy
		// handler params are deprecated and replace all scripts.
		if hasScriptsParam && oldMeta != nil {
			scripts = mergeScripts(oldMeta.Scripts, scripts)
		}
	} else if oldMeta != nil {
		scripts = oldMeta.Scripts
	} else {
		return errorResponse("save_cap: 'scripts' is required for new caps")
	}

	meta := CapMeta{
		Name:        name,
		Description: description,
		Scripts:     scripts,
		Requires:    detectRequiresFromScriptMetas(scripts),
		Tags:        tags,
		Version:     version,
		Tested:      tested,
		TestDate:    testDate,
		AutoActive:  autoActive,
	}
	if actionsStr := stringOr(params, "actions", ""); actionsStr != "" {
		if err := json.Unmarshal([]byte(actionsStr), &meta.Actions); err != nil {
			return errorResponse(fmt.Sprintf("save_cap: actions: %v", err))
		}
	}
	ctxJSON, err := meta.ToJSON()
	if err != nil {
		return errorResponse(fmt.Sprintf("save_cap: %v", err))
	}

	l := &models.Learning{
		Content:            content,
		Category:           "cap",
		Source:             source,
		Project:            project,
		CanonicalProject:   canonicalProjectFor(project),
		Confidence:         1.0,
		Context:            ctxJSON,
		Domain:             "code",
		TriggerRule:        fmt.Sprintf("cap:%s", name),
		Keywords:           tags,
		AnticipatedQueries: append([]string{name, description}, tags...),
		CreatedAt:          timeNow(),
		ContentHash:        hash,
	}

	// Capabilities get clean embedding text — name + description + tags, NOT raw JSON Context
	l.EmbeddingText = content
	if len(tags) > 0 {
		l.EmbeddingText += " " + strings.Join(tags, " ")
	}

	id, err := h.store.InsertLearning(l)
	if err != nil {
		return errorResponse(fmt.Sprintf("save_cap: %v", err))
	}

	if supersededID != nil {
		if err := h.store.SupersedeLearning(*supersededID, id, "cap version upgrade"); err != nil {
			log.Printf("save_cap: supersede %d → %d failed: %v", *supersededID, id, err)
		}
	}

	// Post-mutation: embed + MEMORY.md regen
	embedText := content
	if l.EmbeddingText != "" {
		embedText = l.EmbeddingText
	}
	h.EmbedLearning(id, embedText, "cap", project)
	go h.onMutation()

	// Best-effort: write CAP.md to disk so it's visible as a file
	// Skip when import came from disk (_from_disk flag) to avoid overwriting user edits
	if _, fromDisk := params["_from_disk"]; !fromDisk {
		capsDir := filepath.Join(filepath.Dir(h.dataDir), "caps")
		if err := WriteCapToDisk(meta, project, capsDir, h.store); err != nil {
			log.Printf("save_cap: write CAP.md for %s: %v", name, err)
		}
	}

	result := map[string]any{"id": id, "version": version, "name": name}
	if supersededID != nil {
		result["superseded"] = *supersededID
	}
	return jsonResponse(result)
}

// handleRegisterCaps generates JavaScript registerTool() code for saved caps.
// Claude executes this in the REPL to make caps available as native tools.
func (h *Handler) handleRegisterCaps(params map[string]any) Response {
	project, _ := params["project"].(string)
	tag, _ := params["tag"].(string)

	caps, err := h.store.GetActiveLearnings("cap", project, "", "", 0)
	if err != nil {
		return errorResponse(fmt.Sprintf("register_caps: %v", err))
	}

	var lines []string
	for _, l := range caps {
		meta, err := ParseCapMeta(l.Context)
		if err != nil {
			continue
		}
		if tag != "" && !meta.HasTag(tag) {
			continue
		}

		for _, sc := range meta.ToolScripts() {
			fn := scriptToJSFunc(sc)
			fn = capfile.ProviderToGeneric(fn, capfile.DefaultAdapters())
			if sc.Runtime == "repl" && capfile.UsesStoreAdapter(fn) {
				fn = capfile.WrapToolWithStore(fn, meta.Name)
			}

			schemaJS := "{}"
			if sc.Schema != "" && json.Valid([]byte(sc.Schema)) {
				schemaJS = sc.Schema
			}
			line := fmt.Sprintf("registerTool(%q, %q, %s, %s);", sc.Name, meta.Description, schemaJS, fn)
			lines = append(lines, line)
		}
	}

	code := strings.Join(lines, "\n\n")
	if code != "" {
		sharedJS := capfile.GenerateAdapterJS(capfile.DefaultAdapters(), true)
		if sharedJS != "" {
			code = sharedJS + "\n\n" + code
		}
	}
	return jsonResponse(map[string]any{"code": code, "count": len(lines)})
}

// parseTagsParam handles both []any (JSON array) and comma-separated string.
func parseTagsParam(params map[string]any) []string {
	if arr, ok := params["tags"].([]any); ok {
		var tags []string
		for _, v := range arr {
			if s, ok := v.(string); ok && s != "" {
				tags = append(tags, s)
			}
		}
		return tags
	}
	if csv := stringOr(params, "tags", ""); csv != "" {
		var tags []string
		for _, t := range strings.Split(csv, ",") {
			if trimmed := strings.TrimSpace(t); trimmed != "" {
				tags = append(tags, trimmed)
			}
		}
		return tags
	}
	return nil
}

// handleActivateCap marks a cap active for the current thread
// and returns registerTool() JS code that the REPL can execute to make the
// cap tool-callable. The activation row persists in
// session_active_caps so the proxy can re-inject the registration on
// subsequent turns without the caller re-running the code.
//
// IMPORTANT: Call this as a native MCP tool, not from the REPL. Claude Code's
// REPL VM binds only a subset of MCP tools as JS globals (exact selection is
// VM-init-time). Calling activate_cap from the REPL may fail with a
// confusing ReferenceError.
func (h *Handler) handleActivateCap(params map[string]any) Response {
	name := stringOr(params, "name", "")
	if name == "" {
		return errorResponse("activate_cap: 'name' is required")
	}
	threadID := h.resolveSessionID(params, "thread_id")
	if threadID == "" {
		return errorResponse("activate_cap: 'thread_id' is required")
	}
	project := stringOr(params, "project", "")

	caps, err := h.store.GetActiveLearnings("cap", "", "", "", 0)
	if err != nil {
		return errorResponse(fmt.Sprintf("activate_cap: %v", err))
	}

	// Capability names are unique (enforced by validateCapName at save
	// time in handleSaveCap), so the first match is canonical. If two
	// ever collide we deterministically pick the one GetActiveLearnings
	// returned first.
	var match *models.Learning
	var meta CapMeta
	for i := range caps {
		m, err := ParseCapMeta(caps[i].Context)
		if err != nil {
			continue
		}
		if m.Name == name {
			match = &caps[i]
			meta = m
			break
		}
	}
	if match == nil {
		return errorResponse(fmt.Sprintf("activate_cap: no cap found with name %q", name))
	}

	if match.Project != "" && match.Project != project {
		return errorResponse(fmt.Sprintf(
			"activate_cap: cap %q is scoped to project %q (got project=%q)",
			name, match.Project, project,
		))
	}

	tools := meta.ToolScripts()
	if len(tools) == 0 {
		return errorResponse(fmt.Sprintf("activate_cap: cap %q has no tool scripts (only handlers)", name))
	}

	var regLines []string
	usesAdapters := false
	for _, sc := range tools {
		fn := scriptToJSFunc(sc)
		fn = capfile.ProviderToGeneric(fn, capfile.DefaultAdapters())
		if capfile.UsesGenericAdapters(fn) {
			usesAdapters = true
		}
		if sc.Runtime == "repl" && capfile.UsesStoreAdapter(fn) {
			fn = capfile.WrapToolWithStore(fn, meta.Name)
		}
		schemaJS := "{}"
		if sc.Schema != "" && json.Valid([]byte(sc.Schema)) {
			schemaJS = sc.Schema
		}
		regLines = append(regLines, fmt.Sprintf("registerTool(%q, %q, %s, %s);", sc.Name, meta.Description, schemaJS, fn))
	}

	code := strings.Join(regLines, "\n")
	if usesAdapters {
		sharedJS := capfile.GenerateAdapterJS(capfile.DefaultAdapters(), true)
		if sharedJS != "" {
			code = sharedJS + "\n\n" + code
		}
	}

	if err := h.store.ActivateCap(threadID, name); err != nil {
		return errorResponse(fmt.Sprintf("activate_cap: %v", err))
	}

	return jsonResponse(map[string]any{
		"code":        code,
		"name":        name,
		"version":     meta.Version,
		"description": meta.Description,
	})
}

// handleDeactivateCap removes the activation row for (thread, name).
// After deactivation the proxy will not re-inject the cap's
// registerTool snippet on subsequent turns.
func (h *Handler) handleDeactivateCap(params map[string]any) Response {
	name := stringOr(params, "name", "")
	if name == "" {
		return errorResponse("deactivate_cap: 'name' is required")
	}
	threadID := h.resolveSessionID(params, "thread_id")
	if threadID == "" {
		return errorResponse("deactivate_cap: 'thread_id' is required")
	}

	deleted, err := h.store.DeactivateCap(threadID, name)
	if err != nil {
		return errorResponse(fmt.Sprintf("deactivate_cap: %v", err))
	}
	return jsonResponse(map[string]any{
		"name":        name,
		"deactivated": deleted,
	})
}

// handleGetActiveCaps returns the caps currently active for
// the given thread, each with its full meta. Intended for the proxy's
// schema-injection pipeline — not exposed as a user-facing MCP tool.
func (h *Handler) handleGetActiveCaps(params map[string]any) Response {
	threadID := h.resolveSessionID(params, "thread_id")
	if threadID == "" {
		return errorResponse("get_active_caps: 'thread_id' is required")
	}

	active, err := h.store.GetSessionCaps(threadID)
	if err != nil {
		return errorResponse(fmt.Sprintf("get_active_caps: %v", err))
	}

	if parentID, ok := params["parent_thread_id"].(string); ok && parentID != "" && parentID != threadID {
		parentActive, err := h.store.GetSessionCaps(parentID)
		if err != nil {
			log.Printf("[caps] parent_thread_id=%s lookup failed: %v", parentID, err)
		} else {
			active = append(active, parentActive...)
		}
	}

	caps, err := h.store.GetActiveLearnings("cap", "", "", "", 0)
	if err != nil {
		return errorResponse(fmt.Sprintf("get_active_caps: %v", err))
	}
	byName := make(map[string]*models.Learning)
	metaByName := make(map[string]CapMeta)
	autoActiveNames := []string{}
	for i := range caps {
		m, err := ParseCapMeta(caps[i].Context)
		if err != nil {
			continue
		}
		byName[m.Name] = &caps[i]
		metaByName[m.Name] = m
		if m.AutoActive {
			autoActiveNames = append(autoActiveNames, m.Name)
		}
	}

	results := []capResult{}
	seen := make(map[string]bool)
	for _, a := range active {
		if seen[a.CapName] {
			continue
		}
		l, ok := byName[a.CapName]
		if !ok {
			continue
		}
		results = append(results, capResult{
			ID:      l.ID,
			Project: l.Project,
			Source:  l.Source,
			Meta:    metaByName[a.CapName],
		})
		seen[a.CapName] = true
	}
	for _, name := range autoActiveNames {
		if seen[name] {
			continue
		}
		l := byName[name]
		results = append(results, capResult{
			ID:      l.ID,
			Project: l.Project,
			Source:  l.Source,
			Meta:    metaByName[name],
		})
		seen[name] = true
	}
	return jsonResponse(results)
}
