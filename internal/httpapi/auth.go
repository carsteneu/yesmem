package httpapi

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
)

// EnsureAuthToken reads or generates a bearer token.
// Token is stored at the given path with 0600 permissions.
func EnsureAuthToken(tokenPath string) (string, error) {
	// Try reading existing token
	data, err := os.ReadFile(tokenPath)
	if err == nil {
		token := strings.TrimSpace(string(data))
		if len(token) >= 32 {
			return token, nil
		}
	}

	// Generate new token
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	token := hex.EncodeToString(buf)

	// Ensure directory exists
	os.MkdirAll(filepath.Dir(tokenPath), 0700)

	// Write with restricted permissions
	if err := os.WriteFile(tokenPath, []byte(token+"\n"), 0600); err != nil {
		return "", err
	}

	return token, nil
}
