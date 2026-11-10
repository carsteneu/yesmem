package guardmodel

import (
	"path/filepath"
	"strings"
	"testing"
)

// fixture writes the files the resolver reads and returns the two directories it needs.
func fixture(t *testing.T, configYAML, registry, auth string) (home, dataDir string) {
	t.Helper()
	home = tempHome(t)
	dataDir = filepath.Join(home, ".claude", "yesmem")
	writeFileT(t, filepath.Join(dataDir, "config.yaml"), configYAML)
	writeFileT(t, filepath.Join(home, ".cache", "opencode", "models.json"), registry)
	writeFileT(t, filepath.Join(home, ".local", "share", "opencode", "auth.json"), auth)
	return home, dataDir
}

const deepseekRegistry = `{"deepseek":{"api":"https://api.deepseek.com","npm":"@ai-sdk/openai-compatible","models":{"deepseek-v4-flash":{}}}}`
const deepseekAuth = `{"deepseek":{"key":"sk-deepseek"}}`

func TestResolveOpenCodeProvider(t *testing.T) {
	home, dataDir := fixture(t, "extraction:\n  model: deepseek-v4-flash\n", deepseekRegistry, deepseekAuth)
	server, calls := modelList(t, haiku("claude-haiku-5-5", "2026-10-07T18:00:00Z"))
	t.Setenv("YESMEM_GUARD_MODELS_URL", server.URL)

	res, err := Resolve(home, dataDir)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if res.APIType != APITypeOpenAI {
		t.Fatalf("APIType = %s, want the opencode path", res.APIType)
	}
	if res.Model != "deepseek-v4-flash" {
		t.Fatalf("Model = %s, want the extraction model as before", res.Model)
	}
	if res.RequestURL() != "https://api.deepseek.com/v1/chat/completions" {
		t.Fatalf("RequestURL = %s", res.RequestURL())
	}
	if res.APIKey != "sk-deepseek" {
		t.Fatalf("APIKey = %q", res.APIKey)
	}
	// The OpenAI path never asks for an Anthropic model list.
	if n := *calls; n != 0 {
		t.Fatalf("model list asked %d times on the OpenAI path", n)
	}
}

func TestResolveRuleGuardModelWinsOverExtractionModel(t *testing.T) {
	registry := `{"glm":{"api":"https://api.z.ai/api/coding/paas/v4","npm":"@ai-sdk/openai-compatible","models":{"glm-5.2":{}}}}`
	home, dataDir := fixture(t, "extraction:\n  model: deepseek-v4-flash\nrule_guard:\n  model: glm-5.2\n", registry, `{"glm":{"key":"sk-glm"}}`)

	res, err := Resolve(home, dataDir)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if res.Model != "glm-5.2" || res.Source != SourceConfig {
		t.Fatalf("Model = %s source = %s, want the rule_guard model", res.Model, res.Source)
	}
}

// An opencode installation that carries an Anthropic API key was the same 404 as the plugin: the provider
// entry says @ai-sdk/anthropic, so the request has to go to /v1/messages.
func TestResolveOpenCodeAnthropicProvider(t *testing.T) {
	registry := `{"anthropic":{"api":"https://api.anthropic.com","npm":"@ai-sdk/anthropic","models":{"claude-haiku-4-5-20251001":{}}}}`
	home, dataDir := fixture(t, "extraction:\n  model: claude-haiku-4-5-20251001\n", registry, `{"anthropic":{"key":"sk-ant-opencode"}}`)
	server, _ := modelList(t, haiku("claude-haiku-5-5", "2026-10-07T18:00:00Z"))
	t.Setenv("YESMEM_GUARD_MODELS_URL", server.URL)

	res, err := Resolve(home, dataDir)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if res.APIType != APITypeAnthropic {
		t.Fatalf("APIType = %s, want anthropic for npm %s", res.APIType, AnthropicNPM)
	}
	if res.RequestURL() != "https://api.anthropic.com/v1/messages" {
		t.Fatalf("RequestURL = %s", res.RequestURL())
	}
	if res.Host != "api.anthropic.com" {
		t.Fatalf("Host = %s", res.Host)
	}
}

