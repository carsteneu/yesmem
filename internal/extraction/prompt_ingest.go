package extraction

// DocFilterSystemPrompt is the Pass 1 prompt: filter specialist knowledge from documentation.
const DocFilterSystemPrompt = `You are a knowledge filter for technical documentation.

STEP 0 — PROCESS SKILL DETECTION:
Before extracting anything, check whether the document is a PROCESS SKILL:
- Does it contain step-by-step workflows ("Step 1... Step 2... Step 3...")?
- Does it reference tool calls ("Use Skill()", "Call Agent()", "Run command")?
- Does it define sequences with "MUST do X before Y", "proceed", "wait for", "then do"?
- Does it describe HOW to work rather than WHAT to know?

→ If YES: Reply ONLY with the word PROCESS_SKILL — no further extraction.
→ If NO: Continue with Step 1.

STEP 1 — SPECIALIST KNOWLEDGE EXTRACTION:

Your knowledge cutoff is May 2025. You reliably know Go ≤ 1.22, Go 1.23/1.24 partially.
You do NOT know Go 1.25+ and Go 1.26+ features.

For other technologies: Symfony ≤ 6.4 reliable, PHP ≤ 8.3 reliable, Twig ≤ 3.8 reliable.
Anything released AFTER May 2025 is potentially specialist knowledge.

Your task: Read the entire document and extract ONLY specialist knowledge.

SPECIALIST KNOWLEDGE is:
- Features, APIs, patterns introduced AFTER May 2025 (Go 1.25+, Symfony 7.2+, etc.)
- Non-obvious gotchas only known through experience
- Concrete interaction problems between components
- Framework-specific configuration values that need to be looked up
- Breaking changes and deprecations
- Patterns that DEVIATE from common conventions

NOT SPECIALIST KNOWLEDGE (do NOT extract):
- Anything covered in official tutorials/guides (Effective Go, Go Tour, Symfony Getting Started)
- Generic security best practices (HTTPS, bcrypt, env vars, input validation)
- Obvious coding standards (gofmt, naming conventions)
- Trivial patterns any experienced developer knows (error != nil, defer close)
- General design principles (SOLID, DRY, KISS)

Reply with a numbered list of specialist knowledge points.
For each point: 1-3 sentences, concrete technical content, source section in parentheses.
If a section contains NO specialist knowledge, skip it entirely.
Quality over quantity — better 5 good points than 50 trivial ones.`

// DocIngestSystemPrompt is the Pass 2 prompt: structure filtered specialist knowledge into learnings.
const DocIngestSystemPrompt = `You are a knowledge structurer. You receive a filtered list of specialist knowledge points
and group them into coherent, atomic learnings.

RULES:
- Merge related points into one learning (e.g. all Go 1.25 features)
- Each learning is 1-3 sentences, self-contained and understandable on its own
- Version/framework info as keywords: "go:1.25", "symfony:7.2", "twig:3.x"
- Configuration values, commands, paths, types: copy EXACTLY
- Preserve context: where does the knowledge come from (heading_path)

Categories:
1. explicit_teaching — Guides, new APIs, how-tos
2. gotcha — Breaking changes, deprecations, non-obvious problems
3. pattern — Version-specific patterns, new conventions

For EACH learning:
{
  "category": "explicit_teaching|gotcha|pattern",
  "content": "Core knowledge in 1-3 sentences",
  "context": "Which section (heading_path)",
  "entities": ["affected classes, functions, config keys"],
  "actions": ["relevant commands"],
  "keywords": ["search terms incl. version:X tags"],
  "trigger": "When should this knowledge be surfaced?"
}`
func DocFilterSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"specialist_points": map[string]any{"type": "string"},
		},
		"required":             []string{"specialist_points"},
		"additionalProperties": false,
	}
}

// DocIngestSchema returns the JSON schema for structured doc ingest extraction.
func DocIngestSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"domain": map[string]any{"type": "string"},
			"learnings": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"category": map[string]any{"type": "string", "enum": []string{"explicit_teaching", "gotcha", "pattern"}},
						"content":  map[string]any{"type": "string"},
						"context":  map[string]any{"type": "string"},
						"entities": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
						"actions":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
						"keywords": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
						"trigger":  map[string]any{"type": "string"},
					},
					"required":             []string{"category", "content", "keywords"},
					"additionalProperties": false,
				},
			},
		},
		"required":             []string{"domain", "learnings"},
		"additionalProperties": false,
	}
}
