package proxy

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/carsteneu/yesmem/internal/models"
)

// goldenSpec describes one golden-test scenario: a named fixture with
// byte-stable pre/post dumps, required tokens, and forbidden tokens.
type goldenSpec struct {
	Name            string
	PreSHA256       string
	FinalSHA256     string
	RequiredTokens  []string // must appear in the final fixture (subset check)
	ForbiddenTokens []string // must NOT appear in the final fixture
	Synthetic       bool     // if true, fixture is auto-generated via YESMEM_UPDATE_SYNTHETIC_GOLDEN
}

// goldenRegistry maps profile → scenario list.
// Adding a new profile means:
//  1. Add fixtures under testdata/golden/<profile>/<name>_{pre,final}.json.
//  2. Add an entry to this registry.
var goldenRegistry = map[models.PromptProfile][]goldenSpec{
	models.ProfileClaude: {
		{
			Name:           "001_security_monitor",
			PreSHA256:      "5fda22b0b65832c00afd8158f0b1d7982a172dc34edbea39b1a0c0db28f5ef7f",
			FinalSHA256:    "06ba461b25c142b3609ca3fe149e570375ceea4b4010ef20af92368b0c8eb864",
			RequiredTokens: claudeRequiredTokens(),
			ForbiddenTokens: []string{
				// Codex/OpenAI tokens must never leak into Claude path.
				"codex", "Codex CLI", "openai-codex", ".codex/", "state_5.sqlite",
				// opencode tokens must never leak into Claude path.
				"opencode", "OpenCode",
			},
		},
	},
	models.ProfileCodex: {
		{
			Name:            "001_synthetic_codex_openai_parity",
			Synthetic:       true,
			PreSHA256:       "790cbbe9831c539bef968c20e504119ffefaca401a8aa4266a52f20e5ff9d87c",
			FinalSHA256:     "fc32a017abc429febd4f18dccaf65095345890a4d7e939d970d11e462ccc8662",
			RequiredTokens:  openAIRequiredTokens(),
			ForbiddenTokens: openAIForbiddenTokens(),
		},
	},
	models.ProfileOpencode: {
		{
			Name:            "001_synthetic_opencode_openai_parity",
			Synthetic:       true,
			PreSHA256:       "154e772a1bec92cfa8dbfd4e1c4a4a59e20f315c6df332feeaeca965e5e79aeb",
			FinalSHA256:     "9279a303ad01090ed3834a56352ba3d3b740e84d3f5b303a07fa1656af40be8a",
			RequiredTokens:  openAIRequiredTokens(),
			ForbiddenTokens: openAIForbiddenTokens(),
		},
	},
}

func claudeRequiredTokens() []string {
	return []string{
		`cch=00000;`,
		`"cache_control":{"type":"ephemeral"}`,
		`[yesmem-directives]`,
		`[yesmem-enhance]`,
		`[yesmem-tool-prefs]`,
		`[yesmem-output-discipline]`,
		`[yesmem-coding-discipline]`,
		`[yesmem-beweislast]`,
		`[yesmem-scope-discipline]`,
		`[yesmem-delegation-contract]`,
		`The following is the user's CLAUDE.md configuration.`,
	}
}

func openAIRequiredTokens() []string {
	return []string{
		`[yesmem-directives]`,
		`[yesmem-output-discipline]`,
		`[yesmem-coding-discipline]`,
		`[yesmem-beweislast]`,
		`[yesmem-scope-discipline]`,
		`[yesmem-clarify-first]`,
	}
}

func openAIForbiddenTokens() []string {
	return []string{
		`[yesmem-enhance]`,
		`[yesmem-tool-prefs]`,
		`[yesmem-code-tools-first]`,
		`[yesmem-delegation-contract]`,
		`CLAUDE_CODE_REPL`,
		`The following is the user's CLAUDE.md configuration.`,
	}
}

// --- Golden Tests ---

func TestGolden_AllProfiles_RegistryCompleteness(t *testing.T) {
	for _, profile := range []models.PromptProfile{
		models.ProfileClaude,
		models.ProfileCodex,
		models.ProfileOpencode,
	} {
		specs, ok := goldenRegistry[profile]
		if !ok {
			t.Fatalf("golden registry missing profile %q", profile)
		}
		if len(specs) == 0 {
			t.Fatalf("golden registry has no scenarios for profile %q", profile)
		}
	}
}

