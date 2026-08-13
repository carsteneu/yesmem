package capfile

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

type CapFile struct {
	Name        string
	Description string
	Version     int
	Tags        []string
	Scope       string // "user" | "project"
	Tested      bool
	AutoActive  bool
	Purpose     string
	Scripts     []Script
	DatabaseSQL string
	Requires    []string
	Actions     map[string]string
	SourcePath  string
}

type Script struct {
	Name    string
	Kind    string // "tool" | "handler"
	Runtime string // "repl" | "bash"
	Lang    string // raw code-fence language tag
	Body    string
	Schema  string // JSON schema (tool kind only)
	Sandbox string // "none" | "standard" | "strict" | "" (inherit)
	Timeout int64  // seconds; 0 = auto (default/llm heuristic)
}

type frontmatter struct {
	Name        string   `yaml:"name"`
	Description string   `yaml:"description"`
	Version     int      `yaml:"version"`
	Tags        []string `yaml:"tags"`
	Scope       string   `yaml:"scope"`
	Requires    []string `yaml:"requires"`
	Tested      bool     `yaml:"tested"`
	AutoActive  bool     `yaml:"auto_active"`
}

var (
	namePattern         = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	dangerousSQLPattern = regexp.MustCompile(`(?i)\b(DROP|ALTER|INSERT|UPDATE|DELETE|GRANT|REVOKE|ATTACH|DETACH|PRAGMA|VACUUM)\b`)
	safeSQLPattern      = regexp.MustCompile(`(?i)^\s*CREATE\s+(TABLE|INDEX|VIEW|TRIGGER)\s+(IF\s+NOT\s+EXISTS\s+)?`)
	metadataLinePattern = regexp.MustCompile(`^[a-z][a-z0-9_]*:.*$`)
)

var knownMetadataKey = map[string]bool{
	"kind":    true,
	"runtime": true,
	"schema":  true,
	"sandbox": true,
	"timeout": true,
}

func Parse(data []byte) (*CapFile, error) {
	fm, body, err := splitFrontmatter(data)
	if err != nil {
		return nil, err
	}

	var meta frontmatter
	if err := yaml.Unmarshal([]byte(fm), &meta); err != nil {
		return nil, fmt.Errorf("parse frontmatter: %w", err)
	}

	if meta.Name == "" {
		return nil, fmt.Errorf("frontmatter: name is required")
	}
	if !namePattern.MatchString(meta.Name) {
		return nil, fmt.Errorf("frontmatter: name %q must match [a-z][a-z0-9_]*", meta.Name)
	}
	if meta.Description == "" {
		return nil, fmt.Errorf("frontmatter: description is required")
	}
	if meta.Version == 0 {
		meta.Version = 1
	}
	explicitScope := meta.Scope
	if meta.Scope == "" {
		meta.Scope = "project"
	}

	sections, err := parseSections(body)
	if err != nil {
		return nil, err
	}

	scriptsRaw, ok := sections["Scripts"]
	if !ok {
		return nil, fmt.Errorf("Scripts section missing")
	}
	scripts, err := parseScriptsSection(scriptsRaw)
	if err != nil {
		return nil, err
	}
	if len(scripts) == 0 {
		return nil, fmt.Errorf("Scripts section: at least one ### <name> subsection required")
	}
	if err := validateScripts(scripts); err != nil {
		return nil, err
	}

	var dbSQL string
	if dbContent, ok := sections["Database"]; ok && strings.TrimSpace(dbContent) != "" {
		dbSQL, _, _, err = extractFirstCodeBlock(dbContent)
		if err != nil {
			return nil, fmt.Errorf("Database section: %w", err)
		}
		if dbSQL != "" {
			if err := validateSQL(dbSQL); err != nil {
				return nil, fmt.Errorf("Database section: %w", err)
			}
		}
	}

	requires := meta.Requires
	if len(requires) == 0 {
		requires = detectRequiresFromScripts(scripts)
	}

	if explicitScope == "project" {
		for _, sc := range scripts {
			if sc.Sandbox == "none" {
				// sc.Name included directly — this error returns from Parse(), not via parseScriptsSection's flush wrapper.
				return nil, fmt.Errorf("script %q: sandbox=none not allowed on scope=project caps (use scope=user)", sc.Name)
			}
		}
	}

	return &CapFile{
		Name:        meta.Name,
		Description: meta.Description,
		Version:     meta.Version,
		Tags:        meta.Tags,
		Scope:       meta.Scope,
		Tested:      meta.Tested,
		AutoActive:  meta.AutoActive,
		Purpose:     strings.TrimSpace(sections["Purpose"]),
		Scripts:     scripts,
		DatabaseSQL: dbSQL,
		Requires:    requires,
		Actions:     parseActions(sections["Actions"]),
	}, nil
}