func TestResolveClaudeOnlyInstallation(t *testing.T) {
	// No provider carries the guard's model, so the provider search ends without a key.
	home, dataDir := fixture(t, "extraction:\n  model: gd-flash\n", `{"anthropic":{"api":"https://api.anthropic.com","npm":"@ai-sdk/anthropic","models":{}}}`, `{"anthropic":{"key":"sk-ant-only"}}`)
	server, _ := modelList(t, haiku("claude-haiku-5-5", "2026-10-07T18:00:00Z"), haiku("claude-haiku-4-5-20251001", "2025-10-15T00:00:00Z"))
	t.Setenv("YESMEM_GUARD_MODELS_URL", server.URL)

	res, err := Resolve(home, dataDir)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if res.APIType != APITypeAnthropic || res.APIKey != "sk-ant-only" {
		t.Fatalf("APIType = %s key = %q, want the Anthropic credential", res.APIType, res.APIKey)
	}
	if res.Model != "claude-haiku-5-5" {
		t.Fatalf("Model = %s, want the account's newest haiku", res.Model)
	}
	if len(res.Fallbacks) != 1 || res.Fallbacks[0] != "claude-haiku-4-5-20251001" {
		t.Fatalf("Fallbacks = %v, want one step down", res.Fallbacks)
	}
	if res.Source != SourceAccount {
		t.Fatalf("Source = %s", res.Source)
	}
}

func TestResolveAnthropicKeyLadder(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, home string)
	}{
		{"env", func(t *testing.T, home string) { t.Setenv("ANTHROPIC_API_KEY", "sk-ant-env") }},
		{"claude config", func(t *testing.T, home string) {
			writeFileT(t, filepath.Join(home, ".claude", "config.json"), `{"primaryApiKey":"sk-ant-config"}`)
		}},
		{"claude json", func(t *testing.T, home string) {
			writeFileT(t, filepath.Join(home, ".claude.json"), `{"primaryApiKey":"sk-ant-dotjson"}`)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home, dataDir := fixture(t, "extraction:\n  model: gd-flash\n", "{}", "{}")
			tc.setup(t, home)
			server, _ := modelList(t, haiku("claude-haiku-5-5", "2026-10-07T18:00:00Z"))
			t.Setenv("YESMEM_GUARD_MODELS_URL", server.URL)

			res, err := Resolve(home, dataDir)
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if res.APIType != APITypeAnthropic || res.APIKey == "" {
				t.Fatalf("APIType = %s key = %q, want an Anthropic credential", res.APIType, res.APIKey)
			}
		})
	}
}

// A Claude subscription keeps its OAuth token in auth.json without a key field, and that token is not for
// API calls. An entry of that shape must not become the guard's credential.
func TestResolveIgnoresOAuthEntry(t *testing.T) {
	home, dataDir := fixture(t, "extraction:\n  model: gd-flash\n", "{}", `{"anthropic":{"type":"oauth","access":"token"}}`)

	res, err := Resolve(home, dataDir)
	if err == nil {
		t.Fatalf("Resolve returned %+v, want no credential for a subscription login", res)
	}
}

func TestResolveWithoutAnyCredential(t *testing.T) {
	home, dataDir := fixture(t, "extraction:\n  model: gd-flash\n", "{}", "{}")
	t.Setenv("ANTHROPIC_API_KEY", "")

	if res, err := Resolve(home, dataDir); err == nil {
		t.Fatalf("Resolve returned %+v, want an error", res)
	}
}

func TestResolveDefaultOpenAIModel(t *testing.T) {
	// No model configured and no Anthropic credential: the OpenAI default keeps the old behaviour.
	home, dataDir := fixture(t, "", deepseekRegistry, deepseekAuth)

	res, err := Resolve(home, dataDir)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if res.Model != DefaultOpenAIModel {
		t.Fatalf("Model = %s, want %s", res.Model, DefaultOpenAIModel)
	}
	if !strings.HasSuffix(res.RequestURL(), "/v1/chat/completions") {
		t.Fatalf("RequestURL = %s", res.RequestURL())
	}
}

