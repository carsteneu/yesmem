package httpapi

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureAuthToken(t *testing.T) {
	dir := t.TempDir()
	tokenPath := filepath.Join(dir, "auth_token")

	// First call generates token
	token1, err := EnsureAuthToken(tokenPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(token1) < 32 {
		t.Errorf("token too short: %d chars", len(token1))
	}

	// Second call returns same token
	token2, err := EnsureAuthToken(tokenPath)
	if err != nil {
		t.Fatal(err)
	}
	if token1 != token2 {
		t.Error("token changed on second call")
	}

	// File permissions should be 0600
	info, _ := os.Stat(tokenPath)
	if info.Mode().Perm() != 0600 {
		t.Errorf("token file perms = %o, want 0600", info.Mode().Perm())
	}
}
