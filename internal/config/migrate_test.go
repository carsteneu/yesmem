package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestMigrateConfig_AddsSkillEvalInject(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	os.WriteFile(path, []byte("proxy:\n  enabled: true\n  prompt_ungate: true\n"), 0644)

	n, err := MigrateConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Error("expected at least one field added")
	}

	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "skill_eval_inject") {
		t.Error("config should contain skill_eval_inject after migration")
	}
}

func TestMigrateConfig_AddsEffortFloor(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	os.WriteFile(path, []byte("proxy:\n  enabled: true\n"), 0644)

	MigrateConfig(path)

	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "effort_floor") {
		t.Error("config should contain effort_floor after migration")
	}
}

func TestMigrateConfig_SkipsExistingFields(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	// Config with all fields already present — MigrateConfig should add nothing
	os.WriteFile(path, []byte(`proxy:
  enabled: true
  skill_eval_inject: "true"
  effort_floor: "high"
  auto_configure_providers: true
  openai_target: "https://api.openai.com"
  reset_cache: false
  cache_keepalive_min_messages: 10
  model_features:
    claude:
      skill_eval: true
      briefing: true
      rules_reminder: true
      plan_checkpoint: true
      think_reminder: true
      think_reminder_min_chars: 0
      assoc_context: true
      timestamps: false
      loop_warning: true
  feature_defaults:
    skill_eval: true
    briefing: true
    rules_reminder: true
    plan_checkpoint: true
    think_reminder: true
    think_reminder_min_chars: 0
    assoc_context: false
    timestamps: true
    loop_warning: true

paths:
  opencode_db: /custom/opencode.db

agents:
  default_backend: claude

exclude_projects:
  - /home/testuser
  - /tmp

caps_dir: ""
default_sandbox_profile: ""
secrets_sanitization:
  enabled: false
http:
  enabled: false

forked_agents:
  max_forks_per_session: 50
  max_cost_per_session: 5

token_thresholds:
  deepseek: 600000
  glm-5.2: 500000

pricing:
  deepseek-v4-flash: {input: 0.14, output: 0.56}
`), 0644)

	n, err := MigrateConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("expected 0 fields added for fully migrated config, got %d", n)
	}
}

func TestMigrateConfig_AddsAutoConfigureProviders(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	os.WriteFile(path, []byte("proxy:\n  enabled: true\n"), 0644)

	n, err := MigrateConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Error("expected at least one field added")
	}

	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "auto_configure_providers: true") {
		t.Error("config should contain auto_configure_providers after migration")
	}
}

func TestMigrateConfig_PreservesExistingContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	original := "proxy:\n  enabled: true\n  prompt_ungate: true\n\nextraction:\n  model: sonnet\n"
	os.WriteFile(path, []byte(original), 0644)

	MigrateConfig(path)

	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "prompt_ungate: true") {
		t.Error("existing content should be preserved")
	}
	if !strings.Contains(string(data), "extraction:") {
		t.Error("other sections should be preserved")
	}
}

func TestMigrateConfig_NoProxySection(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	os.WriteFile(path, []byte("extraction:\n  model: sonnet\n"), 0644)

	n, err := MigrateConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	// model_features + opencode_db are added, proxy migrations are skipped
	if n == 0 {
		t.Errorf("expected at least 1 field added (model_features/opencode_db), got %d", n)
	}
}

func TestMigrateConfig_FileNotFound(t *testing.T) {
	_, err := MigrateConfig("/nonexistent/path/config.yaml")
	if err == nil {
		t.Error("expected error for nonexistent file")
	}
}

func TestMigrateConfig_InsertsInsideProxySection(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	os.WriteFile(path, []byte("proxy:\n  enabled: true\n\nextraction:\n  model: sonnet\n"), 0644)

	MigrateConfig(path)

	data, _ := os.ReadFile(path)
	content := string(data)
	proxyIdx := strings.Index(content, "proxy:")
	extractionIdx := strings.Index(content, "extraction:")
	skillEvalIdx := strings.Index(content, "skill_eval_inject")

	if skillEvalIdx < proxyIdx {
		t.Error("skill_eval_inject should appear after proxy:")
	}
	if skillEvalIdx > extractionIdx {
		t.Error("skill_eval_inject should appear before extraction: (inside proxy section)")
	}
}