func TestGolden_AllProfiles_FixtureByteStable(t *testing.T) {
	for profile, specs := range goldenRegistry {
		for _, spec := range specs {
			t.Run(string(profile)+"/"+spec.Name, func(t *testing.T) {
				t.Run("pre", func(t *testing.T) {
					body := readGoldenFixture(t, string(profile), spec.Name+"_pre.json")
					assertSHA256(t, body, spec.PreSHA256)
					assertValidJSONObject(t, body)
				})
				t.Run("final", func(t *testing.T) {
					body := readGoldenFixture(t, string(profile), spec.Name+"_final.json")
					assertSHA256(t, body, spec.FinalSHA256)
					assertValidJSONObject(t, body)
				})
				t.Run("pre_and_final_differ", func(t *testing.T) {
					pre := readGoldenFixture(t, string(profile), spec.Name+"_pre.json")
					final := readGoldenFixture(t, string(profile), spec.Name+"_final.json")
					if string(pre) == string(final) {
						t.Fatal("pre and final fixtures byte-identical — expected pipeline to transform")
					}
				})
			})
		}
	}
}

func TestGolden_AllProfiles_TokenGates(t *testing.T) {
	for profile, specs := range goldenRegistry {
		for _, spec := range specs {
			t.Run(string(profile)+"/"+spec.Name+"/required", func(t *testing.T) {
				if len(spec.RequiredTokens) == 0 {
					t.Skip("no required tokens defined")
				}
				body := readGoldenFixture(t, string(profile), spec.Name+"_final.json")
				lower := strings.ToLower(string(body))
				for _, tok := range spec.RequiredTokens {
					if !strings.Contains(lower, strings.ToLower(tok)) {
						t.Fatalf("final fixture missing required token %q", tok)
					}
				}
			})
			t.Run(string(profile)+"/"+spec.Name+"/forbidden", func(t *testing.T) {
				if len(spec.ForbiddenTokens) == 0 {
					t.Skip("no forbidden tokens defined")
				}
				body := readGoldenFixture(t, string(profile), spec.Name+"_final.json")
				lower := strings.ToLower(string(body))
				for _, tok := range spec.ForbiddenTokens {
					if strings.Contains(lower, strings.ToLower(tok)) {
						t.Fatalf("final fixture contains forbidden token %q", tok)
					}
				}
			})
		}
	}
}

// TestGolden_AllProfiles_WireShape verifies basic wire-format expectations
// that differ per profile. Skipped for profiles without wire-shape checks.
func TestGolden_AllProfiles_WireShape(t *testing.T) {
	for profile, specs := range goldenRegistry {
		for _, spec := range specs {
			t.Run(string(profile)+"/"+spec.Name, func(t *testing.T) {
				body := readGoldenFixture(t, string(profile), spec.Name+"_final.json")
				switch profile {
				case models.ProfileClaude:
					assertClaudeWireShape(t, body)
				case models.ProfileCodex, models.ProfileOpencode:
					assertOpenAIWireShape(t, body)
				default:
					t.Skip("no wire-shape assertions defined for profile")
				}
			})
		}
	}
}

func TestGolden_AllProfiles_SyntheticFixturesReplayPipeline(t *testing.T) {
	for profile, specs := range goldenRegistry {
		for _, spec := range specs {
			t.Run(string(profile)+"/"+spec.Name, func(t *testing.T) {
				if !spec.Synthetic {
					return
				}
				pre := readGoldenFixture(t, string(profile), spec.Name+"_pre.json")
				final := readGoldenFixture(t, string(profile), spec.Name+"_final.json")
				replayed := replaySyntheticOpenAIFixture(t, pre)
				if string(replayed) != string(final) {
					t.Fatal("synthetic final fixture does not match replayed OpenAI parity pipeline output")
				}
			})
		}
	}
}

func TestGolden_AllProfiles_SyntheticFixturesHaveTokenGates(t *testing.T) {
	for profile, specs := range goldenRegistry {
		for _, spec := range specs {
			t.Run(string(profile)+"/"+spec.Name, func(t *testing.T) {
				if !spec.Synthetic {
					return
				}
				if spec.PreSHA256 == "" || spec.FinalSHA256 == "" {
					t.Fatal("synthetic fixture slot must declare byte-stability hashes")
				}
				if len(spec.RequiredTokens) == 0 {
					t.Fatal("synthetic fixture slot must define required token gates")
				}
				if len(spec.ForbiddenTokens) == 0 {
					t.Fatal("synthetic fixture slot must define forbidden token gates")
				}
			})
		}
	}
}

func TestGolden_UpdateSyntheticFixtures(t *testing.T) {
	if os.Getenv("YESMEM_UPDATE_SYNTHETIC_GOLDEN") != "1" {
		t.Skip("set YESMEM_UPDATE_SYNTHETIC_GOLDEN=1 to rewrite synthetic golden fixtures")
	}
	for profile, specs := range goldenRegistry {
		for _, spec := range specs {
			if !spec.Synthetic {
				continue
			}
			pre := syntheticOpenAIPreFixture(t, profile)
			final := replaySyntheticOpenAIFixture(t, pre)
			writeGoldenFixture(t, string(profile), spec.Name+"_pre.json", pre)
			writeGoldenFixture(t, string(profile), spec.Name+"_final.json", final)
		}
	}
}

// --- Helpers ---