func splitFrontmatter(data []byte) (string, string, error) {
	s := string(data)
	if !strings.HasPrefix(s, "---") {
		return "", "", fmt.Errorf("missing frontmatter (must start with ---)")
	}
	rest := s[3:]
	idx := strings.Index(rest, "\n---")
	if idx < 0 {
		return "", "", fmt.Errorf("missing frontmatter closing ---")
	}
	fm := strings.TrimSpace(rest[:idx])
	body := rest[idx+4:]
	return fm, body, nil
}

func parseSections(body string) (map[string]string, error) {
	sections := map[string]string{}
	lines := strings.Split(body, "\n")
	var currentSection string
	var buf strings.Builder

	for _, line := range lines {
		if strings.HasPrefix(line, "## ") {
			if currentSection != "" {
				sections[currentSection] = buf.String()
			}
			currentSection = strings.TrimSpace(strings.TrimPrefix(line, "## "))
			buf.Reset()
			continue
		}
		if currentSection != "" {
			buf.WriteString(line)
			buf.WriteByte('\n')
		}
	}
	if currentSection != "" {
		sections[currentSection] = buf.String()
	}
	return sections, nil
}

func parseScriptsSection(content string) ([]Script, error) {
	var scripts []Script
	var currentName string
	var buf []string

	flush := func() error {
		if currentName == "" {
			return nil
		}
		sc, err := parseScriptSubsection(currentName, strings.Join(buf, "\n"))
		if err != nil {
			return fmt.Errorf("script %q: %w", currentName, err)
		}
		scripts = append(scripts, sc)
		return nil
	}

	for _, line := range strings.Split(content, "\n") {
		if strings.HasPrefix(line, "### ") {
			if err := flush(); err != nil {
				return nil, err
			}
			currentName = strings.TrimSpace(strings.TrimPrefix(line, "### "))
			buf = nil
			continue
		}
		if currentName != "" {
			buf = append(buf, line)
		}
	}
	if err := flush(); err != nil {
		return nil, err
	}
	return scripts, nil
}