func TestMigrateConfig_AddsOpencodeDB(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	os.WriteFile(path, []byte("extraction:\n  model: sonnet\n"), 0644)

	n, err := MigrateConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Error("expected at least one field added")
	}

	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "opencode_db") {
		t.Error("config should contain opencode_db after migration")
	}
}

func TestMigrateConfig_AddsModelFeatures(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	os.WriteFile(path, []byte("proxy:\n  enabled: true\n\nextraction:\n  model: sonnet\n"), 0644)

	MigrateConfig(path)

	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "model_features:") {
		t.Error("config should contain model_features after migration")
	}
	if !strings.Contains(string(data), "feature_defaults:") {
		t.Error("config should contain feature_defaults after migration")
	}
	// model_features should be inside proxy:
	proxyIdx := strings.Index(string(data), "proxy:")
	mfIdx := strings.Index(string(data), "model_features:")
	extractionIdx := strings.Index(string(data), "extraction:")
	if mfIdx < proxyIdx || mfIdx > extractionIdx {
		t.Error("model_features should be inside proxy: section")
	}
}

func TestMigrateConfig_IdempotentModelFeatures(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	// Config that already has all fields MigrateConfig checks for
	os.WriteFile(path, []byte(`proxy:
  enabled: true
  skill_eval_inject: "silent"
  effort_floor: ""
  auto_configure_providers: true
  openai_target: "https://api.openai.com"
  reset_cache: false
  cache_keepalive_min_messages: 10
  model_features:
    claude:
      skill_eval: true
      briefing: true
      rules_reminder: true
      plan_checkpoint: true
      think_reminder: true
      think_reminder_min_chars: 0
      assoc_context: true
      timestamps: false
      loop_warning: true
  feature_defaults:
    skill_eval: true
    briefing: true
    rules_reminder: true
    plan_checkpoint: true
    think_reminder: true
    think_reminder_min_chars: 0
    assoc_context: false
    timestamps: true
    loop_warning: true

paths:
  opencode_db: /custom/opencode.db

agents:
  default_backend: claude

exclude_projects:
  - /home/testuser
  - /tmp

caps_dir: ""
default_sandbox_profile: ""
secrets_sanitization:
  enabled: false
http:
  enabled: false

forked_agents:
  max_forks_per_session: 50
  max_cost_per_session: 5

token_thresholds:
  deepseek: 600000
  glm-5.2: 500000

pricing:
  deepseek-v4-flash: {input: 0.14, output: 0.56}
`), 0644)

	n, err := MigrateConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("expected 0 fields added for fully migrated config, got %d", n)
	}
}

func TestMigrateConfig_AddsAgentsDefaultBackend(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	os.WriteFile(path, []byte("agents:\n  terminal: kitty\n\nextraction:\n  model: sonnet\n"), 0644)

	if _, err := MigrateConfig(path); err != nil {
		t.Fatal(err)
	}

	data, _ := os.ReadFile(path)
	content := string(data)
	if !strings.Contains(content, "default_backend: claude") {
		t.Error("config should contain default_backend: claude after migration")
	}
	dbIdx := strings.Index(content, "default_backend:")
	extIdx := strings.Index(content, "extraction:")
	if dbIdx > extIdx {
		t.Error("default_backend should be inside the agents: section, not after extraction:")
	}

	if _, err := MigrateConfig(path); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(path)
	if c := strings.Count(string(data), "default_backend:"); c != 1 {
		t.Errorf("default_backend should appear exactly once after second run, got %d", c)
	}
}

func TestMigrateConfig_CreatesBackup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	original := "proxy:\n  enabled: true\nextraction:\n  model: sonnet\n"
	os.WriteFile(path, []byte(original), 0644)

	if _, err := MigrateConfig(path); err != nil {
		t.Fatal(err)
	}

	// Check that a timestamped backup was created in the same directory
	entries, _ := os.ReadDir(dir)
	var backups []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "config.yaml.bak.") {
			backups = append(backups, e.Name())
		}
	}
	if len(backups) == 0 {
		t.Error("expected backup file config.yaml.bak.<timestamp> after migration")
	} else if len(backups) > 1 {
		t.Errorf("expected 1 backup, got %d: %v", len(backups), backups)
	}

	// Verify backup content matches original
	if len(backups) > 0 {
		backupData, _ := os.ReadFile(filepath.Join(dir, backups[0]))
		content := strings.TrimSpace(string(backupData))
		if content != strings.TrimSpace(original) {
			t.Errorf("backup content mismatch:\n got:  %s\nwant: %s", content, original)
		}
	}
}

