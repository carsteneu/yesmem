package codescan

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

// maxCodeMapBytes is the hard ceiling for rendered code map output (~10K tokens ≈ 40KB).
const maxCodeMapBytes = 40000

// maxPriorityPackages is how many packages get full signatures in Large tier.
const maxPriorityPackages = 20

// RenderCodeMap renders the scan result as Markdown, adapted to the project tier.
// priority maps package names to relevance scores (higher = more important).
// Pass nil for no prioritization.
func RenderCodeMap(result *ScanResult, priority map[string]int) string {
	if len(result.Packages) == 0 {
		return ""
	}
	importedBy := buildImportedByMap(result)
	switch result.Tier {
	case TierTiny:
		return renderTiny(result)
	case TierSmall:
		return renderSmall(result, importedBy)
	case TierMedium:
		return renderMedium(result, importedBy)
	case TierLarge:
		return renderLarge(result, priority, importedBy)
	default:
		return renderMedium(result, importedBy)
	}
}

// renderTiny: full file content for each source file.
func renderTiny(result *ScanResult) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("## Code Map\nProject structure with %d files. Use this for navigation and to understand which packages exist.\n\n", result.Stats.FileCount))

	for _, f := range result.Files {
		if f.IsTest {
			continue
		}
		b.WriteString(fmt.Sprintf("**`%s`** (%s, %d LOC)\n", f.Path, f.Language, f.LOC))
		b.WriteString("```" + f.Language + "\n")
		b.WriteString(f.Content)
		if !strings.HasSuffix(f.Content, "\n") {
			b.WriteString("\n")
		}
		b.WriteString("```\n\n")

		if b.Len() > maxCodeMapBytes {
			b.WriteString("... (truncated to fit token budget)\n")
			return b.String()
		}
	}

	testCount := countTests(result.Files)
	if testCount > 0 {
		b.WriteString(fmt.Sprintf("*+ %d test files*\n", testCount))
	}
	return b.String()
}

// renderSmall: all signatures for each file, grouped by directory.
func renderSmall(result *ScanResult, importedBy map[string][]string) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("## Code Map\nProject structure with %d files. Use this for navigation and to understand which packages exist.\n\n", result.Stats.FileCount))

	for _, pkg := range result.Packages {
		annotation := renderAnnotation(pkg)
		b.WriteString(fmt.Sprintf("**`%s/`** (%d files, %d LOC%s)\n", pkg.Name, pkg.FileCount, pkg.TotalLOC, annotation))
		b.WriteString(renderDescription(pkg))
		b.WriteString(renderImportedBy(pkg.Name, importedBy))

		for _, f := range pkg.Files {
			if f.IsTest {
				continue
			}
			sigs := f.Signatures
			if len(sigs) == 0 {
				continue
			}
			b.WriteString(fmt.Sprintf("- `%s`: ", baseName(f.Path)))
			b.WriteString(strings.Join(sigs, "; "))
			b.WriteString("\n")
		}
		b.WriteString("\n")

		if b.Len() > maxCodeMapBytes {
			b.WriteString("... (truncated to fit token budget)\n")
			return b.String()
		}
	}

	testCount := countTests(result.Files)
	if testCount > 0 {
		b.WriteString(fmt.Sprintf("*+ %d test files*\n", testCount))
	}
	return b.String()
}