func parseScriptSubsection(name, content string) (Script, error) {
	if !namePattern.MatchString(name) {
		return Script{}, fmt.Errorf("invalid name %q: must match [a-z][a-z0-9_]*", name)
	}

	lines := strings.Split(content, "\n")
	metadata := map[string]string{}
	metaEnd := 0

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			metaEnd = i + 1
			break
		}
		if strings.HasPrefix(trimmed, "```") {
			metaEnd = i
			break
		}
		if !metadataLinePattern.MatchString(trimmed) {
			metaEnd = i
			break
		}
		kv := strings.SplitN(trimmed, ":", 2)
		key := strings.TrimSpace(kv[0])
		val := strings.TrimSpace(kv[1])
		if !knownMetadataKey[key] {
			return Script{}, fmt.Errorf("unknown metadata key %q (allowed: kind, runtime, schema, sandbox, timeout)", key)
		}
		metadata[key] = val
		metaEnd = i + 1
	}

	rest := strings.Join(lines[metaEnd:], "\n")
	body, lang, fenceCount, err := extractFirstCodeBlock(rest)
	if err != nil {
		return Script{}, err
	}
	if fenceCount == 0 {
		return Script{}, fmt.Errorf("missing code fence")
	}
	if fenceCount > 1 {
		return Script{}, fmt.Errorf("multiple code fences (only one body code block allowed per script)")
	}

	sc := Script{
		Name: name,
		Lang: lang,
		Body: body,
	}

	sc.Kind = metadata["kind"]
	if sc.Kind == "" {
		sc.Kind = "tool"
	}
	if sc.Kind != "tool" && sc.Kind != "handler" {
		return Script{}, fmt.Errorf("invalid kind %q (allowed: tool, handler)", sc.Kind)
	}

	explicitRuntime := metadata["runtime"]
	derivedRuntime := deriveRuntime(lang)
	if explicitRuntime != "" {
		if explicitRuntime != "repl" && explicitRuntime != "bash" {
			return Script{}, fmt.Errorf("invalid runtime %q (allowed: repl, bash)", explicitRuntime)
		}
		if derivedRuntime != "" && derivedRuntime != explicitRuntime {
			return Script{}, fmt.Errorf("runtime mismatch: code fence language %q implies runtime %q, but explicit runtime is %q", lang, derivedRuntime, explicitRuntime)
		}
		sc.Runtime = explicitRuntime
	} else {
		if derivedRuntime == "" {
			return Script{}, fmt.Errorf("cannot derive runtime from code fence language %q (set runtime: explicitly)", lang)
		}
		sc.Runtime = derivedRuntime
	}

	sc.Schema = metadata["schema"]
	if sc.Kind == "handler" {
		if sc.Schema != "" {
			return Script{}, fmt.Errorf("handlers cannot have a schema")
		}
	} else { // tool
		if sc.Schema == "" {
			if sc.Runtime == "repl" {
				sc.Schema = DeriveSchema(sc.Body)
			} else { // bash tool with no schema
				return Script{}, fmt.Errorf("kind: tool with runtime: bash requires explicit schema")
			}
		}
	}

	sc.Sandbox = metadata["sandbox"]
	switch sc.Sandbox {
	case "", "none", "standard", "strict":
		// ok
	default:
		return Script{}, fmt.Errorf("invalid sandbox %q (allowed: none, standard, strict)", sc.Sandbox)
	}

	if to := strings.TrimSpace(metadata["timeout"]); to != "" {
		secs, err := strconv.ParseInt(to, 10, 64)
		if err != nil || secs <= 0 {
			return Script{}, fmt.Errorf("invalid timeout %q (must be a positive integer number of seconds)", metadata["timeout"])
		}
		sc.Timeout = secs
	}

	return sc, nil
}

func validateScripts(scripts []Script) error {
	seen := map[string]bool{}
	for _, sc := range scripts {
		if seen[sc.Name] {
			return fmt.Errorf("duplicate script name %q", sc.Name)
		}
		seen[sc.Name] = true
	}
	return nil
}

func extractFirstCodeBlock(content string) (string, string, int, error) {
	lines := strings.Split(content, "\n")
	var body strings.Builder
	var lang string
	inBlock := false
	captured := false
	completedBlocks := 0

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !inBlock && strings.HasPrefix(trimmed, "```") {
			inBlock = true
			if !captured {
				lang = strings.TrimSpace(strings.TrimPrefix(trimmed, "```"))
			}
			continue
		}
		if inBlock && trimmed == "```" {
			inBlock = false
			completedBlocks++
			captured = true
			continue
		}
		if inBlock && !captured {
			body.WriteString(line)
			body.WriteByte('\n')
		}
	}
	if inBlock {
		return "", "", 0, fmt.Errorf("unterminated code fence")
	}
	return strings.TrimRight(body.String(), "\n"), lang, completedBlocks, nil
}

func deriveRuntime(lang string) string {
	switch strings.ToLower(lang) {
	case "javascript", "js":
		return "repl"
	case "bash", "sh", "shell":
		return "bash"
	default:
		return ""
	}
}

