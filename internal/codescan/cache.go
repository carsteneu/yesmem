package codescan

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// ScanStore provides persistent cache for scan results.
type ScanStore interface {
	LoadScan(project string) (scanJSON, gitHead string, cbmMtime int64, err error)
	PersistScan(project, scanJSON, gitHead string, cbmMtime int64) error
}

// CachedScanner wraps a Scanner and caches results per directory.
// Cache layers: in-memory first, then optional SQLite persistence.
// Invalidated when git HEAD changes (new commits/branch switch).
type CachedScanner struct {
	inner Scanner
	store ScanStore
	mu    sync.Mutex
	cache map[string]*cacheEntry
}

type cacheEntry struct {
	head   string
	result *ScanResult
}

// NewCachedScanner creates a cached wrapper around any Scanner.
func NewCachedScanner(inner Scanner) *CachedScanner {
	return &CachedScanner{
		inner: inner,
		cache: make(map[string]*cacheEntry),
	}
}

// WithStore adds SQLite-backed persistence to survive daemon restarts.
func (cs *CachedScanner) WithStore(store ScanStore) *CachedScanner {
	cs.store = store
	return cs
}

func (cs *CachedScanner) Scan(rootDir string) (*ScanResult, error) {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	head := ReadGitHead(rootDir)

	// No git = no stable cache key, always re-scan
	if head == "" {
		return cs.inner.Scan(rootDir)
	}

	cbmMtime := CBMIndexMtime(rootDir).Unix()

	// Layer 1: in-memory cache (git HEAD only — CBM mtime checked in Layer 2)
	if entry, ok := cs.cache[rootDir]; ok && entry.head == head {
		return entry.result, nil
	}

	// Layer 2: SQLite persistent cache (checks both git HEAD and CBM mtime)
	if cs.store != nil {
		project := projectKey(rootDir)
		scanJSON, storedHead, storedMtime, err := cs.store.LoadScan(project)
		if err == nil && scanJSON != "" && storedHead == head && (cbmMtime <= 0 || storedMtime == cbmMtime) {
			var result ScanResult
			if err := json.Unmarshal([]byte(scanJSON), &result); err == nil {
				cs.cache[rootDir] = &cacheEntry{head: head, result: &result}
				log.Printf("[codescan] loaded from SQLite for %s (git %s)", project, head[:min(7, len(head))])
				return &result, nil
			}
		}
	}

	// Cache miss — full scan
	result, err := cs.inner.Scan(rootDir)
	if err != nil {
		return nil, err
	}

	// Re-read CBM mtime after scan (CBM may have auto-indexed during scan)
	cbmMtime = CBMIndexMtime(rootDir).Unix()

	cs.cache[rootDir] = &cacheEntry{head: head, result: result}

	// Persist to SQLite
	if cs.store != nil {
		project := projectKey(rootDir)
		if data, err := json.Marshal(result); err == nil {
			if err := cs.store.PersistScan(project, string(data), head, cbmMtime); err != nil {
				log.Printf("[codescan] persist failed: %v", err)
			}
		}
	}

	return result, nil
}

// ReadGitHead reads the current git HEAD commit hash without shelling out.
// Checks loose refs first, falls back to packed-refs after git gc.
func ReadGitHead(rootDir string) string {
	gitDir := resolveGitDir(rootDir)
	headPath := filepath.Join(gitDir, "HEAD")
	data, err := os.ReadFile(headPath)
	if err != nil {
		return ""
	}
	content := strings.TrimSpace(string(data))

	if !strings.HasPrefix(content, "ref: ") {
		return content
	}

	ref := strings.TrimPrefix(content, "ref: ")

	refPath := filepath.Join(gitDir, ref)
	if refData, err := os.ReadFile(refPath); err == nil {
		return strings.TrimSpace(string(refData))
	}

	commonDir := resolveCommonDir(gitDir)
	if commonDir != gitDir {
		refPath = filepath.Join(commonDir, ref)
		if refData, err := os.ReadFile(refPath); err == nil {
			return strings.TrimSpace(string(refData))
		}
	}

	packedPath := filepath.Join(commonDir, "packed-refs")
	if packed, err := os.ReadFile(packedPath); err == nil {
		for _, line := range strings.Split(string(packed), "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "#") || line == "" {
				continue
			}
			parts := strings.SplitN(line, " ", 2)
			if len(parts) == 2 && parts[1] == ref {
				return parts[0]
			}
		}
	}

	return content
}

func resolveGitDir(rootDir string) string {
	gitPath := filepath.Join(rootDir, ".git")
	info, err := os.Lstat(gitPath)
	if err != nil {
		return gitPath
	}
	if info.IsDir() {
		return gitPath
	}
	data, err := os.ReadFile(gitPath)
	if err != nil {
		return gitPath
	}
	content := strings.TrimSpace(string(data))
	const prefix = "gitdir:"
	if !strings.HasPrefix(content, prefix) {
		return gitPath
	}
	target := strings.TrimSpace(strings.TrimPrefix(content, prefix))
	if !filepath.IsAbs(target) {
		target = filepath.Join(rootDir, target)
	}
	return filepath.Clean(target)
}

func resolveCommonDir(gitDir string) string {
	commonFile := filepath.Join(gitDir, "commondir")
	data, err := os.ReadFile(commonFile)
	if err != nil {
		return gitDir
	}
	rel := strings.TrimSpace(string(data))
	if filepath.IsAbs(rel) {
		return rel
	}
	return filepath.Clean(filepath.Join(gitDir, rel))
}

func projectKey(rootDir string) string {
	gitDir := resolveGitDir(rootDir)
	gitPath := filepath.Join(rootDir, ".git")
	if gitDir == gitPath {
		return filepath.Base(rootDir)
	}
	parts := strings.Split(gitDir, string(filepath.Separator))
	for i := len(parts) - 1; i >= 1; i-- {
		if parts[i] == ".git" {
			return parts[i-1]
		}
	}
	return filepath.Base(rootDir)
}
