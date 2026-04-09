package daemon

import (
	"context"
	"crypto/sha256"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/carsteneu/yesmem/internal/extraction"
	"github.com/carsteneu/yesmem/internal/ingest"
	"github.com/carsteneu/yesmem/internal/storage"
)

var embedDocChunksMu sync.Mutex

// handleDocsSearch handles the "docs_search" daemon command.
// Parameters: query (required), source (optional), section (optional), project (optional),
// exact (optional bool), limit (optional int), extensions (optional []string), doc_type (optional string)
func (h *Handler) handleDocsSearch(params map[string]any) Response {
	query, _ := params["query"].(string)
	if query == "" {
		return errorResponse("query is required")
	}
	source := stringOr(params, "source", "")
	section := stringOr(params, "section", "")
	project := stringOr(params, "project", "")
	since, _ := params["since"].(string)
	before, _ := params["before"].(string)
	limit := intOr(params, "limit", 5)
	exact, _ := params["exact"].(bool)
	docType := stringOr(params, "doc_type", "")

	// Resolve optional extensions filter to source IDs.
	// When extensions provided: only search sources whose trigger_extensions match.
	// When only doc_type provided: filter all sources by type.
	var sourceIDFilter []int64
	if rawExts, ok := params["extensions"].([]any); ok && len(rawExts) > 0 {
		var exts []string
		for _, e := range rawExts {
			if s, ok := e.(string); ok {
				exts = append(exts, s)
			}
		}
		if len(exts) > 0 {
			sources, err := h.store.GetDocSourcesByExtensions(exts, docType)
			if err == nil {
				for _, src := range sources {
					if project == "" || src.Project == "" || src.Project == project {
						sourceIDFilter = append(sourceIDFilter, src.ID)
					}
				}
			}
		}
	} else if docType != "" {
		// No extensions but doc_type specified — filter all sources by type
		allSources, err := h.store.ListDocSources("")
		if err == nil {
			for _, src := range allSources {
				if src.DocType == docType {
					if project == "" || src.Project == "" || src.Project == project {
						sourceIDFilter = append(sourceIDFilter, src.ID)
					}
				}
			}
		}
	}

	// BM25 via FTS5 (always)
	bm25Results, err := h.store.SearchDocChunksFTS(query, source, section, since, before, limit*2, sourceIDFilter)
	if err != nil {
		return errorResponse(fmt.Sprintf("doc search failed: %v", err))
	}

	// Vector search via SSE embeddings — only as fallback when BM25 found nothing (unless exact mode).
	// Design decision: BM25-first is intentional because exact=true queries benefit from precise term matching.
	// Full hybrid (parallel BM25+vector with RRF merge) is a future option but not currently needed.
	method := "bm25"
	var vectorResults []storage.DocChunkResult
	if !exact && len(bm25Results) == 0 {
		if provider := h.EmbedProvider(); provider != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			vecs, err := provider.Embed(ctx, []string{query})
			if err == nil && len(vecs) > 0 {
				vectorResults, _ = h.store.SearchDocChunksSemantic(vecs[0], limit*2, source)
			}
		}
	}

	// Merge: RRF if both have results, otherwise use whichever has results
	var merged []storage.DocChunkResult
	if len(bm25Results) > 0 && len(vectorResults) > 0 {
		method = "hybrid"
		merged = mergeDocResults(bm25Results, vectorResults, limit)
	} else if len(vectorResults) > 0 {
		method = "semantic"
		merged = vectorResults
	} else {
		merged = bm25Results
	}

	// Trim to requested limit
	if len(merged) > limit {
		merged = merged[:limit]
	}

	// Project boost + filter: when project is set, boost project-matching sources 1.2x
	// (re-ranks them above global docs) then remove any sources belonging to a different project.
	if project != "" {
		sourceProjects := h.store.DocSourceProjectMap()
		// Boost first — promotes project docs within the scored list.
		// BM25 scores are negative (higher = better, i.e. closer to zero).
		// To boost a negative score upward: divide the magnitude (divide by 1.2).
		// Hybrid/semantic scores are positive (higher = better): multiply by 1.2.
		for i := range merged {
			if sourceProjects[merged[i].SourceName] == project {
				if merged[i].Score < 0 {
					merged[i].Score /= 1.2 // less negative = higher = better rank
				} else {
					merged[i].Score *= 1.2
				}
			}
		}
		sort.Slice(merged, func(i, j int) bool {
			return merged[i].Score > merged[j].Score
		})
		// Filter: keep results from matching project or global (no project)
		var filtered []storage.DocChunkResult
		for _, r := range merged {
			srcProject := sourceProjects[r.SourceName]
			if srcProject == "" || srcProject == project {
				filtered = append(filtered, r)
			}
		}
		merged = filtered
	}

	// Format results
	type resultItem struct {
		ID          int64   `json:"id"`
		Source      string  `json:"source"`
		Version     string  `json:"version"`
		HeadingPath string  `json:"heading_path"`
		Content     string  `json:"content"`
		Score       float64 `json:"score"`
		SourceFile  string  `json:"source_file"`
		Tokens      int     `json:"tokens_approx"`
	}

	items := make([]resultItem, 0, len(merged))
	for _, r := range merged {
		score := r.Score
		if method == "bm25" {
			score = -score // BM25 returns negative (lower=better), normalize to positive
		}
		items = append(items, resultItem{
			ID:          r.ID,
			Source:      r.SourceName,
			Version:     r.Version,
			HeadingPath: r.HeadingPath,
			Content:     r.Content,
			Score:       score,
			SourceFile:  r.SourceFile,
			Tokens:      r.TokensApprox,
		})
	}

	return jsonResponse(map[string]any{
		"results": items,
		"total":   len(items),
		"method":  method,
		"message": fmt.Sprintf("Doc search: %d results for %q", len(items), query),
	})
}

