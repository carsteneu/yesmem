package proxy

import "testing"

// --- extractProjectPath ---
//
// Daemon RPCs must carry the absolute project path. A bare basename is
// ambiguous once two indexed projects share it (e.g. /home/x/memory/yesmem
// vs /home/x/projects/yesmem) and the daemon refuses to resolve it.

func TestExtractProjectPath_ClaudeCode(t *testing.T) {
	req := map[string]any{
		"system": []any{
			map[string]any{
				"type": "text",
				"text": "Primary working directory: /home/testuser/projects/myapp\n",
			},
		},
	}
	got := extractProjectPath(req)
	if got != "/home/testuser/projects/myapp" {
		t.Errorf("extractProjectPath = %q, want /home/testuser/projects/myapp", got)
	}
}

func TestExtractProjectPath_Codex(t *testing.T) {
	req := map[string]any{
		"messages": []any{
			map[string]any{
				"role": "user",
				"content": []any{
					map[string]any{
						"type": "text",
						"text": "<environment_context>\n  <cwd>/home/testuser/projects/my-app</cwd>\n</environment_context>",
					},
				},
			},
		},
	}
	got := extractProjectPath(req)
	if got != "/home/testuser/projects/my-app" {
		t.Errorf("extractProjectPath = %q, want /home/testuser/projects/my-app", got)
	}
}

func TestExtractProjectPath_Empty(t *testing.T) {
	got := extractProjectPath(map[string]any{})
	if got != "" {
		t.Errorf("extractProjectPath = %q, want empty", got)
	}
}

// extractProjectName keeps returning the short name: prompt_rewrite builds the
// wiki hint (~/.claude/yesmem/wiki/<project>/) from it, and proxy_stub keeps a
// deliberate short form. Guarding the contract so the two stay separate.
func TestExtractProjectName_StaysShortAlongsidePath(t *testing.T) {
	req := map[string]any{
		"system": []any{
			map[string]any{
				"type": "text",
				"text": "Primary working directory: /home/testuser/projects/myapp\n",
			},
		},
	}
	if got := extractProjectName(req); got != "myapp" {
		t.Errorf("extractProjectName = %q, want myapp", got)
	}
	if got := extractProjectPath(req); got != "/home/testuser/projects/myapp" {
		t.Errorf("extractProjectPath = %q, want absolute path", got)
	}
}

// --- relativeProjectParam ---
//
// Regression guard: any daemon RPC that still ships a bare basename is a
// latent ambiguity failure. The guard makes a missed call site loud instead
// of letting it fail silently at resolution time.

func TestRelativeProjectParam_AbsoluteIsFine(t *testing.T) {
	params := map[string]any{"project": "/home/testuser/projects/myapp"}
	if got, bad := relativeProjectParam(params); bad {
		t.Errorf("relativeProjectParam(%q) = bad, want ok", got)
	}
}

func TestRelativeProjectParam_ShortNameIsFlagged(t *testing.T) {
	params := map[string]any{"project": "myapp"}
	got, bad := relativeProjectParam(params)
	if !bad {
		t.Fatal("relativeProjectParam should flag a bare basename")
	}
	if got != "myapp" {
		t.Errorf("relativeProjectParam returned %q, want myapp", got)
	}
}

func TestRelativeProjectParam_MissingOrEmptyIsFine(t *testing.T) {
	if _, bad := relativeProjectParam(map[string]any{}); bad {
		t.Error("missing project param should not be flagged")
	}
	if _, bad := relativeProjectParam(map[string]any{"project": ""}); bad {
		t.Error("empty project param should not be flagged")
	}
	if _, bad := relativeProjectParam(map[string]any{"project": 42}); bad {
		t.Error("non-string project param should not be flagged")
	}
}

// increment_turn uses "__global__" when no project is known. It is a sentinel
// resolved by name on purpose, so the guard must stay quiet about it —
// otherwise every project-less turn logs a false warning.
func TestRelativeProjectParam_SentinelIsNotFlagged(t *testing.T) {
	if _, bad := relativeProjectParam(map[string]any{"project": "__global__"}); bad {
		t.Error("__global__ sentinel should not be flagged")
	}
}

// --- daemonProject ---

func TestDaemonProject_PrefersAbsolutePath(t *testing.T) {
	if got := daemonProject("myapp", "/home/testuser/projects/myapp"); got != "/home/testuser/projects/myapp" {
		t.Errorf("daemonProject = %q, want the absolute path", got)
	}
}

// Requests without a working directory keep the previous behaviour — they were
// never resolvable by path, so the short name is the only thing available.
func TestDaemonProject_FallsBackToShortName(t *testing.T) {
	if got := daemonProject("myapp", ""); got != "myapp" {
		t.Errorf("daemonProject = %q, want myapp", got)
	}
	if got := daemonProject("", ""); got != "" {
		t.Errorf("daemonProject = %q, want empty", got)
	}
}