func readGoldenFixture(t *testing.T, profile, name string) []byte {
	t.Helper()
	path := filepath.Join("testdata", "golden", profile, name)
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return body
}

func writeGoldenFixture(t *testing.T, profile, name string, body []byte) {
	t.Helper()
	path := filepath.Join("testdata", "golden", profile, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func assertSHA256(t *testing.T, body []byte, want string) {
	t.Helper()
	sum := sha256.Sum256(body)
	got := hex.EncodeToString(sum[:])
	if got != want {
		t.Fatalf("sha256 = %s, want %s", got, want)
	}
}

func assertOpenAIWireShape(t *testing.T, body []byte) {
	t.Helper()
	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := req["model"].(string); !ok {
		t.Fatal("model field missing or not string")
	}
	messages, ok := req["messages"].([]any)
	if !ok {
		t.Fatal("messages is not an array")
	}
	if len(messages) == 0 {
		t.Fatal("messages empty")
	}
	for i, msg := range messages {
		m, ok := msg.(map[string]any)
		if !ok {
			t.Fatalf("message %d is not an object", i)
		}
		if _, ok := m["role"].(string); !ok {
			t.Fatalf("message %d role missing or not string", i)
		}
		if _, ok := m["content"]; !ok {
			t.Fatalf("message %d content missing", i)
		}
	}
}

func syntheticOpenAIPreFixture(t *testing.T, profile models.PromptProfile) []byte {
	t.Helper()
	req := OpenAIChatRequest{
		Model:     "gpt-5.4",
		MaxTokens: 256,
		Messages: []OpenAIMessage{
			{
				Role:    "developer",
				Content: syntheticDeveloperPrompt(profile),
			},
			{
				Role: "user",
				Content: []any{
					map[string]any{
						"type": "text",
						"text": "<environment_context>\n  <cwd>/home/chief/memory/yesmem/.worktrees/opencode-proxy</cwd>\n  <shell>bash</shell>\n</environment_context>",
					},
				},
			},
			{
				Role:    "user",
				Content: "Bitte pruefe den Proxy-Pfad und melde nur belegte Ergebnisse.",
			},
		},
	}
	anthReq, err := translateOpenAIToAnthropic(req)
	if err != nil {
		t.Fatalf("translate synthetic OpenAI request: %v", err)
	}
	return canonicalJSON(t, anthReq)
}

func syntheticDeveloperPrompt(profile models.PromptProfile) string {
	switch profile {
	case models.ProfileOpencode:
		return "You are opencode, an OpenAI-compatible coding agent using the opencode plugin system."
	default:
		return "You are Codex, a coding agent based on GPT-5."
	}
}

func replaySyntheticOpenAIFixture(t *testing.T, pre []byte) []byte {
	t.Helper()
	var req map[string]any
	if err := json.Unmarshal(pre, &req); err != nil {
		t.Fatalf("unmarshal synthetic pre fixture: %v", err)
	}
	s := &Server{
		cfg: Config{
			TokenThreshold:         200000,
			TokenMinimumThreshold:  80000,
			PromptRewrite:          true,
			PromptOutputDiscipline: true,
			PromptCodingDiscipline: true,
			PromptBeweislast:       true,
			PromptScopeDiscipline:  true,
			PromptClarifyFirst:     true,
			SawtoothEnabled:        false,
			SkillEvalInject:        "false",
		},
		logger: log.New(io.Discard, "", 0),
	}
	messages, _ := req["messages"].([]any)
	ctx := s.prepareOpenAIRequestContext(req, 1, "", "", "synthetic-golden")
	ctx.ThreadID = ""
	ctx.SessionID = ""
	ctx.Project = ""
	ctx.UserQuery = lastUserText(messages)
	s.runOpenAIParityPipeline(req, &ctx)
	out, err := translateAnthropicToOpenAI(req)
	if err != nil {
		t.Fatalf("translate synthetic final fixture: %v", err)
	}
	return canonicalJSON(t, out)
}

func canonicalJSON(t *testing.T, v any) []byte {
	t.Helper()
	body, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal canonical JSON: %v", err)
	}
	body = append(body, '\n')
	return body
}

func assertValidJSONObject(t *testing.T, body []byte) {
	t.Helper()
	var v any
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatalf("not valid JSON: %v", err)
	}
	if _, ok := v.(map[string]any); !ok {
		t.Fatalf("not a JSON object")
	}
}

func assertClaudeWireShape(t *testing.T, body []byte) {
	t.Helper()
	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := req["model"].(string); !ok {
		t.Fatal("model field missing or not string")
	}
	system, ok := req["system"].([]any)
	if !ok {
		t.Fatal("system is not an array")
	}
	if len(system) == 0 {
		t.Fatal("system blocks empty")
	}
	messages, ok := req["messages"].([]any)
	if !ok {
		t.Fatal("messages is not an array")
	}
	if len(messages) == 0 {
		t.Fatal("messages empty")
	}
}