// mergeDocResults performs Reciprocal Rank Fusion on BM25 and vector results.
func mergeDocResults(bm25, vector []storage.DocChunkResult, limit int) []storage.DocChunkResult {
	const k = 60 // RRF constant
	type scored struct {
		result storage.DocChunkResult
		score  float64
	}
	scoreMap := make(map[int64]*scored)

	for rank, r := range bm25 {
		s := 1.0 / float64(k+rank+1)
		if existing, ok := scoreMap[r.ID]; ok {
			existing.score += s
		} else {
			scoreMap[r.ID] = &scored{r, s}
		}
	}
	for rank, r := range vector {
		s := 1.0 / float64(k+rank+1)
		if existing, ok := scoreMap[r.ID]; ok {
			existing.score += s
		} else {
			scoreMap[r.ID] = &scored{r, s}
		}
	}

	all := make([]scored, 0, len(scoreMap))
	for _, s := range scoreMap {
		all = append(all, *s)
	}
	sort.Slice(all, func(i, j int) bool {
		return all[i].score > all[j].score
	})

	if len(all) > limit {
		all = all[:limit]
	}
	results := make([]storage.DocChunkResult, len(all))
	for i, s := range all {
		s.result.Score = s.score
		results[i] = s.result
	}
	return results
}

// handleListDocSources lists all registered doc sources.
func (h *Handler) handleListDocSources(params map[string]any) Response {
	project := stringOr(params, "project", "")
	sources, err := h.store.ListDocSources(project)
	if err != nil {
		return errorResponse(fmt.Sprintf("list doc sources failed: %v", err))
	}

	type sourceItem struct {
		Name              string `json:"name"`
		Version           string `json:"version"`
		ChunkCount        int    `json:"chunks"`
		Project           string `json:"project"`
		LastSync          string `json:"last_sync"`
		TriggerExtensions string `json:"trigger_extensions,omitempty"`
	}

	items := make([]sourceItem, 0, len(sources))
	for _, s := range sources {
		items = append(items, sourceItem{
			Name:              s.Name,
			Version:           s.Version,
			ChunkCount:        s.ChunkCount,
			Project:           s.Project,
			LastSync:          s.LastSync.Format("2006-01-02 15:04"),
			TriggerExtensions: s.TriggerExtensions,
		})
	}

	var sb strings.Builder
	if len(items) == 0 {
		sb.WriteString("No documentation sources indexed yet.")
	} else {
		sb.WriteString(fmt.Sprintf("%d documentation source(s) indexed.", len(items)))
	}

	return jsonResponse(map[string]any{
		"sources": items,
		"total":   len(items),
		"message": sb.String(),
	})
}

