package codescan

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// DirectoryScanner implements Scanner using filename heuristics and regex-based signature extraction.
// No external dependencies — works for any language via pattern matching.
type DirectoryScanner struct{}

func (d *DirectoryScanner) Scan(rootDir string) (*ScanResult, error) {
	result := &ScanResult{
		RootDir: rootDir,
	}

	pkgMap := make(map[string]*PackageInfo)
	var stats ProjectStats

	err := filepath.Walk(rootDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(rootDir, path)
		if shouldSkipDir(rel, info) {
			return filepath.SkipDir
		}
		if info.IsDir() || !isSourceFile(path) {
			return nil
		}
		stats.FileCount++

		fi := d.scanFile(rootDir, path)
		stats.TotalLOC += fi.LOC
		result.Files = append(result.Files, fi)

		// Group into package by directory
		dir := filepath.Dir(rel)
		if dir == "." {
			dir = "."
		}
		pkg, ok := pkgMap[dir]
		if !ok {
			pkg = &PackageInfo{Name: dir}
			pkgMap[dir] = pkg
		}
		pkg.FileCount++
		pkg.TotalLOC += fi.LOC
		pkg.Files = append(pkg.Files, fi)

		return nil
	})
	if err != nil {
		return nil, err
	}

	tier := classifyTier(stats)
	result.Tier = tier
	result.Stats = stats

	// Convert map to sorted slice
	for _, pkg := range pkgMap {
		result.Packages = append(result.Packages, *pkg)
	}
	sort.Slice(result.Packages, func(i, j int) bool {
		return result.Packages[i].Name < result.Packages[j].Name
	})

	// Drop file content for Medium/Large tiers to save memory.
	// Content is only used in renderTiny().
	if tier == TierMedium || tier == TierLarge {
		for i := range result.Files {
			result.Files[i].Content = ""
		}
		for i := range result.Packages {
			for j := range result.Packages[i].Files {
				result.Packages[i].Files[j].Content = ""
			}
		}
	}

	return result, nil
}

func (d *DirectoryScanner) scanFile(rootDir, path string) FileInfo {
	rel, _ := filepath.Rel(rootDir, path)
	lang := detectLanguage(path)

	content, err := os.ReadFile(path)
	if err != nil {
		return FileInfo{Path: rel, Language: lang}
	}

	loc := countNonEmpty(string(content))
	sigs := extractSignatures(string(content), lang)
	isTest := isTestFile(rel, lang)

	return FileInfo{
		Path:       rel,
		Language:   lang,
		LOC:        loc,
		Signatures: sigs,
		Content:    string(content),
		IsTest:     isTest,
	}
}

func countNonEmpty(content string) int {
	count := 0
	scanner := bufio.NewScanner(strings.NewReader(content))
	for scanner.Scan() {
		if strings.TrimSpace(scanner.Text()) != "" {
			count++
		}
	}
	return count
}

// Language detection by extension.
var langMap = map[string]string{
	".go": "go", ".py": "python", ".js": "javascript", ".ts": "typescript",
	".tsx": "typescript", ".jsx": "javascript", ".rs": "rust", ".java": "java",
	".kt": "kotlin", ".rb": "ruby", ".php": "php", ".c": "c", ".cpp": "cpp",
	".h": "c", ".hpp": "cpp", ".cs": "csharp", ".swift": "swift",
	".lua": "lua", ".zig": "zig", ".ex": "elixir", ".exs": "elixir",
	".erl": "erlang", ".hs": "haskell", ".ml": "ocaml", ".scala": "scala",
	".vue": "vue", ".svelte": "svelte", ".dart": "dart", ".r": "r",
	".sh": "shell", ".bash": "shell", ".zsh": "shell", ".m": "objc",
}

func detectLanguage(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	if lang, ok := langMap[ext]; ok {
		return lang
	}
	return "unknown"
}

// Test file detection heuristics.
func isTestFile(relPath, lang string) bool {
	base := filepath.Base(relPath)
	switch lang {
	case "go":
		return strings.HasSuffix(base, "_test.go")
	case "python":
		return strings.HasPrefix(base, "test_") || strings.HasSuffix(base, "_test.py")
	case "javascript", "typescript":
		return strings.Contains(base, ".test.") || strings.Contains(base, ".spec.")
	case "php":
		return strings.HasSuffix(base, "Test.php")
	case "java", "kotlin":
		return strings.HasSuffix(base, "Test.java") || strings.HasSuffix(base, "Test.kt")
	case "rust":
		return strings.Contains(relPath, "tests/")
	}
	return false
}

// --- Signature extraction per language (regex-based) ---

func extractSignatures(content, lang string) []string {
	switch lang {
	case "go":
		return extractGoSignatures(content)
	case "php":
		return extractPHPSignatures(content)
	case "python":
		return extractPythonSignatures(content)
	case "javascript", "typescript":
		return extractJSTSSignatures(content)
	case "rust":
		return extractRustSignatures(content)
	case "java", "kotlin":
		return extractJavaSignatures(content)
	default:
		return extractGenericSignatures(content)
	}
}