// A gateway model is not in models.json: its endpoint lives in config.yaml's provider_targets and its
// credential in the opencode "gateway" entry. The plugin resolves both; the guard must reach the same endpoint.
func TestResolveProviderTarget(t *testing.T) {
	home, dataDir := fixture(t,
		"rule_guard:\n  model: privateTom\nproxy:\n  provider_targets:\n    privatetom: https://gateway.example.com/v1\n",
		"{}",
		`{"gateway":{"key":"sk-gateway"}}`)
	t.Setenv("YESMEM_GUARD_MODELS_URL", "http://127.0.0.1:1")

	res, err := Resolve(home, dataDir)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if res.Model != "privateTom" {
		t.Errorf("Model = %s, want the configured model", res.Model)
	}
	if res.BaseURL != "https://gateway.example.com/v1" {
		t.Errorf("BaseURL = %s, want the configured target (the key matched case-insensitively)", res.BaseURL)
	}
	if res.APIKey != "sk-gateway" {
		t.Errorf("APIKey = %q, want the gateway key", res.APIKey)
	}
	if res.NPM != "@ai-sdk/openai-compatible" || res.APIType != APITypeOpenAI {
		t.Errorf("APIType = %s npm = %s, want the OpenAI-compatible path", res.APIType, res.NPM)
	}
	// Only the suffix is pinned: the base URL may already carry a version segment, and the concatenation is the
	// same one the plugin does.
	if !strings.HasSuffix(res.RequestURL(), "/chat/completions") {
		t.Errorf("RequestURL = %s, want the completions endpoint", res.RequestURL())
	}
}

// The plugin's explicit override: endpoint, key and model win over everything the installation holds.
func TestResolveEnvOverride(t *testing.T) {
	home, dataDir := fixture(t, "extraction:\n  model: deepseek-v4-flash\n", deepseekRegistry, deepseekAuth)
	t.Setenv("YESMEM_GUARD_URL", "http://127.0.0.1:9999/")
	t.Setenv("YESMEM_GUARD_KEY", "sk-override")
	t.Setenv("YESMEM_GUARD_MODEL", "override-model")
	t.Setenv("YESMEM_GUARD_NPM", "@ai-sdk/openai-compatible")

	res, err := Resolve(home, dataDir)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if res.BaseURL != "http://127.0.0.1:9999" || res.APIKey != "sk-override" || res.Model != "override-model" {
		t.Fatalf("resolution = %+v, want the override endpoint, key and model", res)
	}
	if res.Source != SourceEnv {
		t.Errorf("Source = %s, want the environment", res.Source)
	}
}

// An override may name an Anthropic-shaped provider and no model. Then the model has to come from the
// account's list: the request goes to /v1/messages, where the OpenAI default model does not exist.
func TestResolveEnvOverrideWithoutModelAsksTheAccountForAHaiku(t *testing.T) {
	home, dataDir := fixture(t, "extraction:\n  model: deepseek-v4-flash\n", deepseekRegistry, deepseekAuth)
	server, _ := modelList(t, haiku("claude-haiku-5-5", "2026-10-07T18:00:00Z"))
	t.Setenv("YESMEM_GUARD_MODELS_URL", server.URL)
	t.Setenv("YESMEM_GUARD_URL", AnthropicBase)
	t.Setenv("YESMEM_GUARD_KEY", "sk-override")
	t.Setenv("YESMEM_GUARD_NPM", AnthropicNPM)

	res, err := Resolve(home, dataDir)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if res.APIType != APITypeAnthropic {
		t.Errorf("APIType = %s, want anthropic for the override's npm %s", res.APIType, AnthropicNPM)
	}
	if res.Model != "claude-haiku-5-5" {
		t.Errorf("Model = %s, want the account's newest haiku", res.Model)
	}
	if res.Source != SourceAccount {
		t.Errorf("Source = %s, want the account list", res.Source)
	}
	if res.RequestURL() != AnthropicBase+"/v1/messages" {
		t.Errorf("RequestURL = %s, want the messages endpoint", res.RequestURL())
	}
}

// The same override without a model on the OpenAI-compatible path keeps the built-in default: an empty
// model name would deactivate the guard on every installation whose endpoint is a gateway.
func TestResolveEnvOverrideWithoutModelKeepsTheOpenAIDefault(t *testing.T) {
	home, dataDir := fixture(t, "rule_guard:\n  model: glm-5.2\n", "{}", "{}")
	t.Setenv("YESMEM_GUARD_URL", "http://127.0.0.1:9999")

	res, err := Resolve(home, dataDir)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if res.Model != DefaultOpenAIModel {
		t.Errorf("Model = %s, want the built-in default %s", res.Model, DefaultOpenAIModel)
	}
	if res.Source != SourceFallback {
		t.Errorf("Source = %s, want the built-in fallback", res.Source)
	}
	if res.APIType != APITypeOpenAI {
		t.Errorf("APIType = %s, want the OpenAI-compatible path", res.APIType)
	}
}

