package codescan

// Scanner is the interface for code analysis backends.
type Scanner interface {
	Scan(rootDir string) (*ScanResult, error)
}

// ScanResult holds the complete scan output for a project.
type ScanResult struct {
	RootDir        string
	Tier           Tier
	Files          []FileInfo
	Packages       []PackageInfo
	Stats          ProjectStats
	EntryPoints    []string            // files with func main() or HTTP route handlers
	ChangeCoupling []ChangePair        // files that frequently change together (git-based)
	KeyFiles       map[string][]string // package dir → most-connected files (by CALLS fan-in)
	ActiveZones    []ActiveZone        // packages with recent git activity (last 7 days)
}

// ChangePair represents two files that frequently change together in git history.
type ChangePair struct {
	FileA string
	FileB string
}

// ActiveZone represents a package with recent git activity.
type ActiveZone struct {
	Package     string
	ChangeCount int
}

// FileInfo holds metadata and extracted signatures for a single source file.
type FileInfo struct {
	Path       string   // relative to root
	Language   string   // "go", "php", "python", etc.
	LOC        int      // non-empty lines
	Signatures []string // extracted function/class/type signatures
	Imports    []string // import paths (populated by CBMScanner, empty for regex fallback)
	Content    string   // full content (populated for tiny tier files)
	IsTest     bool     // test file marker
	TestCount  int      // number of test files covering this file (from TESTS_FILE edges)
}

// PackageInfo groups files by their containing directory/package.
type PackageInfo struct {
	Name          string
	FileCount     int
	TotalLOC      int
	Files         []FileInfo
	LearningCount int    // annotated externally: how many learnings reference files in this package
	GotchaCount   int    // annotated externally: gotchas specifically
	Description   string // LLM-generated package description (Phase C, populated from cache)
	AntiPatterns  string // LLM-generated convention hints (Phase C, newline-separated)
}
