package extraction

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"github.com/carsteneu/yesmem/internal/codescan"
	"github.com/carsteneu/yesmem/internal/storage"
)

// PackageDescriptionResponse is the structured LLM output for a package description.
type PackageDescriptionResponse struct {
	Description  string   `json:"description"`
	AntiPatterns []string `json:"anti_patterns"`
}

// packageDescriptionSchema is the JSON schema for CompleteJSON validation.
var packageDescriptionSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"description": map[string]any{
			"type":        "string",
			"description": "2-3 sentence description of what this package does, when it runs, and what it depends on",
		},
		"anti_patterns": map[string]any{
			"type": "array",
			"items": map[string]any{
				"type":        "string",
				"description": "Convention hint starting with →, e.g. '→ New handler = new handler_*.go file'",
			},
			"description": "0-3 convention hints for developers working in this package",
		},
	},
	"required":             []string{"description"},
	"additionalProperties": false,
}

const codeDescribeSystemPrompt = `You are a code documentation expert. Given a Go package's structure (files, function signatures, imports, and known issues), write a concise description.

Rules:
- Description: 2-3 sentences. What does the package do? When does it run? What are its key dependencies?
- Anti-patterns: 0-3 convention hints for developers. Only include if there's a clear pattern. Format: "→ <rule>"
- Be precise and factual. Only describe what you can see from the signatures and imports.
- Write in English.`

// GeneratePackageDescriptions generates LLM descriptions for each substantial package.
// Skips packages with fewer than 2 files or fewer than 3 total signatures.
// Returns a map of package name → description. Errors on individual packages are logged and skipped.
func GeneratePackageDescriptions(client LLMClient, result *codescan.ScanResult, counts map[string]storage.EntityCounts) (map[string]PackageDescriptionResponse, error) {
	descs := make(map[string]PackageDescriptionResponse)

	for _, pkg := range result.Packages {
		// Skip trivial packages
		totalSigs := 0
		for _, f := range pkg.Files {
			totalSigs += len(f.Signatures)
		}
		if pkg.FileCount < 2 || totalSigs < 3 {
			continue
		}

		prompt := buildPackagePrompt(pkg, counts)

		resp, err := client.CompleteJSON(codeDescribeSystemPrompt, prompt, packageDescriptionSchema, WithMaxTokens(1024))
		if err != nil {
			log.Printf("[code-describe] LLM error for %s: %v", pkg.Name, err)
			continue
		}

		var desc PackageDescriptionResponse
		if err := json.Unmarshal([]byte(resp), &desc); err != nil {
			log.Printf("[code-describe] JSON parse error for %s: %v", pkg.Name, err)
			continue
		}

		descs[pkg.Name] = desc
	}

	return descs, nil
}

func buildPackagePrompt(pkg codescan.PackageInfo, counts map[string]storage.EntityCounts) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("Package: %s (%d files, %d LOC)\n\n", pkg.Name, pkg.FileCount, pkg.TotalLOC))

	// Learning context
	if counts != nil {
		if ec, ok := counts[pkg.Name]; ok && ec.Total > 0 {
			b.WriteString(fmt.Sprintf("Known issues: %d learnings, %d gotchas\n\n", ec.Total, ec.Gotchas))
		}
	}

	b.WriteString("Files and signatures:\n")
	for _, f := range pkg.Files {
		if f.IsTest {
			continue
		}
		b.WriteString(fmt.Sprintf("\n  %s", f.Path))
		if len(f.Imports) > 0 {
			b.WriteString(fmt.Sprintf(" (imports: %s)", strings.Join(f.Imports, ", ")))
		}
		b.WriteString("\n")
		for _, sig := range f.Signatures {
			b.WriteString(fmt.Sprintf("    %s\n", sig))
		}
	}

	return b.String()
}
