package codescan

import (
	"bytes"
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
	"unicode"
	"unicode/utf8"
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
type CBMScanner struct {
	bin                string // test override; empty resolves via FindCBMBinary
	ensureIndex        func(rootDir string)
	ensureIndexIfStale func(rootDir, head string)
}

func NewCBMScanner() *CBMScanner {
	return &CBMScanner{
		ensureIndex:        EnsureIndex,
		ensureIndexIfStale: EnsureIndexIfStale,
	}
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
	// Both the CBM project slug and the index lookup derive from this string,
	// so a symlinked root has to collapse onto its real path first (YM-232).
	rootDir = ResolveScanRoot(rootDir)

	if isBlacklistedPath(rootDir) {
		return nil, stateError(ErrUnscannable, "directory %q is blacklisted (too large or system path)", rootDir)
	}

	bin := s.bin
	if bin == "" {
		bin = FindCBMBinary()
	}
	if bin == "" {
		return nil, fmt.Errorf("codebase-memory-mcp binary not found")
	}

	project := cbmProjectName(rootDir)

	// Query files via Folder→File edges
	files, err := cbmQuery(bin, project, `
		MATCH (folder:Folder)-[:CONTAINS_FILE]->(f:File)
		RETURN f.file_path AS path, f.name AS name, f.end_line AS loc, folder.file_path AS pkg
		ORDER BY f.file_path`)
	if err != nil {
		// A query error may be a timeout — never treat it as "not indexed".
		// Indexing happens exclusively in the background (bg_index.go).
		// One exception: a directory that has no CBM database of its own was
		// never indexed (a worktree without its own index), so the failure is
		// attributed to that directory instead of the repository (DT27 / YM-4).
		if CBMIndexPath(rootDir) == "" {
			return nil, stateError(ErrNotIndexed, "project %q not indexed (no CBM database for %q): %v", project, rootDir, err)
		}
		return nil, fmt.Errorf("query files: %w", err)
	}
	if len(files) == 0 {
		if s.ensureIndex != nil {
			s.ensureIndex(rootDir)
		}
		return nil, stateError(ErrNotIndexed, "project %q not indexed (background indexing triggered)", project)
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

	// Query imports (available for JS/TS/Python/Ruby in CBM v0.6.0+, Go pending).
	// Fallback: if IMPORTS edges are empty, derive from CALLS edges.
	importRows, err := cbmQuery(bin, project, `MATCH (a)-[:IMPORTS]->(b) RETURN a.file_path, b.name ORDER BY a.file_path`)
	if err != nil {
		log.Printf("cbm: imports query: %v", err)
	}
	fileImports := parseImportRows(importRows)
	if len(fileImports) == 0 {
		// Fallback: derive per-file imports from CALLS edges across packages.
		callsRows, err := cbmQuery(bin, project, `MATCH (a)-[:CALLS]->(b) RETURN a.file_path, b.file_path ORDER BY a.file_path`)
		if err == nil && len(callsRows) > 0 {
			fileImports = deriveImportsFromCalls(callsRows)
			if len(fileImports) > 0 {
				log.Printf("cbm: derived %d file→imports from CALLS edges", len(fileImports))
			}
		}
	}

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

	if s.ensureIndexIfStale != nil {
		s.ensureIndexIfStale(rootDir, ReadGitHead(rootDir))
	}

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

// projectIndexed reports whether CBM has an index with at least one file
// for rootDir. Query errors also report false — capacity for reindexing
// decisions is handled by the background job.
func projectIndexed(rootDir string) bool {
	bin := FindCBMBinary()
	if bin == "" {
		return false
	}
	rows, err := cbmQuery(bin, cbmProjectName(rootDir), `MATCH (f:File) RETURN f.file_path LIMIT 1`)
	return err == nil && len(rows) > 0
}

// cbmQueryTimeout bounds each CBM CLI query. Valid queries on large graphs can
// take well over the former 15s limit; with a persistent CBM daemon answering
// them, 60s is a safe ceiling (decision: yesloop-cbm-load-fix D2).
const cbmQueryTimeout = 60 * time.Second

// cbmProjectArgOK reports whether s may be handed to the CBM CLI as the value of
// a value-taking flag (--project, --repo-path). The slug is derived from a
// directory path, and in cbm-gc from a cache file name, which anyone with write
// access to the cache directory chooses; a leading "-" would be parsed as a flag
// instead of a value.
func cbmProjectArgOK(s string) bool {
	return s != "" && !strings.HasPrefix(s, "-")
}

// cbmQuery executes a Cypher query via codebase-memory-mcp CLI and returns rows.
func cbmQuery(bin, project, cypher string) ([][]interface{}, error) {
	if !cbmProjectArgOK(project) {
		return nil, fmt.Errorf("cbm cli: unusable project name %q", project)
	}

	ctx, cancel := context.WithTimeout(context.Background(), cbmQueryTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, bin, "cli", "query_graph", "--project", project, "--query", cypher, "--json")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("cbm cli: %w%s", err, stderrSuffix(stderr.String()))
	}

	text, err := parseCBMResponse(stdout.Bytes())
	if err != nil {
		return nil, err
	}
	rows, err := parseQueryTable(text)
	if err != nil {
		return nil, fmt.Errorf("parse query response: %w", err)
	}

	return rows, nil
}

// cbmProjectName converts a directory path to the codebase-memory-mcp project name
// (leading "/" stripped, "/" → "-").
func cbmProjectName(rootDir string) string {
	clean := filepath.Clean(ResolveScanRoot(rootDir))
	clean = strings.TrimPrefix(clean, "/")
	return strings.ReplaceAll(clean, "/", "-")
}

// cbmIndexRepository indexes a project directory in codebase-memory-mcp.
// Called by the background indexer when the index is missing or stale.
func cbmIndexRepository(bin, repoPath string, budget time.Duration) error {
	// The path is handed over as the value of --repo-path, and a leading "-"
	// would be parsed as a flag instead of a path (same predicate as --project,
	// YM-332).
	if !cbmProjectArgOK(repoPath) {
		return fmt.Errorf("cbm index: unusable repo path %q", repoPath)
	}
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "cli", "index_repository", "--repo-path", repoPath, "--json")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("cbm index: %w%s", err, stderrSuffix(stderr.String()))
	}
	_, err := parseCBMResponse(stdout.Bytes())
	return err
}