// YESMEM_GUARD_MODEL alone, without an endpoint override, still wins: a harness that pins the model for
// one call must not get the configured one, and the source has to say where it came from.
func TestResolveEnvModelWinsWithoutAnEndpointOverride(t *testing.T) {
	registry := `{"glm":{"api":"https://api.z.ai/api/coding/paas/v4","npm":"@ai-sdk/openai-compatible","models":{"glm-5.2":{}}},` +
		`"deepseek":{"api":"https://api.deepseek.com","npm":"@ai-sdk/openai-compatible","models":{"deepseek-v4-flash":{}}}}`
	home, dataDir := fixture(t, "extraction:\n  model: deepseek-v4-flash\nrule_guard:\n  model: deepseek-v4-flash\n",
		registry, `{"glm":{"key":"sk-glm"},"deepseek":{"key":"sk-deepseek"}}`)
	t.Setenv("YESMEM_GUARD_MODEL", "glm-5.2")

	res, err := Resolve(home, dataDir)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if res.Model != "glm-5.2" {
		t.Errorf("Model = %s, want the model the environment names", res.Model)
	}
	if res.Source != SourceEnv {
		t.Errorf("Source = %s, want the environment", res.Source)
	}
	if res.APIKey != "sk-glm" {
		t.Errorf("APIKey = %q, want the key of the provider that hosts the env model", res.APIKey)
	}
}

// An Anthropic-shaped provider that hosts the configured model pins it: the guard must ask that model, not
// the account's newest haiku. The pin is the installation's statement, and only a provider that knows the
// name may state it (see the extraction fallback below).
func TestResolveAnthropicProviderHonoursTheConfiguredModel(t *testing.T) {
	registry := `{"anthropic":{"api":"https://api.anthropic.com","npm":"@ai-sdk/anthropic","models":{"claude-haiku-4-5-20251001":{}}}}`
	home, dataDir := fixture(t, "extraction:\n  model: gd-flash\nrule_guard:\n  model: claude-haiku-4-5-20251001\n",
		registry, `{"anthropic":{"key":"sk-ant-opencode"}}`)
	server, calls := modelList(t, haiku("claude-haiku-5-5", "2026-10-07T18:00:00Z"))
	t.Setenv("YESMEM_GUARD_MODELS_URL", server.URL)

	res, err := Resolve(home, dataDir)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if res.Model != "claude-haiku-4-5-20251001" {
		t.Errorf("Model = %s, want the configured model the provider hosts", res.Model)
	}
	if res.Source != SourceConfig {
		t.Errorf("Source = %s, want rule_guard.model", res.Source)
	}
	// The pin decides the request without asking the account at all.
	if n := *calls; n != 0 {
		t.Errorf("model list asked %d times, want none for a pinned model", n)
	}
}

// A rule_guard.model that no provider hosts must not hide the extraction model that one does. That is how
// an installation kept its guard before rule_guard.model existed at all.
func TestResolveFallsBackToTheExtractionModel(t *testing.T) {
	home, dataDir := fixture(t, "extraction:\n  model: deepseek-v4-flash\nrule_guard:\n  model: my-private-model\n",
		deepseekRegistry, deepseekAuth)

	res, err := Resolve(home, dataDir)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if res.Model != "deepseek-v4-flash" {
		t.Errorf("Model = %s, want the extraction model when no provider hosts the configured one", res.Model)
	}
	if res.Source != SourceExtraction {
		t.Errorf("Source = %s, want extraction.model", res.Source)
	}
}