// handleIngestDocs triggers the doc ingest pipeline via MCP.
// handleRemoveDocs removes a documentation source and all its chunks + learnings.
func (h *Handler) handleRemoveDocs(params map[string]any) Response {
	name := stringOr(params, "name", "")
	if name == "" {
		return errorResponse("name is required")
	}
	project := stringOr(params, "project", "")

	// Check if source exists first
	src, err := h.store.GetDocSource(name, project)
	if err != nil || src == nil {
		return errorResponse(fmt.Sprintf("doc source '%s' not found", name))
	}

	chunkCount := src.ChunkCount
	delResult, err := h.store.DeleteDocSource(name, project)
	if err != nil {
		return errorResponse(fmt.Sprintf("remove failed: %v", err))
	}

	learningsDeleted := len(delResult.DeletedLearningIDs)

	// Clean up clone directory
	if delResult != nil && delResult.SourcePath != "" {
		home, _ := os.UserHomeDir()
		if home != "" {
			cloneDir := filepath.Join(home, ".claude", "yesmem", "docs", name)
			if _, statErr := os.Stat(cloneDir); statErr == nil {
				os.RemoveAll(cloneDir)
				log.Printf("Clone directory removed: %s", cloneDir)
			}
		}
	}

	// Clean up orphaned embeddings
	if h.vectorStore != nil && len(delResult.DeletedLearningIDs) > 0 {
		for _, lid := range delResult.DeletedLearningIDs {
			h.vectorStore.Delete(context.Background(), fmt.Sprintf("l:%d", lid))
		}
	}

	return jsonResponse(map[string]any{
		"message":            fmt.Sprintf("Removed '%s' — %d chunks + %d learnings deleted", name, chunkCount, learningsDeleted),
		"name":               name,
		"chunks_deleted":     chunkCount,
		"learnings_deleted":  learningsDeleted,
	})
}

// handleContextualDocs returns top doc chunks from sources matching given file extensions.
// Used by proxy to auto-inject docs when Claude edits specific file types.
func (h *Handler) handleContextualDocs(params map[string]any) Response {
	rawExts, ok := params["extensions"].([]any)
	if !ok || len(rawExts) == 0 {
		return jsonResponse(map[string]any{"results": []any{}, "total": 0})
	}

	var exts []string
	for _, e := range rawExts {
		if s, ok := e.(string); ok {
			exts = append(exts, s)
		}
	}
	if len(exts) == 0 {
		return jsonResponse(map[string]any{"results": []any{}, "total": 0})
	}

	sources, err := h.store.GetDocSourcesByExtensions(exts, "reference")
	if err != nil || len(sources) == 0 {
		return jsonResponse(map[string]any{"results": []any{}, "total": 0})
	}

	project := stringOr(params, "project", "")
	limit := intOr(params, "limit", 3)

	var sourceIDs []int64
	srcNames := make(map[int64]string)
	srcVersions := make(map[int64]string)
	for _, src := range sources {
		if project != "" && src.Project != "" && src.Project != project {
			continue
		}
		sourceIDs = append(sourceIDs, src.ID)
		srcNames[src.ID] = src.Name
		srcVersions[src.ID] = src.Version
	}
	if len(sourceIDs) == 0 {
		return jsonResponse(map[string]any{"results": []any{}, "total": 0})
	}

	chunks, err := h.store.GetDocChunksBySourceIDs(sourceIDs, limit)
	if err != nil {
		return errorResponse(fmt.Sprintf("contextual docs query failed: %v", err))
	}

	type resultItem struct {
		Source      string `json:"source"`
		Version    string `json:"version"`
		HeadingPath string `json:"heading_path"`
		Content    string `json:"content"`
		Tokens     int    `json:"tokens_approx"`
	}

	items := make([]resultItem, 0, len(chunks))
	for _, c := range chunks {
		items = append(items, resultItem{
			Source:      srcNames[c.SourceID],
			Version:    srcVersions[c.SourceID],
			HeadingPath: c.HeadingPath,
			Content:    c.Content,
			Tokens:     c.TokensApprox,
		})
	}

	return jsonResponse(map[string]any{
		"results": items,
		"total":   len(items),
	})
}

