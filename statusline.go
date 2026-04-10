package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/user"
	"strings"
	"time"

	"github.com/carsteneu/yesmem/internal/proxy"
)

// statuslineInput is the JSON structure Claude Code sends to the statusline command.
type statuslineInput struct {
	SessionID string `json:"session_id"`
	Model     struct {
		ID          string `json:"id"`
		DisplayName string `json:"display_name"`
	} `json:"model"`
	Workspace struct {
		CurrentDir string `json:"current_dir"`
	} `json:"workspace"`
	CWD           string `json:"cwd"`
	ContextWindow struct {
		CurrentUsage struct {
			CacheRead  int `json:"cache_read_input_tokens"`
			CacheWrite int `json:"cache_creation_input_tokens"`
			Uncached   int `json:"input_tokens"`
		} `json:"current_usage"`
	} `json:"context_window"`
}

// ANSI color codes
const (
	cReset   = "\033[00m"
	cBoldGrn = "\033[01;32m"
	cBoldBlu = "\033[01;34m"
	cBoldYel = "\033[01;33m"
	cBoldRed = "\033[01;31m"
	cLightRd = "\033[91m"
	cGray    = "\033[37m"
	cCyan    = "\033[36m"
)

// Per-MTok pricing
type modelPricing struct {
	ReadRate    float64 // cache read $/MTok
	WriteRate   float64 // cache write $/MTok
	UncachedRate float64 // uncached input $/MTok
}

var pricingOpus = modelPricing{ReadRate: 0.50, WriteRate: 6.25, UncachedRate: 5.0}
var pricingSonnet = modelPricing{ReadRate: 0.30, WriteRate: 3.75, UncachedRate: 3.0}

func pricingForModel(modelID string) modelPricing {
	m := strings.ToLower(modelID)
	if strings.Contains(m, "sonnet") {
		return pricingSonnet
	}
	return pricingOpus // default
}

func runStatusline() {
	var input statuslineInput
	if err := json.NewDecoder(os.Stdin).Decode(&input); err != nil {
		// Fallback: minimal prompt
		fmt.Print(minimalPrompt())
		return
	}

	cwd := input.Workspace.CurrentDir
	if cwd == "" {
		cwd = input.CWD
	}
	if cwd == "" {
		cwd, _ = os.Getwd()
	}

	hostname, _ := os.Hostname()
	if idx := strings.Index(hostname, "."); idx > 0 {
		hostname = hostname[:idx]
	}
	username := "user"
	if u, err := user.Current(); err == nil {
		username = u.Username
	}

	pricing := pricingForModel(input.Model.ID)
	usage := input.ContextWindow.CurrentUsage
	totalTok := usage.CacheRead + usage.CacheWrite + usage.Uncached

	var sb strings.Builder

	// Line 1: empty (spacer), Line 2: prompt
	sb.WriteString(fmt.Sprintf("\n%s%s@%s%s:%s%s%s", cBoldGrn, username, hostname, cReset, cBoldBlu, cwd, cReset))

	if totalTok > 0 {
		tokK := totalTok / 1000
		coldCost := float64(totalTok) * pricing.WriteRate / 1_000_000

		// Read cache status from proxy status.json
		dataDir := yesmemDataDir()
		statusPath := proxy.CacheStatusPath(dataDir, input.SessionID)
		ttlStr, expiryCost := formatTTL(statusPath, tokK, coldCost)
		sb.WriteString("\n")
		sb.WriteString(ttlStr)

		// R/W/U breakdown + cost
		readK := usage.CacheRead / 1000
		writeK := usage.CacheWrite / 1000
		uncachedK := usage.Uncached / 1000
		hitPct := 0
		if totalTok > 0 {
			hitPct = usage.CacheRead * 100 / totalTok
		}

		reqCost := float64(usage.CacheRead)*pricing.ReadRate/1_000_000 +
			float64(usage.CacheWrite)*pricing.WriteRate/1_000_000 +
			float64(usage.Uncached)*pricing.UncachedRate/1_000_000

		costColor := cBoldGrn
		if hitPct <= 50 {
			costColor = cBoldRed
		}

		sb.WriteString(fmt.Sprintf("\n %sR: %dk  W: %dk  U: %dk  (%d%% hit)  last input request: %s$%.2f%s",
			cGray, readK, writeK, uncachedK, hitPct, costColor, reqCost, cReset))

		if expiryCost != "" {
			sb.WriteString(fmt.Sprintf("\n %s%s%s", cLightRd, expiryCost, cReset))
		}

	}

	if input.SessionID != "" {
		sb.WriteString(fmt.Sprintf("\n%sSessionId: %s%s", cCyan, input.SessionID, cReset))
	}

	output := sb.String()
	fmt.Print(output)
}

func formatTTL(statusPath string, tokK int, coldCost float64) (string, string) {
	data, err := os.ReadFile(statusPath)
	if err != nil {
		return fmt.Sprintf(" %s| %dk Token | %safter expiry $%.2f for refresh%s",
			cGray, tokK, cLightRd, coldCost, cReset), ""
	}

	var status proxy.CacheStatus
	if err := json.Unmarshal(data, &status); err != nil {
		return "", ""
	}

	now := time.Now().Unix()
	remaining := int64(status.TTLSeconds) - (now - status.LastRequestTS)

	lines := proxy.FormatStatusLines(status)

	var sb strings.Builder

	if remaining > 0 {
		color := cBoldGrn
		if remaining < 60 {
			color = cBoldYel
		}
		sb.WriteString(fmt.Sprintf(" %s%s%s", color, lines.CacheLine, cReset))
	} else {
		sb.WriteString(fmt.Sprintf(" %s%s%s", cBoldRed, lines.CacheLine, cReset))
	}

	if lines.CollapsingLine != "" {
		sb.WriteString(fmt.Sprintf("\n %s%s%s", cGray, lines.CollapsingLine, cReset))
	}

	return sb.String(), lines.ExpiryCostLine
}

func minimalPrompt() string {
	hostname, _ := os.Hostname()
	if idx := strings.Index(hostname, "."); idx > 0 {
		hostname = hostname[:idx]
	}
	username := "user"
	if u, err := user.Current(); err == nil {
		username = u.Username
	}
	cwd, _ := os.Getwd()
	return fmt.Sprintf("%s%s@%s%s:%s%s%s", cBoldGrn, username, hostname, cReset, cBoldBlu, cwd, cReset)
}
