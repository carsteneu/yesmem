package codescan

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// CBMIndexPath returns the path to the CBM SQLite database for a project.
// Returns empty string if not found.
func CBMIndexPath(rootDir string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	project := cbmProjectName(rootDir)
	path := filepath.Join(home, ".cache", "codebase-memory-mcp", project+".db")
	if _, err := os.Stat(path); err != nil {
		return ""
	}
	return path
}

// CBMIndexMtime returns the modification time of the CBM index for a project.
// Returns zero time if not available.
func CBMIndexMtime(rootDir string) time.Time {
	path := CBMIndexPath(rootDir)
	if path == "" {
		return time.Time{}
	}
	info, err := os.Stat(path)
	if err != nil {
		return time.Time{}
	}
	return info.ModTime()
}

// CBMScanner uses the codebase-memory-mcp CLI to extract code intelligence.
// Requires the codebase-memory-mcp binary to be installed and the project to be indexed.
type CBMScanner struct{}

func NewCBMScanner() *CBMScanner {
	return &CBMScanner{}
}

// FindCBMBinary returns the path to codebase-memory-mcp binary, or empty if not found.
// FindCBMBinary returns the path to the codebase-memory-mcp binary.
// Checks yesmem CLI dir first, then PATH.
func FindCBMBinary() string {
	home, err := os.UserHomeDir()
	if err == nil {
		yesmemPath := filepath.Join(home, ".claude", "yesmem", "cli", "codebase-memory-mcp")
		if _, err := os.Stat(yesmemPath); err == nil {
			return yesmemPath
		}
	}
	if p, err := exec.LookPath("codebase-memory-mcp"); err == nil {
		return p
	}
	return ""
}