// handleListTriggerExtensions returns all unique trigger_extensions across doc sources.
func (h *Handler) handleListTriggerExtensions(params map[string]any) Response {
	project := stringOr(params, "project", "")
	exts, err := h.store.ListTriggerExtensions(project)
	if err != nil {
		return errorResponse(fmt.Sprintf("list trigger extensions: %v", err))
	}
	return jsonResponse(map[string]any{"extensions": exts})
}

func (h *Handler) handleIngestDocs(params map[string]any) Response {
	name := stringOr(params, "name", "")
	if name == "" {
		return errorResponse("name is required")
	}
	path := stringOr(params, "path", "")
	if path == "" {
		return errorResponse("path is required")
	}
	version := stringOr(params, "version", "")
	project := stringOr(params, "project", "")
	domain := stringOr(params, "domain", "code")
	docType := stringOr(params, "doc_type", "")

	rules, _ := params["rules"].(bool)

	// Parse trigger_extensions:
	// - param absent → TriggerExtensions="" → UpsertDocSource preserves existing value
	// - param present + empty array [] → TriggerExtensions="-" (sentinel) → clears existing value
	// - param present + values → TriggerExtensions=".go,.mod" → overwrites
	var triggerExts string
	if rawExts, ok := params["trigger_extensions"].([]any); ok {
		var extParts []string
		for _, e := range rawExts {
			if s, ok := e.(string); ok {
				extParts = append(extParts, s)
			}
		}
		if len(extParts) == 0 {
			triggerExts = "-" // sentinel: explicitly clear
		} else {
			triggerExts = strings.Join(extParts, ",")
		}
	}

	// Rules path: condense CLAUDE.md into a rules block for periodic re-injection
	if rules {
		return h.handleIngestRules(name, path, project)
	}
	cfg := ingest.Config{
		Name:              name,
		Version:           version,
		Project:           project,
		Domain:            domain,
		TriggerExtensions: triggerExts,
		DocType:           docType,
	}

	// Wire LLM client for destillation
	if cfg.Destill && h.SummarizeClient != nil {
		cfg.LLMClient = h.SummarizeClient
	} else if cfg.Destill {
		log.Printf("  warn: --destill requested but no LLM client available, skipping destillation")
		cfg.Destill = false
	}

	result, err := ingest.Run(cfg, []string{path}, h.store)
	if err != nil {
		return errorResponse(fmt.Sprintf("ingest failed: %v", err))
	}

	// Embed new chunks in background
	if result.ChunksCreated > 0 {
		go h.EmbedDocChunks()
	}

	return jsonResponse(map[string]any{
		"message": fmt.Sprintf("Ingest complete: %d files processed, %d skipped, %d chunks created",
			result.FilesProcessed, result.FilesSkipped, result.ChunksCreated),
		"files_processed":      result.FilesProcessed,
		"files_skipped":        result.FilesSkipped,
		"chunks_created":       result.ChunksCreated,
		"learnings_superseded": result.LearningsSuperseded,
	})
}

