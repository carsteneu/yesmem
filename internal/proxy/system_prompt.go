package proxy

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// TemplateContext holds all values used to fill the SYSTEM.md template.
type TemplateContext struct {
	WorkingDir       string
	IsGitRepo        string
	Platform         string
	Shell            string
	OSVersion        string
	ModelDisplayName string
	ModelID          string
	TodayDate        string
	UserEmail        string
	ClaudeMdProject  string
	ClaudeMdUser     string
	HostAgentName    string
}

var (
	osVersionCache   string
	userEmailOnce    sync.Once
	userEmailCache   string
)

func init() {
	if out, err := os.ReadFile("/proc/version"); err == nil {
		parts := strings.Fields(string(out))
		if len(parts) > 3 {
			osVersionCache = strings.Join(parts[:3], " ")
		} else {
			osVersionCache = strings.TrimSpace(string(out))
		}
	}
	if osVersionCache == "" {
		osVersionCache = strings.TrimSpace(runtime.GOOS)
	}
}

func getUserEmail() string {
	userEmailOnce.Do(func() {
		userEmailCache = readUserEmailOnce()
	})
	return userEmailCache
}

func loadSystemPromptFromPath(path string) []byte {
	if path == "" {
		return nil
	}
	expanded := expandHome(path)
	b, err := os.ReadFile(expanded)
	if err != nil {
		return nil
	}
	return b
}

var modelDisplayNames = map[string]string{
	"deepseek-v4-pro":    "DeepSeek V4 Pro",
	"deepseek-v4-flash":  "DeepSeek V4 Flash",
	"deepseek-chat":      "DeepSeek Chat",
	"deepseek-reasoner":  "DeepSeek Reasoner",
}

func fillSystemTemplate(tpl []byte, ctx TemplateContext) []byte {
	s := string(tpl)
	s = strings.ReplaceAll(s, "{{.WorkingDir}}", ctx.WorkingDir)
	s = strings.ReplaceAll(s, "{{.IsGitRepo}}", ctx.IsGitRepo)
	s = strings.ReplaceAll(s, "{{.Platform}}", ctx.Platform)
	s = strings.ReplaceAll(s, "{{.Shell}}", ctx.Shell)
	s = strings.ReplaceAll(s, "{{.OSVersion}}", ctx.OSVersion)
	s = strings.ReplaceAll(s, "{{.ModelDisplayName}}", ctx.ModelDisplayName)
	s = strings.ReplaceAll(s, "{{.ModelID}}", ctx.ModelID)
	s = strings.ReplaceAll(s, "{{.TodayDate}}", ctx.TodayDate)
	s = strings.ReplaceAll(s, "{{.UserEmail}}", ctx.UserEmail)
	s = strings.ReplaceAll(s, "{{.ClaudeMdProject}}", ctx.ClaudeMdProject)
	s = strings.ReplaceAll(s, "{{.ClaudeMdUser}}", ctx.ClaudeMdUser)
	s = strings.ReplaceAll(s, "{{.HostAgentName}}", ctx.HostAgentName)
	return []byte(s)
}

func isGitRepo(dir string) string {
	if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
		return "true"
	}
	return "false"
}

func shellName() string {
	if sh := os.Getenv("SHELL"); sh != "" {
		return filepath.Base(sh)
	}
	return "bash"
}

func modelDisplayName(modelID string) string {
	if name, ok := modelDisplayNames[modelID]; ok {
		return name
	}
	return modelID
}

// extractSkillBlock extracts the <available_skills>...</available_skills> block from
// the first text block in req["system"] (post-translation format).
func extractSkillBlock(req map[string]any) string {
	system, _ := req["system"].([]any)
	for _, block := range system {
		b, ok := block.(map[string]any)
		if !ok {
			continue
		}
		if typ, _ := b["type"].(string); typ != "text" {
			continue
		}
		content, _ := b["text"].(string)
		start := strings.Index(content, "<available_skills>")
		if start < 0 {
			continue
		}
		end := strings.LastIndex(content, "</available_skills>")
		if end < 0 || end <= start {
			continue
		}
		return "\n\n" + content[start : end+len("</available_skills>")]
	}
	return ""
}

func replaceFirstSystemBlock(req map[string]any, content string) {
	system, _ := req["system"].([]any)
	for i, block := range system {
		b, ok := block.(map[string]any)
		if !ok {
			continue
		}
		if typ, _ := b["type"].(string); typ == "text" {
			system[i] = map[string]any{
				"type": "text",
				"text": content,
			}
			return
		}
	}
}

// --- helpers ---

func expandHome(path string) string {
	if path == "" {
		return ""
	}
	if strings.HasPrefix(path, "~/") {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, path[2:])
	}
	if path == "~" {
		home, _ := os.UserHomeDir()
		return home
	}
	return path
}

func readUserEmailOnce() string {
	out, err := exec.Command("git", "config", "--global", "user.email").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func readFileSafe(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func readOSVersion() string {
	return osVersionCache
}

// buildSystemContextOpts configures buildSystemContext.
type buildSystemContextOpts struct {
	WorkingDir       string
	ModelID          string
	ModelDisplayName string
	HostAgentName    string
}

// buildSystemContext assembles a TemplateContext from runtime values.
func buildSystemContext(opts buildSystemContextOpts) TemplateContext {
	if opts.HostAgentName == "" {
		opts.HostAgentName = "OpenCode"
	}
	ctx := TemplateContext{
		WorkingDir:       opts.WorkingDir,
		ModelID:          opts.ModelID,
		ModelDisplayName: opts.ModelDisplayName,
		HostAgentName:    opts.HostAgentName,
		TodayDate:        time.Now().Format("2006-01-02"),
		Platform:         runtime.GOOS,
		Shell:            shellName(),
		OSVersion:        osVersionCache,
		UserEmail:        getUserEmail(),
		IsGitRepo:        isGitRepo(opts.WorkingDir),
	}
	if opts.WorkingDir != "" {
		ctx.ClaudeMdProject = readFileSafe(filepath.Join(opts.WorkingDir, "CLAUDE.md"))
	}
	ctx.ClaudeMdUser = readFileSafe(expandHome("~/.claude/CLAUDE.md"))
	return ctx
}

// resolveSystemTemplate selects the appropriate template for a model.
// Checks modelTemplates for substring match (longest key first), falls back to default.
func (s *Server) resolveSystemTemplate(model string) []byte {
	if s.modelTemplates == nil || len(s.modelTemplates) == 0 {
		return s.customSystemPrompt
	}
	modelLower := strings.ToLower(model)
	var bestKey string
	for key := range s.modelTemplates {
		if strings.Contains(modelLower, strings.ToLower(key)) {
			if len(key) > len(bestKey) {
				bestKey = key
			}
		}
	}
	if bestKey != "" {
		if tpl, ok := s.modelTemplates[bestKey]; ok && tpl != nil {
			return tpl
		}
	}
	return s.customSystemPrompt
}