// maxStderrSuffix bounds what a failed CBM call contributes to an error string.
// The text reaches daemon.log and the scan_cooldown row; an unbounded, multi-line
// capture would bloat both and could forge log lines.
const maxStderrSuffix = 2048

// stderrSuffix renders captured CLI stderr for an error message. codebase-memory-mcp
// reports the reason on stderr only ("project not found", timeouts); without it the
// daemon log shows nothing but "exit status 1" (DT27 / YM-4).
//
// The text is foreign: it reaches daemon.log and the scan_cooldown row, so it is
// flattened to one line, stripped of control characters and capped on rune
// boundaries. A byte-wise cut through a multi-byte rune would leave invalid
// UTF-8 behind (YM-311), and control characters could recolor or rewrite the
// log on the reader's terminal.
func stderrSuffix(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	// strings.Map replaces every invalid UTF-8 byte with U+FFFD on the way
	// through, so the text is valid UTF-8 once it leaves this loop.
	s = strings.Map(dropControlChar, s)
	if s == "" {
		return ""
	}
	if len(s) > maxStderrSuffix {
		// Keep both ends: the reason can be the first or the last line.
		half := maxStderrSuffix / 2
		s = headAtRuneBoundary(s, half) + " ...(truncated)... " + tailAtRuneBoundary(s, half)
	}
	return ": " + s
}

// dropControlChar removes control and format characters from foreign CLI output.
// Control covers Cc (ESC, bell), format covers Cf: a bidi override is invisible
// itself but reverses the reading order of everything after it, so it would
// reorder the log line the text lands in.
func dropControlChar(r rune) rune {
	if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
		return -1
	}
	return r
}

// headAtRuneBoundary returns the longest prefix of s that is at most n bytes and
// ends on a rune boundary.
func headAtRuneBoundary(s string, n int) string {
	if n >= len(s) {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// tailAtRuneBoundary returns the last bytes of s, starting on a rune boundary,
// with at most n bytes.
func tailAtRuneBoundary(s string, n int) string {
	i := len(s) - n
	if i < 0 {
		i = 0
	}
	for i < len(s) && !utf8.RuneStart(s[i]) {
		i++
	}
	return s[i:]
}

// isBlacklistedPath returns true if rootDir should not be indexed by CBM.
// Blocks: empty paths, root filesystem (/), user home directories,
// directories without a .git, and stale git worktrees (no git activity >24h).
func isBlacklistedPath(rootDir string) bool {
	if rootDir == "" {
		return true
	}
	clean := filepath.Clean(rootDir)
	if clean == "/" {
		return true
	}
	home, err := os.UserHomeDir()
	if err == nil && clean == home {
		return true
	}
	if !hasGitDir(clean) {
		return true
	}
	if isWorktree(clean) && !hasRecentGitActivity(clean) {
		return true
	}
	return false
}

// isWorktree returns true if root/.git is a regular file (git worktree),
// as opposed to a directory (main repo).
func isWorktree(root string) bool {
	info, err := os.Stat(filepath.Join(root, ".git"))
	return err == nil && info.Mode().IsRegular()
}

func hasRecentGitActivity(root string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "log", "--since=24 hours ago", "--oneline", "-1")
	cmd.Dir = root
	out, err := cmd.Output()
	return err == nil && len(out) > 0
}

func hasGitDir(root string) bool {
	info, err := os.Stat(filepath.Join(root, ".git"))
	return err == nil && (info.IsDir() || info.Mode()&os.ModeSymlink != 0 || isGitFile(info))
}

func isGitFile(info os.FileInfo) bool {
	return info.Mode().IsRegular() && info.Name() == ".git"
}

func formatSignature(label, name, qn string) string {
	switch label {
	case "Method":
		return "func " + name
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

// deriveImportsFromCalls builds a file→imports map from CALLS edges.
// A file "imports" a target if it calls a function in a different package.
// Target is the package directory (e.g. "internal/proxy"), unique per source file.
func deriveImportsFromCalls(rows [][]interface{}) map[string][]string {
	// Build: sourceFile → set of target packages
	raw := make(map[string]map[string]bool)
	for _, row := range rows {
		if len(row) < 2 {
			continue
		}
		srcFile, _ := row[0].(string)
		tgtFile, _ := row[1].(string)
		if srcFile == "" || tgtFile == "" || srcFile == tgtFile {
			continue
		}
		srcPkg := filepath.Dir(srcFile)
		tgtPkg := filepath.Dir(tgtFile)
		if srcPkg == tgtPkg {
			continue // same package, not an import
		}
		if raw[srcFile] == nil {
			raw[srcFile] = make(map[string]bool)
		}
		raw[srcFile][tgtPkg] = true
	}
	out := make(map[string][]string, len(raw))
	for file, pkgs := range raw {
		for pkg := range pkgs {
			out[file] = append(out[file], pkg)
		}
		sort.Strings(out[file])
	}
	return out
}