// handleIngestRules condenses a CLAUDE.md (with link resolution) into a rules block.
func (h *Handler) handleIngestRules(name, sourcePath, project string) Response {
	// Prefer quality client (Sonnet/Opus) for rules — Haiku hallucinates generic rules
	client := h.QualityClient
	if client == nil {
		client = h.SummarizeClient
	}
	if client == nil {
		client = h.CommitEvalClient
	}
	if client == nil {
		return errorResponse("no LLM client available for rules condensation — run extraction cycle first or check API key config")
	}

	condensed, hash, err := CondenseRules(sourcePath, project, client, h.store)
	if err != nil {
		if err.Error() == "unchanged" {
			return jsonResponse(map[string]any{
				"message": "Rules unchanged (same hash), skipped condensation",
				"hash":    h.store.GetRulesHash(project),
			})
		}
		return errorResponse(err.Error())
	}

	log.Printf("[rules] condensed %s → %d chars (hash=%s)", sourcePath, len(condensed), hash[:12])
	return jsonResponse(map[string]any{
		"message":       fmt.Sprintf("Rules condensed: %d chars from %s", len(condensed), filepath.Base(sourcePath)),
		"hash":          hash,
		"condensed_len": len(condensed),
		"source_path":   sourcePath,
	})
}

// CondenseRules reads a CLAUDE.md, resolves links, checks hash, and condenses via LLM.
// Returns (condensed, hash, error). Returns error "unchanged" if hash matches.
func CondenseRules(sourcePath, project string, client extraction.LLMClient, store *storage.Store) (string, string, error) {
	expanded, err := expandWithLinks(sourcePath, 0)
	if err != nil {
		return "", "", fmt.Errorf("read rules source: %v", err)
	}

	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(expanded)))
	if existing := store.GetRulesHash(project); existing == hash {
		return "", hash, fmt.Errorf("unchanged")
	}

	// Step 1: Mechanical pre-filter — remove code blocks, tables, directory trees.
	// These are never behavioral rules. Keeps prose, bullets, headings.
	filtered := mechanicalPreFilter(expanded)

	// Guard: cap filtered input to prevent runaway LLM costs on large CLAUDE.mds
	// with many linked files. 50k chars ≈ 12k tokens input to Sonnet.
	const maxFilteredChars = 50000
	if len(filtered) > maxFilteredChars {
		filtered = filtered[:maxFilteredChars]
	}

	// Step 2: Single LLM call to extract and condense rules, conventions, principles.
	// Using quality model (Sonnet/Opus) — Haiku hallucinates generic rules.
	condensePrompt := `You are a rules extractor for an AI coding assistant. The input has been pre-filtered to remove code blocks, tables, directory trees, and reference sections. Everything remaining is potentially relevant — err on the side of including too much rather than missing behavioral guidance.

EXTRACT (keep original meaning, condense wording):
- Hard rules: "Never X", "Always Y", "Do not Z"
- Conventions: naming, language, patterns ("Queries in German", "BM25 with AND-matching")
- Principles: design philosophy ("Quality over cost", "Memory as agent not for agent")
- Gotchas: things that break if done wrong ("additionalProperties: false on ALL nested objects")
- Process requirements: workflows, review steps, deployment procedures
- Design targets: numerical goals that guide tradeoffs ("Target ~130k tokens")
- Known traps: concurrency issues, API quirks, data integrity constraints

DO NOT EXTRACT:
- Feature completion status ("Phase X LIVE", "merged to main", "commit abc123")
- Pure metrics without actionable context ("save_rate 62%", "3.2% impact")
- Tool/CLI reference lists

Output format: grouped by theme, imperative or descriptive form as appropriate.
Include a brief WHY when the source provides reasoning.
Be thorough — include ALL behavioral guidance, but CONDENSE aggressively. Use terse imperative form.
The output must be significantly shorter than the input — aim for 40-60% reduction.
Same language as input. No preamble, no introduction.`
	condenseUser := fmt.Sprintf("Extract all behavioral guidance from this documentation:\n\n%s", filtered)

	condensed, err := client.Complete(condensePrompt, condenseUser)
	if err != nil {
		return "", "", fmt.Errorf("LLM condensation failed: %v", err)
	}

	ds := &storage.DocSource{
		Name:        "rules",
		Path:        sourcePath,
		Project:     project,
		IsSkill:     false,
		FullContent: condensed,
	}
	sourceID, err := store.UpsertDocSource(ds)
	if err != nil {
		return "", "", fmt.Errorf("upsert doc_source: %v", err)
	}
	if err := store.SaveRulesContent(sourceID, condensed, hash); err != nil {
		return "", "", fmt.Errorf("save rules content: %v", err)
	}

	return condensed, hash, nil
}