func validateSQL(sql string) error {
	stmts := strings.Split(sql, ";")
	for _, stmt := range stmts {
		stmt = strings.TrimSpace(stmt)
		if stmt == "" {
			continue
		}
		if dangerousSQLPattern.MatchString(stmt) {
			return fmt.Errorf("dangerous SQL statement: %s", truncate(stmt, 60))
		}
		if !safeSQLPattern.MatchString(stmt) {
			return fmt.Errorf("only CREATE TABLE/INDEX/VIEW/TRIGGER IF NOT EXISTS allowed: %s", truncate(stmt, 60))
		}
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

var paramPattern = regexp.MustCompile(`(?:async\s+)?(?:function\s+\w+|)\s*\(\s*\{([^}]*)\}`)

var knownAdapters = []string{"file", "store", "web"}

// DetectRequires scans a single script body for adapter primitive calls.
// Kept as a package-level helper for callers that need it on a raw script string.
func DetectRequires(script string) []string {
	var found []string
	for _, name := range knownAdapters {
		if strings.Contains(script, name+"(") {
			found = append(found, name)
		}
	}
	if len(found) == 0 {
		return nil
	}
	return found
}

func detectRequiresFromScripts(scripts []Script) []string {
	seen := map[string]bool{}
	var out []string
	for _, sc := range scripts {
		for _, name := range knownAdapters {
			if !seen[name] && strings.Contains(sc.Body, name+"(") {
				seen[name] = true
				out = append(out, name)
			}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// DeriveSchema extracts a JSON Schema from a JavaScript function signature.
func DeriveSchema(script string) string {
	m := paramPattern.FindStringSubmatch(script)
	if m == nil {
		return "{}"
	}
	params := strings.Split(m[1], ",")
	props := map[string]any{}
	for _, p := range params {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		parts := strings.SplitN(p, "=", 2)
		name := strings.TrimSpace(parts[0])
		prop := map[string]any{}
		if len(parts) == 2 {
			defStr := strings.TrimSpace(parts[1])
			if v, err := strconv.Atoi(defStr); err == nil {
				prop["default"] = v
			} else {
				prop["default"] = defStr
			}
		}
		props[name] = prop
	}
	if len(props) == 0 {
		return "{}"
	}
	schema := map[string]any{
		"type":       "object",
		"properties": props,
	}
	out, _ := json.Marshal(schema)
	return string(out)
}

func parseActions(raw string) map[string]string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	actions := map[string]string{}
	var currentKey string
	var currentLines []string

	for _, line := range strings.Split(raw, "\n") {
		if strings.HasPrefix(line, "### ") {
			if currentKey != "" {
				actions[currentKey] = strings.TrimSpace(strings.Join(currentLines, "\n"))
			}
			currentKey = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(line, "### ")))
			currentLines = nil
			continue
		}
		if currentKey != "" {
			currentLines = append(currentLines, line)
		}
	}
	if currentKey != "" {
		actions[currentKey] = strings.TrimSpace(strings.Join(currentLines, "\n"))
	}
	if len(actions) == 0 {
		return nil
	}
	return actions
}

// FindScript returns the first script with the given name, or nil if not found.
func (cf *CapFile) FindScript(name string) *Script {
	for i := range cf.Scripts {
		if cf.Scripts[i].Name == name {
			return &cf.Scripts[i]
		}
	}
	return nil
}

// ToolScripts returns all scripts with kind="tool".
func (cf *CapFile) ToolScripts() []Script {
	var out []Script
	for _, sc := range cf.Scripts {
		if sc.Kind == "tool" {
			out = append(out, sc)
		}
	}
	return out
}

// HandlerScripts returns all scripts with kind="handler".
func (cf *CapFile) HandlerScripts() []Script {
	var out []Script
	for _, sc := range cf.Scripts {
		if sc.Kind == "handler" {
			out = append(out, sc)
		}
	}
	return out
}
