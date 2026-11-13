package codescan

import (
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ScanStore provides persistent cache for scan results.
type ScanStore interface {
	LoadScan(project string) (scanJSON, gitHead string, cbmMtime int64, err error)
	PersistScan(project, scanJSON, gitHead string, cbmMtime int64) error
}

// ScanFailureStore persists failed-scan timestamps so slow-failing scans
// are not re-attempted on every call while the cooldown is active.
// The key is the scanned directory (see scanCooldownKey), not the project name.
type ScanFailureStore interface {
	RecordScanFailure(project, errMsg string) error
	GetScanFailure(project string) (failedAt time.Time, errMsg string, err error)
	ClearScanFailure(project string) error
}

// stateNow is the clock the daily state line is measured against. A seam, so a test can
// move a day without waiting for one.
var stateNow = time.Now

// DefaultScanCooldown suppresses re-scans for 10 minutes after a failed scan.
const DefaultScanCooldown = 10 * time.Minute

// ErrScanCooldown is returned by Scan while a project is in its post-failure cooldown.
var ErrScanCooldown = errors.New("codescan: scan cooldown active (recent scan failure)")

// CachedScanner wraps a Scanner and caches results per directory.
// Cache layers: in-memory first, then optional SQLite persistence.
// Invalidated when git HEAD changes (new commits/branch switch).
type CachedScanner struct {
	inner         Scanner
	store         ScanStore
	mu            sync.Mutex
	cache         map[string]*cacheEntry
	lastWasCached bool
	cooldown      time.Duration
	stateMu       sync.Mutex
	stateLogged   map[string]string // directory -> day of the last state line
}

// noteState reports whether the state of this directory should be logged now, and
// remembers that it was. A state does not become false by waiting, so one line a day
// carries the same information as one per request.
func (cs *CachedScanner) noteState(rootDir string, err error) bool {
	day := stateNow().UTC().Format("2006-01-02")
	cs.stateMu.Lock()
	defer cs.stateMu.Unlock()
	if cs.stateLogged == nil {
		cs.stateLogged = make(map[string]string)
	}
	if cs.stateLogged[rootDir] == day {
		return false
	}
	cs.stateLogged[rootDir] = day
	log.Printf("[codescan] %q: %v (state, not a failure: no cooldown, next request asks again)", rootDir, err)
	return true
}

type cacheEntry struct {
	head     string
	cbmMtime int64
	result   *ScanResult
	graph    *CodeGraph
}

// NewCachedScanner creates a cached wrapper around any Scanner.
func NewCachedScanner(inner Scanner) *CachedScanner {
	return &CachedScanner{
		inner:    inner,
		cache:    make(map[string]*cacheEntry),
		cooldown: DefaultScanCooldown,
	}
}

// WithStore adds SQLite-backed persistence to survive daemon restarts.
func (cs *CachedScanner) WithStore(store ScanStore) *CachedScanner {
	cs.store = store
	return cs
}

// LastWasCached reports whether the most recent Scan call returned a cached result.
func (cs *CachedScanner) LastWasCached() bool {
	return cs.lastWasCached
}

// GetCachedGraph returns the cached CodeGraph for rootDir, or nil if not cached.
// The graph is automatically built and cached during Scan() on cache miss.
func (cs *CachedScanner) GetCachedGraph(rootDir string) *CodeGraph {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	if entry, ok := cs.cache[ResolveScanRoot(rootDir)]; ok {
		return entry.graph
	}
	return nil
}

func (cs *CachedScanner) Scan(rootDir string) (*ScanResult, error) {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	// One project, one key: the scan cache, the failure cooldown and the CBM
	// index are all derived from this string, so a root reached through a
	// symlink must collapse onto the same value as its real path (YM-232).
	rootDir = ResolveScanRoot(rootDir)

	head := ReadGitHead(rootDir)

	// No git = no stable cache key, always re-scan
	if head == "" {
		cs.lastWasCached = false
		return cs.inner.Scan(rootDir)
	}

	cbmMtime := CBMIndexMtime(rootDir).Unix()

	// Layer 1: in-memory cache keyed by git HEAD AND CBM index mtime — a
	// completed background reindex touches the mtime, so the refreshed graph
	// becomes visible without a new commit.
	if entry, ok := cs.cache[rootDir]; ok && entry.head == head && entry.cbmMtime == cbmMtime {
		cs.lastWasCached = true
		return entry.result, nil
	}

	// Layer 2: SQLite persistent cache (checks both git HEAD and CBM mtime)
	// Accepts entries with empty git_head from older code (< v2.0.2).
	// The scan cache (ScanCacheKey) and the failure cooldown (scan directory)
	// use different keys on purpose — see scanCooldownKey.
	var project string
	var cooldownKey string
	if cs.store != nil {
		project = ScanCacheKey(rootDir)
		cooldownKey = scanCooldownKey(rootDir)

		// Post-failure cooldown: skip the full scan while a recent attempt
		// failed — each retry burns a full CBM graph load for nothing.
		if fs, ok := cs.store.(ScanFailureStore); ok {
			if failedAt, _, err := fs.GetScanFailure(cooldownKey); err == nil && !failedAt.IsZero() {
				if remaining := cs.cooldown - time.Since(failedAt); remaining > 0 {
					log.Printf("[codescan] cooldown active for %q (%s remaining)", cooldownKey, remaining.Round(time.Second))
					cs.lastWasCached = false
					return nil, ErrScanCooldown
				}
			}
		}

		scanJSON, storedHead, storedMtime, err := cs.store.LoadScan(project)
		headOK := storedHead == head || storedHead == ""
		if err == nil && scanJSON != "" && headOK && (cbmMtime <= 0 || storedMtime == cbmMtime) {
			var result ScanResult
			if err := json.Unmarshal([]byte(scanJSON), &result); err == nil {
				// A row written before YM-453 can hold another checkout's document
				// under this key: a linked worktree's walk of another branch. It is
				// no index of this checkout, so it counts as a miss and the scan
				// below replaces it. A document without a root keeps the old
				// tolerance — there is nothing to check, and the storage guard
				// accepts it for the same reason (checkScanRoot).
				if result.RootDir != "" && !ScannedRepositoryRoot(project, result.RootDir) {
					log.Printf("[codescan] stored scan for %q describes %q, not this checkout; rescanning", project, result.RootDir)
				} else {
					cs.cache[rootDir] = &cacheEntry{head: head, cbmMtime: cbmMtime, result: &result, graph: BuildCodeGraph(&result)}
					log.Printf("[codescan] loaded from SQLite for %q (git %q)", project, head[:min(7, len(head))])
					cs.lastWasCached = true
					return &result, nil
				}
			}
		}
	}

	// Cache miss — full scan
	cs.lastWasCached = false
	result, err := cs.inner.Scan(rootDir)
	if err != nil {
		// A state is not a failure: no cooldown entry, one line a day. Recording it as a
		// failure made the next ten minutes answer "cooldown active", which reads like a
		// transient problem and hides the real answer.
		if IsState(err) {
			cs.noteState(rootDir, err)
			return nil, err
		}
		if cs.store != nil {
			if fs, ok := cs.store.(ScanFailureStore); ok {
				if recErr := fs.RecordScanFailure(cooldownKey, err.Error()); recErr != nil {
					log.Printf("[codescan] record failure failed: %v", recErr)
				}
			}
		}
		return nil, err
	}

	// Re-read CBM mtime after scan (CBM may have auto-indexed during scan)
	cbmMtime = CBMIndexMtime(rootDir).Unix()

	cs.cache[rootDir] = &cacheEntry{head: head, cbmMtime: cbmMtime, result: result, graph: BuildCodeGraph(result)}

	// Persist to SQLite
	if cs.store != nil {
		if data, err := json.Marshal(result); err == nil {
			if err := cs.store.PersistScan(project, string(data), head, cbmMtime); err != nil {
				log.Printf("[codescan] persist failed: %v", err)
			}
			if fs, ok := cs.store.(ScanFailureStore); ok {
				if err := fs.ClearScanFailure(cooldownKey); err != nil {
					log.Printf("[codescan] clear scan failure failed: %v", err)
				}
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

// ProjectKey returns the NAME the code index of a directory is presented
// under: the enclosing repository's name. It is a display value only and
// ambiguous by construction — two repositories with the same basename share it,
// and the worktrees of one repository keep that repository's name.
// Anything that stores or looks up an index must use ScanCacheKey instead.
func ProjectKey(rootDir string) string {
	return filepath.Base(repoRootPath(CheckoutRoot(ResolveScanRoot(rootDir))))
}

// ScanCacheKey returns the key a directory's code index is stored under: the
// checkout the directory belongs to. The repository name is not usable here
// because two repositories with the same basename would share one cache row,
// and with a matching git head one is served the other's file list (YM-277).
//
// A linked worktree is a checkout of its own and keeps its own root (YM-453).
// It is another tree than the main checkout — another branch, another file
// list — and the CBM index below the cache is already built per scanned
// directory (cbmProjectName). Folding the worktree onto the main root filed the
// worktree's document as the main checkout's index, so whichever checkout
// scanned last overwrote the other's paths and git head. A submodule is a
// repository of its own for the same reason (YM-311). Below the checkout the
// key is the checkout itself: a subdirectory names no index of its own, and a
// scan of it would list part of the checkout (YM-442).
func ScanCacheKey(rootDir string) string {
	return CheckoutRoot(ResolveScanRoot(rootDir))
}

// ScannedRepositoryRoot reports whether a scan of scanRoot may be the index that
// projectKey stores. One rule, asked on both sides — here for the CLI's save and,
// through storage.checkScanRoot, for the daemon's: the document belongs to
// exactly one checkout, the one its key names.
//
// A scan of a subdirectory answers for part of the repository and would be filed
// as the checkout's complete file list (YM-277, YM-442). A linked worktree or a
// submodule is a checkout of its own and therefore its own index — under the main
// checkout's key it stays refused (YM-453).
func ScannedRepositoryRoot(projectKey, scanRoot string) bool {
	if projectKey == "" || scanRoot == "" {
		return false
	}
	root := ResolveScanRoot(scanRoot)
	if root != CheckoutRoot(root) {
		return false
	}
	return root == ResolveScanRoot(projectKey)
}

// CheckoutRoot walks up from dir to the directory that holds .git: the checkout the
// directory belongs to, a plain clone as well as a linked worktree. dir is returned
// unchanged (cleaned) when no checkout encloses it.
//
// It is the single answer to that ascent. ScanCacheKey asks it and returns it
// unchanged (one checkout, one index key), while ProjectKey names the repository
// the checkout belongs to (the worktrees of one repository share that name) and
// the code-nav hook anchors its index lookup at the checkout the command runs in
// (internal/hooks/codenav.go). Two implementations of this walk drifted apart
// before (YM-436).
func CheckoutRoot(dir string) string {
	clean := filepath.Clean(dir)
	cur := clean
	for {
		if _, err := os.Lstat(filepath.Join(cur, ".git")); err == nil {
			return cur
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return clean
		}
		cur = parent
	}
}

// repoRootPath returns the path of the repository that owns rootDir. It serves
// the display name only (ProjectKey); the index key is the checkout itself
// (ScanCacheKey). A linked worktree keeps its git data in
// <main>/.git/worktrees/<name>, so its repository is the main repository: the
// worktree is presented under the repository's name, not under its own.
// resolveGitDir already parses the worktree's gitdir link.
//
// A submodule is the opposite case and must not be folded into its
// superproject: it is a repository of its own, and its index key is its own
// checkout for the same reason (YM-311). Both scopes can appear in one gitdir,
// in this order: .git/worktrees/<name> for a linked worktree, .git/modules/...
// for a submodule.
func repoRootPath(rootDir string) string {
	gitDir := resolveGitDir(rootDir)
	gitPath := filepath.Join(rootDir, ".git")
	if gitDir == gitPath {
		return rootDir
	}
	parts := strings.Split(gitDir, string(filepath.Separator))
	for i := len(parts) - 1; i >= 1; i-- {
		if parts[i] != ".git" {
			continue
		}
		super := strings.Join(parts[:i], string(filepath.Separator))
		// Skip the worktree scope when searching for a submodule scope: its name
		// is a name, not a scope, and a worktree may well be called "modules".
		j := i + 1
		if j < len(parts) && parts[j] == "worktrees" {
			j += 2
		}
		for ; j < len(parts); j++ {
			if parts[j] == "modules" {
				return submoduleRepoPath(super, parts[j+1:], gitDir, rootDir)
			}
		}
		return super
	}
	return rootDir
}

// submoduleRepoPath maps the part of a submodule gitdir below .git/modules back
// to the submodule's own root: <super>/.git/modules/libs/inner holds the git data
// of <super>/libs/inner, and a nested submodule repeats the "modules" segment.
//
// The reconstruction is a guess, because git reuses the segment name for real
// path components (a submodule may itself be called "modules"), so the candidate
// has to resolve back to the same gitdir to count. A worktree of a submodule
// fails that check and keeps its own path as its key: separate rather than
// shared, which is what the key has to guarantee.
func submoduleRepoPath(super string, rest []string, gitDir, rootDir string) string {
	segments := []string{super}
	for _, part := range rest {
		if part == "modules" {
			continue
		}
		segments = append(segments, part)
	}
	candidate := filepath.Join(segments...)
	if resolveGitDir(candidate) != gitDir {
		return rootDir
	}
	return candidate
}

// scanCooldownKey identifies one scanned directory in the failure store.
// ScanCacheKey is not usable here: it names the checkout, while a scan that
// failed may have been taken in a subdirectory of it or in a directory without
// git, and the cooldown has to suppress exactly the retry that failed
// (DT27 / YM-4). The directory path also names the culprit in the log line.
func scanCooldownKey(rootDir string) string {
	return filepath.Clean(ResolveScanRoot(rootDir))
}
