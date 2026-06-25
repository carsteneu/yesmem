package setup

import (
	"os"
	"testing"
)

func TestTemplateDump_OpenCodePath(t *testing.T) {
	cfgStr := generateConfig("deepseek-v4-flash", true, "", "sk-ds-key", "https://api.deepseek.com/v1",
		"openai_compatible", "gnome-terminal",
		"deepseek-v4-pro", "deepseek-v4-pro", "deepseek-v4-flash")

	outPath := "/tmp/generated_opencode_config.yaml"
	if err := os.WriteFile(outPath, []byte(cfgStr), 0644); err != nil {
		t.Fatal(err)
	}
	t.Logf("Config written to %s (%d bytes)", outPath, len(cfgStr))
}