// renderMedium: packages with top signatures.
func renderMedium(result *ScanResult, importedBy map[string][]string) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("## Code Map\nProject structure with %d files. Use this for navigation and to understand which packages exist.\n\n", result.Stats.FileCount))

	for _, pkg := range result.Packages {
		annotation := renderAnnotation(pkg)
		b.WriteString(fmt.Sprintf("**`%s/`** (%d files, %d LOC%s)\n", pkg.Name, pkg.FileCount, pkg.TotalLOC, annotation))
		b.WriteString(renderDescription(pkg))
		b.WriteString(renderImportedBy(pkg.Name, importedBy))
		var allSigs []string
		for _, f := range pkg.Files {
			if f.IsTest {
				continue
			}
			allSigs = append(allSigs, f.Signatures...)
		}
		// Show up to 10 signatures per package
		limit := 10
		if len(allSigs) < limit {
			limit = len(allSigs)
		}
		if limit > 0 {
			b.WriteString("  ")
			b.WriteString(strings.Join(allSigs[:limit], "; "))
			if len(allSigs) > limit {
				b.WriteString(fmt.Sprintf(" (+%d more)", len(allSigs)-limit))
			}
			b.WriteString("\n")
		}
		renderImportLine(&b, pkg)
		b.WriteString("\n")

		if b.Len() > maxCodeMapBytes {
			b.WriteString("... (truncated to fit token budget)\n")
			return b.String()
		}
	}

	testCount := countTests(result.Files)
	if testCount > 0 {
		b.WriteString(fmt.Sprintf("*+ %d test files*\n", testCount))
	}

	// Enrichment sections from CBM graph data
	health := renderTestCoverageStats(result.Packages)
	entry := renderEntryPoints(result.EntryPoints)
	if health != "" || entry != "" {
		b.WriteString("### Code Health\n")
		b.WriteString(health)
		b.WriteString(entry)
	}

	return b.String()
}

// renderLarge: Spec Ebene 1 table format — sorted by file count, descriptions, collapsed tail.
func renderLarge(result *ScanResult, priority map[string]int, importedBy map[string][]string) string {
	// Sort packages by file count descending
	pkgs := make([]PackageInfo, len(result.Packages))
	copy(pkgs, result.Packages)
	sort.Slice(pkgs, func(i, j int) bool {
		if pkgs[i].FileCount != pkgs[j].FileCount {
			return pkgs[i].FileCount > pkgs[j].FileCount
		}
		return pkgs[i].Name < pkgs[j].Name
	})

	// Project name from root dir
	projectName := "project"
	if result.RootDir != "" {
		parts := strings.Split(strings.TrimRight(result.RootDir, "/"), "/")
		projectName = parts[len(parts)-1]
	}

	var b strings.Builder

	// Project header
	b.WriteString(fmt.Sprintf("## Code Map\n**%s** (%d files, %d packages)\n\n",
		projectName, result.Stats.FileCount, len(result.Packages)))

	// Table header
	b.WriteString("| Package | Files | Description |\n")
	b.WriteString("|---------|-------|-------------|\n")

	// Top packages get individual rows (threshold: >5 files or top 10)
	const collapseThreshold = 5
	const maxTopRows = 10
	topCount := 0
	for i, pkg := range pkgs {
		if i >= maxTopRows || (i > 0 && pkg.FileCount <= collapseThreshold) {
			break
		}
		topCount++
	}
	if topCount < 1 && len(pkgs) > 0 {
		topCount = 1
	}

	for _, pkg := range pkgs[:topCount] {
		desc := pkg.Description
		if desc == "" {
			desc = "—"
		}
		if len(desc) > 120 {
			desc = desc[:117] + "..."
		}
		annotation := ""
		if pkg.GotchaCount > 0 {
			annotation = fmt.Sprintf(" *(%d gotchas)*", pkg.GotchaCount)
		}
		b.WriteString(fmt.Sprintf("| %s | %d | %s%s |\n", pkg.Name, pkg.FileCount, desc, annotation))

		if b.Len() > maxCodeMapBytes {
			b.WriteString("\n... (truncated to fit token budget)\n")
			return b.String()
		}
	}

	// Remaining packages: individual rows with descriptions (not collapsed)
	if len(pkgs) > topCount {
		remaining := pkgs[topCount:]
		for _, pkg := range remaining {
			desc := pkg.Description
			if desc == "" {
				desc = "—"
			}
			if len(desc) > 80 {
				desc = desc[:77] + "..."
			}
			annotation := ""
			if pkg.GotchaCount > 0 {
				annotation = fmt.Sprintf(" *(%d gotchas)*", pkg.GotchaCount)
			}
			b.WriteString(fmt.Sprintf("| %s | %d | %s%s |\n", pkg.Name, pkg.FileCount, desc, annotation))

			if b.Len() > maxCodeMapBytes {
				b.WriteString("\n... (truncated to fit token budget)\n")
				return b.String()
			}
		}
	}

	testCount := countTests(result.Files)
	if testCount > 0 {
		b.WriteString(fmt.Sprintf("\n*+ %d test files*\n", testCount))
	}

	// Enrichment sections from CBM graph data
	health := renderTestCoverageStats(result.Packages)
	entry := renderEntryPoints(result.EntryPoints)
	keys := renderKeyFiles(result.KeyFiles)
	coupling := renderChangeCoupling(result.ChangeCoupling, 10)
	active := renderActiveZones(result.ActiveZones, 10)
	if health != "" || entry != "" || keys != "" || coupling != "" || active != "" {
		b.WriteString("### Code Health\n")
		b.WriteString(health)
		b.WriteString(entry)
		b.WriteString(keys)
		b.WriteString(coupling)
		b.WriteString(active)
	}

	return b.String()
}

