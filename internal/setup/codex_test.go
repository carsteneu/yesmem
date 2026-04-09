package setup

import (
	"strings"
	"testing"
)

func TestMergeCodexConfigContent_AddsYesmemIntegration(t *testing.T) {
	content := strings.TrimSpace(`
model = "gpt-5.4"

[projects."/home/testuser/projects/myapp"]
trust_level = "trusted"
`) + "\n"

	merged := mergeCodexConfigContent(content, "/home/testuser/.local/bin/yesmem", "/home/testuser/.codex/instructions/yesmem.md")

	if !strings.Contains(merged, `model_provider = "yesmem"`) {
		t.Fatalf("missing model_provider in:\n%s", merged)
	}
	if !strings.Contains(merged, `developer_instructions_file = "/home/testuser/.codex/instructions/yesmem.md"`) {
		t.Fatalf("missing developer_instructions_file in:\n%s", merged)
	}
	if !strings.Contains(merged, `[model_providers.yesmem]`) {
		t.Fatalf("missing provider section in:\n%s", merged)
	}
	if !strings.Contains(merged, `base_url = "http://127.0.0.1:9099/v1"`) {
		t.Fatalf("missing proxy base_url in:\n%s", merged)
	}
	if !strings.Contains(merged, `[mcp_servers.yesmem]`) {
		t.Fatalf("missing mcp section in:\n%s", merged)
	}
	if !strings.Contains(merged, `command = "/home/testuser/.local/bin/yesmem"`) {
		t.Fatalf("missing yesmem command in:\n%s", merged)
	}
	if !strings.Contains(merged, `[projects."/home/testuser/projects/myapp"]`) {
		t.Fatalf("unrelated config was lost:\n%s", merged)
	}
}

func TestMergeCodexConfigContent_ReplacesExistingYesmemBlocks(t *testing.T) {
	content := strings.TrimSpace(`
model_provider = "openai"
developer_instructions_file = "/tmp/old.md"

[model_providers.yesmem]
name = "Old"
base_url = "http://127.0.0.1:9100/v1"
env_key = "OLD_KEY"

[mcp_servers.yesmem]
command = "/tmp/old-yesmem"
args = ["old"]
`) + "\n"

	merged := mergeCodexConfigContent(content, "/home/testuser/.local/bin/yesmem", "/home/testuser/.codex/instructions/yesmem.md")

	if strings.Count(merged, `[model_providers.yesmem]`) != 1 {
		t.Fatalf("provider section duplicated:\n%s", merged)
	}
	if strings.Count(merged, `[mcp_servers.yesmem]`) != 1 {
		t.Fatalf("mcp section duplicated:\n%s", merged)
	}
	if !strings.Contains(merged, `model_provider = "yesmem"`) {
		t.Fatalf("model_provider not updated:\n%s", merged)
	}
	if !strings.Contains(merged, `env_key = "OPENAI_API_KEY"`) {
		t.Fatalf("provider section not refreshed:\n%s", merged)
	}
	if !strings.Contains(merged, `args = ["mcp"]`) {
		t.Fatalf("mcp args not refreshed:\n%s", merged)
	}
}

func TestRemoveCodexConfigContent_PreservesUnrelatedSettings(t *testing.T) {
	content := strings.TrimSpace(`
model_provider = "yesmem"
developer_instructions_file = "/home/testuser/.codex/instructions/yesmem.md"
model = "gpt-5.4"

[model_providers.yesmem]
name = "OpenAI via YesMem Proxy"
base_url = "http://127.0.0.1:9099/v1"
env_key = "OPENAI_API_KEY"

[mcp_servers.yesmem]
command = "/home/testuser/.local/bin/yesmem"
args = ["mcp"]

[projects."/home/testuser/projects"]
trust_level = "trusted"
`) + "\n"

	cleaned := removeCodexConfigContent(content, "/home/testuser/.codex/instructions/yesmem.md")

	if strings.Contains(cleaned, `model_provider = "yesmem"`) {
		t.Fatalf("model_provider was not removed:\n%s", cleaned)
	}
	if strings.Contains(cleaned, `developer_instructions_file = "/home/testuser/.codex/instructions/yesmem.md"`) {
		t.Fatalf("instructions line was not removed:\n%s", cleaned)
	}
	if strings.Contains(cleaned, `[model_providers.yesmem]`) {
		t.Fatalf("provider section was not removed:\n%s", cleaned)
	}
	if strings.Contains(cleaned, `[mcp_servers.yesmem]`) {
		t.Fatalf("mcp section was not removed:\n%s", cleaned)
	}
	if !strings.Contains(cleaned, `model = "gpt-5.4"`) {
		t.Fatalf("unrelated top-level setting was removed:\n%s", cleaned)
	}
	if !strings.Contains(cleaned, `[projects."/home/testuser/projects"]`) {
		t.Fatalf("unrelated project section was removed:\n%s", cleaned)
	}
}

func TestInspectCodexConfigContent_DetectsConfiguredState(t *testing.T) {
	content := mergeCodexConfigContent("", "/home/testuser/.local/bin/yesmem", "/home/testuser/.codex/instructions/yesmem.md")
	state := inspectCodexConfigContent(content, "/home/testuser/.codex/instructions/yesmem.md")

	if !state.ConfigPresent {
		t.Fatal("expected config to be present")
	}
	if !state.ProviderConfigured {
		t.Fatal("expected provider to be configured")
	}
	if !state.MCPConfigured {
		t.Fatal("expected mcp to be configured")
	}
	if !state.InstructionsReferenced {
		t.Fatal("expected instructions to be referenced")
	}
}