// TestMigrateConfig_PerKeyAddsMissingGates verifies that an existing model_features
// block with pre-loop-warning vintage (8 keys per entry, missing loop_warning)
// gets the missing gate key added to each model entry AND to feature_defaults.
func TestMigrateConfig_PerKeyAddsMissingGates(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	os.WriteFile(path, []byte(`proxy:
  enabled: true
  skill_eval_inject: "silent"
  effort_floor: ""
  auto_configure_providers: true
  openai_target: "https://api.openai.com"
  reset_cache: false
  cache_keepalive_min_messages: 10
  model_features:
    claude:
      skill_eval: true
      briefing: true
      rules_reminder: true
      plan_checkpoint: true
      think_reminder: true
      think_reminder_min_chars: 0
      assoc_context: true
      timestamps: false
    deepseek:
      skill_eval: true
      briefing: true
      rules_reminder: true
      plan_checkpoint: false
      think_reminder: true
      think_reminder_min_chars: 0
      assoc_context: true
      timestamps: true
  feature_defaults:
    skill_eval: true
    briefing: true
    rules_reminder: true
    plan_checkpoint: true
    think_reminder: true
    think_reminder_min_chars: 0
    assoc_context: false
    timestamps: true

paths:
  opencode_db: /custom/opencode.db

agents:
  default_backend: claude

exclude_projects:
  - /home/testuser
  - /tmp

caps_dir: ""
default_sandbox_profile: ""
secrets_sanitization:
  enabled: false
http:
  enabled: false

forked_agents:
  max_forks_per_session: 50
  max_cost_per_session: 5

token_thresholds:
  deepseek: 600000
  glm-5.2: 500000

pricing:
  deepseek-v4-flash: {input: 0.14, output: 0.56}
`), 0644)

	n, err := MigrateConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	// 3 keys added: loop_warning to claude + deepseek + feature_defaults.
	if n != 3 {
		t.Errorf("expected 3 fields added (loop_warning x3), got %d", n)
	}

	data, _ := os.ReadFile(path)
	content := string(data)
	if got := strings.Count(content, "loop_warning: true"); got != 3 {
		t.Errorf("expected 3 loop_warning:true occurrences (claude+deepseek+feature_defaults), got %d", got)
	}
}

// TestMigrateConfig_PerKeyLoopWarningTrue verifies that loop_warning specifically
// migrates to true (not false like the other gates). Rationale: loop detection
// ran ungated before the feature gate existed; defaulting to false would regress.
func TestMigrateConfig_PerKeyLoopWarningTrue(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	os.WriteFile(path, []byte(`proxy:
  enabled: true
  model_features:
    claude:
      skill_eval: true

feature_defaults:
  skill_eval: true
`), 0644)

	MigrateConfig(path)

	data, _ := os.ReadFile(path)
	content := string(data)
	if !strings.Contains(content, "loop_warning: true") {
		t.Errorf("expected loop_warning: true in migrated config, got:\n%s", content)
	}
	if strings.Contains(content, "loop_warning: false") {
		t.Errorf("loop_warning must NOT be false — would regress pre-gate loop detection")
	}
}

// TestMigrateConfig_PerKeyNoGlmInsert verifies that migration does NOT add a
// glm model entry to existing configs. glm only ships via the fresh template
// (modelFeaturesBlock); existing configs rely on feature_defaults fallback.
func TestMigrateConfig_PerKeyNoGlmInsert(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	os.WriteFile(path, []byte(`proxy:
  enabled: true
  model_features:
    claude:
      skill_eval: true
`), 0644)

	MigrateConfig(path)

	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), "glm:") {
		t.Errorf("migration must NOT insert glm block into existing configs; got:\n%s", string(data))
	}
}

// TestMigrateConfig_PerKeyIdempotent verifies that a second MigrateConfig run
// after per-key migration adds zero fields.
func TestMigrateConfig_PerKeyIdempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	os.WriteFile(path, []byte(`proxy:
  enabled: true
  model_features:
    claude:
      skill_eval: true

feature_defaults:
  skill_eval: true
`), 0644)

	if _, err := MigrateConfig(path); err != nil {
		t.Fatal(err)
	}

	n, err := MigrateConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("second MigrateConfig run should be idempotent (0 added), got %d", n)
	}
}