func (s *CBMScanner) Scan(rootDir string) (*ScanResult, error) {
	bin := FindCBMBinary()
	if bin == "" {
		return nil, fmt.Errorf("codebase-memory-mcp binary not found")
	}

	project := cbmProjectName(rootDir)

	// Query files via Folder→File edges
	files, err := cbmQuery(bin, project, `
		MATCH (folder:Folder)-[:CONTAINS_FILE]->(f:File)
		RETURN f.file_path AS path, f.name AS name, f.end_line AS loc, folder.file_path AS pkg
		ORDER BY f.file_path`)
	if err != nil || len(files) == 0 {
		// Project not indexed — trigger indexing, then retry
		if indexErr := cbmIndexRepository(bin, rootDir); indexErr == nil {
			project = cbmProjectName(rootDir)
			files, err = cbmQuery(bin, project, `
				MATCH (folder:Folder)-[:CONTAINS_FILE]->(f:File)
				RETURN f.file_path AS path, f.name AS name, f.end_line AS loc, folder.file_path AS pkg
				ORDER BY f.file_path`)
		}
		if err != nil {
			return nil, fmt.Errorf("query files: %w", err)
		}
	}

	// CBM labels some files as Module instead of File (inconsistent).
	// Merge Module nodes that are missing from the File query.
	fileSet := make(map[string]bool)
	for _, row := range files {
		if path, ok := row[0].(string); ok {
			fileSet[path] = true
		}
	}
	modules, _ := cbmQuery(bin, project, `
		MATCH (n) WHERE n.label = 'Module'
		RETURN n.file_path AS path, n.name AS name, n.end_line AS loc
		ORDER BY n.file_path`)
	for _, row := range modules {
		path, _ := row[0].(string)
		if path == "" || fileSet[path] {
			continue
		}
		// Derive package from path: "internal/proxy/foo.go" → "internal/proxy"
		pkg := filepath.Dir(path)
		files = append(files, []interface{}{path, row[1], row[2], pkg})
		fileSet[path] = true
	}

	// Query functions/methods/interfaces/classes
	sigs, err := cbmQuery(bin, project, `
		MATCH (n)
		WHERE n.label IN ['Function', 'Method', 'Interface', 'Class']
		RETURN n.file_path AS path, n.name AS name, n.label AS label
		ORDER BY n.file_path`)
	if err != nil {
		return nil, fmt.Errorf("query signatures: %w", err)
	}

	// Build signature map: file_path → []string
	fileSigs := make(map[string][]string)
	for _, row := range sigs {
		path, _ := row[0].(string)
		name, _ := row[1].(string)
		label, _ := row[2].(string)
		fileSigs[path] = append(fileSigs[path], formatSignature(label, name, ""))
	}

	// Build result
	pkgMap := make(map[string]*PackageInfo)
	var stats ProjectStats
	var allFiles []FileInfo

	// Query imports (available for JS/TS/Python/Ruby in CBM v0.6.0+, Go pending)
	importRows, err := cbmQuery(bin, project, `MATCH (a)-[:IMPORTS]->(b) RETURN a.file_path, b.name ORDER BY a.file_path`)
	if err != nil {
		log.Printf("cbm: imports query: %v", err)
	}
	fileImports := parseImportRows(importRows)

	// Query entry points (func main + HTTP routes)
	entryRows, err := cbmQuery(bin, project, `MATCH (f) WHERE f.label = 'Function' AND f.name = 'main' RETURN f.name, f.file_path`)
	if err != nil {
		log.Printf("cbm: entry points query: %v", err)
	}
	routeRows, err := cbmQuery(bin, project, `MATCH (r) WHERE r.label = 'Route' RETURN r.name, r.file_path`)
	if err != nil {
		log.Printf("cbm: routes query: %v", err)
	}
	entryPoints := dedup(append(parseEntryPoints(entryRows), parseEntryPoints(routeRows)...))

	// Query test coverage (TESTS_FILE edges) — use file_path for unique join
	testRows, err := cbmQuery(bin, project, `MATCH (t)-[:TESTS_FILE]->(f) RETURN t.name, f.file_path`)
	if err != nil {
		log.Printf("cbm: test coverage query: %v", err)
	}
	testCoverage := parseTestCoverage(testRows)

	// Query change coupling (FILE_CHANGES_WITH edges, dedup in Go)
	changeRows, err := cbmQuery(bin, project, `MATCH (a)-[:FILE_CHANGES_WITH]->(b) RETURN a.name, b.name`)
	if err != nil {
		log.Printf("cbm: change coupling query: %v", err)
	}

	// Query key files (most-called functions per package)
	keyRows, err := cbmQuery(bin, project, `MATCH (n)-[:CALLS]->(f) WHERE f.label = 'Function' RETURN f.file_path, f.name, count(n) ORDER BY count(n) DESC`)
	if err != nil {
		log.Printf("cbm: key files query: %v", err)
	}

	for _, row := range files {
		filePath, _ := row[0].(string)
		_, _ = row[1].(string) // name (unused)
		locStr, _ := row[2].(string)
		pkgPath, _ := row[3].(string)

		loc := 1
		if locStr != "" && locStr != "0" {
			fmt.Sscanf(locStr, "%d", &loc)
			if loc == 0 {
				loc = 1
			}
		}

		lang := detectLanguage(filePath)
		fi := FileInfo{
			Path:       filePath,
			Language:   lang,
			LOC:        loc,
			IsTest:     isTestFile(filePath, lang),
			Signatures: fileSigs[filePath],
			Imports:    fileImports[filePath],
			TestCount:  testCoverage[filePath],
		}

		stats.FileCount++
		stats.TotalLOC += loc
		allFiles = append(allFiles, fi)

		dir := pkgPath
		if dir == "" {
			dir = filepath.Dir(filePath)
		}
		pkg, ok := pkgMap[dir]
		if !ok {
			pkg = &PackageInfo{Name: dir}
			pkgMap[dir] = pkg
		}
		pkg.FileCount++
		pkg.TotalLOC += loc
		pkg.Files = append(pkg.Files, fi)
	}

	var packages []PackageInfo
	for _, pkg := range pkgMap {
		packages = append(packages, *pkg)
	}
	sort.Slice(packages, func(i, j int) bool {
		return packages[i].Name < packages[j].Name
	})

	return &ScanResult{
		RootDir:        rootDir,
		Tier:           classifyTier(stats),
		Files:          allFiles,
		Packages:       packages,
		Stats:          stats,
		EntryPoints:    entryPoints,
		ChangeCoupling: parseChangeCoupling(changeRows),
		KeyFiles:       parseKeyFiles(keyRows, 3),
		ActiveZones:    gitActiveZones(rootDir),
	}, nil
}