func countTests(files []FileInfo) int {
	count := 0
	for _, f := range files {
		if f.IsTest {
			count++
		}
	}
	return count
}

func baseName(path string) string {
	if i := strings.LastIndex(path, "/"); i >= 0 {
		return path[i+1:]
	}
	return path
}

// renderAnnotation returns a suffix like ", 12 learnings, 3 gotchas" for package headers.
// Returns empty string if no annotations.
func renderAnnotation(pkg PackageInfo) string {
	if pkg.LearningCount == 0 && pkg.GotchaCount == 0 {
		return ""
	}
	var parts []string
	if pkg.LearningCount > 0 {
		parts = append(parts, fmt.Sprintf("%d learnings", pkg.LearningCount))
	}
	if pkg.GotchaCount > 0 {
		parts = append(parts, fmt.Sprintf("%d gotchas", pkg.GotchaCount))
	}
	return ", " + strings.Join(parts, ", ")
}

// renderDescription renders the LLM-generated description and anti-pattern hints
// below a package header. Returns empty string if no description is available.
func renderDescription(pkg PackageInfo) string {
	if pkg.Description == "" {
		return ""
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf("  %s\n", pkg.Description))
	if pkg.AntiPatterns != "" {
		for _, line := range strings.Split(pkg.AntiPatterns, "\n") {
			line = strings.TrimSpace(line)
			if line != "" {
				b.WriteString(fmt.Sprintf("  %s\n", line))
			}
		}
	}
	return b.String()
}

// buildImportedByMap inverts the import graph: for each package, which other packages import it.
// Only includes internal imports (filters out stdlib and external modules).
// Returns map[packageBaseName][]importerPackageName, sorted and deduplicated.
func buildImportedByMap(result *ScanResult) map[string][]string {
	// Build set of known package names
	known := make(map[string]bool)
	for _, pkg := range result.Packages {
		known[pkg.Name] = true
	}

	// Invert: for each file's import, record which package imports it
	type edge struct{ target, source string }
	seen := make(map[edge]bool)
	refs := make(map[string]map[string]bool)

	for _, pkg := range result.Packages {
		for _, f := range pkg.Files {
			if f.IsTest {
				continue
			}
			for _, imp := range f.Imports {
				// Extract base name from import path (e.g. "internal/storage" → "storage")
				base := imp
				if idx := strings.LastIndex(imp, "/"); idx >= 0 {
					base = imp[idx+1:]
				}
				// Only include imports that match known packages
				if !known[base] || base == pkg.Name {
					continue
				}
				e := edge{base, pkg.Name}
				if seen[e] {
					continue
				}
				seen[e] = true
				if refs[base] == nil {
					refs[base] = make(map[string]bool)
				}
				refs[base][pkg.Name] = true
			}
		}
	}

	// Convert to sorted slices
	result2 := make(map[string][]string)
	for target, sources := range refs {
		var names []string
		for s := range sources {
			names = append(names, s)
		}
		sort.Strings(names)
		result2[target] = names
	}
	return result2
}