// TestMigrateConfig_PerKeyCustomModel verifies that a custom (non-template)
// model entry in a user config also receives per-key gate migration. The
// discovery logic walks any sub-section header under model_features, not a
// hardcoded model list.
func TestMigrateConfig_PerKeyCustomModel(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	os.WriteFile(path, []byte(`proxy:
  enabled: true
  model_features:
    qwen:
      skill_eval: true
`), 0644)

	MigrateConfig(path)

	data, _ := os.ReadFile(path)
	content := string(data)
	if !strings.Contains(content, "qwen:") {
		t.Errorf("qwen entry should still be present after migration")
	}
	// qwen should have received loop_warning: true (dynamic discovery, not hardcoded).
	qwenIdx := strings.Index(content, "qwen:")
	loopIdx := strings.Index(content, "loop_warning:")
	mfEnd := strings.Index(content, "\n  feature_defaults:")
	if mfEnd < 0 {
		mfEnd = len(content)
	}
	if loopIdx < 0 || loopIdx < qwenIdx || loopIdx > mfEnd {
		t.Errorf("expected loop_warning: true inside qwen section, content:\n%s", content)
	}
}

// TestMigrateConfig_PerKeyInlineCommentHeader verifies that section headers
// carrying an inline comment (e.g., "claude: # preset") are still migrated.
// Previously the exact-match header check silently skipped such entries.
func TestMigrateConfig_PerKeyInlineCommentHeader(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	os.WriteFile(path, []byte(`proxy:
  enabled: true
  model_features:
    claude: # anthropic preset
      skill_eval: true
`), 0644)

	n, _ := MigrateConfig(path)

	data, _ := os.ReadFile(path)
	content := string(data)
	if !strings.Contains(content, "loop_warning: true") {
		t.Errorf("claude with inline comment header should still receive loop_warning, content:\n%s", content)
	}
	if n == 0 {
		t.Errorf("expected migration to add keys to inline-comment-header section, got n=%d", n)
	}
}

// TestMigrateConfig_CreatesFeatureDefaultsWhenMissing verifies that an existing
// model_features block WITHOUT a sibling feature_defaults gets the full
// feature_defaults section inserted with documented template values (NOT the
// gateDefaults-false values). Rationale: previously a missing feature_defaults
// meant code-false fallback; a newly created visible section should carry the
// documented defaults so ungated features (e.g. loop_warning) don't regress.
func TestMigrateConfig_CreatesFeatureDefaultsWhenMissing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	os.WriteFile(path, []byte(`proxy:
  enabled: true
  model_features:
    claude:
      skill_eval: true
`), 0644)

	n, err := MigrateConfig(path)
	if err != nil {
		t.Fatal(err)
	}

	data, _ := os.ReadFile(path)
	content := string(data)

	// feature_defaults section must now exist.
	fdIdx := strings.Index(content, "feature_defaults:")
	if fdIdx < 0 {
		t.Fatalf("feature_defaults section not created; content:\n%s", content)
	}

	// Template values must be present (NOT gateDefaults-false values).
	// Critical: loop_warning and briefing must be true.
	wantTrue := []string{"briefing: true", "loop_warning: true", "plan_checkpoint: true",
		"rules_reminder: true", "skill_eval: true", "think_reminder: true", "timestamps: true"}
	fdEnd := strings.Index(content[fdIdx:], "\n\n")
	if fdEnd < 0 {
		fdEnd = len(content) - fdIdx
	}
	fdBlock := content[fdIdx : fdIdx+fdEnd]
	for _, want := range wantTrue {
		if !strings.Contains(fdBlock, want) {
			t.Errorf("feature_defaults block missing %q; got:\n%s", want, fdBlock)
		}
	}
	if !strings.Contains(fdBlock, "assoc_context: false") {
		t.Errorf("feature_defaults assoc_context should be false; got:\n%s", fdBlock)
	}
	if !strings.Contains(fdBlock, "think_reminder_min_chars: 0") {
		t.Errorf("feature_defaults think_reminder_min_chars should be 0; got:\n%s", fdBlock)
	}

	// At least one key added (the new section).
	if n == 0 {
		t.Errorf("expected fields added for new feature_defaults section, got %d", n)
	}

	// Idempotency: second run adds 0.
	n2, err := MigrateConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if n2 != 0 {
		data2, _ := os.ReadFile(path)
		t.Errorf("second MigrateConfig should be idempotent (0 added), got %d\ncontent after 2nd run:\n%s", n2, string(data2))
	}
}

