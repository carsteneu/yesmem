package daemon

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ScriptMeta holds one callable script within a cap.
type ScriptMeta struct {
	Name    string `json:"name"`
	Kind    string `json:"kind"`           // "tool" | "handler"
	Runtime string `json:"runtime"`        // "repl" | "bash"
	Lang    string `json:"lang,omitempty"` // code fence language tag
	Body    string `json:"body"`
	Schema  string `json:"schema,omitempty"`  // JSON schema (tool kind only)
	Sandbox string `json:"sandbox,omitempty"` // "" | "none" | "standard" | "strict"
	Timeout int64  `json:"timeout,omitempty"` // seconds; 0 = auto (default/llm heuristic)
}

// CapMeta holds structured metadata for a cap learning.
// Serialized as JSON in the Learning's Context field.
type CapMeta struct {
	Name        string            `json:"cap_name"`
	Description string            `json:"cap_description,omitempty"`
	Scripts     []ScriptMeta      `json:"cap_scripts"`
	Schema      string            `json:"cap_schema,omitempty"`       // legacy single-tool schema
	HandlerREPL string            `json:"cap_handler_repl,omitempty"` // legacy REPL handler
	HandlerBash string            `json:"cap_handler_bash,omitempty"` // legacy bash handler
	Tags        []string          `json:"cap_tags,omitempty"`
	Requires    []string          `json:"cap_requires,omitempty"`
	Version     int               `json:"cap_version"`
	Tested      bool              `json:"cap_tested"`
	TestDate    string            `json:"cap_test_date,omitempty"`
	AutoActive  bool              `json:"cap_auto_active,omitempty"`
	Actions     map[string]string `json:"cap_actions,omitempty"`
	Scope       string            `json:"cap_scope,omitempty"` // "user" | "project"
}

// ParseCapMeta parses JSON from a Learning's Context field into CapMeta.
func ParseCapMeta(s string) (CapMeta, error) {
	if s == "" {
		return CapMeta{}, fmt.Errorf("empty cap metadata")
	}
	var meta CapMeta
	if err := json.Unmarshal([]byte(s), &meta); err != nil {
		return CapMeta{}, fmt.Errorf("parse cap metadata: %w", err)
	}
	if meta.Name == "" {
		return CapMeta{}, fmt.Errorf("cap_name is required")
	}
	meta.normalize()
	if len(meta.Scripts) == 0 {
		return CapMeta{}, fmt.Errorf("at least one script required (cap_scripts must be non-empty)")
	}
	for i, sc := range meta.Scripts {
		if sc.Name == "" {
			return CapMeta{}, fmt.Errorf("script[%d]: name is required", i)
		}
		if sc.Kind != "tool" && sc.Kind != "handler" {
			return CapMeta{}, fmt.Errorf("script %q: kind must be tool or handler, got %q", sc.Name, sc.Kind)
		}
		if sc.Runtime != "repl" && sc.Runtime != "bash" {
			return CapMeta{}, fmt.Errorf("script %q: runtime must be repl or bash, got %q", sc.Name, sc.Runtime)
		}
		switch sc.Sandbox {
		case "", "none", "standard", "strict":
		default:
			return CapMeta{}, fmt.Errorf("script %q: sandbox must be none, standard, or strict, got %q", sc.Name, sc.Sandbox)
		}
		if sc.Body == "" {
			return CapMeta{}, fmt.Errorf("script %q: body is required", sc.Name)
		}
	}
	return meta, nil
}

// ToJSON serializes CapMeta to a JSON string for storage in Learning.Context.
func (m CapMeta) ToJSON() (string, error) {
	m.normalize()
	b, err := json.Marshal(m)
	if err != nil {
		return "", fmt.Errorf("marshal cap metadata: %w", err)
	}
	return string(b), nil
}

// HasTag returns true if the cap has the given tag (case-insensitive).
func (m CapMeta) HasTag(tag string) bool {
	lower := strings.ToLower(tag)
	for _, t := range m.Tags {
		if strings.ToLower(t) == lower {
			return true
		}
	}
	return false
}

// FindScript returns the script with the given name, or nil if not found.
func (m CapMeta) FindScript(name string) *ScriptMeta {
	for i := range m.Scripts {
		if m.Scripts[i].Name == name {
			return &m.Scripts[i]
		}
	}
	return nil
}

// ToolScripts returns all scripts with kind="tool".
func (m CapMeta) ToolScripts() []ScriptMeta {
	var out []ScriptMeta
	for _, sc := range m.Scripts {
		if sc.Kind == "tool" {
			out = append(out, sc)
		}
	}
	return out
}

// HandlerScripts returns all scripts with kind="handler".
func (m CapMeta) HandlerScripts() []ScriptMeta {
	var out []ScriptMeta
	for _, sc := range m.Scripts {
		if sc.Kind == "handler" {
			out = append(out, sc)
		}
	}
	return out
}

func (m *CapMeta) normalize() {
	if len(m.Scripts) == 0 {
		if m.HandlerREPL != "" {
			schema := m.Schema
			if schema == "" {
				schema = "{}"
			}
			m.Scripts = append(m.Scripts, ScriptMeta{
				Name:    m.Name,
				Kind:    "tool",
				Runtime: "repl",
				Lang:    "javascript",
				Body:    m.HandlerREPL,
				Schema:  schema,
			})
		}
		if m.HandlerBash != "" {
			name := m.Name
			kind := "tool"
			schema := m.Schema
			if m.HandlerREPL != "" {
				name = m.Name + "_bash"
				kind = "handler"
				schema = ""
			} else if schema == "" {
				schema = "{}"
			}
			m.Scripts = append(m.Scripts, ScriptMeta{
				Name:    name,
				Kind:    kind,
				Runtime: "bash",
				Lang:    "bash",
				Body:    m.HandlerBash,
				Schema:  schema,
			})
		}
	}

	if m.HandlerREPL == "" {
		for _, sc := range m.Scripts {
			if sc.Runtime == "repl" && sc.Kind == "tool" {
				m.HandlerREPL = sc.Body
				if m.Schema == "" {
					m.Schema = sc.Schema
				}
				break
			}
		}
	}
	if m.HandlerBash == "" {
		for _, sc := range m.Scripts {
			if sc.Runtime == "bash" {
				m.HandlerBash = sc.Body
				if m.Schema == "" && sc.Kind == "tool" {
					m.Schema = sc.Schema
				}
				break
			}
		}
	}
	if m.Schema == "" {
		for _, sc := range m.Scripts {
			if sc.Kind == "tool" && sc.Schema != "" {
				m.Schema = sc.Schema
				break
			}
		}
	}
}