// cbmQuery executes a Cypher query via codebase-memory-mcp CLI and returns rows.
func cbmQuery(bin, project, cypher string) ([][]interface{}, error) {
	params, _ := json.Marshal(map[string]string{
		"project": project,
		"query":   cypher,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, bin, "cli", "query_graph", string(params), "--raw")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("cbm cli: %w", err)
	}

	// Parse MCP content wrapper: {"content":[{"type":"text","text":"..."}]}
	var mcpResp struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(out, &mcpResp); err != nil {
		return nil, fmt.Errorf("parse mcp response: %w", err)
	}
	if len(mcpResp.Content) == 0 {
		return nil, fmt.Errorf("empty mcp response")
	}

	var queryResp struct {
		Columns []string        `json:"columns"`
		Rows    [][]interface{} `json:"rows"`
		Total   int             `json:"total"`
	}
	if err := json.Unmarshal([]byte(mcpResp.Content[0].Text), &queryResp); err != nil {
		return nil, fmt.Errorf("parse query response: %w", err)
	}

	return queryResp.Rows, nil
}

// cbmProjectName converts a directory path to the codebase-memory-mcp project name.
// Resolves git worktree paths to the main repo path.
func cbmProjectName(rootDir string) string {
	clean := filepath.Clean(rootDir)
	clean = strings.TrimPrefix(clean, "/")
	return strings.ReplaceAll(clean, "/", "-")
}

// cbmIndexRepository indexes a project directory in codebase-memory-mcp.
// Called automatically when search_graph finds no data for a project.
func cbmIndexRepository(bin, repoPath string) error {
	args, _ := json.Marshal(map[string]string{"repo_path": repoPath})
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "cli", "index_repository", string(args), "--raw")
	return withWorktreeGitSymlink(repoPath, func() error {
		_, err := cmd.Output()
		return err
	})
}

// withWorktreeGitSymlink ensures a git worktree's .git file appears as a
// directory (via symlink to the real gitdir) for the duration of fn. Tools
// like CBM that check for .git/ presence to enable .gitignore-aware scanning
// would otherwise fall back to scanning the entire filesystem (including
// gopath/gocache with thousands of vendored files).
// Non-worktrees (.git is already a dir/symlink or missing) run fn unchanged.
// On any setup failure, fn still runs (best effort). The original .git file
// is always restored on return.
func withWorktreeGitSymlink(repoPath string, fn func() error) error {
	dotGit := filepath.Join(repoPath, ".git")
	info, err := os.Lstat(dotGit)
	if err != nil {
		return fn()
	}
	if info.Mode()&os.ModeSymlink != 0 || info.IsDir() {
		return fn()
	}

	data, err := os.ReadFile(dotGit)
	if err != nil {
		return fn()
	}
	line := strings.TrimSpace(string(data))
	const prefix = "gitdir:"
	if !strings.HasPrefix(line, prefix) {
		return fn()
	}
	gitdir := strings.TrimSpace(strings.TrimPrefix(line, prefix))
	if !filepath.IsAbs(gitdir) {
		gitdir = filepath.Join(repoPath, gitdir)
	}
	if gdInfo, err := os.Stat(gitdir); err != nil || !gdInfo.IsDir() {
		return fn()
	}

	backup := dotGit + ".yesmem-bak"
	_ = os.Remove(backup)
	if err := os.Rename(dotGit, backup); err != nil {
		return fn()
	}
	defer func() {
		_ = os.Remove(dotGit)
		_ = os.Rename(backup, dotGit)
	}()

	if err := os.Symlink(gitdir, dotGit); err != nil {
		return fn()
	}

	return fn()
}

func formatSignature(label, name, qn string) string {
	switch label {
	case "Method":
		return "method " + name
	case "Interface":
		return "interface " + name
	case "Class":
		return "type " + name
	default:
		return "func " + name
	}
}

// parseImportRows maps CBM query results (file_path, import_target) to a file→imports map.
func parseImportRows(rows [][]interface{}) map[string][]string {
	result := make(map[string][]string)
	for _, row := range rows {
		if len(row) < 2 {
			continue
		}
		filePath, _ := row[0].(string)
		importTarget, _ := row[1].(string)
		if filePath != "" && importTarget != "" {
			result[filePath] = append(result[filePath], importTarget)
		}
	}
	return result
}