// Go: func, type, const, var, interface
var (
	reGoFunc      = regexp.MustCompile(`(?m)^func\s+(\([^)]+\)\s+)?\w+[^{]*`)
	reGoType      = regexp.MustCompile(`(?m)^type\s+\w+\s+(struct|interface)\b`)
	reGoConst     = regexp.MustCompile(`(?m)^const\s+\w+`)
	reGoVar       = regexp.MustCompile(`(?m)^var\s+\w+`)
)

func extractGoSignatures(content string) []string {
	var sigs []string
	for _, re := range []*regexp.Regexp{reGoFunc, reGoType, reGoConst, reGoVar} {
		for _, m := range re.FindAllString(content, -1) {
			sigs = append(sigs, strings.TrimSpace(m))
		}
	}
	return sigs
}

// PHP: class, function, interface, trait
var (
	rePHPClass = regexp.MustCompile(`(?m)^\s*(abstract\s+|final\s+)?class\s+\w+`)
	rePHPFunc  = regexp.MustCompile(`(?m)^\s*(public|private|protected|static)?\s*function\s+\w+`)
	rePHPIface = regexp.MustCompile(`(?m)^\s*interface\s+\w+`)
	rePHPTrait = regexp.MustCompile(`(?m)^\s*trait\s+\w+`)
)

func extractPHPSignatures(content string) []string {
	var sigs []string
	for _, re := range []*regexp.Regexp{rePHPClass, rePHPFunc, rePHPIface, rePHPTrait} {
		for _, m := range re.FindAllString(content, -1) {
			sigs = append(sigs, strings.TrimSpace(m))
		}
	}
	return sigs
}

// Python: class, def
var (
	rePyClass = regexp.MustCompile(`(?m)^class\s+\w+`)
	rePyFunc  = regexp.MustCompile(`(?m)^def\s+\w+`)
)

func extractPythonSignatures(content string) []string {
	var sigs []string
	for _, re := range []*regexp.Regexp{rePyClass, rePyFunc} {
		for _, m := range re.FindAllString(content, -1) {
			sigs = append(sigs, strings.TrimSpace(m))
		}
	}
	return sigs
}

// JS/TS: function, class, export, const/let with arrow
var (
	reJSFunc   = regexp.MustCompile(`(?m)^(export\s+)?(async\s+)?function\s+\w+`)
	reJSClass  = regexp.MustCompile(`(?m)^(export\s+)?class\s+\w+`)
	reJSConst  = regexp.MustCompile(`(?m)^(export\s+)?const\s+\w+\s*=\s*(async\s+)?\(`)
)

func extractJSTSSignatures(content string) []string {
	var sigs []string
	for _, re := range []*regexp.Regexp{reJSFunc, reJSClass, reJSConst} {
		for _, m := range re.FindAllString(content, -1) {
			sigs = append(sigs, strings.TrimSpace(m))
		}
	}
	return sigs
}

// Rust: fn, struct, enum, impl, trait
var (
	reRustFn     = regexp.MustCompile(`(?m)^\s*(pub\s+)?(async\s+)?fn\s+\w+`)
	reRustStruct = regexp.MustCompile(`(?m)^\s*(pub\s+)?struct\s+\w+`)
	reRustEnum   = regexp.MustCompile(`(?m)^\s*(pub\s+)?enum\s+\w+`)
	reRustImpl   = regexp.MustCompile(`(?m)^\s*impl\s+`)
	reRustTrait  = regexp.MustCompile(`(?m)^\s*(pub\s+)?trait\s+\w+`)
)

func extractRustSignatures(content string) []string {
	var sigs []string
	for _, re := range []*regexp.Regexp{reRustFn, reRustStruct, reRustEnum, reRustImpl, reRustTrait} {
		for _, m := range re.FindAllString(content, -1) {
			sigs = append(sigs, strings.TrimSpace(m))
		}
	}
	return sigs
}

// Java/Kotlin: class, interface, method
var (
	reJavaClass  = regexp.MustCompile(`(?m)^\s*(public|private|protected)?\s*(abstract\s+|final\s+)?(class|interface|enum)\s+\w+`)
	reJavaMethod = regexp.MustCompile(`(?m)^\s*(public|private|protected)\s+(static\s+)?\w+\s+\w+\s*\(`)
)

func extractJavaSignatures(content string) []string {
	var sigs []string
	for _, re := range []*regexp.Regexp{reJavaClass, reJavaMethod} {
		for _, m := range re.FindAllString(content, -1) {
			sigs = append(sigs, strings.TrimSpace(m))
		}
	}
	return sigs
}

// Generic fallback: lines that look like definitions
var reGenericDef = regexp.MustCompile(`(?m)^(func|def|class|type|struct|interface|trait|impl|fn|const|var|let|export)\s+`)

func extractGenericSignatures(content string) []string {
	var sigs []string
	for _, m := range reGenericDef.FindAllString(content, -1) {
		sigs = append(sigs, strings.TrimSpace(m))
	}
	return sigs
}