// expandWithLinks reads a file and recursively expands @path references (depth max 2).
func expandWithLinks(filePath string, depth int) (string, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return "", err
	}
	if depth >= 2 {
		return string(data), nil
	}
	dir := filepath.Dir(filePath)
	var result strings.Builder
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "@") {
			ref := strings.TrimPrefix(trimmed, "@")
			refPath := filepath.Join(dir, ref)
			if isTextFile(refPath) {
				expanded, err := expandWithLinks(refPath, depth+1)
				if err == nil {
					result.WriteString(expanded)
					result.WriteByte('\n')
					continue
				}
			}
		}
		result.WriteString(line)
		result.WriteByte('\n')
	}
	return result.String(), nil
}

// mechanicalPreFilter removes code blocks, tables, directory trees, reference sections,
// and other non-behavioral content from markdown. Heading-aware: skips entire sections
// whose heading signals reference/listing content. Keeps prose, bullets, headings that
// contain behavioral guidance.
// This is a deterministic pre-processing step — no LLM needed.
func mechanicalPreFilter(input string) string {
	var result strings.Builder
	lines := strings.Split(input, "\n")
	inCodeBlock := false
	inTable := false
	skipSection := false    // true when current section heading signals non-behavioral content
	skipSectionLevel := 0   // heading level that triggered skip (only same or higher level ends it)
	skipIndented := 0

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		// Toggle code blocks
		if strings.HasPrefix(trimmed, "```") {
			inCodeBlock = !inCodeBlock
			continue
		}
		if inCodeBlock {
			continue
		}

		// Heading detection — controls section-level skip
		if strings.HasPrefix(trimmed, "#") {
			level := 0
			for _, c := range trimmed {
				if c == '#' {
					level++
				} else {
					break
				}
			}
			headingText := strings.ToLower(strings.TrimSpace(trimmed[level:]))

			// End section skip if we hit a heading at same or higher level
			if skipSection && level <= skipSectionLevel {
				skipSection = false
			}

			// Check if this heading signals a reference/listing section
			if isReferenceHeading(headingText) {
				skipSection = true
				skipSectionLevel = level
				continue
			}

			// Keep the heading
			inTable = false
			skipIndented = 0
			result.WriteString(line)
			result.WriteByte('\n')
			continue
		}

		// Skip entire reference sections
		if skipSection {
			continue
		}

		// Skip table rows (pipes)
		if strings.Contains(trimmed, "|") && strings.Count(trimmed, "|") >= 2 {
			inTable = true
			continue
		}
		if inTable && trimmed == "" {
			inTable = false
			continue
		}
		if inTable {
			continue
		}

		// Skip directory tree lines
		if strings.ContainsAny(trimmed, "├└│") || strings.HasPrefix(trimmed, "./") || strings.HasPrefix(trimmed, "../") {
			skipIndented++
			continue
		}
		// Skip runs of heavily indented lines (likely directory listings or formatted output)
		// but preserve bullet points
		if len(line) > 0 && (line[0] == '\t' || strings.HasPrefix(line, "    ")) && !strings.HasPrefix(trimmed, "-") && !strings.HasPrefix(trimmed, "*") {
			skipIndented++
			if skipIndented > 3 {
				continue
			}
		} else {
			skipIndented = 0
		}

		// Skip lines that are just file paths, URLs, or bare key:value config
		if isNonBehavioralLine(trimmed) {
			continue
		}

		// Keep everything else
		if trimmed == "" {
			result.WriteByte('\n')
			continue
		}

		result.WriteString(line)
		result.WriteByte('\n')
	}
	return result.String()
}