// renderImportedBy renders the "← used by:" line for a package.
// Shows up to 3 importers. Returns empty string if no cross-package imports.
func renderImportedBy(pkgName string, importedBy map[string][]string) string {
	refs, ok := importedBy[pkgName]
	if !ok || len(refs) == 0 {
		return ""
	}
	display := refs
	suffix := ""
	if len(refs) > 3 {
		display = refs[:3]
		suffix = fmt.Sprintf(" +%d more", len(refs)-3)
	}
	return fmt.Sprintf("  ← used by: %s%s\n", strings.Join(display, ", "), suffix)
}

// renderImportLine appends a compact imports line for a package.
func renderImportLine(b *strings.Builder, pkg PackageInfo) {
	seen := make(map[string]bool)
	var allImports []string
	for _, f := range pkg.Files {
		if f.IsTest {
			continue
		}
		for _, imp := range f.Imports {
			if !seen[imp] {
				seen[imp] = true
				allImports = append(allImports, imp)
			}
		}
	}
	if len(allImports) == 0 {
		return
	}
	limit := 5
	if len(allImports) < limit {
		limit = len(allImports)
	}
	b.WriteString("  imports: " + strings.Join(allImports[:limit], ", "))
	if len(allImports) > limit {
		b.WriteString(fmt.Sprintf(" (+%d more)", len(allImports)-limit))
	}
	b.WriteString("\n")
}

func renderEntryPoints(entryPoints []string) string {
	if len(entryPoints) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("### Entry Points\n")
	for _, ep := range entryPoints {
		b.WriteString("- `" + ep + "`\n")
	}
	return b.String()
}

func renderTestCoverageStats(packages []PackageInfo) string {
	var totalTested, totalSource int
	for _, pkg := range packages {
		for _, f := range pkg.Files {
			if f.IsTest {
				continue
			}
			totalSource++
			if f.TestCount > 0 {
				totalTested++
			}
		}
	}
	if totalSource == 0 {
		return ""
	}
	return fmt.Sprintf("- %d/%d source files have test coverage\n", totalTested, totalSource)
}

func renderKeyFiles(keyFiles map[string][]string) string {
	if len(keyFiles) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("### Key Files\n")
	var pkgs []string
	for pkg := range keyFiles {
		pkgs = append(pkgs, pkg)
	}
	sort.Strings(pkgs)
	for _, pkg := range pkgs {
		files := keyFiles[pkg]
		b.WriteString(fmt.Sprintf("- **%s**: %s\n", filepath.Base(pkg), strings.Join(files, ", ")))
	}
	return b.String()
}

func renderChangeCoupling(pairs []ChangePair, limit int) string {
	if len(pairs) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("### Change Coupling\n")
	show := limit
	if show > len(pairs) {
		show = len(pairs)
	}
	for _, p := range pairs[:show] {
		b.WriteString(fmt.Sprintf("- `%s` <-> `%s`\n", filepath.Base(p.FileA), filepath.Base(p.FileB)))
	}
	if len(pairs) > show {
		b.WriteString(fmt.Sprintf("*+ %d more pairs*\n", len(pairs)-show))
	}
	return b.String()
}

func renderActiveZones(zones []ActiveZone, limit int) string {
	if len(zones) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("### Active Zones (7d)\n")
	show := limit
	if show > len(zones) {
		show = len(zones)
	}
	for _, z := range zones[:show] {
		pkg := z.Package
		if pkg == "." {
			pkg = "(root)"
		}
		b.WriteString(fmt.Sprintf("- `%s` (%d changes)\n", pkg, z.ChangeCount))
	}
	if len(zones) > show {
		b.WriteString(fmt.Sprintf("*+ %d more*\n", len(zones)-show))
	}
	return b.String()
}