// The target table is the endpoint of a gateway model that no provider lists. Against a provider entry
// that carries its own key the entry wins: the target is the second source of an endpoint, not an override.
func TestResolveProviderTargetLosesToAProviderWithAKey(t *testing.T) {
	home, dataDir := fixture(t,
		"rule_guard:\n  model: glm-5.2\nproxy:\n  provider_targets:\n    glm-5.2: https://gateway.example.com/v1\n",
		`{"zai":{"api":"https://api.z.ai/api/coding/paas/v4","npm":"@ai-sdk/openai-compatible","models":{"glm-5.2":{}}}}`,
		`{"zai":{"key":"sk-zai"},"gateway":{"key":"sk-gateway"}}`)

	res, err := Resolve(home, dataDir)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if res.BaseURL != "https://api.z.ai/api/coding/paas/v4" {
		t.Errorf("BaseURL = %s, want the registry endpoint of the provider that holds the key", res.BaseURL)
	}
	if res.APIKey != "sk-zai" {
		t.Errorf("APIKey = %q, want the provider's key, not the gateway's", res.APIKey)
	}
}

// A registry entry without a key does not own the endpoint: the target serves the model, matched
// case-insensitively, and the opencode gateway credential authenticates it.
func TestResolveProviderTargetServesAKeylessRegistryEntry(t *testing.T) {
	home, dataDir := fixture(t,
		"rule_guard:\n  model: glm-5.2\nproxy:\n  provider_targets:\n    GLM-5.2: https://gateway.example.com/v1/\n",
		`{"zai":{"api":"https://api.z.ai/api/coding/paas/v4","npm":"@ai-sdk/openai-compatible","models":{"glm-5.2":{}}}}`,
		`{"gateway":{"key":"sk-gateway"}}`)

	res, err := Resolve(home, dataDir)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if res.BaseURL != "https://gateway.example.com/v1" {
		t.Errorf("BaseURL = %s, want the configured target", res.BaseURL)
	}
	if res.APIKey != "sk-gateway" {
		t.Errorf("APIKey = %q, want the gateway key", res.APIKey)
	}
}

// A configured model that no provider hosts says nothing about this credential: it must not
// be sent to /v1/messages, where the name does not exist. The pin holds only for a provider
// that knows the model (see TestResolveAnthropicProviderHonoursTheConfiguredModel).
func TestResolveDoesNotSendAnUnhostedConfiguredModelToAnthropic(t *testing.T) {
	home, dataDir := fixture(t, "extraction:\n  model: gd-flash\nrule_guard:\n  model: my-private-model\n",
		"{}", `{"anthropic":{"key":"sk-ant-only"}}`)
	server, _ := modelList(t, haiku("claude-haiku-5-5", "2026-10-07T18:00:00Z"))
	t.Setenv("YESMEM_GUARD_MODELS_URL", server.URL)

	res, err := Resolve(home, dataDir)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if res.APIType != APITypeAnthropic {
		t.Errorf("APIType = %s, want the Anthropic path", res.APIType)
	}
	if res.Model != "claude-haiku-5-5" {
		t.Errorf("Model = %s, want the account's haiku; the unhosted configured name says nothing about this credential", res.Model)
	}
	if res.Source != SourceAccount {
		t.Errorf("Source = %s, want the account list", res.Source)
	}
}

// A provider entry that names the model but carries no key does not own the guard: the
// credential ladder continues, and the model must come from where the credential came from.
func TestResolveANamedProviderWithoutAKeyDoesNotOwnTheGuard(t *testing.T) {
	registry := `{"zai":{"api":"https://api.z.ai/api/coding/paas/v4","npm":"@ai-sdk/openai-compatible","models":{"glm-5.2":{}}}}`
	home, dataDir := fixture(t, "rule_guard:\n  model: glm-5.2\n", registry, `{"anthropic":{"key":"sk-ant-only"}}`)
	server, _ := modelList(t, haiku("claude-haiku-5-5", "2026-10-07T18:00:00Z"))
	t.Setenv("YESMEM_GUARD_MODELS_URL", server.URL)

	res, err := Resolve(home, dataDir)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if res.APIType != APITypeAnthropic {
		t.Errorf("APIType = %s, want the Anthropic path: the keyless entry cannot serve the guard", res.APIType)
	}
	if res.Model != "claude-haiku-5-5" {
		t.Errorf("Model = %s, want the account's haiku", res.Model)
	}
	if res.BaseURL != AnthropicBase {
		t.Errorf("BaseURL = %s, want %s", res.BaseURL, AnthropicBase)
	}
}