// parseEntryPoints extracts file paths from CBM query rows (func_name, file_path).
func parseEntryPoints(rows [][]interface{}) []string {
	var result []string
	for _, row := range rows {
		if len(row) < 2 {
			continue
		}
		filePath, _ := row[1].(string)
		if filePath != "" {
			result = append(result, filePath)
		}
	}
	return result
}

// parseTestCoverage counts how many test files cover each source file.
func parseTestCoverage(rows [][]interface{}) map[string]int {
	result := make(map[string]int)
	for _, row := range rows {
		if len(row) < 2 {
			continue
		}
		targetFile, _ := row[1].(string)
		if targetFile != "" {
			result[targetFile]++
		}
	}
	return result
}

// parseChangeCoupling deduplicates bidirectional FILE_CHANGES_WITH edges.
func parseChangeCoupling(rows [][]interface{}) []ChangePair {
	seen := make(map[string]bool)
	var result []ChangePair
	for _, row := range rows {
		if len(row) < 2 {
			continue
		}
		a, _ := row[0].(string)
		b, _ := row[1].(string)
		if a == "" || b == "" || a == b {
			continue
		}
		key := a + "|" + b
		if a > b {
			key = b + "|" + a
		}
		if !seen[key] {
			seen[key] = true
			result = append(result, ChangePair{FileA: a, FileB: b})
		}
	}
	return result
}

// parseKeyFiles identifies the most-called files per package directory.
func parseKeyFiles(rows [][]interface{}, topN int) map[string][]string {
	type fileScore struct {
		path  string
		score int
	}
	scores := make(map[string]int)
	for _, row := range rows {
		if len(row) < 3 {
			continue
		}
		filePath, _ := row[0].(string)
		count := parseCount(row[2])
		scores[filePath] += count
	}
	pkgFiles := make(map[string][]fileScore)
	for fp, score := range scores {
		dir := filepath.Dir(fp)
		pkgFiles[dir] = append(pkgFiles[dir], fileScore{path: filepath.Base(fp), score: score})
	}
	result := make(map[string][]string)
	for dir, files := range pkgFiles {
		sort.Slice(files, func(i, j int) bool { return files[i].score > files[j].score })
		limit := topN
		if limit > len(files) {
			limit = len(files)
		}
		for _, f := range files[:limit] {
			result[dir] = append(result[dir], f.path)
		}
	}
	return result
}

// parseCount handles CBM CLI returning counts as either string or float64.
func parseCount(v interface{}) int {
	switch c := v.(type) {
	case string:
		var n int
		fmt.Sscanf(c, "%d", &n)
		return n
	case float64:
		return int(c)
	case json.Number:
		n, _ := c.Int64()
		return int(n)
	default:
		return 0
	}
}

// dedup removes duplicate strings while preserving order.
func dedup(items []string) []string {
	seen := make(map[string]bool, len(items))
	result := make([]string, 0, len(items))
	for _, s := range items {
		if !seen[s] {
			seen[s] = true
			result = append(result, s)
		}
	}
	return result
}

// parseGitActiveZones parses `git log --name-only` output into per-package change counts, sorted descending.
func parseGitActiveZones(gitOutput string) []ActiveZone {
	counts := make(map[string]int)
	for _, line := range strings.Split(gitOutput, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		dir := filepath.Dir(line)
		counts[dir]++
	}
	zones := make([]ActiveZone, 0, len(counts))
	for pkg, count := range counts {
		zones = append(zones, ActiveZone{Package: pkg, ChangeCount: count})
	}
	sort.Slice(zones, func(i, j int) bool { return zones[i].ChangeCount > zones[j].ChangeCount })
	return zones
}

// gitActiveZones runs git log to find recently changed packages.
func gitActiveZones(rootDir string) []ActiveZone {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "log", "--since=7 days ago", "--name-only", "--format=")
	cmd.Dir = rootDir
	out, err := cmd.Output()
	if err != nil {
		log.Printf("git active zones: %v", err)
		return nil
	}
	return parseGitActiveZones(string(out))
}