// TestMigrateConfig_TopLevelHeaderWithComment verifies that a model_features
// header carrying an inline comment (e.g., "model_features: # gates") is
// recognized. Previously the exact-match check silently skipped it, leaving
// all model blocks unmigrated.
func TestMigrateConfig_TopLevelHeaderWithComment(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	os.WriteFile(path, []byte(`proxy:
  enabled: true
  model_features: # per-model gates
    claude:
      skill_eval: true
`), 0644)

	n, err := MigrateConfig(path)
	if err != nil {
		t.Fatal(err)
	}

	data, _ := os.ReadFile(path)
	content := string(data)
	if !strings.Contains(content, "loop_warning: true") {
		t.Errorf("model_features with inline-comment header should still trigger migration, content:\n%s", content)
	}
	if n == 0 {
		t.Errorf("expected migration to add keys under inline-comment top-level header, got n=%d", n)
	}
}

// TestMigrateConfig_ProducesValidYAML is a regression test for the update-flow
// config corruption: all injected snippets must keep the YAML structurally
// valid (correct indentation, no glued lines / no "10embedding:"-style merges).
func TestMigrateConfig_ProducesValidYAML(t *testing.T) {
	input := `proxy:
  enabled: true
  usage_deflation_factor: 0.7

embedding:
  provider: sse

extraction:
  model: sonnet

pricing:
  haiku: { input: 1.0, output: 5.0 }
`
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	os.WriteFile(path, []byte(input), 0644)

	if _, err := MigrateConfig(path); err != nil {
		t.Fatalf("MigrateConfig: %v", err)
	}

	data, _ := os.ReadFile(path)
	content := string(data)

	// Glued lines: two keys on one physical line must never happen.
	for _, glue := range []string{
		"true  openai_target:",     // timestamps: true + openai_target joined
		"10embedding:",             // cache_keepalive_min_messages glued to embedding:
		"true  auto_configure",     // any future glue of that shape
	} {
		if strings.Contains(content, glue) {
			t.Errorf("migrated config contains glued tokens %q — invalid YAML: %s", glue, content)
		}
	}

	// Full parse — must be structurally valid YAML with intended shape.
	var parsed map[string]any
	if err := yaml.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("migrated config is not valid YAML: %v\n---\n%s", err, content)
	}

	if parsed["embedding"] == nil {
		t.Error("embedding section lost during migration")
	}
	if parsed["pricing"] == nil {
		t.Error("pricing section lost during migration")
	}

	proxy := parsed["proxy"].(map[string]any)
	for _, wantKey := range []string{
		"skill_eval_inject", "auto_configure_providers",
		"model_features", "feature_defaults", "openai_target", "reset_cache",
		"cache_keepalive_min_messages",
	} {
		if _, ok := proxy[wantKey]; !ok {
			t.Errorf("proxy.%s missing after migration", wantKey)
		}
	}
	// effort_floor is intentionally an example (commented out), not an active key.
	if _, ok := proxy["effort_floor"]; ok {
		t.Error("effort_floor should stay commented (example), not be an active key")
	}

	fd := proxy["feature_defaults"].(map[string]any)
	if fd["timestamps"] != true {
		t.Errorf("feature_defaults.timestamps = %v, want true", fd["timestamps"])
	}
	if mf, ok := proxy["model_features"].(map[string]any); !ok {
		t.Error("model_features is not a mapping")
	} else if _, ok := mf["claude"]; !ok {
		t.Error("model_features.claude missing")
	}
}

// TestMigrateConfig_PricingDeepseekIndent guards the deepseek pricing snippet
// indentation: entries must sit as children of pricing (2-space), not nested
// one level too deep (4-space) which makes them a sub-block of gpt-5.4.
func TestMigrateConfig_PricingDeepseekIndent(t *testing.T) {
	input := "pricing:\n  haiku: { input: 1.0, output: 5.0 }\n"
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	os.WriteFile(path, []byte(input), 0644)

	MigrateConfig(path)

	data, _ := os.ReadFile(path)
	var parsed map[string]any
	if err := yaml.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("migrated config not valid YAML: %v\n---\n%s", err, data)
	}
	pricing := parsed["pricing"].(map[string]any)
	if _, ok := pricing["deepseek-v4-flash"]; !ok {
		t.Error("deepseek-v4-flash not a child of pricing (wrong indent)")
	}
}