// isReferenceHeading returns true if a heading signals a reference/listing section
// that never contains behavioral guidance. Matches common CLAUDE.md patterns across projects.
func isReferenceHeading(heading string) bool {
	// Exact patterns that are always reference content
	referencePatterns := []string{
		"schema", "reference", "api endpoint", "cli command", "mcp tool",
		"database", "rpc method", "internal package", "file structure",
		"directory", "route", "endpoint", "table", "migration",
	}
	for _, p := range referencePatterns {
		if strings.Contains(heading, p) {
			return true
		}
	}
	return false
}

// isNonBehavioralLine returns true for lines that are pure data, not guidance.
func isNonBehavioralLine(line string) bool {
	// Bare file paths
	if strings.HasPrefix(line, "/") && !strings.Contains(line, " ") {
		return true
	}
	// URLs without context
	if (strings.HasPrefix(line, "http://") || strings.HasPrefix(line, "https://")) && !strings.Contains(line, " ") {
		return true
	}
	// Lines that are just a command name + description (CLI reference patterns)
	// e.g. "  build          Build binary → ./yesmem"
	if len(line) > 2 && line[0] == ' ' && strings.Contains(line, "  ") {
		parts := strings.Fields(line)
		if len(parts) == 2 && !strings.ContainsAny(parts[0], ".-()") {
			return false // Could be a bullet point abbreviation
		}
	}
	return false
}

func isTextFile(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".md", ".txt", ".rst", ".yaml", ".yml", ".toml":
		return true
	}
	return false
}

// handleGetRulesBlock returns the condensed rules block for proxy re-injection.
func (h *Handler) handleGetRulesBlock(params map[string]any) Response {
	project, _ := params["project"].(string)
	content := h.store.GetRulesContent(project)
	if content == "" {
		return jsonResponse(map[string]any{"content": "", "exists": false})
	}
	return jsonResponse(map[string]any{"content": content, "exists": true})
}

// EmbedDocChunks embeds all doc chunks that don't have embeddings yet.
// Called at daemon startup and after ingest. Mutex prevents parallel runs.
func (h *Handler) EmbedDocChunks() {
	embedDocChunksMu.Lock()
	defer embedDocChunksMu.Unlock()

	provider := h.EmbedProvider()
	if provider == nil {
		return
	}

	chunks, err := h.store.DocChunksWithoutEmbedding()
	if err != nil {
		log.Printf("embed doc chunks: list failed: %v", err)
		return
	}
	if len(chunks) == 0 {
		return
	}

	log.Printf("embed doc chunks: %d chunks to embed", len(chunks))

	// Batch embed (32 at a time)
	const batchSize = 32
	embedded := 0
	for i := 0; i < len(chunks); i += batchSize {
		end := i + batchSize
		if end > len(chunks) {
			end = len(chunks)
		}
		batch := chunks[i:end]

		texts := make([]string, len(batch))
		for j, c := range batch {
			texts[j] = c.HeadingPath + " " + c.Content
		}

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		vecs, err := provider.Embed(ctx, texts)
		cancel()
		if err != nil {
			log.Printf("embed doc chunks: batch %d-%d failed: %v", i, end, err)
			continue
		}

		for j, c := range batch {
			if j >= len(vecs) {
				break
			}
			if err := h.store.SetDocChunkEmbedding(c.ID, vecs[j], c.ContentHash); err != nil {
				log.Printf("embed doc chunks: store chunk %d failed: %v", c.ID, err)
			} else {
				embedded++
			}
		}
	}

	log.Printf("embed doc chunks: %d/%d embedded", embedded, len(chunks))
}
