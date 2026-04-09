package proxy

import "testing"

func TestDetectGitCommit_StandardOutput(t *testing.T) {
	messages := []any{
		map[string]any{"role": "assistant", "content": "I'll commit the changes."},
		map[string]any{"role": "user", "content": []any{
			map[string]any{
				"type":        "tool_result",
				"tool_use_id": "test-123",
				"content":     "[main abc1234] Fix the login bug\n 2 files changed, 15 insertions(+), 3 deletions(-)",
			},
		}},
	}

	ci := detectGitCommit(messages)
	if ci == nil {
		t.Fatal("expected CommitInfo, got nil")
	}
	if ci.Hash != "abc1234" {
		t.Errorf("Hash = %q, want %q", ci.Hash, "abc1234")
	}
	if ci.Branch != "main" {
		t.Errorf("Branch = %q, want %q", ci.Branch, "main")
	}
	if ci.Message != "Fix the login bug" {
		t.Errorf("Message = %q, want %q", ci.Message, "Fix the login bug")
	}
}

func TestDetectGitCommit_BranchWithSlash(t *testing.T) {
	messages := []any{
		map[string]any{"role": "assistant", "content": "Committing now."},
		map[string]any{"role": "user", "content": []any{
			map[string]any{
				"type":        "tool_result",
				"tool_use_id": "test-456",
				"content":     "[feature/auth-fix def5678] Add token refresh\n 1 file changed, 5 insertions(+)",
			},
		}},
	}

	ci := detectGitCommit(messages)
	if ci == nil {
		t.Fatal("expected CommitInfo, got nil")
	}
	if ci.Hash != "def5678" {
		t.Errorf("Hash = %q, want %q", ci.Hash, "def5678")
	}
	if ci.Branch != "feature/auth-fix" {
		t.Errorf("Branch = %q, want %q", ci.Branch, "feature/auth-fix")
	}
	if ci.Message != "Add token refresh" {
		t.Errorf("Message = %q, want %q", ci.Message, "Add token refresh")
	}
}

func TestDetectGitCommit_NoCommit(t *testing.T) {
	messages := []any{
		map[string]any{"role": "assistant", "content": "Let me list files."},
		map[string]any{"role": "user", "content": []any{
			map[string]any{
				"type":        "tool_result",
				"tool_use_id": "test-789",
				"content":     "total 28\ndrwxr-xr-x 5 user staff 160 Mar 19 12:00 .",
			},
		}},
	}

	ci := detectGitCommit(messages)
	if ci != nil {
		t.Errorf("expected nil, got %+v", ci)
	}
}

func TestDetectGitCommit_GitStatusNotCommit(t *testing.T) {
	messages := []any{
		map[string]any{"role": "assistant", "content": "Checking status."},
		map[string]any{"role": "user", "content": []any{
			map[string]any{
				"type":        "tool_result",
				"tool_use_id": "test-status",
				"content":     "On branch main\nYour branch is up to date with 'origin/main'.\n\nnothing to commit, working tree clean",
			},
		}},
	}

	ci := detectGitCommit(messages)
	if ci != nil {
		t.Errorf("expected nil, got %+v", ci)
	}
}

func TestDetectGitCommit_AmendCommit(t *testing.T) {
	messages := []any{
		map[string]any{"role": "assistant", "content": "Amending the commit."},
		map[string]any{"role": "user", "content": []any{
			map[string]any{
				"type":        "tool_result",
				"tool_use_id": "test-amend",
				"content":     "[main 9a8b7c6] Updated commit message\n 1 file changed, 2 insertions(+)",
			},
		}},
	}

	ci := detectGitCommit(messages)
	if ci == nil {
		t.Fatal("expected CommitInfo, got nil")
	}
	if ci.Hash != "9a8b7c6" {
		t.Errorf("Hash = %q, want %q", ci.Hash, "9a8b7c6")
	}
	if ci.Branch != "main" {
		t.Errorf("Branch = %q, want %q", ci.Branch, "main")
	}
	if ci.Message != "Updated commit message" {
		t.Errorf("Message = %q, want %q", ci.Message, "Updated commit message")
	}
}

func TestDetectGitCommit_ShortHash(t *testing.T) {
	messages := []any{
		map[string]any{"role": "assistant", "content": "Committing."},
		map[string]any{"role": "user", "content": []any{
			map[string]any{
				"type":        "tool_result",
				"tool_use_id": "test-short",
				"content":     "[develop a1b2c3d] Short hash commit\n 3 files changed, 10 insertions(+), 1 deletion(-)",
			},
		}},
	}

	ci := detectGitCommit(messages)
	if ci == nil {
		t.Fatal("expected CommitInfo, got nil")
	}
	if ci.Hash != "a1b2c3d" {
		t.Errorf("Hash = %q, want %q", ci.Hash, "a1b2c3d")
	}
	if ci.Branch != "develop" {
		t.Errorf("Branch = %q, want %q", ci.Branch, "develop")
	}
	if ci.Message != "Short hash commit" {
		t.Errorf("Message = %q, want %q", ci.Message, "Short hash commit")
	}
}
