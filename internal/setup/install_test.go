package setup

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultExtractionModel_IsSonnet(t *testing.T) {
	if DefaultExtractionModel != "sonnet" {
		t.Errorf("DefaultExtractionModel = %q, want %q", DefaultExtractionModel, "sonnet")
	}
}

func TestDetectUserTypeDefault_EnvApiKey(t *testing.T) {
	home := t.TempDir()
	got := detectUserTypeDefault(home, "sk-ant-test123")
	if got != "api" {
		t.Errorf("detectUserTypeDefault(env set) = %q, want %q", got, "api")
	}
}

func TestDetectUserTypeDefault_OauthAccount(t *testing.T) {
	home := t.TempDir()
	claudeJSON := `{"oauthAccount":{"emailAddress":"u@example.com"}}`
	if err := os.WriteFile(filepath.Join(home, ".claude.json"), []byte(claudeJSON), 0644); err != nil {
		t.Fatal(err)
	}
	got := detectUserTypeDefault(home, "")
	if got != "cli" {
		t.Errorf("detectUserTypeDefault(oauth) = %q, want %q", got, "cli")
	}
}

func TestDetectUserTypeDefault_EmptyHome(t *testing.T) {
	home := t.TempDir()
	got := detectUserTypeDefault(home, "")
	if got != "cli" {
		t.Errorf("detectUserTypeDefault(empty home) = %q, want %q", got, "cli")
	}
}

func TestDetectUserTypeDefault_EnvKeyWinsOverOauth(t *testing.T) {
	home := t.TempDir()
	claudeJSON := `{"oauthAccount":{"emailAddress":"u@example.com"}}`
	if err := os.WriteFile(filepath.Join(home, ".claude.json"), []byte(claudeJSON), 0644); err != nil {
		t.Fatal(err)
	}
	got := detectUserTypeDefault(home, "sk-ant-test123")
	if got != "api" {
		t.Errorf("detectUserTypeDefault(env+oauth) = %q, want %q (env key wins)", got, "api")
	}
}

func TestPromptUserType_DefaultOnEnter(t *testing.T) {
	orig := reader
	defer func() { reader = orig }()
	reader = bufio.NewReader(strings.NewReader("\n"))
	got := promptUserType("cli")
	if got != "cli" {
		t.Errorf("promptUserType(default=cli, input=<enter>) = %q, want %q", got, "cli")
	}
}

func TestPromptUserType_Choice2ReturnsApi(t *testing.T) {
	orig := reader
	defer func() { reader = orig }()
	reader = bufio.NewReader(strings.NewReader("2\n"))
	got := promptUserType("cli")
	if got != "api" {
		t.Errorf("promptUserType(input=2) = %q, want %q", got, "api")
	}
}

func TestPromptUserType_Choice1ReturnsCli(t *testing.T) {
	orig := reader
	defer func() { reader = orig }()
	reader = bufio.NewReader(strings.NewReader("1\n"))
	got := promptUserType("api")
	if got != "cli" {
		t.Errorf("promptUserType(input=1) = %q, want %q", got, "cli")
	}
}

func TestPromptUserType_DefaultApi(t *testing.T) {
	orig := reader
	defer func() { reader = orig }()
	reader = bufio.NewReader(strings.NewReader("\n"))
	got := promptUserType("api")
	if got != "api" {
		t.Errorf("promptUserType(default=api, input=<enter>) = %q, want %q", got, "api")
	}
}

func TestPromptUserType_InvalidFallsBackToDefault(t *testing.T) {
	orig := reader
	defer func() { reader = orig }()
	reader = bufio.NewReader(strings.NewReader("xyz\n"))
	got := promptUserType("cli")
	if got != "cli" {
		t.Errorf("promptUserType(invalid) = %q, want %q (default)", got, "cli")
	}
}
