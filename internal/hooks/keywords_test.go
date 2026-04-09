package hooks

import (
	"testing"
)

func TestExtractKeywords(t *testing.T) {
	tests := []struct {
		cmd     string
		wantMin int
		wantHas []string
	}{
		{"cp foo ~/.local/bin/bar", 2, []string{"foo", ".local", "bin", "bar"}},
		{"git push --force origin main", 2, []string{"git", "push", "origin", "main"}},
		{"sudo systemctl restart nginx", 1, []string{"systemctl", "restart", "nginx"}},
		{"echo hello", 0, nil},
		{"ls -la", 0, nil},
		{"sqlite3 ~/.claude/yesmem/yesmem.db", 2, []string{"sqlite3", "yesmem"}},
		{"go build -o ~/.local/bin/yesmem .", 2, []string{"build", ".local", "yesmem"}},
	}

	for _, tt := range tests {
		kw := extractKeywords(tt.cmd)
		if len(kw) < tt.wantMin {
			t.Errorf("extractKeywords(%q) = %v (%d), want at least %d keywords", tt.cmd, kw, len(kw), tt.wantMin)
		}
		for _, want := range tt.wantHas {
			found := false
			for _, k := range kw {
				if k == want {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("extractKeywords(%q) = %v, missing %q", tt.cmd, kw, want)
			}
		}
	}
}

func TestMatchScore(t *testing.T) {
	keywords := []string{"sandbox", "local", "bin"}
	content := "When copying to ~/.local/bin/ sandbox must be disabled"

	score := matchScore(keywords, content)
	if score != 3 {
		t.Errorf("matchScore got %d, want 3", score)
	}
}

func TestMatchScoreNoMatch(t *testing.T) {
	keywords := []string{"docker", "compose"}
	content := "Deploy nach ~/.local/bin/ IMMER mit dangerouslyDisableSandbox"

	score := matchScore(keywords, content)
	if score != 0 {
		t.Errorf("matchScore got %d, want 0", score)
	}
}

func TestHasLongKeywordMatch(t *testing.T) {
	tests := []struct {
		keywords []string
		content  string
		want     bool
	}{
		{[]string{"systemctl", "nginx"}, "systemctl restart failed", true},
		{[]string{"cp", "bin"}, "copy to bin directory", false}, // both < 6 chars
		{[]string{"sqlite3"}, "sqlite3 CLI ist NICHT installiert", true},
	}

	for _, tt := range tests {
		got := hasLongKeywordMatch(tt.keywords, tt.content)
		if got != tt.want {
			t.Errorf("hasLongKeywordMatch(%v, %q) = %v, want %v", tt.keywords, tt.content, got, tt.want)
		}
	}
}

func TestExtractKeywordsFiltersGoTestNoise(t *testing.T) {
	// "go test ./internal/ivf/ -run TestIsStale -count=1 -v -timeout 30s"
	// should NOT produce "test" or "internal" — these are too generic
	kw := extractKeywords("go test ./internal/ivf/ -run TestIsStale -count=1 -v -timeout 30s")

	for _, noise := range []string{"test", "internal", "count", "timeout", "run"} {
		for _, k := range kw {
			if k == noise {
				t.Errorf("extractKeywords(go test ...) should NOT include %q — too generic, causes cross-matching between unrelated go test commands", noise)
			}
		}
	}

	// But specific tokens like "ivf" and "testisstale" should still be present
	wantHas := []string{"ivf", "testisstale"}
	for _, want := range wantHas {
		found := false
		for _, k := range kw {
			if k == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("extractKeywords(go test ...) = %v, missing specific keyword %q", kw, want)
		}
	}
}

func TestExtractPathKeywords(t *testing.T) {
	tests := []struct {
		path    string
		wantMin int
		wantHas []string
	}{
		{"/home/user/memory/yesmem/main.go", 3, []string{"memory", "yesmem", "main.go", "main"}},
		{"/home/user/memory/yesmem/internal/hooks/check.go", 3, []string{"memory", "yesmem", "hooks", "check.go", "check"}},
		{"/var/www/html/erecht/analyser/src/Service/MetricsService.php", 3, []string{"erecht", "analyser", "service", "metricsservice.php", "metricsservice"}},
		{"/home/user/.claude/settings.json", 2, []string{".claude", "settings.json", "settings"}},
		{"/tmp/foo", 0, nil}, // too short / noise
	}

	for _, tt := range tests {
		kw := extractPathKeywords(tt.path)
		if len(kw) < tt.wantMin {
			t.Errorf("extractPathKeywords(%q) = %v (%d), want at least %d", tt.path, kw, len(kw), tt.wantMin)
		}
		for _, want := range tt.wantHas {
			found := false
			for _, k := range kw {
				if k == want {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("extractPathKeywords(%q) = %v, missing %q", tt.path, kw, want)
			}
		}
	}
}
